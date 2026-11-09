package devices

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
)

const getDiskScript = `$ErrorActionPreference='Stop'; ` +
	`Get-Disk | ForEach-Object { $d=$_; ` +
	`$l=(Get-Partition -DiskNumber $d.Number -ErrorAction SilentlyContinue | Where-Object DriveLetter | ForEach-Object { [string]$_.DriveLetter }) -join ' '; ` +
	`[pscustomobject]@{Number=$d.Number;FriendlyName=$d.FriendlyName;Size=$d.Size;BusType=[string]$d.BusType;` +
	`IsBoot=$d.IsBoot;IsSystem=$d.IsSystem;IsReadOnly=$d.IsReadOnly;Letters=$l} } | ConvertTo-Json -Compress`

func list(ctx context.Context) ([]Device, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", getDiskScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Get-Disk: %w", err)
	}
	return parseWindowsDisks(out)
}
