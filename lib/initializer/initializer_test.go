package initializer

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- downloadFile ---

func TestDownloadFile_HTTPError404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "allCountries.zip")

	err := downloadFile(dst, srv.URL)
	require.Error(t, err, "a 404 response must fail the download")
	assert.Contains(t, err.Error(), "404")

	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "no final file may be left behind after a failed download")
	_, statErr = os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may be left behind after a failed download")
}

func TestDownloadFile_Success(t *testing.T) {
	payload := []byte("geonames test payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "allCountries.zip")

	require.NoError(t, downloadFile(dst, srv.URL))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may remain after a successful download")
}

func TestDownloadFile_ServerUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately so the connection fails

	dir := t.TempDir()
	dst := filepath.Join(dir, "allCountries.zip")

	err := downloadFile(dst, srv.URL)
	require.Error(t, err)

	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "no final file may be left behind after a failed download")
	_, statErr = os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may be left behind after a failed download")
}

func TestDownloadFile_ClientTimeout(t *testing.T) {
	origClient := httpClient
	defer func() { httpClient = origClient }()
	httpClient = &http.Client{Timeout: 100 * time.Millisecond}

	// Handler stalls without ever sending headers; it exits early once the
	// client aborts so srv.Close() below does not block.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "allCountries.zip")

	err := downloadFile(dst, srv.URL)
	require.Error(t, err, "a stalled response must hit the client timeout")
	assert.Contains(t, err.Error(), "Client.Timeout")

	_, statErr := os.Stat(dst)
	assert.True(t, os.IsNotExist(statErr), "no final file may be left behind after a timed-out download")
	_, statErr = os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may be left behind after a timed-out download")
}

// --- unzipAndRename ---

// buildZip builds an in-memory zip archive with the given entries
// (name -> content) and writes it to path.
func buildZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// deterministic order for multi-entry archives
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	for _, name := range names {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(entries[name]))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func TestUnzipAndRename_SingleEntry(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dataset.zip")
	buildZip(t, src, map[string]string{"allCountries.txt": "line1\nline2\n"})

	dest := t.TempDir()
	require.NoError(t, unzipAndRename(src, dest, "renamed.txt"))

	got, err := os.ReadFile(filepath.Join(dest, "renamed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2\n", string(got))
}

func TestUnzipAndRename_MultiEntryRejected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dataset.zip")
	buildZip(t, src, map[string]string{
		"allCountries.txt": "line1\n",
		"extra.txt":        "line2\n",
	})

	dest := t.TempDir()
	err := unzipAndRename(src, dest, "renamed.txt")
	require.Error(t, err, "multi-entry archives must be rejected explicitly")
	assert.Contains(t, err.Error(), "2 entries")
}

func TestUnzipAndRename_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dataset.zip")
	buildZip(t, src, map[string]string{"../evil.txt": "payload"})

	dest := t.TempDir()
	err := unzipAndRename(src, dest, "renamed.txt")
	require.Error(t, err, "zip-slip entries must be rejected")
	assert.Contains(t, err.Error(), "illegal file path")

	// nothing may have been written outside dest
	_, statErr := os.Stat(filepath.Join(dir, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr), "no file may escape the destination directory")
}

func TestUnzipAndRename_CorruptArchive(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dataset.zip")
	require.NoError(t, os.WriteFile(src, []byte("this is not a zip file"), 0o600))

	err := unzipAndRename(src, dir, "renamed.txt")
	require.Error(t, err)
}

// --- downloadAndExtractDataset ---

// validZipBytes builds a single-entry zip in memory and returns its bytes.
func validZipBytes(t *testing.T, entry, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(entry)
	require.NoError(t, err)
	_, err = w.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestDownloadAndExtract_RecoversFromCorruptExistingZip(t *testing.T) {
	zipBytes := validZipBytes(t, "allCountries.txt", "recovered payload\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config.Config{DatasetsFolder: dir}

	// Simulate the bricked state left by a previously failed download:
	// a corrupt zip sits where the dataset should be, final file missing.
	zipPath := filepath.Join(dir, "allCountries.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("<html>404 page saved as zip</html>"), 0o600))

	require.NoError(t, downloadAndExtractDataset(srv.URL, "allCountries.zip", "allCountries_dump.txt", cfg))

	got, err := os.ReadFile(filepath.Join(dir, "allCountries_dump.txt"))
	require.NoError(t, err)
	assert.Equal(t, "recovered payload\n", string(got))
}

func TestDownloadAndExtract_CorruptZipAndFailingServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config.Config{DatasetsFolder: dir}
	zipPath := filepath.Join(dir, "allCountries.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("corrupt"), 0o600))

	err := downloadAndExtractDataset(srv.URL, "allCountries.zip", "allCountries_dump.txt", cfg)
	require.Error(t, err, "when the re-download also fails, the error must surface")
}

func TestDownloadAndExtract_SkipsWhenFinalFileExists(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DatasetsFolder: dir}
	finalPath := filepath.Join(dir, "allCountries_dump.txt")
	require.NoError(t, os.WriteFile(finalPath, []byte("existing"), 0o600))

	// No server needed: everything must be skipped.
	require.NoError(t, downloadAndExtractDataset("http://127.0.0.1:0/unused", "allCountries.zip", "allCountries_dump.txt", cfg))

	got, err := os.ReadFile(finalPath)
	require.NoError(t, err)
	assert.Equal(t, "existing", string(got), "existing extracted dataset must be left untouched")
}

// --- warm-start path resolution predicate ---

func TestIndexPaths_Resolution(t *testing.T) {
	cfg := &config.Config{
		DatasetsFolder:      string(filepath.Separator) + filepath.Join("data", "datasets"),
		NameIndexFile:       "name_index.gob",
		PostalCodeIndexFile: "postal_code_index.gob",
		S2:                  config.S2{IndexFile: "s2index.gob"},
	}
	s2Path, namePath, postalPath := indexFilePaths(cfg)

	root := string(filepath.Separator) + filepath.Join("data", "datasets")
	assert.Equal(t, filepath.Join(root, "s2index.gob"), s2Path)
	assert.Equal(t, filepath.Join(root, "name_index.gob"), namePath)
	assert.Equal(t, filepath.Join(root, "postal_code_index.gob"), postalPath)
}

func TestAllIndexesPresent(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 3)
	for i := range paths {
		paths[i] = filepath.Join(dir, fmt.Sprintf("index%d.gob", i))
		require.NoError(t, os.WriteFile(paths[i], []byte("x"), 0o600))
	}
	assert.True(t, allIndexesPresent(paths...))

	// Each index individually must matter.
	for _, missing := range paths {
		require.NoError(t, os.Remove(missing))
		assert.False(t, allIndexesPresent(paths...), "predicate must fail when %s is missing", missing)
		require.NoError(t, os.WriteFile(missing, []byte("x"), 0o600))
	}

	assert.False(t, allIndexesPresent(filepath.Join(dir, "never-created.gob")))
}

// --- ensureFinders warm-start skip ---

func testConfig(dir string) *config.Config {
	return &config.Config{
		DatasetsFolder:      dir,
		AllCitiesFile:       "cities.txt",
		PostalCodesFile:     "postal.txt",
		NameIndexFile:       "name_index.gob",
		PostalCodeIndexFile: "postal_code_index.gob",
		S2: config.S2{
			MinLevel:  10,
			MaxLevel:  15,
			MaxCells:  8,
			IndexFile: "s2index.gob",
		},
	}
}

func writeTinyDatasets(t *testing.T, cfg *config.Config) {
	t.Helper()
	cities := "2994701\tRoc Meler\tRoc Meler\tRoc Mele,Roc Meler\t42.58765\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03\n" +
		"3040051\tles Escaldes\tles Escaldes\tEscaldes\t42.50729\t1.53414\tPPLA\tAD\tAD\t\t07\t\t\t\t16316\t\t\t1032\tEurope/Andorra\t2023-10-03\n"
	postal := "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t42.5833\t1.6667\t6\n" +
		"AD\tAD200\tEncamp\tEncamp\t03\t\t\t\t\t42.5347\t1.5801\t6\n"
	require.NoError(t, os.WriteFile(filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile), []byte(cities), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile), []byte(postal), 0o600))
}

func TestEnsureFinders_WarmStartSkipsDatasetLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	// Cold start: builds and serializes all three indexes.
	finder1, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, finder1)
	for _, name := range []string{cfg.S2.IndexFile, cfg.NameIndexFile, cfg.PostalCodeIndexFile} {
		_, statErr := os.Stat(filepath.Join(dir, name))
		assert.NoError(t, statErr, "index %s should exist after cold start", name)
	}

	// Remove the raw datasets: a warm start that still called loadData
	// would fail to open them. Skipping the load must succeed via the
	// serialized indexes alone.
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.AllCitiesFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, cfg.PostalCodesFile)))

	finder2, err := ensureFinders(cfg)
	require.NoError(t, err, "warm start must skip the dataset load when all indexes exist")
	require.NotNil(t, finder2)
	require.NotNil(t, finder2.S2Finder)
	require.NotNil(t, finder2.NameFinder)
	require.NotNil(t, finder2.PostalCodeFinder)
}

func TestEnsureFinders_ColdStartLoadsDatasets(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	writeTinyDatasets(t, cfg)

	f, err := ensureFinders(cfg)
	require.NoError(t, err)
	require.NotNil(t, f)

	// The built finders must actually contain the loaded data.
	cityResult, _, err := f.S2Finder.NearestPlace(42.5876, 1.7418)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(cityResult.Name, "Roc Meler"), "unexpected nearest city %q", cityResult.Name)
}
