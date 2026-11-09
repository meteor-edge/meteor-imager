package flash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// fileTarget is a regular image file.
type fileTarget struct{ *os.File }

func (f fileTarget) Sync() error { return f.File.Sync() }

// ConfigureFile writes a configured, uncompressed copy of srcPath to outPath,
// for flashing later with any tool. outPath must not exist yet.
func ConfigureFile(ctx context.Context, srcPath, outPath string, p *profile.FlashProfile, opt Options) (*Report, error) {
	if same, _ := sameFile(srcPath, outPath); same {
		return nil, errors.New("the output must be a new file, not the source image")
	}
	src, err := diskimage.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	f, err := os.OpenFile(outPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(outPath)
		}
	}()
	opt.SkipVerify = true // a local file; the hash is checked while reading the source
	rep, err := Write(ctx, src, fileTarget{f}, 0, p, opt)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", outPath, err)
	}
	ok = true
	return rep, nil
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		aa, _ := filepath.Abs(a)
		bb, _ := filepath.Abs(b)
		return aa == bb, nil
	}
	return os.SameFile(sa, sb), nil
}
