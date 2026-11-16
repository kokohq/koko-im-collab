package main

// Natural Language Query (NLQ) engine for koko.
//
// Accepts free-form English business questions and maps them to structured
// DB queries using Vertex AI (Gemini). Handles English variants and common
// acronyms used in South Asian enterprise contexts:
//
//   Temporal:  FY, Q1-Q4 (Indian fiscal quarters), LW, LM, YTD, MOM, YOY,
//              "this week", "last month", "next quarter"
//   Domain:    GST → revenue; P&L/PnL → revenue; gold/jewellery → industrial;
//              PCB/chips/component → electronics; sales/CRM/pipeline → revenue;
//              consumer/lifestyle → consumer
//   Intent:    "show me", "fetch", "get me", "gimme", "pull up", "find",
//              "summaries"/"chats"/"what was said" → summaries intent
//              "actions"/"act prompts"/"AI suggested" → act_prompts intent
//
// REST endpoint:  POST /api/nlq   (protected by apiAuthMiddleware)
// Also called by: MCP natural_language_query tool and Telegram NLQ path.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// NLQParsedIntent is the structured output of Gemini-powered query parsing.
type NLQParsedIntent struct {
	Intent     string `json:"intent"`     // summaries | act_prompts | both | general
	Domain     string `json:"domain"`     // industrial | electronics | revenue | consumer | all | ""
	GroupName  string `json:"group_name"` // exact group name or ""
	From       string `json:"from"`       // YYYY-MM-DD or ""
	To         string `json:"to"`         // YYYY-MM-DD or ""
	Page       int    `json:"page"`
	PageSize   int    `json:"page_size"`
	PlainQuery string `json:"plain_query"` // cleaned version for logging
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/nlq — external REST endpoint
// ─────────────────────────────────────────────────────────────────────────────

type nlqRequest struct {
	Query     string `json:"query"`
	AccountID string `json:"account_id"`
	Domain    string `json:"domain"` // optional hint
}

// handleAPIQuery handles both:
//
//	GET  /api/nlq?q=<query>&domain=<domain>&account_id=<id>
//	     → executes the NLQ synchronously and returns the answer inline.
//
//	POST /api/nlq  {"query":"...","domain":"...","account_id":"..."}
//	     → enqueues the NLQ as an async job and returns 202 + job_id.
//
// Both paths are protected by apiAuthMiddleware.
func (h *WebhookHandler) handleAPIQuery(w http.ResponseWriter, r *http.Request) {
	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var (
		rawQuery   string
		rawDomain  string
		rawAccount string
	)

	switch r.Method {
	case http.MethodGet:
		rawQuery = r.URL.Query().Get("q")
		rawDomain = r.URL.Query().Get("domain")
		rawAccount = r.URL.Query().Get("account_id")

	case http.MethodPost:
		var payload nlqRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&payload); err != nil {
			apiError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		rawQuery = payload.Query
		rawDomain = payload.Domain
		rawAccount = payload.AccountID

	default:
		apiError(w, http.StatusMethodNotAllowed, "GET or POST required")
		return
	}

	// Validate + sanitize inputs
	query, qOK := sanitizeString(rawQuery)
	domainHint, _ := sanitizeString(rawDomain)
	if !qOK || strings.TrimSpace(query) == "" {
		apiError(w, http.StatusBadRequest, "q / query is required and must not contain disallowed characters")
		return
	}

	accountID := strings.TrimSpace(rawAccount)
	if accountID == "" {
		accountID = key.AccountID
	}
	if accountID != key.AccountID {
		apiError(w, http.StatusForbidden, "account_id does not match your API key")
		return
	}

	meta := ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   accountID,
		Domain:      apiNormDomain(domainHint),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// ── GET: execute synchronously, return the answer immediately ────────────
	if r.Method == http.MethodGet {
		result, err := h.executeNLQ(r.Context(), accountID, key.UserName, query, domainHint)
		if err != nil {
			log.Printf("NLQ_EXECUTE_FAILED: account=%s error=%v", accountID, err)
			apiError(w, http.StatusInternalServerError, "query execution failed: "+err.Error())
			return
		}
		stampedResponse(w, http.StatusOK, meta, map[string]string{"response": result})
		return
	}

	// ── POST: enqueue for async processing, return 202 + job_id ─────────────
	capturedQuery := query
	capturedDomain := domainHint
	capturedAccount := accountID
	job := globalJobQueue.Enqueue(meta, func() JobResultData {
		res, err := h.executeNLQ(context.Background(), capturedAccount, key.UserName, capturedQuery, capturedDomain)
		if err != nil {
			log.Printf("NLQ_EXECUTE_FAILED: account=%s error=%v", capturedAccount, err)
			return JobResultData{Error: "NLQ execution failed: " + err.Error()}
		}
		return JobResultData{Results: map[string]string{"response": res}}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":    meta,
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "your query is being processed; poll /api/jobs/" + job.ID,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// executeNLQ — shared core: parse intent → query store → format response
// Called by: handleAPIQuery, mcpNaturalLanguageQuery, runKokoNLQDataQuery (Telegram)
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) executeNLQ(ctx context.Context, accountID, userName, query, domainHint string) (string, error) {
	preprocessedQuery, _ := nlpPreprocessEnglishWithContext(h.store, accountID, "", query, apiNormDomain(domainHint))
	if strings.TrimSpace(preprocessedQuery) == "" {
		preprocessedQuery = query
	}

	// First try fast keyword-based detection (no AI call, instant)
	parsed := nlqFastParse(preprocessedQuery, domainHint)

	// If keyword parsing didn't produce a confident data-retrieval intent,
	// use Gemini for deeper understanding (handles complex phrasing, acronyms, etc.)
	if parsed.Intent == "general" {
		geminiParsed, err := h.parseNLQWithGemini(preprocessedQuery, domainHint)
		if err != nil {
			log.Printf("NLQ_GEMINI_PARSE_FAILED account=%s: %v — using keyword fallback", accountID, err)
			// Keep keyword-based result
		} else {
			parsed = geminiParsed
		}
	}

	// Clamp page size
	if parsed.PageSize <= 0 || parsed.PageSize > apiMaxPageSize {
		parsed.PageSize = apiMaxPageSize
	}
	if parsed.Page <= 0 {
		parsed.Page = 1
	}

	from, to, _ := parseAPIDateRange(parsed.From, parsed.To)
	domain := apiNormDomain(parsed.Domain)

	switch parsed.Intent {
	case "summaries":
		if h.store == nil {
			return "", fmt.Errorf("store unavailable")
		}
		results, total, err := h.store.ListSummaries(ctx, accountID, parsed.GroupName, domain, from, to, parsed.Page, parsed.PageSize)
		if err != nil {
			return "", fmt.Errorf("summaries query failed: %w", err)
		}
		return mcpFormatSummaries(results, total, parsed.Page, parsed.PageSize, parsed.GroupName, domain), nil

	case "act_prompts":
		if h.store == nil {
			return "", fmt.Errorf("store unavailable")
		}
		results, total, err := h.store.ListActPrompts(ctx, accountID, parsed.GroupName, domain, from, to, parsed.Page, parsed.PageSize)
		if err != nil {
			return "", fmt.Errorf("act prompts query failed: %w", err)
		}
		return mcpFormatActPrompts(results, total, parsed.Page, parsed.PageSize, parsed.GroupName, domain), nil

	case "both":
		if h.store == nil {
			return "", fmt.Errorf("store unavailable")
		}
		summaries, totalS, errS := h.store.ListSummaries(ctx, accountID, parsed.GroupName, domain, from, to, parsed.Page, parsed.PageSize)
		prompts, totalP, errP := h.store.ListActPrompts(ctx, accountID, parsed.GroupName, domain, from, to, parsed.Page, parsed.PageSize)
		parts := []string{}
		if errS == nil {
			parts = append(parts, mcpFormatSummaries(summaries, totalS, parsed.Page, parsed.PageSize, parsed.GroupName, domain))
		}
		if errP == nil {
			parts = append(parts, mcpFormatActPrompts(prompts, totalP, parsed.Page, parsed.PageSize, parsed.GroupName, domain))
		}
		if len(parts) == 0 {
			return "No data found for your query.", nil
		}
		return strings.Join(parts, "\n\n---\n\n"), nil

	default:
		// general — no DB data to return; provide helpful hint
		return fmt.Sprintf(
			"koko understood your query about '%s'. "+
				"For stored data, try: 'show summaries for last month', 'fetch act prompts for revenue this FY', "+
				"or 'get gold domain summaries for Q3'.",
			parsed.PlainQuery,
		), nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fast keyword-based NLQ parser (no AI call)
// Handles the most common patterns instantly.
// ─────────────────────────────────────────────────────────────────────────────

func nlqFastParse(query, domainHint string) NLQParsedIntent {
	q := strings.ToLower(strings.TrimSpace(query))
	out := NLQParsedIntent{
		Intent:     "general",
		Domain:     apiNormDomain(domainHint),
		Page:       1,
		PageSize:   apiMaxPageSize,
		PlainQuery: query,
	}

	// ── Intent detection ──────────────────────────────────────────────────────
	summaryKW := []string{
		"summar", "what was said", "chat log", "conversation", "chats",
		"discussion", "transcript",
	}
	actKW := []string{
		"act prompt", "action suggested", "ai suggested", "koko act", "suggested action",
		"what action", "what did koko suggest", "suggested by koko",
	}
	bothKW := []string{"everything", "all data", "both", "summaries and actions"}

	intentScore := func(kws []string) int {
		score := 0
		for _, kw := range kws {
			if strings.Contains(q, kw) {
				score++
			}
		}
		return score
	}
	sScore := intentScore(summaryKW)
	aScore := intentScore(actKW)
	bScore := intentScore(bothKW)

	if bScore > 0 {
		out.Intent = "both"
	} else if sScore > aScore && sScore > 0 {
		out.Intent = "summaries"
	} else if aScore > sScore && aScore > 0 {
		out.Intent = "act_prompts"
	} else if sScore > 0 && aScore > 0 {
		out.Intent = "both"
	}

	// ── Domain detection ──────────────────────────────────────────────────────
	if out.Domain == "" {
		switch {
		case anyIn(q, "gold", "jewel", "jewellery", "bullion", "hallmark", "gem", "gems", "carats"):
			out.Domain = "industrial"
		case anyIn(q, "pcb", "chip", "gadget", "component", "electronic"):
			out.Domain = "electronics"
		case anyIn(q, "gst", "p&l", "pnl", "profit", "loss", "revenue", "sales", "crm", "pipeline", "tax", "turnover"):
			out.Domain = "revenue"
		case anyIn(q, "consumer", "lifestyle", "personal", "household"):
			out.Domain = "consumer"
		}
	}

	// ── Temporal detection (Indian FY) ────────────────────────────────────────
	today := time.Now().UTC()
	fyStart, fyEnd := currentIndianFY(today)

	switch {
	case anyIn(q, "last week", "lw", "past week"):
		out.From = today.AddDate(0, 0, -7).Format("2006-01-02")
		out.To = today.Format("2006-01-02")

	case anyIn(q, "last month", "lm", "past month"):
		lm := today.AddDate(0, -1, 0)
		out.From = time.Date(lm.Year(), lm.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		out.To = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Format("2006-01-02")

	case anyIn(q, "this week"):
		weekday := int(today.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		out.From = today.AddDate(0, 0, -(weekday - 1)).Format("2006-01-02")
		out.To = today.Format("2006-01-02")

	case anyIn(q, "this month"):
		out.From = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		out.To = today.Format("2006-01-02")

	case anyIn(q, "this fy", "this fiscal year", "current fy", "current fiscal"):
		out.From = fyStart.Format("2006-01-02")
		out.To = fyEnd.Format("2006-01-02")

	case anyIn(q, "last fy", "last fiscal year", "previous fy"):
		prevStart := fyStart.AddDate(-1, 0, 0)
		prevEnd := fyStart.AddDate(0, 0, -1)
		out.From = prevStart.Format("2006-01-02")
		out.To = prevEnd.Format("2006-01-02")

	case anyIn(q, "ytd", "year to date"):
		out.From = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		out.To = today.Format("2006-01-02")

	case anyIn(q, "q1"):
		q1e := fyStart.AddDate(0, 3, 0).AddDate(0, 0, -1)
		out.From = fyStart.Format("2006-01-02")
		out.To = q1e.Format("2006-01-02")

	case anyIn(q, "q2"):
		q2s := fyStart.AddDate(0, 3, 0)
		q2e := fyStart.AddDate(0, 6, 0).AddDate(0, 0, -1)
		out.From = q2s.Format("2006-01-02")
		out.To = q2e.Format("2006-01-02")

	case anyIn(q, "q3"):
		q3s := fyStart.AddDate(0, 6, 0)
		q3e := fyStart.AddDate(0, 9, 0).AddDate(0, 0, -1)
		out.From = q3s.Format("2006-01-02")
		out.To = q3e.Format("2006-01-02")

	case anyIn(q, "q4"):
		q4s := fyStart.AddDate(0, 9, 0)
		out.From = q4s.Format("2006-01-02")
		out.To = fyEnd.Format("2006-01-02")

	case anyIn(q, "today"):
		out.From = today.Format("2006-01-02")
		out.To = today.Format("2006-01-02")

	case anyIn(q, "yesterday"):
		yd := today.AddDate(0, 0, -1)
		out.From = yd.Format("2006-01-02")
		out.To = yd.Format("2006-01-02")
	}

	return out
}

// anyIn returns true if the input string contains any of the given substrings.
func anyIn(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// nlqEmptyOrGeneral returns true when an executeNLQ result indicates that no
// stored data was found (zero-result response from DB formatters) or that the
// NLQ engine could not map the query to a specific data intent. Callers use
// this to decide whether to return a "no data found / please refine" message.
func nlqEmptyOrGeneral(result string) bool {
	if strings.TrimSpace(result) == "" {
		return true
	}
	r := strings.ToLower(result)
	// Zero-result responses from mcpFormatSummaries / mcpFormatActPrompts
	if strings.Contains(r, "no summaries found") && strings.Contains(r, "total: 0") {
		return true
	}
	if strings.Contains(r, "no act prompts found") && strings.Contains(r, "total: 0") {
		return true
	}
	// General-intent hint returned by executeNLQ when intent == "general"
	if strings.Contains(r, "koko understood your query") {
		return true
	}
	return false
}

// looksLikeNLQDataRequest returns true when the query appears to be asking for
// stored data — triggers Gemini NLQ path even when keyword parse returns "general".
func looksLikeNLQDataRequest(query string) bool {
	q := strings.ToLower(query)
	dataVerbs := []string{
		"show", "fetch", "get", "gimme", "pull", "retrieve", "find", "list", "display",
		"what", "which", "give me", "tell me",
	}
	for _, v := range dataVerbs {
		if strings.Contains(q, v) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Gemini-powered NLQ parser
// ─────────────────────────────────────────────────────────────────────────────

// nlqJSONFence strips ```json ... ``` fences from Gemini's response.
var nlqJSONFence = regexp.MustCompile(`(?s)` + "```" + `(?:json)?\s*(\{.*?\})\s*` + "```")

func (h *WebhookHandler) parseNLQWithGemini(query, domainHint string) (NLQParsedIntent, error) {
	today := time.Now().UTC()
	fyStart, fyEnd := currentIndianFY(today)
	prompt := buildNLQGeminiPrompt(query, domainHint, today, fyStart, fyEnd)

	parts := []map[string]interface{}{{"text": prompt}}
	raw, err := h.callVertexAIGenerateContent(parts)
	if err != nil {
		return NLQParsedIntent{}, err
	}
	return parseNLQJSON(raw, query, domainHint), nil
}

func buildNLQGeminiPrompt(query, domainHint string, today, fyStart, fyEnd time.Time) string {
	q1e := fyStart.AddDate(0, 3, 0).AddDate(0, 0, -1)
	q2s := fyStart.AddDate(0, 3, 0)
	q2e := fyStart.AddDate(0, 6, 0).AddDate(0, 0, -1)
	q3s := fyStart.AddDate(0, 6, 0)
	q3e := fyStart.AddDate(0, 9, 0).AddDate(0, 0, -1)
	q4s := fyStart.AddDate(0, 9, 0)

	var b strings.Builder
	b.WriteString("You are a precise query parameter extractor for koko, a business intelligence assistant used by South Asian enterprises.\n\n")
	b.WriteString(fmt.Sprintf("Today: %s\n", today.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Current Indian FY: %s to %s\n", fyStart.Format("2006-01-02"), fyEnd.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Q1 (Apr-Jun): %s to %s\n", fyStart.Format("2006-01-02"), q1e.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Q2 (Jul-Sep): %s to %s\n", q2s.Format("2006-01-02"), q2e.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Q3 (Oct-Dec): %s to %s\n", q3s.Format("2006-01-02"), q3e.Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("Q4 (Jan-Mar): %s to %s\n\n", q4s.Format("2006-01-02"), fyEnd.Format("2006-01-02")))

	b.WriteString("Available domains in koko: industrial, electronics, revenue, consumer, all\n\n")

	b.WriteString("Expand the following acronyms and modern English phrases:\n")
	b.WriteString("Temporal:\n")
	b.WriteString("  FY / fiscal year → current FY date range\n")
	b.WriteString("  Q1/Q2/Q3/Q4 → FY quarter dates listed above\n")
	b.WriteString("  LW / last week / past week → 7 days before today\n")
	b.WriteString("  LM / last month / past month → previous calendar month\n")
	b.WriteString("  YTD / year to date → Jan 1 to today\n")
	b.WriteString("  MOM / month over month → same window last month\n")
	b.WriteString("  YOY / year over year → same window last year\n")
	b.WriteString("  'this week' → Mon to today of current week\n")
	b.WriteString("  'this month' → 1st to today of current month\n")
	b.WriteString("Domain hints:\n")
	b.WriteString("  GST / tax file / tax compliance / GSTIN → domain: revenue\n")
	b.WriteString("  P&L / PnL / profit and loss / income statement / financials → domain: revenue\n")
	b.WriteString("  gold / jewellery / jewelry / gems / bullion / hallmark / carats → domain: industrial\n")
	b.WriteString("  PCB / chips / components / gadgets / electronics / embedded → domain: electronics\n")
	b.WriteString("  sales / CRM / pipeline / leads / GTM / ARR / MRR → domain: revenue\n")
	b.WriteString("  consumer / personal / household / lifestyle → domain: consumer\n")
	b.WriteString("Intent:\n")
	b.WriteString("  summaries / summary / chat log / what was said / conversation / discussion → intent: summaries\n")
	b.WriteString("  actions / act prompts / suggested actions / AI suggested / koko:act / what koko suggested → intent: act_prompts\n")
	b.WriteString("  everything / both / all data → intent: both\n")
	b.WriteString("  retrieval verbs (show me, fetch, get, gimme, pull up, find, display, list) with 'summaries' → intent: summaries\n")
	b.WriteString("  retrieval verbs with 'actions/prompts' → intent: act_prompts\n\n")

	if strings.TrimSpace(domainHint) != "" {
		b.WriteString(fmt.Sprintf("User-provided domain hint: %s\n\n", domainHint))
	}

	b.WriteString(fmt.Sprintf("User query: %q\n\n", query))
	b.WriteString("Return ONLY a valid JSON object on a single line, no markdown, no explanation:\n")
	b.WriteString(`{"intent":"summaries|act_prompts|both|general","domain":"industrial|electronics|revenue|consumer|all|","group_name":"","from":"YYYY-MM-DD or empty","to":"YYYY-MM-DD or empty","page":1,"page_size":5,"plain_query":"cleaned query text"}`)
	b.WriteString("\n")
	return b.String()
}

// parseNLQJSON extracts the JSON object from Gemini's raw response.
func parseNLQJSON(raw, originalQuery, domainHint string) NLQParsedIntent {
	cleaned := strings.TrimSpace(raw)

	// Strip ```json ... ``` code fences if present
	if m := nlqJSONFence.FindStringSubmatch(cleaned); len(m) > 1 {
		cleaned = m[1]
	} else {
		// Find bare JSON object
		start := strings.Index(cleaned, "{")
		end := strings.LastIndex(cleaned, "}")
		if start >= 0 && end > start {
			cleaned = cleaned[start : end+1]
		}
	}

	var parsed NLQParsedIntent
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		log.Printf("NLQ_JSON_PARSE_FAILED: %v — raw=%s", err, truncateForLog(cleaned, 200))
		return NLQParsedIntent{
			Intent:     "general",
			Domain:     apiNormDomain(domainHint),
			Page:       1,
			PageSize:   apiMaxPageSize,
			PlainQuery: originalQuery,
		}
	}

	// Normalize outputs
	parsed.Domain = apiNormDomain(parsed.Domain)
	if parsed.Page <= 0 {
		parsed.Page = 1
	}
	if parsed.PageSize <= 0 || parsed.PageSize > apiMaxPageSize {
		parsed.PageSize = apiMaxPageSize
	}
	if strings.TrimSpace(parsed.PlainQuery) == "" {
		parsed.PlainQuery = originalQuery
	}
	// Validate intent
	switch parsed.Intent {
	case "summaries", "act_prompts", "both", "general":
		// valid
	default:
		parsed.Intent = "general"
	}
	return parsed
}

// ─────────────────────────────────────────────────────────────────────────────
// Indian FY helper (shared with mcp_server.go via same package)
// ─────────────────────────────────────────────────────────────────────────────

// currentIndianFY returns start (Apr 1) and end (Mar 31) of the FY that
// contains the given date. Indian FY runs April 1 – March 31.
func currentIndianFY(t time.Time) (time.Time, time.Time) {
	year := t.Year()
	if t.Month() < time.April {
		year-- // Jan–Mar belongs to the previous FY
	}
	start := time.Date(year, time.April, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, time.March, 31, 23, 59, 59, 0, time.UTC)
	return start, end
}
