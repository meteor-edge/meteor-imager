//go:build linux

package flash

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

// diskRW is the open card (or image file) used to grow the filesystem without
// needing root or losetup: we already hold write access from the flash itself.
type diskRW interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
}

// ResizeRootThrough grows the ext filesystem of the extended root partition so
// it fills the card. A small image (for example a 1.5 GB backup) written to a
// larger card ends with a root filesystem that uses the whole card.
//
// It copies the existing filesystem into a temporary sparse file, runs
// resize2fs there (no root needed), and writes the grown image back through
// rw, skipping sparse holes. Prefer this while the flash Target is still open.
func ResizeRootThrough(ctx context.Context, rw diskRW, startSector, hadSectors, newSectors uint64) error {
	if hadSectors == 0 || newSectors <= hadSectors {
		return fmt.Errorf("root was not extended (had %d sectors, now %d)", hadSectors, newSectors)
	}
	e2fsck, err := lookE2fs("e2fsck")
	if err != nil {
		return err
	}
	resize, err := lookE2fs("resize2fs")
	if err != nil {
		return err
	}
	offset := int64(startSector) * diskimage.SectorSize
	hadBytes := int64(hadSectors) * diskimage.SectorSize
	newBytes := int64(newSectors) * diskimage.SectorSize

	dir, err := os.MkdirTemp("", "meteor-imager-grow-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "root.img")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := f.Truncate(newBytes); err != nil {
		f.Close()
		return err
	}
	if err := copyAt(ctx, rw, offset, hadBytes, f); err != nil {
		f.Close()
		return fmt.Errorf("copy root filesystem: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	out, code, err := runTool(ctx, e2fsck, "-fp", tmp)
	if err != nil {
		return err
	}
	if code > 2 {
		return fmt.Errorf("e2fsck exited %d: %s", code, lastLine(out))
	}
	out, code, err = runTool(ctx, resize, tmp)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("resize2fs exited %d: %s", code, lastLine(out))
	}
	if err := verifyExtSize(ctx, tmp, newBytes); err != nil {
		return err
	}

	src, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := writeSparseAt(ctx, src, newBytes, rw, offset); err != nil {
		return fmt.Errorf("write grown filesystem: %w", err)
	}
	return rw.Sync()
}

func copyAt(ctx context.Context, src io.ReaderAt, srcOff, n int64, dst *os.File) error {
	buf := make([]byte, 4<<20)
	var done int64
	for done < n {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := int64(len(buf))
		if chunk > n-done {
			chunk = n - done
		}
		read, err := src.ReadAt(buf[:chunk], srcOff+done)
		if read > 0 {
			if _, werr := dst.WriteAt(buf[:read], done); werr != nil {
				return werr
			}
			done += int64(read)
		}
		if err == io.EOF {
			break
		}
		if err != nil && read == 0 {
			return err
		}
	}
	if done < n {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// writeSparseAt copies src into dst at dstOff, skipping hole regions so the
// free space of a freshly grown ext4 is not rewritten onto the card.
func writeSparseAt(ctx context.Context, src *os.File, size int64, dst io.WriterAt, dstOff int64) error {
	buf := make([]byte, 4<<20)
	var pos int64
	for pos < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := seekData(src, pos)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		hole, err := seekHole(src, data)
		if err != nil && err != io.EOF {
			return err
		}
		if hole < 0 || hole > size {
			hole = size
		}
		for off := data; off < hole; {
			chunk := int64(len(buf))
			if chunk > hole-off {
				chunk = hole - off
			}
			read, rerr := src.ReadAt(buf[:chunk], off)
			if read > 0 {
				if _, werr := dst.WriteAt(buf[:read], dstOff+off); werr != nil {
					return werr
				}
				off += int64(read)
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil && read == 0 {
				return rerr
			}
		}
		if hole >= size {
			return nil
		}
		pos = hole
	}
	return nil
}
