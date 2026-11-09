package gui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		// Under sudo, open the browser as the desktop user, not root.
		if u := os.Getenv("SUDO_USER"); u != "" && os.Geteuid() == 0 {
			cmd = exec.Command("sudo", "-u", u, "xdg-open", url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

var errNoPicker = errors.New("no file chooser available; type the image path")

// pickFile shows the platform's native "open file" dialog.
func pickFile(ctx context.Context) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "osascript", "-e",
			`POSIX path of (choose file with prompt "Choose a Raspberry Pi OS image")`)
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-Command",
			`Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.OpenFileDialog; `+
				`$d.Filter = 'Disk images (*.img;*.xz;*.gz;*.zip)|*.img;*.xz;*.gz;*.zip|All files (*.*)|*.*'; `+
				`if ($d.ShowDialog() -eq 'OK') { $d.FileName }`)
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			cmd = exec.CommandContext(ctx, p, "--file-selection", "--title=Choose a Raspberry Pi OS image",
				"--file-filter=Disk images | *.img *.img.xz *.xz *.gz *.zip", "--file-filter=All files | *")
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			cmd = exec.CommandContext(ctx, p, "--getopenfilename", ".", "*.img *.xz *.gz *.zip")
		} else {
			return "", errNoPicker
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", nil // dialog cancelled
		}
		return "", errNoPicker
	}
	return strings.TrimSpace(string(out)), nil
}
