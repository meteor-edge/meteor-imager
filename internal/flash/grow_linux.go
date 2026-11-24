//go:build linux

package flash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/meteor-edge/meteor-imager/internal/devices"
	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

// ResizeRoot grows the ext filesystem of an MBR Linux slot so it fills that
// partition on the card (or image file). A small image written to a larger card
// ends with a root filesystem that uses the whole card, not the image size.
//
// The loop device is sized from the on-disk partition table, not from the
// kernel's cached partition nodes, so a card that was just rewritten still
// grows when BLKRRPART leaves the old size in place. A missing tool or a
// failed check is returned to the caller; the card itself has already been
// written.
func ResizeRoot(ctx context.Context, disk string, index int) error {
	if disk == "" || strings.Contains(disk, "..") {
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
	losetup, err := lookTool("losetup")
	if err != nil {
		return fmt.Errorf("losetup is not installed; apt install util-linux")
	}
	if strings.HasPrefix(disk, "/dev/") {
		if err := unmountAll(ctx, devices.Device{Path: disk}); err != nil {
			return err
		}
		// Best-effort: free kernel partition nodes so nothing holds the card.
		_ = rereadPartitions(ctx, disk)
		if err := unmountAll(ctx, devices.Device{Path: disk}); err != nil {
			return err
		}
	}

	part, err := mbrPartition(disk, index)
	if err != nil {
		return err
	}
	loop, err := attachLoop(ctx, losetup, disk, part.Offset(), part.Size())
	if err != nil {
		return err
	}
	defer detachLoop(ctx, losetup, loop)

	out, code, err := runTool(ctx, e2fsck, "-fp", loop)
	if err != nil {
		return err
	}
	// 0 clean, 1 errors fixed, 2 fixed and a reboot was advised.
	if code > 2 {
		return fmt.Errorf("e2fsck %s exited %d: %s", loop, code, lastLine(out))
	}
	out, code, err = runTool(ctx, resize, loop)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("resize2fs %s exited %d: %s", loop, code, lastLine(out))
	}
	if err := verifyExtSize(ctx, loop, part.Size()); err != nil {
		return err
	}
	return nil
}

// mbrPartition reads the on-disk MBR entry for index (1-based).
func mbrPartition(disk string, index int) (diskimage.Partition, error) {
	f, err := os.Open(disk)
	if err != nil {
		return diskimage.Partition{}, fmt.Errorf("read partition table on %s: %w", disk, err)
	}
	defer f.Close()
	parts, err := diskimage.ReadPartitions(f)
	if err != nil {
		return diskimage.Partition{}, err
	}
	for _, p := range parts {
		if p.Index == index {
			if len(p.Type) != 2 || p.Type != "83" {
				return diskimage.Partition{}, fmt.Errorf("partition %d is type %s, not Linux", index, p.Type)
			}
			if p.Size() <= 0 {
				return diskimage.Partition{}, fmt.Errorf("partition %d has no size", index)
			}
			return p, nil
		}
	}
	return diskimage.Partition{}, fmt.Errorf("partition %d not found on %s", index, disk)
}

func attachLoop(ctx context.Context, losetup, disk string, offset, size int64) (string, error) {
	out, code, err := runTool(ctx, losetup, "-f", "--show", "-o", strconv.FormatInt(offset, 10),
		"--sizelimit", strconv.FormatInt(size, 10), disk)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("losetup exited %d: %s (need root to grow the filesystem on the card)", code, lastLine(out))
	}
	loop := strings.TrimSpace(out)
	if !strings.HasPrefix(loop, "/dev/loop") {
		return "", fmt.Errorf("losetup returned %q", loop)
	}
	if err := waitForNode(ctx, loop); err != nil {
		_ = detachLoop(ctx, losetup, loop)
		return "", err
	}
	return loop, nil
}

func detachLoop(ctx context.Context, losetup, loop string) error {
	_, _, err := runTool(ctx, losetup, "-d", loop)
	return err
}

// verifyExtSize checks that the filesystem now fills the partition (within one
// block). Catches the failure mode where resize2fs exits 0 because the kernel
// still exposed the old, smaller partition size.
func verifyExtSize(ctx context.Context, dev string, wantBytes int64) error {
	dump, err := lookE2fs("dumpe2fs")
	if err != nil {
		return err
	}
	out, code, err := runTool(ctx, dump, "-h", dev)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("dumpe2fs %s exited %d: %s", dev, code, lastLine(out))
	}
	bs, blocks := extField(out, "Block size:"), extField(out, "Block count:")
	if bs <= 0 || blocks <= 0 {
		return errors.New("dumpe2fs: no block size or count")
	}
	got := bs * blocks
	if got < wantBytes-bs || got > wantBytes {
		return fmt.Errorf("filesystem is %d bytes after resize, partition is %d; the card did not take the new size", got, wantBytes)
	}
	return nil
}

func extField(out, label string) int64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, label) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		n, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
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
			return fmt.Errorf("%s did not appear", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func lookE2fs(name string) (string, error) {
	return lookTool(name)
}

func lookTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/opt/homebrew/opt/e2fsprogs/sbin", "/usr/local/opt/e2fsprogs/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if name == "losetup" {
		return "", fmt.Errorf("losetup is not installed; apt install util-linux")
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
