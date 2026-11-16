package diskimage

import (
	"bytes"
	"encoding/binary"
	"math"
)

// RootAlign is the alignment of a grown root partition: 4 MiB, the same unit
// backup uses when it shrinks.
const RootAlign = 4 << 20

// GrowLinuxRoot extends the last MBR Linux partition (type 83) in the first
// sector so it ends on a 4 MiB boundary at the end of a card of capacity bytes.
// The filesystem is not resized. GPT, a non-Linux last partition, and a card
// that is not larger leave head unchanged and ok false.
func GrowLinuxRoot(head []byte, capacity int64) (index int, sectors uint32, ok bool) {
	if capacity < SectorSize || len(head) < SectorSize {
		return 0, 0, false
	}
	parts, err := ReadPartitions(bytes.NewReader(head))
	if err != nil || len(parts) == 0 {
		return 0, 0, false
	}
	last := parts[0]
	for _, p := range parts[1:] {
		if p.Offset() > last.Offset() {
			last = p
		}
	}
	// MBR type bytes are two hex digits. GPT type GUIDs are left as they are:
	// Raspberry Pi OS root is type 83, and moving a GPT entry needs a new CRC.
	if len(last.Type) != 2 || last.Type != "83" || last.Index < 1 || last.Index > 4 {
		return 0, 0, false
	}
	align := uint64(RootAlign / SectorSize)
	cardSectors := uint64(capacity / SectorSize)
	end := cardSectors / align * align
	if end <= last.StartSector+last.Sectors {
		return 0, 0, false
	}
	count := end - last.StartSector
	if count > math.MaxUint32 {
		return 0, 0, false
	}
	binary.LittleEndian.PutUint32(head[446+16*(last.Index-1)+12:], uint32(count))
	return last.Index, uint32(count), true
}
