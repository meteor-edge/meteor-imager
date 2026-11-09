// Package diskimage reads disk images: partition tables and compressed sources.
package diskimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// SectorSize is the logical sector size of Raspberry Pi images and SD cards.
const SectorSize = 512

// Partition is one entry of an MBR or GPT partition table.
type Partition struct {
	Index       int    // 1-based, as the OS numbers it
	StartSector uint64 // in 512-byte sectors
	Sectors     uint64
	// Type is the MBR type byte as hex ("0c") or the GPT type GUID.
	Type string
	// MaybeFAT is true for MBR types and GPT GUIDs that FAT volumes use. The
	// filesystem itself is still checked when it is opened.
	MaybeFAT bool
}

// Offset returns the partition start in bytes.
func (p Partition) Offset() int64 { return int64(p.StartSector) * SectorSize }

// Size returns the partition length in bytes.
func (p Partition) Size() int64 { return int64(p.Sectors) * SectorSize }

var (
	// ErrNoPartitionTable means the first sector has no MBR signature.
	ErrNoPartitionTable = errors.New("not a disk image: no MBR or GPT partition table")

	fatMBRTypes = map[byte]bool{0x01: true, 0x04: true, 0x06: true, 0x0b: true, 0x0c: true, 0x0e: true, 0xef: true}
	// GPT type GUIDs as stored on disk (mixed endian).
	gptBasicData = guid("EBD0A0A2-B9E5-4433-87C0-68B6B72699C7")
	gptESP       = guid("C12A7328-F81F-11D2-BA4B-00A0C93EC93B")
)

// ReadPartitions parses the MBR, following a protective MBR into the GPT.
func ReadPartitions(r io.ReaderAt) ([]Partition, error) {
	mbr := make([]byte, SectorSize)
	if _, err := r.ReadAt(mbr, 0); err != nil {
		return nil, fmt.Errorf("read first sector: %w", err)
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, ErrNoPartitionTable
	}
	var parts []Partition
	for i := 0; i < 4; i++ {
		e := mbr[446+16*i : 446+16*(i+1)]
		typ := e[4]
		start := uint64(binary.LittleEndian.Uint32(e[8:12]))
		count := uint64(binary.LittleEndian.Uint32(e[12:16]))
		if typ == 0xee {
			return readGPT(r)
		}
		if typ == 0 || count == 0 {
			continue
		}
		parts = append(parts, Partition{
			Index: i + 1, StartSector: start, Sectors: count,
			Type: fmt.Sprintf("%02x", typ), MaybeFAT: fatMBRTypes[typ],
		})
	}
	if len(parts) == 0 {
		return nil, ErrNoPartitionTable
	}
	return parts, nil
}

func readGPT(r io.ReaderAt) ([]Partition, error) {
	hdr := make([]byte, SectorSize)
	if _, err := r.ReadAt(hdr, SectorSize); err != nil {
		return nil, fmt.Errorf("read GPT header: %w", err)
	}
	if string(hdr[0:8]) != "EFI PART" {
		return nil, errors.New("protective MBR without a GPT header")
	}
	entriesLBA := binary.LittleEndian.Uint64(hdr[72:80])
	count := binary.LittleEndian.Uint32(hdr[80:84])
	size := binary.LittleEndian.Uint32(hdr[84:88])
	if size < 128 || count == 0 || count > 1024 {
		return nil, errors.New("GPT header has an unsupported entry layout")
	}
	table := make([]byte, int(count)*int(size))
	if _, err := r.ReadAt(table, int64(entriesLBA)*SectorSize); err != nil {
		return nil, fmt.Errorf("read GPT entries: %w", err)
	}
	var parts []Partition
	for i := 0; i < int(count); i++ {
		e := table[i*int(size) : (i+1)*int(size)]
		typ := e[0:16]
		if bytes.Equal(typ, make([]byte, 16)) {
			continue
		}
		first := binary.LittleEndian.Uint64(e[32:40])
		last := binary.LittleEndian.Uint64(e[40:48])
		if last < first {
			continue
		}
		parts = append(parts, Partition{
			Index: i + 1, StartSector: first, Sectors: last - first + 1,
			Type: guidString(typ), MaybeFAT: bytes.Equal(typ, gptBasicData) || bytes.Equal(typ, gptESP),
		})
	}
	if len(parts) == 0 {
		return nil, ErrNoPartitionTable
	}
	return parts, nil
}

// guid converts a textual GUID to its on-disk byte order.
func guid(s string) []byte {
	var hex []byte
	for _, c := range []byte(s) {
		if c != '-' {
			hex = append(hex, c)
		}
	}
	raw := make([]byte, 16)
	for i := 0; i < 16; i++ {
		fmt.Sscanf(string(hex[2*i:2*i+2]), "%02x", &raw[i])
	}
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = raw[3], raw[2], raw[1], raw[0]
	out[4], out[5] = raw[5], raw[4]
	out[6], out[7] = raw[7], raw[6]
	copy(out[8:], raw[8:])
	return out
}

func guidString(b []byte) string {
	return fmt.Sprintf("%08X-%04X-%04X-%X-%X",
		binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint16(b[4:6]),
		binary.LittleEndian.Uint16(b[6:8]), b[8:10], b[10:16])
}
