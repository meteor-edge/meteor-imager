package bootfs

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/soypat/fat"
	"gopkg.in/yaml.v3"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
	"github.com/meteor-edge/meteor-imager/internal/profile"
	"github.com/meteor-edge/meteor-imager/internal/shacrypt"
)

const (
	bootStart   = 8192   // sectors, as Raspberry Pi OS lays out its images
	bootSectors = 131072 // 64 MiB, enough for FAT32 with 512-byte clusters
	rootSectors = 8192
)

// stockUserData is what Raspberry Pi OS Bookworm (2024-11 and later) ships.
const stockUserData = `#cloud-config
# This is the user-data configuration file for cloud-init. By default this sets
# up an initial user called "pi" with password "raspberry".
manage_resolv_conf: false
users:
  - name: pi
    groups: users,adm,dialout,audio,netdev,video,plugdev,cdrom,games,input,gpio,spi,i2c,render,sudo
    shell: /bin/bash
    lock_passwd: true
`

const stockCmdline = "console=serial0,115200 console=tty1 root=PARTUUID=4e639091-02 rootfstype=ext4 fsck.repair=yes rootwait quiet init=/usr/lib/raspberrypi-sys-mods/firstboot\n"

// syntheticDisk builds a Pi-OS-shaped image: an MBR with a FAT32 boot partition
// holding files, then a Linux partition filled with a marker that must survive.
func syntheticDisk(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "disk.img")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total := int64(bootStart+bootSectors+rootSectors) * diskimage.SectorSize
	if err := f.Truncate(total); err != nil {
		t.Fatal(err)
	}
	mbr := make([]byte, 512)
	entry := func(i int, typ byte, start, count uint32) {
		e := mbr[446+16*i:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
	}
	entry(0, 0x0c, bootStart, bootSectors)
	entry(1, 0x83, bootStart+bootSectors, rootSectors)
	mbr[510], mbr[511] = 0x55, 0xAA
	if _, err := f.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte(strings.Repeat("ROOTFS", 1000)), int64(bootStart+bootSectors)*512); err != nil {
		t.Fatal(err)
	}

	dev := &partitionDevice{disk: f, offset: bootStart * 512, size: bootSectors * 512}
	var fm fat.Formatter
	if err := fm.Format(dev, 512, bootSectors, fat.FormatParams{Label: "bootfs", Format: fat.FormatFAT32}); err != nil {
		t.Fatalf("format: %v", err)
	}
	v, err := OpenBoot(f)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := v.WriteFile(name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func injectInto(t *testing.T, path string, p *profile.FlashProfile) *Result {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := OpenBoot(f)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Inject(v, p, Options{Now: func() time.Time { return time.UnixMilli(1700000000000) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	return res
}

func readBack(t *testing.T, path, name string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := OpenBoot(readOnly{f})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	data, err := v.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

type readOnly struct{ *os.File }

func (readOnly) WriteAt([]byte, int64) (int, error) { return 0, os.ErrPermission }

func fullProfile() *profile.FlashProfile {
	host, tz := "pi-kitchen", "Europe/Berlin"
	return &profile.FlashProfile{
		SchemaVersion: 1,
		WiFi:          &profile.WiFi{SSID: `Cafe "Wi-Fi": yes`, PSK: "p#ss: word", Country: "DE"},
		Enrollment:    &profile.Enrollment{MeteorDomain: "fleet.example.com", MeteorAPIKey: "key_abc123"},
		Hostname:      &host,
		Timezone:      &tz,
	}
}

func fsck(t *testing.T, path string) {
	t.Helper()
	bin, err := exec.LookPath("fsck.vfat")
	if err != nil {
		t.Log("fsck.vfat not installed; skipping the filesystem check")
		return
	}
	out, err := exec.Command(bin, "-n", "--offset", "4194304", path).CombinedOutput()
	if err != nil {
		// Older dosfstools have no --offset: check a copy of the partition.
		part := filepath.Join(t.TempDir(), "boot.img")
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(part, data[bootStart*512:(bootStart+bootSectors)*512], 0o600); werr != nil {
			t.Fatal(werr)
		}
		out, err = exec.Command(bin, "-n", part).CombinedOutput()
	}
	if err != nil {
		t.Fatalf("fsck.vfat: %v\n%s", err, out)
	}
}

func TestInjectCloudInitImage(t *testing.T) {
	path := syntheticDisk(t, map[string]string{
		UserDataFile: stockUserData,
		MetaDataFile: "instance-id: stock\n",
		CmdlineFile:  stockCmdline,
	})
	res := injectInto(t, path, fullProfile())
	if !res.CloudInit || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v", res)
	}

	if got := readBack(t, path, MeteorEnvFile); got != "METEOR_DOMAIN=fleet.example.com\nMETEOR_API_KEY=key_abc123\n" {
		t.Errorf(".meteorenv = %q", got)
	}

	userData := readBack(t, path, UserDataFile)
	if !strings.HasPrefix(userData, "#cloud-config\n") {
		t.Errorf("user-data must start with #cloud-config:\n%s", userData)
	}
	var ud map[string]any
	if err := yaml.Unmarshal([]byte(userData), &ud); err != nil {
		t.Fatal(err)
	}
	if ud["hostname"] != "pi-kitchen" || ud["manage_etc_hosts"] != true || ud["timezone"] != "Europe/Berlin" {
		t.Errorf("user-data settings: %v", ud)
	}
	if ud["manage_resolv_conf"] != false {
		t.Errorf("manage_resolv_conf = %v", ud["manage_resolv_conf"])
	}
	if users, _ := ud["users"].([]any); len(users) != 1 {
		t.Errorf("stock users lost: %v", ud["users"])
	}
	if pkgs := ud["packages"].([]any); len(pkgs) != 2 {
		t.Errorf("packages = %v", pkgs)
	}
	if cmds := ud["runcmd"].([]any); len(cmds) != 1 || cmds[0] != FirstrunCommand {
		t.Errorf("runcmd = %v", cmds)
	}
	if got := readBack(t, path, FirstrunFile); got != string(FirstrunScript) {
		t.Errorf("%s on the card differs from the embedded script", FirstrunFile)
	}
	if !strings.Contains(userData, "This is the user-data configuration file") {
		t.Error("comments from the stock file were dropped")
	}

	var nc struct {
		Network struct {
			Version   int
			Ethernets map[string]map[string]any
			Wifis     map[string]struct {
				Dhcp4            bool
				RegulatoryDomain string                    `yaml:"regulatory-domain"`
				AccessPoints     map[string]map[string]any `yaml:"access-points"`
				Optional         bool
			}
		}
	}
	if err := yaml.Unmarshal([]byte(readBack(t, path, NetworkConfigFile)), &nc); err != nil {
		t.Fatal(err)
	}
	wlan := nc.Network.Wifis["wlan0"]
	if nc.Network.Version != 2 || nc.Network.Ethernets["eth0"]["dhcp4"] != true {
		t.Errorf("network-config = %+v", nc)
	}
	if !wlan.Dhcp4 || !wlan.Optional || wlan.RegulatoryDomain != "DE" {
		t.Errorf("wlan0 = %+v", wlan)
	}
	ap, ok := wlan.AccessPoints[`Cafe "Wi-Fi": yes`]
	if !ok || ap["password"] != "p#ss: word" || ap["hidden"] != nil {
		t.Errorf("access point = %v", wlan.AccessPoints)
	}

	if got := readBack(t, path, MetaDataFile); got != "instance-id: meteor-imager-1700000000000\n" {
		t.Errorf("meta-data = %q", got)
	}
	if got := readBack(t, path, CmdlineFile); got != strings.TrimSpace(stockCmdline)+" ds=nocloud;i=meteor-imager-1700000000000\n" {
		t.Errorf("cmdline.txt = %q", got)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data[(bootStart+bootSectors)*512:]), strings.Repeat("ROOTFS", 1000)) {
		t.Error("root partition was modified")
	}
	fsck(t, path)
}

func TestInjectIsIdempotent(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData, CmdlineFile: stockCmdline})
	injectInto(t, path, fullProfile())
	injectInto(t, path, fullProfile())

	var ud map[string]any
	if err := yaml.Unmarshal([]byte(readBack(t, path, UserDataFile)), &ud); err != nil {
		t.Fatal(err)
	}
	if len(ud["runcmd"].([]any)) != 1 || len(ud["packages"].([]any)) != 2 {
		t.Errorf("second run duplicated entries: %v", ud)
	}
	if got := readBack(t, path, CmdlineFile); strings.Count(got, "ds=nocloud") != 1 || strings.Count(got, "\n") != 1 {
		t.Errorf("cmdline.txt = %q", got)
	}
	fsck(t, path)
}

func TestInjectReplacesLegacyFirstbootCommand(t *testing.T) {
	legacy := stockUserData + "runcmd:\n  - echo keep-me\n  - 'for i in $(seq 1 30); do curl -fsSL https://raw.githubusercontent.com/meteor-edge/meteor-cloud/master/src/device-plane/agent/packaging/firstboot/install-meteorcli-firstboot.sh -o /run/meteor-firstboot.sh; done'\n"
	path := syntheticDisk(t, map[string]string{UserDataFile: legacy, CmdlineFile: stockCmdline})
	injectInto(t, path, fullProfile())
	var ud map[string]any
	if err := yaml.Unmarshal([]byte(readBack(t, path, UserDataFile)), &ud); err != nil {
		t.Fatal(err)
	}
	cmds := ud["runcmd"].([]any)
	if len(cmds) != 2 || cmds[0] != "echo keep-me" || cmds[1] != FirstrunCommand {
		t.Errorf("runcmd = %v", cmds)
	}
	fsck(t, path)
}

func TestInjectWithoutEnrollmentWritesNoFirstrun(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData, CmdlineFile: stockCmdline})
	p := fullProfile()
	p.Enrollment = nil
	injectInto(t, path, p)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := OpenBoot(readOnly{f})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Exists(FirstrunFile) {
		t.Errorf("%s written without enrollment", FirstrunFile)
	}
	if strings.Contains(readBack(t, path, UserDataFile), FirstrunFile) {
		t.Error("runcmd added without enrollment")
	}
}

func TestInjectHiddenNetworkAndEmptyUserData(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: ""})
	p := &profile.FlashProfile{SchemaVersion: 1, WiFi: &profile.WiFi{SSID: "lab", PSK: "12345678", Hidden: true, Country: "GB"}}
	res := injectInto(t, path, p)
	if strings.Contains(strings.Join(res.Written, ","), MeteorEnvFile) {
		t.Error(".meteorenv written without enrollment")
	}
	userData := readBack(t, path, UserDataFile)
	if userData != "#cloud-config\nmanage_resolv_conf: false\n" {
		t.Errorf("user-data = %q", userData)
	}
	nc := readBack(t, path, NetworkConfigFile)
	if !strings.Contains(nc, "hidden: true") || !strings.Contains(nc, `password: "12345678"`) {
		t.Errorf("network-config:\n%s", nc)
	}
}

func TestInjectLegacyImage(t *testing.T) {
	path := syntheticDisk(t, map[string]string{CmdlineFile: stockCmdline})
	res := injectInto(t, path, fullProfile())
	if res.CloudInit || len(res.Warnings) != 2 {
		t.Fatalf("result = %+v", res)
	}
	wpa := readBack(t, path, WPASupplicantFile)
	if !strings.Contains(wpa, "country=DE") || !strings.Contains(wpa, "ssid=43616665") || strings.Contains(wpa, "p#ss") {
		t.Errorf("wpa_supplicant.conf:\n%s", wpa)
	}
	if got := readBack(t, path, CmdlineFile); got != stockCmdline {
		t.Errorf("cmdline.txt changed on a legacy image: %q", got)
	}
	fsck(t, path)
}

func TestWPAPSKKnownVector(t *testing.T) {
	// IEEE 802.11i-2004 Annex H.4.1 test vector.
	if got := WPAPSK("IEEE", "password"); got != "f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e" {
		t.Errorf("WPAPSK = %s", got)
	}
}

func TestInjectRejectsInvalidProfile(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData})
	f, _ := os.OpenFile(path, os.O_RDWR, 0)
	defer f.Close()
	v, err := OpenBoot(f)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	bad := &profile.FlashProfile{SchemaVersion: 1, WiFi: &profile.WiFi{SSID: "x", PSK: "short", Country: "DE"}}
	if _, err := Inject(v, bad, Options{}); err == nil {
		t.Fatal("expected a validation error")
	}
}

func TestOpenBootWithoutPartitionTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.img")
	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_RDWR, 0)
	defer f.Close()
	if _, err := OpenBoot(f); err == nil {
		t.Fatal("expected an error")
	}
}

// Real Pi OS boot partitions have far more than 65535 clusters, so new files
// start above cluster 65535 and need the high word of the start cluster.
func TestInjectAboveCluster65535(t *testing.T) {
	path := syntheticDisk(t, map[string]string{
		"filler.bin": strings.Repeat("F", 34<<20), // 34 MiB of 512-byte clusters
		UserDataFile: stockUserData,
		CmdlineFile:  stockCmdline,
	})
	injectInto(t, path, fullProfile())
	if got := readBack(t, path, MeteorEnvFile); !strings.Contains(got, "METEOR_API_KEY=key_abc123") {
		t.Errorf(".meteorenv = %q", got)
	}
	if got := readBack(t, path, UserDataFile); !strings.Contains(got, FirstrunCommand) {
		t.Errorf("user-data = %q", got)
	}
	if got := readBack(t, path, FirstrunFile); got != string(FirstrunScript) {
		t.Errorf("%s above cluster 65535 differs from the embedded script", FirstrunFile)
	}
	fsck(t, path)
}

const testSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyOnlyForTests user@laptop"

func v2Profile() *profile.FlashProfile {
	p := fullProfile()
	p.SchemaVersion = 2
	p.Enrollment.DeviceName = "Kitchen Pi"
	p.SSH = &profile.SSH{AuthorizedKeys: []string{testSSHKey}, User: "pi"}
	return p
}

func TestInjectBaseImageWritesOnlyMeteorenvV2(t *testing.T) {
	path := syntheticDisk(t, map[string]string{
		BaseImageMarker: "meteor_settings=2\n",
		UserDataFile:    stockUserData,
		CmdlineFile:     stockCmdline,
	})
	res := injectInto(t, path, v2Profile())

	if !res.BaseImage || len(res.Written) != 1 || res.Written[0] != MeteorEnvFile {
		t.Fatalf("result = %+v", res)
	}
	want := "METEOR_ENV_VERSION=2\n" +
		"METEOR_DOMAIN=fleet.example.com\n" +
		"METEOR_API_KEY=key_abc123\n" +
		"METEOR_NAME=Kitchen Pi\n" +
		"METEOR_HOSTNAME=pi-kitchen\n" +
		"METEOR_TIMEZONE=Europe/Berlin\n" +
		"METEOR_WIFI_SSID=Cafe \"Wi-Fi\": yes\n" +
		"METEOR_WIFI_PSK=p#ss: word\n" +
		"METEOR_WIFI_COUNTRY=DE\n" +
		"METEOR_WIFI_HIDDEN=false\n" +
		"METEOR_SSH_AUTHORIZED_KEY=" + testSSHKey + "\n" +
		"METEOR_SSH_USER=pi\n"
	if got := readBack(t, path, MeteorEnvFile); got != want {
		t.Errorf(".meteorenv:\n%s\nwant:\n%s", got, want)
	}
	// cloud-init must not run a second enrollment on a base image.
	if got := readBack(t, path, UserDataFile); got != stockUserData {
		t.Errorf("user-data changed:\n%s", got)
	}
	if got := readBack(t, path, CmdlineFile); got != stockCmdline {
		t.Errorf("cmdline.txt changed: %q", got)
	}
	fsck(t, path)
}

func TestInjectStockImageGivesTheSSHKeyToCloudInit(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData, CmdlineFile: stockCmdline})
	res := injectInto(t, path, v2Profile())

	if res.BaseImage || !res.CloudInit {
		t.Fatalf("result = %+v", res)
	}
	env := readBack(t, path, MeteorEnvFile)
	if env != "METEOR_DOMAIN=fleet.example.com\nMETEOR_API_KEY=key_abc123\nMETEOR_NAME=Kitchen Pi\n" {
		t.Errorf(".meteorenv = %q", env)
	}
	var ud struct {
		Keys   []string `yaml:"ssh_authorized_keys"`
		RunCmd []string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(readBack(t, path, UserDataFile)), &ud); err != nil {
		t.Fatal(err)
	}
	if len(ud.Keys) != 1 || ud.Keys[0] != testSSHKey || !slices.Contains(ud.RunCmd, sshEnableCommand) {
		t.Errorf("user-data = %+v", ud)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "No user set") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

const testPassword = "correct horse battery"

func v3Profile() *profile.FlashProfile {
	p := v2Profile()
	p.SchemaVersion = 3
	on, off := true, false
	p.User = &profile.User{Name: "ops", Password: testPassword}
	p.SSH = &profile.SSH{Enabled: &on, PasswordAuthentication: &off, AuthorizedKeys: []string{testSSHKey}}
	return p
}

func checkHash(t *testing.T, hash string) {
	t.Helper()
	if ok, err := shacrypt.Verify(testPassword, hash); err != nil || !ok {
		t.Errorf("password hash %q does not verify (%v)", hash, err)
	}
}

func TestInjectBaseImageUserAndSSH(t *testing.T) {
	path := syntheticDisk(t, map[string]string{BaseImageMarker: "meteor_settings=2\n", CmdlineFile: stockCmdline})
	injectInto(t, path, v3Profile())

	env := readBack(t, path, MeteorEnvFile)
	if strings.Contains(env, testPassword) {
		t.Fatal("plain-text password on the card")
	}
	vals := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
		k, v, _ := strings.Cut(line, "=")
		vals[k] = v
	}
	checkHash(t, vals["METEOR_USER_PASSWORD_HASH"])
	for k, want := range map[string]string{
		"METEOR_USER":               "ops",
		"METEOR_SSH_ENABLED":        "true",
		"METEOR_SSH_PASSWORD_AUTH":  "false",
		"METEOR_SSH_AUTHORIZED_KEY": testSSHKey,
	} {
		if vals[k] != want {
			t.Errorf("%s = %q, want %q", k, vals[k], want)
		}
	}
	if _, ok := vals["METEOR_SSH_USER"]; ok {
		t.Error("METEOR_SSH_USER written although the key goes to the user")
	}
}

func TestInjectCloudInitUserAndSSH(t *testing.T) {
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData, CmdlineFile: stockCmdline})
	injectInto(t, path, v3Profile())
	injectInto(t, path, v3Profile()) // a second write must not add the user twice

	raw := readBack(t, path, UserDataFile)
	if strings.Contains(raw, testPassword) {
		t.Fatal("plain-text password on the card")
	}
	var ud struct {
		Users []struct {
			Name       string   `yaml:"name"`
			Groups     string   `yaml:"groups"`
			Sudo       string   `yaml:"sudo"`
			LockPasswd *bool    `yaml:"lock_passwd"`
			Passwd     string   `yaml:"passwd"`
			Keys       []string `yaml:"ssh_authorized_keys"`
		} `yaml:"users"`
		PwAuth *bool    `yaml:"ssh_pwauth"`
		Keys   []string `yaml:"ssh_authorized_keys"`
		RunCmd []string `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(raw), &ud); err != nil {
		t.Fatal(err)
	}
	if len(ud.Users) != 2 || ud.Users[0].Name != "pi" {
		t.Fatalf("users = %+v", ud.Users)
	}
	u := ud.Users[1]
	if u.Name != "ops" || !strings.Contains(u.Groups, "sudo") || u.Sudo == "" || u.LockPasswd == nil || *u.LockPasswd {
		t.Errorf("user = %+v", u)
	}
	checkHash(t, u.Passwd)
	if len(u.Keys) != 1 || u.Keys[0] != testSSHKey || len(ud.Keys) != 0 {
		t.Errorf("key placement: user %v, top level %v", u.Keys, ud.Keys)
	}
	if ud.PwAuth == nil || *ud.PwAuth {
		t.Errorf("ssh_pwauth = %v", ud.PwAuth)
	}
	if n := countOf(ud.RunCmd, sshEnableCommand); n != 1 {
		t.Errorf("runcmd has %d ssh enable entries: %v", n, ud.RunCmd)
	}
	fsck(t, path)
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

func TestInjectLegacyImageUserAndSSH(t *testing.T) {
	path := syntheticDisk(t, map[string]string{CmdlineFile: stockCmdline})
	p := v3Profile()
	p.Enrollment = nil
	res := injectInto(t, path, p)

	name, hash, _ := strings.Cut(strings.TrimSpace(readBack(t, path, UserconfFile)), ":")
	if name != "ops" {
		t.Errorf("userconf.txt user = %q", name)
	}
	checkHash(t, hash)
	if got := readBack(t, path, SSHFile); got != "" {
		t.Errorf("ssh file = %q", got)
	}
	joined := strings.Join(res.Warnings, " ")
	if !strings.Contains(joined, "SSH key was not written") || !strings.Contains(joined, "password logins stay allowed") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	fsck(t, path)
}

// configDisk is syntheticDisk plus a third partition: FAT32 labelled METEORCFG.
func configDisk(t *testing.T) string {
	t.Helper()
	path := syntheticDisk(t, map[string]string{UserDataFile: stockUserData})
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfgStart := uint32(bootStart + bootSectors + rootSectors)
	if err := f.Truncate(int64(cfgStart+bootSectors) * 512); err != nil {
		t.Fatal(err)
	}
	mbr := make([]byte, 512)
	if _, err := f.ReadAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	e := mbr[446+16*2:]
	e[4] = 0x0c
	binary.LittleEndian.PutUint32(e[8:], cfgStart)
	binary.LittleEndian.PutUint32(e[12:], bootSectors)
	if _, err := f.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	dev := &partitionDevice{disk: f, offset: int64(cfgStart) * 512, size: bootSectors * 512}
	var fm fat.Formatter
	if err := fm.Format(dev, 512, bootSectors, fat.FormatParams{Label: ConfigLabel, Format: fat.FormatFAT32}); err != nil {
		t.Fatalf("format: %v", err)
	}
	// The formatter leaves "NO NAME"; set the boot-sector label as mkfs.vfat -n does.
	if _, err := f.WriteAt([]byte("METEORCFG  "), int64(cfgStart)*512+71); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenBootPrefersTheMeteorConfigPartition(t *testing.T) {
	path := configDisk(t)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := OpenBoot(f)
	if err != nil {
		t.Fatal(err)
	}
	if v.Label != ConfigLabel || v.Partition.Index != 3 {
		t.Errorf("opened partition %d labelled %q", v.Partition.Index, v.Label)
	}
	if v.Exists(UserDataFile) {
		t.Error("expected the empty config partition, not the boot partition")
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
}
