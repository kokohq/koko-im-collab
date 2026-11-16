package main

import (
	"context"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// AppMetricsSample holds one periodic runtime snapshot.
type AppMetricsSample struct {
	ID             int64
	SampledAt      time.Time
	HeapAllocBytes int64
	HeapSysBytes   int64
	SysBytes       int64
	Goroutines     int32
	CPUPct         float64
	ConcurrentReqs int32
	MsgsTotalCum   int64
	MsgsKokoCum    int64
	MsgsNonKokoCum int64
}

// AppRejectionEvent holds one request-rejection record.
type AppRejectionEvent struct {
	ID         int64
	RejectedAt time.Time
	Reason     string
	Endpoint   string
	UserName   string
	AccountID  string
}

// AppLifecycleEvent holds a service lifecycle record (startup / shutdown).
type AppLifecycleEvent struct {
	ID        int64
	EventType string
	EventAt   time.Time
	Details   string
}

// CommandUsageCount is a command name with its invocation count.
type CommandUsageCount struct {
	CommandName string
	Count       int64
}

// HourlyMsgCount is a UTC hour bucket with its message count.
type HourlyMsgCount struct {
	Hour  time.Time
	Count int64
}

// ─────────────────────────────────────────────────────────────────────────────
// Insert helpers
// ─────────────────────────────────────────────────────────────────────────────

// InsertMetricsSample persists one runtime snapshot. No-op in memory-only mode.
func (s *AppStore) InsertMetricsSample(ctx context.Context, m AppMetricsSample) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_metrics_samples
			(sampled_at, heap_alloc_bytes, heap_sys_bytes, sys_bytes,
			 goroutines, cpu_pct, concurrent_reqs,
			 msgs_total_cum, msgs_koko_cum, msgs_non_koko_cum)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		m.SampledAt,
		m.HeapAllocBytes,
		m.HeapSysBytes,
		m.SysBytes,
		m.Goroutines,
		m.CPUPct,
		m.ConcurrentReqs,
		m.MsgsTotalCum,
		m.MsgsKokoCum,
		m.MsgsNonKokoCum,
	)
	return err
}

// InsertRejectionEvent persists one rejected-request record.
func (s *AppStore) InsertRejectionEvent(ctx context.Context, reason, endpoint, userName, accountID string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_rejection_events (reason, endpoint, user_name, account_id)
		VALUES ($1,$2,$3,$4)`,
		reason, endpoint, userName, accountID,
	)
	return err
}

// InsertLifecycleEvent persists one lifecycle record.
func (s *AppStore) InsertLifecycleEvent(ctx context.Context, eventType, details string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_lifecycle_events (event_type, details) VALUES ($1,$2)`,
		eventType, details,
	)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Query helpers (used by the daily report generator)
// ─────────────────────────────────────────────────────────────────────────────

// QueryMetricsSamples returns all runtime samples in [from, to) ordered by time.
func (s *AppStore) QueryMetricsSamples(ctx context.Context, from, to time.Time) ([]AppMetricsSample, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sampled_at,
		       heap_alloc_bytes, heap_sys_bytes, sys_bytes,
		       goroutines, cpu_pct::float8, concurrent_reqs,
		       msgs_total_cum, msgs_koko_cum, msgs_non_koko_cum
		FROM app_metrics_samples
		WHERE sampled_at >= $1 AND sampled_at < $2
		ORDER BY sampled_at ASC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppMetricsSample
	for rows.Next() {
		var m AppMetricsSample
		if err := rows.Scan(
			&m.ID, &m.SampledAt,
			&m.HeapAllocBytes, &m.HeapSysBytes, &m.SysBytes,
			&m.Goroutines, &m.CPUPct, &m.ConcurrentReqs,
			&m.MsgsTotalCum, &m.MsgsKokoCum, &m.MsgsNonKokoCum,
		); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// QueryRejectionEvents returns all rejection events in [from, to).
func (s *AppStore) QueryRejectionEvents(ctx context.Context, from, to time.Time) ([]AppRejectionEvent, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rejected_at, reason, endpoint, user_name, account_id
		FROM app_rejection_events
		WHERE rejected_at >= $1 AND rejected_at < $2
		ORDER BY rejected_at ASC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppRejectionEvent
	for rows.Next() {
		var e AppRejectionEvent
		if err := rows.Scan(&e.ID, &e.RejectedAt, &e.Reason, &e.Endpoint, &e.UserName, &e.AccountID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueryLifecycleEvents returns lifecycle events in [from, to).
func (s *AppStore) QueryLifecycleEvents(ctx context.Context, from, to time.Time) ([]AppLifecycleEvent, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_type, event_at, details
		FROM app_lifecycle_events
		WHERE event_at >= $1 AND event_at < $2
		ORDER BY event_at ASC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppLifecycleEvent
	for rows.Next() {
		var e AppLifecycleEvent
		if err := rows.Scan(&e.ID, &e.EventType, &e.EventAt, &e.Details); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueryCommandUsageSummary returns per-command invocation counts in [from, to),
// ordered by count descending.
func (s *AppStore) QueryCommandUsageSummary(ctx context.Context, from, to time.Time) ([]CommandUsageCount, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT command_name, COUNT(*) AS cnt
		FROM koko_command_audit
		WHERE requested_at >= $1 AND requested_at < $2
		GROUP BY command_name
		ORDER BY cnt DESC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandUsageCount
	for rows.Next() {
		var c CommandUsageCount
		if err := rows.Scan(&c.CommandName, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// QueryHourlyMessageCounts returns per-hour message counts from messenger_inputs
// in [from, to), ordered chronologically.
func (s *AppStore) QueryHourlyMessageCounts(ctx context.Context, from, to time.Time) ([]HourlyMsgCount, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DATE_TRUNC('hour', received_at) AS hr, COUNT(*) AS cnt
		FROM messenger_inputs
		WHERE received_at >= $1 AND received_at < $2
		GROUP BY hr
		ORDER BY hr ASC`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourlyMsgCount
	for rows.Next() {
		var h HourlyMsgCount
		if err := rows.Scan(&h.Hour, &h.Count); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// QueryResourceIntensiveFlows returns commands that were executing during
// high-CPU periods (cpu_pct > 40%), ordered by co-occurrence count descending.
func (s *AppStore) QueryResourceIntensiveFlows(ctx context.Context, from, to time.Time) ([]CommandUsageCount, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ka.command_name, COUNT(*) AS hit_count
		FROM koko_command_audit ka
		WHERE ka.requested_at >= $1 AND ka.requested_at < $2
		  AND EXISTS (
		      SELECT 1 FROM app_metrics_samples ms
		      WHERE ms.cpu_pct > 40
		        AND ms.sampled_at BETWEEN ka.requested_at - INTERVAL '2 minutes'
		                              AND ka.requested_at + INTERVAL '2 minutes'
		  )
		GROUP BY ka.command_name
		ORDER BY hit_count DESC
		LIMIT 10`,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandUsageCount
	for rows.Next() {
		var c CommandUsageCount
		if err := rows.Scan(&c.CommandName, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
