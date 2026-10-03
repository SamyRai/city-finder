package main

import (
	"runtime"
	"time"
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

// gcSettle is how long measureOperation waits after a forced GC before it
// takes the baseline; a variable so tests do not sleep.
var gcSettle = 100 * time.Millisecond

// measureOperation runs operation and reports its duration, heap growth and
// GC cycles. A failing operation is returned as is with the partial result.
func measureOperation(name string, items int, operation func() error) (OperationResult, error) {
	// Force GC before measurement for accurate baseline
	runtime.GC()
	time.Sleep(gcSettle) // Give GC time to complete

	memBefore := getMemoryStats()
	start := time.Now()

	err := operation()

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
	}, err
}
