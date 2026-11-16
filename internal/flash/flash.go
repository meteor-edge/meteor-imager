// Package flash writes an image to a card, verifies it, then writes the
// FlashProfile to the card's boot partition.
//
// The first megabyte (partition table) is held back and written last, so until
// the very end the OS sees no partitions on the card and does not mount or lock
// them mid-write, and a card interrupted half way is never mistaken for a
// working one.
package flash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/meteor-edge/meteor-imager/internal/bootfs"
	"github.com/meteor-edge/meteor-imager/internal/diskimage"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// Target is an opened raw disk.
type Target interface {
	io.ReaderAt
	io.WriterAt
	// Sync flushes writes to the medium and drops cached data so reads come
	// from the card.
	Sync() error
	Close() error
}

// Stage is a step of a flash run.
type Stage string

const (
	StageWrite     Stage = "write"
	StageVerify    Stage = "verify"
	StageConfigure Stage = "configure"
	StageFinish    Stage = "finish"
)

// Progress reports progress within a stage: fraction from 0 to 1, and bytes
// written (write) or read back (verify) so far.
type Progress func(stage Stage, fraction float64, bytes int64)

// Options control a flash run.
type Options struct {
	// SkipVerify skips reading the card back (faster, not recommended).
	SkipVerify bool
	Progress   Progress
	// Inject tunes boot partition edits; tests pin the clock here.
	Inject bootfs.Options
	// SyncEvery flushes the target after this many bytes, so progress follows
	// what reached the card rather than the OS write cache (0: only at the end).
	SyncEvery int64
}

// DeviceSyncEvery is the flush interval for real cards.
const DeviceSyncEvery = 64 << 20

// Report is the outcome, with no secrets in it.
type Report struct {
	BytesWritten int64          `json:"bytes_written"`
	Verified     bool           `json:"verified"`
	Boot         *bootfs.Result `json:"boot,omitempty"`
	// RootPartition is the 1-based MBR slot extended to the card, or 0 when
	// the image was written as-is (unknown size, GPT, or no spare space).
	RootPartition int `json:"root_partition,omitempty"`
	// RootSectors is that partition's new length in 512-byte sectors.
	RootSectors uint64 `json:"root_sectors,omitempty"`
	// GrowNote is set when the partition was extended but the filesystem was not.
	GrowNote string `json:"grow_note,omitempty"`
}

const (
	headSize  = 1 << 20
	chunkSize = 4 << 20
)

// ErrTooSmall means the image does not fit on the card.
var ErrTooSmall = errors.New("the image is larger than the card")

// Write streams src to tgt, verifies it, and applies p (nil for a plain write).
// capacity is the card size in bytes, or 0 if unknown.
func Write(ctx context.Context, src *diskimage.Source, tgt Target, capacity int64, p *profile.FlashProfile, opt Options) (*Report, error) {
	progress := opt.Progress
	if progress == nil {
		progress = func(Stage, float64, int64) {}
	}
	if capacity > 0 && src.ImageSize > capacity {
		return nil, fmt.Errorf("%w (%s image, %s card)", ErrTooSmall, human(src.ImageSize), human(capacity))
	}

	// 1. Image body, hashed as written; the head stays in memory.
	head := make([]byte, 0, headSize)
	h := sha256.New()
	buf := make([]byte, chunkSize)
	var off, unsynced int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, rerr := io.ReadFull(src, buf)
		if n > 0 {
			chunk := buf[:n]
			if rerr != nil && n%diskimage.SectorSize != 0 {
				// Raw devices only take whole sectors: pad the image's tail.
				pad := diskimage.SectorSize - n%diskimage.SectorSize
				chunk = append(chunk, make([]byte, pad)...)
			}
			if capacity > 0 && off+int64(len(chunk)) > capacity {
				return nil, ErrTooSmall
			}
			h.Write(chunk)
			if len(head) < headSize {
				take := min(headSize-len(head), len(chunk))
				head = append(head, chunk[:take]...)
				chunk = chunk[take:]
				off += int64(take)
			}
			if len(chunk) > 0 {
				if _, err := tgt.WriteAt(chunk, off); err != nil {
					return nil, fmt.Errorf("write at byte %d: %w", off, err)
				}
				off += int64(len(chunk))
				unsynced += int64(len(chunk))
			}
			if opt.SyncEvery > 0 && unsynced >= opt.SyncEvery {
				if err := tgt.Sync(); err != nil {
					return nil, fmt.Errorf("flush: %w", err)
				}
				unsynced = 0
			}
			progress(StageWrite, writeFraction(src, off), off)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("read image: %w", rerr)
		}
	}
	total := off
	if total == 0 {
		return nil, errors.New("the image is empty")
	}
	if err := tgt.Sync(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}
	progress(StageWrite, 1, total)
	rep := &Report{BytesWritten: total}

	// 2. Read the body back and compare hashes.
	if !opt.SkipVerify {
		vh := sha256.New()
		vh.Write(head)
		rb := make([]byte, chunkSize)
		for pos := int64(len(head)); pos < total; {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n := int(min(int64(len(rb)), total-pos))
			if _, err := tgt.ReadAt(rb[:n], pos); err != nil {
				return nil, fmt.Errorf("read back at byte %d: %w", pos, err)
			}
			vh.Write(rb[:n])
			pos += int64(n)
			progress(StageVerify, float64(pos)/float64(total), pos)
		}
		if !bytes.Equal(vh.Sum(nil), h.Sum(nil)) {
			return nil, errors.New("verification failed: the card returned different data than was written; it may be faulty or counterfeit")
		}
		rep.Verified = true
	}

	// 3. Boot partition edits, through the held-back head.
	ov := &overlay{head: head, tgt: tgt, writes: map[int64][]byte{}}
	if p != nil {
		progress(StageConfigure, 0, 0)
		vol, err := bootfs.OpenBoot(ov)
		if err != nil {
			return nil, err
		}
		res, err := bootfs.Inject(vol, p, opt.Inject)
		if cerr := vol.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		rep.Boot = res
		progress(StageConfigure, 1, 0)
	}

	// Extend the last Linux partition to the card. The hash above used the
	// original table, so this only changes the sector count written last.
	if index, sectors, ok := diskimage.GrowLinuxRoot(head, capacity); ok {
		rep.RootPartition = index
		rep.RootSectors = uint64(sectors)
	}

	// 4. Partition table last, then confirm everything since step 2 landed.
	progress(StageFinish, 0, 0)
	if _, err := tgt.WriteAt(head, 0); err != nil {
		return nil, fmt.Errorf("write partition table: %w", err)
	}
	if err := tgt.Sync(); err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}
	ov.writes[0] = head
	if err := ov.checkWrites(); err != nil {
		return nil, err
	}
	progress(StageFinish, 1, 0)
	return rep, nil
}

// overlay serves the held-back head from memory and remembers what it wrote
// to the card, so those sectors can be read back at the end.
type overlay struct {
	head   []byte
	tgt    Target
	writes map[int64][]byte
}

func (o *overlay) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	if off < int64(len(o.head)) {
		n = copy(p, o.head[off:])
	}
	if n < len(p) {
		m, err := o.tgt.ReadAt(p[n:], off+int64(n))
		return n + m, err
	}
	return n, nil
}

func (o *overlay) WriteAt(p []byte, off int64) (int, error) {
	n := 0
	if off < int64(len(o.head)) {
		n = copy(o.head[off:], p)
	}
	if n < len(p) {
		m, err := o.tgt.WriteAt(p[n:], off+int64(n))
		if err != nil {
			return n + m, err
		}
		o.writes[off+int64(n)] = append([]byte(nil), p[n:]...)
	}
	return len(p), nil
}

func (o *overlay) checkWrites() error {
	offs := make([]int64, 0, len(o.writes))
	for off := range o.writes {
		offs = append(offs, off)
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	for _, off := range offs {
		want := o.writes[off]
		got := make([]byte, len(want))
		if _, err := o.tgt.ReadAt(got, off); err != nil {
			return fmt.Errorf("read back settings at byte %d: %w", off, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("verification failed at byte %d: the card did not keep the boot settings", off)
		}
	}
	return nil
}

// writeFraction uses the image size when known (raw, zip). For xz and gzip it
// falls back to how much of the compressed file was read, capped below 1:
// zeros at the end of an image compress to almost nothing, so the compressed
// file can be fully read while gigabytes are still to be written.
func writeFraction(src *diskimage.Source, written int64) float64 {
	if src.ImageSize > 0 {
		return min(float64(written)/float64(src.ImageSize), 1)
	}
	return min(src.Progress(), 0.99)
}

func human(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }
