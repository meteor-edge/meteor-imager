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

// SchemaVersion is the FlashProfile JSON version this build reads and writes.
const SchemaVersion = 1

// FlashProfile is everything written to the boot partition. Secrets (Wi-Fi PSK,
// enrollment API key) are only ever held in memory or in a file the operator
// chose; Meteor Imager never stores them itself.
type FlashProfile struct {
	SchemaVersion int         `json:"schema_version"`
	WiFi          *WiFi       `json:"wifi,omitempty"`
	Enrollment    *Enrollment `json:"enrollment,omitempty"`
	Hostname      *string     `json:"hostname"`
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
}

// SSH is reserved in v1: set values are rejected so nothing is silently ignored.
type SSH struct {
	Enabled        *bool    `json:"enabled"`
	AuthorizedKeys []string `json:"authorized_keys"`
}

var (
	countryPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	domainPattern   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
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

	if p.SchemaVersion != SchemaVersion {
		add("schema_version must be %d", SchemaVersion)
	}
	if p.WiFi == nil && p.Enrollment == nil && p.Hostname == nil && p.Timezone == nil {
		add("nothing to write: set wifi, enrollment, hostname or timezone")
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
	}
	if p.Hostname != nil && !hostnamePattern.MatchString(*p.Hostname) {
		add("hostname must be 1 to 63 lower-case letters, digits or hyphens")
	}
	if p.Timezone != nil {
		if _, err := time.LoadLocation(*p.Timezone); err != nil || *p.Timezone == "" || *p.Timezone == "Local" {
			add("timezone must be an IANA name such as Europe/Berlin")
		}
	}
	if p.SSH != nil && (p.SSH.Enabled != nil || len(p.SSH.AuthorizedKeys) > 0) {
		add("ssh is reserved for a later version and is not written yet; remove it")
	}
	if len(p.Extras) > 0 {
		add("extras is reserved for a later version; remove it")
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
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
	return c
}
