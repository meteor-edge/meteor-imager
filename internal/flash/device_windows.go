package flash

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/meteor-edge/meteor-imager/internal/devices"
)

var physicalDrive = regexp.MustCompile(`^\\\\\.\\PhysicalDrive([0-9]+)$`)

type winTarget struct{ *os.File }

func (t winTarget) Sync() error { return windows.FlushFileBuffers(windows.Handle(t.Fd())) }

func canWriteDevices() (bool, string) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return true, ""
	}
	return false, "Writing cards needs administrator rights. Start Meteor Imager as administrator."
}

func openDevice(ctx context.Context, d devices.Device) (Target, error) {
	m := physicalDrive.FindStringSubmatch(d.Path)
	if m == nil {
		return nil, fmt.Errorf("not a physical drive path: %s", d.Path)
	}
	if ok, why := canWriteDevices(); !ok {
		return nil, fmt.Errorf("%s", why)
	}
	// Remove the card's partitions first, so Windows holds no volume locks on
	// it while it is written. The new partition table is written last.
	script := fmt.Sprintf("select disk %s\nattributes disk clear readonly noerr\nclean\nrescan\nexit\n", m[1])
	cmd := exec.CommandContext(ctx, "diskpart.exe")
	cmd.Stdin = strings.NewReader(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("diskpart clean disk %s: %v\n%s", m[1], err, out)
	}
	path, err := windows.UTF16PtrFromString(d.Path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", d.Path, err)
	}
	return winTarget{os.NewFile(uintptr(h), d.Path)}, nil
}

// eject: Windows has no simple per-disk eject for card readers; after the
// final flush the card is safe to remove.
func eject(context.Context, devices.Device) error { return nil }
