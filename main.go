package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sort"
	"time"
)

const (
	timeoutThreshold = 5 * time.Second
	interval         = 50 * time.Millisecond
	logFilePath      = "dns_results.log"
	maxHistoryWindow = 5 * time.Minute
	topN             = 5
	maxRecords       = 10000 // Maximum records to keep for percentile calculation
)

type queryResult struct {
	Timestamp time.Time
	Duration  time.Duration
}

var (
	successCount    int
	slowCount       int
	failureCount    int
	lastResult      string
	lastDuration    time.Duration
	lastResolvedIPs []string
	resultHistory   []queryResult
	lastDurations   []time.Duration
	startTime       = time.Now()
)

func clearTerminal() {
	cmd := exec.Command("clear")
	cmd.Stdout = os.Stdout
	_ = cmd.Run()
}

func main() {
	hostname := os.Getenv("DNS_HOSTNAME")
	if hostname == "" {
		fmt.Println("❌ Environment variable DNS_HOSTNAME is not set.")
		os.Exit(1)
	}

	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)

	resolver := net.Resolver{}
	fmt.Printf("🔍 Starting DNS probe for hostname: %s\n", hostname)

	for {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeoutThreshold)
		ips, err := resolver.LookupHost(ctx, hostname)
		duration := time.Since(start)
		cancel()

		now := time.Now()

		if err != nil {
			failureCount++
			lastResult = fmt.Sprintf("❌ FAIL (%v)", err)
			lastDuration = duration
			logger.Printf("[%s] FAIL - Error: %v - Time: %v\n", now.Format("2006-01-02 15:04:05.000"), err, duration)
		} else if duration > timeoutThreshold {
			slowCount++
			lastResult = "🐢 SLOW"
			lastDuration = duration
			lastResolvedIPs = ips
			logger.Printf("[%s] SLOW - IPs: %v - Time: %v\n", now.Format("2006-01-02 15:04:05.000"), ips, duration)
		} else {
			successCount++
			lastResult = "✅ SUCCESS"
			lastDuration = duration
			lastResolvedIPs = ips
			logger.Printf("[%s] OK - IPs: %v - Time: %v\n", now.Format("2006-01-02 15:04:05.000"), ips, duration)
		}

		resultHistory = append(resultHistory, queryResult{Timestamp: now, Duration: duration})
		cleanupOldResults()
		updateLastDurations(duration)

		printStats(hostname)
		time.Sleep(interval)
	}
}

func cleanupOldResults() {
	// Ensure that resultHistory does not exceed maxRecords for percentile calculation
	if len(resultHistory) > maxRecords {
		resultHistory = resultHistory[len(resultHistory)-maxRecords:]
	}

	// Clean up results older than maxHistoryWindow
	cutoff := time.Now().Add(-maxHistoryWindow)
	filtered := resultHistory[:0]
	for _, r := range resultHistory {
		if r.Timestamp.After(cutoff) {
			filtered = append(filtered, r)
		}
	}
	resultHistory = filtered
}

func updateLastDurations(duration time.Duration) {
	lastDurations = append(lastDurations, duration)
	if len(lastDurations) > 5 {
		lastDurations = lastDurations[1:]
	}
}

func printStats(hostname string) {
	clearTerminal()

	uptime := time.Since(startTime).Truncate(time.Second)
	fmt.Printf("📡 DNS Monitor\n")
	fmt.Printf("🌐 Resolving Hostname: %s\n", hostname)
	fmt.Printf("⏱️  Uptime: %v\n\n", uptime)

	fmt.Printf("✅ Successes     : %d\n", successCount)
	fmt.Printf("🐢 Slow Responses: %d\n", slowCount)
	fmt.Printf("❌ Failures      : %d\n", failureCount)

	fmt.Println("\n📊 Last Attempt:")
	fmt.Printf("   Result       : %s\n", lastResult)
	fmt.Printf("   Duration     : %v\n", lastDuration)
	if len(lastResolvedIPs) > 0 {
		fmt.Printf("   Resolved IPs : %v\n", lastResolvedIPs)
	}

	fmt.Println("\n🧮 Last 5 Durations:")
	for i, d := range lastDurations {
		fmt.Printf("   %d. %v\n", i+1, d)
	}

	top := getTopSlowest()
	fmt.Println("\n⏱️  Top 5 Slowest in Last 5 Minutes:")
	for i, r := range top {
		fmt.Printf("   %d. %s - %v\n", i+1, r.Timestamp.Format("15:04:05.000"), r.Duration)
	}

	fmt.Println("\n📈 Duration Percentiles (Last 10,000 Records):")
	printPercentiles(resultHistory)

	fmt.Println("\n(Press Ctrl+C to stop)")
}

func getTopSlowest() []queryResult {
	cutoff := time.Now().Add(-maxHistoryWindow)
	slowResults := []queryResult{}
	for _, r := range resultHistory {
		if r.Timestamp.After(cutoff) {
			slowResults = append(slowResults, r)
		}
	}

	// Sort the slow results by duration
	sort.Slice(slowResults, func(i, j int) bool {
		return slowResults[i].Duration > slowResults[j].Duration
	})
	if len(slowResults) > topN {
		return slowResults[:topN]
	}
	return slowResults
}

func printPercentiles(history []queryResult) {
	if len(history) == 0 {
		fmt.Println("   (no data yet)")
		return
	}

	durations := make([]float64, len(history))
	for i, r := range history {
		durations[i] = float64(r.Duration.Milliseconds())
	}
	sort.Float64s(durations)

	percentiles := []int{50, 75, 90, 95, 99, 999} // Added 99.9 percentile (999)
	for _, p := range percentiles {
		// Adjust the printing logic to handle 999 for the 99.9 percentile
		if p == 999 {
			val := percentile(durations, 99) // Use 99 for the 99.9 percentile
			bar := buildBar(val, durations[len(durations)-1])
			fmt.Printf("P99.9 │ %s %dms\n", bar, int(val))
		} else {
			val := percentile(durations, p)
			bar := buildBar(val, durations[len(durations)-1])
			fmt.Printf("P%02d │ %s %dms\n", p, bar, int(val))
		}
	}
}

func percentile(sorted []float64, percent int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := float64(percent) / 100 * float64(len(sorted)-1)
	f := int(k)
	c := f + 1
	if c >= len(sorted) {
		return sorted[f]
	}
	d := k - float64(f)
	return sorted[f]*(1-d) + sorted[c]*d
}

func buildBar(value float64, max float64) string {
	const maxBarWidth = 40
	if max == 0 {
		return ""
	}
	ratio := value / max
	barLen := int(ratio * maxBarWidth)
	return "█" + string(repeatRune('█', barLen))
}

func repeatRune(r rune, count int) []rune {
	bar := make([]rune, count)
	for i := 0; i < count; i++ {
		bar[i] = r
	}
	return bar
}
