package main

import (
	"log"
	"path/filepath"

	"github.com/SamyRai/cityFinder/lib/config"
)

// buildPaths bundles every filesystem location one build run reads from or
// writes to.
type buildPaths struct {
	dataFile        string
	postalCodeFile  string
	outputDir       string
	s2IndexPath     string
	nameIndexPath   string
	postalIndexPath string
	// excludeAdminDivisions mirrors cfg.ExcludeAdminDivisions when a config
	// was loaded (false on the legacy-literal fallback), so an index built
	// here matches what a first boot would build from the same config.
	excludeAdminDivisions bool
	// includeFeatureClasses mirrors cfg.IncludeFeatureClasses (nil on the
	// legacy-literal fallback) for the same parity reason.
	includeFeatureClasses []string
}

// resolvePaths returns the input and output paths for the given mode.
//
// Test mode uses fixed testdata literals (inputs and the *_test.gob outputs).
//
// Prod mode resolves everything from the config located the same way
// cmd/server locates it (CONFIG_PATH env var, "config.json" default — see
// config.LoadFromEnv). The serialized index outputs come from
// (*config.Config).IndexFilePaths, which joins each index file key with the
// config's datasets_folder exactly like the initializer's IndexFilePaths call on
// the reader side, so a build under any non-default config is the one the
// initializer will actually load. When no config file can be loaded, prod
// falls back to the legacy literal names below with a warning — build-index
// must remain runnable before a config exists. With the shipped default
// config.json the fallback values coincide with the config-driven ones.
func resolvePaths(mode string) buildPaths {
	if mode == "test" {
		return buildPaths{
			dataFile:        "testdata/allCountries.txt",
			postalCodeFile:  "testdata/zipCodes.txt",
			outputDir:       "testdata",
			s2IndexPath:     filepath.Join("testdata", "s2index_test.gob"),
			nameIndexPath:   filepath.Join("testdata", "name_index_test.gob"),
			postalIndexPath: filepath.Join("testdata", "postal_code_index_test.gob"),
		}
	}

	// mode == "prod": legacy literals double as the missing-config fallback.
	paths := buildPaths{
		dataFile:        "datasets/allCountries.txt",
		postalCodeFile:  "datasets/zipCodes.txt",
		outputDir:       "datasets",
		s2IndexPath:     "datasets/s2index.gob",
		nameIndexPath:   "datasets/name_index.gob",
		postalIndexPath: "datasets/postal_code_index.gob",
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Printf("Warning: could not load config (%v); falling back to legacy literal dataset and index file names %s / %s — symlink them if the initializer produced different names", err, paths.dataFile, paths.postalCodeFile)
		return paths
	}
	paths.dataFile = filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile)
	paths.postalCodeFile = filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile)
	paths.outputDir = cfg.DatasetsFolder
	paths.s2IndexPath, paths.nameIndexPath, paths.postalIndexPath = cfg.IndexFilePaths()
	paths.excludeAdminDivisions = cfg.ExcludeAdminDivisions
	paths.includeFeatureClasses = cfg.IncludeFeatureClasses
	return paths
}
