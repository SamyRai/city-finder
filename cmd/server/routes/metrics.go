package routes

import (
	"runtime"
	rmetrics "runtime/metrics"
	"sync/atomic"

	"github.com/SamyRai/cityFinder/cmd/server/metrics"
	"github.com/SamyRai/cityFinder/lib/finder"
	"github.com/gofiber/fiber/v2"
)

// heapAllocMetricName is the runtime gauge backing go_heap_alloc_bytes:
// /memory/classes/heap/objects:bytes is maintained continuously by the
// runtime, so reading it needs no stop-the-world.
const heapAllocMetricName = "/memory/classes/heap/objects:bytes"

// metricsHandler serves GET /metrics: it refreshes the scrape-time gauges and
// counters in reg, then renders it.
func metricsHandler(mainFinder *finder.Finder, reg *metrics.Registry) fiber.Handler {
	// fuzzy_budget_trips_total is scraped from the Finder's counter as a
	// delta since the previous scrape; Swap makes concurrent scrapes
	// count each trip exactly once.
	var lastTrips atomic.Uint64
	lastTrips.Store(mainFinder.FuzzyBudgetTrips())
	return func(c *fiber.Ctx) error {
		cur := mainFinder.FuzzyBudgetTrips()
		if delta := cur - lastTrips.Swap(cur); delta > 0 {
			reg.AddCounter("fuzzy_budget_trips_total", int64(delta))
		}
		// 0 = not built, 1 = building, 2 = built, 3 = disabled —
		// name.Finder.FuzzyBuildState documents the mapping.
		reg.SetGauge("fuzzy_build_state", float64(mainFinder.FuzzyBuildState()))
		// Runtime gauges, computed per scrape. The heap figure comes
		// from runtime/metrics rather than runtime.ReadMemStats so a
		// scrape never pays that call's stop-the-world. The sample slice
		// is scrape-local on purpose: Read writes into it, so a shared
		// package-level slice would race across concurrent scrapes.
		reg.SetGauge("go_goroutines", float64(runtime.NumGoroutine()))
		samples := []rmetrics.Sample{{Name: heapAllocMetricName}}
		rmetrics.Read(samples)
		if v := samples[0].Value; v.Kind() == rmetrics.KindUint64 {
			reg.SetGauge("go_heap_alloc_bytes", float64(v.Uint64()))
		} else {
			reg.SetGauge("go_heap_alloc_bytes", v.Float64())
		}
		return c.Type("text", "plain").SendString(reg.Render())
	}
}
