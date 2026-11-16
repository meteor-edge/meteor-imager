//go:build !linux

package flash

import (
	"context"
	"errors"
)

// ResizeRoot grows an ext filesystem to its partition. Only Linux has the
// e2fsprogs tools and the partition-node naming this step uses.
func ResizeRoot(context.Context, string, int) error {
	return errors.New("filesystem resize runs on Linux, where e2fsprogs is installed")
}
