package profile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Prefs are the last-used non-secret values, so the next card takes fewer clicks.
// The Wi-Fi PSK, the enrollment API key and the user password are never stored.
type Prefs struct {
	ServerURL    string `json:"server_url,omitempty"`
	WiFiSSID     string `json:"wifi_ssid,omitempty"`
	WiFiHidden   bool   `json:"wifi_hidden,omitempty"`
	WiFiCountry  string `json:"wifi_country,omitempty"`
	MeteorDomain string `json:"meteor_domain,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	UserName     string `json:"user_name,omitempty"`
	// SSHEnabled and SSHPassword are nil when never chosen; the UI then turns
	// SSH with password login on.
	SSHEnabled  *bool `json:"ssh_enabled,omitempty"`
	SSHPassword *bool `json:"ssh_password_authentication,omitempty"`
	// SSHKey is a public key: not a secret.
	SSHKey string `json:"ssh_authorized_key,omitempty"`
}

// PrefsPath is <user config dir>/meteor-imager/prefs.json.
func PrefsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "meteor-imager", "prefs.json"), nil
}

// LoadPrefs returns saved prefs, or empty prefs if none exist or the file is unreadable.
func LoadPrefs() Prefs {
	var p Prefs
	path, err := PrefsPath()
	if err != nil {
		return p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	_ = json.Unmarshal(data, &p)
	return p
}

// SavePrefs writes prefs with owner-only permissions.
func SavePrefs(p Prefs) error {
	path, err := PrefsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PrefsFrom keeps only the non-secret fields of a profile.
func PrefsFrom(serverURL string, p *FlashProfile) Prefs {
	out := Prefs{ServerURL: serverURL}
	if p.WiFi != nil {
		out.WiFiSSID, out.WiFiHidden, out.WiFiCountry = p.WiFi.SSID, p.WiFi.Hidden, p.WiFi.Country
	}
	if p.Enrollment != nil {
		out.MeteorDomain = p.Enrollment.MeteorDomain
	}
	if p.Hostname != nil {
		out.Hostname = *p.Hostname
	}
	out.SSHKey = p.SSHKey()
	if p.User != nil {
		out.UserName = p.User.Name
	}
	// No SSH settings on a card means the SSH box was unticked: remember that.
	off := false
	out.SSHEnabled = &off
	if on := p.SSHEnabled(); on != nil {
		out.SSHEnabled = on
	}
	if p.SSH != nil {
		out.SSHPassword = p.SSH.PasswordAuthentication
	}
	if p.Timezone != nil {
		out.Timezone = *p.Timezone
	}
	return out
}

// RemovePrefs deletes saved preferences.
func RemovePrefs() error {
	path, err := PrefsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
