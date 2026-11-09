//go:build !windows

package gui

// ensureElevated: Linux uses udisks2/polkit and macOS uses authopen when a card
// is written, so the UI itself runs as the normal user.
func ensureElevated() (bool, error) { return false, nil }
