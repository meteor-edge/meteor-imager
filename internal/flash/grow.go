package flash

import (
	"path/filepath"
	"strconv"
	"unicode"
)

// partitionNode is the kernel name of an MBR slot: /dev/sdb2, /dev/mmcblk0p2.
func partitionNode(disk string, index int) string {
	base := filepath.Base(disk)
	sep := ""
	if base != "" && unicode.IsDigit(rune(base[len(base)-1])) {
		sep = "p"
	}
	return disk + sep + strconv.Itoa(index)
}
