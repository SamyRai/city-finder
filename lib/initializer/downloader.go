package initializer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	// downloadTimeout bounds an entire dataset download (headers plus
	// body). The GeoNames allCountries archives are ~400MB, so the timeout
	// must accommodate slow links: 15 minutes still allows ~450KB/s.
	downloadTimeout = 15 * time.Minute

	// defaultDownloadAttempts bounds how often a transient failure is tried.
	defaultDownloadAttempts = 3

	// defaultRetryDelay is the first backoff, doubled per retry.
	defaultRetryDelay = 2 * time.Second
)

// downloader fetches dataset files over HTTP. It owns everything a download
// needs, so nothing about it is package state: tests build one with a short
// timeout or a millisecond retry delay instead of patching globals.
type downloader struct {
	client     *http.Client
	attempts   int           // tries per download, first one included
	retryDelay time.Duration // first backoff, doubled per retry
}

// newDownloader returns the production configuration.
func newDownloader() *downloader {
	return &downloader{
		client:     &http.Client{Timeout: downloadTimeout},
		attempts:   defaultDownloadAttempts,
		retryDelay: defaultRetryDelay,
	}
}

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

// download fetches url into dst atomically. The body is streamed into
// dst+".part" and only renamed to dst after a complete, status-verified
// transfer, so a failed download (network error, non-2xx status, timeout)
// never leaves a corrupt file behind that would poison every later startup.
// Cancelling ctx aborts a transfer in flight and a backoff in progress.
func (d *downloader) download(ctx context.Context, dst string, url string) error {
	partPath := dst + ".part"

	for attempt := 1; ; attempt++ {
		err := d.fetch(ctx, partPath, url)
		if err == nil {
			break
		}
		_ = os.Remove(partPath) // never leave a partial download behind
		if ctx.Err() != nil {
			return fmt.Errorf("failed to download %s: %w", url, ctx.Err())
		}
		if attempt >= d.attempts || !retryableDownloadError(err) {
			return fmt.Errorf("failed to download %s (attempt %d of %d): %w", url, attempt, d.attempts, err)
		}
		delay := d.retryDelay << (attempt - 1)
		log.Printf("download %s failed (attempt %d of %d): %v; retrying in %s", url, attempt, d.attempts, err, delay)
		if err := sleepContext(ctx, delay); err != nil {
			return fmt.Errorf("failed to download %s: %w", url, err)
		}
	}

	if err := os.Rename(partPath, dst); err != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("failed to move %s to %s: %w", partPath, dst, err)
	}
	return nil
}

// fetch makes one attempt, leaving a complete, synced partPath on success.
func (d *downloader) fetch(ctx context.Context, partPath, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid request for %s: %w", url, err)
	}
	resp, err := d.client.Do(req)
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

// sleepContext waits for d or until ctx is done, whichever comes first, and
// returns ctx's error in the second case.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
