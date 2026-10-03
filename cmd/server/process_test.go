package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer guards a child process's output: os/exec drains Stdout/Stderr
// from goroutines that live until Wait returns, while the tests snapshot the
// logs while the server is still running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serverProc is a running server binary started by startServer.
type serverProc struct {
	t    *testing.T
	cmd  *exec.Cmd
	logs *syncBuffer
	done chan error // receives cmd.Wait's result once
	port int
}

// subprocessCoverDir is where `go test -cover` collects coverage data, or ""
// when the tests run without coverage. A server binary built with -cover and
// started with GOCOVERDIR set to it writes its counters there at exit, so the
// process-level tests produce coverage for cmd/server (main itself is only
// reachable through a real process). `go test -cover` prints only the test
// binary's own counters; to see the subprocess data too, keep the directory
// and read it with covdata:
//
//	mkdir -p /tmp/cov && go test -cover ./cmd/server/ -args -test.gocoverdir=/tmp/cov
//	go tool covdata percent -i=/tmp/cov -pkg=github.com/SamyRai/cityFinder/cmd/server
func subprocessCoverDir() string {
	if testing.CoverMode() == "" {
		return ""
	}
	if f := flag.Lookup("test.gocoverdir"); f != nil {
		return f.Value.String()
	}
	return ""
}

// buildServer compiles the real server binary into a temp dir, instrumented
// for coverage when the tests themselves run with -cover.
func buildServer(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "cityfinder-server")
	args := []string{"build", "-o", binPath}
	if subprocessCoverDir() != "" {
		args = append(args, "-cover")
	}
	build := exec.Command("go", append(args, "./cmd/server")...)
	build.Dir = findRepoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		require.NoError(t, err, "go build failed: %s", out)
	}
	return binPath
}

// writeServerConfig writes a config that points at copies of the fixture
// datasets in a temp dir and returns its absolute path. Absolute paths
// throughout: config.LoadConfig resolves a relative CONFIG_PATH against the
// process CWD and a relative datasets_folder against the config file's
// directory, so the temp dirs are referenced directly.
func writeServerConfig(t *testing.T) string {
	t.Helper()
	rootDir := findRepoRoot(t)
	dataDir := t.TempDir()
	for _, name := range []string{"allCountries.txt", "zipCodes.txt"} {
		require.NoError(t, copyFile(filepath.Join(rootDir, "testdata", name), filepath.Join(dataDir, name)))
	}
	cfg := config.Config{
		DatasetsFolder:      dataDir,
		AllCitiesFile:       "allCountries.txt",
		PostalCodesFile:     "zipCodes.txt",
		NameIndexFile:       "name_index_proc_test.gob",
		PostalCodeIndexFile: "postal_code_index_proc_test.gob",
		S2:                  config.S2{IndexFile: "s2index_proc_test.gob"},
	}
	cfgBytes, err := json.Marshal(cfg)
	require.NoError(t, err)
	cfgPath := filepath.Join(t.TempDir(), "config_proc_test.json")
	require.NoError(t, os.WriteFile(cfgPath, cfgBytes, 0o600))
	return cfgPath
}

// freePort grabs a free port and releases it for the server to bind.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// startServer starts binPath with env (on top of the test's own) and returns
// immediately; the process is killed on test cleanup.
func startServer(t *testing.T, binPath string, port int, env ...string) *serverProc {
	t.Helper()
	p := &serverProc{t: t, logs: &syncBuffer{}, done: make(chan error, 1), port: port}
	p.cmd = exec.Command(binPath)
	p.cmd.Dir = findRepoRoot(t)
	p.cmd.Env = append(os.Environ(), env...)
	if dir := subprocessCoverDir(); dir != "" {
		p.cmd.Env = append(p.cmd.Env, "GOCOVERDIR="+dir)
	}
	p.cmd.Stdout = p.logs
	p.cmd.Stderr = p.logs
	require.NoError(t, p.cmd.Start())
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() }) // never leak the process on a failing path
	return p
}

// startHealthyServer starts the server over the fixture datasets and waits
// until /healthz answers 200.
func startHealthyServer(t *testing.T) *serverProc {
	t.Helper()
	port := freePort(t)
	p := startServer(t, buildServer(t), port,
		"CONFIG_PATH="+writeServerConfig(t), "PORT="+strconv.Itoa(port))
	p.waitHealthy()
	return p
}

// waitHealthy polls /healthz over real HTTP (this also exercises the
// endpoint outside httptest). Building the indexes can take a moment.
func (p *serverProc) waitHealthy() {
	p.t.Helper()
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", p.port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case waitErr := <-p.done:
			p.t.Fatalf("server exited before becoming healthy: %v\nlogs:\n%s", waitErr, p.logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	p.t.Fatalf("server did not become healthy in time; logs:\n%s", p.logs.String())
}

// waitExit waits up to d for the process to exit and returns Wait's error.
func (p *serverProc) waitExit(d time.Duration) error {
	p.t.Helper()
	select {
	case err := <-p.done:
		return err
	case <-time.After(d):
		p.t.Fatalf("server did not exit within %v; logs:\n%s", d, p.logs.String())
		return nil
	}
}

// waitLog waits until the process output contains substr.
func (p *serverProc) waitLog(substr string) {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains([]byte(p.logs.String()), []byte(substr)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.t.Fatalf("log never contained %q; logs:\n%s", substr, p.logs.String())
}

// TestServerGracefulShutdownSignal builds the real server binary, starts it
// on a random port with the fixture datasets, waits for /healthz over real
// HTTP, sends SIGTERM, and asserts a clean exit 0 within a few seconds.
func TestServerGracefulShutdownSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level signal test skipped in short mode")
	}
	p := startHealthyServer(t)

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))

	require.NoError(t, p.waitExit(10*time.Second),
		"server must exit with code 0 after SIGTERM; logs:\n%s", p.logs.String())
	assert.Contains(t, p.logs.String(), "shutting down")
}

// TestServerSecondSignalForcesExit: with a stalled in-flight connection the
// graceful drain would run for the full shutdown timeout (10 s); a second
// SIGTERM during the drain must terminate the process immediately instead of
// being swallowed by the already-fired signal context.
func TestServerSecondSignalForcesExit(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level signal test skipped in short mode")
	}
	p := startHealthyServer(t)

	// A half-sent request keeps the connection in flight, so Shutdown waits.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.port))
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("GET /healthz HTTP/1.1\r\nHost: x\r\n"))
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	p.waitLog("shutting down")
	start := time.Now()
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))

	waitErr := p.waitExit(5 * time.Second)
	assert.Error(t, waitErr, "a forced exit is a signal death, not a clean exit 0")
	assert.Less(t, time.Since(start), 5*time.Second, "second signal must not wait out the 10 s drain")
}

// exitCode returns the process exit status from Wait's error (0 for nil).
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	require.True(t, errors.As(err, &ee), "unexpected wait error: %v", err)
	return ee.ExitCode()
}

// TestServerBadPortExitsBeforeIndexLoad: an invalid PORT is a startup error
// (exit 1, named in the log) that must not wait for the index load. The
// config is valid and cold, so a process that got past the PORT check would
// start building indexes.
func TestServerBadPortExitsBeforeIndexLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level test skipped in short mode")
	}
	bin := buildServer(t)
	for _, port := range []string{"abc", "0", "99999", "-1"} {
		p := startServer(t, bin, 0, "CONFIG_PATH="+writeServerConfig(t), "PORT="+port)
		err := p.waitExit(5 * time.Second)
		assert.Equal(t, 1, exitCode(t, err), "PORT=%s; logs:\n%s", port, p.logs.String())
		assert.Contains(t, p.logs.String(), "invalid PORT", "PORT=%s", port)
		assert.NotContains(t, p.logs.String(), "Building", "PORT=%s must fail before any index is built", port)
	}
}

// TestServerBadConfigExitsNonZero: a missing or malformed config file stops
// the process with exit 1 and a "Failed to load config" line.
func TestServerBadConfigExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level test skipped in short mode")
	}
	bin := buildServer(t)
	notJSON := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(notJSON, []byte("{not json"), 0o600))
	for name, path := range map[string]string{
		"missing":  filepath.Join(t.TempDir(), "absent.json"),
		"not json": notJSON,
	} {
		p := startServer(t, bin, 0, "CONFIG_PATH="+path)
		err := p.waitExit(10 * time.Second)
		assert.Equal(t, 1, exitCode(t, err), "%s; logs:\n%s", name, p.logs.String())
		assert.Contains(t, p.logs.String(), "Failed to load config", name)
	}
}

// TestServerPortInUseExitsNonZero: a listen failure after initialization is
// fatal (exit 1), not a silent hang.
func TestServerPortInUseExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level test skipped in short mode")
	}
	occupied, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	p := startServer(t, buildServer(t), port,
		"CONFIG_PATH="+writeServerConfig(t), "PORT="+strconv.Itoa(port))
	waitErr := p.waitExit(30 * time.Second)
	assert.Equal(t, 1, exitCode(t, waitErr), "logs:\n%s", p.logs.String())
	assert.Contains(t, p.logs.String(), "Server error")
}

// TestServerPprofListener: PPROF_ADDR opens the opt-in profiling listener on
// its own address, and it goes away with the server on shutdown.
func TestServerPprofListener(t *testing.T) {
	if testing.Short() {
		t.Skip("process-level test skipped in short mode")
	}
	pprofPort := freePort(t)
	port := freePort(t)
	p := startServer(t, buildServer(t), port,
		"CONFIG_PATH="+writeServerConfig(t), "PORT="+strconv.Itoa(port),
		"PPROF_ADDR=127.0.0.1:"+strconv.Itoa(pprofPort))
	p.waitHealthy()
	p.waitLog("pprof listening on")

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/cmdline", pprofPort))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	require.NoError(t, p.waitExit(10*time.Second))
}
