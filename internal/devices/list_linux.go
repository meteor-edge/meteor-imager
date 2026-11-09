package devices

import (
	"context"
	"fmt"
	"os/exec"
)

func list(ctx context.Context) ([]Device, error) {
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-b",
		"-o", "NAME,PATH,SIZE,MODEL,VENDOR,TRAN,RM,HOTPLUG,RO,TYPE,MOUNTPOINT").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	return parseLsblk(out)
}
