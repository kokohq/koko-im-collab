package main

import (
	"context"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const metricsSampleInterval = 60 * time.Second

// ─────────────────────────────────────────────────────────────────────────────
// CPU helpers — Linux /proc/self/stat; degrade gracefully on other platforms
// ─────────────────────────────────────────────────────────────────────────────

type cpuSnapshot struct {
	ticks uint64
	at    time.Time
}

// readCPUTicks reads cumulative (utime + stime) CPU ticks from /proc/self/stat.
// Returns (ticks, true) on Linux; (0, false) on other platforms or on error.
func readCPUTicks() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// /proc/self/stat format:
	//   pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime ...
	// The process name (comm) may contain spaces; parse from after the last ')'.
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return 0, false
	}
	// After ')': state[0] ppid[1] pgrp[2] session[3] tty[4] tpgid[5] flags[6]
	//             minflt[7] cminflt[8] majflt[9] cmajflt[10] utime[11] stime[12]
	fields := strings.Fields(s[idx+1:])
	if len(fields) < 13 {
		return 0, false
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	return utime + stime, true
}

// cpuPctBetween computes CPU utilisation as a percentage of one core between
// two snapshots. CLK_TCK = 100 on Linux: delta_ticks/100 = CPU seconds.
func cpuPctBetween(prev, curr cpuSnapshot) float64 {
	if prev.at.IsZero() || curr.at.IsZero() || curr.ticks < prev.ticks {
		return 0
	}
	elapsed := curr.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	cpuSecs := float64(curr.ticks-prev.ticks) / 100.0
	return (cpuSecs / elapsed) * 100.0
}

// ─────────────────────────────────────────────────────────────────────────────
// MetricsCollector
// ─────────────────────────────────────────────────────────────────────────────

// MetricsCollector gathers runtime metrics and persists periodic samples to DB.
// All methods are goroutine-safe. globalMetrics is set during main() initialisation.
type MetricsCollector struct {
	// Atomic counters — int64 at top of struct for 32-bit alignment.
	msgsTotal      int64 // all user messages since process start
	msgsKoko       int64 // koko: command messages
	msgsNonKoko    int64 // non-koko messages
	concurrentReqs int64 // currently in-flight processing goroutines

	// Peak values + timestamps (protected by mu).
	mu               sync.Mutex
	peakMemBytes     uint64
	peakMemAt        time.Time
	peakCPUPct       float64
	peakCPUAt        time.Time
	peakConcurrent   int64
	peakConcurrentAt time.Time

	// CPU between-sample state.
	lastCPU cpuSnapshot

	store *AppStore
}

// globalMetrics is the process-wide metrics singleton, initialised in main().
var globalMetrics *MetricsCollector

// NewMetricsCollector creates a MetricsCollector backed by the given store.
func NewMetricsCollector(store *AppStore) *MetricsCollector {
	m := &MetricsCollector{store: store}
	if ticks, ok := readCPUTicks(); ok {
		m.lastCPU = cpuSnapshot{ticks: ticks, at: time.Now()}
	}
	return m
}

// IncrMessage registers one incoming user message.
// isKoko should be true when the message has a koko: prefix.
func (m *MetricsCollector) IncrMessage(isKoko bool) {
	atomic.AddInt64(&m.msgsTotal, 1)
	if isKoko {
		atomic.AddInt64(&m.msgsKoko, 1)
	} else {
		atomic.AddInt64(&m.msgsNonKoko, 1)
	}
}

// IncConcurrent increments the in-flight processing counter and tracks the peak.
func (m *MetricsCollector) IncConcurrent() {
	v := atomic.AddInt64(&m.concurrentReqs, 1)
	m.mu.Lock()
	if v > m.peakConcurrent {
		m.peakConcurrent = v
		m.peakConcurrentAt = time.Now()
	}
	m.mu.Unlock()
}

// DecConcurrent decrements the in-flight processing counter.
func (m *MetricsCollector) DecConcurrent() {
	atomic.AddInt64(&m.concurrentReqs, -1)
}

// RecordRejection asynchronously persists a rejection event to the DB.
// Safe to call with m == nil (no-op).
func (m *MetricsCollector) RecordRejection(reason, endpoint, userName, accountID string) {
	if m == nil || m.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.store.InsertRejectionEvent(ctx, reason, endpoint, userName, accountID); err != nil {
			log.Printf("REJECTION_EVENT_PERSIST_FAILED: %v", err)
		}
	}()
}

// StartSampler launches the background goroutine that writes one metrics row
// to the DB every metricsSampleInterval (60 s).
func (m *MetricsCollector) StartSampler() {
	go func() {
		ticker := time.NewTicker(metricsSampleInterval)
		defer ticker.Stop()
		for range ticker.C {
			m.takeSample()
		}
	}()
}

// takeSample reads current runtime stats and persists them to app_metrics_samples.
func (m *MetricsCollector) takeSample() {
	if m.store == nil {
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	now := time.Now()
	cpuPct := 0.0
	if ticks, ok := readCPUTicks(); ok {
		curr := cpuSnapshot{ticks: ticks, at: now}
		m.mu.Lock()
		cpuPct = cpuPctBetween(m.lastCPU, curr)
		m.lastCPU = curr
		m.mu.Unlock()
	}

	concurrent := atomic.LoadInt64(&m.concurrentReqs)

	// Update per-process peaks.
	m.mu.Lock()
	if mem.HeapAlloc > m.peakMemBytes {
		m.peakMemBytes = mem.HeapAlloc
		m.peakMemAt = now
	}
	if cpuPct > m.peakCPUPct {
		m.peakCPUPct = cpuPct
		m.peakCPUAt = now
	}
	m.mu.Unlock()

	sample := AppMetricsSample{
		SampledAt:      now,
		HeapAllocBytes: int64(mem.HeapAlloc),
		HeapSysBytes:   int64(mem.HeapSys),
		SysBytes:       int64(mem.Sys),
		Goroutines:     int32(runtime.NumGoroutine()),
		CPUPct:         cpuPct,
		ConcurrentReqs: int32(concurrent),
		MsgsTotalCum:   atomic.LoadInt64(&m.msgsTotal),
		MsgsKokoCum:    atomic.LoadInt64(&m.msgsKoko),
		MsgsNonKokoCum: atomic.LoadInt64(&m.msgsNonKoko),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.InsertMetricsSample(ctx, sample); err != nil {
		log.Printf("METRICS_SAMPLE_PERSIST_FAILED: %v", err)
	}
}
