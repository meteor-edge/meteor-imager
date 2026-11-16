//go:build linux

package flash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/meteor-edge/meteor-imager/internal/devices"
)

// ResizeRoot grows the ext filesystem in an MBR slot to the partition size.
// The disk must already be closed. A missing tool or a failed check is returned
// to the caller; the card itself has already been written.
func ResizeRoot(ctx context.Context, disk string, index int) error {
	if !strings.HasPrefix(disk, "/dev/") || strings.Contains(disk, "..") {
		return fmt.Errorf("not a device path: %s", disk)
	}
	if index < 1 || index > 4 {
		return fmt.Errorf("partition %d is not an MBR slot", index)
	}
	e2fsck, err := lookE2fs("e2fsck")
	if err != nil {
		return err
	}
	resize, err := lookE2fs("resize2fs")
	if err != nil {
		return err
	}
	if err := unmountAll(ctx, devices.Device{Path: disk}); err != nil {
		return err
	}
	if err := rereadPartitions(ctx, disk); err != nil {
		return err
	}
	// The desktop may mount the fresh boot partition the moment the table is reread.
	if err := unmountAll(ctx, devices.Device{Path: disk}); err != nil {
		return err
	}
	part := partitionNode(disk, index)
	if err := waitForNode(ctx, part); err != nil {
		return err
	}
	out, code, err := runTool(ctx, e2fsck, "-fp", part)
	if err != nil {
		return err
	}
	// 0 clean, 1 errors fixed, 2 fixed and a reboot was advised.
	if code > 2 {
		return fmt.Errorf("e2fsck %s exited %d: %s", part, code, lastLine(out))
	}
	out, code, err = runTool(ctx, resize, part)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("resize2fs %s exited %d: %s", part, code, lastLine(out))
	}
	return nil
}

func rereadPartitions(ctx context.Context, disk string) error {
	f, err := os.Open(disk)
	if err != nil {
		return fmt.Errorf("reread partition table: %w", err)
	}
	ioctlErr := unix.IoctlSetInt(int(f.Fd()), unix.BLKRRPART, 0)
	f.Close()
	if ioctlErr == nil {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "partx", "-u", disk).CombinedOutput(); err != nil {
		return fmt.Errorf("reread partition table: %w (%s)", ioctlErr, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitForNode(ctx context.Context, path string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear after the partition table was reread", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func lookE2fs(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/opt/homebrew/opt/e2fsprogs/sbin", "/usr/local/opt/e2fsprogs/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("e2fsprogs is not installed (%s); apt install e2fsprogs", name)
}

func runTool(ctx context.Context, bin string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), -1, err
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
