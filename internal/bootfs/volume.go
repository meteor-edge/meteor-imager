// Package bootfs edits files on a disk's FAT boot partition in userspace:
// no mount, no root, works the same on an image file and on a raw SD card.
package bootfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/soypat/fat"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

// Disk is a whole disk or disk image: an image file or an opened raw device.
type Disk interface {
	io.ReaderAt
	io.WriterAt
}

// ErrNoBootPartition means no partition holds a FAT filesystem.
var ErrNoBootPartition = errors.New("no FAT boot partition found; is this a Raspberry Pi OS image?")

// Volume is a mounted FAT boot partition.
type Volume struct {
	Partition diskimage.Partition
	Label     string
	fs        fat.FS
}

// OpenBoot finds the first partition holding a FAT filesystem and mounts it read-write.
func OpenBoot(d Disk) (*Volume, error) {
	parts, err := diskimage.ReadPartitions(d)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, p := range parts {
		if !p.MaybeFAT {
			continue
		}
		v := &Volume{Partition: p}
		dev := &partitionDevice{disk: d, offset: p.Offset(), size: p.Size()}
		if err := v.fs.Mount(dev, diskimage.SectorSize, fat.ModeRW); err != nil {
			lastErr = err
			continue
		}
		if label, err := v.fs.AppendLabel(nil); err == nil {
			v.Label = strings.TrimSpace(string(label))
		}
		return v, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoBootPartition, lastErr)
	}
	return nil, ErrNoBootPartition
}

// Close flushes and unmounts the filesystem.
func (v *Volume) Close() error { return v.fs.Unmount() }

// Exists reports whether a file is in the root directory.
func (v *Volume) Exists(name string) bool {
	var info fat.FileInfo
	return v.fs.Stat(name, &info) == nil
}

// ReadFile returns a root-directory file's contents.
func (v *Volume) ReadFile(name string) ([]byte, error) {
	var f fat.File
	if err := v.fs.OpenFile(&f, name, fat.ModeRead); err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, &f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFile creates or replaces a root-directory file.
func (v *Volume) WriteFile(name string, data []byte) error {
	var f fat.File
	if err := v.fs.OpenFile(&f, name, fat.ModeWrite|fat.ModeCreateAlways); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

// partitionDevice exposes one partition of a disk as a 512-byte block device.
type partitionDevice struct {
	disk   Disk
	offset int64
	size   int64
}

func (p *partitionDevice) bounds(n int, block int64) (int64, error) {
	off := block * diskimage.SectorSize
	if block < 0 || off+int64(n) > p.size {
		return 0, fmt.Errorf("access beyond the partition (block %d)", block)
	}
	return p.offset + off, nil
}

func (p *partitionDevice) ReadBlocks(dst []byte, block int64) (int, error) {
	off, err := p.bounds(len(dst), block)
	if err != nil {
		return 0, err
	}
	return p.disk.ReadAt(dst, off)
}

func (p *partitionDevice) WriteBlocks(src []byte, block int64) (int, error) {
	off, err := p.bounds(len(src), block)
	if err != nil {
		return 0, err
	}
	return p.disk.WriteAt(src, off)
}

func (p *partitionDevice) EraseBlocks(int64, int64) error { return nil }
