package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Rate limiter: sliding window, configurable per-key limit
// Default: 100 requests per minute. Override via RATE_LIMIT_PER_KEY env var.
// ─────────────────────────────────────────────────────────────────────────────

const (
	defaultRateLimitPerKey = 100
	rateLimitWindow        = time.Minute
	apiMaxPageSize         = 5
)

// rateLimitPerKey returns the configured per-API-key request cap, reading
// RATE_LIMIT_PER_KEY from the environment (default 100 req / min).
func rateLimitPerKey() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("RATE_LIMIT_PER_KEY"))); err == nil && v > 0 {
		return v
	}
	return defaultRateLimitPerKey
}

type rateBucket struct {
	mu   sync.Mutex
	hits []time.Time
}

func (b *rateBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rateLimitWindow)
	// Drop expired hits
	filtered := b.hits[:0]
	for _, t := range b.hits {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	b.hits = filtered
	if len(b.hits) >= rateLimitPerKey() {
		return false
	}
	b.hits = append(b.hits, now)
	return true
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
}

var globalRateLimiter = &rateLimiter{buckets: map[string]*rateBucket{}}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &rateBucket{}
		rl.buckets[key] = b
	}
	rl.mu.Unlock()
	return b.allow()
}

// ─────────────────────────────────────────────────────────────────────────────
// Input sanitization
// ─────────────────────────────────────────────────────────────────────────────

var (
	// Reject SQL injection keywords and script injection patterns
	dangerousPattern = regexp.MustCompile(`(?i)(--|\b(drop|truncate|delete from|insert into|exec\s*\(|xp_|script>|<script|javascript:|onerror=|onload=)\b)`)
	// Max reasonable field length
	maxFieldLen = 512
)

func sanitizeString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) > maxFieldLen {
		return "", false
	}
	if dangerousPattern.MatchString(s) {
		return "", false
	}
	return s, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth context key
// ─────────────────────────────────────────────────────────────────────────────

type contextKey string

const ctxAPIKey contextKey = "api_key_record"

// AuthedKey returns the validated KokoAPIKey from context (set by apiAuthMiddleware).
func AuthedKey(r *http.Request) (KokoAPIKey, bool) {
	v, ok := r.Context().Value(ctxAPIKey).(KokoAPIKey)
	return v, ok
}

// ─────────────────────────────────────────────────────────────────────────────
// API auth + rate limit middleware
// ─────────────────────────────────────────────────────────────────────────────

// apiAuthMiddleware validates the Bearer token, checks rate limit, and injects
// the KokoAPIKey into the request context.
func (h *WebhookHandler) apiAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			authHeader = r.Header.Get("X-API-Key")
		}
		rawKey := strings.TrimPrefix(authHeader, "Bearer ")
		rawKey = strings.TrimSpace(rawKey)
		if rawKey == "" {
			apiError(w, http.StatusUnauthorized, "missing API key — send Authorization: Bearer <key>")
			return
		}

		// Sanitize the key to avoid log injection
		if len(rawKey) > 128 || strings.ContainsAny(rawKey, "\r\n\t") {
			apiError(w, http.StatusUnauthorized, "invalid API key format")
			return
		}

		// Validate against DB
		if h.store == nil {
			apiError(w, http.StatusServiceUnavailable, "store unavailable")
			return
		}
		rec, err := h.store.ValidateAPIKey(r.Context(), rawKey)
		if err != nil {
			apiError(w, http.StatusUnauthorized, "invalid or unknown API key")
			return
		}

		// Rate limit per API key
		if !globalRateLimiter.allow(rawKey) {
			if globalMetrics != nil {
				globalMetrics.RecordRejection("rate_limit", r.URL.Path, rec.UserName, rec.AccountID)
			}
			apiError(w, http.StatusTooManyRequests, fmt.Sprintf("rate limit exceeded — max %d requests per minute", rateLimitPerKey()))
			return
		}

		// Inject into context
		ctx := context.WithValue(r.Context(), ctxAPIKey, rec)
		next(w, r.WithContext(ctx))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Group authorization helper
// ─────────────────────────────────────────────────────────────────────────────

// authorizeGroupAccess verifies that the authenticated API key's account matches
// the requested accountID and that the user belongs to the requested group.
// Returns the matched Group or an empty Group + false.
func (h *WebhookHandler) authorizeGroupAccess(r *http.Request, requestedAccountID, requestedGroupName string) (Group, bool) {
	key, ok := AuthedKey(r)
	if !ok {
		return Group{}, false
	}
	// Account must match the key's account
	if strings.TrimSpace(requestedAccountID) != "" && key.AccountID != requestedAccountID {
		return Group{}, false
	}
	if strings.TrimSpace(requestedGroupName) == "" {
		return Group{}, true // no group restriction requested
	}
	if h.store == nil {
		return Group{}, false
	}
	groups, err := h.store.ListGroups(r.Context(), key.AccountID)
	if err != nil {
		return Group{}, false
	}
	for _, g := range groups {
		if strings.EqualFold(g.GroupName, requestedGroupName) || g.GroupID == requestedGroupName {
			return g, true
		}
	}
	return Group{}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// Response stamped header helper
// ─────────────────────────────────────────────────────────────────────────────

// ResponseMeta carries standard header metadata stamped on every API response.
type ResponseMeta struct {
	RequestedBy string `json:"requested_by"`
	AccountID   string `json:"account_id"`
	GroupName   string `json:"group_name"`
	Domain      string `json:"domain"`
	DateFrom    string `json:"date_from,omitempty"`
	DateTo      string `json:"date_to,omitempty"`
	RequestedAt string `json:"requested_at"`
}

func stampedResponse(w http.ResponseWriter, statusCode int, meta ResponseMeta, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta": meta,
		"data": data,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Error helpers
// ─────────────────────────────────────────────────────────────────────────────

func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
