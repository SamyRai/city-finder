package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/lib/builder"
	"github.com/SamyRai/cityFinder/lib/city"
	"github.com/SamyRai/cityFinder/lib/dataLoader"
)

// parseMode returns the build mode from the command line: "test" (default)
// or "prod". flag.ErrHelp is returned for -h; any other error is a usage
// error and the caller prints the usage.
func parseMode(args []string) (string, error) {
	fs := flag.NewFlagSet("build-index", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	switch fs.NArg() {
	case 0:
		return "test", nil
	case 1:
		if mode := fs.Arg(0); mode == "test" || mode == "prod" {
			return mode, nil
		}
		return "", fmt.Errorf("unknown mode %q", fs.Arg(0))
	default:
		return "", fmt.Errorf("expected at most one mode, got %d arguments", fs.NArg())
	}
}

// run builds the indexes for the mode named in args, reporting progress on
// stdout and usage on stderr. Every failure is returned; only main exits.
func run(args []string, stdout, stderr io.Writer) error {
	mode, err := parseMode(args)
	if errors.Is(err, flag.ErrHelp) {
		printUsage(stderr)
		return nil
	}
	if err != nil {
		printUsage(stderr)
		return err
	}

	// Prod resolves BOTH the dataset filenames and the index output paths
	// from the same config the initializer uses, located the same way
	// cmd/server locates it, so build-index writes its outputs exactly where
	// the initializer looks for them. Test mode uses fixed testdata literals.
	paths := resolvePaths(mode)

	printBanner(stdout, "FULL INDEX BUILD - Time & Memory Tracking")
	printSystemInfo(stdout)
	fmt.Fprintf(stdout, "Mode:         %s\n", mode)
	fmt.Fprintf(stdout, "Data Files:\n")
	fmt.Fprintf(stdout, "  Cities:      %s\n", paths.dataFile)
	fmt.Fprintf(stdout, "  Postal Codes: %s\n", paths.postalCodeFile)
	fmt.Fprintln(stdout)

	initialMem := getMemoryStats()
	fmt.Fprintf(stdout, "Initial Memory State:\n")
	fmt.Fprintf(stdout, "  Heap Alloc:  %s\n", formatBytes(initialMem.HeapAlloc))
	fmt.Fprintf(stdout, "  Heap Sys:    %s\n", formatBytes(initialMem.HeapSys))
	fmt.Fprintf(stdout, "  GC Cycles:   %d\n", initialMem.NumGC)
	fmt.Fprintln(stdout)

	sum := summary{mode: mode, initialMem: initialMem}
	overallStart := time.Now()
	if err := buildAll(paths, stdout, &sum); err != nil {
		return err
	}
	sum.total = time.Since(overallStart)
	sum.finalMem = getMemoryStats()
	printSummary(stdout, sum)
	return nil
}

// buildAll runs the timed steps (load, S2, name, postal, write) through
// lib/builder and records the results in sum.
func buildAll(paths buildPaths, stdout io.Writer, sum *summary) error {
	// The filters thread the same way as the initializer's loadData so a
	// pre-built index matches what a first boot would build from the same
	// config (they apply only when an index is (re)built).
	src := builder.Sources{
		CitiesFile: paths.dataFile,
		PostalFile: paths.postalCodeFile,
		Options: dataLoader.LoadOptions{
			ExcludeAdminDivisions: paths.excludeAdminDivisions,
			IncludeFeatureClasses: paths.includeFeatureClasses,
		},
	}

	// step times op and prints its result; a failing step stops the build.
	step := func(name string, items int, op func() error) (OperationResult, error) {
		res, err := measureOperation(name, items, op)
		if err != nil {
			return res, err
		}
		printResult(stdout, res)
		return res, nil
	}
	var (
		cities      []city.SpatialCity
		postalCodes builder.PostalCodes
		s2Cities    []city.City
		writes      [3]builder.Write
		err         error
	)

	sum.load, err = step("1. Loading Cities Data", 0, func() (err error) {
		cities, err = src.LoadCities()
		if err != nil {
			return fmt.Errorf("failed to load cities: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sum.cities = len(cities)
	sum.load.ItemsProcessed = len(cities)
	sum.load.Throughput = float64(len(cities)) / sum.load.Duration.Seconds()
	// A postal file that fails to load is fatal, as in the initializer; one
	// with zero rows is allowed. The step is reported after the loop below
	// because its item count is only known once the table is loaded.
	postalLoad, err := measureOperation("2. Loading Postal Codes", 0, func() (err error) {
		postalCodes, err = src.LoadPostal()
		if err != nil {
			return fmt.Errorf("failed to load postal codes: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, countryCodes := range postalCodes {
		sum.postalCodes += len(countryCodes)
	}
	postalLoad.ItemsProcessed = sum.postalCodes
	if postalLoad.Duration > 0 {
		postalLoad.Throughput = float64(sum.postalCodes) / postalLoad.Duration.Seconds()
	}
	printResult(stdout, postalLoad)

	sum.s2, err = step("3. Building S2 Spatial Index", len(cities), func() error {
		s2Finder, w, err := builder.BuildS2(paths.s2IndexPath, cities)
		if err != nil {
			return err
		}
		writes[0], s2Cities = w, s2Finder.Cities
		return nil
	})
	if err != nil {
		return err
	}
	sum.name, err = step("4. Building Name Index", len(cities), func() error {
		_, w, err := builder.BuildName(paths.nameIndexPath, cities, s2Cities)
		writes[1] = w
		return err
	})
	if err != nil {
		return err
	}
	sum.postal, err = step("5. Building Postal Code Index", sum.postalCodes, func() error {
		_, w, err := builder.BuildPostal(paths.postalIndexPath, postalCodes)
		writes[2] = w
		return err
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout)
	printBanner(stdout, "SERIALIZING INDEXES TO "+strings.ToUpper(paths.outputDir))
	_, err = step("6. Serializing Indexes", 0, func() error {
		if err := builder.WriteAll(context.Background(), writes[:]...); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "  ✓ S2 index saved to: %s\n", paths.s2IndexPath)
		fmt.Fprintf(stdout, "  ✓ Name index saved to: %s\n", paths.nameIndexPath)
		fmt.Fprintf(stdout, "  ✓ Postal Code index saved to: %s\n", paths.postalIndexPath)
		return nil
	})
	return err
}
