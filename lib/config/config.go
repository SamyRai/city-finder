package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	DatasetsFolder      string `json:"datasets_folder"`
	AllCitiesURL        string `json:"all_cities_url"`
	PostalCodesURL      string `json:"postal_codes_url"`
	AllCitiesFile       string `json:"all_cities_file"`
	PostalCodesFile     string `json:"postal_codes_file"`
	AllCitiesZip        string `json:"all_cities_zip"`
	PostalCodesZip      string `json:"postal_codes_zip"`
	NameIndexFile       string `json:"name_index_file"`
	PostalCodeIndexFile string `json:"postal_code_index_file"`
	S2                  S2     `json:"s2"`
}

type S2 struct {
	MinLevel  int    `json:"min_level"`
	MaxLevel  int    `json:"max_level"`
	MaxCells  int    `json:"max_cells"`
	IndexFile string `json:"index_file"`
}

// LoadConfig loads the JSON configuration at configPath.
//
// Path resolution is project-root-independent (v1.1 semantics; v1.0 resolved
// every relative path against a "project root" discovered by walking up from
// the CWD to a go.mod file and failed outright when none existed, even for an
// absolute CONFIG_PATH — which forced the container image to ship a fake
// go.mod marker):
//
//   - An absolute configPath is opened as-is; no project-root discovery is
//     performed anywhere on this path.
//   - A relative configPath resolves against the process working directory.
//     This is a deliberate v1.1 behavior change from project-root-relative
//     resolution: CWD-relative is the standard meaning of a relative path,
//     and Go API users pass relative paths from their working directory.
//
// With an empty configPath, the CONFIG_FILE environment variable is honored
// (the CONFIG_PATH env var belongs to cmd/server's main, which feeds it into
// LoadConfig as configPath).
//
// datasets_folder inside the config file: absolute values are kept verbatim;
// relative values resolve against the directory containing the config file
// (v1.0 joined them onto the discovered project root — another v1.1 change).
// For the shipped repo-root config.json whose datasets_folder is "datasets",
// the config directory IS the repository root, so the default development
// layout resolves exactly as before.
func LoadConfig(configPath string) (*Config, error) {
	cfg := &Config{}

	if configPath == "" {
		configPath = os.Getenv("CONFIG_FILE")
	}

	if configPath == "" {
		return nil, fmt.Errorf("no config file provided")
	}

	// A relative config path resolves against the process working directory
	// (see the doc comment for the v1.0 → v1.1 semantics change).
	if !filepath.IsAbs(configPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %v", err)
		}
		configPath = filepath.Join(cwd, configPath)
	} else {
		configPath = filepath.Clean(configPath)
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %v", err)
	}
	decoder := json.NewDecoder(file)
	decodeErr := decoder.Decode(cfg)
	closeErr := file.Close()

	if decodeErr != nil {
		return nil, fmt.Errorf("failed to decode config file: %v", decodeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close config file: %v", closeErr)
	}

	// Relative datasets_folder values resolve against the config file's
	// directory (v1.0 joined them onto the discovered project root; another
	// v1.1 change, see the LoadConfig doc comment). Absolute values are kept
	// verbatim, matching how the initializer joins the index file names onto
	// cfg.DatasetsFolder.
	if !filepath.IsAbs(cfg.DatasetsFolder) {
		cfg.DatasetsFolder = filepath.Join(filepath.Dir(configPath), cfg.DatasetsFolder)
	}

	return cfg, nil
}
