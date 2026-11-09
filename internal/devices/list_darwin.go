package devices

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

func list(ctx context.Context) ([]Device, error) {
	// "physical" leaves out APFS synthesized disks and disk images.
	out, err := exec.CommandContext(ctx, "diskutil", "list", "-plist", "physical").Output()
	if err != nil {
		return nil, fmt.Errorf("diskutil list: %w", err)
	}
	ids, err := parseDiskutilList(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	var devs []Device
	for _, id := range ids {
		info, err := exec.CommandContext(ctx, "diskutil", "info", "-plist", id).Output()
		if err != nil {
			continue
		}
		d, err := parseDiskutilInfo(bytes.NewReader(info))
		if err != nil {
			continue
		}
		devs = append(devs, d)
	}
	return devs, nil
}
