// Package gui is Meteor Imager's desktop UI: a small web app served on
// 127.0.0.1 and opened in the default browser. Only this machine can reach it,
// every request must carry the random session token set when the browser is
// opened, and Host/Origin checks stop other web pages from calling it.
package gui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/meteor-edge/meteor-imager/internal/backup"
	"github.com/meteor-edge/meteor-imager/internal/devices"
	"github.com/meteor-edge/meteor-imager/internal/flash"
	"github.com/meteor-edge/meteor-imager/internal/job"
	"github.com/meteor-edge/meteor-imager/internal/meteorcloud"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

//go:embed web
var webFS embed.FS

const cookieName = "meteor_imager_session"

// Options configure the UI.
type Options struct {
	Version     string
	OpenBrowser bool
	// IdleTimeout ends the app when the page has been closed this long.
	IdleTimeout time.Duration
}

// Run serves the UI until the user quits, the page goes away, or ctx ends.
func Run(ctx context.Context, opt Options) error {
	if relaunched, err := ensureElevated(); relaunched || err != nil {
		return err
	}
	if opt.IdleTimeout == 0 {
		opt.IdleTimeout = 2 * time.Minute
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s := newServer(ln.Addr().String(), opt.Version)
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.quit = cancel

	url := fmt.Sprintf("http://%s/?token=%s", s.addr, s.token)
	fmt.Fprintf(os.Stderr, "Meteor Imager is running at http://%s\nKeep this window open while you use it; close it (or press Ctrl+C) to quit.\n", s.addr)
	if opt.OpenBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "Open this address in your browser:\n  %s\n", url)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Open this address in your browser:\n  %s\n", url)
	}

	go s.watchIdle(ctx, opt.IdleTimeout)
	go func() {
		<-ctx.Done()
		s.cancelJob()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type server struct {
	addr    string
	token   string
	version string
	mux     *http.ServeMux
	quit    func()

	mu       sync.Mutex
	lastSeen time.Time
	state    jobState
	cancel   context.CancelFunc
}

type jobState struct {
	// Kind is "write" or "backup".
	Kind     string         `json:"kind,omitempty"`
	Running  bool           `json:"running"`
	Stage    string         `json:"stage,omitempty"`
	Fraction float64        `json:"fraction"`
	Bytes    int64          `json:"bytes,omitempty"`
	Messages []string       `json:"messages,omitempty"`
	Error    string         `json:"error,omitempty"`
	Problems []string       `json:"problems,omitempty"`
	Report   *flash.Report  `json:"report,omitempty"`
	Backup   *backup.Report `json:"backup,omitempty"`
	Device   string         `json:"device,omitempty"`
}

func newServer(addr, version string) *server {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s := &server{addr: addr, token: hex.EncodeToString(b), version: version, mux: http.NewServeMux(), lastSeen: time.Now()}
	static, _ := fs.Sub(webFS, "web")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("POST /api/images", s.handleImages)
	s.mux.HandleFunc("GET /api/devices", s.handleDevices)
	s.mux.HandleFunc("POST /api/browse", s.handleBrowse)
	s.mux.HandleFunc("POST /api/flash", s.handleFlash)
	s.mux.HandleFunc("POST /api/backup", s.handleBackup)
	s.mux.HandleFunc("GET /api/job", s.handleJob)
	s.mux.HandleFunc("POST /api/cancel", s.handleCancel)
	s.mux.HandleFunc("POST /api/forget", s.handleForget)
	s.mux.HandleFunc("POST /api/quit", s.handleQuit)
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// DNS rebinding: only answer to the exact address we listen on.
	if r.Host != s.addr {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")

	// First visit: trade the token in the URL for a cookie, then drop it from the address bar.
	if r.URL.Path == "/" && r.URL.Query().Has("token") {
		if !equal(r.URL.Query().Get("token"), s.token) {
			http.Error(w, "invalid or expired link; start Meteor Imager again", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	c, err := r.Cookie(cookieName)
	if err != nil || !equal(c.Value, s.token) {
		http.Error(w, "Open Meteor Imager from the link it printed when it started.", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		if r.Header.Get("Origin") != "http://"+s.addr || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	}
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
	s.mux.ServeHTTP(w, r)
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (s *server) watchIdle(ctx context.Context, idle time.Duration) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			gone := !s.state.Running && time.Since(s.lastSeen) > idle
			s.mu.Unlock()
			if gone {
				fmt.Fprintln(os.Stderr, "The Meteor Imager page was closed; quitting.")
				s.quit()
				return
			}
		}
	}
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	data, _ := webFS.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	body := map[string]any{"error": err.Error()}
	var ve *profile.ValidationError
	if errors.As(err, &ve) {
		body["problems"] = ve.Problems
	}
	writeJSON(w, status, body)
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	canWrite, why := flash.CanWriteDevices()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":          s.version,
		"prefs":            profile.LoadPrefs(),
		"can_write":        canWrite,
		"why_not":          why,
		"shrink_available": backup.ShrinkAvailable(),
		"backup_dir":       backupDir(),
	})
}

// backupDir suggests where backups go: Downloads, else the home folder.
func backupDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if st, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && st.IsDir() {
		return filepath.Join(home, "Downloads")
	}
	return home
}

type serverRef struct {
	Server   string `json:"server"`
	Insecure bool   `json:"insecure_http"`
}

func (ref serverRef) client(version string) (*meteorcloud.Client, error) {
	u, err := meteorcloud.ParseServer(ref.Server, ref.Insecure)
	if err != nil {
		return nil, err
	}
	return meteorcloud.New(u, version), nil
}

func (s *server) handleImages(w http.ResponseWriter, r *http.Request) {
	var ref serverRef
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := ref.client(s.version)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	images, err := c.ListImages(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if images == nil {
		images = []meteorcloud.Image{}
	}
	// Suggest the server's address as METEOR_DOMAIN, unless the Pi could not reach it.
	domain := c.Base.Host
	if profile.ValidateDomain(domain) != nil {
		domain = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server":        c.Base.String(),
		"meteor_domain": domain,
		"images":        images,
	})
}

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	devs, err := devices.List(r.Context(), true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []map[string]any{}
	for _, d := range devs {
		if d.System {
			continue // never offered, not even with "show all"
		}
		if !all && !d.Safe() {
			continue
		}
		out = append(out, map[string]any{"device": d, "summary": d.Describe(), "size": devices.HumanSize(d.SizeBytes)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path, err := pickFile(r.Context())
	if err != nil {
		writeErr(w, http.StatusNotImplemented, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

type flashRequest struct {
	serverRef
	ImageID           string                `json:"image_id"`
	Path              string                `json:"path"`
	Device            string                `json:"device"`
	ConfirmDevice     string                `json:"confirm_device"`
	AllowNonRemovable bool                  `json:"allow_non_removable"`
	SkipVerify        bool                  `json:"skip_verify"`
	Profile           *profile.FlashProfile `json:"profile"`
	Remember          bool                  `json:"remember"`
}

func (s *server) handleFlash(w http.ResponseWriter, r *http.Request) {
	var req flashRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid request"))
		return
	}
	p := req.Profile
	if p != nil {
		p.SchemaVersion = profile.SchemaVersion
		if err := p.Validate(); err != nil {
			p.Clear()
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
	}
	if ok, why := flash.CanWriteDevices(); !ok {
		writeErr(w, http.StatusForbidden, errors.New(why))
		return
	}
	d, err := devices.Find(r.Context(), req.Device)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.ConfirmDevice != d.Path {
		writeErr(w, http.StatusBadRequest, errors.New("confirm the card to erase"))
		return
	}
	if err := job.CheckDevice(d, req.AllowNonRemovable); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var src job.Source
	if req.Path != "" {
		if st, err := os.Stat(req.Path); err != nil || st.IsDir() {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("image file not found: %s", req.Path))
			return
		}
		src.Path = req.Path
	} else {
		c, err := req.client(s.version)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		images, err := c.ListImages(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		for i := range images {
			if images[i].ID == req.ImageID {
				src = job.Source{Client: c, Image: &images[i]}
			}
		}
		if src.Image == nil {
			writeErr(w, http.StatusBadRequest, errors.New("that image is no longer published; reload the list"))
			return
		}
	}

	ctx, ok := s.startJob(w, "write", d)
	if !ok {
		return
	}

	if req.Remember && p != nil {
		server := ""
		if src.Client != nil {
			server = req.Server
		}
		_ = profile.SavePrefs(profile.PrefsFrom(server, p))
	}

	go func() {
		rep, err := job.Run(ctx, job.Request{
			Source: src, Device: d, Profile: p,
			AllowNonRemovable: req.AllowNonRemovable, SkipVerify: req.SkipVerify,
		}, s.progress)
		if errors.Is(err, context.Canceled) {
			err = errors.New("cancelled; the card is incomplete and must be written again")
		}
		s.finishJob(err, func(st *jobState) { st.Report = rep })
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

type backupRequest struct {
	Device            string `json:"device"`
	Out               string `json:"out"`
	Shrink            bool   `json:"shrink"`
	AllowNonRemovable bool   `json:"allow_non_removable"`
}

func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req backupRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid request"))
		return
	}
	if !filepath.IsAbs(req.Out) {
		writeErr(w, http.StatusBadRequest, errors.New("give the full path of the backup file"))
		return
	}
	if err := backup.CheckOutput(req.Out); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if ok, why := flash.CanWriteDevices(); !ok {
		writeErr(w, http.StatusForbidden, errors.New(why))
		return
	}
	d, err := devices.Find(r.Context(), req.Device)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := job.CheckBackupDevice(d, req.AllowNonRemovable); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, ok := s.startJob(w, "backup", d)
	if !ok {
		return
	}
	go func() {
		rep, err := job.Backup(ctx, job.BackupRequest{
			Device: d, Out: req.Out, Shrink: req.Shrink, AllowNonRemovable: req.AllowNonRemovable,
		}, s.progress)
		if errors.Is(err, context.Canceled) {
			err = errors.New("cancelled; no backup file was saved")
		}
		s.finishJob(err, func(st *jobState) { st.Backup = rep })
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// startJob claims the single job slot; it writes the error response and
// returns false when a job is already running.
func (s *server) startJob(w http.ResponseWriter, kind string, d devices.Device) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		writeErr(w, http.StatusConflict, errors.New("a card is already being written or backed up"))
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.state = jobState{Kind: kind, Running: true, Stage: job.StageOpen, Device: d.Describe()}
	return ctx, true
}

func (s *server) progress(e job.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Stage, s.state.Fraction, s.state.Bytes = e.Stage, e.Fraction, e.Bytes
	if e.Message != "" && e.Stage != job.StageOpen {
		s.state.Messages = append(s.state.Messages, e.Message)
	}
}

func (s *server) finishJob(err error, done func(*jobState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.state.Running = false
	s.cancel = nil
	if err != nil {
		s.state.Error = err.Error()
		var ve *profile.ValidationError
		if errors.As(err, &ve) {
			s.state.Problems = ve.Problems
		}
		return
	}
	s.state.Stage, s.state.Fraction = "done", 1
	done(&s.state)
}

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	st := s.state
	st.Messages = append([]string(nil), s.state.Messages...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

func (s *server) cancelJob() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.cancelJob()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleForget(w http.ResponseWriter, r *http.Request) {
	if err := profile.RemovePrefs(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleQuit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	running := s.state.Running
	s.mu.Unlock()
	if running {
		writeErr(w, http.StatusConflict, errors.New("a card is still being written or backed up; cancel it first"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.quit()
	}()
}
