package initializer

import (
	"log"
	"os"
	"path/filepath"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// ensureAdmin1NamesPath resolves the OPTIONAL admin1-names dataset path
// (config key admin1_codes_file; relative values resolve against the
// datasets folder like every other dataset file) and cold-downloads it from
// admin1_codes_url when configured and missing. Empty file key = names not
// configured = disabled. Every failure degrades to codes-only mode inside
// ensureAdmin1Names — the dataset is enhancement data, never a startup
// requirement.
func ensureAdmin1NamesPath(cfg *config.Config) string {
	if cfg.Admin1CodesFile == "" {
		return ""
	}
	path := cfg.Admin1CodesFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(cfg.DatasetsFolder, path)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) && cfg.Admin1CodesURL != "" {
		log.Printf("Downloading %s...", cfg.Admin1CodesURL)
		if err := downloadFile(path, cfg.Admin1CodesURL); err != nil {
			// Logged and degradated, not fatal: ensureAdmin1Names sees the
			// missing file and serves codes-only.
			log.Printf("admin1 names download failed: %v", err)
		}
	}
	return path
}

// ensureAdmin1Names loads the optional admin1 names dataset. An empty path
// means "not configured" (nil, silent). A configured path that is missing or
// unreadable degrades to codes-only mode: nil map plus exactly one log line,
// per the design note — the API then serves admin1 codes without names
// rather than failing.
func ensureAdmin1Names(path string) map[string]string {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		log.Printf("admin1 names file %s not available (%v); serving admin1 codes only", path, err)
		return nil
	}
	names, err := dataLoader.LoadAdmin1Names(path)
	if err != nil {
		log.Printf("admin1 names file %s failed to load (%v); serving admin1 codes only", path, err)
		return nil
	}
	return names
}
