//go:build !linux

package flash

import (
	"context"
	"errors"
)

// ResizeRootThrough grows an ext filesystem through an open card handle.
// Only Linux has e2fsprogs and sparse SEEK_DATA/SEEK_HOLE for this path.
func ResizeRootThrough(context.Context, diskRW, uint64, uint64, uint64) error {
	return errors.New("filesystem resize runs on Linux, where e2fsprogs is installed")
}

// diskRW matches the Linux build's interface so call sites type-check everywhere.
type diskRW interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Sync() error
}
