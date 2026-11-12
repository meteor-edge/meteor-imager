package diskimage

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func testMBR(rootSectors uint32) []byte {
	head := make([]byte, SectorSize)
	entry := func(i int, typ byte, start, count uint32) {
		e := head[446+16*i:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
	}
	entry(0, 0x0c, 8192, 131072)
	entry(1, 0x83, 8192+131072, rootSectors)
	head[510], head[511] = 0x55, 0xAA
	return head
}

func TestGrowLinuxRootFillsTheCard(t *testing.T) {
	head := testMBR(2048)
	const card = 128 << 20
	index, sectors, ok := GrowLinuxRoot(head, card)
	if !ok || index != 2 {
		t.Fatalf("grow = %d %d %v", index, sectors, ok)
	}
	start := uint64(8192 + 131072)
	end := uint64(card/SectorSize) / uint64(RootAlign/SectorSize) * uint64(RootAlign/SectorSize)
	if uint64(sectors) != end-start {
		t.Fatalf("sectors = %d, want %d", sectors, end-start)
	}
	parts, err := ReadPartitions(bytes.NewReader(head))
	if err != nil {
		t.Fatal(err)
	}
	if parts[1].Sectors != uint64(sectors) || parts[0].Sectors != 131072 {
		t.Fatalf("partitions = %+v", parts)
	}
}

func TestGrowLinuxRootLeavesAFittingImage(t *testing.T) {
	const root = 2048
	head := testMBR(root)
	start := int64(8192+131072) * SectorSize
	if _, _, ok := GrowLinuxRoot(head, start+root*SectorSize); ok {
		t.Fatal("grew a partition that already fills the card")
	}
	if binary.LittleEndian.Uint32(head[446+16+12:]) != root {
		t.Fatal("sector count changed")
	}
}

func TestGrowLinuxRootExtends4GiBImageToLargerCards(t *testing.T) {
	// Pi-shaped layout: 256 MiB boot, root filling a 4 GiB image.
	const boot = 8192 + 131072
	const imageSectors = 4 << 30 / SectorSize
	root := uint32(imageSectors - boot)
	for _, cardGiB := range []int64{16, 32} {
		head := testMBR(root)
		card := cardGiB << 30
		index, sectors, ok := GrowLinuxRoot(head, card)
		if !ok || index != 2 {
			t.Fatalf("%d GiB card: grow = %d %d %v", cardGiB, index, sectors, ok)
		}
		end := uint64(card/SectorSize) / uint64(RootAlign/SectorSize) * uint64(RootAlign/SectorSize)
		if uint64(sectors) != end-uint64(boot) {
			t.Fatalf("%d GiB card: root sectors = %d, want %d", cardGiB, sectors, end-uint64(boot))
		}
		parts, err := ReadPartitions(bytes.NewReader(head))
		if err != nil {
			t.Fatal(err)
		}
		if parts[0].Sectors != 131072 {
			t.Fatalf("%d GiB card: boot partition changed to %d sectors", cardGiB, parts[0].Sectors)
		}
		got := int64(sectors) * SectorSize
		if got < (cardGiB-1)<<30 {
			t.Fatalf("%d GiB card: root is only %d bytes", cardGiB, got)
		}
	}
	// A card the same size as the image has no spare space.
	same := testMBR(root)
	if _, _, ok := GrowLinuxRoot(same, imageSectors*SectorSize); ok {
		t.Fatal("grew a 4 GiB image onto a 4 GiB card")
	}
}

func TestGrowLinuxRootSkipsGPT(t *testing.T) {
	head := testMBR(2048)
	head[446+4] = 0xee
	if _, _, ok := GrowLinuxRoot(head, 128<<20); ok {
		t.Fatal("grew a protective MBR")
	}
}
