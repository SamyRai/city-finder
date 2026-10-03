package main

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
)

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.2f ms", float64(d.Nanoseconds())/1e6)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", minutes, seconds)
}

func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	if n < 1000000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	return fmt.Sprintf("%.1fB", float64(n)/1000000000)
}

// printBanner writes a title between two 70-column rules.
func printBanner(w io.Writer, title string) {
	rule := strings.Repeat("=", 70)
	fmt.Fprintln(w, rule)
	fmt.Fprintf(w, "  %s\n", title)
	fmt.Fprintln(w, rule)
}

func printSystemInfo(w io.Writer) {
	fmt.Fprintf(w, "\nSystem Info:\n")
	fmt.Fprintf(w, "  Go Version:  %s\n", runtime.Version())
	fmt.Fprintf(w, "  OS:          %s\n", runtime.GOOS)
	fmt.Fprintf(w, "  Architecture: %s\n", runtime.GOARCH)
	fmt.Fprintf(w, "  CPU Cores:   %d\n", runtime.NumCPU())
	fmt.Fprintln(w)
}

func printResult(w io.Writer, result OperationResult) {
	fmt.Fprintf(w, "\n%s\n", result.Name)
	fmt.Fprintln(w, strings.Repeat("=", len(result.Name)))
	fmt.Fprintf(w, "  Duration:        %s\n", formatDuration(result.Duration))
	fmt.Fprintf(w, "  Items Processed: %s\n", formatNumber(int64(result.ItemsProcessed)))
	fmt.Fprintf(w, "  Throughput:      %s/sec\n", formatNumber(int64(result.Throughput)))
	fmt.Fprintf(w, "  Memory Delta:    %s\n", formatBytes(result.MemoryDelta))
	fmt.Fprintf(w, "  Memory After:    %s\n", formatBytes(result.MemoryAfter.HeapAlloc))
	fmt.Fprintf(w, "  GC Cycles:       %d\n", result.GCsDelta)
	if result.GCsDelta > 0 {
		fmt.Fprintf(w, "  GC Time:         ~%s (estimated)\n", formatDuration(result.Duration/time.Duration(result.GCsDelta)))
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: go run ./cmd/build-index [mode]")
	fmt.Fprintln(w, "Modes:")
	fmt.Fprintln(w, "  test - Use small test dataset (default)")
	fmt.Fprintln(w, "  prod - Use full production dataset")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  go run ./cmd/build-index test")
	fmt.Fprintln(w, "  go run ./cmd/build-index prod")
}

// summary is what the closing report prints.
type summary struct {
	mode         string
	total        time.Duration
	cities       int
	postalCodes  int
	initialMem   MemoryStats
	finalMem     MemoryStats
	load, s2     OperationResult
	name, postal OperationResult
}

func printSummary(w io.Writer, s summary) {
	fmt.Fprintln(w)
	printBanner(w, "SUMMARY")
	fmt.Fprintf(w, "\nMode:               %s\n", s.mode)
	fmt.Fprintf(w, "Total Duration:     %s\n", formatDuration(s.total))
	fmt.Fprintf(w, "Total Cities:        %s\n", formatNumber(int64(s.cities)))
	fmt.Fprintf(w, "Total Postal Codes:  %s\n", formatNumber(int64(s.postalCodes)))
	fmt.Fprintf(w, "\nMemory Usage:\n")
	fmt.Fprintf(w, "  Initial Heap:     %s\n", formatBytes(s.initialMem.HeapAlloc))
	fmt.Fprintf(w, "  Final Heap:       %s\n", formatBytes(s.finalMem.HeapAlloc))
	fmt.Fprintf(w, "  Peak Heap:        %s\n", formatBytes(s.finalMem.HeapSys))
	fmt.Fprintf(w, "  Total Allocated:  %s\n", formatBytes(s.finalMem.TotalAlloc))
	fmt.Fprintf(w, "  GC Cycles:        %d\n", s.finalMem.NumGC-s.initialMem.NumGC)
	fmt.Fprintf(w, "\nThroughput Summary:\n")
	fmt.Fprintf(w, "  Data Loading:     %s cities/sec\n", formatNumber(int64(s.load.Throughput)))
	fmt.Fprintf(w, "  S2 Index:         %s cities/sec\n", formatNumber(int64(s.s2.Throughput)))
	fmt.Fprintf(w, "  Name Index:       %s cities/sec\n", formatNumber(int64(s.name.Throughput)))
	if s.postal.Throughput > 0 {
		fmt.Fprintf(w, "  Postal Index:     %s entries/sec\n", formatNumber(int64(s.postal.Throughput)))
	}
	fmt.Fprintln(w)
}
