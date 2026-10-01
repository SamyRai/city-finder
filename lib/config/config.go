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
	Admin1CodesFile     string `json:"admin1_codes_file"`
	Admin1CodesURL      string `json:"admin1_codes_url"`
	// ExcludeAdminDivisions drops GeoNames feature class A rows — countries
	// (PCLI), states/provinces (ADM1/ADM2), districts (ADM3/ADM4) — at dataset
	// LOAD time. Those rows carry huge synthetic populations and otherwise win
	// mid-ocean rank=population queries, returning a whole country as the
	// "nearest city". The filter applies only when an index is (re)built from
	// the raw dump: warm boots that deserialize existing index files are
	// unaffected until the operator deletes an index file to force a rebuild.
	// When enabled, admin-division names (e.g. "California" as an ADM1 row)
	// also disappear from name lookups and /coordinates results — that is the
	// point of the knob. Default false (or key absent) keeps the historical
	// load exactly as-is.
	ExcludeAdminDivisions bool `json:"exclude_admin_divisions"`
	S2                    S2   `json:"s2"`
}

// S2 names the serialized S2 index file. The v1.1 min_level / max_level /
// max_cells tuning knobs were removed in v1.2: nothing ever consumed them —
// coordinates.BuildIndex took a *S2 but read no tuning from it (golang/geo's
// s2.ShapeIndex exposes no such tuning for a PointVector index), so the
// parameter was removed as well. A config file still carrying those keys
// loads fine: the JSON decoder silently ignores unknown keys (see
// TestLoadConfigIgnoresRemovedS2Keys).
type S2 struct {
	IndexFile string `json:"index_file"`
}

// IndexFilePaths returns the on-disk locations of the three serialized indexes
// (S2, name, postal code): each index file key joined with DatasetsFolder. It
// mirrors the initializer's indexFilePaths exactly, so index producers
// (cmd/build-index) and consumers (lib/initializer) agree on the same paths
// for any config, not just the shipped default.
func (c *Config) IndexFilePaths() (s2Path, namePath, postalPath string) {
	return filepath.Join(c.DatasetsFolder, c.S2.IndexFile),
		filepath.Join(c.DatasetsFolder, c.NameIndexFile),
		filepath.Join(c.DatasetsFolder, c.PostalCodeIndexFile)
}

// LoadFromEnv resolves the config file the way the binaries do — the
// CONFIG_PATH environment variable when it is set (even to an empty string),
// otherwise the "config.json" default relative to the process working
// directory — and loads it. cmd/server/main.go resolves CONFIG_PATH inline
// today; switching it to this helper is a behavior-preserving refactor.
func LoadFromEnv() (*Config, error) {
	configPath, exists := os.LookupEnv("CONFIG_PATH")
	if !exists {
		configPath = "config.json"
	}
	return LoadConfig(configPath)
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
// (the CONFIG_PATH env var is resolved by the binaries — directly or via
// LoadFromEnv — and fed into LoadConfig as configPath).
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
