//go:build linux

package flash

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func seekData(f *os.File, off int64) (int64, error) {
	n, err := unix.Seek(int(f.Fd()), off, unix.SEEK_DATA)
	if err != nil {
		if err == unix.ENXIO {
			return 0, io.EOF
		}
		return 0, err
	}
	return n, nil
}

func seekHole(f *os.File, off int64) (int64, error) {
	n, err := unix.Seek(int(f.Fd()), off, unix.SEEK_HOLE)
	if err != nil {
		return 0, err
	}
	return n, nil
}
