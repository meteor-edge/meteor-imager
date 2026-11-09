package bootfs

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// Files on the boot partition that Meteor Imager writes.
const (
	MeteorEnvFile     = ".meteorenv"
	UserDataFile      = "user-data"
	MetaDataFile      = "meta-data"
	NetworkConfigFile = "network-config"
	CmdlineFile       = "cmdline.txt"
	WPASupplicantFile = "wpa_supplicant.conf"
)

// Result says what was written, without any secret values.
type Result struct {
	Written  []string `json:"written"`
	Warnings []string `json:"warnings,omitempty"`
	// CloudInit is false for images without cloud-init (Raspberry Pi OS before
	// Bookworm 2024-11): Wi-Fi then goes to wpa_supplicant.conf, and enrollment
	// needs MeteorCloud first boot installed in the image.
	CloudInit bool `json:"cloud_init"`
}

// Options tune injection; the zero value is right for normal use.
type Options struct {
	// Now gives the instance id; tests pin it.
	Now func() time.Time
}

// Inject writes the profile to the boot partition. It never touches the root
// filesystem. The profile must already be valid.
func Inject(v *Volume, p *profile.FlashProfile, opt Options) (*Result, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	res := &Result{CloudInit: v.Exists(UserDataFile)}

	write := func(name string, data []byte) error {
		if err := v.WriteFile(name, data); err != nil {
			return err
		}
		res.Written = append(res.Written, name)
		return nil
	}

	if p.Enrollment != nil {
		if err := write(MeteorEnvFile, MeteorEnv(p.Enrollment)); err != nil {
			return nil, err
		}
		if !res.CloudInit {
			res.Warnings = append(res.Warnings,
				"This image has no cloud-init, so it will not install MeteorCloud first boot by itself. "+
					"Use a Raspberry Pi OS image from November 2024 or later, or one with MeteorCloud first boot preinstalled.")
		}
	}

	if !res.CloudInit {
		if p.Hostname != nil || p.Timezone != nil {
			res.Warnings = append(res.Warnings, "Hostname and timezone need cloud-init; this image has none, so they were not set.")
		}
		if p.WiFi != nil {
			if err := write(WPASupplicantFile, WPASupplicant(p.WiFi)); err != nil {
				return nil, err
			}
		}
		return res, nil
	}

	if p.Enrollment == nil && p.WiFi == nil && p.Hostname == nil && p.Timezone == nil {
		return res, nil
	}

	existing, err := v.ReadFile(UserDataFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", UserDataFile, err)
	}
	userData, err := MergeUserData(existing, p)
	if err != nil {
		return nil, err
	}
	if err := write(UserDataFile, userData); err != nil {
		return nil, err
	}

	if p.WiFi != nil {
		var current []byte
		if v.Exists(NetworkConfigFile) {
			if current, err = v.ReadFile(NetworkConfigFile); err != nil {
				return nil, fmt.Errorf("read %s: %w", NetworkConfigFile, err)
			}
		}
		netcfg, err := NetworkConfig(current, p.WiFi)
		if err != nil {
			return nil, err
		}
		if err := write(NetworkConfigFile, netcfg); err != nil {
			return nil, err
		}
	}

	// A new instance id makes cloud-init treat the card as a fresh instance and
	// run user-data again, even on a card that has booted before.
	id := fmt.Sprintf("meteor-imager-%d", now().UnixMilli())
	if err := write(MetaDataFile, []byte("instance-id: "+id+"\n")); err != nil {
		return nil, err
	}

	// Point cloud-init at the boot partition explicitly, as Raspberry Pi Imager does.
	if v.Exists(CmdlineFile) {
		cmdline, err := v.ReadFile(CmdlineFile)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", CmdlineFile, err)
		}
		if err := write(CmdlineFile, []byte(withDatasource(string(cmdline), id))); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// MeteorEnv is the .meteorenv file MeteorCloud first boot reads (KEY=value lines).
func MeteorEnv(e *profile.Enrollment) []byte {
	return []byte("METEOR_DOMAIN=" + e.MeteorDomain + "\nMETEOR_API_KEY=" + e.MeteorAPIKey + "\n")
}

// withDatasource sets "ds=nocloud;i=<id>" on the single cmdline.txt line,
// replacing one an earlier run added.
func withDatasource(cmdline, id string) string {
	line := strings.TrimRight(strings.SplitN(cmdline, "\n", 2)[0], " \r\t")
	var kept []string
	for _, f := range strings.Fields(line) {
		if !strings.HasPrefix(f, "ds=nocloud") {
			kept = append(kept, f)
		}
	}
	kept = append(kept, "ds=nocloud;i="+id)
	return strings.Join(kept, " ") + "\n"
}

// WPASupplicant is the legacy first-boot Wi-Fi file (Raspberry Pi OS Bullseye
// and older copy it from the boot partition). The SSID is hex-encoded so any
// bytes are safe, and the PSK is stored as the derived 256-bit key, not the passphrase.
func WPASupplicant(w *profile.WiFi) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev\nupdate_config=1\ncountry=%s\n\n", w.Country)
	b.WriteString("network={\n")
	fmt.Fprintf(&b, "\tssid=%s\n", hex.EncodeToString([]byte(w.SSID)))
	if w.Hidden {
		b.WriteString("\tscan_ssid=1\n")
	}
	fmt.Fprintf(&b, "\tpsk=%s\n", WPAPSK(w.SSID, w.PSK))
	b.WriteString("}\n")
	return []byte(b.String())
}

// WPAPSK derives the WPA2 pre-shared key (IEEE 802.11i: PBKDF2-SHA1, 4096 rounds).
func WPAPSK(ssid, passphrase string) string {
	key, err := pbkdf2.Key(sha1.New, passphrase, []byte(ssid), 4096, 32)
	if err != nil {
		// Only possible for invalid parameters, which are constants here.
		panic(err)
	}
	return hex.EncodeToString(key)
}
