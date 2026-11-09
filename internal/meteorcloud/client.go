// Package meteorcloud lists and downloads the OS images a MeteorCloud server
// publishes for Meteor Imager. No login: GET /api/v1/os-images and
// GET /api/v1/os-images/{id}/download are public on the server.
package meteorcloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Image is one published OS image.
type Image struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Version        string    `json:"version"`
	Description    *string   `json:"description"`
	DeviceType     *string   `json:"device_type"`
	FileName       string    `json:"file_name"`
	ContentType    *string   `json:"content_type"`
	SizeBytes      int64     `json:"size_bytes"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	DownloadURL    string    `json:"download_url"`
}

// Client talks to one MeteorCloud server.
type Client struct {
	// Base is the server root, e.g. https://fleet.example.com.
	Base *url.URL
	HTTP *http.Client
	// UserAgent identifies the client in server logs.
	UserAgent string
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseServer turns what the operator typed into a server URL.
//
// Without a scheme, HTTPS is used, except for IP addresses, "localhost" and
// ".local" names, which are LAN/dev servers and get HTTP. An explicit http://
// is only accepted for those LAN/dev hosts unless allowInsecure is set.
func ParseServer(input string, allowInsecure bool) (*url.URL, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, errors.New("enter the MeteorCloud address, for example fleet.example.com")
	}
	if !strings.Contains(s, "://") {
		host := s
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		scheme := "https"
		if IsLANHost(hostOnly(host)) {
			scheme = "http"
		}
		s = scheme + "://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("invalid server address: %w", err)
	}
	if u.Host == "" || u.User != nil {
		return nil, errors.New("invalid server address")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecure && !IsLANHost(u.Hostname()) {
			return nil, fmt.Errorf("refusing plain HTTP to %s: use https://, or allow insecure HTTP for a LAN/dev server", u.Hostname())
		}
	default:
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u, nil
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// IsLANHost reports hosts where plain HTTP is acceptable: private, loopback and
// link-local IPs, localhost and mDNS (.local) names.
func IsLANHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

// New returns a client with sane timeouts for listing; downloads are bounded
// by their context instead.
func New(base *url.URL, version string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		Base: base,
		HTTP: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				// Never follow a redirect from HTTPS down to HTTP.
				if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
					return errors.New("refusing a redirect from HTTPS to HTTP")
				}
				return nil
			},
		},
		UserAgent: "meteor-imager/" + version,
	}
}

func (c *Client) resolve(ref string) (*url.URL, error) {
	r, err := url.Parse(ref)
	if err != nil {
		return nil, err
	}
	u := c.Base.ResolveReference(r)
	if r.IsAbs() || r.Host != "" {
		return nil, errors.New("server returned a download link to another host")
	}
	if !strings.HasPrefix(r.Path, "/") {
		return nil, errors.New("server returned a relative download link")
	}
	u.Path = c.Base.Path + r.Path
	return u, nil
}

func (c *Client) get(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, statusError(resp.StatusCode, body)
	}
	return resp, nil
}

func statusError(code int, body []byte) error {
	var detail struct {
		Detail any `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &detail)
	msg := detail.Error.Message
	if msg == "" {
		if s, ok := detail.Detail.(string); ok {
			msg = s
		}
	}
	switch code {
	case http.StatusNotFound:
		if msg == "" {
			msg = "not found; is this a MeteorCloud server with Meteor Imager support?"
		}
	case http.StatusTooManyRequests:
		msg = "the server is rate limiting requests; wait a minute and try again"
	}
	if msg == "" {
		msg = http.StatusText(code)
	}
	return fmt.Errorf("server returned %d: %s", code, msg)
}

// ListImages returns the server's published OS images, newest first.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	u, err := c.resolve("/api/v1/os-images")
	if err != nil {
		return nil, err
	}
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var images []Image
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&images); err != nil {
		return nil, fmt.Errorf("unexpected response from the server: %w", err)
	}
	return images, nil
}

// Progress reports bytes done of total (total may be 0 when unknown).
type Progress func(done, total int64)

// Download fetches an image into cacheDir and returns the local path. Files
// are named by SHA-256, so a cached image is reused after re-checking its hash.
func (c *Client) Download(ctx context.Context, img Image, cacheDir string, progress Progress) (string, error) {
	sum := strings.ToLower(img.ChecksumSHA256)
	if !sha256Pattern.MatchString(sum) {
		return "", errors.New("the server sent no valid SHA-256 for this image")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(cacheDir, sum+cacheExt(img.FileName))
	if ok, err := verifyFile(final, sum, img.SizeBytes, progress); err != nil {
		return "", err
	} else if ok {
		return final, nil
	}

	u, err := c.resolve(img.DownloadURL)
	if err != nil {
		return "", err
	}
	resp, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if header := strings.ToLower(resp.Header.Get("X-Checksum-SHA256")); header != "" && header != sum {
		return "", errors.New("the server's download checksum does not match the image list")
	}
	total := img.SizeBytes
	if total <= 0 {
		total = resp.ContentLength
	}

	tmp, err := os.CreateTemp(cacheDir, sum+".*.part")
	if err != nil {
		return "", err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	h := sha256.New()
	n, err := copyWithProgress(io.MultiWriter(tmp, h), resp.Body, total, progress)
	if err != nil {
		return "", fmt.Errorf("download interrupted: %w", err)
	}
	if img.SizeBytes > 0 && n != img.SizeBytes {
		return "", fmt.Errorf("download incomplete: got %d of %d bytes", n, img.SizeBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("downloaded image is corrupt: SHA-256 %s, expected %s", got, sum)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
}

// CacheDir is <user cache dir>/meteor-imager/images.
func CacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "meteor-imager", "images"), nil
}

func cacheExt(fileName string) string {
	name := strings.ToLower(path.Base(strings.ReplaceAll(fileName, "\\", "/")))
	for _, ext := range []string{".img.xz", ".img.gz", ".img.zip", ".img", ".xz", ".gz", ".zip"} {
		if strings.HasSuffix(name, ext) {
			return ext
		}
	}
	return ".bin"
}

func verifyFile(p, sum string, size int64, progress Progress) (bool, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if size > 0 && st.Size() != size {
		f.Close()
		return false, os.Remove(p)
	}
	h := sha256.New()
	if _, err := copyWithProgress(h, f, st.Size(), progress); err != nil {
		return false, err
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		f.Close()
		return false, os.Remove(p)
	}
	return true, nil
}

func copyWithProgress(dst io.Writer, src io.Reader, total int64, progress Progress) (int64, error) {
	buf := make([]byte, 1<<20)
	var done int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if err == io.EOF {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
}
