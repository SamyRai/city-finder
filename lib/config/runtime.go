package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
)

// Process environment variables read by the binaries. This file is the only
// place in the repository that reads them: main loads a Runtime once and
// passes it down.
const (
	envPort       = "PORT"
	envPprofAddr  = "PPROF_ADDR"
	envConfigPath = "CONFIG_PATH"
	// envConfigFile is the legacy spelling of CONFIG_PATH, honored only when
	// the config path resolves to the empty string (CONFIG_PATH set to ""
	// or a LoadConfig("") call). Kept so existing deployments keep booting;
	// new deployments should set CONFIG_PATH.
	envConfigFile = "CONFIG_FILE"

	defaultPort       = "3000"
	defaultConfigPath = "config.json"
)

// Runtime is the process-level settings that come from the environment
// rather than the config file.
type Runtime struct {
	// Port is the validated listen port (decimal, 1-65535).
	Port string
	// PprofAddr is the opt-in pprof listener address; "" disables it.
	PprofAddr string
	// ConfigPath is the config file to load: CONFIG_PATH when set (even to
	// an empty string, which then falls back to the deprecated CONFIG_FILE),
	// otherwise "config.json" relative to the working directory.
	ConfigPath string
}

// LoadRuntime reads PORT, PPROF_ADDR and CONFIG_PATH (and the deprecated
// CONFIG_FILE) from the process environment. It fails on an invalid PORT so
// a misconfigured process exits before the slow index load.
func LoadRuntime() (Runtime, error) {
	return loadRuntime(os.LookupEnv)
}

func loadRuntime(lookup func(string) (string, bool)) (Runtime, error) {
	port, err := parsePort(lookup)
	if err != nil {
		return Runtime{}, err
	}
	rt := Runtime{Port: port, ConfigPath: configPathFrom(lookup)}
	rt.PprofAddr, _ = lookup(envPprofAddr)
	return rt, nil
}

// LoadConfig loads the config file the Runtime resolved.
func (r Runtime) LoadConfig() (*Config, error) {
	return LoadConfig(r.ConfigPath)
}

// parsePort returns PORT in canonical decimal form, or the default when it
// is unset or empty.
func parsePort(lookup func(string) (string, bool)) (string, error) {
	raw, _ := lookup(envPort)
	if raw == "" {
		return defaultPort, nil
	}
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 {
		return "", fmt.Errorf("invalid %s %q: must be a number between 1 and 65535", envPort, raw)
	}
	return strconv.FormatUint(n, 10), nil
}

// configPathFrom resolves the config file path: CONFIG_PATH when set (even
// to ""), else the default; an empty result falls back to the deprecated
// CONFIG_FILE.
func configPathFrom(lookup func(string) (string, bool)) string {
	path, exists := lookup(envConfigPath)
	if !exists {
		path = defaultConfigPath
	}
	if path == "" {
		return configFileFallback(lookup)
	}
	return path
}

// configFileFallback returns the deprecated CONFIG_FILE value, logging a
// deprecation notice when it is used.
func configFileFallback(lookup func(string) (string, bool)) string {
	path, _ := lookup(envConfigFile)
	if path != "" {
		log.Printf("%s is deprecated; set %s instead", envConfigFile, envConfigPath)
	}
	return path
}

// LoadFromEnv resolves the config file the way the binaries do (see
// Runtime.ConfigPath) and loads it, without validating PORT: for tools such
// as cmd/build-index that only need the config.
func LoadFromEnv() (*Config, error) {
	return LoadConfig(configPathFrom(os.LookupEnv))
}
