package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Relative date / time parsing
//
// Supports the following formats:
//   - "now"       → current UTC instant
//   - "now-2d"    → 2 days ago
//   - "now-2h"    → 2 hours ago
//   - "now-2w"    → 2 weeks ago
//   - "now-2m"    → 2 months ago
//   - "now-2y"    → 2 years ago
//   - "now-3"     → 3 hours ago (bare number defaults to hours)
//
// All formats are case-insensitive.  Standard YYYY-MM-DD and RFC3339 strings
// pass through unchanged (parsed by callers).
// ─────────────────────────────────────────────────────────────────────────────

// ParseRelativeDate parses a relative-date string and returns the corresponding
// UTC time.  The second return value is false when the string is not a
// recognisable relative-date expression (the caller should try other parsers).
func ParseRelativeDate(s string) (time.Time, bool) {
	// Collapse any spaces around the '-' after "now" so that
	// "now -2d", "now- 2d", and "now - 2d" all parse identically to "now-2d".
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return time.Time{}, false
	}
	if lower == "now" {
		return time.Now().UTC(), true
	}

	// Normalise "now <spaces> - <spaces> <magnitude>" → "now-<magnitude>"
	if strings.HasPrefix(lower, "now") {
		after := strings.TrimSpace(lower[3:])
		if strings.HasPrefix(after, "-") {
			lower = "now-" + strings.TrimSpace(after[1:])
		}
	}

	// Must start with "now-<magnitude>[unit]"
	if !strings.HasPrefix(lower, "now-") {
		return time.Time{}, false
	}

	rest := lower[len("now-"):]
	if rest == "" {
		return time.Time{}, false
	}

	// Split magnitude digits from optional unit suffix.
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return time.Time{}, false // no digits
	}
	n, err := strconv.Atoi(rest[:i])
	if err != nil || n < 0 {
		return time.Time{}, false
	}
	unit := rest[i:] // may be empty → default hours

	now := time.Now().UTC()
	switch unit {
	case "", "h":
		return now.Add(-time.Duration(n) * time.Hour), true
	case "d":
		return now.AddDate(0, 0, -n), true
	case "w":
		return now.AddDate(0, 0, -n*7), true
	case "m":
		return now.AddDate(0, -n, 0), true
	case "y":
		return now.AddDate(-n, 0, 0), true
	default:
		return time.Time{}, false
	}
}

// ParseRelativeDateRange parses a pair of strings as relative or absolute dates
// and returns (start, end, error).  Each string may be:
//   - A relative expression ("now", "now-2d", …)
//   - An RFC3339 timestamp
//   - A YYYY-MM-DD date (end date is expanded to end-of-day)
//
// An empty string for either bound returns the zero time.Time (no bound).
func ParseRelativeDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	from, err := parseDateTimeString(fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from date %q: %w", fromStr, err)
	}
	to, err := parseDateTimeString(toStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to date %q: %w", toStr, err)
	}
	return from, to, nil
}

// parseDateTimeString tries relative-date parsing first, then RFC3339, then
// YYYY-MM-DD.  An empty string returns (zero, nil).
func parseDateTimeString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}

	// Relative expression
	if t, ok := ParseRelativeDate(s); ok {
		return t, nil
	}

	// RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	// YYYY-MM-DD (end-of-day inclusive: add 24h so queries use < end)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Add(24 * time.Hour), nil
	}

	return time.Time{}, fmt.Errorf("unrecognised date format (use now, now-2d/h/w/m/y, RFC3339, or YYYY-MM-DD)")
}
