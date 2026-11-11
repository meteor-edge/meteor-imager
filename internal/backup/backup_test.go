package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meteor-edge/meteor-imager/internal/diskimage"
)

const (
	bootStart   = 8192 // sectors
	bootSectors = 8192
)

// card builds a card file: MBR, a "boot" partition of marker bytes, a root
// partition from rootImg (or marker bytes), then unpartitioned space.
func card(t *testing.T, rootImg string, rootSectors uint32, tail int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "card.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rootStart := uint32(bootStart + bootSectors)
	end := int64(rootStart+rootSectors) * 512
	if err := f.Truncate(end + tail); err != nil {
		t.Fatal(err)
	}
	mbr := make([]byte, 512)
	put := func(i int, typ byte, start, count uint32) {
		e := mbr[446+16*i:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
	}
	put(0, 0x0c, bootStart, bootSectors)
	put(1, 0x83, rootStart, rootSectors)
	mbr[510], mbr[511] = 0x55, 0xAA
	f.WriteAt(mbr, 0)
	f.WriteAt(bytes.Repeat([]byte("BOOT"), 1024), bootStart*512)
	if rootImg == "" {
		f.WriteAt(bytes.Repeat([]byte("ROOT"), 1024), int64(rootStart)*512)
	} else {
		data, err := os.ReadFile(rootImg)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt(data, int64(rootStart)*512)
	}
	if tail > 0 {
		f.WriteAt([]byte("TAIL-NOT-COPIED"), end+100)
	}
	return path
}

func runBackup(t *testing.T, cardPath, out string, opt Options) *Report {
	t.Helper()
	f, err := os.Open(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	rep, err := Run(context.Background(), f, st.Size(), out, opt)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func fileSHA(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestBackupStopsAtTheLastPartition(t *testing.T) {
	src := card(t, "", 8192, 1<<20)
	out := filepath.Join(t.TempDir(), "pi.img")
	rep := runBackup(t, src, out, Options{})

	want, _ := os.ReadFile(src)
	want = want[:(bootStart+bootSectors+8192)*512]
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, want) {
		t.Fatalf("backup differs from the card's partitions (%d vs %d bytes)", len(got), len(want))
	}
	if rep.ImageBytes != int64(len(want)) || rep.FileBytes != int64(len(want)) || rep.SHA256 != fileSHA(t, out) {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(out + ".part"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}

func TestBackupGzipRoundTrips(t *testing.T) {
	src := card(t, "", 8192, 0)
	out := filepath.Join(t.TempDir(), "pi.img.gz")
	rep := runBackup(t, src, out, Options{})

	f, _ := os.Open(out)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	want, _ := os.ReadFile(src)
	if !bytes.Equal(got, want) {
		t.Fatal("decompressed backup differs from the card")
	}
	if rep.FileBytes >= rep.ImageBytes || rep.SHA256 != fileSHA(t, out) {
		t.Errorf("report = %+v", rep)
	}
	// The result is a valid image source for flashing.
	s, err := diskimage.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestCheckOutput(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.img")
	os.WriteFile(existing, nil, 0o600)
	for _, bad := range []string{existing, filepath.Join(dir, "x.iso"), filepath.Join(dir, "x.img.xz"), filepath.Join(dir, "missing", "x.img")} {
		if CheckOutput(bad) == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if err := CheckOutput(filepath.Join(dir, "new.img.gz")); err != nil {
		t.Error(err)
	}
}

func TestBackupShrinksTheRootFilesystem(t *testing.T) {
	if !ShrinkAvailable() {
		t.Skip("e2fsprogs not installed")
	}
	mkfs, err := lookTool("mkfs.ext4")
	if err != nil {
		t.Skip("mkfs.ext4 not installed")
	}
	debugfs, err := lookTool("debugfs")
	if err != nil {
		t.Skip("debugfs not installed")
	}
	content := t.TempDir()
	os.WriteFile(filepath.Join(content, "hello.txt"), []byte("kept after shrinking\n"), 0o644)
	const rootSectors = 1 << 20 // 512 MiB, mostly empty
	rootImg := filepath.Join(t.TempDir(), "root.img")
	if out, err := exec.Command(mkfs, "-q", "-F", "-d", content, rootImg, "512M").CombinedOutput(); err != nil {
		t.Skipf("mkfs.ext4 -d not supported: %v %s", err, out)
	}
	src := card(t, rootImg, rootSectors, 0)
	out := filepath.Join(t.TempDir(), "pi.img")

	rep := runBackup(t, src, out, Options{Shrink: true})

	if !rep.Shrunk || rep.ImageBytes >= int64(bootStart+bootSectors+rootSectors)*512 {
		t.Fatalf("not shrunk: %+v", rep)
	}
	img, _ := os.ReadFile(out)
	if int64(len(img)) != rep.ImageBytes {
		t.Fatalf("file is %d bytes, report says %d", len(img), rep.ImageBytes)
	}
	parts, err := diskimage.ReadPartitions(bytes.NewReader(img))
	if err != nil || len(parts) != 2 {
		t.Fatalf("partitions: %v %v", parts, err)
	}
	if end := parts[1].Offset() + parts[1].Size(); end != int64(len(img)) {
		t.Errorf("root partition ends at %d, image is %d bytes", end, len(img))
	}
	if !bytes.Equal(img[bootStart*512:bootStart*512+4096], bytes.Repeat([]byte("BOOT"), 1024)) {
		t.Error("boot partition changed")
	}
	shrunk := filepath.Join(t.TempDir(), "root.img")
	os.WriteFile(shrunk, img[parts[1].Offset():], 0o600)
	fsck, _ := lookTool("e2fsck")
	if out, err := exec.Command(fsck, "-fn", shrunk).CombinedOutput(); err != nil {
		t.Fatalf("e2fsck on the shrunk partition: %v\n%s", err, out)
	}
	cat, err := exec.Command(debugfs, "-R", "cat /hello.txt", shrunk).Output()
	if err != nil || !strings.Contains(string(cat), "kept after shrinking") {
		t.Errorf("file lost: %q %v", cat, err)
	}
}

func TestShrinkSkipsGPTAndNonLinux(t *testing.T) {
	if _, err := shrinkable([]diskimage.Partition{{Index: 1, Type: "C12A7328-F81F-11D2-BA4B-00A0C93EC93B"}}); err == nil {
		t.Error("GPT accepted")
	}
	if _, err := shrinkable([]diskimage.Partition{{Index: 1, StartSector: 8192, Type: "83"}, {Index: 2, StartSector: 99999, Type: "0c"}}); err == nil {
		t.Error("FAT last partition accepted")
	}
}
