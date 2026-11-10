package bootfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// firstrunEnv runs firstrun.sh against stub meteorcli, systemctl, systemd-run
// and curl that record their arguments in calls.log.
type firstrunEnv struct {
	t       *testing.T
	boot    string
	config  string
	bin     string
	calls   string
	logFile string
}

const testKey = "key_supersecret123"

func newFirstrunEnv(t *testing.T) *firstrunEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("firstrun.sh runs on the Pi; needs bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir()
	e := &firstrunEnv{
		t:       t,
		boot:    filepath.Join(dir, "boot"),
		config:  filepath.Join(dir, "etc-meteorcli"),
		bin:     filepath.Join(dir, "bin"),
		calls:   filepath.Join(dir, "calls.log"),
		logFile: filepath.Join(dir, "firstrun.log"),
	}
	for _, d := range []string{e.boot, e.config, e.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e.write(filepath.Join(e.boot, FirstrunFile), string(FirstrunScript), 0o644)
	e.write(filepath.Join(e.boot, MeteorEnvFile), "METEOR_DOMAIN=fleet.example.com\nMETEOR_API_KEY="+testKey+"\n", 0o644)
	record := `echo "$(basename "$0") $*" >>"$CALLS"` + "\n"
	e.stub("systemctl", record)
	e.stub("systemd-run", record)
	e.stub("hostname", "echo pi-kitchen\n")
	return e
}

func (e *firstrunEnv) write(path, body string, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		e.t.Fatal(err)
	}
}

func (e *firstrunEnv) stub(name, body string) {
	e.write(filepath.Join(e.bin, name), "#!/bin/bash\n"+body, 0o755)
}

// meteorcli stub. requestOK decides whether request-token succeeds (and writes
// a claim secret); newRun decides whether `run --help` advertises the wait.
func (e *firstrunEnv) meteorcli(requestOK, newRun bool) {
	help := "usage: meteorcli run"
	if newRun {
		help += " If a token request is still pending, it first waits for approval (waits for pending enrollment)."
	}
	e.stub("meteorcli", `echo "meteorcli $*" >>"$CALLS"
case "$1" in
  --version) echo "meteorcli 0.1.0" ;;
  run) echo "`+help+`" ;;
  request-token)
    `+map[bool]string{true: `echo '{"request_id":"r","claim_secret":"c"}' >"$METEORCLI_CONFIG_DIR/claim-secret"`, false: `echo "error: enrollment request failed" >&2; exit 1`}[requestOK]+` ;;
esac
exit 0
`)
}

func (e *firstrunEnv) run() (string, error) {
	e.t.Helper()
	cmd := exec.Command("bash", filepath.Join(e.boot, FirstrunFile))
	cmd.Env = []string{
		"PATH=" + e.bin + ":/usr/bin:/bin",
		"HOME=" + e.t.TempDir(),
		"CALLS=" + e.calls,
		"METEOR_FIRSTRUN_ALLOW_NONROOT=1",
		"METEORCLI_CONFIG_DIR=" + e.config,
		"METEOR_FIRSTRUN_LOG=" + e.logFile,
		"METEOR_RETRIES=2",
		"METEOR_RETRY_SECONDS=0",
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *firstrunEnv) callLog() string {
	data, _ := os.ReadFile(e.calls)
	return string(data)
}

func (e *firstrunEnv) bootHas(name string) bool {
	_, err := os.Stat(filepath.Join(e.boot, name))
	return err == nil
}

func TestFirstrunRequestsTokenThenRemovesItself(t *testing.T) {
	e := newFirstrunEnv(t)
	e.meteorcli(true, true)
	out, err := e.run()
	if err != nil {
		t.Fatalf("firstrun failed: %v\n%s", err, out)
	}
	calls := e.callLog()
	for _, want := range []string{
		"meteorcli config --domain fleet.example.com --api-key " + testKey,
		"meteorcli request-token --name pi-kitchen --wait 0",
		"systemctl enable meteorcli.service",
		"systemctl restart meteorcli.service",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("missing call %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "systemd-run") {
		t.Errorf("new meteorcli must wait in meteorcli.service, not a transient unit:\n%s", calls)
	}
	if e.bootHas(FirstrunFile) || e.bootHas(MeteorEnvFile) {
		t.Error("script and .meteorenv must be removed after the token request")
	}
	logData, _ := os.ReadFile(e.logFile)
	if strings.Contains(out+string(logData), testKey) {
		t.Error("the enrollment key was logged")
	}
}

func TestFirstrunKeepsFilesWhenRequestFails(t *testing.T) {
	e := newFirstrunEnv(t)
	e.meteorcli(false, true)
	out, err := e.run()
	if err == nil {
		t.Fatalf("expected failure:\n%s", out)
	}
	if !e.bootHas(FirstrunFile) || !e.bootHas(MeteorEnvFile) {
		t.Error("files must stay when no request was sent, so the run can be repeated")
	}
	calls := e.callLog()
	if n := strings.Count(calls, "request-token"); n != 2 {
		t.Errorf("request-token tried %d times, want METEOR_RETRIES=2:\n%s", n, calls)
	}
	if strings.Contains(calls, "systemctl") {
		t.Errorf("must not start the agent without a request:\n%s", calls)
	}
}

func TestFirstrunWithOlderMeteorcliWaitsInTransientUnit(t *testing.T) {
	e := newFirstrunEnv(t)
	e.meteorcli(true, false)
	if out, err := e.run(); err != nil {
		t.Fatalf("firstrun failed: %v\n%s", err, out)
	}
	calls := e.callLog()
	if !strings.Contains(calls, "systemd-run --unit=meteorcli-claim") || strings.Contains(calls, "\nsystemctl restart") {
		t.Errorf("calls:\n%s", calls)
	}
	if e.bootHas(FirstrunFile) || e.bootHas(MeteorEnvFile) {
		t.Error("files must be removed after the token request")
	}
}

func TestFirstrunResumesAfterRequestWasSent(t *testing.T) {
	e := newFirstrunEnv(t)
	e.meteorcli(true, true)
	e.write(filepath.Join(e.config, "claim-secret"), `{"request_id":"r","claim_secret":"c"}`, 0o600)
	if out, err := e.run(); err != nil {
		t.Fatalf("firstrun failed: %v\n%s", err, out)
	}
	calls := e.callLog()
	if strings.Contains(calls, "request-token") {
		t.Errorf("must not send a second request:\n%s", calls)
	}
	if !strings.Contains(calls, "systemctl restart meteorcli.service") || e.bootHas(FirstrunFile) {
		t.Errorf("calls:\n%s", calls)
	}
}

func TestFirstrunInstallsMissingMeteorcli(t *testing.T) {
	e := newFirstrunEnv(t)
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		if _, err := os.Stat(filepath.Join(dir, "meteorcli")); err == nil {
			t.Skip("meteorcli is installed on this machine")
		}
	}
	// The stub "installer" puts meteorcli on PATH, as install-meteorcli.sh does.
	e.meteorcli(true, true)
	if err := os.Rename(filepath.Join(e.bin, "meteorcli"), filepath.Join(e.boot, "..", "meteorcli.pending")); err != nil {
		t.Fatal(err)
	}
	installer := filepath.Join(e.boot, "..", "installer.sh")
	e.write(installer, "mv "+filepath.Join(e.boot, "..", "meteorcli.pending")+" "+filepath.Join(e.bin, "meteorcli")+"\n", 0o644)
	e.stub("curl", `echo "curl $*" >>"$CALLS"; out=""; while [ $# -gt 0 ]; do [ "$1" = -o ] && out="$2"; shift; done; cp `+installer+` "$out"`+"\n")
	out, err := e.run()
	if err != nil {
		t.Fatalf("firstrun failed: %v\n%s", err, out)
	}
	calls := e.callLog()
	if !strings.Contains(calls, "curl -fsSL https://github.com/meteor-edge/meteor-cloud/releases/latest/download/install-meteorcli.sh") ||
		!strings.Contains(calls, "meteorcli request-token") {
		t.Errorf("calls:\n%s", calls)
	}
}

func TestFirstrunRejectsMissingKey(t *testing.T) {
	e := newFirstrunEnv(t)
	e.meteorcli(true, true)
	e.write(filepath.Join(e.boot, MeteorEnvFile), "METEOR_DOMAIN=fleet.example.com\n", 0o644)
	if out, err := e.run(); err == nil || !strings.Contains(out, "needs METEOR_DOMAIN and METEOR_API_KEY") {
		t.Fatalf("err=%v out=%s", err, out)
	}
	if strings.Contains(e.callLog(), "request-token") || !e.bootHas(FirstrunFile) {
		t.Error("must not request or remove anything without a key")
	}
}
