package flash

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soypat/fat"
	"github.com/ulikunitz/xz"

	"github.com/meteor-edge/meteor-imager/internal/bootfs"
	"github.com/meteor-edge/meteor-imager/internal/diskimage"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// memDisk is a card in memory that records the order of writes.
type memDisk struct {
	data       []byte
	firstWrite []int64
	corrupt    int64 // flip a byte here on read, to simulate a bad card
}

func (m *memDisk) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(m.data)) {
		return 0, errors.New("read past end")
	}
	n := copy(p, m.data[off:])
	if m.corrupt > 0 && m.corrupt >= off && m.corrupt < off+int64(n) {
		p[m.corrupt-off] ^= 0xff
	}
	return n, nil
}

func (m *memDisk) WriteAt(p []byte, off int64) (int, error) {
	if off%512 != 0 || len(p)%512 != 0 {
		return 0, errors.New("unaligned write")
	}
	if off+int64(len(p)) > int64(len(m.data)) {
		return 0, errors.New("no space left on device")
	}
	m.firstWrite = append(m.firstWrite, off)
	return copy(m.data[off:], p), nil
}

func (m *memDisk) Sync() error  { return nil }
func (m *memDisk) Close() error { return nil }

// testImage writes a small Pi-shaped image (FAT32 boot + root marker) and
// returns its path, xz-compressed when compress is set.
func testImage(t *testing.T, compress bool) (string, []byte) {
	t.Helper()
	const boot, bootLen, rootLen = 8192, 131072, 2048
	raw := make([]byte, (boot+bootLen+rootLen)*512-100) // odd size: exercises padding
	entry := func(i int, typ byte, start, count uint32) {
		e := raw[446+16*i:]
		e[4] = typ
		binary.LittleEndian.PutUint32(e[8:], start)
		binary.LittleEndian.PutUint32(e[12:], count)
	}
	entry(0, 0x0c, boot, bootLen)
	entry(1, 0x83, boot+bootLen, rootLen)
	raw[510], raw[511] = 0x55, 0xAA
	copy(raw[(boot+bootLen)*512:], strings.Repeat("ROOTFS", 100))

	d := &memDisk{data: make([]byte, len(raw)+412)}
	copy(d.data, raw)
	vol := &partDev{d: d, off: boot * 512}
	var fm fat.Formatter
	if err := fm.Format(vol, 512, bootLen, fat.FormatParams{Label: "bootfs", Format: fat.FormatFAT32}); err != nil {
		t.Fatal(err)
	}
	v, err := bootfs.OpenBoot(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFile("user-data", []byte("#cloud-config\n")); err != nil {
		t.Fatal(err)
	}
	if err := v.WriteFile("cmdline.txt", []byte("console=tty1 root=PARTUUID=x-02 rootwait\n")); err != nil {
		t.Fatal(err)
	}
	v.Close()
	raw = d.data[:len(raw)]

	path := filepath.Join(t.TempDir(), "pi.img")
	data := raw
	if compress {
		var buf bytes.Buffer
		w, _ := xz.NewWriter(&buf)
		w.Write(raw)
		w.Close()
		data = buf.Bytes()
		path += ".xz"
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, raw
}

type partDev struct {
	d   *memDisk
	off int64
}

func (p *partDev) ReadBlocks(b []byte, blk int64) (int, error)  { return p.d.ReadAt(b, p.off+blk*512) }
func (p *partDev) WriteBlocks(b []byte, blk int64) (int, error) { return p.d.WriteAt(b, p.off+blk*512) }
func (p *partDev) EraseBlocks(int64, int64) error               { return nil }

func testProfile() *profile.FlashProfile {
	return &profile.FlashProfile{
		SchemaVersion: 1,
		WiFi:          &profile.WiFi{SSID: "home", PSK: "secret-psk", Country: "NL"},
		Enrollment:    &profile.Enrollment{MeteorDomain: "fleet.example.com", MeteorAPIKey: "key_x1y2z3"},
	}
}

func TestWriteVerifyConfigure(t *testing.T) {
	path, raw := testImage(t, true)
	src, err := diskimage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	card := &memDisk{data: make([]byte, 128<<20)}
	var stages []Stage
	rep, err := Write(context.Background(), src, card, int64(len(card.data)), testProfile(), Options{
		Progress: func(s Stage, f float64) {
			if len(stages) == 0 || stages[len(stages)-1] != s {
				stages = append(stages, s)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified || rep.BytesWritten != int64(len(raw)+100) || !rep.Boot.CloudInit {
		t.Fatalf("report = %+v", rep)
	}
	if strings.Join([]string{string(stages[0]), string(stages[1]), string(stages[2]), string(stages[3])}, ",") != "write,verify,configure,finish" {
		t.Errorf("stages = %v", stages)
	}
	if card.firstWrite[0] == 0 || card.firstWrite[len(card.firstWrite)-1] != 0 {
		t.Errorf("partition table must be written last; write offsets %v", card.firstWrite)
	}
	v, err := bootfs.OpenBoot(card)
	if err != nil {
		t.Fatal(err)
	}
	env, err := v.ReadFile(bootfs.MeteorEnvFile)
	if err != nil || !strings.Contains(string(env), "METEOR_API_KEY=key_x1y2z3") {
		t.Errorf(".meteorenv = %q, %v", env, err)
	}
	if !bytes.Equal(card.data[(8192+131072)*512:][:600], raw[(8192+131072)*512:][:600]) {
		t.Error("root partition differs from the image")
	}
}

func TestWriteDetectsBadCard(t *testing.T) {
	path, _ := testImage(t, false)
	src, _ := diskimage.Open(path)
	defer src.Close()
	card := &memDisk{data: make([]byte, 128<<20), corrupt: 5 << 20}
	_, err := Write(context.Background(), src, card, 0, nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("err = %v", err)
	}
	for _, b := range card.data[:512] {
		if b != 0 {
			t.Fatal("partition table written although verification failed")
		}
	}
}

func TestWriteRejectsSmallCard(t *testing.T) {
	path, _ := testImage(t, false)
	src, _ := diskimage.Open(path)
	defer src.Close()
	if _, err := Write(context.Background(), src, &memDisk{data: make([]byte, 8<<20)}, 8<<20, nil, Options{}); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("err = %v", err)
	}
	// Compressed: size unknown up front, caught while writing.
	xzPath, _ := testImage(t, true)
	xsrc, _ := diskimage.Open(xzPath)
	defer xsrc.Close()
	if _, err := Write(context.Background(), xsrc, &memDisk{data: make([]byte, 8<<20)}, 8<<20, nil, Options{}); !errors.Is(err, ErrTooSmall) {
		t.Fatalf("xz err = %v", err)
	}
}

func TestWriteCancel(t *testing.T) {
	path, _ := testImage(t, false)
	src, _ := diskimage.Open(path)
	defer src.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Write(ctx, src, &memDisk{data: make([]byte, 128<<20)}, 0, nil, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigureFile(t *testing.T) {
	path, _ := testImage(t, true)
	out := filepath.Join(t.TempDir(), "configured.img")
	rep, err := ConfigureFile(context.Background(), path, out, testProfile(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Boot.Written) == 0 {
		t.Fatalf("report = %+v", rep)
	}
	f, _ := os.OpenFile(out, os.O_RDWR, 0)
	defer f.Close()
	v, err := bootfs.OpenBoot(f)
	if err != nil {
		t.Fatal(err)
	}
	if nc, err := v.ReadFile(bootfs.NetworkConfigFile); err != nil || !strings.Contains(string(nc), `"home"`) {
		t.Errorf("network-config = %q, %v", nc, err)
	}
	if _, err := ConfigureFile(context.Background(), path, out, testProfile(), Options{}); err == nil {
		t.Error("overwrote an existing output file")
	}
}
