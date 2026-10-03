package main

import (
	"bytes"
	"encoding/json"
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

// buildServer compiles the real server binary into a temp dir.
func buildServer(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "cityfinder-server")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/server")
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
