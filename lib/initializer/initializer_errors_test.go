package initializer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/SamyRai/cityFinder/lib/indexfile"
)

// These tests cover the initializer's I/O error paths. None depends on file
// modes, so they behave the same for root: a path whose parent is a regular
// file always fails with ENOTDIR, and a dangling symlink always fails Mkdir.

// underAFile returns a path that cannot be created or written: its parent is
// a regular file.
func underAFile(t *testing.T, leaf string) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	return filepath.Join(blocker, leaf)
}

// captureLog collects the standard logger's output for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// --- ensureDatasetsFolder / Initialize ---

func TestEnsureDatasetsFolder_MkdirFails(t *testing.T) {
	// A dangling symlink: Stat reports "not exist", yet Mkdir finds the name
	// taken, so the creation error is the one returned.
	dangling := filepath.Join(t.TempDir(), "datasets")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "gone"), dangling))

	err := ensureDatasetsFolder(&config.Config{DatasetsFolder: dangling})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create datasets folder")
}

func TestEnsureDatasetsFolder_CreatesNestedFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "datasets")
	require.NoError(t, ensureDatasetsFolder(&config.Config{DatasetsFolder: dir}))
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
}

func TestInitialize_InvalidConfigFails(t *testing.T) {
	_, err := Initialize(&config.Config{DatasetsFolder: t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is required")
}

func TestInitialize_FolderUnderAFileFails(t *testing.T) {
	cfg := testConfig(underAFile(t, "datasets"))
	_, err := Initialize(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ENOTDIR)
}

func TestInitialize_LockHeldFails(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)
	release, err := acquireInitLock(dir, true)
	require.NoError(t, err)
	defer release()

	_, err = Initialize(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another initializer")
}

// --- ensureDatasets ---

// datasetServer serves /cities.zip and /postal.zip: a valid archive, or the
// given status when the path is listed in failing.
func datasetServer(t *testing.T, failing map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status, ok := failing[r.URL.Path]; ok {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(validZipBytes(t, "data.txt", "payload\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func downloadConfig(dir, base string) *config.Config {
	cfg := testConfig(dir)
	cfg.AllCitiesURL, cfg.AllCitiesZip = base+"/cities.zip", "cities.zip"
	cfg.PostalCodesURL, cfg.PostalCodesZip = base+"/postal.zip", "postal.zip"
	return cfg
}

func TestEnsureDatasets_CitiesDownloadFailsStopsBeforePostal(t *testing.T) {
	srv := datasetServer(t, map[string]int{"/cities.zip": http.StatusNotFound})
	dir := t.TempDir()
	cfg := downloadConfig(dir, srv.URL)

	err := ensureDatasets(context.Background(), fastDownloader(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cities.zip")
	_, statErr := os.Stat(filepath.Join(dir, cfg.PostalCodesFile))
	assert.True(t, os.IsNotExist(statErr), "the postal dataset must not be fetched after the cities step failed")
}

func TestEnsureDatasets_PostalDownloadFailsAfterCitiesSucceed(t *testing.T) {
	srv := datasetServer(t, map[string]int{"/postal.zip": http.StatusNotFound})
	dir := t.TempDir()
	cfg := downloadConfig(dir, srv.URL)

	err := ensureDatasets(context.Background(), fastDownloader(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postal.zip")
	_, statErr := os.Stat(filepath.Join(dir, cfg.AllCitiesFile))
	assert.NoError(t, statErr, "the cities dataset fetched before the failure stays")
}

// --- downloader failure modes ---

func TestDownload_CannotCreatePartFile(t *testing.T) {
	srv, requests := flakyServer(t, 0, 0, "payload")
	dst := underAFile(t, "data.zip")
	err := fastDownloader().download(context.Background(), dst, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create")
	assert.EqualValues(t, 1, requests.Load(), "a local file error is not retried")
}

func TestDownload_RenameTargetIsDirectory(t *testing.T) {
	srv, requests := flakyServer(t, 0, 0, "payload")
	dst := filepath.Join(t.TempDir(), "data.zip")
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "child"), 0o755)) // a non-empty directory cannot be replaced

	err := fastDownloader().download(context.Background(), dst, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to move")
	assert.EqualValues(t, 1, requests.Load())
	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "the .part file must be removed after a failed publish")
	_, statErr = os.Stat(filepath.Join(dst, "child"))
	assert.NoError(t, statErr, "the directory in the way must be left alone")
}

func TestDownload_InvalidURL(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "data.zip")
	err := fastDownloader().download(context.Background(), dst, "http://[::1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request")
	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr))
}

// --- removeExtractedArchive ---

func TestRemoveExtractedArchive_RemovesFile(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "data.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("zip"), 0o600))
	logs := captureLog(t)

	removeExtractedArchive(zipPath)
	_, err := os.Stat(zipPath)
	assert.True(t, os.IsNotExist(err))
	assert.Contains(t, logs.String(), "removed archive")
}

func TestRemoveExtractedArchive_MissingFileIsSilent(t *testing.T) {
	logs := captureLog(t)
	removeExtractedArchive(filepath.Join(t.TempDir(), "absent.zip"))
	assert.Empty(t, logs.String())
}

func TestRemoveExtractedArchive_FailureOnlyWarns(t *testing.T) {
	// A non-empty directory where the archive should be: Remove fails with
	// an error that is not "not exist". The boot must go on, with a warning.
	zipPath := filepath.Join(t.TempDir(), "data.zip")
	require.NoError(t, os.MkdirAll(filepath.Join(zipPath, "child"), 0o755))
	logs := captureLog(t)

	removeExtractedArchive(zipPath)
	assert.Contains(t, logs.String(), "could not remove archive")
	_, err := os.Stat(filepath.Join(zipPath, "child"))
	assert.NoError(t, err)
}

// --- datasetEntry / extractEntryTo ---

func TestDatasetEntry_RejectsEscapingNames(t *testing.T) {
	for _, entryName := range []string{"../evil.txt", "a/../../evil.txt", "/etc/evil.txt", "dir/../../x"} {
		t.Run(entryName, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "dataset.zip")
			buildZip(t, src, map[string]string{entryName: "payload"})
			dest := t.TempDir()

			err := unzipAndRename(src, dest, "renamed.txt")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "illegal file path")
			entries, readErr := os.ReadDir(dest)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "nothing may be written, inside or outside the destination")
		})
	}
}

// firstEntry opens the zip at path and returns its first file.
func firstEntry(t *testing.T, path string) *zip.File {
	t.Helper()
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	require.NotEmpty(t, r.File)
	return r.File[0]
}

func TestExtractEntryTo_CannotCreateOutput(t *testing.T) {
	src := filepath.Join(t.TempDir(), "dataset.zip")
	buildZip(t, src, map[string]string{"data.txt": "payload"})
	err := extractEntryTo(firstEntry(t, src), underAFile(t, "out.part"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create")
}

func TestExtractEntryTo_CorruptEntryFailsTheCopy(t *testing.T) {
	// A stored (uncompressed) entry whose data byte is flipped fails its CRC
	// check when read to the end.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "data.txt", Method: zip.Store})
	require.NoError(t, err)
	_, err = w.Write([]byte("payload-payload-payload"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	raw := buf.Bytes()
	i := bytes.Index(raw, []byte("payload"))
	require.GreaterOrEqual(t, i, 0)
	raw[i] ^= 0xff
	src := filepath.Join(t.TempDir(), "dataset.zip")
	require.NoError(t, os.WriteFile(src, raw, 0o600))

	err = extractEntryTo(firstEntry(t, src), filepath.Join(t.TempDir(), "out.part"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to extract")
}

// --- pending index writes and migrations ---

func tinyCities() []city.SpatialCity {
	return []city.SpatialCity{
		{City: city.City{Name: "Roc Meler", Latitude: 42.58765, Longitude: 1.7418, Country: "AD"}},
		{City: city.City{Name: "les Escaldes", Latitude: 42.50729, Longitude: 1.53414, Country: "AD"}},
	}
}

func TestPendingWrites_UnwritablePathsFail(t *testing.T) {
	data := &datasetSource{
		cities:      tinyCities(),
		postalCodes: map[string]map[string]dataLoader.PostalCodeEntry{"AD": {"AD100": {PlaceName: "Canillo", Latitude: 42.58, Longitude: 1.66}}},
	}

	s2Finder, writeS2, err := buildS2Index(underAFile(t, "s2.gob"), data)
	require.NoError(t, err)
	err = writeS2()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to serialize S2 index")

	_, writeName, err := buildNameIndex(underAFile(t, "name.gob"), data, s2Finder.Cities)
	require.NoError(t, err)
	err = writeName()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to serialize name index")

	_, writePostal, err := buildPostalCodeIndex(underAFile(t, "postal.gob"), data)
	require.NoError(t, err)
	err = writePostal()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to serialize postal code index")
}

// legacyPostalFinder loads a v3 postal file, which reports LegacyFormat.
func legacyPostalFinder(t *testing.T) *postalCode.Finder {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postal_v3.gob")
	header := struct {
		Magic   string
		Version uint32
		Count   int
	}{"CFPOSTIDX", 3, 1}
	payload := struct {
		PostalCode map[string]map[string]dataLoader.PostalCodeEntry
	}{map[string]map[string]dataLoader.PostalCodeEntry{"AD": {"AD100": {PlaceName: "Canillo", Latitude: 42.58, Longitude: 1.66}}}}
	require.NoError(t, indexfile.Write(path, header, &payload))
	f, err := postalCode.DeserializeIndex(path)
	require.NoError(t, err)
	require.True(t, f.LegacyFormat())
	return f
}

func TestMigratePostalIndex_WriteFailureOnlyWarns(t *testing.T) {
	logs := captureLog(t)
	migratePostalIndex(underAFile(t, "postal.gob"), legacyPostalFinder(t))
	assert.Contains(t, logs.String(), "will retry next boot")
	assert.NotContains(t, logs.String(), "migrated postal code index")
}

func TestMigratePostalIndex_RewritesLegacyFile(t *testing.T) {
	f := legacyPostalFinder(t)
	path := filepath.Join(t.TempDir(), "postal.gob")
	logs := captureLog(t)

	migratePostalIndex(path, f)
	assert.Contains(t, logs.String(), "migrated postal code index")
	rewritten, err := postalCode.DeserializeIndex(path)
	require.NoError(t, err)
	assert.False(t, rewritten.LegacyFormat(), "the file must now be in the current format")
}

func TestMigratePostalIndex_CurrentFormatIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postal.gob")
	current := postalCode.BuildIndex(map[string]map[string]dataLoader.PostalCodeEntry{"AD": {"AD100": {PlaceName: "Canillo"}}})
	logs := captureLog(t)

	migratePostalIndex(path, current)
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "a current-format finder must not be written")
	assert.Empty(t, logs.String())
}

// --- attachNameIndex ---

func TestAttachNameIndex_CityTableMismatch(t *testing.T) {
	s2Finder, err := coordinates.BuildIndex(tinyCities())
	require.NoError(t, err)
	other := name.BuildIndex([]city.SpatialCity{
		{City: city.City{Name: "Elsewhere", Latitude: 1, Longitude: 1, Country: "XX"}},
	})

	err = attachNameIndex("name.gob", other, s2Finder.Cities)
	require.Error(t, err)
	assert.True(t, errors.Is(err, name.ErrCityTableMismatch), "got %v", err)
	assert.Contains(t, err.Error(), "name.gob")
}
