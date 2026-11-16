package flash

import "testing"

func TestPartitionNode(t *testing.T) {
	cases := map[string]string{
		"/dev/sdb":     "/dev/sdb2",
		"/dev/mmcblk0": "/dev/mmcblk0p2",
		"/dev/nvme0n1": "/dev/nvme0n1p2",
		"/dev/loop0":   "/dev/loop0p2",
	}
	for disk, want := range cases {
		if got := partitionNode(disk, 2); got != want {
			t.Errorf("%s -> %s, want %s", disk, got, want)
		}
	}
}
