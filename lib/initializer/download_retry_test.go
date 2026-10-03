package initializer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastDownloader is the production downloader with retries that back off by
// milliseconds, not seconds.
func fastDownloader() *downloader {
	d := newDownloader()
	d.retryDelay = time.Millisecond
	return d
}

// flakyServer fails the first `failures` requests with `status`, then serves
// payload; it counts every request.
func flakyServer(t *testing.T, failures int64, status int, payload string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= failures {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestDownloadFile_RetriesTransientStatus(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusTooManyRequests} {
		srv, requests := flakyServer(t, defaultDownloadAttempts-1, status, "payload")
		dst := filepath.Join(t.TempDir(), "data.zip")
		require.NoError(t, fastDownloader().download(context.Background(), dst, srv.URL), "status %d", status)
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, "payload", string(got))
		assert.EqualValues(t, defaultDownloadAttempts, requests.Load(), "status %d", status)
	}
}

func TestDownloadFile_GivesUpAfterLastAttempt(t *testing.T) {
	srv, requests := flakyServer(t, 100, http.StatusServiceUnavailable, "")
	dst := filepath.Join(t.TempDir(), "data.zip")
	err := fastDownloader().download(context.Background(), dst, srv.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attempt 3 of 3")
	assert.EqualValues(t, defaultDownloadAttempts, requests.Load())
	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may be left behind")
}

func TestDownloadFile_PermanentStatusIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest} {
		srv, requests := flakyServer(t, 100, status, "")
		err := fastDownloader().download(context.Background(), filepath.Join(t.TempDir(), "data.zip"), srv.URL)
		require.Error(t, err, "status %d", status)
		assert.EqualValues(t, 1, requests.Load(), "status %d must not be retried", status)
	}
}

// TestDownloadFile_RetriesDroppedBody: a connection cut mid-body (the common
// failure on a 400 MB download) is retried, and the retry's body wins.
func TestDownloadFile_RetriesDroppedBody(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("partial"))
			panic(http.ErrAbortHandler) // drop the connection mid-body
		}
		_, _ = w.Write([]byte("complete"))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "data.zip")
	require.NoError(t, fastDownloader().download(context.Background(), dst, srv.URL))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "complete", string(got))
	assert.EqualValues(t, 2, requests.Load())
}

func TestDownloadFile_TimeoutIsNotRetried(t *testing.T) {
	dl := fastDownloader()
	dl.client = &http.Client{Timeout: 50 * time.Millisecond}

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	defer srv.Close()

	require.Error(t, dl.download(context.Background(), filepath.Join(t.TempDir(), "data.zip"), srv.URL))
	assert.EqualValues(t, 1, requests.Load(), "a timeout already spent the download budget")
}

// TestDownload_CancelDuringBackoff: the pause between attempts ends when the
// context does, instead of sleeping out an hour-long delay.
func TestDownload_CancelDuringBackoff(t *testing.T) {
	srv, requests := flakyServer(t, 100, http.StatusServiceUnavailable, "")
	dl := newDownloader()
	dl.retryDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for requests.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	done := make(chan error, 1)
	dst := filepath.Join(t.TempDir(), "data.zip")
	go func() { done <- dl.download(ctx, dst, srv.URL) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("download kept sleeping after its context was cancelled")
	}
	assert.EqualValues(t, 1, requests.Load(), "no second attempt after cancellation")
	_, statErr := os.Stat(dst + ".part")
	assert.True(t, os.IsNotExist(statErr), "no .part file may be left behind")
}

// TestDownload_CancelledContextNeverRequests: an already-cancelled context
// fails fast without a retry cycle.
func TestDownload_CancelledContextNeverRequests(t *testing.T) {
	srv, requests := flakyServer(t, 0, 0, "payload")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := fastDownloader().download(ctx, filepath.Join(t.TempDir(), "data.zip"), srv.URL)
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 0, requests.Load())
}
