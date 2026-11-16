package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type mcpClientInvokeRequest struct {
	AccountID        string                 `json:"account_id"`
	GroupName        string                 `json:"group_name"`
	Domain           string                 `json:"domain"`
	ToolName         string                 `json:"tool_name"`
	Arguments        map[string]interface{} `json:"arguments"`
	UseExternal      bool                   `json:"use_external"`
	ExternalMCPURL   string                 `json:"external_mcp_url"`
	ExternalToken    string                 `json:"external_bearer_token"`
	ExternalModel    string                 `json:"external_model"`
	LanguageCode     string                 `json:"language_code"`
	LearnAcronym     string                 `json:"learn_acronym"`
	AcronymExpanded  string                 `json:"acronym_expanded"`
	LearnPhrase      string                 `json:"learn_phrase"`
	PhraseNormalized string                 `json:"phrase_normalized"`
}

var mcpDomainDefaultTool = map[string]string{
	featureModeRevenue:    "revenue_insights",
	featureModeFinance:    "finance_insights",
	featureModeIndustrial: "industrial_insights",
	featureModeAccounting: "accounting_placeholder",
	featureModeOperations: "operations_placeholder",
	featureModeRealEstate: "real_estate_placeholder",
}

func (h *WebhookHandler) handleMCPClientInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req mcpClientInvokeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		accountID = key.AccountID
	}
	if accountID != key.AccountID {
		apiError(w, http.StatusForbidden, "account_id does not match your API key")
		return
	}

	domain := apiNormDomain(req.Domain)
	if domain == "" {
		domain = featureModeAll
	}
	groupName := strings.TrimSpace(req.GroupName)

	if strings.TrimSpace(req.LearnAcronym) != "" && strings.TrimSpace(req.AcronymExpanded) != "" {
		nlpLearnAcronym(domain, req.LearnAcronym, req.AcronymExpanded)
		if h.store != nil {
			_, _ = h.store.UpsertNLPAcronymExpansion(r.Context(), NLPAcronymExpansion{
				AccountID:    accountID,
				GroupName:    groupName,
				Domain:       domain,
				Locale:       req.LanguageCode,
				ShortForm:    req.LearnAcronym,
				ExpandedForm: req.AcronymExpanded,
				Source:       "mcp-client",
			})
			nlpInvalidateContextLearningCache(accountID, groupName)
		}
	}
	if strings.TrimSpace(req.LearnPhrase) != "" && strings.TrimSpace(req.PhraseNormalized) != "" {
		nlpLearnPhrase(domain, req.LearnPhrase, req.PhraseNormalized)
		if h.store != nil {
			_, _ = h.store.UpsertNLPPhraseRewrite(r.Context(), NLPPhraseRewrite{
				AccountID:        accountID,
				GroupName:        groupName,
				Domain:           domain,
				Locale:           req.LanguageCode,
				SourcePhrase:     req.LearnPhrase,
				NormalizedPhrase: req.PhraseNormalized,
				Source:           "mcp-client",
			})
			nlpInvalidateContextLearningCache(accountID, groupName)
		}
	}

	toolName := strings.TrimSpace(req.ToolName)
	if toolName == "" {
		toolName = mcpDomainDefaultTool[domain]
		if toolName == "" {
			toolName = "natural_language_query"
		}
	}

	args := req.Arguments
	if args == nil {
		args = map[string]interface{}{}
	}
	if _, ok := args["account_id"]; !ok {
		args["account_id"] = accountID
	}
	if _, ok := args["domain"]; !ok && domain != featureModeAll {
		args["domain"] = domain
	}
	if _, ok := args["group_name"]; !ok && groupName != "" {
		args["group_name"] = groupName
	}
	if strings.TrimSpace(req.ExternalModel) != "" {
		args["model_hint"] = strings.TrimSpace(req.ExternalModel)
	}
	if strings.TrimSpace(req.LanguageCode) != "" {
		args["language_code"] = strings.TrimSpace(req.LanguageCode)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	resultText := ""
	var err error
	if req.UseExternal && strings.TrimSpace(req.ExternalMCPURL) != "" {
		resultText, err = h.callExternalMCPTool(ctx, req.ExternalMCPURL, req.ExternalToken, toolName, args)
	} else {
		resultText, err = h.callInternalMCPTool(ctx, key, toolName, args)
	}
	if err != nil {
		apiError(w, http.StatusBadGateway, "mcp client invoke failed: "+err.Error())
		return
	}

	stampedResponse(w, http.StatusOK, ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   accountID,
		Domain:      domain,
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}, map[string]interface{}{
		"tool":          toolName,
		"domain":        domain,
		"used_external": req.UseExternal && strings.TrimSpace(req.ExternalMCPURL) != "",
		"result":        resultText,
	})
}

func (h *WebhookHandler) callInternalMCPTool(ctx context.Context, key KokoAPIKey, toolName string, args map[string]interface{}) (string, error) {
	switch toolName {
	case "query_summaries":
		return h.mcpQuerySummaries(ctx, key, args)
	case "query_act_prompts":
		return h.mcpQueryActPrompts(ctx, key, args)
	case "natural_language_query":
		return h.mcpNaturalLanguageQuery(ctx, key, args)
	case "get_koko_help":
		return h.cachedHelpText, nil
	case "revenue_insights":
		args["domain"] = featureModeRevenue
		return h.mcpQuerySummaries(ctx, key, args)
	case "finance_insights":
		args["domain"] = featureModeFinance
		return h.mcpNaturalLanguageQuery(ctx, key, args)
	case "industrial_insights":
		args["domain"] = featureModeIndustrial
		return h.mcpQuerySummaries(ctx, key, args)
	case "accounting_placeholder":
		return "Placeholder tool: accounting domain support is scaffolded and will be expanded with accounting-specific workflows.", nil
	case "operations_placeholder":
		return "Placeholder tool: operations domain support is scaffolded and will be expanded with operations workflows.", nil
	case "real_estate_placeholder":
		return "Placeholder tool: real-estate domain support is scaffolded and will be expanded with real-estate workflows.", nil
	default:
		return "", fmt.Errorf("unsupported internal MCP client tool: %s", toolName)
	}
}

func (h *WebhookHandler) callExternalMCPTool(ctx context.Context, endpoint, bearerToken, toolName string, args map[string]interface{}) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("external_mcp_url is required")
	}

	reqBody := map[string]interface{}{
		"jsonrpc": jsonRPCVersion,
		"id":      "koko-mcp-client-1",
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("external MCP returned %d: %s", resp.StatusCode, truncateForLog(string(rb), 600))
	}

	var parsed struct {
		Result struct {
			Content []mcpTextContent `json:"content"`
			IsError bool             `json:"isError"`
		} `json:"result"`
		Error *mcpRPCError `json:"error"`
	}
	if err := json.Unmarshal(rb, &parsed); err != nil {
		return "", err
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("external MCP error: %s", parsed.Error.Message)
	}
	if parsed.Result.IsError {
		return "", fmt.Errorf("external MCP tool returned isError=true")
	}
	parts := make([]string, 0, len(parsed.Result.Content))
	for _, c := range parsed.Result.Content {
		if strings.TrimSpace(c.Text) != "" {
			parts = append(parts, strings.TrimSpace(c.Text))
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("external MCP response had no text content")
	}
	return strings.Join(parts, "\n"), nil
}
