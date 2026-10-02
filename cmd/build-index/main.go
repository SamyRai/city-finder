package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// buildPaths bundles every filesystem location one build run reads from or
// writes to.
type buildPaths struct {
	dataFile        string
	postalCodeFile  string
	outputDir       string
	s2IndexPath     string
	nameIndexPath   string
	postalIndexPath string
	// excludeAdminDivisions mirrors cfg.ExcludeAdminDivisions when a config
	// was loaded (false on the legacy-literal fallback), so an index built
	// here matches what a first boot would build from the same config.
	excludeAdminDivisions bool
	// includeFeatureClasses mirrors cfg.IncludeFeatureClasses (nil on the
	// legacy-literal fallback) for the same parity reason.
	includeFeatureClasses []string
}

// resolvePaths returns the input and output paths for the given mode.
//
// Test mode uses fixed testdata literals (inputs and the *_test.gob outputs).
//
// Prod mode resolves everything from the config located the same way
// cmd/server locates it (CONFIG_PATH env var, "config.json" default — see
// config.LoadFromEnv). The serialized index outputs come from
// (*config.Config).IndexFilePaths, which joins each index file key with the
// config's datasets_folder exactly like the initializer's indexFilePaths on
// the reader side, so a build under any non-default config is the one the
// initializer will actually load. When no config file can be loaded, prod
// falls back to the legacy literal names below with a warning — build-index
// must remain runnable before a config exists. With the shipped default
// config.json the fallback values coincide with the config-driven ones.
func resolvePaths(mode string) buildPaths {
	if mode == "test" {
		return buildPaths{
			dataFile:        "testdata/allCountries.txt",
			postalCodeFile:  "testdata/zipCodes.txt",
			outputDir:       "testdata",
			s2IndexPath:     filepath.Join("testdata", "s2index_test.gob"),
			nameIndexPath:   filepath.Join("testdata", "name_index_test.gob"),
			postalIndexPath: filepath.Join("testdata", "postal_code_index_test.gob"),
		}
	}

	// mode == "prod": legacy literals double as the missing-config fallback.
	paths := buildPaths{
		dataFile:        "datasets/allCountries.txt",
		postalCodeFile:  "datasets/zipCodes.txt",
		outputDir:       "datasets",
		s2IndexPath:     "datasets/s2index.gob",
		nameIndexPath:   "datasets/name_index.gob",
		postalIndexPath: "datasets/postal_code_index.gob",
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Printf("Warning: could not load config (%v); falling back to legacy literal dataset and index file names %s / %s — symlink them if the initializer produced different names", err, paths.dataFile, paths.postalCodeFile)
		return paths
	}
	paths.dataFile = filepath.Join(cfg.DatasetsFolder, cfg.AllCitiesFile)
	paths.postalCodeFile = filepath.Join(cfg.DatasetsFolder, cfg.PostalCodesFile)
	paths.outputDir = cfg.DatasetsFolder
	paths.s2IndexPath, paths.nameIndexPath, paths.postalIndexPath = cfg.IndexFilePaths()
	paths.excludeAdminDivisions = cfg.ExcludeAdminDivisions
	paths.includeFeatureClasses = cfg.IncludeFeatureClasses
	return paths
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

	// Resolve data file paths, output directory, and serialized index output
	// paths based on mode. Prod resolves BOTH the dataset filenames and the
	// index output paths from the same config the initializer uses, located
	// the same way cmd/server locates it (CONFIG_PATH env var with a
	// "config.json" default — config.LoadFromEnv), so build-index writes its
	// outputs exactly where the initializer looks for them. Test mode uses
	// fixed testdata literals.
	paths := resolvePaths(mode)
	dataFile, postalCodeFile, outputDir := paths.dataFile, paths.postalCodeFile, paths.outputDir

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
		// The knob threads the same way as the initializer's loadData so a
		// pre-built index matches what a first boot would build from the
		// same config (it applies only when an index is (re)built).
		cities, err = dataLoader.LoadGeoNamesCSVWithOptions(dataFile, dataLoader.LoadOptions{
			ExcludeAdminDivisions: paths.excludeAdminDivisions,
			IncludeFeatureClasses: paths.includeFeatureClasses,
		})
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
	var s2Finder *coordinates.S2Finder
	s2Result := measureOperation("3. Building S2 Spatial Index", len(cities), func() {
		var err error
		s2Finder, err = coordinates.BuildIndex(cities)
		if err != nil {
			log.Fatalf("Failed to build S2 index: %v", err)
		}
	})
	printResult(s2Result)

	// Step 4: Build Name Index
	var nameFinder *name.Finder
	nameResult := measureOperation("4. Building Name Index", len(cities), func() {
		nameFinder = name.BuildIndex(cities)
		// Same rows as the S2 index: share its city table so the name index
		// file references it instead of embedding a second copy (the server
		// attaches the two the same way at boot).
		if err := nameFinder.ShareCities(s2Finder.Cities); err != nil {
			log.Fatalf("Failed to share the S2 city table with the name index: %v", err)
		}
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
		// Serialize S2 index
		if err := s2Finder.SerializeIndex(paths.s2IndexPath); err != nil {
			log.Fatalf("Failed to serialize S2 index: %v", err)
		}
		fmt.Printf("  ✓ S2 index saved to: %s\n", paths.s2IndexPath)

		// Serialize Name index
		if err := nameFinder.SerializeIndex(paths.nameIndexPath); err != nil {
			log.Fatalf("Failed to serialize Name index: %v", err)
		}
		fmt.Printf("  ✓ Name index saved to: %s\n", paths.nameIndexPath)

		// Serialize Postal Code index
		if err := postalFinder.SerializeIndex(paths.postalIndexPath); err != nil {
			log.Fatalf("Failed to serialize Postal Code index: %v", err)
		}
		fmt.Printf("  ✓ Postal Code index saved to: %s\n", paths.postalIndexPath)
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
