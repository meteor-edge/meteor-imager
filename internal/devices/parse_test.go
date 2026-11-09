package devices

import (
	"strings"
	"testing"
)

const lsblkJSON = `{"blockdevices":[
 {"name":"nvme0n1","path":"/dev/nvme0n1","size":512110190592,"model":"Samsung SSD","vendor":null,"tran":"nvme","rm":false,"hotplug":false,"ro":false,"type":"disk","mountpoint":null,
  "children":[{"name":"nvme0n1p1","path":"/dev/nvme0n1p1","size":536870912,"type":"part","rm":false,"hotplug":false,"ro":false,"mountpoint":"/boot/efi"},
              {"name":"nvme0n1p2","path":"/dev/nvme0n1p2","size":511571771392,"type":"part","rm":false,"hotplug":false,"ro":false,"mountpoint":"/"}]},
 {"name":"sdb","path":"/dev/sdb","size":"31914983424","model":"SD/MMC        ","vendor":"Generic ","tran":"usb","rm":"1","hotplug":"1","ro":"0","type":"disk","mountpoint":null,
  "children":[{"name":"sdb1","path":"/dev/sdb1","size":536870912,"type":"part","mountpoint":"/media/me/bootfs"}]},
 {"name":"mmcblk0","path":"/dev/mmcblk0","size":15931539456,"model":null,"tran":null,"rm":false,"hotplug":false,"ro":false,"type":"disk","mountpoint":null},
 {"name":"loop0","path":"/dev/loop0","size":4096,"type":"loop","rm":false,"hotplug":false,"ro":true,"mountpoint":"/snap/x"},
 {"name":"zram0","path":"/dev/zram0","size":4096,"type":"disk","rm":false,"hotplug":false,"ro":false,"mountpoint":"[SWAP]"}
]}`

func TestParseLsblk(t *testing.T) {
	devs, err := parseLsblk([]byte(lsblkJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("got %d devices: %+v", len(devs), devs)
	}
	nvme, sd, mmc := devs[0], devs[1], devs[2]
	if !nvme.System || nvme.Safe() {
		t.Errorf("system disk offered: %+v", nvme)
	}
	if !sd.Safe() || sd.SizeBytes != 31914983424 || sd.Model != "Generic SD/MMC" || sd.Mountpoints[0] != "/media/me/bootfs" {
		t.Errorf("usb reader: %+v", sd)
	}
	if !mmc.Safe() {
		t.Errorf("built-in SD reader not offered: %+v", mmc)
	}
}

func TestParseWindowsDisks(t *testing.T) {
	out := `[{"Number":0,"FriendlyName":"NVMe Disk","Size":512110190592,"BusType":"NVMe","IsBoot":true,"IsSystem":true,"IsReadOnly":false,"Letters":"C"},
	         {"Number":2,"FriendlyName":"Generic SD Card","Size":31914983424,"BusType":7,"IsBoot":false,"IsSystem":false,"IsReadOnly":false,"Letters":"E F"}]`
	devs, err := parseWindowsDisks([]byte(out))
	if err != nil || len(devs) != 2 {
		t.Fatalf("%+v %v", devs, err)
	}
	if devs[0].Safe() {
		t.Errorf("system disk offered: %+v", devs[0])
	}
	sd := devs[1]
	if !sd.Safe() || sd.Path != `\\.\PhysicalDrive2` || sd.Bus != "USB" || strings.Join(sd.Mountpoints, ",") != `E:\,F:\` {
		t.Errorf("sd: %+v", sd)
	}
	one, err := parseWindowsDisks([]byte(`{"Number":1,"FriendlyName":"X","Size":"100","BusType":"SD"}`))
	if err != nil || len(one) != 1 || !one[0].Removable || one[0].SizeBytes != 100 {
		t.Errorf("single object: %+v %v", one, err)
	}
}

const diskutilList = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>AllDisks</key><array><string>disk0</string><string>disk0s1</string><string>disk4</string></array>
<key>WholeDisks</key><array><string>disk0</string><string>disk4</string></array>
</dict></plist>`

const diskutilInfoSD = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>BusProtocol</key><string>USB</string>
<key>DeviceIdentifier</key><string>disk4</string>
<key>Ejectable</key><true/>
<key>Internal</key><false/>
<key>MediaName</key><string>Apple SDXC Reader Media</string>
<key>Removable</key><true/>
<key>Size</key><integer>31914983424</integer>
<key>WritableMedia</key><true/>
</dict></plist>`

const diskutilInfoInternal = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>BusProtocol</key><string>Apple Fabric</string>
<key>DeviceIdentifier</key><string>disk0</string>
<key>Ejectable</key><false/>
<key>Internal</key><true/>
<key>MediaName</key><string>APPLE SSD</string>
<key>Size</key><integer>500277790720</integer>
<key>WritableMedia</key><true/>
</dict></plist>`

func TestParseDiskutil(t *testing.T) {
	ids, err := parseDiskutilList(strings.NewReader(diskutilList))
	if err != nil || strings.Join(ids, ",") != "disk0,disk4" {
		t.Fatalf("%v %v", ids, err)
	}
	sd, err := parseDiskutilInfo(strings.NewReader(diskutilInfoSD))
	if err != nil || !sd.Safe() || sd.Path != "/dev/rdisk4" || sd.SizeBytes != 31914983424 {
		t.Errorf("sd: %+v %v", sd, err)
	}
	internal, err := parseDiskutilInfo(strings.NewReader(diskutilInfoInternal))
	if err != nil || internal.Safe() || !internal.System {
		t.Errorf("internal: %+v %v", internal, err)
	}
}

func TestHumanSize(t *testing.T) {
	if got := HumanSize(31914983424); got != "31.9 GB" {
		t.Errorf("HumanSize = %s", got)
	}
}
