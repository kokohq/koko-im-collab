package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"math"
	"net/smtp"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants & configuration
// ─────────────────────────────────────────────────────────────────────────────

const (
	// defaultReportRecipient is overridden by REPORT_EMAIL_TO env var.
	defaultReportRecipient = "harishankar@kokohq.com"

	// cpuHighThreshold is the CPU % above which a period is considered high-load.
	cpuHighThreshold = 60.0
	// memHighMB is the heap allocation in MB above which a warning is raised.
	memHighMB = 400.0
	// downtimeGapThreshold is the gap between samples that signals an outage.
	downtimeGapThreshold = 3 * time.Minute
)

// ─────────────────────────────────────────────────────────────────────────────
// Report domain types
// ─────────────────────────────────────────────────────────────────────────────

type downtimeGap struct {
	Start    time.Time
	End      time.Time
	Duration time.Duration
}

type reportData struct {
	PeriodStart time.Time
	PeriodEnd   time.Time
	GeneratedAt time.Time

	// Message volumes
	TotalMessages   int64
	KokoMessages    int64
	NonKokoMessages int64

	// System resource peaks & averages
	PeakCPUPct       float64
	PeakCPUAt        time.Time
	AvgCPUPct        float64
	PeakMemMB        float64
	PeakMemAt        time.Time
	AvgMemMB         float64
	MaxGoroutines    int32
	PeakConcurrent   int32
	PeakConcurrentAt time.Time
	AvgConcurrent    float64

	// Hourly message breakdown
	HourlyMsgs []HourlyMsgCount

	// Command invocation counts
	CommandUsage []CommandUsageCount

	// Commands that ran during high-CPU intervals
	HighCPUCommands []CommandUsageCount

	// Rejected requests
	TotalRejections    int
	RejectionsByReason map[string]int
	RecentRejections   []AppRejectionEvent

	// Availability
	DowntimeGaps      []downtimeGap
	TotalDowntimeMins float64
	UptimePct         float64
	ServiceStartups   int

	// Meta
	SampleCount int
}

// ─────────────────────────────────────────────────────────────────────────────
// Scheduler
// ─────────────────────────────────────────────────────────────────────────────

func reportHourUTC() int {
	if h, err := strconv.Atoi(os.Getenv("REPORT_HOUR_UTC")); err == nil && h >= 0 && h < 24 {
		return h
	}
	return 7 // default 07:00 UTC
}

func reportEmailRecipient() string {
	if r := strings.TrimSpace(os.Getenv("REPORT_EMAIL_TO")); r != "" {
		return r
	}
	return defaultReportRecipient
}

// StartDailyReporter launches a background goroutine that fires once each day
// at REPORT_HOUR_UTC (default 07:00 UTC) and emails a 24-hour usage report.
func StartDailyReporter(store *AppStore) {
	go func() {
		for {
			now := time.Now().UTC()
			target := time.Date(now.Year(), now.Month(), now.Day(), reportHourUTC(), 0, 0, 0, time.UTC)
			if !now.Before(target) {
				target = target.Add(24 * time.Hour)
			}
			timer := time.NewTimer(target.Sub(now))
			<-timer.C
			timer.Stop()

			periodEnd := time.Now().UTC()
			periodStart := periodEnd.Add(-24 * time.Hour)
			if err := SendDailyReport(store, periodStart, periodEnd); err != nil {
				log.Printf("DAILY_REPORT_ERROR: %v", err)
			}
		}
	}()
}

// ─────────────────────────────────────────────────────────────────────────────
// Data gathering
// ─────────────────────────────────────────────────────────────────────────────

// SendDailyReport builds and sends the HTML usage report for [start, end).
// Exported so it can be triggered manually for testing.
func SendDailyReport(store *AppStore, start, end time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	data, err := gatherReportData(ctx, store, start, end)
	if err != nil {
		return fmt.Errorf("gathering report data: %w", err)
	}

	html := buildReportHTML(data)
	subject := fmt.Sprintf("koko app: usage report — %s", data.GeneratedAt.UTC().Format("Jan 02, 2006"))
	to := reportEmailRecipient()

	if err := sendEmail(to, subject, html); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	log.Printf("DAILY_REPORT_SENT: to=%s period=%s – %s samples=%d",
		to, start.Format(time.RFC3339), end.Format(time.RFC3339), data.SampleCount)
	return nil
}

// gatherReportData queries all metrics sources for the report period.
func gatherReportData(ctx context.Context, store *AppStore, start, end time.Time) (reportData, error) {
	d := reportData{
		PeriodStart:        start,
		PeriodEnd:          end,
		GeneratedAt:        time.Now().UTC(),
		RejectionsByReason: make(map[string]int),
	}

	if store == nil || store.db == nil {
		return d, nil // in-memory mode: return skeletal data
	}

	// Total message counts from messenger_inputs (authoritative per-row record).
	_ = store.db.QueryRowContext(ctx, `
		SELECT
		    COUNT(*),
		    COUNT(*) FILTER (WHERE message_type = 'text' AND text_content ILIKE 'koko:%'),
		    COUNT(*) FILTER (WHERE NOT (message_type = 'text' AND text_content ILIKE 'koko:%'))
		FROM messenger_inputs
		WHERE received_at >= $1 AND received_at < $2`,
		start, end,
	).Scan(&d.TotalMessages, &d.KokoMessages, &d.NonKokoMessages)

	// Runtime samples.
	samples, _ := store.QueryMetricsSamples(ctx, start, end)
	d.SampleCount = len(samples)

	if len(samples) > 0 {
		var sumCPU, sumMem, sumConcurrent float64
		var maxGoroutines int32
		for _, s := range samples {
			if s.CPUPct > d.PeakCPUPct {
				d.PeakCPUPct = s.CPUPct
				d.PeakCPUAt = s.SampledAt
			}
			memMB := float64(s.HeapAllocBytes) / (1024 * 1024)
			if memMB > d.PeakMemMB {
				d.PeakMemMB = memMB
				d.PeakMemAt = s.SampledAt
			}
			if s.ConcurrentReqs > d.PeakConcurrent {
				d.PeakConcurrent = s.ConcurrentReqs
				d.PeakConcurrentAt = s.SampledAt
			}
			if s.Goroutines > maxGoroutines {
				maxGoroutines = s.Goroutines
			}
			sumCPU += s.CPUPct
			sumMem += memMB
			sumConcurrent += float64(s.ConcurrentReqs)
		}
		n := float64(len(samples))
		d.AvgCPUPct = sumCPU / n
		d.AvgMemMB = sumMem / n
		d.AvgConcurrent = sumConcurrent / n
		d.MaxGoroutines = maxGoroutines

		// Downtime gaps.
		d.DowntimeGaps = detectDowntimeGaps(samples, downtimeGapThreshold)
		for _, g := range d.DowntimeGaps {
			d.TotalDowntimeMins += g.Duration.Minutes()
		}
		totalMins := end.Sub(start).Minutes()
		if totalMins > 0 {
			d.UptimePct = math.Max(0, (totalMins-d.TotalDowntimeMins)/totalMins*100)
		}
	}

	d.HourlyMsgs, _ = store.QueryHourlyMessageCounts(ctx, start, end)
	d.CommandUsage, _ = store.QueryCommandUsageSummary(ctx, start, end)
	d.HighCPUCommands, _ = store.QueryResourceIntensiveFlows(ctx, start, end)

	rejections, _ := store.QueryRejectionEvents(ctx, start, end)
	d.TotalRejections = len(rejections)
	d.RecentRejections = rejections
	for _, r := range rejections {
		d.RejectionsByReason[r.Reason]++
	}

	events, _ := store.QueryLifecycleEvents(ctx, start, end)
	for _, e := range events {
		if e.EventType == "startup" {
			d.ServiceStartups++
		}
	}

	return d, nil
}

// detectDowntimeGaps identifies periods with no metrics samples (process was
// not running or DB writes failed).
func detectDowntimeGaps(samples []AppMetricsSample, threshold time.Duration) []downtimeGap {
	var gaps []downtimeGap
	for i := 1; i < len(samples); i++ {
		gap := samples[i].SampledAt.Sub(samples[i-1].SampledAt)
		if gap > threshold {
			gaps = append(gaps, downtimeGap{
				Start:    samples[i-1].SampledAt,
				End:      samples[i].SampledAt,
				Duration: gap,
			})
		}
	}
	return gaps
}

// ─────────────────────────────────────────────────────────────────────────────
// Intelligent analysis
// ─────────────────────────────────────────────────────────────────────────────

func buildAlerts(d reportData) []string {
	var a []string
	if d.PeakCPUPct > 80 {
		a = append(a, fmt.Sprintf(
			"High CPU peak: %.1f%% recorded at %s. Sustained spikes above 80%% indicate compute saturation.",
			d.PeakCPUPct, fmtTS(d.PeakCPUAt)))
	}
	if d.AvgCPUPct > cpuHighThreshold {
		a = append(a, fmt.Sprintf(
			"Elevated average CPU: %.1f%% over the 24-hour period — sustained high load detected.",
			d.AvgCPUPct))
	}
	if d.PeakMemMB > memHighMB {
		a = append(a, fmt.Sprintf(
			"High heap memory: %.0f MB at %s. Review goroutine leaks or large payload buffering.",
			d.PeakMemMB, fmtTS(d.PeakMemAt)))
	}
	if d.TotalRejections > 0 {
		a = append(a, fmt.Sprintf(
			"%d user request(s) were rejected. Users experienced service denials during the report period.",
			d.TotalRejections))
	}
	if d.TotalDowntimeMins > 30 {
		a = append(a, fmt.Sprintf(
			"Service was unavailable for %.0f minutes (%.1f%% downtime). Consider enabling Cloud Run min-instances.",
			d.TotalDowntimeMins, 100-d.UptimePct))
	}
	if d.ServiceStartups > 1 {
		a = append(a, fmt.Sprintf(
			"%d service restarts detected — check application logs for crash signals or forced redeployments.",
			d.ServiceStartups))
	}
	return a
}

func buildRecommendations(d reportData) []string {
	var r []string
	if d.AvgCPUPct > 60 {
		r = append(r, "Scale CPU allocation: add --cpu=2 (or higher) to your gcloud run deploy command.")
	}
	if d.AvgMemMB > 350 {
		r = append(r, "Increase Cloud Run memory: add --memory=1Gi or profile large response buffers.")
	}
	if n, ok := d.RejectionsByReason["rate_limit"]; ok && n > 5 {
		r = append(r, fmt.Sprintf(
			"%d rate-limit rejections: review the 5 req/min cap and consider tiered limits per API key.", n))
	}
	if n, ok := d.RejectionsByReason["upload_capacity"]; ok && n > 0 {
		r = append(r, fmt.Sprintf(
			"%d upload-capacity rejections: consider raising maxConcurrentUploads beyond 50.", n))
	}
	if len(d.HighCPUCommands) > 0 {
		r = append(r, fmt.Sprintf(
			"Command '%s' correlates with CPU peaks. Profile its Vertex AI call latency and streaming response handling.",
			d.HighCPUCommands[0].CommandName))
	}
	if d.TotalDowntimeMins > 5 {
		r = append(r, "Set --min-instances=1 on Cloud Run to eliminate cold-start induced downtime.")
	}
	if len(r) == 0 {
		r = append(r, "System performance is within normal parameters. No immediate action required.")
	}
	return r
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML report builder
// ─────────────────────────────────────────────────────────────────────────────

// Formatting helpers.
func fmtTS(t time.Time) string {
	if t.IsZero() {
		return "&mdash;"
	}
	return t.UTC().Format("Jan 02, 2006 15:04 UTC")
}
func fmtMB(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.0f MB", mb)
}
func fmtPct(v float64) string { return fmt.Sprintf("%.1f%%", v) }
func fmtDur(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
func escHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// colorForCPU returns a hex color code based on CPU %.
func colorForCPU(pct float64) string {
	if pct > 80 {
		return "#dc2626"
	}
	if pct > 50 {
		return "#d97706"
	}
	return "#16a34a"
}

// colorForMem returns a hex color code based on heap MB.
func colorForMem(mb float64) string {
	if mb > 600 {
		return "#dc2626"
	}
	if mb > memHighMB {
		return "#d97706"
	}
	return "#16a34a"
}

// barWidth converts a count to a bar width % capped at 100.
func barWidth(count, maxCount int64) int {
	if maxCount == 0 {
		return 0
	}
	w := int(count * 100 / maxCount)
	if w > 100 {
		return 100
	}
	return w
}

// topN returns the top N items from a slice sorted desc by Count.
func topNHours(hourly []HourlyMsgCount, n int) []HourlyMsgCount {
	sorted := append([]HourlyMsgCount(nil), hourly...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// kpiBox renders one KPI summary tile.
func kpiBox(label, value, color, note string) string {
	return fmt.Sprintf(`
	<td width="25%%" style="padding:0 8px 0 0">
	  <div style="background:#f8fafc;border:1px solid #e2e8f0;border-left:4px solid %s;
	              border-radius:4px;padding:18px 16px;text-align:center">
	    <div style="font-size:26px;font-weight:700;color:%s;line-height:1">%s</div>
	    <div style="font-size:11px;color:#64748b;margin-top:6px;text-transform:uppercase;
	                letter-spacing:0.6px;font-weight:600">%s</div>
	    %s
	  </div>
	</td>`,
		color, color, value, label,
		func() string {
			if note != "" {
				return fmt.Sprintf(`<div style="font-size:10px;color:#94a3b8;margin-top:4px">%s</div>`, note)
			}
			return ""
		}(),
	)
}

// sectionHeader renders a section divider.
func sectionHeader(title string) string {
	return fmt.Sprintf(`
	<tr><td>
	  <div style="margin:28px 0 12px;border-bottom:2px solid #e2e8f0;padding-bottom:8px">
	    <span style="font-size:14px;font-weight:700;color:#1e293b;letter-spacing:0.3px;
	                 text-transform:uppercase">%s</span>
	  </div>
	</td></tr>`, title)
}

// alertRow renders one alert or recommendation row.
func alertRow(text, icon, bg, border string) string {
	return fmt.Sprintf(`
	<div style="background:%s;border-left:4px solid %s;border-radius:4px;
	            padding:10px 14px;margin-bottom:8px;font-size:12px;color:#1e293b">
	  <span style="margin-right:6px">%s</span>%s
	</div>`, bg, border, icon, text)
}

// buildReportHTML generates the full HTML email body for the usage report.
func buildReportHTML(d reportData) string {
	var b strings.Builder

	alerts := buildAlerts(d)
	recommendations := buildRecommendations(d)

	// ── compute bar chart max ──────────────────────────────────────────────
	var maxHourlyCount int64
	for _, h := range d.HourlyMsgs {
		if h.Count > maxHourlyCount {
			maxHourlyCount = h.Count
		}
	}

	top5Hours := topNHours(d.HourlyMsgs, 5)

	// ── outer wrapper ──────────────────────────────────────────────────────
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>koko App — Daily Usage Report</title>
</head>
<body style="margin:0;padding:0;background:#f1f5f9;font-family:Arial,Helvetica,sans-serif;
             font-size:13px;color:#334155">
<table width="100%" cellpadding="0" cellspacing="0"
       style="background:#f1f5f9;padding:24px 0">
<tr><td align="center">
<table width="680" cellpadding="0" cellspacing="0"
       style="background:#ffffff;border-radius:6px;overflow:hidden;
              box-shadow:0 2px 8px rgba(0,0,0,0.08)">`)

	// ── Header ────────────────────────────────────────────────────────────
	fmt.Fprintf(&b, `
<tr><td style="background:#0f172a;padding:28px 32px">
  <div style="font-size:22px;font-weight:700;color:#f8fafc;letter-spacing:0.3px">
    koko App &mdash; Daily Usage Report
  </div>
  <div style="font-size:12px;color:#94a3b8;margin-top:8px">
    Report Period: %s &ndash; %s
    &nbsp;&nbsp;|&nbsp;&nbsp;
    Generated: %s
  </div>
</td></tr>`,
		d.PeriodStart.UTC().Format("Jan 02, 2006 15:04 UTC"),
		d.PeriodEnd.UTC().Format("Jan 02, 2006 15:04 UTC"),
		d.GeneratedAt.UTC().Format("Jan 02, 2006 15:04:05 UTC"),
	)

	// ── Alert banner (if any) ─────────────────────────────────────────────
	if len(alerts) > 0 {
		b.WriteString(`<tr><td style="background:#fef3c7;padding:12px 32px;
		  border-bottom:1px solid #fde68a">`)
		for _, a := range alerts {
			b.WriteString(alertRow(escHTML(a), "&#9888;", "#fef3c7", "#f59e0b"))
		}
		b.WriteString(`</td></tr>`)
	}

	// ── Main content ──────────────────────────────────────────────────────
	b.WriteString(`<tr><td style="padding:24px 32px">
<table width="100%" cellpadding="0" cellspacing="0">`)

	// ── KPI summary boxes ─────────────────────────────────────────────────
	b.WriteString(`<tr>`)
	b.WriteString(kpiBox("Total Messages",
		strconv.FormatInt(d.TotalMessages, 10),
		"#3b82f6",
		fmt.Sprintf("koko: %d &nbsp;/&nbsp; other: %d", d.KokoMessages, d.NonKokoMessages)))
	b.WriteString(kpiBox("Peak CPU",
		fmtPct(d.PeakCPUPct),
		colorForCPU(d.PeakCPUPct),
		fmtTS(d.PeakCPUAt)))
	b.WriteString(kpiBox("Peak Memory",
		fmtMB(d.PeakMemMB),
		colorForMem(d.PeakMemMB),
		fmtTS(d.PeakMemAt)))
	b.WriteString(kpiBox("Max Concurrent",
		strconv.Itoa(int(d.PeakConcurrent)),
		"#7c3aed",
		fmtTS(d.PeakConcurrentAt)))
	b.WriteString(`</tr>`)

	// ── Section: Message Volume ───────────────────────────────────────────
	b.WriteString(sectionHeader("&#128172; Message Volume"))
	b.WriteString(`<tr><td>
	<table width="100%" cellpadding="6" cellspacing="0"
	       style="border-collapse:collapse;font-size:12px">
	  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
	    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Metric</th>
	    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Count</th>
	    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Share</th>
	  </tr>`)
	totalF := float64(d.TotalMessages)
	if totalF == 0 {
		totalF = 1
	}
	msgRows := []struct {
		label string
		count int64
		color string
	}{
		{"All messages", d.TotalMessages, "#3b82f6"},
		{"koko: commands / NLQ", d.KokoMessages, "#7c3aed"},
		{"Non-koko (broadcast / chat)", d.NonKokoMessages, "#0ea5e9"},
	}
	for i, row := range msgRows {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#f8fafc"
		}
		pct := float64(row.count) / totalF * 100
		fmt.Fprintf(&b, `
	  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
	    <td style="padding:8px 10px">
	      <span style="display:inline-block;width:10px;height:10px;border-radius:50%%;
	                   background:%s;margin-right:6px"></span>%s
	    </td>
	    <td align="right" style="padding:8px 10px;font-weight:600">%s</td>
	    <td align="right" style="padding:8px 10px;color:#64748b">%.1f%%</td>
	  </tr>`, bg, row.color, row.label, strconv.FormatInt(row.count, 10), pct)
	}
	b.WriteString(`</table></td></tr>`)

	// ── Section: System Performance ───────────────────────────────────────
	b.WriteString(sectionHeader("&#128202; System Performance"))
	b.WriteString(`<tr><td>
	<table width="100%" cellpadding="6" cellspacing="0"
	       style="border-collapse:collapse;font-size:12px">
	  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
	    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Metric</th>
	    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Peak</th>
	    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Peak Time</th>
	    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Average</th>
	  </tr>`)
	perfRows := []struct {
		label   string
		peak    string
		peakAt  string
		avg     string
		pkColor string
	}{
		{
			"CPU Utilisation",
			fmtPct(d.PeakCPUPct), fmtTS(d.PeakCPUAt),
			fmtPct(d.AvgCPUPct),
			colorForCPU(d.PeakCPUPct),
		},
		{
			"Heap Memory",
			fmtMB(d.PeakMemMB), fmtTS(d.PeakMemAt),
			fmtMB(d.AvgMemMB),
			colorForMem(d.PeakMemMB),
		},
		{
			"Concurrent Requests",
			strconv.Itoa(int(d.PeakConcurrent)), fmtTS(d.PeakConcurrentAt),
			fmt.Sprintf("%.1f", d.AvgConcurrent),
			"#7c3aed",
		},
		{
			"Goroutines",
			strconv.Itoa(int(d.MaxGoroutines)), "&mdash;",
			"&mdash;",
			"#0ea5e9",
		},
	}
	for i, row := range perfRows {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#f8fafc"
		}
		fmt.Fprintf(&b, `
	  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
	    <td style="padding:8px 10px">%s</td>
	    <td align="right" style="padding:8px 10px;font-weight:700;color:%s">%s</td>
	    <td style="padding:8px 10px;color:#64748b;font-size:11px">%s</td>
	    <td align="right" style="padding:8px 10px;color:#64748b">%s</td>
	  </tr>`, bg, row.label, row.pkColor, row.peak, row.peakAt, row.avg)
	}
	fmt.Fprintf(&b, `
	  <tr style="background:#f0f9ff;border-bottom:1px solid #bae6fd">
	    <td colspan="4" style="padding:6px 10px;font-size:11px;color:#0369a1">
	      Monitoring data: %d samples over 24 hours (1 sample / 60 s)
	    </td>
	  </tr>`, d.SampleCount)
	b.WriteString(`</table></td></tr>`)

	// ── Section: Peak Usage Times ──────────────────────────────────────────
	b.WriteString(sectionHeader("&#128337; Peak Usage Times (Top 5 Busiest Hours)"))
	if len(top5Hours) == 0 {
		b.WriteString(`<tr><td style="padding:8px 0;color:#94a3b8;font-size:12px">
		  No message data available for this period.</td></tr>`)
	} else {
		b.WriteString(`<tr><td>
		<table width="100%" cellpadding="4" cellspacing="0"
		       style="border-collapse:collapse;font-size:12px">
		  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
		    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Hour (UTC)</th>
		    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Messages</th>
		    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Volume</th>
		  </tr>`)
		var topMax int64
		for _, h := range top5Hours {
			if h.Count > topMax {
				topMax = h.Count
			}
		}
		for i, h := range top5Hours {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#f8fafc"
			}
			bw := barWidth(h.Count, topMax)
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:8px 10px;font-weight:600">%s</td>
		    <td align="right" style="padding:8px 10px;font-weight:700;color:#3b82f6">%d</td>
		    <td style="padding:8px 10px">
		      <div style="background:#e2e8f0;border-radius:3px;height:14px;width:180px">
		        <div style="background:#3b82f6;border-radius:3px;height:14px;width:%d%%"></div>
		      </div>
		    </td>
		  </tr>`, bg, h.Hour.UTC().Format("Jan 02 &middot; 15:04"), h.Count, bw)
		}
		b.WriteString(`</table></td></tr>`)
	}

	// ── Section: Hourly Breakdown ──────────────────────────────────────────
	if len(d.HourlyMsgs) > 0 {
		b.WriteString(sectionHeader("&#128200; Hourly Message Breakdown"))
		b.WriteString(`<tr><td>
		<table width="100%" cellpadding="3" cellspacing="0"
		       style="border-collapse:collapse;font-size:11px">
		  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Hour (UTC)</th>
		    <th align="right" style="padding:6px 8px;color:#64748b;font-weight:600">Messages</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Volume</th>
		  </tr>`)
		for i, h := range d.HourlyMsgs {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#f8fafc"
			}
			bw := barWidth(h.Count, maxHourlyCount)
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:6px 8px">%s</td>
		    <td align="right" style="padding:6px 8px;font-weight:600;color:#475569">%d</td>
		    <td style="padding:6px 8px">
		      <div style="background:#e2e8f0;border-radius:2px;height:10px;width:200px">
		        <div style="background:#0ea5e9;border-radius:2px;height:10px;width:%d%%"></div>
		      </div>
		    </td>
		  </tr>`, bg, h.Hour.UTC().Format("Jan 02 &middot; 15:04"), h.Count, bw)
		}
		b.WriteString(`</table></td></tr>`)
	}

	// ── Section: Command Activity ──────────────────────────────────────────
	b.WriteString(sectionHeader("&#128640; Command Activity"))
	if len(d.CommandUsage) == 0 {
		b.WriteString(`<tr><td style="padding:8px 0;color:#94a3b8;font-size:12px">
		  No koko commands were executed during this period.</td></tr>`)
	} else {
		b.WriteString(`<tr><td>
		<table width="100%" cellpadding="6" cellspacing="0"
		       style="border-collapse:collapse;font-size:12px">
		  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
		    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Command</th>
		    <th align="right" style="padding:8px 10px;color:#64748b;font-weight:600">Invocations</th>
		  </tr>`)
		var maxCmd int64
		for _, c := range d.CommandUsage {
			if c.Count > maxCmd {
				maxCmd = c.Count
			}
		}
		for i, c := range d.CommandUsage {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#f8fafc"
			}
			bw := barWidth(c.Count, maxCmd)
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:8px 10px">
		      <div style="background:#e2e8f0;border-radius:2px;height:14px;width:%d%%;
		                   margin-bottom:2px"></div>
		      <span style="font-weight:600">koko:%s</span>
		    </td>
		    <td align="right" style="padding:8px 10px;font-weight:700;color:#7c3aed">%d</td>
		  </tr>`, bg, bw, escHTML(c.CommandName), c.Count)
		}
		b.WriteString(`</table></td></tr>`)
	}

	// ── Section: Resource-Intensive Flows ─────────────────────────────────
	b.WriteString(sectionHeader("&#9889; Flows Correlating with High CPU (&gt;40%)"))
	if len(d.HighCPUCommands) == 0 {
		b.WriteString(`<tr><td style="padding:8px 0;color:#94a3b8;font-size:12px">
		  No commands were detected during high-CPU intervals (threshold: 40%).</td></tr>`)
	} else {
		b.WriteString(`<tr><td>
		<table width="100%" cellpadding="6" cellspacing="0"
		       style="border-collapse:collapse;font-size:12px">
		  <tr style="background:#fff7ed;border-bottom:2px solid #fed7aa">
		    <th align="left" style="padding:8px 10px;color:#92400e;font-weight:600">Command</th>
		    <th align="right" style="padding:8px 10px;color:#92400e;font-weight:600">
		      High-CPU Co-occurrences</th>
		  </tr>`)
		for i, c := range d.HighCPUCommands {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#fff7ed"
			}
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:8px 10px;font-weight:600">koko:%s</td>
		    <td align="right" style="padding:8px 10px;font-weight:700;color:#d97706">%d</td>
		  </tr>`, bg, escHTML(c.CommandName), c.Count)
		}
		b.WriteString(`<tr style="background:#fff7ed"><td colspan="2" style="padding:6px 10px;
		  font-size:11px;color:#92400e">Commands executed within &plusmn;2 minutes of a CPU spike
		  (&gt;40%). These flows are the primary compute consumers.</td></tr>`)
		b.WriteString(`</table></td></tr>`)
	}

	// ── Section: Rejected Requests ────────────────────────────────────────
	b.WriteString(sectionHeader("&#128683; Rejected Requests"))
	if d.TotalRejections == 0 {
		b.WriteString(`<tr><td>
		  <div style="background:#f0fdf4;border:1px solid #bbf7d0;border-radius:4px;
		              padding:12px 16px;font-size:12px;color:#15803d">
		    &#10003; No requests were rejected during this period. All users were served successfully.
		  </div></td></tr>`)
	} else {
		// Summary by reason.
		b.WriteString(`<tr><td>
		<table width="100%" cellpadding="6" cellspacing="0"
		       style="border-collapse:collapse;font-size:12px;margin-bottom:12px">
		  <tr style="background:#fef2f2;border-bottom:2px solid #fecaca">
		    <th align="left" style="padding:8px 10px;color:#991b1b;font-weight:600">Rejection Reason</th>
		    <th align="right" style="padding:8px 10px;color:#991b1b;font-weight:600">Count</th>
		  </tr>`)
		// Sort reasons by count descending.
		type reasonCount struct {
			Reason string
			Count  int
		}
		var rc []reasonCount
		for k, v := range d.RejectionsByReason {
			rc = append(rc, reasonCount{k, v})
		}
		sort.Slice(rc, func(i, j int) bool { return rc[i].Count > rc[j].Count })
		for i, r := range rc {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#fef2f2"
			}
			label := map[string]string{
				"rate_limit":      "API rate limit exceeded (5 req / min)",
				"upload_capacity": "Document upload semaphore full (cap: 50)",
				"auth_invalid":    "Invalid or expired API key",
			}[r.Reason]
			if label == "" {
				label = escHTML(r.Reason)
			}
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:8px 10px">%s</td>
		    <td align="right" style="padding:8px 10px;font-weight:700;color:#dc2626">%d</td>
		  </tr>`, bg, label, r.Count)
		}
		b.WriteString(`</table>`)

		// Recent rejection detail list (up to 20).
		shown := d.RecentRejections
		if len(shown) > 20 {
			shown = shown[:20]
		}
		b.WriteString(`<table width="100%" cellpadding="4" cellspacing="0"
		  style="border-collapse:collapse;font-size:11px">
		  <tr style="background:#f8fafc;border-bottom:1px solid #e2e8f0">
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Time (UTC)</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Reason</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Endpoint</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">User</th>
		  </tr>`)
		for i, r := range shown {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#f8fafc"
			}
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:6px 8px;color:#64748b">%s</td>
		    <td style="padding:6px 8px;color:#dc2626;font-weight:600">%s</td>
		    <td style="padding:6px 8px">%s</td>
		    <td style="padding:6px 8px">%s</td>
		  </tr>`,
				bg,
				r.RejectedAt.UTC().Format("Jan 02 15:04:05"),
				escHTML(r.Reason),
				escHTML(r.Endpoint),
				escHTML(r.UserName),
			)
		}
		if len(d.RecentRejections) > 20 {
			fmt.Fprintf(&b,
				`<tr><td colspan="4" style="padding:6px 8px;color:#94a3b8;font-size:11px">
				  ... and %d more rejections not shown.</td></tr>`,
				len(d.RecentRejections)-20)
		}
		b.WriteString(`</table></td></tr>`)
	}

	// ── Section: Service Availability ─────────────────────────────────────
	b.WriteString(sectionHeader("&#128268; Service Availability"))
	b.WriteString(`<tr><td>
	<table width="100%" cellpadding="6" cellspacing="0"
	       style="border-collapse:collapse;font-size:12px">
	  <tr style="background:#f8fafc;border-bottom:2px solid #e2e8f0">
	    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Metric</th>
	    <th align="left" style="padding:8px 10px;color:#64748b;font-weight:600">Value</th>
	  </tr>`)

	uptimeColor := "#16a34a"
	if d.UptimePct < 95 {
		uptimeColor = "#dc2626"
	} else if d.UptimePct < 99 {
		uptimeColor = "#d97706"
	}

	availRows := []struct{ label, value, col string }{
		{"Uptime (monitored period)", fmt.Sprintf("%.2f%%", d.UptimePct), uptimeColor},
		{"Total downtime", fmtDur(time.Duration(d.TotalDowntimeMins) * time.Minute), "#64748b"},
		{"Downtime gaps detected", strconv.Itoa(len(d.DowntimeGaps)), "#64748b"},
		{"Service restarts / cold starts", strconv.Itoa(d.ServiceStartups), "#64748b"},
	}
	for i, row := range availRows {
		bg := "#ffffff"
		if i%2 == 1 {
			bg = "#f8fafc"
		}
		fmt.Fprintf(&b, `
	  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
	    <td style="padding:8px 10px">%s</td>
	    <td style="padding:8px 10px;font-weight:700;color:%s">%s</td>
	  </tr>`, bg, row.label, row.col, row.value)
	}
	b.WriteString(`</table>`)

	// Downtime gap list.
	if len(d.DowntimeGaps) > 0 {
		b.WriteString(`<table width="100%" cellpadding="4" cellspacing="0"
		  style="border-collapse:collapse;font-size:11px;margin-top:10px">
		  <tr style="background:#f8fafc;border-bottom:1px solid #e2e8f0">
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Gap Start (UTC)</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Gap End (UTC)</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Duration</th>
		    <th align="left" style="padding:6px 8px;color:#64748b;font-weight:600">Likely Cause</th>
		  </tr>`)
		for i, g := range d.DowntimeGaps {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#f8fafc"
			}
			cause := "Cold start / idle shutdown (Cloud Run scale-to-zero)"
			if g.Duration > 30*time.Minute {
				cause = "Extended outage — review Cloud Run logs for crash or OOM signals"
			}
			fmt.Fprintf(&b, `
		  <tr style="background:%s;border-bottom:1px solid #f1f5f9">
		    <td style="padding:6px 8px">%s</td>
		    <td style="padding:6px 8px">%s</td>
		    <td style="padding:6px 8px;font-weight:600;color:#dc2626">%s</td>
		    <td style="padding:6px 8px;color:#64748b">%s</td>
		  </tr>`,
				bg,
				g.Start.UTC().Format("Jan 02 15:04:05"),
				g.End.UTC().Format("Jan 02 15:04:05"),
				fmtDur(g.Duration),
				cause,
			)
		}
		b.WriteString(`</table>`)
	}
	b.WriteString(`</td></tr>`)

	// ── Section: Recommendations ──────────────────────────────────────────
	b.WriteString(sectionHeader("&#128161; Intelligent Analysis &amp; Recommendations"))
	b.WriteString(`<tr><td>`)
	for _, rec := range recommendations {
		b.WriteString(alertRow(escHTML(rec), "&#8594;", "#f0f9ff", "#3b82f6"))
	}
	b.WriteString(`</td></tr>`)

	// ── close main content table ──────────────────────────────────────────
	b.WriteString(`</table></td></tr>`)

	// ── Footer ────────────────────────────────────────────────────────────
	fmt.Fprintf(&b, `
<tr><td style="background:#f8fafc;border-top:1px solid #e2e8f0;padding:16px 32px">
  <div style="font-size:11px;color:#94a3b8;text-align:center;line-height:1.6">
    koko App &mdash; Demo Company &nbsp;|&nbsp; Cloud Run: <your-gcp-project-id> &middot; us-west2
    <br>Report generated automatically at %s each day. Override: <code>REPORT_HOUR_UTC</code> env var.
    <br>To change recipient, set <code>REPORT_EMAIL_TO</code>.
  </div>
</td></tr>`,
		fmt.Sprintf("%02d:00 UTC", reportHourUTC()))

	b.WriteString(`</table>
</td></tr>
</table>
</body>
</html>`)

	return b.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// SMTP email sender
// ─────────────────────────────────────────────────────────────────────────────

// sendEmail sends an HTML email via SMTP (STARTTLS on port 587 by default).
// Required env vars: SMTP_HOST, SMTP_USER, SMTP_PASSWORD.
// Optional:         SMTP_PORT (default 587), SMTP_FROM (default SMTP_USER).
// If SMTP_HOST is not set, the function logs a warning and returns nil.
func sendEmail(to, subject, htmlBody string) error {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host == "" {
		log.Printf("REPORT_EMAIL_SKIPPED: SMTP_HOST not configured — set SMTP_HOST, SMTP_USER, SMTP_PASSWORD to enable email delivery")
		return nil
	}

	portStr := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	port := 587
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			port = p
		}
	}

	user := strings.TrimSpace(os.Getenv("SMTP_USER"))
	pass := os.Getenv("SMTP_PASSWORD")
	from := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	if from == "" {
		from = user
	}

	if user == "" || pass == "" {
		return fmt.Errorf("SMTP misconfigured: SMTP_USER and SMTP_PASSWORD are required when SMTP_HOST is set")
	}

	// Build RFC 2822 + MIME message; base64-encode the HTML body so that
	// any non-ASCII characters are transmitted safely.
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody))
	// Wrap base64 at 76 chars per line as required by RFC 2045.
	var bodyLines strings.Builder
	for len(encoded) > 76 {
		bodyLines.WriteString(encoded[:76])
		bodyLines.WriteString("\r\n")
		encoded = encoded[76:]
	}
	if len(encoded) > 0 {
		bodyLines.WriteString(encoded)
		bodyLines.WriteString("\r\n")
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: koko App <%s>\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	fmt.Fprintf(&msg, "Date: %s\r\n", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: base64\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(bodyLines.String())

	addr := fmt.Sprintf("%s:%d", host, port)
	auth := smtp.PlainAuth("", user, pass, host)
	return smtp.SendMail(addr, auth, from, []string{to}, msg.Bytes())
}
