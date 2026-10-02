package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
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
func peakRSSMB() float64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return -1
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		var kb float64
		if _, err := fmt.Sscanf(s.Text(), "VmHWM: %f kB", &kb); err == nil {
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

func measure(cfgPath, dumpPath, profilePath string) error {
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	s2Path := filepath.Join(cfg.DatasetsFolder, cfg.S2.IndexFile)
	mode := "warm"
	if _, err := os.Stat(s2Path); err != nil {
		mode = "cold"
	}
	log.SetOutput(io.Discard) // library progress logs
	start := time.Now()
	f, err := initializer.Initialize(cfg)
	if err != nil {
		return err
	}
	initDur := time.Since(start)
	heapInit := liveHeapMB()

	start = time.Now()
	f.WarmFuzzy()
	for f.FuzzyBuildState() == 1 || f.FuzzyBuildState() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	fuzzyDur := time.Since(start)
	heapFuzzy := liveHeapMB()

	fmt.Printf("mode: %s\n", mode)
	fmt.Printf("go: %s GOMAXPROCS=%d\n", runtime.Version(), runtime.GOMAXPROCS(0))
	fmt.Printf("init_seconds: %.2f\n", initDur.Seconds())
	fmt.Printf("heap_after_init_mb: %.1f\n", heapInit)
	fmt.Printf("fuzzy_build_seconds: %.2f\n", fuzzyDur.Seconds())
	fmt.Printf("heap_with_fuzzy_mb: %.1f\n", heapFuzzy)
	fmt.Printf("fuzzy_mb: %.1f\n", heapFuzzy-heapInit)
	fmt.Printf("file_s2_mb: %.1f\n", fileMB(s2Path))
	fmt.Printf("file_name_mb: %.1f\n", fileMB(filepath.Join(cfg.DatasetsFolder, cfg.NameIndexFile)))
	fmt.Printf("file_postal_mb: %.1f\n", fileMB(filepath.Join(cfg.DatasetsFolder, cfg.PostalCodeIndexFile)))

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
	fmt.Printf("peak_rss_mb: %.1f\n", peakRSSMB())
	runtime.KeepAlive(f)
	return nil
}
