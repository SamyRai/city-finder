package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/config"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
)

type MemoryStats struct {
	Alloc      uint64 // bytes allocated
	TotalAlloc uint64 // bytes allocated (cumulative)
	Sys        uint64 // bytes obtained from system
	NumGC      uint32 // number of GC cycles
	HeapAlloc  uint64 // bytes allocated and not yet freed
	HeapSys    uint64 // bytes obtained from system for heap
	HeapInuse  uint64 // bytes in use
	HeapIdle   uint64 // bytes idle
}

func getMemoryStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemoryStats{
		Alloc:      m.Alloc,
		TotalAlloc: m.TotalAlloc,
		Sys:        m.Sys,
		NumGC:      m.NumGC,
		HeapAlloc:  m.HeapAlloc,
		HeapSys:    m.HeapSys,
		HeapInuse:  m.HeapInuse,
		HeapIdle:   m.HeapIdle,
	}
}

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

type OperationResult struct {
	Name           string
	Duration       time.Duration
	MemoryBefore   MemoryStats
	MemoryAfter    MemoryStats
	MemoryDelta    uint64
	ItemsProcessed int
	Throughput     float64
	GCsBefore      uint32
	GCsAfter       uint32
	GCsDelta       uint32
}

func measureOperation(name string, items int, operation func()) OperationResult {
	// Force GC before measurement for accurate baseline
	runtime.GC()
	time.Sleep(100 * time.Millisecond) // Give GC time to complete

	memBefore := getMemoryStats()
	start := time.Now()

	operation()

	duration := time.Since(start)
	memAfter := getMemoryStats()

	memoryDelta := memAfter.HeapAlloc - memBefore.HeapAlloc
	if memAfter.HeapAlloc < memBefore.HeapAlloc {
		// If memory decreased, use Sys as delta (actual memory used)
		memoryDelta = memAfter.Sys - memBefore.Sys
	}

	throughput := float64(items) / duration.Seconds()

	return OperationResult{
		Name:           name,
		Duration:       duration,
		MemoryBefore:   memBefore,
		MemoryAfter:    memAfter,
		MemoryDelta:    memoryDelta,
		ItemsProcessed: items,
		Throughput:     throughput,
		GCsBefore:      memBefore.NumGC,
		GCsAfter:       memAfter.NumGC,
		GCsDelta:       memAfter.NumGC - memBefore.NumGC,
	}
}

func printResult(result OperationResult) {
	fmt.Printf("\n%s\n", result.Name)
	for i := 0; i < len(result.Name); i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Printf("  Duration:        %s\n", formatDuration(result.Duration))
	fmt.Printf("  Items Processed: %s\n", formatNumber(int64(result.ItemsProcessed)))
	fmt.Printf("  Throughput:      %s/sec\n", formatNumber(int64(result.Throughput)))
	fmt.Printf("  Memory Delta:    %s\n", formatBytes(result.MemoryDelta))
	fmt.Printf("  Memory After:    %s\n", formatBytes(result.MemoryAfter.HeapAlloc))
	fmt.Printf("  GC Cycles:       %d\n", result.GCsDelta)
	if result.GCsDelta > 0 {
		fmt.Printf("  GC Time:         ~%s (estimated)\n", formatDuration(result.Duration/time.Duration(result.GCsDelta)))
	}
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

func main() {
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Println("  FULL INDEX BUILD - Time & Memory Tracking")
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Printf("\nSystem Info:\n")
	fmt.Printf("  Go Version:  %s\n", runtime.Version())
	fmt.Printf("  OS:          %s\n", runtime.GOOS)
	fmt.Printf("  Architecture: %s\n", runtime.GOARCH)
	fmt.Printf("  CPU Cores:   %d\n", runtime.NumCPU())
	fmt.Println()

	// Parse command line arguments
	var mode string
	if len(os.Args) > 1 {
		mode = os.Args[1]
	} else {
		mode = "test" // Default to test mode
	}

	// Validate mode
	if mode != "test" && mode != "prod" {
		fmt.Println("Usage: go run cmd/build-index/main.go [mode]")
		fmt.Println("Modes:")
		fmt.Println("  test - Use small test dataset (default)")
		fmt.Println("  prod - Use full production dataset")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  go run cmd/build-index/main.go test")
		fmt.Println("  go run cmd/build-index/main.go prod")
		os.Exit(1)
	}

	// Set data file paths and output directory based on mode
	var dataFile, postalCodeFile, outputDir string
	if mode == "test" {
		dataFile = "testdata/allCountries.txt"
		postalCodeFile = "testdata/zipCodes.txt"
		outputDir = "testdata"
	} else { // mode == "prod"
		dataFile = "datasets/allCountries.txt"
		postalCodeFile = "datasets/zipCodes.txt"
		outputDir = "datasets"
	}

	fmt.Printf("Mode:         %s\n", mode)
	fmt.Printf("Data Files:\n")
	fmt.Printf("  Cities:      %s\n", dataFile)
	fmt.Printf("  Postal Codes: %s\n", postalCodeFile)
	fmt.Println()

	// Initial memory state
	initialMem := getMemoryStats()
	fmt.Printf("Initial Memory State:\n")
	fmt.Printf("  Heap Alloc:  %s\n", formatBytes(initialMem.HeapAlloc))
	fmt.Printf("  Heap Sys:    %s\n", formatBytes(initialMem.HeapSys))
	fmt.Printf("  GC Cycles:   %d\n", initialMem.NumGC)
	fmt.Println()

	overallStart := time.Now()
	var cities []city.SpatialCity
	var postalCodes map[string]map[string]dataLoader.PostalCodeEntry

	// Step 1: Load Cities Data
	loadResult := measureOperation("1. Loading Cities Data", 0, func() {
		var err error
		cities, err = dataLoader.LoadGeoNamesCSV(dataFile)
		if err != nil {
			log.Fatalf("Failed to load cities: %v", err)
		}
	})
	loadResult.ItemsProcessed = len(cities)
	loadResult.Throughput = float64(len(cities)) / loadResult.Duration.Seconds()
	printResult(loadResult)

	// Step 2: Load Postal Codes
	postalLoadResult := measureOperation("2. Loading Postal Codes", 0, func() {
		var err error
		postalCodes, err = dataLoader.LoadPostalCodes(postalCodeFile)
		if err != nil {
			log.Printf("Warning: failed to load postal codes: %v", err)
			postalCodes = make(map[string]map[string]dataLoader.PostalCodeEntry)
		}
	})
	totalPostalEntries := 0
	for _, countryCodes := range postalCodes {
		totalPostalEntries += len(countryCodes)
	}
	postalLoadResult.ItemsProcessed = totalPostalEntries
	if postalLoadResult.Duration > 0 {
		postalLoadResult.Throughput = float64(totalPostalEntries) / postalLoadResult.Duration.Seconds()
	}
	printResult(postalLoadResult)

	// Step 3: Build S2 Index
	s2Config := &config.S2{
		MinLevel: 10,
		MaxLevel: 16,
		MaxCells: 8,
	}
	var s2Finder *coordinates.S2Finder
	s2Result := measureOperation("3. Building S2 Spatial Index", len(cities), func() {
		var err error
		s2Finder, err = coordinates.BuildIndex(cities, s2Config)
		if err != nil {
			log.Fatalf("Failed to build S2 index: %v", err)
		}
	})
	printResult(s2Result)

	// Step 4: Build Name Index
	var nameFinder *name.Finder
	nameResult := measureOperation("4. Building Name Index", len(cities), func() {
		nameFinder = name.BuildIndex(cities)
	})
	printResult(nameResult)

	// Step 5: Build Postal Code Index
	var postalFinder *postalCode.Finder
	postalResult := measureOperation("5. Building Postal Code Index", totalPostalEntries, func() {
		postalFinder = postalCode.BuildIndex(postalCodes)
	})
	printResult(postalResult)

	// Step 6: Serialize indexes to testdata directory
	fmt.Println()
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Printf("  SERIALIZING INDEXES TO %s\n", strings.ToUpper(outputDir))
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()

	serializeResult := measureOperation("6. Serializing Indexes", 0, func() {
		// Determine filename suffix based on mode
		fileSuffix := "_test.gob"
		if mode == "prod" {
			fileSuffix = ".gob"
		}

		// Serialize S2 index
		s2IndexPath := fmt.Sprintf("%s/s2index%s", outputDir, fileSuffix)
		if err := s2Finder.SerializeIndex(s2IndexPath); err != nil {
			log.Fatalf("Failed to serialize S2 index: %v", err)
		}
		fmt.Printf("  ✓ S2 index saved to: %s\n", s2IndexPath)

		// Serialize Name index
		nameIndexPath := fmt.Sprintf("%s/name_index%s", outputDir, fileSuffix)
		if err := nameFinder.SerializeIndex(nameIndexPath); err != nil {
			log.Fatalf("Failed to serialize Name index: %v", err)
		}
		fmt.Printf("  ✓ Name index saved to: %s\n", nameIndexPath)

		// Serialize Postal Code index
		postalIndexPath := fmt.Sprintf("%s/postal_code_index%s", outputDir, fileSuffix)
		if err := postalFinder.SerializeIndex(postalIndexPath); err != nil {
			log.Fatalf("Failed to serialize Postal Code index: %v", err)
		}
		fmt.Printf("  ✓ Postal Code index saved to: %s\n", postalIndexPath)
	})
	printResult(serializeResult)

	// Final summary
	overallDuration := time.Since(overallStart)
	finalMem := getMemoryStats()

	fmt.Println()
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Println("  SUMMARY")
	for i := 0; i < 70; i++ {
		fmt.Print("=")
	}
	fmt.Println()
	fmt.Printf("\nMode:               %s\n", mode)
	fmt.Printf("Total Duration:     %s\n", formatDuration(overallDuration))
	fmt.Printf("Total Cities:        %s\n", formatNumber(int64(len(cities))))
	fmt.Printf("Total Postal Codes:  %s\n", formatNumber(int64(totalPostalEntries)))
	fmt.Printf("\nMemory Usage:\n")
	fmt.Printf("  Initial Heap:     %s\n", formatBytes(initialMem.HeapAlloc))
	fmt.Printf("  Final Heap:       %s\n", formatBytes(finalMem.HeapAlloc))
	fmt.Printf("  Peak Heap:        %s\n", formatBytes(finalMem.HeapSys))
	fmt.Printf("  Total Allocated:  %s\n", formatBytes(finalMem.TotalAlloc))
	fmt.Printf("  GC Cycles:        %d\n", finalMem.NumGC-initialMem.NumGC)
	fmt.Printf("\nThroughput Summary:\n")
	fmt.Printf("  Data Loading:     %s cities/sec\n", formatNumber(int64(loadResult.Throughput)))
	fmt.Printf("  S2 Index:         %s cities/sec\n", formatNumber(int64(s2Result.Throughput)))
	fmt.Printf("  Name Index:       %s cities/sec\n", formatNumber(int64(nameResult.Throughput)))
	if postalResult.Throughput > 0 {
		fmt.Printf("  Postal Index:     %s entries/sec\n", formatNumber(int64(postalResult.Throughput)))
	}
	fmt.Println()
}
