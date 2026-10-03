package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"time"

	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/initializer"
)

func liveHeapMB() float64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// peakRSSMB reads VmHWM (the process's peak resident set) from /proc.
func peakRSSMB() float64 { return procStatusFileMB("VmHWM") }

// rssMB reads VmRSS (the current resident set) from /proc.
func rssMB() float64 { return procStatusFileMB("VmRSS") }

// procStatusFileMB reads field from /proc/self/status, -1 when unavailable
// (non-Linux).
func procStatusFileMB(field string) float64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return -1
	}
	defer f.Close()
	return procStatusMB(f, field)
}

// procStatusMB returns a kB field of a /proc/<pid>/status stream in MB, -1
// when the field is absent or malformed.
func procStatusMB(r io.Reader, field string) float64 {
	s := bufio.NewScanner(r)
	for s.Scan() {
		var kb float64
		if _, err := fmt.Sscanf(s.Text(), field+": %f kB", &kb); err == nil {
			return kb / 1024
		}
	}
	return -1
}

func fileMB(path string) float64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return float64(st.Size()) / (1 << 20)
}

// defaultFuzzyTimeout bounds the wait for the background fuzzy build; the
// largest production-scale build takes minutes, not hours.
const defaultFuzzyTimeout = 30 * time.Minute

// fuzzyPollInterval is how often waitFuzzy samples the build state.
const fuzzyPollInterval = 20 * time.Millisecond

type measureOptions struct {
	cfgPath, dumpPath, profilePath string
	fuzzyTimeout                   time.Duration
}

// waitFuzzy polls state (finder.FuzzyBuildState: 0 not built, 1 building,
// 2 built, 3 disabled) until the build has finished or been disabled. A build
// that failed resets the state to 0, which polling alone cannot tell from
// "not started yet", so it ends with an error when ctx is done first.
func waitFuzzy(ctx context.Context, state func() int32, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for s := state(); s == 0 || s == 1; s = state() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("fuzzy index not built (state %d): %w", s, ctx.Err())
		case <-t.C:
		}
	}
	return nil
}

func measure(ctx context.Context, o measureOptions, stdout io.Writer) error {
	cfgPath, dumpPath, profilePath := o.cfgPath, o.dumpPath, o.profilePath
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	s2Path := filepath.Join(cfg.DatasetsFolder, cfg.S2.IndexFile)
	mode := "warm"
	if _, err := os.Stat(s2Path); err != nil {
		mode = "cold"
	}
	defer log.SetOutput(log.Writer())
	log.SetOutput(io.Discard) // library progress logs
	start := time.Now()
	f, err := initializer.Initialize(cfg)
	if err != nil {
		return err
	}
	initDur := time.Since(start)
	heapInit := liveHeapMB()
	// The same release cmd/server/main performs after init: the boot's
	// transient garbage goes back to the OS now instead of lingering until
	// the background scavenger gets to it.
	rssInit := rssMB()
	debug.FreeOSMemory()
	rssReleased := rssMB()

	start = time.Now()
	f.WarmFuzzy()
	fuzzyCtx, cancel := context.WithTimeout(ctx, o.fuzzyTimeout)
	defer cancel()
	if err := waitFuzzy(fuzzyCtx, f.FuzzyBuildState, fuzzyPollInterval); err != nil {
		return fmt.Errorf("waiting %s: %w", o.fuzzyTimeout, err)
	}
	fuzzyDur := time.Since(start)
	heapFuzzy := liveHeapMB()

	fmt.Fprintf(stdout, "mode: %s\n", mode)
	fmt.Fprintf(stdout, "go: %s GOMAXPROCS=%d\n", runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Fprintf(stdout, "init_seconds: %.2f\n", initDur.Seconds())
	fmt.Fprintf(stdout, "heap_after_init_mb: %.1f\n", heapInit)
	fmt.Fprintf(stdout, "rss_after_init_mb: %.1f\n", rssInit)
	fmt.Fprintf(stdout, "rss_after_release_mb: %.1f\n", rssReleased)
	fmt.Fprintf(stdout, "fuzzy_build_seconds: %.2f\n", fuzzyDur.Seconds())
	fmt.Fprintf(stdout, "heap_with_fuzzy_mb: %.1f\n", heapFuzzy)
	fmt.Fprintf(stdout, "fuzzy_mb: %.1f\n", heapFuzzy-heapInit)
	fmt.Fprintf(stdout, "file_s2_mb: %.1f\n", fileMB(s2Path))
	fmt.Fprintf(stdout, "file_name_mb: %.1f\n", fileMB(filepath.Join(cfg.DatasetsFolder, cfg.NameIndexFile)))
	fmt.Fprintf(stdout, "file_postal_mb: %.1f\n", fileMB(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodeIndexFile)))

	if profilePath != "" {
		pf, err := os.Create(profilePath)
		if err != nil {
			return err
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(pf); err != nil {
			return err
		}
		if err := pf.Close(); err != nil {
			return err
		}
	}
	if dumpPath != "" {
		if err := dumpTranscript(f, cfg, dumpPath); err != nil {
			return err
		}
	}
	// Last: the peak covers init, the fuzzy build and the transcript.
	fmt.Fprintf(stdout, "peak_rss_mb: %.1f\n", peakRSSMB())
	runtime.KeepAlive(f)
	return nil
}
