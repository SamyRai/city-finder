package initializer

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/lib/config"
)

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

// downloadAttempts bounds how often downloadFile tries a transient failure;
// downloadRetryDelay is the first backoff, doubled per retry (a variable so
// tests need not sleep).
const downloadAttempts = 3

var downloadRetryDelay = 2 * time.Second

// httpStatusError is a non-2xx download response.
type httpStatusError struct {
	status string
	code   int
}

func (e *httpStatusError) Error() string { return "unexpected HTTP status: " + e.status }

// transientError marks a network-side failure of one attempt (the request
// or the body transfer), as opposed to a local file error.
type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }

func (e transientError) Unwrap() error { return e.err }

// retryableDownloadError reports whether a failed attempt may succeed when
// repeated: network-side failures, 5xx and 429. Other 4xx responses are
// permanent (a wrong URL stays wrong), a client timeout already spent the
// whole downloadTimeout budget, and local file errors are not the network's,
// so none of those is retried.
func retryableDownloadError(err error) bool {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.code >= 500 || statusErr.code == http.StatusTooManyRequests
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return errors.As(err, new(transientError))
}

// downloadFile downloads url into dst atomically. The body is streamed into
// dst+".part" and only renamed to dst after a complete, status-verified
// transfer, so a failed download (network error, non-2xx status, timeout)
// never leaves a corrupt file behind that would poison every later startup.
func downloadFile(dst string, url string) error {
	partPath := dst + ".part"

	fetch := func() error {
		resp, err := httpClient.Get(url)
		if err != nil {
			return transientError{fmt.Errorf("request failed: %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &httpStatusError{status: resp.Status, code: resp.StatusCode}
		}

		out, err := os.Create(partPath)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", partPath, err)
		}

		if _, err := io.Copy(out, resp.Body); err != nil {
			_ = out.Close()
			// Usually the connection dropped mid-body; a local write error
			// (disk full) retries harmlessly and fails again.
			return transientError{fmt.Errorf("failed to write response body: %w", err)}
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

	for attempt := 1; ; attempt++ {
		err := fetch()
		if err == nil {
			break
		}
		_ = os.Remove(partPath) // never leave a partial download behind
		if attempt >= downloadAttempts || !retryableDownloadError(err) {
			return fmt.Errorf("failed to download %s (attempt %d of %d): %w", url, attempt, downloadAttempts, err)
		}
		delay := downloadRetryDelay << (attempt - 1)
		log.Printf("download %s failed (attempt %d of %d): %v; retrying in %s", url, attempt, downloadAttempts, err, delay)
		time.Sleep(delay)
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
