package initializer

import (
	"archive/zip"
	"errors"
	"fmt"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Initialize ensures datasets are downloaded and extracted, and the indexes are built
func Initialize(cfg *config.Config) (*finder.Finder, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := ensureDatasetsFolder(cfg); err != nil {
		return nil, err
	}
	release, err := acquireInitLock(cfg.DatasetsFolder)
	if err != nil {
		return nil, err
	}
	defer release()

	admin1NamesPath := ensureAdmin1NamesPath(cfg)

	s2Path, namePath, postalPath := indexFilePaths(cfg)
	if allIndexesPresent(s2Path, namePath, postalPath) {
		// Warm start: the raw datasets would only be needed to rebuild an
		// index, and datasetSource.load re-ensures them on demand in that
		// case. Skipping here avoids a ~442 MB re-download plus a ~1.9 GB
		// re-extract for volumes seeded with indexes but no datasets (or
		// after an operator deleted the raw files).
		log.Printf("all indexes present, skipping dataset ensure (delete an index file to force a refresh)")
	} else if err := ensureDatasets(cfg); err != nil {
		return nil, err
	}
	return ensureFinders(cfg, admin1NamesPath)
}

// ensureDatasetsFolder creates the datasets folder when missing. MkdirAll so
// nested paths (config datasets_folder: "data/datasets") work.
func ensureDatasetsFolder(cfg *config.Config) error {
	if _, err := os.Stat(cfg.DatasetsFolder); os.IsNotExist(err) {
		if err := os.MkdirAll(cfg.DatasetsFolder, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create datasets folder: %v", err)
		}
	}
	return nil
}

// initLockName is the lock file serializing initializers per datasets folder.
const initLockName = ".cityfinder-init.lock"

// errLockHeld reports that another live process holds the init lock.
var errLockHeld = errors.New("init lock held by another process")

// acquireInitLock guards a datasets folder against concurrent boots. Two
// cold-booting processes otherwise write the same fixed "<index>.gob.part"
// paths: the second os.Create truncates the first's in-flight part file and
// both rename, leaving interleaved garbage (a real scenario under a rolling
// update with maxSurge, where two pods share one PVC).
//
// The lock is an exclusive flock on the lock file (see lockExclusive): the
// kernel releases it when the holder dies, so there is no stale-lock
// detection to get wrong. (The previous pid-file lock refused forever after
// an OOM-killed cold build: the restarted container is PID 1 again, and its
// own pid in the file looked like a live foreign owner.) The file stays in
// place between boots and holds the owner's pid as a diagnostic hint only.
// Living holder → fail fast: the caller exits and the orchestrator restarts
// it once the first boot finishes.
func acquireInitLock(datasetsFolder string) (release func(), err error) {
	lockPath := filepath.Join(datasetsFolder, initLockName)
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open init lock %s: %w", lockPath, err)
	}
	if err := lockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockHeld) {
			holder := "unknown pid"
			if pid, perr := readLockPID(lockPath); perr == nil {
				holder = fmt.Sprintf("pid %d", pid)
			}
			return nil, fmt.Errorf("another initializer (%s) is running against datasets folder %s; refusing to race it", holder, datasetsFolder)
		}
		return nil, fmt.Errorf("failed to lock %s: %w", lockPath, err)
	}
	// Best-effort pid hint for the error message above; the lock itself is
	// the flock, not this content.
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() { _ = f.Close() }, nil // closing the descriptor releases the flock
}

// readLockPID parses the pid hint stored in the lock file.
func readLockPID(lockPath string) (int, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

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

// ensureDatasets ensures that the datasets are downloaded and extracted
func ensureDatasets(cfg *config.Config) error {
	log.Printf("Ensuring datasets are downloaded and extracted in %s", cfg.DatasetsFolder)

	if err := downloadAndExtractDataset(cfg.AllCitiesURL, cfg.AllCitiesZip, cfg.AllCitiesFile, cfg); err != nil {
		return err
	}
	if err := downloadAndExtractDataset(cfg.PostalCodesURL, cfg.PostalCodesZip, cfg.PostalCodesFile, cfg); err != nil {
		return err
	}
	return nil
}

// downloadAndExtractDataset downloads and extracts the dataset if not already present
func downloadAndExtractDataset(url, zipName, fileName string, cfg *config.Config) error {
	if zipName == "" {
		return nil
	}
	zipPath := filepath.Join(cfg.DatasetsFolder, zipName)
	filePath := filepath.Join(cfg.DatasetsFolder, fileName)

	// Check if the final extracted file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		downloaded := false
		// Check if the zip file exists before downloading
		if _, err := os.Stat(zipPath); os.IsNotExist(err) {
			log.Printf("Downloading %s...", url)
			err := downloadFile(zipPath, url)
			if err != nil {
				return fmt.Errorf("failed to download %s: %v", url, err)
			}
			downloaded = true
		} else {
			log.Printf("Zip file %s already exists, skipping download.", zipPath)
		}

		log.Printf("Extracting %s...", zipName)
		err := unzipAndRename(zipPath, cfg.DatasetsFolder, fileName)
		if err != nil {
			if downloaded {
				return fmt.Errorf("failed to extract %s: %v", zipName, err)
			}
			// The zip predates this run and fails to extract: treat it as
			// corrupt, replace it with a fresh download, and retry once so a
			// single bad archive cannot brick every later startup.
			log.Printf("Existing archive %s failed to extract (%v); re-downloading once", zipPath, err)
			if rmErr := os.Remove(zipPath); rmErr != nil {
				return fmt.Errorf("failed to extract %s; also failed to remove the suspect archive %s: %v", zipName, zipPath, rmErr)
			}
			if dlErr := downloadFile(zipPath, url); dlErr != nil {
				return fmt.Errorf("failed to re-download %s: %v", url, dlErr)
			}
			if err := unzipAndRename(zipPath, cfg.DatasetsFolder, fileName); err != nil {
				return fmt.Errorf("failed to extract re-downloaded %s: %v", zipName, err)
			}
			removeExtractedArchive(zipPath)
		} else {
			removeExtractedArchive(zipPath)
		}
	} else {
		log.Printf("Dataset file %s already exists, skipping extraction.", filePath)
	}
	return nil
}

// removeExtractedArchive deletes a zip after its successful extraction. The
// archive is a re-downloadable cache of the extracted dataset; keeping it
// pins ~442 MB of dead weight per volume (allCountries + zipCodes zips).
// Best-effort: a failure to remove never fails the boot.
func removeExtractedArchive(zipPath string) {
	if err := os.Remove(zipPath); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("warning: could not remove archive %s after extraction: %v", zipPath, err)
		}
		return
	}
	log.Printf("removed archive %s after successful extraction", zipPath)
}

// downloadTimeout bounds an entire dataset download (headers plus body).
// The GeoNames allCountries archives are ~400MB, so the timeout must
// accommodate slow links: 15 minutes still allows ~450KB/s.
const downloadTimeout = 15 * time.Minute

// httpClient is package-level so tests can inject a client with a short
// timeout; production code always uses the default timeout above.
var httpClient = &http.Client{Timeout: downloadTimeout}

// downloadFile downloads url into dst atomically. The body is streamed into
// dst+".part" and only renamed to dst after a complete, status-verified
// transfer, so a failed download (network error, non-2xx status, timeout)
// never leaves a corrupt file behind that would poison every later startup.
func downloadFile(dst string, url string) error {
	partPath := dst + ".part"

	fetch := func() error {
		resp, err := httpClient.Get(url)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("unexpected HTTP status: %s", resp.Status)
		}

		out, err := os.Create(partPath)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", partPath, err)
		}

		if _, err := io.Copy(out, resp.Body); err != nil {
			_ = out.Close()
			return fmt.Errorf("failed to write response body: %w", err)
		}
		// Durable before the rename publishes it: after a power loss the
		// final path must never name a zero-length or partial file.
		if err := out.Sync(); err != nil {
			_ = out.Close()
			return fmt.Errorf("failed to sync %s: %w", partPath, err)
		}

		if err := out.Close(); err != nil {
			return fmt.Errorf("failed to finalize %s: %w", partPath, err)
		}
		return nil
	}

	if err := fetch(); err != nil {
		_ = os.Remove(partPath) // never leave a partial download behind
		return fmt.Errorf("failed to download %s: %w", url, err)
	}

	if err := os.Rename(partPath, dst); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, dst, err)
	}
	return nil
}

// unzipAndRename extracts the single data file from the zip archive at src
// into dest under newFileName. GeoNames archives hold one dataset file,
// possibly next to a readme; the data entry is selected by datasetEntry, and
// archives with any other layout are rejected instead of silently picking
// one.
//
// The entry streams through outPath+".part" and is renamed into place only
// after a complete copy, mirroring downloadFile: a crash or a full disk
// mid-extract must never leave a truncated file at the final path, because
// the next boot's skip-if-exists check (downloadAndExtractDataset) only stats
// the path and would otherwise bake the partial dataset into the indexes.
func unzipAndRename(src string, dest string, newFileName string) (err error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("failed to open zip archive %s: %w", src, err)
	}
	defer func() {
		if closeErr := r.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close zip archive %s: %w", src, closeErr)
		}
	}()

	f, err := datasetEntry(r.File)
	if err != nil {
		return fmt.Errorf("archive %s: %w", src, err)
	}

	// The entry's own name is never used as a write path (the output is
	// always dest/newFileName), so archive paths cannot escape dest.
	outPath := filepath.Join(dest, newFileName)
	partPath := outPath + ".part"
	if err := extractEntryTo(f, partPath); err != nil {
		_ = os.Remove(partPath) // never leave a partial extraction behind
		return fmt.Errorf("failed to extract from %s: %w", src, err)
	}
	if err := os.Rename(partPath, outPath); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, outPath, err)
	}
	return nil
}

// datasetEntry selects the dataset file of a GeoNames archive: the single
// regular entry that is not a readme. The postal export, for one, has been
// published with a readme next to the data; requiring exactly one entry
// would reject it and fail the cold boot.
func datasetEntry(files []*zip.File) (*zip.File, error) {
	var data []*zip.File
	for _, f := range files {
		// Defense in depth: a dataset archive has no business carrying
		// absolute or parent-relative names, even though the entry name is
		// never used as a write path.
		if path.IsAbs(f.Name) || slices.Contains(strings.Split(f.Name, "/"), "..") {
			return nil, fmt.Errorf("illegal file path %q", f.Name)
		}
		base := strings.ToLower(path.Base(f.Name))
		if f.FileInfo().IsDir() || strings.HasPrefix(base, "readme") {
			continue
		}
		data = append(data, f)
	}
	if len(data) != 1 {
		names := make([]string, len(files))
		for i, f := range files {
			names[i] = f.Name
		}
		return nil, fmt.Errorf("expected exactly 1 dataset entry (readmes and directories aside), found %d among %q", len(data), names)
	}
	return data[0], nil
}

// extractEntryTo streams one archive entry into partPath. The caller owns
// cleanup of partPath on any failure.
func extractEntryTo(f *zip.File, partPath string) (err error) {
	// Fixed permissions: an entry stored without mode bits would otherwise
	// produce an unreadable 0000 file.
	outFile, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", partPath, err)
	}
	defer func() {
		if closeErr := outFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close %s: %w", partPath, closeErr)
		}
	}()

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("failed to open entry %q: %w", f.Name, err)
	}
	defer func() {
		if closeErr := rc.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close entry %q: %w", f.Name, closeErr)
		}
	}()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("failed to extract %q: %w", f.Name, err)
	}
	// Durable before the caller's rename publishes it (see downloadFile).
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync %s: %w", partPath, err)
	}
	return nil
}

// indexFilePaths resolves the on-disk locations of the three serialized
// indexes. The ensure*Index functions receive these precomputed paths so the
// resolution lives in exactly one place; the method itself is the same one
// cmd/build-index writes to, so the two binaries cannot drift apart.
func indexFilePaths(cfg *config.Config) (s2Path, namePath, postalCodePath string) {
	return cfg.IndexFilePaths()
}

// allIndexesPresent reports whether every given file exists. Any stat error
// counts as missing so the caller falls back to the full load-and-build path.
func allIndexesPresent(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// datasetSource lazily loads and memoizes the raw datasets. A warm start
// (every index file present) skips the load entirely; if an index then fails
// to decode, the rebuild path materializes the data on demand exactly once
// instead of re-parsing for each rebuilt index.
type datasetSource struct {
	cfg         *config.Config
	cities      []city.SpatialCity
	postalCodes map[string]map[string]dataLoader.PostalCodeEntry
	loaded      bool
}

// load loads the raw datasets on first call and is a no-op afterwards. A
// failed load retries once after re-running ensureDatasets: a warm start that
// found all indexes skips the dataset ensure, so a rebuild triggered by a
// corrupt/legacy index may reach this point with the raw files absent.
func (s *datasetSource) load() error {
	if s.loaded {
		return nil
	}
	cities, postalCodes, err := loadData(s.cfg)
	if err != nil {
		if ensureErr := ensureDatasets(s.cfg); ensureErr != nil {
			return fmt.Errorf("%v (additionally, ensuring the missing datasets failed: %v)", err, ensureErr)
		}
		if cities, postalCodes, err = loadData(s.cfg); err != nil {
			return err
		}
	}
	s.cities, s.postalCodes, s.loaded = cities, postalCodes, true
	return nil
}

// ensureFinders ensures that the indexes are built and serialized.
// When every serialized index already exists, the expensive dataset load
// (multi-GB TSV parse) is skipped entirely: each ensure*Index call then
// deserializes its index from disk instead. An index that fails to decode
// is rebuilt once from the source data (see the ensure*Index functions),
// which re-materializes the datasets on demand.
//
// admin1NamesPath points at the OPTIONAL admin1CodesASCII.txt dataset: empty
// disables names entirely; a present file attaches the composite-key ->
// name map to the S2 finder; a configured-but-missing file degrades to
// codes-only mode with one log line (responses carry admin1 CODE, no name —
// the dataset is enhancement data, never a startup requirement).
func ensureFinders(cfg *config.Config, admin1NamesPath string) (*finder.Finder, error) {
	s2IndexPath, nameIndexPath, postalCodeIndexPath := indexFilePaths(cfg)

	data := &datasetSource{cfg: cfg}
	if allIndexesPresent(s2IndexPath, nameIndexPath, postalCodeIndexPath) {
		log.Printf("all indexes present, skipping dataset load")

		// Warm start: the three decodes are independent, so they run
		// concurrently. Name decode dominates (~14 s of the ~20 s warm
		// start at prod); overlapping it with S2 (~4 s) and postal (~1 s)
		// removes the two smaller decodes from the critical path. Peak RSS
		// rises by the smaller indexes' transient buffers (~0.5–1 GB) —
		// within the chart's request/limit headroom. Any decode failure
		// falls through to the sequential ensure path below, which alone
		// owns rebuilds (datasetSource is not synchronized).
		type s2Result struct {
			finder *coordinates.S2Finder
			err    error
		}
		type nameResult struct {
			finder *name.Finder
			err    error
		}
		type postalResult struct {
			finder *postalCode.Finder
			err    error
		}
		s2Ch := make(chan s2Result, 1)
		nameCh := make(chan nameResult, 1)
		postalCh := make(chan postalResult, 1)
		go func() {
			f, err := coordinates.DeserializeIndex(s2IndexPath)
			s2Ch <- s2Result{f, err}
		}()
		go func() {
			f, err := name.DeserializeIndex(nameIndexPath)
			nameCh <- nameResult{f, err}
		}()
		go func() {
			f, err := postalCode.DeserializeIndex(postalCodeIndexPath)
			postalCh <- postalResult{f, err}
		}()
		s2Res, nameRes, postalRes := <-s2Ch, <-nameCh, <-postalCh
		if s2Res.err == nil && nameRes.err == nil {
			// The name index resolves its ids through the S2 index's city
			// table; an index that cannot attach is rebuilt by the
			// sequential path below.
			nameRes.err = attachNameIndex(nameIndexPath, nameRes.finder, s2Res.finder.Cities)
		}
		if s2Res.err == nil && nameRes.err == nil && postalRes.err == nil {
			migratePostalIndex(postalCodeIndexPath, postalRes.finder)
			s2Finder := s2Res.finder
			// Names are attached on every boot (warm or cold): the map is
			// ~120 KB and deliberately not serialized with the index, so
			// updating the names file never invalidates it.
			s2Finder.Admin1Names = ensureAdmin1Names(admin1NamesPath)
			return &finder.Finder{
				S2Finder:         s2Finder,
				NameFinder:       nameRes.finder,
				PostalCodeFinder: postalRes.finder,
			}, nil
		}
		log.Printf("warm decode incomplete (s2=%v, name=%v, postal=%v); falling back to sequential ensure",
			s2Res.err, nameRes.err, postalRes.err)
	} else if err := data.load(); err != nil {
		return nil, err
	}

	s2Finder, err := ensureS2Index(s2IndexPath, cfg, data)
	if err != nil {
		return nil, err
	}
	// Names are attached on every boot (warm or cold): the map is ~120 KB
	// and deliberately not serialized with the index, so updating the
	// names file never invalidates it.
	s2Finder.Admin1Names = ensureAdmin1Names(admin1NamesPath)

	nameFinder, err := ensureNameIndex(nameIndexPath, data, s2Finder.Cities)
	if err != nil {
		return nil, err
	}

	postalCodeFinder, err := ensurePostalCodeIndex(postalCodeIndexPath, data)
	if err != nil {
		return nil, err
	}

	return &finder.Finder{
		S2Finder:         s2Finder,
		NameFinder:       nameFinder,
		PostalCodeFinder: postalCodeFinder,
	}, nil
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

func loadData(cfg *config.Config) ([]city.SpatialCity, map[string]map[string]dataLoader.PostalCodeEntry, error) {
	cities, err := dataLoader.LoadGeoNamesCSVWithOptions(
		filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile),
		dataLoader.LoadOptions{
			ExcludeAdminDivisions: cfg.ExcludeAdminDivisions,
			IncludeFeatureClasses: cfg.IncludeFeatureClasses,
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load GeoNames data from CSV: %v", err)
	}

	postalCodes, err := dataLoader.LoadPostalCodes(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load Postal Code data: %v", err)
	}

	return cities, postalCodes, nil
}

// ensureS2Index returns the S2 index, building and serializing it when the
// file is missing. A file that exists but fails to decode (truncated by a
// crash mid-write, or written by an incompatible format version) is logged
// and rebuilt once from the source data, re-serializing over the bad file;
// a rebuild that also fails is fatal, exactly as a build failure is.
func ensureS2Index(s2IndexPath string, cfg *config.Config, data *datasetSource) (*coordinates.S2Finder, error) {
	log.Printf("Ensuring S2 index is built and serialized in %s", s2IndexPath)
	if _, errStat := os.Stat(s2IndexPath); os.IsNotExist(errStat) {
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets to build S2 index: %v", err)
		}
		return buildAndSerializeS2Index(s2IndexPath, cfg, data)
	}

	s2Finder, err := coordinates.DeserializeIndex(s2IndexPath)
	if err != nil {
		if !errors.Is(err, coordinates.ErrCorruptIndex) {
			return nil, fmt.Errorf("failed to deserialize S2 index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets needed to rebuild the S2 index: %v", err)
		}
		return buildAndSerializeS2Index(s2IndexPath, cfg, data)
	}
	return s2Finder, nil
}

func buildAndSerializeS2Index(s2IndexPath string, cfg *config.Config, data *datasetSource) (*coordinates.S2Finder, error) {
	log.Printf("Building S2 index at %s", s2IndexPath)
	s2Finder, err := coordinates.BuildIndex(data.cities)
	if err != nil {
		return nil, fmt.Errorf("failed to build S2 index: %v", err)
	}
	if err := s2Finder.SerializeIndex(s2IndexPath); err != nil {
		return nil, fmt.Errorf("failed to serialize S2 index: %v", err)
	}
	return s2Finder, nil
}

// ensureNameIndex returns the name index attached to the S2 index's city
// table (cities), building and serializing it when the file is missing. A
// file that exists but fails to decode with name.ErrCorruptIndex (truncated
// by a crash mid-write, legacy format, or a future version mismatch), or that
// cannot attach to cities (it was built against a different S2 index), is
// logged and rebuilt once from the source data, re-serializing over the bad
// file. Any other error — for example a wrapped fs error from an unreadable
// file — is fatal, exactly as it is for the S2 and postal code indexes.
func ensureNameIndex(nameIndexPath string, data *datasetSource, cities []city.City) (*name.Finder, error) {
	log.Printf("Ensuring name index is built and serialized in %s", nameIndexPath)
	if _, errStat := os.Stat(nameIndexPath); os.IsNotExist(errStat) {
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets to build name index: %v", err)
		}
		return buildAndSerializeNameIndex(nameIndexPath, data, cities)
	}

	nameFinder, err := name.DeserializeIndex(nameIndexPath)
	if err == nil {
		err = attachNameIndex(nameIndexPath, nameFinder, cities)
	}
	if err != nil {
		if !errors.Is(err, name.ErrCorruptIndex) && !errors.Is(err, name.ErrCityTableMismatch) {
			return nil, fmt.Errorf("failed to deserialize name index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets needed to rebuild the name index: %v", err)
		}
		return buildAndSerializeNameIndex(nameIndexPath, data, cities)
	}
	return nameFinder, nil
}

// attachNameIndex makes a loaded name index resolve through the S2 index's
// city table, so every city is held once per process. A file that embeds its
// own copy (the legacy v2 format, or a standalone v3 file) and matches is
// re-serialized in the compact format that only references the table — a
// one-time, in-place migration that needs no dataset download. A failed
// migration write is logged and ignored: the attached index is already
// correct, and the next boot retries.
//
// It returns an error wrapping name.ErrCityTableMismatch when the index was
// built against a different table. An embedding index stays usable on its
// own copy in that case, but it is still reported, because the pair of index
// files disagrees and the caller rebuilds.
func attachNameIndex(nameIndexPath string, nameFinder *name.Finder, cities []city.City) error {
	embedded := nameFinder.OwnsCityTable()
	if err := nameFinder.ShareCities(cities); err != nil {
		return fmt.Errorf("name index %s does not match the S2 index: %w", nameIndexPath, err)
	}
	if embedded {
		if err := nameFinder.SerializeIndex(nameIndexPath); err != nil {
			log.Printf("Warning: migrating name index %s to the shared-table format failed (will retry next boot): %v", nameIndexPath, err)
		} else {
			log.Printf("migrated name index %s to the shared-table format", nameIndexPath)
		}
	}
	return nil
}

func buildAndSerializeNameIndex(nameIndexPath string, data *datasetSource, cities []city.City) (*name.Finder, error) {
	log.Printf("Building name index at %s", nameIndexPath)
	nameFinder := name.BuildIndex(data.cities)
	// Both indexes are built from the same rows, so the tables are identical
	// and sharing cannot fail; if it ever did, the index keeps (and
	// serializes) its own copy — larger, never wrong.
	if err := nameFinder.ShareCities(cities); err != nil {
		log.Printf("Warning: name index keeps its own city table: %v", err)
	}
	if err := nameFinder.SerializeIndex(nameIndexPath); err != nil {
		return nil, fmt.Errorf("failed to serialize name index: %v", err)
	}
	return nameFinder, nil
}

// ensurePostalCodeIndex returns the postal code index, building and
// serializing it when the file is missing. A file that exists but fails to
// decode is logged and rebuilt once from the source data, re-serializing
// over the bad file; a rebuild that also fails is fatal.
func ensurePostalCodeIndex(postalCodeIndexPath string, data *datasetSource) (*postalCode.Finder, error) {
	log.Printf("Ensuring postal code index is built and serialized in %s", postalCodeIndexPath)
	if _, errStat := os.Stat(postalCodeIndexPath); os.IsNotExist(errStat) {
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets to build postal code index: %v", err)
		}
		return buildAndSerializePostalCodeIndex(postalCodeIndexPath, data)
	}

	postalCodeFinder, err := postalCode.DeserializeIndex(postalCodeIndexPath)
	if err != nil {
		if !errors.Is(err, postalCode.ErrCorruptIndex) {
			return nil, fmt.Errorf("failed to deserialize postal code index: %v", err)
		}
		log.Printf("Warning: %v", err)
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets needed to rebuild the postal code index: %v", err)
		}
		return buildAndSerializePostalCodeIndex(postalCodeIndexPath, data)
	}
	migratePostalIndex(postalCodeIndexPath, postalCodeFinder)
	return postalCodeFinder, nil
}

// migratePostalIndex rewrites a postal index loaded from a legacy file in
// the current format (a one-time, in-place conversion; the loaded finder is
// already in the compact layout). A failed write is logged and ignored: the
// next boot retries.
func migratePostalIndex(path string, f *postalCode.Finder) {
	if !f.LegacyFormat() {
		return
	}
	if err := f.SerializeIndex(path); err != nil {
		log.Printf("Warning: migrating postal code index %s to the current format failed (will retry next boot): %v", path, err)
		return
	}
	log.Printf("migrated postal code index %s to the current format", path)
}

func buildAndSerializePostalCodeIndex(postalCodeIndexPath string, data *datasetSource) (*postalCode.Finder, error) {
	log.Printf("Building postal code index at %s", postalCodeIndexPath)
	postalCodeFinder := postalCode.BuildIndex(data.postalCodes)
	if err := postalCodeFinder.SerializeIndex(postalCodeIndexPath); err != nil {
		return nil, fmt.Errorf("failed to serialize postal code index: %v", err)
	}
	return postalCodeFinder, nil
}
