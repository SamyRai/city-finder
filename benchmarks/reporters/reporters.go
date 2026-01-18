package reporters

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"strings"
	"time"

	"github.com/SamyRai/cityFinder/benchmarks/types"
)

// Reporter defines the interface for benchmark result reporters
type Reporter interface {
	Report(result types.BenchmarkResult) error
	ReportComparison(comparison types.ComparisonResult) error
}

// ConsoleReporter outputs results to console
type ConsoleReporter struct {
	verbose bool
}

// NewConsoleReporter creates a new console reporter
func NewConsoleReporter(verbose bool) *ConsoleReporter {
	return &ConsoleReporter{verbose: verbose}
}

// Report outputs benchmark results to console
func (r *ConsoleReporter) Report(result types.BenchmarkResult) error {
	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("BENCHMARK RESULTS: %s\n", result.Config.Name)
	fmt.Printf("%s\n", strings.Repeat("=", 80))
	fmt.Printf("Timestamp: %s\n", result.Timestamp.Format(time.RFC3339))
	fmt.Printf("Duration: %v\n", result.Duration)
	fmt.Printf("Dataset Sizes: %v\n", result.Config.DatasetSizes)
	fmt.Printf("Iterations: %d\n", result.Config.Iterations)

	fmt.Printf("\nMEMORY USAGE:\n")
	fmt.Printf("  Initial: %.2f MB\n", float64(result.MemoryUsage.Initial)/1024/1024)
	fmt.Printf("  Peak: %.2f MB\n", float64(result.MemoryUsage.Peak)/1024/1024)
	fmt.Printf("  Final: %.2f MB\n", float64(result.MemoryUsage.Final)/1024/1024)
	fmt.Printf("  Allocations: %d\n", result.MemoryUsage.Allocations)
	fmt.Printf("  GC Cycles: %d\n", result.MemoryUsage.GCCycles)

	fmt.Printf("\nOPERATIONS:\n")
	fmt.Printf("%-25s %-12s %-12s %-15s %-12s\n", "Operation", "Duration", "Memory(MB)", "Items", "Throughput/sec")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	for _, op := range result.Operations {
		memoryMB := float64(op.MemoryDelta) / 1024 / 1024
		fmt.Printf("%-25s %-12s %-12.2f %-15d %-12.0f\n",
			op.Name, op.Duration.Round(time.Millisecond), memoryMB, op.ItemsProcessed, op.Throughput)
	}

	// Calculate totals
	var totalDuration time.Duration
	var totalMemory uint64
	var totalItems int
	for _, op := range result.Operations {
		totalDuration += op.Duration
		totalMemory += op.MemoryDelta
		totalItems += op.ItemsProcessed
	}

	totalMemoryMB := float64(totalMemory) / 1024 / 1024
	avgThroughput := float64(totalItems) / totalDuration.Seconds()

	fmt.Printf("%s\n", strings.Repeat("-", 80))
	fmt.Printf("%-25s %-12s %-12.2f %-15d %-12.0f\n",
		"TOTAL", totalDuration.Round(time.Millisecond), totalMemoryMB, totalItems, avgThroughput)

	if r.verbose {
		fmt.Printf("\nSYSTEM INFO:\n")
		fmt.Printf("  Go Version: %s\n", result.SystemInfo.GoVersion)
		fmt.Printf("  OS: %s\n", result.SystemInfo.OS)
		fmt.Printf("  Architecture: %s\n", result.SystemInfo.Architecture)
		fmt.Printf("  CPU Cores: %d\n", result.SystemInfo.CPUCores)
		fmt.Printf("  Total Memory: %.2f GB\n", float64(result.SystemInfo.MemoryTotal)/1024/1024/1024)
		fmt.Printf("  Hostname: %s\n", result.SystemInfo.Hostname)

		if len(result.Config.Tags) > 0 {
			fmt.Printf("\nTAGS:\n")
			for k, v := range result.Config.Tags {
				fmt.Printf("  %s: %s\n", k, v)
			}
		}
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	return nil
}

// ReportComparison outputs comparison results to console
func (r *ConsoleReporter) ReportComparison(comparison types.ComparisonResult) error {
	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	fmt.Printf("BENCHMARK COMPARISON\n")
	fmt.Printf("%s\n", strings.Repeat("=", 80))

	fmt.Printf("Baseline: %s (%s)\n", comparison.Baseline.Config.Name, comparison.Baseline.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("Current:  %s (%s)\n", comparison.Current.Config.Name, comparison.Current.Timestamp.Format("2006-01-02 15:04:05"))

	fmt.Printf("\nPERFORMANCE COMPARISON:\n")
	fmt.Printf("%-30s %-15s %-15s %-12s %-12s\n", "Metric", "Baseline", "Current", "Change", "Status")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	for _, comp := range comparison.Comparisons {
		status := "⚪ Same"
		if comp.Improvement {
			if comp.PercentChange > 10 {
				status = "🟢 Much Better"
			} else {
				status = "🟡 Better"
			}
		} else if comp.PercentChange < -10 {
			status = "🔴 Worse"
		} else if comp.PercentChange < 0 {
			status = "🟠 Slightly Worse"
		}

		fmt.Printf("%-30s %-15.2f %-15.2f %-+12.2f %-12s\n",
			comp.Metric, comp.Baseline, comp.Current, comp.PercentChange, status)
	}

	fmt.Printf("\nSUMMARY:\n")
	fmt.Printf("Overall Improvement: %t\n", comparison.Summary.OverallImprovement)
	fmt.Printf("Significant Changes: %d\n", comparison.Summary.SignificantChanges)
	fmt.Printf("Performance Delta: %+6.2f%%\n", comparison.Summary.PerformanceDelta)
	fmt.Printf("Memory Delta: %+6.2f%%\n", comparison.Summary.MemoryDelta)

	fmt.Printf("\n%s\n", strings.Repeat("=", 80))
	return nil
}

// JSONReporter outputs results in JSON format
type JSONReporter struct {
	outputFile string
	pretty     bool
}

// NewJSONReporter creates a new JSON reporter
func NewJSONReporter(outputFile string, pretty bool) *JSONReporter {
	return &JSONReporter{
		outputFile: outputFile,
		pretty:     pretty,
	}
}

// Report outputs benchmark results to JSON file
func (r *JSONReporter) Report(result types.BenchmarkResult) error {
	var data []byte
	var err error

	if r.pretty {
		data, err = json.MarshalIndent(result, "", "  ")
	} else {
		data, err = json.Marshal(result)
	}

	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	filename := r.getFilename("benchmark", result.Config.Name, "json")
	return os.WriteFile(filename, data, 0644)
}

// ReportComparison outputs comparison results to JSON file
func (r *JSONReporter) ReportComparison(comparison types.ComparisonResult) error {
	var data []byte
	var err error

	if r.pretty {
		data, err = json.MarshalIndent(comparison, "", "  ")
	} else {
		data, err = json.Marshal(comparison)
	}

	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	filename := r.getFilename("comparison", "benchmark_comparison", "json")
	return os.WriteFile(filename, data, 0644)
}

func (r *JSONReporter) getFilename(prefix, name, ext string) string {
	if r.outputFile != "" {
		return r.outputFile
	}
	timestamp := time.Now().Format("20060102_150405")
	return fmt.Sprintf("%s_%s_%s.%s", prefix, name, timestamp, ext)
}

// CSVReporter outputs results in CSV format
type CSVReporter struct {
	outputFile string
}

// NewCSVReporter creates a new CSV reporter
func NewCSVReporter(outputFile string) *CSVReporter {
	return &CSVReporter{outputFile: outputFile}
}

// Report outputs benchmark results to CSV file
func (r *CSVReporter) Report(result types.BenchmarkResult) error {
	filename := r.getFilename("benchmark", result.Config.Name, "csv")
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{
		"Timestamp", "BenchmarkName", "Operation", "Duration(ms)", "MemoryDelta(MB)",
		"ItemsProcessed", "Throughput", "Status", "Error",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write operation results
	for _, op := range result.Operations {
		record := []string{
			result.Timestamp.Format(time.RFC3339),
			result.Config.Name,
			op.Name,
			fmt.Sprintf("%.2f", float64(op.Duration.Nanoseconds())/1000000),
			fmt.Sprintf("%.2f", float64(op.MemoryDelta)/1024/1024),
			fmt.Sprintf("%d", op.ItemsProcessed),
			fmt.Sprintf("%.2f", op.Throughput),
			op.Status,
			op.Error,
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}
	}

	return nil
}

// ReportComparison outputs comparison results to CSV file
func (r *CSVReporter) ReportComparison(comparison types.ComparisonResult) error {
	filename := r.getFilename("comparison", "benchmark_comparison", "csv")
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{"Metric", "Baseline", "Current", "Change", "PercentChange", "Improvement"}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write comparison results
	for _, comp := range comparison.Comparisons {
		record := []string{
			comp.Metric,
			fmt.Sprintf("%.2f", comp.Baseline),
			fmt.Sprintf("%.2f", comp.Current),
			fmt.Sprintf("%.2f", comp.Change),
			fmt.Sprintf("%.2f", comp.PercentChange),
			fmt.Sprintf("%t", comp.Improvement),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}
	}

	return nil
}

func (r *CSVReporter) getFilename(prefix, name, ext string) string {
	if r.outputFile != "" {
		return r.outputFile
	}
	timestamp := time.Now().Format("20060102_150405")
	return fmt.Sprintf("%s_%s_%s.%s", prefix, name, timestamp, ext)
}

// HTMLReporter outputs results in HTML format with charts
type HTMLReporter struct {
	outputFile string
	template   *template.Template
}

// NewHTMLReporter creates a new HTML reporter
func NewHTMLReporter(outputFile string) *HTMLReporter {
	tmpl := template.Must(template.New("report").Funcs(template.FuncMap{
		"safeJS": func(s string) template.JS { return template.JS(s) },
	}).Parse(htmlTemplate))
	return &HTMLReporter{
		outputFile: outputFile,
		template:   tmpl,
	}
}

// Report outputs benchmark results to HTML file
func (r *HTMLReporter) Report(result types.BenchmarkResult) error {
	filename := r.getFilename("benchmark", result.Config.Name, "html")
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create HTML file: %w", err)
	}
	defer file.Close()

	// Calculate derived values for template
	peakMemoryMB := float64(result.MemoryUsage.Peak) / 1024 / 1024
	totalMemoryGB := float64(result.SystemInfo.MemoryTotal) / 1024 / 1024 / 1024

	// Create operations with pre-calculated memory values
	type OperationWithMemory struct {
		types.OperationResult
		MemoryMB float64
	}

	operations := make([]OperationWithMemory, len(result.Operations))
	for i, op := range result.Operations {
		operations[i] = OperationWithMemory{
			OperationResult: op,
			MemoryMB:        float64(op.MemoryDelta) / 1024 / 1024,
		}
	}

	// Calculate additional summary stats
	totalAllocations := uint64(0)
	totalGCCycles := uint32(0)
	totalGCTime := time.Duration(0)
	for _, op := range operations {
		totalAllocations += op.Allocations
		totalGCCycles += op.GCCycles
		totalGCTime += op.GCTime
	}

	data := struct {
		Title            string
		Result           types.BenchmarkResult
		Operations       []OperationWithMemory
		ChartData        string
		Now              time.Time
		PeakMemoryMB     float64
		TotalMemoryGB    float64
		TotalAllocations uint64
		TotalGCCycles    uint32
		TotalGCTime      time.Duration
	}{
		Title:            fmt.Sprintf("Benchmark Report: %s", result.Config.Name),
		Result:           result,
		Operations:       operations,
		ChartData:        r.generateChartData(result),
		Now:              time.Now(),
		PeakMemoryMB:     peakMemoryMB,
		TotalMemoryGB:    totalMemoryGB,
		TotalAllocations: totalAllocations,
		TotalGCCycles:    totalGCCycles,
		TotalGCTime:      totalGCTime,
	}

	return r.template.Execute(file, data)
}

// ReportComparison outputs comparison results to HTML file
func (r *HTMLReporter) ReportComparison(comparison types.ComparisonResult) error {
	filename := r.getFilename("comparison", "benchmark_comparison", "html")
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create HTML file: %w", err)
	}
	defer file.Close()

	data := struct {
		Title       string
		Comparison  types.ComparisonResult
		ChartData   string
	}{
		Title:      "Benchmark Comparison Report",
		Comparison: comparison,
		ChartData:  r.generateComparisonChartData(comparison),
	}

	return r.template.Execute(file, data)
}

func (r *HTMLReporter) getFilename(prefix, name, ext string) string {
	if r.outputFile != "" {
		return r.outputFile
	}
	timestamp := time.Now().Format("20060102_150405")
	return fmt.Sprintf("%s_%s_%s.%s", prefix, name, timestamp, ext)
}

func (r *HTMLReporter) generateChartData(result types.BenchmarkResult) string {
	// Generate Chart.js data for operations
	labels := make([]string, len(result.Operations))
	durations := make([]float64, len(result.Operations))
	memory := make([]float64, len(result.Operations))

	for i, op := range result.Operations {
		labels[i] = op.Name
		durations[i] = float64(op.Duration.Nanoseconds()) / 1000000 // ms
		memory[i] = float64(op.MemoryDelta) / 1024 / 1024 // MB
	}

	// Properly format arrays as JSON strings for JavaScript
	labelsJSON, _ := json.Marshal(labels)
	durationsJSON, _ := json.Marshal(durations)
	memoryJSON, _ := json.Marshal(memory)

	return fmt.Sprintf(`{
		labels: %s,
		datasets: [{
			label: 'Duration (ms)',
			data: %s,
			backgroundColor: 'rgba(54, 162, 235, 0.5)',
			borderColor: 'rgba(54, 162, 235, 1)',
			borderWidth: 1
		}, {
			label: 'Memory Delta (MB)',
			data: %s,
			backgroundColor: 'rgba(255, 99, 132, 0.5)',
			borderColor: 'rgba(255, 99, 132, 1)',
			borderWidth: 1
		}]
	}`, string(labelsJSON), string(durationsJSON), string(memoryJSON))
}

func (r *HTMLReporter) generateComparisonChartData(comparison types.ComparisonResult) string {
	// Generate Chart.js data for comparison
	labels := make([]string, len(comparison.Comparisons))
	baseline := make([]float64, len(comparison.Comparisons))
	current := make([]float64, len(comparison.Comparisons))

	for i, comp := range comparison.Comparisons {
		labels[i] = comp.Metric
		baseline[i] = comp.Baseline
		current[i] = comp.Current
	}

	// Properly format arrays as JSON
	labelsJSON, _ := json.Marshal(labels)
	baselineJSON, _ := json.Marshal(baseline)
	currentJSON, _ := json.Marshal(current)

	return fmt.Sprintf(`{
		labels: %s,
		datasets: [{
			label: 'Baseline',
			data: %s,
			backgroundColor: 'rgba(255, 99, 132, 0.5)',
			borderColor: 'rgba(255, 99, 132, 1)',
			borderWidth: 1
		}, {
			label: 'Current',
			data: %s,
			backgroundColor: 'rgba(54, 162, 235, 0.5)',
			borderColor: 'rgba(54, 162, 235, 1)',
			borderWidth: 1
		}]
	}`, string(labelsJSON), string(baselineJSON), string(currentJSON))
}

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>{{.Title}}</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        .container { max-width: 1200px; margin: 0 auto; }
        .header { text-align: center; margin-bottom: 30px; }
        .metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 20px; margin-bottom: 30px; }
        .metric-card { background: #f5f5f5; padding: 20px; border-radius: 8px; text-align: center; }
        .metric-value { font-size: 2em; font-weight: bold; color: #333; }
        .metric-label { font-size: 0.9em; color: #666; }
        .chart-container { background: white; padding: 20px; border-radius: 8px; margin-bottom: 30px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        .table { width: 100%; border-collapse: collapse; margin-bottom: 30px; }
        .table th, .table td { padding: 12px; text-align: left; border-bottom: 1px solid #ddd; }
        .table th { background-color: #f5f5f5; font-weight: bold; }
        .status-good { color: #28a745; }
        .status-bad { color: #dc3545; }
        .status-neutral { color: #6c757d; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>{{.Title}}</h1>
            <p>Generated on {{.Now.Format "2006-01-02 15:04:05"}}</p>
        </div>

        <div class="metrics">
            <div class="metric-card">
                <div class="metric-value">{{len .Result.Operations}}</div>
                <div class="metric-label">Operations</div>
            </div>
            <div class="metric-card">
                <div class="metric-value">{{.Result.Duration}}</div>
                <div class="metric-label">Total Duration</div>
            </div>
            <div class="metric-card">
                <div class="metric-value">{{printf "%.1f" .PeakMemoryMB}} MB</div>
                <div class="metric-label">Peak Memory</div>
            </div>
            <div class="metric-card">
                <div class="metric-value">{{printf "%d" .TotalAllocations}}</div>
                <div class="metric-label">Total Allocations</div>
            </div>
            <div class="metric-card">
                <div class="metric-value">{{.TotalGCCycles}}</div>
                <div class="metric-label">GC Cycles</div>
            </div>
            <div class="metric-card">
                <div class="metric-value">{{.TotalGCTime}}</div>
                <div class="metric-label">GC Time</div>
            </div>
        </div>

        <div class="chart-container">
            <h2>Performance Metrics</h2>
            <canvas id="performanceChart" width="400" height="200"></canvas>
        </div>

        <div class="chart-container">
            <h2>Operation Details</h2>
            <table class="table">
                <thead>
                    <tr>
                        <th>Operation</th>
                        <th>Duration</th>
                        <th>Memory Delta</th>
                        <th>Allocations</th>
                        <th>GC Cycles</th>
                        <th>GC Time</th>
                        <th>Items Processed</th>
                        <th>Throughput</th>
                        <th>Status</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .Operations}}
                    <tr>
                        <td>{{.Name}}</td>
                        <td>{{.Duration}}</td>
                        <td>{{printf "%.2f MB" .MemoryMB}}</td>
                        <td>{{printf "%d" .Allocations}}</td>
                        <td>{{.GCCycles}}</td>
                        <td>{{.GCTime}}</td>
                        <td>{{.ItemsProcessed}}</td>
                        <td>{{printf "%.0f/sec" .Throughput}}</td>
                        <td class="{{if eq .Status "completed"}}status-good{{else}}status-bad{{end}}">{{.Status}}</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
        </div>

        <div class="chart-container">
            <h2>System Information</h2>
            <table class="table">
                <tr><td>Go Version</td><td>{{.Result.SystemInfo.GoVersion}}</td></tr>
                <tr><td>OS</td><td>{{.Result.SystemInfo.OS}}</td></tr>
                <tr><td>Architecture</td><td>{{.Result.SystemInfo.Architecture}}</td></tr>
                <tr><td>CPU Cores</td><td>{{.Result.SystemInfo.CPUCores}}</td></tr>
                <tr><td>Total Memory</td><td>{{printf "%.2f GB" .TotalMemoryGB}}</td></tr>
                <tr><td>Hostname</td><td>{{.Result.SystemInfo.Hostname}}</td></tr>
            </table>
        </div>

        <div class="chart-container">
            <h2>Benchmark Configuration</h2>
            <table class="table">
                <tr><td>Benchmark Name</td><td>{{.Result.Config.Name}}</td></tr>
                <tr><td>Dataset Sizes</td><td>{{.Result.Config.DatasetSizes}}</td></tr>
                <tr><td>Iterations</td><td>{{.Result.Config.Iterations}}</td></tr>
                <tr><td>Warmup Runs</td><td>{{.Result.Config.WarmupRuns}}</td></tr>
                <tr><td>CPU Profiling</td><td>{{if .Result.Config.EnableCPUProf}}Enabled{{else}}Disabled{{end}}</td></tr>
                <tr><td>Memory Profiling</td><td>{{if .Result.Config.EnableMemProf}}Enabled{{else}}Disabled{{end}}</td></tr>
                <tr><td>Trace Profiling</td><td>{{if .Result.Config.EnableTrace}}Enabled{{else}}Disabled{{end}}</td></tr>
                {{if .Result.Config.Tags}}
                <tr><th colspan="2">Tags</th></tr>
                {{range $key, $value := .Result.Config.Tags}}
                <tr><td>{{$key}}</td><td>{{$value}}</td></tr>
                {{end}}
                {{end}}
            </table>
        </div>
    </div>

    <script>
        const ctx = document.getElementById('performanceChart').getContext('2d');
        const chart = new Chart(ctx, {
            type: 'bar',
            data: {{safeJS .ChartData}},
            options: {
                responsive: true,
                scales: {
                    y: {
                        beginAtZero: true
                    }
                }
            }
        });
    </script>
</body>
</html>`

// MultiReporter combines multiple reporters
type MultiReporter struct {
	reporters []Reporter
}

// NewMultiReporter creates a multi-reporter
func NewMultiReporter(reporters ...Reporter) *MultiReporter {
	return &MultiReporter{reporters: reporters}
}

// Report sends results to all reporters
func (r *MultiReporter) Report(result types.BenchmarkResult) error {
	for _, reporter := range r.reporters {
		if err := reporter.Report(result); err != nil {
			return err
		}
	}
	return nil
}

// ReportComparison sends comparison results to all reporters
func (r *MultiReporter) ReportComparison(comparison types.ComparisonResult) error {
	for _, reporter := range r.reporters {
		if err := reporter.ReportComparison(comparison); err != nil {
			return err
		}
	}
	return nil
}