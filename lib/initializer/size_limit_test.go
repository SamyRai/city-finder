package initializer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// limitedDownloader is a fast downloader with a small body ceiling.
func limitedDownloader(max int64) *downloader {
	d := fastDownloader()
	d.maxBytes = max
	return d
}

func TestDownload_BodyOverLimitFailsWithoutRetryOrLeftovers(t *testing.T) {
	// Chunked (no Content-Length) so the body bound, not the header check,
	// is what trips.
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		for range 4 {
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), "data.zip")
	err := limitedDownloader(300).download(context.Background(), dst, srv.URL)
	require.Error(t, err)
	var limitErr *sizeLimitError
	require.ErrorAs(t, err, &limitErr)
	assert.Contains(t, err.Error(), "download limit")
	assert.EqualValues(t, 1, requests.Load(), "an oversized body is permanent and must not be retried")
	for _, p := range []string{dst, dst + ".part"} {
		_, statErr := os.Stat(p)
		assert.True(t, os.IsNotExist(statErr), "%s must not be left behind", p)
	}
}

func TestDownload_DeclaredContentLengthOverLimitFailsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1000))) // sets Content-Length: 1000
	}))
	t.Cleanup(srv.Close)

	dst := filepath.Join(t.TempDir(), "data.zip")
	err := limitedDownloader(300).download(context.Background(), dst, srv.URL)
	var limitErr *sizeLimitError
	require.ErrorAs(t, err, &limitErr)
	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "nothing may be written for a declared oversize")
}

func TestDownload_BodyExactlyAtLimitSucceeds(t *testing.T) {
	srv, _ := flakyServer(t, 0, 0, strings.Repeat("x", 300))
	dst := filepath.Join(t.TempDir(), "data.zip")
	require.NoError(t, limitedDownloader(300).download(context.Background(), dst, srv.URL))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Len(t, got, 300)
}

func TestNewDownloader_HasDefaultCeiling(t *testing.T) {
	assert.Equal(t, defaultMaxDownloadBytes, newDownloader().maxBytes)
	// Real GeoNames archives are ~422 MB; the ceiling must clear them.
	assert.Greater(t, defaultMaxDownloadBytes, int64(422_497_095))
	// The extracted cities file is ~1.6 GB.
	assert.Greater(t, maxExtractedBytes, int64(1_700_000_000))
}

// craftZip writes a single-entry stored zip holding payload.
func craftZip(t *testing.T, payload string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "allCountries.txt", Method: zip.Store})
	require.NoError(t, err)
	_, err = w.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	p := filepath.Join(t.TempDir(), "a.zip")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o600))
	return p
}

func TestUnzip_DeclaredSizeOverLimitRejectedBeforeExtracting(t *testing.T) {
	src := craftZip(t, strings.Repeat("x", 2000))
	dest := t.TempDir()
	err := unzipAndRenameLimit(src, dest, "out.txt", 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
	entries, readErr := os.ReadDir(dest)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no part file may be created for a declared oversize")
}

func TestUnzip_EntryAtLimitExtracts(t *testing.T) {
	src := craftZip(t, strings.Repeat("x", 1000))
	dest := t.TempDir()
	require.NoError(t, unzipAndRenameLimit(src, dest, "out.txt", 1000))
	got, err := os.ReadFile(filepath.Join(dest, "out.txt"))
	require.NoError(t, err)
	assert.Len(t, got, 1000)
}

// TestUnzip_UnderstatedSizeStillBoundedByActualCopy: the declared size is
// attacker-controlled. The central directory here claims 10 bytes for a
// 2000-byte stored entry; the declared check passes, so the copy is what
// has to stop it.
func TestUnzip_UnderstatedSizeStillBoundedByActualCopy(t *testing.T) {
	src := craftZip(t, strings.Repeat("x", 2000))
	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	i := bytes.Index(raw, []byte("PK\x01\x02"))
	require.GreaterOrEqual(t, i, 0)
	binary.LittleEndian.PutUint32(raw[i+24:], 10) // central-directory uncompressed size
	require.NoError(t, os.WriteFile(src, raw, 0o600))

	dest := t.TempDir()
	err = unzipAndRenameLimit(src, dest, "out.txt", 1000)
	// archive/zip itself refuses to yield more bytes than the header declares
	// (zip.ErrFormat), so either that or our own limit error may fire first;
	// what matters is that the lie never produces a file.
	require.Error(t, err)
	entries, readErr := os.ReadDir(dest)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "the partial extraction must be removed")
}
