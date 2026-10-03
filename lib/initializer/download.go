package initializer

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/SamyRai/cityFinder/lib/config"
)

// ensureDatasets ensures that the datasets are downloaded and extracted
func ensureDatasets(ctx context.Context, dl *downloader, cfg *config.Config) error {
	log.Printf("Ensuring datasets are downloaded and extracted in %s", cfg.DatasetsFolder)

	if err := downloadAndExtractDataset(ctx, dl, cfg.AllCitiesURL, cfg.AllCitiesZip, cfg.AllCitiesFile, cfg); err != nil {
		return err
	}
	if err := downloadAndExtractDataset(ctx, dl, cfg.PostalCodesURL, cfg.PostalCodesZip, cfg.PostalCodesFile, cfg); err != nil {
		return err
	}
	return nil
}

// downloadAndExtractDataset downloads and extracts the dataset if not already present
func downloadAndExtractDataset(ctx context.Context, dl *downloader, url, zipName, fileName string, cfg *config.Config) error {
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
			err := dl.download(ctx, zipPath, url)
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
			if dlErr := dl.download(ctx, zipPath, url); dlErr != nil {
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
