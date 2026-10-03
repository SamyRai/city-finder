package config

import (
	"bytes"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// env returns a lookup over a fixed environment.
func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func TestLoadRuntimeDefaults(t *testing.T) {
	rt, err := loadRuntime(env(nil))
	require.NoError(t, err)
	assert.Equal(t, Runtime{Port: "3000", ConfigPath: "config.json"}, rt)
}

func TestLoadRuntimeReadsEveryVariable(t *testing.T) {
	rt, err := loadRuntime(env(map[string]string{
		"PORT": "8080", "PPROF_ADDR": "127.0.0.1:6060", "CONFIG_PATH": "/etc/cf.json",
	}))
	require.NoError(t, err)
	assert.Equal(t, Runtime{Port: "8080", PprofAddr: "127.0.0.1:6060", ConfigPath: "/etc/cf.json"}, rt)
}

// TestLoadRuntimePortValidation: PORT must be a plain decimal in 1-65535;
// anything else fails before any index is loaded.
func TestLoadRuntimePortValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"":      "3000", // empty behaves like unset
		"1":     "1",
		"3000":  "3000",
		"08080": "8080", // canonical form
		"65535": "65535",
	} {
		rt, err := loadRuntime(env(map[string]string{"PORT": raw}))
		require.NoError(t, err, "PORT=%q", raw)
		assert.Equal(t, want, rt.Port, "PORT=%q", raw)
	}
	for _, raw := range []string{"abc", "0", "-1", "65536", "99999", "+80", " 80", "80 ", "8_0", "0x50", "1.5", "99999999999999999999"} {
		_, err := loadRuntime(env(map[string]string{"PORT": raw}))
		require.Error(t, err, "PORT=%q", raw)
		assert.Contains(t, err.Error(), "invalid PORT")
	}
}

// TestLoadRuntimeConfigFileFallback pins the deprecated CONFIG_FILE: it only
// applies when CONFIG_PATH resolves to the empty string, and using it logs a
// deprecation notice.
func TestLoadRuntimeConfigFileFallback(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rt, err := loadRuntime(env(map[string]string{"CONFIG_PATH": "", "CONFIG_FILE": "/legacy.json"}))
	require.NoError(t, err)
	assert.Equal(t, "/legacy.json", rt.ConfigPath)
	assert.Contains(t, buf.String(), "CONFIG_FILE is deprecated")

	// CONFIG_PATH wins when non-empty; CONFIG_FILE is ignored without the notice.
	buf.Reset()
	rt, err = loadRuntime(env(map[string]string{"CONFIG_PATH": "/new.json", "CONFIG_FILE": "/legacy.json"}))
	require.NoError(t, err)
	assert.Equal(t, "/new.json", rt.ConfigPath)
	assert.Empty(t, buf.String())

	// Unset CONFIG_PATH selects the default, not CONFIG_FILE.
	rt, err = loadRuntime(env(map[string]string{"CONFIG_FILE": "/legacy.json"}))
	require.NoError(t, err)
	assert.Equal(t, "config.json", rt.ConfigPath)
}

func TestRuntimeLoadConfig(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.json", validConfigJSON)
	cfg, err := Runtime{ConfigPath: path}.LoadConfig()
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.S2.IndexFile)

	t.Setenv("CONFIG_FILE", "")
	_, err = Runtime{ConfigPath: ""}.LoadConfig()
	assert.Error(t, err, "no path and no CONFIG_FILE")
}
