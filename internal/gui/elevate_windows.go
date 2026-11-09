package gui

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// ensureElevated restarts Meteor Imager with administrator rights (one UAC
// prompt), which Windows needs for raw disk writes. It returns true when this
// process should exit because the elevated copy took over.
func ensureElevated() (bool, error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	args := strings.Join(os.Args[1:], " ")
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(args)
	cwd, _ := os.Getwd()
	dir, _ := syscall.UTF16PtrFromString(cwd)
	if err := windows.ShellExecute(0, verb, file, params, dir, windows.SW_NORMAL); err != nil {
		return false, fmt.Errorf("Meteor Imager needs administrator rights to write SD cards: %w", err)
	}
	return true, nil
}
