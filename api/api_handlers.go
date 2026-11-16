package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiNormDomain lowercases and trims a domain value for consistent DB queries.
// Unlike the feature-runtime normalizedDomainKey, it returns "" for empty input
// (meaning no domain filter) rather than mapping to a default.
func apiNormDomain(d string) string {
	return strings.ToLower(strings.TrimSpace(d))
}

// globalJobQueue is initialised in main() and wired to all API handler closures.
var globalJobQueue *JobQueue

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/input  — enqueue an external text/audio/video message
// ─────────────────────────────────────────────────────────────────────────────

type InputPayload struct {
	Type      string `json:"type"`    // "text" | "audio" | "video"
	Content   string `json:"content"` // text content or base64-encoded media
	Domain    string `json:"domain"`
	GroupName string `json:"group_name"`
}

func (h *WebhookHandler) handleAPIInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var payload InputPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20 /* 1 MB */)).Decode(&payload); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Sanitize
	msgType, ok1 := sanitizeString(payload.Type)
	content, ok2 := sanitizeString(payload.Content)
	domain, ok3 := sanitizeString(payload.Domain)
	groupName, ok4 := sanitizeString(payload.GroupName)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		apiError(w, http.StatusBadRequest, "input contains disallowed characters or exceeds size limit")
		return
	}

	msgType = strings.ToLower(msgType)
	if msgType != "text" && msgType != "audio" && msgType != "video" {
		apiError(w, http.StatusBadRequest, "type must be one of: text, audio, video")
		return
	}
	if strings.TrimSpace(content) == "" {
		apiError(w, http.StatusBadRequest, "content is required")
		return
	}

	normDomainInput := apiNormDomain(domain)
	meta := ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   key.AccountID,
		GroupName:   groupName,
		Domain:      normDomainInput,
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// Capture values for async closure — r.Context() will be cancelled after return.
	accountID := key.AccountID
	userName := key.UserName
	store := h.store
	job := globalJobQueue.Enqueue(meta, func() JobResultData {
		// Persist the incoming message via the store using the same path as Telegram messages
		if store != nil && store.DBEnabled() {
			_, err := store.SaveMessengerInput(context.Background(), MessengerInput{
				AccountID:     accountID,
				GroupName:     groupName,
				UserName:      userName,
				MessengerType: "api",
				ChatID:        accountID + "::" + groupName,
				MessageType:   msgType,
				TextContent:   content,
				ReceivedAt:    time.Now().UTC(),
			})
			if err != nil {
				log.Printf("API_INPUT_SAVE_FAILED: account=%s error=%v", accountID, err)
				return JobResultData{Error: "failed to persist message: " + err.Error()}
			}
		}
		return JobResultData{Results: map[string]string{"status": "message stored"}}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":    meta,
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "your request is being processed; poll /api/jobs/" + job.ID + " for results",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/summaries — paginated summaries with domain + date range filters
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) handleGetSummaries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query()
	groupName := cleanParam(q.Get("group_name"))
	domain := cleanParam(q.Get("domain"))
	from, to, err := parseAPIDateRange(q.Get("from"), q.Get("to"))
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid date format — use now, now-2d/h/w/m/y, RFC3339, or YYYY-MM-DD")
		return
	}
	page := intParam(q.Get("page"), 1)
	pageSize := intParam(q.Get("page_size"), apiMaxPageSize)
	if pageSize > apiMaxPageSize {
		pageSize = apiMaxPageSize
	}

	// Verify group access
	if strings.TrimSpace(groupName) != "" {
		if _, authorized := h.authorizeGroupAccess(r, key.AccountID, groupName); !authorized {
			apiError(w, http.StatusForbidden, "you are not authorized for this group")
			return
		}
	}

	normDomain := apiNormDomain(domain)
	meta := ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   key.AccountID,
		GroupName:   groupName,
		Domain:      normDomain,
		DateFrom:    formatDateParam(from),
		DateTo:      formatDateParam(to),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	store := h.store
	accountID := key.AccountID
	job := globalJobQueue.Enqueue(meta, func() JobResultData {
		results, total, err := store.ListSummaries(context.Background(), accountID, groupName, normDomain, from, to, page, pageSize)
		if err != nil {
			log.Printf("LIST_SUMMARIES_FAILED: account=%s error=%v", accountID, err)
			return JobResultData{Error: "failed to retrieve summaries: " + err.Error()}
		}
		return JobResultData{Results: results, TotalCount: total, Page: page, PageSize: pageSize}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":    meta,
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "your request is being processed; poll /api/jobs/" + job.ID + " for results",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/act-prompts — paginated koko:act AI prompts with filters
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) handleGetActPrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query()
	groupName := cleanParam(q.Get("group_name"))
	domain := cleanParam(q.Get("domain"))
	from, to, err := parseAPIDateRange(q.Get("from"), q.Get("to"))
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid date format — use now, now-2d/h/w/m/y, RFC3339, or YYYY-MM-DD")
		return
	}
	page := intParam(q.Get("page"), 1)
	pageSize := intParam(q.Get("page_size"), apiMaxPageSize)
	if pageSize > apiMaxPageSize {
		pageSize = apiMaxPageSize
	}

	if strings.TrimSpace(groupName) != "" {
		if _, authorized := h.authorizeGroupAccess(r, key.AccountID, groupName); !authorized {
			apiError(w, http.StatusForbidden, "you are not authorized for this group")
			return
		}
	}

	normDomain := apiNormDomain(domain)
	meta := ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   key.AccountID,
		GroupName:   groupName,
		Domain:      normDomain,
		DateFrom:    formatDateParam(from),
		DateTo:      formatDateParam(to),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	store := h.store
	accountID := key.AccountID
	job := globalJobQueue.Enqueue(meta, func() JobResultData {
		results, total, err := store.ListActPrompts(context.Background(), accountID, groupName, normDomain, from, to, page, pageSize)
		if err != nil {
			log.Printf("LIST_ACT_PROMPTS_FAILED: account=%s error=%v", accountID, err)
			return JobResultData{Error: "failed to retrieve act prompts: " + err.Error()}
		}
		return JobResultData{Results: results, TotalCount: total, Page: page, PageSize: pageSize}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":    meta,
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "your request is being processed; poll /api/jobs/" + job.ID + " for results",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/jobs/{id} — poll job status and result
// ─────────────────────────────────────────────────────────────────────────────

func handleGetJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	// Extract job ID from path: /api/jobs/<id>
	id := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/?#") {
		apiError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	rec, ok := globalJobQueue.Get(id)
	if !ok {
		apiError(w, http.StatusNotFound, "job not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(rec)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func cleanParam(v string) string {
	s, ok := sanitizeString(v)
	if !ok {
		return ""
	}
	return s
}

func intParam(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}

func parseAPIDateRange(fromStr, toStr string) (time.Time, time.Time, error) {
	// Delegates to the unified ParseRelativeDateRange which handles "now",
	// "now-2d/h/w/m/y", RFC3339 and YYYY-MM-DD.
	return ParseRelativeDateRange(fromStr, toStr)
}

func formatDateParam(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
