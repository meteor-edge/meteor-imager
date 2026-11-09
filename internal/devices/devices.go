// Package devices lists disks Meteor Imager may write to. Internal and system
// disks are never offered unless the caller explicitly asks for them.
package devices

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Device is a whole disk.
type Device struct {
	// Path is what to open for raw writes: /dev/sdb, /dev/rdisk4, \\.\PhysicalDrive2.
	Path string `json:"path"`
	// Name is the OS's short name: sdb, disk4, PhysicalDrive2.
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	SizeBytes   int64    `json:"size_bytes"`
	Bus         string   `json:"bus,omitempty"`
	Removable   bool     `json:"removable"`
	ReadOnly    bool     `json:"read_only"`
	System      bool     `json:"system"`
	Mountpoints []string `json:"mountpoints,omitempty"`
}

// Safe reports whether the device may be offered by default: removable,
// writable, and not holding the running system.
func (d Device) Safe() bool { return d.Removable && !d.System && !d.ReadOnly && d.SizeBytes > 0 }

// Describe is a one-line summary for confirmation prompts.
func (d Device) Describe() string {
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = "Unknown device"
	}
	s := fmt.Sprintf("%s  %s  %s", d.Path, HumanSize(d.SizeBytes), model)
	if d.Bus != "" {
		s += "  (" + d.Bus + ")"
	}
	if len(d.Mountpoints) > 0 {
		s += "  mounted at " + strings.Join(d.Mountpoints, ", ")
	}
	return s
}

// List returns disks; with all=false only Safe ones.
func List(ctx context.Context, all bool) ([]Device, error) {
	devs, err := list(ctx)
	if err != nil {
		return nil, err
	}
	out := devs[:0]
	for _, d := range devs {
		if all || d.Safe() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Find looks a device up by path or name among all disks.
func Find(ctx context.Context, pathOrName string) (Device, error) {
	devs, err := list(ctx)
	if err != nil {
		return Device{}, err
	}
	for _, d := range devs {
		if d.Path == pathOrName || d.Name == pathOrName || altPath(d) == pathOrName {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("no disk %s found; run list-devices to see what is connected", pathOrName)
}

// altPath accepts /dev/diskN for /dev/rdiskN on macOS.
func altPath(d Device) string { return strings.Replace(d.Path, "/dev/rdisk", "/dev/disk", 1) }

// HumanSize formats bytes in decimal units, as card packaging does.
func HumanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
