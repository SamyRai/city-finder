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
	"path/filepath"
	"strings"
	"time"
)

// Initialize ensures datasets are downloaded and extracted, and the indexes are built
func Initialize(cfg *config.Config) (*finder.Finder, error) {
	if err := ensureDatasets(cfg); err != nil {
		return nil, err
	}
	return ensureFinders(cfg)
}

// ensureDatasets ensures that the datasets are downloaded and extracted
func ensureDatasets(cfg *config.Config) error {
	log.Printf("Ensuring datasets are downloaded and extracted in %s", cfg.DatasetsFolder)
	if _, err := os.Stat(cfg.DatasetsFolder); os.IsNotExist(err) {
		err := os.Mkdir(cfg.DatasetsFolder, os.ModePerm)
		if err != nil {
			return fmt.Errorf("failed to create datasets folder: %v", err)
		}
	}

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
		}
	} else {
		log.Printf("Dataset file %s already exists, skipping extraction.", filePath)
	}
	return nil
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
// into dest under newFileName. GeoNames archives contain exactly one dataset
// file; archives with a different layout are rejected instead of silently
// overwriting the output with the last entry.
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

	if len(r.File) != 1 {
		return fmt.Errorf("archive %s contains %d entries, expected exactly 1 (a single GeoNames dataset file)", src, len(r.File))
	}
	f := r.File[0]
	if f.FileInfo().IsDir() {
		return fmt.Errorf("archive %s: sole entry %q is a directory", src, f.Name)
	}

	// Zip-slip guard: the entry name must resolve inside dest.
	fpath := filepath.Join(dest, f.Name)
	if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
		return fmt.Errorf("%s: illegal file path in archive %s", fpath, src)
	}
	if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", filepath.Dir(fpath), err)
	}

	outPath := filepath.Join(dest, newFileName)
	outFile, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", outPath, err)
	}
	defer func() {
		if closeErr := outFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close %s: %w", outPath, closeErr)
		}
	}()

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("failed to open entry %q in %s: %w", f.Name, src, err)
	}
	defer func() {
		if closeErr := rc.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("failed to close entry %q in %s: %w", f.Name, src, closeErr)
		}
	}()

	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("failed to extract %q from %s: %w", f.Name, src, err)
	}
	return nil
}

// indexFilePaths resolves the on-disk locations of the three serialized
// indexes. The ensure*Index functions receive these precomputed paths so the
// resolution lives in exactly one place.
func indexFilePaths(cfg *config.Config) (s2Path, namePath, postalCodePath string) {
	return filepath.Join(cfg.DatasetsFolder, cfg.S2.IndexFile),
		filepath.Join(cfg.DatasetsFolder, cfg.NameIndexFile),
		filepath.Join(cfg.DatasetsFolder, cfg.PostalCodeIndexFile)
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

// load loads the raw datasets on first call and is a no-op afterwards.
func (s *datasetSource) load() error {
	if s.loaded {
		return nil
	}
	cities, postalCodes, err := loadData(s.cfg)
	if err != nil {
		return err
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
func ensureFinders(cfg *config.Config) (*finder.Finder, error) {
	s2IndexPath, nameIndexPath, postalCodeIndexPath := indexFilePaths(cfg)

	data := &datasetSource{cfg: cfg}
	if allIndexesPresent(s2IndexPath, nameIndexPath, postalCodeIndexPath) {
		log.Printf("all indexes present, skipping dataset load")
	} else if err := data.load(); err != nil {
		return nil, err
	}

	s2Finder, err := ensureS2Index(s2IndexPath, cfg, data)
	if err != nil {
		return nil, err
	}

	nameFinder, err := ensureNameIndex(nameIndexPath, data)
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

func loadData(cfg *config.Config) ([]city.SpatialCity, map[string]map[string]dataLoader.PostalCodeEntry, error) {
	cities, err := dataLoader.LoadGeoNamesCSV(filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile))
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
	s2Finder, err := coordinates.BuildIndex(data.cities, &cfg.S2)
	if err != nil {
		return nil, fmt.Errorf("failed to build S2 index: %v", err)
	}
	if err := s2Finder.SerializeIndex(s2IndexPath); err != nil {
		return nil, fmt.Errorf("failed to serialize S2 index: %v", err)
	}
	return s2Finder, nil
}

// ensureNameIndex returns the name index, building and serializing it when
// the file is missing. An existing file that fails to decode is logged and
// rebuilt once from the source data (the name index does not yet expose a
// corruption sentinel, so any decode failure — truncation, legacy format, a
// future version mismatch after the coordinated name-format change — takes
// the same rebuild-once path).
func ensureNameIndex(nameIndexPath string, data *datasetSource) (*name.Finder, error) {
	log.Printf("Ensuring name index is built and serialized in %s", nameIndexPath)
	if _, errStat := os.Stat(nameIndexPath); os.IsNotExist(errStat) {
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets to build name index: %v", err)
		}
		return buildAndSerializeNameIndex(nameIndexPath, data)
	}

	nameFinder, err := name.DeserializeIndex(nameIndexPath)
	if err != nil {
		log.Printf("Warning: failed to deserialize name index (%v); rebuilding it from the source datasets", err)
		if err := data.load(); err != nil {
			return nil, fmt.Errorf("failed to load datasets needed to rebuild the name index: %v", err)
		}
		return buildAndSerializeNameIndex(nameIndexPath, data)
	}
	return nameFinder, nil
}

func buildAndSerializeNameIndex(nameIndexPath string, data *datasetSource) (*name.Finder, error) {
	log.Printf("Building name index at %s", nameIndexPath)
	nameFinder := name.BuildIndex(data.cities)
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
	return postalCodeFinder, nil
}

func buildAndSerializePostalCodeIndex(postalCodeIndexPath string, data *datasetSource) (*postalCode.Finder, error) {
	log.Printf("Building postal code index at %s", postalCodeIndexPath)
	postalCodeFinder := postalCode.BuildIndex(data.postalCodes)
	if err := postalCodeFinder.SerializeIndex(postalCodeIndexPath); err != nil {
		return nil, fmt.Errorf("failed to serialize postal code index: %v", err)
	}
	return postalCodeFinder, nil
}
