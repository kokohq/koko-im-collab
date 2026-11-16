package main

// MCP (Model Context Protocol) server — JSON-RPC 2.0 over stateless HTTP.
//
// Endpoint:  POST /mcp   (protected by apiAuthMiddleware)
// Transport: Streamable HTTP — single request / single response (no SSE required)
// Protocol:  https://spec.modelcontextprotocol.io/specification/2024-11-05/
//
// Exposes three tools:
//   query_summaries       — paginated DB summaries with domain/date filters
//   query_act_prompts     — paginated koko:act AI prompts
//   natural_language_query — free-form English query, parsed by Gemini
//   get_koko_help         — returns the cached koko command menu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constatnts
// ─────────────────────────────────────────────────────────────────────────────

const (
	mcpProtocolVersion = "2024-11-05"
	mcpServerName      = "koko"
	mcpServerVersion   = "1.0.0"
	jsonRPCVersion     = "2.0"

	// JSON-RPC 2.0 standard error codes
	mcpErrParse          = -32700
	mcpErrInvalidRequest = -32600
	mcpErrMethodNotFound = -32601
	mcpErrInvalidParams  = -32602
	mcpErrInternal       = -32603
)

// ─────────────────────────────────────────────────────────────────────────────
// JSON-RPC wire types
// ─────────────────────────────────────────────────────────────────────────────

type mcpRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"` // int | string | null
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpRPCResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      interface{}  `json:"id"`
	Result  interface{}  `json:"result,omitempty"`
	Error   *mcpRPCError `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool schema types
// ─────────────────────────────────────────────────────────────────────────────

type mcpProperty struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Enum        []string    `json:"enum,omitempty"`
	Default     interface{} `json:"default,omitempty"`
}

type mcpInputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]mcpProperty `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema mcpInputSchema `json:"inputSchema"`
}

// Tool call result content (MCP spec §tools/call)
type mcpTextContent struct {
	Type string `json:"type"` // always "text"
	Text string `json:"text"`
}

type mcpCallToolResult struct {
	Content []mcpTextContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Main HTTP handler
// ─────────────────────────────────────────────────────────────────────────────

// handleMCP dispatches MCP JSON-RPC 2.0 messages to the appropriate tool.
// It is wrapped by apiAuthMiddleware before being registered in main.go.
func (h *WebhookHandler) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(mcpErrorResponse(nil, mcpErrParse, "request body read failed"))
		return
	}

	// Support JSON-RPC batch requests (array) or single objects
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(mcpErrorResponse(nil, mcpErrParse, "invalid JSON batch"))
			return
		}
		responses := make([]interface{}, 0, len(batch))
		for _, raw := range batch {
			var req mcpRPCRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				responses = append(responses, mcpErrorResponse(nil, mcpErrInvalidRequest, "invalid JSON-RPC object in batch"))
				continue
			}
			if resp, isNotification := h.dispatchMCP(r.Context(), key, req); !isNotification {
				responses = append(responses, resp)
			}
		}
		_ = json.NewEncoder(w).Encode(responses)
		return
	}

	var req mcpRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(mcpErrorResponse(nil, mcpErrParse, "invalid JSON"))
		return
	}

	// MCP notifications (id absent, method prefixed notifications/) expect 202 and no body
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		h.handleMCPNotification(req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp, _ := h.dispatchMCP(r.Context(), key, req)
	_ = json.NewEncoder(w).Encode(resp)
}

// dispatchMCP routes a single RPC request to the right handler.
// Returns (response, isNotification). isNotification means no response should be sent.
func (h *WebhookHandler) dispatchMCP(ctx context.Context, key KokoAPIKey, req mcpRPCRequest) (interface{}, bool) {
	if req.JSONRPC != jsonRPCVersion {
		return mcpErrorResponse(req.ID, mcpErrInvalidRequest, "jsonrpc must be '2.0'"), false
	}
	// Bare notifications have no id
	if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
		h.handleMCPNotification(req)
		return nil, true
	}
	switch req.Method {
	case "initialize":
		return h.mcpInitialize(req)
	case "ping":
		return mcpRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: map[string]interface{}{}}, false
	case "tools/list":
		return h.mcpToolsList(req)
	case "tools/call":
		return h.mcpToolsCall(ctx, key, req)
	default:
		return mcpErrorResponse(req.ID, mcpErrMethodNotFound, "method not found: "+req.Method), false
	}
}

func (h *WebhookHandler) handleMCPNotification(req mcpRPCRequest) {
	switch req.Method {
	case "notifications/initialized":
		log.Printf("MCP_INITIALIZED: client ready")
	case "notifications/cancelled":
		// no-op — cancel is best-effort
	default:
		log.Printf("MCP_NOTIFICATION_UNHANDLED: %s", req.Method)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// initialize
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) mcpInitialize(req mcpRPCRequest) (interface{}, bool) {
	result := map[string]interface{}{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    mcpServerName,
			"version": mcpServerVersion,
		},
		"instructions": "koko is a business intelligence assistant for South Asian enterprises. " +
			"Use query_summaries or query_act_prompts to query stored data by domain, group, and date. " +
			"Use natural_language_query for free-form English questions including acronyms like FY, GST, P&L, Q1-Q4, YTD, LW, LM.",
	}
	return mcpRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// tools/list
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) mcpToolsList(req mcpRPCRequest) (interface{}, bool) {
	domainEnums := []string{"industrial", "electronics", "revenue", "finance", "consumer", "accounting", "operations", "real_estate", "all", ""}
	sharedArgs := map[string]mcpProperty{
		"account_id": {Type: "string", Description: "Your koko account ID (required)"},
		"group_name": {Type: "string", Description: "Filter by group name (optional)"},
		"domain": {
			Type:        "string",
			Description: "Business domain filter: industrial, electronics, revenue, consumer, or all",
			Enum:        domainEnums,
		},
		"from":      {Type: "string", Description: "Start date — YYYY-MM-DD or RFC3339 (optional)"},
		"to":        {Type: "string", Description: "End date — YYYY-MM-DD or RFC3339 (optional)"},
		"page":      {Type: "integer", Description: "1-based page number (default 1)"},
		"page_size": {Type: "integer", Description: fmt.Sprintf("Results per page, max %d", apiMaxPageSize)},
	}

	tools := []mcpTool{
		{
			Name: "query_summaries",
			Description: "Retrieve paginated conversation summaries from koko. " +
				"Supports domain filters (industrial, electronics, revenue, consumer), group filters, " +
				"and date ranges including Indian FY quarters (Q1=Apr-Jun, Q2=Jul-Sep, Q3=Oct-Dec, Q4=Jan-Mar).",
			InputSchema: mcpInputSchema{
				Type:       "object",
				Properties: sharedArgs,
				Required:   []string{"account_id"},
			},
		},
		{
			Name: "query_act_prompts",
			Description: "Retrieve AI action prompts that koko:act generated for business groups. " +
				"Useful for auditing AI-suggested actions by domain, team, and time period.",
			InputSchema: mcpInputSchema{
				Type:       "object",
				Properties: sharedArgs,
				Required:   []string{"account_id"},
			},
		},
		{
			Name: "natural_language_query",
			Description: "Ask koko a free-form business intelligence question in plain English. " +
				"Understands South Asian business acronyms and modern language: FY / fiscal year, " +
				"Q1-Q4 (Indian quarters), GST, P&L / PnL, YTD, LW / last week, LM / last month, " +
				"MOM, YOY, gold / jewellery / bullion → industrial, and more. " +
				"Examples: \"show me last quarter's gold audit summaries\", " +
				"\"what actions were suggested for GST compliance this FY?\", " +
				"\"gimme act prompts for electronics team last month\".",
			InputSchema: mcpInputSchema{
				Type: "object",
				Properties: map[string]mcpProperty{
					"query": {
						Type: "string",
						Description: "Free-form English question or instruction, " +
							"e.g. 'show summaries for Q3' or 'what did koko:act suggest for revenue last week?'",
					},
					"account_id": {Type: "string", Description: "Your koko account ID"},
					"domain": {
						Type:        "string",
						Description: "Optional domain hint: industrial, electronics, revenue, consumer, all",
						Enum:        domainEnums,
					},
				},
				Required: []string{"query", "account_id"},
			},
		},
		{
			Name:        "revenue_insights",
			Description: "Domain toolset for revenue analytics (pipeline, GST, P&L, ARR/MRR signals).",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "finance_insights",
			Description: "Domain toolset for finance analytics (cashflow, AP/AR, ledger-oriented natural language queries).",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "industrial_insights",
			Description: "Domain toolset for industrial operations insights (incidents, maintenance, handover summaries).",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "accounting_placeholder",
			Description: "Placeholder toolset for accounting domain. Schema is stable; implementation can be upgraded without client changes.",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "operations_placeholder",
			Description: "Placeholder toolset for operations domain. Schema is stable; implementation can be upgraded without client changes.",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "real_estate_placeholder",
			Description: "Placeholder toolset for real-estate domain. Schema is stable; implementation can be upgraded without client changes.",
			InputSchema: mcpInputSchema{Type: "object", Properties: sharedArgs, Required: []string{"account_id"}},
		},
		{
			Name:        "get_koko_help",
			Description: "Get koko's full command reference — available commands, syntax, and examples.",
			InputSchema: mcpInputSchema{
				Type:       "object",
				Properties: map[string]mcpProperty{},
			},
		},
	}

	result := map[string]interface{}{"tools": tools}
	return mcpRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// tools/call
// ─────────────────────────────────────────────────────────────────────────────

type mcpToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func (h *WebhookHandler) mcpToolsCall(ctx context.Context, key KokoAPIKey, req mcpRPCRequest) (interface{}, bool) {
	var params mcpToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return mcpErrorResponse(req.ID, mcpErrInvalidParams, "invalid tools/call params: "+err.Error()), false
	}
	if params.Arguments == nil {
		params.Arguments = map[string]interface{}{}
	}

	var resultText string
	var toolErr error

	switch params.Name {
	case "query_summaries":
		resultText, toolErr = h.mcpQuerySummaries(ctx, key, params.Arguments)
	case "query_act_prompts":
		resultText, toolErr = h.mcpQueryActPrompts(ctx, key, params.Arguments)
	case "natural_language_query":
		resultText, toolErr = h.mcpNaturalLanguageQuery(ctx, key, params.Arguments)
	case "revenue_insights":
		params.Arguments["domain"] = featureModeRevenue
		resultText, toolErr = h.mcpQuerySummaries(ctx, key, params.Arguments)
	case "finance_insights":
		params.Arguments["domain"] = featureModeFinance
		resultText, toolErr = h.mcpNaturalLanguageQuery(ctx, key, params.Arguments)
	case "industrial_insights":
		params.Arguments["domain"] = featureModeIndustrial
		resultText, toolErr = h.mcpQuerySummaries(ctx, key, params.Arguments)
	case "accounting_placeholder":
		resultText = "Placeholder tool: accounting workflows are scaffolded and can be expanded with accounting-specific connectors."
	case "operations_placeholder":
		resultText = "Placeholder tool: operations workflows are scaffolded and can be expanded with operations-specific connectors."
	case "real_estate_placeholder":
		resultText = "Placeholder tool: real-estate workflows are scaffolded and can be expanded with real-estate-specific connectors."
	case "get_koko_help":
		resultText = h.cachedHelpText
	default:
		return mcpErrorResponse(req.ID, mcpErrMethodNotFound, "unknown tool: "+params.Name), false
	}

	if toolErr != nil {
		log.Printf("MCP_TOOL_ERROR: tool=%s account=%s error=%v", params.Name, key.AccountID, toolErr)
		result := mcpCallToolResult{
			Content: []mcpTextContent{{Type: "text", Text: "Error: " + toolErr.Error()}},
			IsError: true,
		}
		return mcpRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}, false
	}

	result := mcpCallToolResult{
		Content: []mcpTextContent{{Type: "text", Text: resultText}},
	}
	return mcpRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: result}, false
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool implementations
// ─────────────────────────────────────────────────────────────────────────────

func (h *WebhookHandler) mcpQuerySummaries(ctx context.Context, key KokoAPIKey, args map[string]interface{}) (string, error) {
	accountID := mcpStrArg(args, "account_id", key.AccountID)
	if accountID != key.AccountID {
		return "", fmt.Errorf("forbidden: account_id does not match your API key")
	}
	groupName := mcpStrArg(args, "group_name", "")
	domain := apiNormDomain(mcpStrArg(args, "domain", ""))
	from, to, err := parseAPIDateRange(mcpStrArg(args, "from", ""), mcpStrArg(args, "to", ""))
	if err != nil {
		return "", fmt.Errorf("invalid date: %w", err)
	}
	page := mcpIntArg(args, "page", 1)
	pageSize := mcpIntArg(args, "page_size", apiMaxPageSize)
	if pageSize > apiMaxPageSize {
		pageSize = apiMaxPageSize
	}
	if h.store == nil {
		return "", fmt.Errorf("store unavailable")
	}
	results, total, err := h.store.ListSummaries(ctx, accountID, groupName, domain, from, to, page, pageSize)
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}
	return mcpFormatSummaries(results, total, page, pageSize, groupName, domain), nil
}

func (h *WebhookHandler) mcpQueryActPrompts(ctx context.Context, key KokoAPIKey, args map[string]interface{}) (string, error) {
	accountID := mcpStrArg(args, "account_id", key.AccountID)
	if accountID != key.AccountID {
		return "", fmt.Errorf("forbidden: account_id does not match your API key")
	}
	groupName := mcpStrArg(args, "group_name", "")
	domain := apiNormDomain(mcpStrArg(args, "domain", ""))
	from, to, err := parseAPIDateRange(mcpStrArg(args, "from", ""), mcpStrArg(args, "to", ""))
	if err != nil {
		return "", fmt.Errorf("invalid date: %w", err)
	}
	page := mcpIntArg(args, "page", 1)
	pageSize := mcpIntArg(args, "page_size", apiMaxPageSize)
	if pageSize > apiMaxPageSize {
		pageSize = apiMaxPageSize
	}
	if h.store == nil {
		return "", fmt.Errorf("store unavailable")
	}
	results, total, err := h.store.ListActPrompts(ctx, accountID, groupName, domain, from, to, page, pageSize)
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}
	return mcpFormatActPrompts(results, total, page, pageSize, groupName, domain), nil
}

func (h *WebhookHandler) mcpNaturalLanguageQuery(ctx context.Context, key KokoAPIKey, args map[string]interface{}) (string, error) {
	query := strings.TrimSpace(mcpStrArg(args, "query", ""))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	accountID := mcpStrArg(args, "account_id", key.AccountID)
	if accountID != key.AccountID {
		return "", fmt.Errorf("forbidden: account_id does not match your API key")
	}
	domainHint := mcpStrArg(args, "domain", "")
	return h.executeNLQ(ctx, accountID, key.UserName, query, domainHint)
}

// ─────────────────────────────────────────────────────────────────────────────
// Result formatters shared with NLQ handler
// ─────────────────────────────────────────────────────────────────────────────

func mcpFormatSummaries(results []MessengerSummary, total, page, pageSize int, groupName, domain string) string {
	filter := mcpFilterLabel(groupName, domain)
	if len(results) == 0 {
		return fmt.Sprintf("No summaries found%s. Total: 0", filter)
	}
	lines := []string{
		fmt.Sprintf("Summaries%s — page %d, showing %d of %d total:", filter, page, len(results), total),
	}
	for i, s := range results {
		lines = append(lines,
			fmt.Sprintf("\n[%d] Domain: %s | Chat: %s | Messages: %d", i+1, s.Domain, s.ChatID, s.SourceMessageCount),
			fmt.Sprintf("    Period: %s → %s", s.WindowStart.UTC().Format("2006-01-02"), s.WindowEnd.UTC().Format("2006-01-02")),
			fmt.Sprintf("    %s", truncateForLog(s.SummaryText, 350)),
		)
	}
	return strings.Join(lines, "\n")
}

func mcpFormatActPrompts(results []KokoActPrompt, total, page, pageSize int, groupName, domain string) string {
	filter := mcpFilterLabel(groupName, domain)
	if len(results) == 0 {
		return fmt.Sprintf("No act prompts found%s. Total: 0", filter)
	}
	lines := []string{
		fmt.Sprintf("Act prompts%s — page %d, showing %d of %d total:", filter, page, len(results), total),
	}
	for i, a := range results {
		lines = append(lines,
			fmt.Sprintf("\n[%d] Group: %s | Domain: %s | By: %s | At: %s",
				i+1, a.GroupName, a.Domain, a.UserName, a.CreatedAt.UTC().Format("2006-01-02 15:04")),
			fmt.Sprintf("    %s", truncateForLog(a.PromptText, 350)),
		)
	}
	return strings.Join(lines, "\n")
}

func mcpFilterLabel(groupName, domain string) string {
	parts := []string{}
	if groupName != "" {
		parts = append(parts, "group="+groupName)
	}
	if domain != "" && domain != "all" {
		parts = append(parts, "domain="+domain)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// ─────────────────────────────────────────────────────────────────────────────
// Argument helpers for tools/call arguments map
// ─────────────────────────────────────────────────────────────────────────────

func mcpStrArg(args map[string]interface{}, key, def string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func mcpIntArg(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return def
		}
		return int(i)
	}
	return def
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper
// ─────────────────────────────────────────────────────────────────────────────

func mcpErrorResponse(id interface{}, code int, message string) mcpRPCResponse {
	return mcpRPCResponse{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &mcpRPCError{Code: code, Message: message},
	}
}
