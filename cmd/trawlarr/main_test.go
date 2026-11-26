package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var binary string

// TestMain builds the real binary once so the tests exercise the shipped
// entrypoint, flags and shutdown path rather than a library shim.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "trawlarr-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	binary = filepath.Join(dir, "trawlarr")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeConfig(t *testing.T, dir string, adminPort, phishPort int) string {
	t.Helper()
	cfg := map[string]any{
		"admin_server":      map[string]any{"listen_url": fmt.Sprintf("127.0.0.1:%d", adminPort)},
		"phish_server":      map[string]any{"listen_url": fmt.Sprintf("127.0.0.1:%d", phishPort), "public_url": fmt.Sprintf("http://127.0.0.1:%d", phishPort)},
		"db_path":           filepath.Join(dir, "trawlarr.db"),
		"data_dir":          dir,
		"log_level":         "error",
		"session_ttl_hours": 1,
	}
	path := filepath.Join(dir, "config.json")
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, url string, timeout time.Duration) *http.Response {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			return resp
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never became reachable", url)
	return nil
}

func TestServerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level test in -short mode")
	}
	dir := t.TempDir()
	adminPort, phishPort := freePort(t), freePort(t)
	cfgPath := writeConfig(t, dir, adminPort, phishPort)

	cmd := exec.Command(binary, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TRAWLARR_ADMIN_PASSWORD=a-process-test-password")
	cmd.Dir = dir
	var logs strings.Builder
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}()

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	resp := waitFor(t, adminURL+"/api/v1/health", 20*time.Second)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("admin health = %d %s", resp.StatusCode, body)
	}

	// The UI is served by the same listener.
	resp = waitFor(t, adminURL+"/", 5*time.Second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UI index = %d", resp.StatusCode)
	}

	// The phishing listener answers health but never the admin API.
	phishURL := fmt.Sprintf("http://127.0.0.1:%d", phishPort)
	resp = waitFor(t, phishURL+"/healthz", 10*time.Second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("phish health = %d", resp.StatusCode)
	}
	resp = waitFor(t, phishURL+"/api/v1/me", 5*time.Second)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("admin API served on the phishing listener")
	}

	// Login works with the seeded password.
	login, err := http.Post(adminURL+"/api/v1/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"a-process-test-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", login.StatusCode)
	}

	// Graceful shutdown on SIGTERM.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown returned %v\nlogs:\n%s", err, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestVersionSubcommand(t *testing.T) {
	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version failed: %v %s", err, out)
	}
	if !strings.HasPrefix(string(out), "trawlarr ") {
		t.Fatalf("unexpected version output: %s", out)
	}
}

func TestImportRequiresSource(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cmd := exec.Command(binary, "import-gophish", "-config", cfgPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("import without -from succeeded: %s", out)
	}
	if !strings.Contains(string(out), "import failed") {
		t.Fatalf("unexpected import error: %s", out)
	}
}

func TestMCPRefusesWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, freePort(t), freePort(t))
	cmd := exec.Command(binary, "mcp", "-config", cfgPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("mcp started while disabled: %s", out)
	}
	if !strings.Contains(string(out), "disabled") {
		t.Fatalf("unexpected error: %s", out)
	}
}

func TestHelpListsSubcommands(t *testing.T) {
	out, err := exec.Command(binary, "help").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"import-gophish", "mcp", "version"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("help output is missing %q:\n%s", want, out)
		}
	}
}

// TestSinglePortMode covers the combined listener: one address serving the
// admin API, the UI and the tracking endpoints.
func TestSinglePortMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level test in -short mode")
	}
	dir := t.TempDir()
	port := freePort(t)
	cfgPath := writeConfig(t, dir, port, port)
	cmd := exec.Command(binary, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TRAWLARR_ADMIN_PASSWORD=a-process-test-password")
	cmd.Dir = dir
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp := waitFor(t, base+"/api/v1/health", 20*time.Second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
	// The UI is served from the same port.
	resp = waitFor(t, base+"/", 5*time.Second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UI = %d", resp.StatusCode)
	}
	// Tracking endpoints answer too, and unknown ids get a plain 404.
	resp = waitFor(t, base+"/open.gif?rid=nope", 5*time.Second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tracking = %d", resp.StatusCode)
	}
	cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
}

// powerLogin returns a session cookie for the seeded admin.
func powerLogin(t *testing.T, baseURL, password string) *http.Cookie {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/v1/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "trawlarr_session" {
			return cookie
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func powerPost(t *testing.T, baseURL, action string, cookie *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/power/"+action, nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPowerShutdownStopsTheProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level test in -short mode")
	}
	dir := t.TempDir()
	port := freePort(t)
	cfgPath := writeConfig(t, dir, port, freePort(t))
	cmd := exec.Command(binary, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TRAWLARR_ADMIN_PASSWORD=power-test-password")
	cmd.Dir = dir
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp := waitFor(t, base+"/api/v1/health", 20*time.Second)
	resp.Body.Close()

	cookie := powerLogin(t, base, "power-test-password")
	if status := powerPost(t, base, "shutdown", cookie); status != http.StatusAccepted {
		t.Fatalf("shutdown = %d", status)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown exit = %v\n%s", err, logs.String())
		}
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatal("server did not stop after the shutdown request")
	}
}

func TestPowerRestartKeepsServing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level test in -short mode")
	}
	dir := t.TempDir()
	port := freePort(t)
	cfgPath := writeConfig(t, dir, port, freePort(t))
	setLogLevel(t, cfgPath, "info")
	cmd := exec.Command(binary, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TRAWLARR_ADMIN_PASSWORD=power-test-password")
	cmd.Dir = dir
	// The logs are read while the process is still running (and after it
	// re-execs), so they go to a file rather than a strings.Builder.
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	resp := waitFor(t, base+"/api/v1/health", 20*time.Second)
	resp.Body.Close()

	cookie := powerLogin(t, base, "power-test-password")
	if status := powerPost(t, base, "restart", cookie); status != http.StatusAccepted {
		t.Fatalf("restart = %d", status)
	}

	// The address goes quiet, then answers again from the re-exec'd process.
	time.Sleep(powerDelay + 200*time.Millisecond)
	deadline := time.Now().Add(25 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				recovered = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	written, _ := os.ReadFile(logPath)
	if !recovered {
		t.Fatalf("server did not come back after restart\n%s", written)
	}
	if !strings.Contains(string(written), "re-executing the server") {
		t.Fatalf("restart path not taken\n%s", written)
	}
	// The restart is a re-exec, so the process id is unchanged and the
	// session survives in the database.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/me", nil)
	req.AddCookie(cookie)
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("session lost after restart: %d", meResp.StatusCode)
	}
}

const powerDelay = 300 * time.Millisecond

// setLogLevel rewrites log_level in an existing config file.
func setLogLevel(t *testing.T, path, level string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["log_level"] = level
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
