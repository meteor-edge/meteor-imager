package meteorcloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestParseServer(t *testing.T) {
	cases := []struct {
		in, want string
		insecure bool
		wantErr  bool
	}{
		{in: "fleet.example.com", want: "https://fleet.example.com"},
		{in: "fleet.example.com/", want: "https://fleet.example.com"},
		{in: "https://fleet.example.com/base/", want: "https://fleet.example.com/base"},
		{in: "192.168.0.118:8000", want: "http://192.168.0.118:8000"},
		{in: "localhost:8000", want: "http://localhost:8000"},
		{in: "meteor.local", want: "http://meteor.local"},
		{in: "http://10.0.0.5", want: "http://10.0.0.5"},
		{in: "http://fleet.example.com", wantErr: true},
		{in: "http://fleet.example.com", insecure: true, want: "http://fleet.example.com"},
		{in: "http://8.8.8.8", wantErr: true},
		{in: "ftp://fleet.example.com", wantErr: true},
		{in: "https://user:pw@fleet.example.com", wantErr: true},
		{in: "  ", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseServer(c.in, c.insecure)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseServer(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("ParseServer(%q) = %v, %v; want %s", c.in, got, err, c.want)
		}
	}
}

func fakeServer(t *testing.T, body []byte, sum string) (*httptest.Server, *int) {
	t.Helper()
	downloads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/os-images", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"id": "11111111-1111-1111-1111-111111111111", "name": "Pi OS Lite", "version": "2025.05",
			"description": nil, "device_type": "Raspberry Pi 4", "file_name": "pi.img.xz",
			"content_type": "application/x-xz", "size_bytes": len(body), "checksum_sha256": sum,
			"created_at":   "2025-05-01T10:00:00Z",
			"download_url": "/api/v1/os-images/11111111-1111-1111-1111-111111111111/download",
		}})
	})
	mux.HandleFunc("GET /api/v1/os-images/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		w.Header().Set("X-Checksum-SHA256", sum)
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &downloads
}

func TestListAndDownload(t *testing.T) {
	body := []byte(strings.Repeat("image-bytes", 300000))
	h := sha256.Sum256(body)
	sum := hex.EncodeToString(h[:])
	srv, downloads := fakeServer(t, body, sum)
	base, _ := url.Parse(srv.URL)
	c := New(base, "test")

	images, err := c.ListImages(context.Background())
	if err != nil || len(images) != 1 || images[0].Name != "Pi OS Lite" || *images[0].DeviceType != "Raspberry Pi 4" {
		t.Fatalf("ListImages = %+v, %v", images, err)
	}
	cache := t.TempDir()
	var last int64
	p, err := c.Download(context.Background(), images[0], cache, func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, sum+".img.xz") || last != int64(len(body)) {
		t.Fatalf("path %s, progress %d", p, last)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(body) {
		t.Fatal("downloaded bytes differ")
	}

	if _, err := c.Download(context.Background(), images[0], cache, nil); err != nil || *downloads != 1 {
		t.Fatalf("cached download: err %v, downloads %d", err, *downloads)
	}

	// A corrupted cache entry is replaced.
	os.WriteFile(p, []byte(strings.Repeat("X", len(body))), 0o600)
	if _, err := c.Download(context.Background(), images[0], cache, nil); err != nil || *downloads != 2 {
		t.Fatalf("re-download: err %v, downloads %d", err, *downloads)
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 {
		t.Fatalf("cache has leftovers: %v", entries)
	}
}

func TestDownloadRejectsWrongChecksum(t *testing.T) {
	body := []byte("tampered")
	srv, _ := fakeServer(t, body, strings.Repeat("a", 64))
	base, _ := url.Parse(srv.URL)
	c := New(base, "test")
	images, err := c.ListImages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if _, err := c.Download(context.Background(), images[0], cache, nil); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Fatalf("partial file left behind: %v", entries)
	}
}

func TestDownloadRefusesOtherHosts(t *testing.T) {
	base, _ := url.Parse("https://fleet.example.com")
	c := New(base, "test")
	img := Image{ChecksumSHA256: strings.Repeat("b", 64), DownloadURL: "https://evil.example.net/x.img", FileName: "x.img"}
	if _, err := c.Download(context.Background(), img, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for a foreign download host")
	}
}

func TestErrorMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	_, err := New(base, "test").ListImages(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate limiting") {
		t.Fatalf("err = %v", err)
	}
}
