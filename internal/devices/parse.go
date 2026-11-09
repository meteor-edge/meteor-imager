package devices

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ---- Linux: lsblk -J -b -o NAME,PATH,SIZE,MODEL,VENDOR,TRAN,RM,HOTPLUG,RO,TYPE,MOUNTPOINT

// flexBool accepts lsblk's true/false, "1"/"0" and 1/0 across versions.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(s == "true" || s == "1")
	return nil
}

// flexInt accepts numbers and numeric strings.
type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	*n = flexInt(v)
	return err
}

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        flexInt    `json:"size"`
	Model       *string    `json:"model"`
	Vendor      *string    `json:"vendor"`
	Tran        *string    `json:"tran"`
	RM          flexBool   `json:"rm"`
	Hotplug     flexBool   `json:"hotplug"`
	RO          flexBool   `json:"ro"`
	Type        string     `json:"type"`
	Mountpoint  *string    `json:"mountpoint"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

func (d lsblkDev) mounts() []string {
	var out []string
	if d.Mountpoint != nil && *d.Mountpoint != "" {
		out = append(out, *d.Mountpoint)
	}
	for _, m := range d.Mountpoints {
		if m != nil && *m != "" && (d.Mountpoint == nil || *m != *d.Mountpoint) {
			out = append(out, *m)
		}
	}
	for _, c := range d.Children {
		out = append(out, c.mounts()...)
	}
	return out
}

var systemMounts = map[string]bool{
	"/": true, "/boot": true, "/boot/efi": true, "/boot/firmware": true, "/efi": true,
	"/usr": true, "/var": true, "/home": true, "/etc": true, "[SWAP]": true,
}

func parseLsblk(data []byte) ([]Device, error) {
	var out struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	var devs []Device
	for _, d := range out.Blockdevices {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") {
			continue
		}
		path := d.Path
		if path == "" {
			path = "/dev/" + d.Name
		}
		tran := str(d.Tran)
		mounts := d.mounts()
		system := false
		for _, m := range mounts {
			if systemMounts[m] {
				system = true
			}
		}
		devs = append(devs, Device{
			Path: path, Name: d.Name,
			Model:     strings.TrimSpace(strings.TrimSpace(str(d.Vendor)) + " " + strings.TrimSpace(str(d.Model))),
			SizeBytes: int64(d.Size), Bus: tran,
			// Built-in SD readers often report rm=0; mmcblk disks are SD/eMMC.
			Removable: bool(d.RM) || bool(d.Hotplug) || tran == "usb" || tran == "mmc" || strings.HasPrefix(d.Name, "mmcblk"),
			ReadOnly:  bool(d.RO), System: system, Mountpoints: mounts,
		})
	}
	return devs, nil
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- Windows: Get-Disk | ConvertTo-Json

type winDisk struct {
	Number       int     `json:"Number"`
	FriendlyName string  `json:"FriendlyName"`
	Size         flexInt `json:"Size"`
	BusType      any     `json:"BusType"`
	IsBoot       bool    `json:"IsBoot"`
	IsSystem     bool    `json:"IsSystem"`
	IsReadOnly   bool    `json:"IsReadOnly"`
	Letters      string  `json:"Letters"`
}

// Windows BusType enum values (MSFT_Disk), for PowerShell versions that emit numbers.
var winBusTypes = map[float64]string{
	1: "SCSI", 2: "ATAPI", 3: "ATA", 4: "1394", 5: "SSA", 6: "Fibre Channel", 7: "USB", 8: "RAID",
	9: "iSCSI", 10: "SAS", 11: "SATA", 12: "SD", 13: "MMC", 17: "NVMe",
}

func parseWindowsDisks(data []byte) ([]Device, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, nil
	}
	var disks []winDisk
	if data[0] == '{' {
		var one winDisk
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("parse Get-Disk: %w", err)
		}
		disks = []winDisk{one}
	} else if err := json.Unmarshal(data, &disks); err != nil {
		return nil, fmt.Errorf("parse Get-Disk: %w", err)
	}
	var devs []Device
	for _, d := range disks {
		bus := ""
		switch v := d.BusType.(type) {
		case string:
			bus = v
		case float64:
			bus = winBusTypes[v]
		}
		var mounts []string
		for _, l := range strings.Fields(d.Letters) {
			mounts = append(mounts, l+":\\")
		}
		name := fmt.Sprintf("PhysicalDrive%d", d.Number)
		devs = append(devs, Device{
			Path: `\\.\` + name, Name: name, Model: strings.TrimSpace(d.FriendlyName),
			SizeBytes: int64(d.Size), Bus: bus,
			Removable: bus == "USB" || bus == "SD" || bus == "MMC",
			ReadOnly:  d.IsReadOnly, System: d.IsBoot || d.IsSystem, Mountpoints: mounts,
		})
	}
	return devs, nil
}

// ---- macOS: diskutil list -plist / diskutil info -plist (XML property lists)

// plistValue decodes the subset of XML plists diskutil emits.
func parsePlist(r io.Reader) (any, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse plist: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local != "plist" {
			return plistElement(dec, se)
		}
	}
}

func plistElement(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		var key string
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					if err := dec.DecodeElement(&k, &t); err != nil {
						return nil, err
					}
					key = k
					continue
				}
				v, err := plistElement(dec, t)
				if err != nil {
					return nil, err
				}
				m[key] = v
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var a []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := plistElement(dec, t)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			case xml.EndElement:
				return a, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	default: // string, real, date, data
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return s, nil
	}
}

// parseDiskutilList returns WholeDisks from `diskutil list -plist`.
func parseDiskutilList(r io.Reader) ([]string, error) {
	v, err := parsePlist(r)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	arr, _ := m["WholeDisks"].([]any)
	var out []string
	for _, d := range arr {
		if s, ok := d.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// parseDiskutilInfo converts `diskutil info -plist diskN` to a Device.
func parseDiskutilInfo(r io.Reader) (Device, error) {
	v, err := parsePlist(r)
	if err != nil {
		return Device{}, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Device{}, errors.New("unexpected diskutil output")
	}
	s := func(k string) string { x, _ := m[k].(string); return x }
	b := func(k string) bool { x, _ := m[k].(bool); return x }
	n := func(k string) int64 { x, _ := m[k].(int64); return x }
	id := s("DeviceIdentifier")
	size := n("Size")
	if size == 0 {
		size = n("TotalSize")
	}
	bus := s("BusProtocol")
	internal := b("Internal")
	d := Device{
		Path: "/dev/r" + id, Name: id, Model: strings.TrimSpace(s("MediaName")),
		SizeBytes: size, Bus: bus,
		Removable: !internal || b("Removable") || b("RemovableMedia") || b("Ejectable") || bus == "Secure Digital",
		ReadOnly:  !b("WritableMedia") && m["WritableMedia"] != nil,
		// The boot disk is internal; APFS containers have no physical store here.
		System: s("MountPoint") == "/" || (internal && !b("Ejectable") && bus != "Secure Digital"),
	}
	if mp := s("MountPoint"); mp != "" {
		d.Mountpoints = []string{mp}
	}
	return d, nil
}
