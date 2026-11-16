package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Retention policy — keeps data within plan-tier storage limits and age caps.
//
// Enforcement is split into two independent axes:
//   1. Age-based: delete rows older than KOKO_RETENTION_DAYS (default 90 days)
//      across messenger_inputs, messenger_summaries, policy_audit_logs,
//      command_usage_events and koko_command_audit.
//   2. Storage-based: for each account, if the estimated byte count of
//      messenger_inputs exceeds the plan tier's storage_bytes quota, delete
//      the oldest rows until the account is back within quota.
//
// Both axes are evaluated per account on every enforcement cycle.
// The enforcer runs every retentionCheckInterval and is started once from
// main.go via handler.store.StartRetentionEnforcer().
// ─────────────────────────────────────────────────────────────────────────────

const (
	retentionCheckInterval = 6 * time.Hour
	defaultRetentionDays   = 90
	envRetentionDays       = "KOKO_RETENTION_DAYS"
)

// retentionDays returns the configured retention window in days.
func retentionDays() int {
	if v := os.Getenv(envRetentionDays); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRetentionDays
}

// StartRetentionEnforcer launches a background goroutine that periodically
// enforces the data retention policy.  It runs once immediately on startup
// so stale data from a cold start is not carried over, and then repeats every
// retentionCheckInterval.
func (s *AppStore) StartRetentionEnforcer() {
	if s.db == nil {
		// In-memory mode — nothing to vacuum.
		return
	}
	go func() {
		// Initial run shortly after startup so cold-start data is not
		// accumulated indefinitely without waiting a full cycle.
		time.Sleep(30 * time.Second)
		s.runRetentionCycle()

		ticker := time.NewTicker(retentionCheckInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.runRetentionCycle()
		}
	}()
}

// runRetentionCycle collects all accounts and enforces both age-based and
// storage-based retention on each one.
func (s *AppStore) runRetentionCycle() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays())
	log.Printf("RETENTION_CYCLE_START: cutoff=%s", cutoff.Format(time.RFC3339))

	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		log.Printf("RETENTION_CYCLE_LIST_ACCOUNTS_FAILED: %v", err)
		return
	}
	for _, acct := range accounts {
		s.enforceAccountRetention(ctx, acct, cutoff)
	}
	log.Printf("RETENTION_CYCLE_DONE: accounts=%d", len(accounts))
}

// enforceAccountRetention applies both age-based and storage-based trimming
// for a single account.
func (s *AppStore) enforceAccountRetention(ctx context.Context, acct Account, cutoff time.Time) {
	accountID := acct.AccountID

	// ── 1. Age-based deletion ────────────────────────────────────────────────

	tables := []struct {
		table string
		tsCol string
	}{
		{"messenger_inputs", "received_at"},
		{"messenger_summaries", "summarized_at"},
		{"policy_audit_logs", "created_at"},
		{"command_usage_events", "executed_at"},
		{"koko_command_audit", "requested_at"},
	}
	for _, t := range tables {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err := s.db.ExecContext(qctx, `
			DELETE FROM `+t.table+`
			WHERE account_id = $1 AND `+t.tsCol+` < $2
		`, accountID, cutoff)
		cancel()
		if err != nil {
			log.Printf("RETENTION_AGE_DELETE_FAILED: account=%s table=%s error=%v",
				accountID, t.table, err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			log.Printf("RETENTION_AGE_DELETED: account=%s table=%s rows=%d",
				accountID, t.table, n)
		}
	}

	// ── 2. Storage-based trimming for messenger_inputs ───────────────────────
	// Look up the plan tier for this account's storage quota.
	tier, err := s.GetPlanTierForAccount(ctx, accountID)
	if err != nil || tier.StorageBytes <= 0 {
		// No plan tier or no quota — skip storage enforcement.
		return
	}

	// Estimate current storage usage in messenger_inputs for this account.
	// We use pg_column_size() as a lightweight approximation.
	var estimatedBytes int64
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = s.db.QueryRowContext(qctx, `
		SELECT COALESCE(SUM(
			COALESCE(pg_column_size(text_content),0) +
			COALESCE(pg_column_size(payload),0) +
			COALESCE(pg_column_size(media_id),0) +
			64
		), 0)
		FROM messenger_inputs
		WHERE account_id = $1
	`, accountID).Scan(&estimatedBytes)
	cancel()
	if err != nil {
		log.Printf("RETENTION_STORAGE_ESTIMATE_FAILED: account=%s error=%v", accountID, err)
		return
	}

	if estimatedBytes <= tier.StorageBytes {
		// Within quota — nothing to do.
		return
	}

	excess := estimatedBytes - tier.StorageBytes
	log.Printf("RETENTION_STORAGE_OVER_QUOTA: account=%s used=%d quota=%d excess=%d",
		accountID, estimatedBytes, tier.StorageBytes, excess)

	// Delete the oldest messenger_inputs rows in batches until the estimated
	// excess is cleared. We delete in batches of 100 to limit lock contention.
	const batchSize = 100
	for estimatedBytes > tier.StorageBytes {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res, err := s.db.ExecContext(qctx, `
			DELETE FROM messenger_inputs
			WHERE id IN (
				SELECT id FROM messenger_inputs
				WHERE account_id = $1
				ORDER BY received_at ASC
				LIMIT $2
			)
		`, accountID, batchSize)
		cancel()
		if err != nil {
			log.Printf("RETENTION_STORAGE_DELETE_BATCH_FAILED: account=%s error=%v", accountID, err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
		// Rough per-row size estimate: reduce by (excess / batchSize) per deleted row.
		// We re-estimate crudely rather than re-querying on every batch.
		if estimatedBytes > 0 {
			estimatedBytes -= (excess / int64(batchSize+1)) * n
		}
		log.Printf("RETENTION_STORAGE_DELETED_BATCH: account=%s rows=%d", accountID, n)
	}

	// Keep the accounts.total_storage_used column up to date.
	qctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	_, _ = s.db.ExecContext(qctx, `
		UPDATE accounts
		SET total_storage_used = (
			SELECT COALESCE(SUM(
				COALESCE(pg_column_size(text_content),0) +
				COALESCE(pg_column_size(payload),0) +
				COALESCE(pg_column_size(media_id),0) +
				64
			), 0)
			FROM messenger_inputs
			WHERE account_id = $1
		)
		WHERE account_id = $1
	`, accountID)
	cancel()
}
