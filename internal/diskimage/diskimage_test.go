package diskimage

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ulikunitz/xz"
)

func mbrImage() []byte {
	img := make([]byte, 64*1024)
	e := img[446:]
	e[4] = 0x0c
	binary.LittleEndian.PutUint32(e[8:], 8192)
	binary.LittleEndian.PutUint32(e[12:], 1024)
	e = img[462:]
	e[4] = 0x83
	binary.LittleEndian.PutUint32(e[8:], 9216)
	binary.LittleEndian.PutUint32(e[12:], 2048)
	img[510], img[511] = 0x55, 0xAA
	copy(img[1000:], "payload")
	return img
}

func TestReadPartitionsMBR(t *testing.T) {
	parts, err := ReadPartitions(bytes.NewReader(mbrImage()))
	if err != nil || len(parts) != 2 {
		t.Fatalf("%v %v", parts, err)
	}
	if !parts[0].MaybeFAT || parts[0].Offset() != 8192*512 || parts[1].MaybeFAT || parts[1].Index != 2 {
		t.Errorf("parts = %+v", parts)
	}
	if _, err := ReadPartitions(bytes.NewReader(make([]byte, 512))); err != ErrNoPartitionTable {
		t.Errorf("blank disk: %v", err)
	}
}

func TestReadPartitionsGPT(t *testing.T) {
	img := make([]byte, 64*512)
	img[446+4] = 0xee
	binary.LittleEndian.PutUint32(img[446+8:], 1)
	img[510], img[511] = 0x55, 0xAA
	hdr := img[512:]
	copy(hdr, "EFI PART")
	binary.LittleEndian.PutUint64(hdr[72:], 2)
	binary.LittleEndian.PutUint32(hdr[80:], 4)
	binary.LittleEndian.PutUint32(hdr[84:], 128)
	e := img[1024:]
	copy(e[0:16], gptESP)
	binary.LittleEndian.PutUint64(e[32:], 2048)
	binary.LittleEndian.PutUint64(e[40:], 4095)
	parts, err := ReadPartitions(bytes.NewReader(img))
	if err != nil || len(parts) != 1 {
		t.Fatalf("%v %v", parts, err)
	}
	if !parts[0].MaybeFAT || parts[0].Sectors != 2048 || parts[0].Type != "C12A7328-F81F-11D2-BA4B-00A0C93EC93B" {
		t.Errorf("part = %+v", parts[0])
	}
}

func TestOpenFormats(t *testing.T) {
	raw := mbrImage()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var xzBuf, gzBuf, zipBuf bytes.Buffer
	xw, _ := xz.NewWriter(&xzBuf)
	xw.Write(raw)
	xw.Close()
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(raw)
	gw.Close()
	zw := zip.NewWriter(&zipBuf)
	readme, _ := zw.Create("README.txt")
	readme.Write([]byte("hi"))
	f, _ := zw.Create("pi.img")
	f.Write(raw)
	zw.Close()

	for _, c := range []struct {
		path string
		want Format
		size int64
	}{
		{write("a.img", raw), FormatRaw, int64(len(raw))},
		{write("b.bin", xzBuf.Bytes()), FormatXZ, -1}, // detected by content, not name
		{write("c.img.gz", gzBuf.Bytes()), FormatGzip, -1},
		{write("d.zip", zipBuf.Bytes()), FormatZip, int64(len(raw))},
	} {
		s, err := Open(c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		got, err := io.ReadAll(s)
		if err != nil || !bytes.Equal(got, raw) || s.Format != c.want || s.ImageSize != c.size {
			t.Errorf("%s: format %s size %d err %v equal %v", c.path, s.Format, s.ImageSize, err, bytes.Equal(got, raw))
		}
		if s.Progress() < 0.99 {
			t.Errorf("%s: progress %f after reading everything", c.path, s.Progress())
		}
		s.Close()
	}
}

func TestZipWithoutImage(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("notes.txt")
	w.Write([]byte("x"))
	zw.Close()
	p := filepath.Join(t.TempDir(), "x.zip")
	os.WriteFile(p, buf.Bytes(), 0o600)
	if _, err := Open(p); err == nil {
		t.Fatal("expected an error")
	}
}
