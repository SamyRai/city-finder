package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
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
	// IncludeFeatureClasses, when non-empty, is the parsed include_feature_classes
	// allowlist: at dataset LOAD (index build/rebuild) only rows whose GeoNames
	// feature class is in the list are loaded. A populated-places-only config
	// ("P") answers both validation findings — a class-L row ("HHS Region 9",
	// population 49.34M) winning every rank=population query in the western US,
	// and country-less undersea/international rows being the nearest raw
	// features to far-from-land queries. Warm boots that deserialize existing
	// index files are unaffected until an index file is deleted to force a
	// rebuild; /coordinates results and population-rank winners change
	// accordingly — that is the point of the knob. Entries are parsed from the
	// comma-separated JSON string by UnmarshalJSON (trim + uppercase +
	// validation); nil ("" or key absent) is the default and keeps the
	// historical load exactly as-is.
	IncludeFeatureClasses []string `json:"include_feature_classes"`
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
	// load exactly as-is. See also IncludeFeatureClasses: the include-list is
	// applied first, so when it already excludes A this flag is a no-op.
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

// UnmarshalJSON decodes a Config and turns the comma-separated
// include_feature_classes string into the parsed IncludeFeatureClasses slice.
// Implemented as a method (rather than inline in LoadConfig) so every decode
// path — LoadConfig and any caller feeding a Config to encoding/json itself —
// parses and validates identically. The alias type breaks the method's
// recursion; the alias-level IncludeFeatureClasses string shadows the promoted
// []string field (encoding/json resolves the shallower depth), capturing the
// raw value before parsing.
func (c *Config) UnmarshalJSON(data []byte) error {
	type configAlias Config
	aux := struct {
		*configAlias
		IncludeFeatureClasses string `json:"include_feature_classes"`
	}{configAlias: (*configAlias)(c)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	classes, err := parseIncludeFeatureClasses(aux.IncludeFeatureClasses)
	if err != nil {
		return err
	}
	c.IncludeFeatureClasses = classes
	return nil
}

// parseIncludeFeatureClasses parses the include_feature_classes value: a
// comma-separated list of GeoNames feature classes ("P", "P,A"); empty or
// absent means no filter (nil), the default. Entries are space-trimmed and
// uppercased ("p" -> "P") and validated against the GeoNames feature-class
// set (dataLoader.GeoNamesFeatureClasses) — the same set the loader enforces,
// in one place. An invalid entry is a config LOAD error naming the offending
// value: fail loud, never silently ignore a typo that would otherwise mean
// "no rows loaded". Duplicates collapse; the parsed slice preserves first
// occurrence order.
func parseIncludeFeatureClasses(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var classes []string
	seen := make(map[string]bool)
	var invalid []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		class := strings.ToUpper(entry)
		if !dataLoader.IsValidFeatureClass(class) {
			invalid = append(invalid, entry)
			continue
		}
		if !seen[class] {
			seen[class] = true
			classes = append(classes, class)
		}
	}

	if len(invalid) > 0 {
		return nil, fmt.Errorf("invalid include_feature_classes entries %q in %q: each must be one of the GeoNames feature classes A P H L R S T U V (single uppercase letter, comma-separated)",
			invalid, raw)
	}
	return classes, nil
}

// IndexFilePaths returns the on-disk locations of the three serialized indexes
// (S2, name, postal code): each index file key joined with DatasetsFolder. It
// is the single resolution both index producers (cmd/build-index) and
// consumers (lib/initializer) use, so they agree on the same paths for any
// config, not just the shipped default.
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
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, fmt.Errorf("failed to read config file: %v", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed to close config file: %v", closeErr)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %v", err)
	}
	// One JSON document per file: anything after it (a second object, a
	// botched merge) would otherwise be silently ignored.
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("failed to decode config file: unexpected content after the JSON object")
	}
	// Unknown keys still load (old files carry removed keys, see S2), but a
	// misspelled key would silently fall back to its zero value, so say so.
	if unknown := unknownKeys(data); len(unknown) > 0 {
		log.Printf("config %s: ignoring unknown keys %s", configPath, strings.Join(unknown, ", "))
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

// unknownKeys lists the keys of a config document that no Config field
// decodes (top level and inside "s2"), sorted. A document that is not an
// object yields nothing: Decode has already reported it.
func unknownKeys(data []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil
	}
	var unknown []string
	known := jsonKeys(reflect.TypeOf(Config{}))
	for key, raw := range top {
		if !known[key] {
			unknown = append(unknown, key)
			continue
		}
		if key == "s2" {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil {
				s2Known := jsonKeys(reflect.TypeOf(S2{}))
				for k := range nested {
					if !s2Known[k] {
						unknown = append(unknown, "s2."+k)
					}
				}
			}
		}
	}
	slices.Sort(unknown)
	return unknown
}

// jsonKeys returns the json tag names of t's fields.
func jsonKeys(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// Validate checks what Initialize needs from a config: every dataset and
// index file name is set, and no two of them name the same file — a shared
// name would make one download or index overwrite another. LoadConfig
// applies no defaults and accepts partial configs, so this runs where the
// files are used, not at load.
func (c *Config) Validate() error {
	// The zips are only touched when a dataset must be downloaded, and the
	// admin1 names file is optional, so those may be empty; every name that
	// is set takes part in the collision check.
	files := []struct {
		key, value string
		required   bool
	}{
		{"all_cities_file", c.AllCitiesFile, true},
		{"postal_codes_file", c.PostalCodesFile, true},
		{"name_index_file", c.NameIndexFile, true},
		{"postal_code_index_file", c.PostalCodeIndexFile, true},
		{"s2.index_file", c.S2.IndexFile, true},
		{"all_cities_zip", c.AllCitiesZip, false},
		{"postal_codes_zip", c.PostalCodesZip, false},
		{"admin1_codes_file", c.Admin1CodesFile, false},
	}
	var errs []error
	owner := make(map[string]string, len(files))
	for _, f := range files {
		if f.value == "" {
			if f.required {
				errs = append(errs, fmt.Errorf("%s is required", f.key))
			}
			continue
		}
		name := filepath.Clean(f.value)
		if prev, dup := owner[name]; dup {
			errs = append(errs, fmt.Errorf("%s and %s both name %q", prev, f.key, f.value))
			continue
		}
		owner[name] = f.key
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	return nil
}
