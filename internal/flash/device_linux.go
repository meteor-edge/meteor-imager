package flash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/meteor-edge/meteor-imager/internal/devices"
)

type unixTarget struct{ *os.File }

func (t unixTarget) Sync() error {
	if err := t.File.Sync(); err != nil {
		return err
	}
	// Drop cached pages so verification reads from the card itself.
	_ = unix.Fadvise(int(t.Fd()), 0, 0, unix.FADV_DONTNEED)
	_ = unix.IoctlSetInt(int(t.Fd()), unix.BLKFLSBUF, 0)
	return nil
}

func canWriteDevices() (bool, string) {
	if os.Geteuid() == 0 {
		return true, ""
	}
	if _, err := dbus.SystemBus(); err == nil {
		return true, ""
	}
	return false, "Writing cards needs root or a desktop session with udisks2. Run with sudo."
}

func openDevice(ctx context.Context, d devices.Device) (Target, error) {
	if !strings.HasPrefix(d.Path, "/dev/") {
		return nil, fmt.Errorf("not a device path: %s", d.Path)
	}
	if err := unmountAll(ctx, d); err != nil {
		return nil, err
	}
	if os.Geteuid() == 0 {
		f, err := os.OpenFile(d.Path, os.O_RDWR|unix.O_EXCL|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", d.Path, err)
		}
		return unixTarget{f}, nil
	}
	f, err := udisksOpen(d.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s through udisks2 (or run with sudo): %w", d.Path, err)
	}
	return unixTarget{f}, nil
}

// udisksOpen asks udisks2 (and polkit, which may show a password prompt) for
// a read-write descriptor on the whole disk, as Raspberry Pi Imager does.
func udisksOpen(dev string) (*os.File, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	obj := conn.Object("org.freedesktop.UDisks2", udisksPath(dev))
	opts := map[string]dbus.Variant{
		"flags":                    dbus.MakeVariant(int32(unix.O_EXCL | unix.O_CLOEXEC)),
		"auth.no_user_interaction": dbus.MakeVariant(false),
	}
	var fd dbus.UnixFD
	if err := obj.Call("org.freedesktop.UDisks2.Block.OpenDevice", 0, "rw", opts).Store(&fd); err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), dev), nil
}

func udisksPath(dev string) dbus.ObjectPath {
	name := filepath.Base(dev)
	var b strings.Builder
	for _, c := range []byte(name) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	return dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/" + b.String())
}

// unmountAll unmounts every mounted partition of the disk.
func unmountAll(ctx context.Context, d devices.Device) error {
	out, err := exec.CommandContext(ctx, "lsblk", "-lnp", "-o", "NAME,MOUNTPOINT", d.Path).Output()
	if err != nil {
		return fmt.Errorf("lsblk %s: %w", d.Path, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		part, mnt := f[0], f[1]
		if mnt == "[SWAP]" {
			return fmt.Errorf("%s is in use as swap", part)
		}
		var cmd *exec.Cmd
		if os.Geteuid() == 0 {
			cmd = exec.CommandContext(ctx, "umount", part)
		} else {
			cmd = exec.CommandContext(ctx, "udisksctl", "unmount", "--no-user-interaction", "-b", part)
		}
		if msg, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("unmount %s (%s): %s", part, mnt, strings.TrimSpace(string(msg)))
		}
	}
	return nil
}

func eject(ctx context.Context, d devices.Device) error {
	_ = unmountAll(ctx, d) // the desktop may have mounted the fresh boot partition
	if _, err := exec.LookPath("udisksctl"); err == nil {
		if out, err := exec.CommandContext(ctx, "udisksctl", "power-off", "--no-user-interaction", "-b", d.Path).CombinedOutput(); err == nil {
			return nil
		} else if os.Geteuid() != 0 {
			return errors.New(strings.TrimSpace(string(out)))
		}
	}
	if _, err := exec.LookPath("eject"); err == nil {
		return exec.CommandContext(ctx, "eject", d.Path).Run()
	}
	return nil
}
