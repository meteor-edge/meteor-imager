package flash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/meteor-edge/meteor-imager/internal/devices"
)

type unixTarget struct{ *os.File }

// Sync: /dev/rdiskN is unbuffered, so there is no cache to drop.
func (t unixTarget) Sync() error { return t.File.Sync() }

func canWriteDevices() (bool, string) { return true, "" }

func openDevice(ctx context.Context, d devices.Device) (Target, error) {
	if !strings.HasPrefix(d.Path, "/dev/rdisk") {
		return nil, fmt.Errorf("not a raw disk path: %s", d.Path)
	}
	whole := strings.Replace(d.Path, "/dev/rdisk", "/dev/disk", 1)
	if out, err := exec.CommandContext(ctx, "diskutil", "unmountDisk", "force", whole).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("unmount %s: %s", whole, strings.TrimSpace(string(out)))
	}
	if os.Geteuid() == 0 {
		f, err := os.OpenFile(d.Path, os.O_RDWR|unix.O_EXLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", d.Path, err)
		}
		return unixTarget{f}, nil
	}
	f, err := authopen(ctx, d.Path)
	if err != nil {
		return nil, err
	}
	return unixTarget{f}, nil
}

// authopen asks macOS for an administrator password and receives the opened
// descriptor over a socket (SCM_RIGHTS), the same way Raspberry Pi Imager and
// balenaEtcher get raw disk access without running as root.
func authopen(ctx context.Context, path string) (*os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	ours := os.NewFile(uintptr(fds[0]), "authopen-parent")
	theirs := os.NewFile(uintptr(fds[1]), "authopen-child")
	defer ours.Close()

	cmd := exec.CommandContext(ctx, "/usr/libexec/authopen", "-stdoutpipe", "-o", strconv.Itoa(unix.O_RDWR), path)
	cmd.Stdout = theirs
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		theirs.Close()
		return nil, err
	}
	theirs.Close()

	buf := make([]byte, 64)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, rerr := unix.Recvmsg(fds[0], buf, oob, 0)
	werr := cmd.Wait()
	if rerr != nil || oobn == 0 {
		if werr != nil {
			return nil, fmt.Errorf("permission to write %s was not granted: %s", path, strings.TrimSpace(stderr.String()))
		}
		return nil, errors.New("authopen returned no file descriptor")
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return nil, errors.New("authopen returned no file descriptor")
	}
	rights, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(rights) == 0 {
		return nil, errors.New("authopen returned no file descriptor")
	}
	return os.NewFile(uintptr(rights[0]), path), nil
}

func eject(ctx context.Context, d devices.Device) error {
	whole := strings.Replace(d.Path, "/dev/rdisk", "/dev/disk", 1)
	if out, err := exec.CommandContext(ctx, "diskutil", "eject", whole).CombinedOutput(); err != nil {
		return fmt.Errorf("eject %s: %s", whole, strings.TrimSpace(string(out)))
	}
	return nil
}
