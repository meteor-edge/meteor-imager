// Package profile defines FlashProfile, the settings Meteor Imager writes to a
// card's boot partition. The JSON shape is versioned (schema_version) so new
// settings can be added without breaking saved profiles.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the FlashProfile JSON version this build writes. It also reads
// version 1 (no device name, no SSH key) and 2 (no user, SSH key only).
const SchemaVersion = 3

// FlashProfile is everything written to the boot partition. Secrets (Wi-Fi PSK,
// enrollment API key, user password) are only ever held in memory or in a file
// the operator chose; Meteor Imager never stores them itself.
type FlashProfile struct {
	SchemaVersion int         `json:"schema_version"`
	WiFi          *WiFi       `json:"wifi,omitempty"`
	Enrollment    *Enrollment `json:"enrollment,omitempty"`
	Hostname      *string     `json:"hostname"`
	User          *User       `json:"user,omitempty"`
	SSH           *SSH        `json:"ssh,omitempty"`
	Timezone      *string     `json:"timezone"`
	// Extras is reserved for settings added in later schema versions. v1 rejects
	// unknown keys here so nothing is silently ignored.
	Extras map[string]any `json:"extras,omitempty"`
}

// WiFi joins a WPA2/WPA3-personal network on first boot.
type WiFi struct {
	SSID   string `json:"ssid"`
	PSK    string `json:"psk"`
	Hidden bool   `json:"hidden"`
	// Country is the ISO 3166-1 alpha-2 regulatory domain, e.g. "DE".
	Country string `json:"country"`
}

// Enrollment is written to .meteorenv for MeteorCloud first boot.
type Enrollment struct {
	// MeteorDomain is the control plane the Pi reaches: "fleet.example.com" or
	// "192.168.0.10:8000". No scheme; first boot tries http then https.
	MeteorDomain string `json:"meteor_domain"`
	MeteorAPIKey string `json:"meteor_api_key"`
	// DeviceName is the name the device asks for in MeteorCloud (v2). Empty: the hostname.
	DeviceName string `json:"device_name,omitempty"`
}

// User is the login user (v3). As in Raspberry Pi Imager, the image's first user
// (uid 1000) is renamed to Name; images without one get a new user with sudo.
type User struct {
	Name string `json:"name"`
	// Password is plain text here and reaches the card only as a SHA-512 crypt
	// hash. Empty leaves the password unchanged (log in with an SSH key) unless
	// PasswordHash is set.
	Password string `json:"password,omitempty"`
	// PasswordHash is an already hashed SHA-512 crypt value ($6$…), e.g. from an
	// uploaded meteorenv.txt. Used only when Password is empty.
	PasswordHash string `json:"password_hash,omitempty"`
}

// SSH configures the SSH server (v3) and adds a public key (v2).
type SSH struct {
	// Enabled true turns SSH on, false turns it off; unset keeps the image's
	// setting (a key alone also turns SSH on).
	Enabled *bool `json:"enabled,omitempty"`
	// PasswordAuthentication allows (true) or refuses (false) password logins;
	// unset keeps the image's setting.
	PasswordAuthentication *bool `json:"password_authentication,omitempty"`
	// AuthorizedKeys holds one OpenSSH public key.
	AuthorizedKeys []string `json:"authorized_keys,omitempty"`
	// User gets the key; empty means user.name, else the image's first login user.
	User string `json:"user,omitempty"`
}

var (
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	domainPattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	sshKeyPattern   = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( [^\r\n]*)?$`)
	userPattern     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// Parse reads a FlashProfile, rejecting unknown fields and newer schema versions.
func Parse(r io.Reader) (*FlashProfile, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var p FlashProfile
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if p.SchemaVersion == 0 {
		return nil, errors.New("profile: schema_version is required")
	}
	if p.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("profile: schema_version %d is newer than this Meteor Imager supports (%d); update Meteor Imager", p.SchemaVersion, SchemaVersion)
	}
	return &p, nil
}

// Validate checks the profile. Error messages never contain secret values.
func (p *FlashProfile) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if p.SchemaVersion < 1 || p.SchemaVersion > SchemaVersion {
		add("schema_version must be 1 or %d", SchemaVersion)
	}
	if p.Empty() {
		add("nothing to write: set wifi, enrollment, hostname, timezone, user or ssh")
	}
	if w := p.WiFi; w != nil {
		if n := len(w.SSID); n == 0 || n > 32 {
			add("wifi.ssid must be 1 to 32 bytes")
		}
		if n := len(w.PSK); n < 8 || n > 63 {
			// 64 hex characters (a raw PSK) is not supported: NetworkManager and
			// wpa_supplicant both accept passphrases of 8 to 63 characters.
			add("wifi.psk must be 8 to 63 characters")
		}
		if strings.ContainsAny(w.SSID+w.PSK, "\x00\r\n") {
			add("wifi.ssid and wifi.psk must not contain line breaks")
		}
		if w.Country == "" {
			add("wifi.country is required (two letters, e.g. DE); Wi-Fi stays off without it")
		} else if !countryPattern.MatchString(w.Country) {
			add("wifi.country must be two upper-case letters, e.g. DE")
		}
	}
	if e := p.Enrollment; e != nil {
		if err := ValidateDomain(e.MeteorDomain); err != nil {
			add("enrollment.meteor_domain: %v", err)
		}
		if !strings.HasPrefix(e.MeteorAPIKey, "key_") || len(e.MeteorAPIKey) < 8 || strings.ContainsAny(e.MeteorAPIKey, " \t\r\n=\"'") {
			add("enrollment.meteor_api_key must be a MeteorCloud enrollment key (starts with key_)")
		}
		if e.DeviceName != "" {
			if p.SchemaVersion < 2 {
				add("enrollment.device_name needs schema_version 2")
			}
			if len(e.DeviceName) > 63 || strings.TrimSpace(e.DeviceName) != e.DeviceName || strings.ContainsAny(e.DeviceName, "\x00\r\n=") {
				add("enrollment.device_name must be 1 to 63 characters, without line breaks or leading/trailing spaces")
			}
		}
	}
	if p.Hostname != nil && !hostnamePattern.MatchString(*p.Hostname) {
		add("hostname must be 1 to 63 lower-case letters, digits or hyphens")
	}
	if p.Timezone != nil {
		if _, err := time.LoadLocation(*p.Timezone); err != nil || *p.Timezone == "" || *p.Timezone == "Local" {
			add("timezone must be an IANA name such as Europe/Berlin")
		}
	}
	if u := p.User; u != nil {
		if p.SchemaVersion < 3 {
			add("user needs schema_version 3")
		}
		if !userPattern.MatchString(u.Name) || u.Name == "root" {
			add("user.name must be a Linux user name (lower-case letters, digits, - or _; not root)")
		}
		if len(u.Password) > 256 || strings.ContainsAny(u.Password, "\x00\r\n") {
			add("user.password must be at most 256 characters, without line breaks")
		}
		if u.PasswordHash != "" {
			if !strings.HasPrefix(u.PasswordHash, "$6$") || strings.ContainsAny(u.PasswordHash, "\x00\r\n") {
				add("user.password_hash must be a SHA-512 crypt hash ($6$…)")
			}
		}
	}
	if s := p.SSH; s != nil {
		switch {
		case p.SchemaVersion < 2 && (s.Enabled != nil || len(s.AuthorizedKeys) > 0 || s.User != ""):
			add("ssh needs schema_version 2")
		case p.SchemaVersion < 3 && (s.Enabled != nil || s.PasswordAuthentication != nil):
			add("ssh.enabled and ssh.password_authentication need schema_version 3")
		case len(s.AuthorizedKeys) > 1:
			add("ssh.authorized_keys takes one key")
		}
		for _, k := range s.AuthorizedKeys {
			if !sshKeyPattern.MatchString(k) {
				add("ssh.authorized_keys must be an OpenSSH public key (ssh-ed25519 AAAA… comment)")
			}
		}
		if s.User != "" && !userPattern.MatchString(s.User) {
			add("ssh.user must be a Linux user name")
		}
	}
	if len(p.Extras) > 0 {
		add("extras is reserved for a later version; remove it")
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// Empty reports whether the profile would write nothing.
func (p *FlashProfile) Empty() bool {
	return p.WiFi == nil && p.Enrollment == nil && p.Hostname == nil && p.Timezone == nil &&
		p.User == nil && !p.SSH.configured()
}

func (s *SSH) configured() bool {
	return s != nil && (s.Enabled != nil || s.PasswordAuthentication != nil || len(s.AuthorizedKeys) > 0)
}

// SSHKey returns the SSH public key to install, or "".
func (p *FlashProfile) SSHKey() string {
	if p.SSH == nil || len(p.SSH.AuthorizedKeys) == 0 {
		return ""
	}
	return p.SSH.AuthorizedKeys[0]
}

// SSHKeyUser is the user that gets the SSH key: ssh.user, else user.name, else
// "" for the image's first login user.
func (p *FlashProfile) SSHKeyUser() string {
	if p.SSH != nil && p.SSH.User != "" {
		return p.SSH.User
	}
	if p.User != nil {
		return p.User.Name
	}
	return ""
}

// SSHEnabled is the SSH server state to set: a key alone turns SSH on; nil
// leaves the image's setting.
func (p *FlashProfile) SSHEnabled() *bool {
	if p.SSH == nil {
		return nil
	}
	if p.SSH.Enabled != nil {
		return p.SSH.Enabled
	}
	if len(p.SSH.AuthorizedKeys) > 0 {
		on := true
		return &on
	}
	return nil
}

// ValidateDomain checks a METEOR_DOMAIN value: a host or host:port, no scheme or path.
func ValidateDomain(domain string) error {
	if domain == "" {
		return errors.New("is required")
	}
	if strings.Contains(domain, "://") || strings.ContainsAny(domain, "/?# ") {
		return errors.New("use only the host (and port), e.g. fleet.example.com or 192.168.0.10:8000")
	}
	if strings.EqualFold(strings.Split(domain, ":")[0], "localhost") || strings.HasPrefix(domain, "127.") {
		return errors.New("must be an address the Pi can reach, not localhost")
	}
	if !domainPattern.MatchString(domain) {
		return errors.New("is not a valid host or host:port")
	}
	if host, port, err := net.SplitHostPort(domain); err == nil {
		if host == "" || port == "" {
			return errors.New("is not a valid host:port")
		}
	}
	return nil
}

// ValidationError lists every problem at once so a form can show them together.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid profile: " + strings.Join(e.Problems, "; ")
}

// Clear drops the secrets once they have been written. Go strings are
// immutable and cannot be wiped, so this only releases the references early;
// secrets are never written anywhere except the card or a file the operator chose.
func (p *FlashProfile) Clear() {
	if p.WiFi != nil {
		p.WiFi.PSK = ""
	}
	if p.Enrollment != nil {
		p.Enrollment.MeteorAPIKey = ""
	}
	if p.User != nil {
		p.User.Password = ""
		p.User.PasswordHash = ""
	}
}

// Redacted returns a copy safe to log or persist: secrets removed.
func (p *FlashProfile) Redacted() FlashProfile {
	c := *p
	if p.WiFi != nil {
		w := *p.WiFi
		w.PSK = ""
		c.WiFi = &w
	}
	if p.Enrollment != nil {
		e := *p.Enrollment
		e.MeteorAPIKey = ""
		c.Enrollment = &e
	}
	if p.User != nil {
		u := *p.User
		u.Password = ""
		u.PasswordHash = ""
		c.User = &u
	}
	return c
}
