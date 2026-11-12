package flash

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

// A small Pi-shaped image: FAT boot, a second FAT partition we also use
// (settings), and an ext4 root. Writing it to a larger card must leave the
// first two partitions alone and grow root to the card.
func TestWriteFitsRootToTheCard(t *testing.T) {
	mkfs := findTool(t, "mkfs.ext4")
	e2fsck := findTool(t, "e2fsck")
	resize := findTool(t, "resize2fs")
	debugfs := findTool(t, "debugfs")
	dumpe2fs := findTool(t, "dumpe2fs")

	const (
		bootStart   = 8192
		bootSectors = 8192 // 4 MiB, the boot partition we write settings to
		cfgSectors  = 8192 // 4 MiB, a second partition we use
		rootSectors = 16384
	)
	rootStart := uint32(bootStart + bootSectors + cfgSectors)
	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "hello.txt"), []byte("kept on the grown card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootImg := filepath.Join(t.TempDir(), "root.img")
	if out, err := exec.Command(mkfs, "-q", "-F", "-d", content, rootImg, "8M").CombinedOutput(); err != nil {
		t.Skipf("mkfs.ext4 -d: %v %s", err, out)
	}

	imgPath := filepath.Join(t.TempDir(), "pi.img")
	img := mustCreate(t, imgPath, int64(rootStart+rootSectors)*diskimage.SectorSize)
	mbr := partitionTable(bootStart, bootSectors, cfgSectors, rootStart, rootSectors)
	if _, err := img.WriteAt(mbr, 0); err != nil {
		t.Fatal(err)
	}
	rootData, err := os.ReadFile(rootImg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := img.WriteAt(rootData, int64(rootStart)*diskimage.SectorSize); err != nil {
		t.Fatal(err)
	}
	img.Close()

	const card = 64 << 20
	cardPath := filepath.Join(t.TempDir(), "card.img")
	cardFile := mustCreate(t, cardPath, card)
	src, err := diskimage.Open(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rep, err := Write(context.Background(), src, fileTarget{cardFile}, card, nil, Options{SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	cardFile.Close()
	if rep.RootPartition != 3 {
		t.Fatalf("root partition = %d, want 3", rep.RootPartition)
	}

	f, err := os.Open(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parts, err := diskimage.ReadPartitions(f)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Sectors != bootSectors || parts[1].Sectors != cfgSectors {
		t.Fatalf("boot or settings partition changed: %+v", parts)
	}
	end := uint64(card/diskimage.SectorSize) / uint64(diskimage.RootAlign/diskimage.SectorSize) * uint64(diskimage.RootAlign/diskimage.SectorSize)
	if parts[2].Sectors != end-uint64(rootStart) {
		t.Fatalf("root sectors = %d, want %d", parts[2].Sectors, end-uint64(rootStart))
	}

	grown := filepath.Join(t.TempDir(), "root-grown.img")
	part := make([]byte, parts[2].Size())
	if _, err := f.ReadAt(part, parts[2].Offset()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grown, part, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(e2fsck, "-fp", grown).CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() > 2 {
			t.Fatalf("e2fsck: %v %s", err, out)
		}
	}
	if out, err := exec.Command(resize, grown).CombinedOutput(); err != nil {
		t.Fatalf("resize2fs: %v %s", err, out)
	}
	info, err := exec.Command(dumpe2fs, "-h", grown).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	bs := mustMatch(t, `(?m)^Block size:\s*(\d+)`, info)
	blocks := mustMatch(t, `(?m)^Block count:\s*(\d+)`, info)
	got := bs * blocks
	if got < parts[2].Size()-bs || got > parts[2].Size() {
		t.Fatalf("filesystem is %d bytes, partition is %d", got, parts[2].Size())
	}
	cat, err := exec.Command(debugfs, "-R", "cat hello.txt", grown).CombinedOutput()
	if err != nil || !bytes.Contains(cat, []byte("kept on the grown card")) {
		t.Fatalf("hello.txt after grow: %v %s", err, cat)
	}
}

func findTool(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Skip(name + " not installed")
	return ""
}

func mustCreate(t *testing.T, path string, size int64) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return f
}

func partitionTable(bootStart, bootSectors, cfgSectors uint32, rootStart, rootSectors uint32) []byte {
	mbr := make([]byte, diskimage.SectorSize)
	put := func(i int, typ byte, start, count uint32) {
		e := mbr[446+16*i:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
	}
	put(0, 0x0c, bootStart, bootSectors)
	put(1, 0x0c, bootStart+bootSectors, cfgSectors)
	put(2, 0x83, rootStart, rootSectors)
	mbr[510], mbr[511] = 0x55, 0xAA
	return mbr
}

func mustMatch(t *testing.T, expr string, text []byte) int64 {
	t.Helper()
	m := regexp.MustCompile(expr).FindSubmatch(text)
	if m == nil {
		t.Fatalf("no match %s in %s", expr, text)
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
