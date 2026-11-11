package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `{
  "schema_version": 1,
  "wifi": {"ssid": "home", "psk": "correct horse", "hidden": false, "country": "DE"},
  "enrollment": {"meteor_domain": "192.168.0.10:8000", "meteor_api_key": "key_abcdef"},
  "hostname": "pi-01",
  "timezone": "Europe/Berlin"
}`

func TestParseValid(t *testing.T) {
	p, err := Parse(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseRejects(t *testing.T) {
	for name, in := range map[string]string{
		"unknown field":  `{"schema_version":1,"surprise":true}`,
		"newer schema":   `{"schema_version":3}`,
		"missing schema": `{"hostname":"x"}`,
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestValidateProblems(t *testing.T) {
	host, tz := "Bad_Host", "Mars/Olympus"
	enabled := true
	p := &FlashProfile{
		SchemaVersion: 1,
		WiFi:          &WiFi{SSID: strings.Repeat("x", 33), PSK: "short\n", Country: "de"},
		Enrollment:    &Enrollment{MeteorDomain: "https://fleet.example.com", MeteorAPIKey: "secret-value"},
		Hostname:      &host,
		Timezone:      &tz,
		SSH:           &SSH{Enabled: &enabled},
		Extras:        map[string]any{"x": 1},
	}
	err := p.Validate()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	if len(ve.Problems) != 10 {
		t.Errorf("got %d problems: %v", len(ve.Problems), ve.Problems)
	}
	if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "short") && strings.Contains(err.Error(), "short\n") {
		t.Errorf("secret in error: %v", err)
	}
}

func TestValidateDomain(t *testing.T) {
	for _, ok := range []string{"fleet.example.com", "192.168.0.10:8000", "meteor.local"} {
		if err := ValidateDomain(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "localhost:8000", "127.0.0.1", "http://x.com", "x.com/path", "x.com:"} {
		if err := ValidateDomain(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestRedactedAndClear(t *testing.T) {
	p, _ := Parse(strings.NewReader(valid))
	r := p.Redacted()
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "correct horse") || strings.Contains(string(b), "key_abcdef") {
		t.Errorf("redacted copy has secrets: %s", b)
	}
	if p.WiFi.PSK == "" {
		t.Error("Redacted changed the original")
	}
	p.Clear()
	if p.WiFi.PSK != "" || p.Enrollment.MeteorAPIKey != "" {
		t.Error("Clear left secrets")
	}
}

func TestPrefsNeverHoldSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	p, _ := Parse(strings.NewReader(valid))
	if err := SavePrefs(PrefsFrom("fleet.example.com", p)); err != nil {
		t.Fatal(err)
	}
	path, _ := PrefsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "correct horse") || strings.Contains(string(data), "key_") {
		t.Fatalf("prefs hold secrets: %s", data)
	}
	if st, _ := os.Stat(path); st.Mode().Perm()&0o077 != 0 && filepath.Separator == '/' {
		t.Errorf("prefs mode %v", st.Mode())
	}
	if got := LoadPrefs(); got.WiFiSSID != "home" || got.MeteorDomain != "192.168.0.10:8000" {
		t.Errorf("LoadPrefs = %+v", got)
	}
	if err := RemovePrefs(); err != nil {
		t.Fatal(err)
	}
	if got := LoadPrefs(); got.WiFiSSID != "" {
		t.Error("prefs not removed")
	}
}

const testSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyOnlyForTests user@laptop"

func TestV2DeviceNameAndSSHKey(t *testing.T) {
	in := `{
  "schema_version": 2,
  "enrollment": {"meteor_domain": "fleet.example.com", "meteor_api_key": "key_abcdef", "device_name": "Lab Pi 01"},
  "ssh": {"authorized_keys": ["` + testSSHKey + `"], "user": "pi"},
  "hostname": null,
  "timezone": null
}`
	p, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.SSHKey() != testSSHKey || p.Enrollment.DeviceName != "Lab Pi 01" {
		t.Errorf("got %+v", p)
	}
	// An SSH key alone is something to write.
	only := &FlashProfile{SchemaVersion: 2, SSH: &SSH{AuthorizedKeys: []string{testSSHKey}}}
	if err := only.Validate(); err != nil {
		t.Errorf("ssh only: %v", err)
	}
}

func TestV2FieldsAreRejectedInV1AndChecked(t *testing.T) {
	enabled := true
	for name, p := range map[string]*FlashProfile{
		"device name in v1": {SchemaVersion: 1, Enrollment: &Enrollment{MeteorDomain: "x.example.com", MeteorAPIKey: "key_abcdef", DeviceName: "a"}},
		"ssh in v1":         {SchemaVersion: 1, SSH: &SSH{AuthorizedKeys: []string{testSSHKey}}},
		"not a key":         {SchemaVersion: 2, SSH: &SSH{AuthorizedKeys: []string{"hello world"}}},
		"key with newline":  {SchemaVersion: 2, SSH: &SSH{AuthorizedKeys: []string{testSSHKey + "\nssh-rsa AAAA x"}}},
		"two keys":          {SchemaVersion: 2, SSH: &SSH{AuthorizedKeys: []string{testSSHKey, testSSHKey}}},
		"enabled reserved":  {SchemaVersion: 2, SSH: &SSH{Enabled: &enabled, AuthorizedKeys: []string{testSSHKey}}},
		"bad user":          {SchemaVersion: 2, SSH: &SSH{AuthorizedKeys: []string{testSSHKey}, User: "Root User"}},
		"name with newline": {SchemaVersion: 2, Enrollment: &Enrollment{MeteorDomain: "x.example.com", MeteorAPIKey: "key_abcdef", DeviceName: "a\nMETEOR_API_KEY=x"}},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
