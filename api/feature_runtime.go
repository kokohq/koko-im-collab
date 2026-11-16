package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	telegramAnalyzeCommand     = "koko:analyze"
	telegramActCommand         = "koko:act"
	telegramGroupCommand       = "koko:group"
	telegramHelpCommand        = "koko:help"
	telegramPlanCommand        = "koko:plan"
	telegramAddGroupCommand    = "koko:addgroup"
	telegramDeleteGroupCommand = "koko:deletegroup"
	telegramAddUserCommand     = "koko:adduser"
	telegramDeleteUserCommand  = "koko:deleteuser"
)

var domainCodeToMode = map[string]string{
	"0":       featureModeAll,
	"1":       featureModeIndustrial,
	"2":       featureModeElectronics,
	"3":       featureModeRevenue,
	"4":       featureModeConsumer,
	"finance": "finance",
}

func mapDomainCodeOrName(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}

	if mode, ok := domainCodeToMode[normalized]; ok {
		return mode
	}

	knownDomains := map[string]bool{
		featureModeIndustrial:  true,
		featureModeElectronics: true,
		featureModeRevenue:     true,
		featureModeConsumer:    true,
		featureModeAll:         true,
		"finance":              true,
	}
	if knownDomains[normalized] {
		return normalized
	}

	return ""
}

type ParsedControlCommand struct {
	Name         string
	Argument     string
	Domain       string
	UserContext  string
	OutputMode   string // "", "ui", or "voice"
	HasDateRange bool
	DateStart    time.Time
	DateEnd      time.Time
}

type ChatCommandState struct {
	DefaultDomain                   string
	ActiveAccountID                 string
	ActiveGroupID                   string
	ActiveGroupName                 string
	LastSummary                     string
	LastSummaryDomain               string
	LastSummaryAt                   time.Time
	LastSummaryHasDateRange         bool
	LastSummaryStart                time.Time
	LastSummaryEnd                  time.Time
	LastAnalysis                    string
	LastAnalysisDomain              string
	LastAnalysisAt                  time.Time
	LastDataMessageAt               time.Time
	LastDataProcessedAt             time.Time
	AwaitingActConfirmation         bool
	PendingActCommand               ParsedControlCommand
	PendingActAuditID               int64 // ID of the koko_command_audit row for the pending koko:act, for confirmation update
	AwaitingDeleteGroupConfirmation bool
	PendingDeleteGroupName          string
	PendingDeleteGroupAccountID     string
}

func trimKokoPrefix(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < len("koko:") {
		return "", false
	}

	if !strings.EqualFold(trimmed[:len("koko:")], "koko:") {
		return "", false
	}

	return strings.TrimSpace(trimmed[len("koko:"):]), true
}

func normalizedDomainKey(domain string) string {
	mapped := mapDomainCodeOrName(domain)
	if mapped == "" {
		return featureModeAll
	}
	return mapped
}

func canonicalCommandName(name string) string {
	switch name {
	case telegramSummarizeLegacy, telegramSummaryAlias:
		return telegramSummarizeCommand
	default:
		return name
	}
}

func buildIssuedCommandLabel(control ParsedControlCommand) string {
	name := canonicalCommandName(control.Name)
	if name == "" {
		return "koko:"
	}

	parts := []string{name}
	if strings.TrimSpace(control.Domain) != "" {
		parts = append(parts, control.Domain)
	}
	if control.OutputMode == "ui" || control.OutputMode == "voice" {
		parts = append(parts, control.OutputMode)
	}
	if control.HasDateRange {
		parts = append(parts, fmt.Sprintf("range:%s..%s", control.DateStart.Format("2006-01-02"), control.DateEnd.Add(-24*time.Hour).Format("2006-01-02")))
	}
	if strings.TrimSpace(control.UserContext) != "" {
		parts = append(parts, "context: "+strings.TrimSpace(control.UserContext))
	}
	if strings.TrimSpace(control.Argument) != "" && (name == telegramHelpCommand || name == telegramGroupCommand || name == telegramPlanCommand ||
		name == telegramAddGroupCommand || name == telegramDeleteGroupCommand ||
		name == telegramAddUserCommand || name == telegramDeleteUserCommand) {
		parts = append(parts, strings.TrimSpace(control.Argument))
	}
	return strings.Join(parts, " ")
}

func buildKokoPromptFooter() string {
	return "── koko: · 1 summarize · 2 analyze · 3 act · 4 plan · 9 addGroup · 10 deleteGroup · 11 addUser · 12 deleteUser · append ui or voice for rich output"
}

func formatKokoCommandResponse(control ParsedControlCommand, body string) string {
	sections := []string{buildIssuedCommandLabel(control)}
	if strings.TrimSpace(body) != "" {
		sections = append(sections, strings.TrimSpace(body))
	}
	sections = append(sections, buildKokoPromptFooter())
	return strings.Join(sections, "\n\n")
}

func (h *WebhookHandler) sendCommandResultToGroup(chatID int64, requester string, control ParsedControlCommand, body string) error {
	messageBody := strings.TrimSpace(body)
	if control.Name != telegramHelpCommand {
		requester = strings.TrimSpace(requester)
		if requester != "" {
			messageBody = "Requested by: " + requester + "\n\n" + messageBody
		}
	}
	return h.sendTextToChatID(chatID, formatKokoCommandResponse(control, messageBody))
}

func parseTelegramControlCommand(text string) ParsedControlCommand {
	raw := strings.TrimSpace(text)
	normalized := strings.ToLower(raw)
	if raw == "" {
		return ParsedControlCommand{}
	}

	if cmd, ok := numericCommandToControl(raw); ok {
		return cmd
	}
	if stripped, ok := trimKokoPrefix(raw); ok {
		// Bare "koko:" (any case) with nothing after → help
		if strings.TrimSpace(stripped) == "" {
			return ParsedControlCommand{Name: telegramHelpCommand}
		}
		if cmd, numeric := numericCommandToControl(stripped); numeric {
			return cmd
		}
	}

	switch normalized {
	case telegramSummarizeCommand:
		return ParsedControlCommand{Name: telegramSummarizeCommand}
	case telegramSummaryAlias:
		return ParsedControlCommand{Name: telegramSummarizeCommand}
	case telegramSummarizeLegacy:
		return ParsedControlCommand{Name: telegramSummarizeCommand}
	case telegramAnalyzeCommand:
		return ParsedControlCommand{Name: telegramAnalyzeCommand}
	case telegramActCommand:
		return ParsedControlCommand{Name: telegramActCommand}
	case telegramGroupCommand:
		return ParsedControlCommand{Name: telegramGroupCommand}
	case telegramHelpCommand:
		return ParsedControlCommand{Name: telegramHelpCommand}
	case telegramPlanCommand:
		return ParsedControlCommand{Name: telegramPlanCommand}
	case telegramAddGroupCommand:
		return ParsedControlCommand{Name: telegramAddGroupCommand}
	case telegramDeleteGroupCommand:
		return ParsedControlCommand{Name: telegramDeleteGroupCommand}
	case telegramAddUserCommand:
		return ParsedControlCommand{Name: telegramAddUserCommand}
	case telegramDeleteUserCommand:
		return ParsedControlCommand{Name: telegramDeleteUserCommand}
	default:
		if inferred := nlpInterpretKokoCommand(raw, ""); inferred.Command != "" && inferred.Confidence >= 0.65 {
			control := ParsedControlCommand{Name: inferred.Command, Domain: inferred.Domain, Argument: strings.TrimSpace(inferred.Argument)}
			if inferred.Command == telegramSummarizeCommand || inferred.Command == telegramAnalyzeCommand || inferred.Command == telegramActCommand {
				opts := parseCommandOptions(strings.TrimSpace(inferred.ExpandedText))
				opts.Name = inferred.Command
				if control.Domain != "" {
					opts.Domain = control.Domain
				}
				if control.Argument != "" {
					opts.Argument = control.Argument
				}
				return opts
			}
			return control
		}

		argCommands := []string{
			telegramSummarizeCommand, telegramSummaryAlias, telegramSummarizeLegacy,
			telegramAnalyzeCommand, telegramActCommand,
			telegramGroupCommand, telegramPlanCommand,
			telegramAddGroupCommand, telegramDeleteGroupCommand,
			telegramAddUserCommand, telegramDeleteUserCommand,
		}
		for _, cmd := range argCommands {
			cmdLower := strings.ToLower(cmd)
			if strings.HasPrefix(normalized, cmdLower+" ") {
				argRaw := strings.TrimSpace(raw[len(cmd):])
				switch cmd {
				case telegramGroupCommand, telegramPlanCommand,
					telegramAddGroupCommand, telegramDeleteGroupCommand,
					telegramAddUserCommand, telegramDeleteUserCommand:
					return ParsedControlCommand{Name: cmd, Argument: argRaw}
				}
				opts := parseCommandOptions(argRaw)
				canonical := cmd
				if cmd == telegramSummarizeLegacy || cmd == telegramSummaryAlias {
					canonical = telegramSummarizeCommand
				}
				opts.Name = canonical
				return opts
			}
		}

		if strings.HasPrefix(normalized, telegramHelpCommand+" ") {
			arg := strings.TrimSpace(strings.TrimPrefix(normalized, strings.ToLower(telegramHelpCommand)))
			return ParsedControlCommand{Name: telegramHelpCommand, Argument: arg}
		}
		return ParsedControlCommand{}
	}
}

func numericCommandToControl(value string) (ParsedControlCommand, bool) {
	normalized := strings.TrimSpace(strings.ToLower(value))
	parts := strings.Fields(normalized)
	if len(parts) == 0 {
		return ParsedControlCommand{}, false
	}

	command := parts[0]
	argRaw := ""
	if len(parts) > 1 {
		argRaw = strings.TrimSpace(value[len(parts[0]):])
	}

	switch command {
	case "1":
		opts := parseCommandOptions(argRaw)
		opts.Name = telegramSummarizeCommand
		return opts, true
	case "2":
		opts := parseCommandOptions(argRaw)
		opts.Name = telegramAnalyzeCommand
		return opts, true
	case "3":
		opts := parseCommandOptions(argRaw)
		opts.Name = telegramActCommand
		return opts, true
	case "4":
		return ParsedControlCommand{Name: telegramHelpCommand, Argument: strings.TrimSpace(argRaw)}, true
	case "5":
		return ParsedControlCommand{Name: telegramHelpCommand, Argument: featureModeIndustrial}, true
	case "6":
		return ParsedControlCommand{Name: telegramHelpCommand, Argument: featureModeElectronics}, true
	case "7":
		return ParsedControlCommand{Name: telegramHelpCommand, Argument: featureModeRevenue}, true
	case "8":
		return ParsedControlCommand{Name: telegramHelpCommand, Argument: featureModeConsumer}, true
	case "9":
		return ParsedControlCommand{Name: telegramAddGroupCommand, Argument: strings.TrimSpace(argRaw)}, true
	case "10":
		return ParsedControlCommand{Name: telegramDeleteGroupCommand, Argument: strings.TrimSpace(argRaw)}, true
	case "11":
		return ParsedControlCommand{Name: telegramAddUserCommand, Argument: strings.TrimSpace(argRaw)}, true
	case "12":
		return ParsedControlCommand{Name: telegramDeleteUserCommand, Argument: strings.TrimSpace(argRaw)}, true
	default:
		return ParsedControlCommand{}, false
	}
}

func parseCommandOptions(raw string) ParsedControlCommand {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ParsedControlCommand{}
	}

	// Extract output mode token (ui / voice) — it can appear anywhere in the arg list.
	outputMode := ""
	remaining := trimmed
	{
		parts := strings.Fields(remaining)
		kept := make([]string, 0, len(parts))
		for _, p := range parts {
			switch strings.ToLower(p) {
			case "ui":
				outputMode = "ui"
			case "voice":
				outputMode = "voice"
			default:
				kept = append(kept, p)
			}
		}
		remaining = strings.Join(kept, " ")
	}

	context := ""
	prefix := remaining
	lower := strings.ToLower(remaining)
	if idx := strings.Index(lower, "context:"); idx >= 0 {
		prefix = strings.TrimSpace(remaining[:idx])
		context = strings.TrimSpace(remaining[idx+len("context:"):])
	}

	prefixParts := strings.Fields(prefix)
	domain := ""
	if len(prefixParts) > 0 {
		mappedDomain := mapDomainCodeOrName(prefixParts[0])
		if mappedDomain != "" {
			domain = mappedDomain
			prefix = strings.TrimSpace(prefix[len(prefixParts[0]):])
		}
	}

	hasRange, start, end, rangeRemainder := parseDateRange(prefix)
	if context == "" {
		context = strings.TrimSpace(rangeRemainder)
	}

	return ParsedControlCommand{
		Domain:       domain,
		OutputMode:   outputMode,
		UserContext:  context,
		HasDateRange: hasRange,
		DateStart:    start,
		DateEnd:      end,
	}
}

func parseDateRange(input string) (bool, time.Time, time.Time, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return false, time.Time{}, time.Time{}, ""
	}

	lower := strings.ToLower(trimmed)
	if idx := strings.Index(lower, "range:"); idx >= 0 {
		after := strings.TrimSpace(trimmed[idx+len("range:"):])
		rangeToken := after
		remainder := ""
		if cut := strings.IndexAny(after, " \t\n"); cut >= 0 {
			rangeToken = strings.TrimSpace(after[:cut])
			remainder = strings.TrimSpace(after[cut:])
		}

		if has, start, end := parseDateRangeToken(rangeToken); has {
			before := strings.TrimSpace(trimmed[:idx])
			combined := strings.TrimSpace(before + " " + remainder)
			return true, start, end, strings.TrimSpace(combined)
		}
	}

	fromIdx := strings.Index(lower, "from:")
	toIdx := strings.Index(lower, "to:")
	if fromIdx >= 0 && toIdx > fromIdx {
		fromValue := strings.TrimSpace(trimmed[fromIdx+len("from:") : toIdx])
		toValue := strings.TrimSpace(trimmed[toIdx+len("to:"):])
		toToken := toValue
		remainder := ""
		if cut := strings.IndexAny(toValue, " \t\n"); cut >= 0 {
			toToken = strings.TrimSpace(toValue[:cut])
			remainder = strings.TrimSpace(toValue[cut:])
		}

		startDate, err1 := time.Parse("2006-01-02", fromValue)
		endDate, err2 := time.Parse("2006-01-02", toToken)
		startRel, startRelOk := ParseRelativeDate(fromValue)
		endRel, endRelOk := ParseRelativeDate(toToken)
		var start, end time.Time
		var parsedOk bool
		if err1 == nil && err2 == nil {
			start = startDate.UTC()
			end = endDate.UTC().Add(24 * time.Hour)
			parsedOk = true
		} else if startRelOk && endRelOk {
			start = startRel
			end = endRel
			parsedOk = true
		} else if err1 == nil && endRelOk {
			start = startDate.UTC()
			end = endRel
			parsedOk = true
		} else if startRelOk && err2 == nil {
			start = startRel
			end = endDate.UTC().Add(24 * time.Hour)
			parsedOk = true
		}
		if parsedOk {
			// Validate and swap if dates are backwards
			if start.After(end) {
				start, end = end, start
			}
			// Ensure end is after start
			if !end.After(start) {
				return false, time.Time{}, time.Time{}, trimmed
			}
			before := strings.TrimSpace(trimmed[:fromIdx])
			combined := strings.TrimSpace(before + " " + remainder)
			return true, start, end, strings.TrimSpace(combined)
		}
	}

	// Two space-separated tokens: treat as START END
	// e.g. "now-2d now", "2024-01-01 2024-01-31", "now-7d now-1d"
	fields := strings.Fields(trimmed)
	if len(fields) == 2 {
		var start, end time.Time
		var startOk, endOk bool
		if rel, ok := ParseRelativeDate(fields[0]); ok {
			start, startOk = rel, true
		} else if t, err := time.Parse("2006-01-02", fields[0]); err == nil {
			start, startOk = t.UTC(), true
		}
		if startOk {
			if rel, ok := ParseRelativeDate(fields[1]); ok {
				end, endOk = rel, true
			} else if t, err := time.Parse("2006-01-02", fields[1]); err == nil {
				end, endOk = t.UTC().Add(24*time.Hour), true
			}
		}
		if startOk && endOk {
			// Validate and swap if dates are backwards
			if start.After(end) {
				start, end = end, start
			}
			// Ensure end is after start
			if !end.After(start) {
				return false, time.Time{}, time.Time{}, trimmed
			}
			return true, start, end, ""
		}
	}

	return false, time.Time{}, time.Time{}, trimmed
}

func parseDateRangeToken(token string) (bool, time.Time, time.Time) {
	t := strings.TrimSpace(token)
	if t == "" {
		return false, time.Time{}, time.Time{}
	}

	// Single relative date: "now-2d" etc → (now-2d, now)
	if rel, ok := ParseRelativeDate(t); ok {
		now := time.Now().UTC()
		return true, rel, now
	}

	if strings.Contains(t, "..") {
		parts := strings.SplitN(t, "..", 2)
		if len(parts) != 2 {
			return false, time.Time{}, time.Time{}
		}
		leftStr := strings.TrimSpace(parts[0])
		rightStr := strings.TrimSpace(parts[1])

		// Try relative date on each side first, then YYYY-MM-DD.
		var start, end time.Time
		if rel, ok := ParseRelativeDate(leftStr); ok {
			start = rel
		} else if d, err := time.Parse("2006-01-02", leftStr); err == nil {
			start = d.UTC()
		} else {
			return false, time.Time{}, time.Time{}
		}
		if rel, ok := ParseRelativeDate(rightStr); ok {
			end = rel
		} else if d, err := time.Parse("2006-01-02", rightStr); err == nil {
			end = d.UTC().Add(24 * time.Hour)
		} else {
			return false, time.Time{}, time.Time{}
		}

		// Validate and swap if dates are backwards
		if start.After(end) {
			start, end = end, start
		}

		// Ensure end is at least after start
		if !end.After(start) {
			return false, time.Time{}, time.Time{}
		}

		return true, start, end
	}

	singleDate, err := time.Parse("2006-01-02", t)
	if err != nil {
		return false, time.Time{}, time.Time{}
	}
	return true, singleDate.UTC(), singleDate.UTC().Add(24 * time.Hour)
}

func looksLikeKokoPrefixedInput(text string) bool {
	// Require the colon — bare "koko" text without ":" is a normal group message.
	_, ok := trimKokoPrefix(text)
	return ok
}

func extractKokoNaturalLanguageQuery(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}

	stripped, ok := trimKokoPrefix(trimmed)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(stripped) == "" {
		return "", false
	}
	if control := parseTelegramControlCommand(trimmed); control.Name != "" {
		return "", false
	}
	return strings.TrimSpace(stripped), true
}

func parseYesNo(text string) (bool, bool) {
	if control := parseTelegramControlCommand(text); control.Name == telegramActCommand {
		return true, false
	}

	normalized := strings.ToLower(strings.TrimSpace(text))
	switch normalized {
	case "yes", "y", "yeah", "yep", "yup", "sure", "ok", "okay", "confirm", "go", "proceed", "do it", "run it", "execute":
		return true, false
	case "no", "n", "nope", "nah", "cancel", "stop", "abort", "reject", "decline", "skip":
		return false, true
	}
	return false, false
}

func (h *WebhookHandler) getOrCreateChatState(chatID int64) *ChatCommandState {
	h.chatStateMu.Lock()
	defer h.chatStateMu.Unlock()

	state, ok := h.chatStateByChat[chatID]
	if !ok {
		state = &ChatCommandState{}
		h.chatStateByChat[chatID] = state
	}

	return state
}

func (h *WebhookHandler) applyCommandDefaults(chatID int64, control ParsedControlCommand) ParsedControlCommand {
	state := h.getOrCreateChatState(chatID)
	if control.Domain != "" {
		state.DefaultDomain = control.Domain
	} else if state.DefaultDomain != "" {
		control.Domain = state.DefaultDomain
	}
	return control
}

func (h *WebhookHandler) featureModulesForControl(control ParsedControlCommand) []FeatureModule {
	domain := normalizedDomainKey(control.Domain)
	if domain == featureModeAll {
		return h.featureModules
	}

	selected := make([]FeatureModule, 0, 1)
	for _, module := range h.featureModules {
		if module.Mode() == domain {
			selected = append(selected, module)
		}
	}

	return selected
}

func (h *WebhookHandler) handlePendingActConfirmation(chatID int64, userText string) (bool, error) {
	state := h.getOrCreateChatState(chatID)
	if !state.AwaitingActConfirmation {
		return false, nil
	}

	yes, no := parseYesNo(userText)
	if !yes && !no {
		_ = h.sendTextToChatID(chatID, "Reply yes or no to continue (you can also say: sure, ok, nope, cancel).")
		return true, nil
	}

	if no {
		// Record declined confirmation in audit
		if h.store != nil && state.PendingActAuditID != 0 {
			_ = h.store.UpdateKokoCommandAuditConfirmation(context.Background(), state.PendingActAuditID, "declined")
		}
		state.AwaitingActConfirmation = false
		state.PendingActCommand = ParsedControlCommand{}
		state.PendingActAuditID = 0
		return true, h.sendTextToChatID(chatID, "Act canceled.")
	}

	// Record confirmed in audit
	if h.store != nil && state.PendingActAuditID != 0 {
		_ = h.store.UpdateKokoCommandAuditConfirmation(context.Background(), state.PendingActAuditID, "confirmed")
	}
	pending := state.PendingActCommand
	auditID := state.PendingActAuditID
	state.AwaitingActConfirmation = false
	state.PendingActCommand = ParsedControlCommand{}
	state.PendingActAuditID = 0
	_ = auditID // already recorded above; runFeatureAct with confirmed=true won't create a duplicate since PendingActAuditID is now 0
	return true, h.runFeatureAct(chatID, pending, "", true)
}

func (h *WebhookHandler) detectControlCommandFromAudio(msg ProcessedMessage) (string, error) {
	if msg.Type != "audio" || msg.MediaID == "" {
		return "", nil
	}

	mediaURL, err := h.getTelegramFileDownloadURL(msg.MediaID)
	if err != nil {
		return "", err
	}
	mediaBytes, err := h.downloadTelegramMedia(mediaURL)
	if err != nil {
		return "", err
	}
	if len(mediaBytes) == 0 {
		return "", fmt.Errorf("audio command media is empty")
	}
	if len(mediaBytes) > maxVertexAIInlineBytes {
		return "", fmt.Errorf("audio command media exceeds inline limit")
	}

	mimeType := defaultOrValue(msg.MimeType, defaultTelegramMimeType(msg.Type))
	prompt := strings.Join([]string{
		"Detect the user's intent from this audio and respond with exactly one of the following:",
		"Support native spoken English accents from UK, Australia, US, and India (South Asian English).",
		"For Indian English: handle retroflex consonants, different vowel timings, and common Indian English expressions (e.g., 'only' used differently, 'is it?' for 'right?').",
		"Map spelling/wording variants (summarise/summarize, analyse/analyze) and official domain acronyms (GST, VAT, HMRC, ATO, IRS, AP, AR, P&L).",
		"- If the user says a koko command (summarize, analyze, act, help) or a shortcut number, return ONLY that command token: koko:summarize koko:analyze koko:act koko:help 1 2 3 4",
		"- If the user says 'koko:' followed by a question, query, or any other natural language (not one of the commands above), return the FULL verbatim transcription of what was said, starting with 'koko:'.",
		"- If neither, return 'none'.",
	}, "\n")

	resp, err := h.callVertexAIGenerateContent([]map[string]interface{}{
		{"text": prompt},
		{
			"inline_data": map[string]string{
				"mime_type": mimeType,
				"data":      base64Encode(mediaBytes),
			},
		},
	})
	if err != nil {
		return "", err
	}

	firstLine := strings.TrimSpace(strings.Split(resp, "\n")[0])
	candidate := strings.ToLower(firstLine)
	switch {
	case strings.Contains(candidate, telegramSummarizeCommand):
		return telegramSummarizeCommand, nil
	case strings.Contains(candidate, telegramSummarizeLegacy):
		return telegramSummarizeCommand, nil
	case strings.Contains(candidate, telegramAnalyzeCommand):
		return telegramAnalyzeCommand, nil
	case strings.Contains(candidate, telegramActCommand):
		return telegramActCommand, nil
	case strings.Contains(candidate, telegramHelpCommand):
		return telegramHelpCommand, nil
	case candidate == "1":
		return "1", nil
	case candidate == "2":
		return "2", nil
	case candidate == "3":
		return "3", nil
	case candidate == "4":
		return "4", nil
	default:
		// If the transcription starts with "koko:" it's an NLQ query from audio.
		// Return the first line of the actual response (not lowercased) so the
		// caller can route it through the NLQ assistant.
		if _, ok := trimKokoPrefix(firstLine); ok {
			if strings.TrimSpace(firstLine) != "" {
				return firstLine, nil
			}
		}
		return "", nil
	}
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func (h *WebhookHandler) runFeatureAnalyze(chatID int64, control ParsedControlCommand, requester string, accountID string, activeGroup Group) error {
	// Resolve effective domain: prefer explicit; fall back to group-level last domain
	effectiveDomain := normalizedDomainKey(control.Domain)
	if (effectiveDomain == featureModeAll || strings.TrimSpace(control.Domain) == "") && strings.TrimSpace(accountID) != "" {
		if groupDomain := h.getGroupLastDomain(accountID, activeGroup.GroupName); groupDomain != "" && groupDomain != featureModeAll {
			effectiveDomain = groupDomain
		}
	}
	effectiveControl := control
	effectiveControl.Domain = effectiveDomain

	// Persist the domain at group level so koko:act can use it later
	if effectiveDomain != "" && effectiveDomain != featureModeAll {
		h.setGroupLastDomain(accountID, activeGroup.GroupName, effectiveDomain)
	}

	ctx, err := h.buildFeatureContext(chatID, effectiveControl, false)
	if err != nil {
		return err
	}

	modules := h.featureModulesForControl(effectiveControl)
	if len(modules) == 0 {
		return h.sendCommandResultToGroup(chatID, requester, effectiveControl, "No feature modules are available for domain: "+effectiveDomain)
	}

	sections := []string{"koko:analyze result"}
	for _, module := range modules {
		analysis := module.Analyze(ctx)
		actions := module.PlanActions(ctx)
		sections = append(sections,
			module.Title()+":\n"+analysis+"\n"+buildActionPlanText(module, actions)+"\n"+buildFlowChartText(module),
		)
	}

	result := strings.Join(sections, "\n\n")
	// Record domain metadata only (no text) so koko:act can infer domain.
	state := h.getOrCreateChatState(chatID)
	state.LastAnalysisDomain = effectiveDomain
	state.LastAnalysisAt = time.Now().UTC()

	if err := h.sendCommandResultToGroup(chatID, requester, effectiveControl, result); err != nil {
		return err
	}
	// Broadcast analyze result to other group members
	if strings.TrimSpace(accountID) != "" && strings.TrimSpace(activeGroup.GroupName) != "" {
		kokoResp := formatKokoCommandResponse(effectiveControl, "Requested by "+requester+"\n\n"+result)
		h.broadcastToGroupByMessenger(accountID, activeGroup.GroupName, h.messengerForChat(chatID), fmt.Sprintf("%d", chatID), "From "+requester+":\n\n"+kokoResp)
	}
	// Enqueue rich media (ui/voice) rendering – handled here to avoid storing result text in state.
	if (control.OutputMode == "ui" || control.OutputMode == "voice") && strings.TrimSpace(result) != "" {
		richReq := &RichMediaRequest{
			ChatIDs:     []int64{chatID},
			Requesters:  []string{requester},
			AccountID:   accountID,
			GroupName:   activeGroup.GroupName,
			CommandName: telegramAnalyzeCommand,
			TextContent: result,
			OutputMode:  control.OutputMode,
		}
		senderKey := requester + "/" + fmt.Sprintf("%d", chatID)
		if _, ackMsg := h.richMediaQueue.TryEnqueue(richReq, senderKey); ackMsg != "" {
			_ = h.sendTextToChatID(chatID, ackMsg)
		}
	}
	return nil
}

func (h *WebhookHandler) runFeatureAct(chatID int64, control ParsedControlCommand, requester string, confirmed bool) error {
	state := h.getOrCreateChatState(chatID)

	// Infer effective domain from last summarize/analyze when no explicit domain given
	effectiveDomain := normalizedDomainKey(control.Domain)
	if effectiveDomain == featureModeAll {
		if !state.LastSummaryAt.IsZero() || !state.LastAnalysisAt.IsZero() {
			if !state.LastAnalysisAt.IsZero() && state.LastAnalysisAt.After(state.LastSummaryAt) {
				effectiveDomain = normalizedDomainKey(state.LastAnalysisDomain)
			} else if state.LastSummaryDomain != "" {
				effectiveDomain = normalizedDomainKey(state.LastSummaryDomain)
			}
		}
	}
	// Last resort: fall back to the group-level last domain used by any member
	if effectiveDomain == featureModeAll && strings.TrimSpace(state.ActiveAccountID) != "" {
		if groupDomain := h.getGroupLastDomain(state.ActiveAccountID, state.ActiveGroupName); groupDomain != "" && groupDomain != featureModeAll {
			effectiveDomain = groupDomain
		}
	}
	effectiveControl := control
	effectiveControl.Domain = effectiveDomain

	hasMatchingSummary := state.LastSummary != "" && normalizedDomainKey(state.LastSummaryDomain) == effectiveDomain
	hasMatchingAnalysis := state.LastAnalysis != "" && normalizedDomainKey(state.LastAnalysisDomain) == effectiveDomain
	if !confirmed && (!hasMatchingSummary || !hasMatchingAnalysis) {
		ctx, err := h.buildFeatureContext(chatID, effectiveControl, false)
		if err != nil {
			return err
		}

		modules := h.featureModulesForControl(effectiveControl)
		if len(modules) == 0 {
			return h.sendCommandResultToGroup(chatID, requester, effectiveControl, "No feature modules are available for domain: "+effectiveDomain)
		}

		// Record audit entry for this koko:act request (confirmation pending)
		if h.store != nil {
			audit, auditErr := h.store.RecordKokoCommandAudit(context.Background(), KokoCommandAudit{
				AccountID:   state.ActiveAccountID,
				GroupID:     state.ActiveGroupID,
				GroupName:   state.ActiveGroupName,
				CommandName: telegramActCommand,
				RequestedBy: requester,
				RequestedAt: time.Now().UTC(),
			})
			if auditErr == nil {
				state.PendingActAuditID = audit.ID
			}
		}

		preview := []string{
			"Act pre-check — no prior summarize/analyze found, generated fresh context.",
			"Summary:\n" + ctx.Summary,
		}

		analysisSections := []string{}
		for _, module := range modules {
			analysisSections = append(analysisSections, module.Title()+":\n"+module.Analyze(ctx))
		}
		if len(analysisSections) > 0 {
			preview = append(preview, "Analysis:\n"+strings.Join(analysisSections, "\n\n"))
		}
		preview = append(preview, "Reply Yes or No to proceed.")

		state.AwaitingActConfirmation = true
		state.PendingActCommand = effectiveControl
		return h.sendTextToChatID(chatID, strings.Join(preview, "\n\n"))
	}

	// Record audit for directly-confirmed (or first-time confirmed) act
	if h.store != nil && state.PendingActAuditID == 0 {
		_, _ = h.store.RecordKokoCommandAudit(context.Background(), KokoCommandAudit{
			AccountID:   state.ActiveAccountID,
			GroupID:     state.ActiveGroupID,
			GroupName:   state.ActiveGroupName,
			CommandName: telegramActCommand,
			RequestedBy: requester,
			RequestedAt: time.Now().UTC(),
		})
	}

	ctx, err := h.buildFeatureContext(chatID, effectiveControl, true)
	if err != nil {
		return err
	}

	modules := h.featureModulesForControl(effectiveControl)
	if len(modules) == 0 {
		return h.sendTextToChatID(chatID, "No feature modules are available for domain: "+effectiveDomain)
	}

	promptSections := []string{}
	for _, module := range modules {
		actions := module.PlanActions(ctx)
		if len(actions) == 0 {
			continue
		}
		moduleAnalysis := module.Analyze(ctx)
		for _, action := range actions {
			h.executeDownstreamAction(module.Mode(), action)
		}
		agentPrompt := buildEnterpriseAgentPrompt(
			module,
			actions,
			ctx.Summary,
			moduleAnalysis,
			state.ActiveAccountID,
			state.ActiveGroupName,
			effectiveControl.UserContext,
		)
		promptSections = append(promptSections, agentPrompt)
	}

	if len(promptSections) == 0 {
		return h.sendTextToChatID(chatID, "No actions could be generated from the current context.")
	}

	// The AI prompt section is the primary output; send it to the requester
	agentPromptOutput := strings.Join(promptSections, "\n\n"+strings.Repeat("─", 48)+"\n\n")
	if err := h.sendTextToChatID(chatID, agentPromptOutput); err != nil {
		return err
	}

	// Broadcast to other group members: separate metadata header from AI prompt data
	if strings.TrimSpace(state.ActiveAccountID) != "" && strings.TrimSpace(state.ActiveGroupName) != "" {
		meta := "From " + requester + " (koko:act — domain: " + effectiveDomain + "):"
		h.broadcastToGroupByMessenger(state.ActiveAccountID, state.ActiveGroupName, h.messengerForChat(chatID), fmt.Sprintf("%d", chatID),
			meta+"\n\n"+strings.Repeat("─", 48)+"\n\n"+agentPromptOutput)
	}

	// Persist the AI prompt to DB
	if h.store != nil && strings.TrimSpace(state.ActiveAccountID) != "" {
		_, saveErr := h.store.SaveActPrompt(context.Background(), KokoActPrompt{
			AccountID:  state.ActiveAccountID,
			UserName:   requester,
			GroupID:    state.ActiveGroupID,
			GroupName:  state.ActiveGroupName,
			Domain:     effectiveDomain,
			PromptText: agentPromptOutput,
			CreatedAt:  time.Now().UTC(),
		})
		if saveErr != nil {
			log.Printf("ACT_PROMPT_SAVE_FAILED: account=%s group=%s error=%v", state.ActiveAccountID, state.ActiveGroupName, saveErr)
		}
	}

	// Enqueue rich media (ui/voice) rendering on top of the text result if requested.
	if (control.OutputMode == "ui" || control.OutputMode == "voice") && strings.TrimSpace(agentPromptOutput) != "" {
		richReq := &RichMediaRequest{
			ChatIDs:     []int64{chatID},
			Requesters:  []string{requester},
			AccountID:   state.ActiveAccountID,
			GroupName:   state.ActiveGroupName,
			CommandName: telegramActCommand,
			TextContent: agentPromptOutput,
			OutputMode:  control.OutputMode,
		}
		senderKey := requester + "/" + fmt.Sprintf("%d", chatID)
		if _, ackMsg := h.richMediaQueue.TryEnqueue(richReq, senderKey); ackMsg != "" {
			_ = h.sendTextToChatID(chatID, ackMsg)
		}
	}
	return nil
}

func (h *WebhookHandler) runFeatureHelp(chatID int64, domain string) error {
	trimmedDomain := strings.ToLower(strings.TrimSpace(domain))
	trimmedDomain = mapDomainCodeOrName(trimmedDomain)
	return h.sendTextToChatID(chatID, buildKokoHelpText(trimmedDomain))
}

func (h *WebhookHandler) runFeatureGroupCommand(chatID int64, requester string, control ParsedControlCommand) error {
	groupRef := strings.TrimSpace(control.Argument)
	if groupRef == "" {
		return h.sendCommandResultToGroup(chatID, requester, control,
			"Usage: koko:group <group_name_or_id>\n\n"+
				"Switches your active workspace to the named group.\n"+
				"Groups are your team workspaces (e.g. demo_account_a, sales_team).\n"+
				"They are NOT the same as domains (industrial/revenue/electronics/consumer).\n\n"+
				"Example:  koko:group demo_account_a")
	}

	messengerType := h.messengerForChat(chatID)
	accountID, groups, err := h.lookupUserGroupsByMessenger(requester, messengerType)
	if err != nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Unable to verify your group memberships right now. Please try again.")
	}
	if strings.TrimSpace(accountID) == "" || len(groups) == 0 {
		return h.sendCommandResultToGroup(chatID, requester, control, "Access denied: your user is not registered to any account/group.")
	}

	matchedGroup, found := matchGroupReference(groups, groupRef)
	if !found {
		available := []string{}
		for _, group := range groups {
			entry := group.GroupName
			if strings.TrimSpace(group.GroupID) != "" {
				entry += " [ID: " + group.GroupID + "]"
			}
			available = append(available, entry)
		}
		msg := "Group \"" + groupRef + "\" not found.\n\n" +
			"⚠️ Reminder: groups are team workspaces (e.g. demo_account_a).\n" +
			"   Domains (industrial/revenue/electronics/consumer) are NOT group names.\n\n" +
			"Your groups:\n  • " + strings.Join(available, "\n  • ")
		return h.sendCommandResultToGroup(chatID, requester, control, msg)
	}

	state := h.getOrCreateChatState(chatID)
	state.ActiveAccountID = matchedGroup.AccountID
	state.ActiveGroupID = matchedGroup.GroupID
	state.ActiveGroupName = matchedGroup.GroupName

	response := "Active group set to: " + matchedGroup.GroupName
	if strings.TrimSpace(matchedGroup.GroupID) != "" {
		response += " (" + matchedGroup.GroupID + ")"
	}
	if strings.TrimSpace(matchedGroup.AccountID) != "" {
		response += "\nAccount: " + matchedGroup.AccountID
	}

	return h.sendCommandResultToGroup(chatID, requester, control, response)
}

// runFeatureAddGroup creates a new group with the given name and adds the
// requesting user as its first member. messengerType is the originating platform.
func (h *WebhookHandler) runFeatureAddGroup(chatID int64, requester, accountID, messengerType string, control ParsedControlCommand) error {
	groupName := strings.TrimSpace(control.Argument)
	if groupName == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "Usage: koko:addGroup <groupname>")
	}
	if h.store == nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group management is not available right now.")
	}

	// Create the group.
	newGroup, err := h.store.UpsertGroup(context.Background(), Group{
		AccountID:     accountID,
		GroupName:     groupName,
		MessengerType: messengerType,
		AddedAt:       time.Now().UTC(),
	})
	if err != nil {
		log.Printf("ADD_GROUP_UPSERT_FAILED: account=%s group=%s error=%v", accountID, groupName, err)
		return h.sendCommandResultToGroup(chatID, requester, control, "Failed to create group. Please try again.")
	}

	// Look up the requesting user to get their user ID so we can add them as a member.
	user, uErr := h.store.FindUserInAccount(context.Background(), accountID, requester, messengerType)
	if uErr != nil {
		log.Printf("ADD_GROUP_REQUESTER_LOOKUP_FAILED: account=%s user=%s error=%v", accountID, requester, uErr)
		// Group was still created — report partial success.
		return h.sendCommandResultToGroup(chatID, requester, control,
			"Group \""+newGroup.GroupName+"\" created, but could not add you as a member automatically (user not found).")
	}

	_, mErr := h.store.AddGroupMembership(context.Background(), newGroup.ID, user.ID, time.Now().UTC())
	if mErr != nil {
		log.Printf("ADD_GROUP_MEMBERSHIP_FAILED: account=%s group=%s user=%s error=%v", accountID, groupName, requester, mErr)
		return h.sendCommandResultToGroup(chatID, requester, control,
			"Group \""+newGroup.GroupName+"\" created, but could not add you as a member automatically.")
	}

	return h.sendCommandResultToGroup(chatID, requester, control,
		"Group \""+newGroup.GroupName+"\" created and you have been added as a member.")
}

// runFeatureDeleteGroup initiates a delete-group flow.  On Telegram the user
// is asked to confirm interactively; on other platforms the caller may pass
// "confirm" as the Argument suffix to bypass the prompt.
func (h *WebhookHandler) runFeatureDeleteGroup(chatID int64, requester, accountID string, control ParsedControlCommand) error {
	arg := strings.TrimSpace(control.Argument)
	// Strip a trailing " confirm" token (for non-interactive callers).
	confirmed := false
	argLower := strings.ToLower(arg)
	if strings.HasSuffix(argLower, " confirm") {
		arg = strings.TrimSpace(arg[:len(arg)-len(" confirm")])
		confirmed = true
	}

	groupName := arg
	if groupName == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "Usage: koko:deleteGroup <groupname>  (add 'confirm' to skip the confirmation prompt)")
	}
	if h.store == nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group management is not available right now.")
	}

	// Verify the group exists.
	_, gErr := h.store.FindGroupInAccount(context.Background(), accountID, groupName)
	if gErr != nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group \""+groupName+"\" not found in your account.")
	}

	if confirmed || chatID == 0 {
		return h.executeDeleteGroup(chatID, requester, accountID, groupName, control)
	}

	// Interactive confirmation for Telegram (chatID != 0).
	state := h.getOrCreateChatState(chatID)
	state.AwaitingDeleteGroupConfirmation = true
	state.PendingDeleteGroupName = groupName
	state.PendingDeleteGroupAccountID = accountID

	warning := strings.Join([]string{
		"\u26a0\ufe0f Confirm: Delete group \"" + groupName + "\"?",
		"",
		"Consequences:",
		"\u2022 All group members will lose access immediately.",
		"\u2022 All group message history and summaries will be permanently deleted.",
		"\u2022 This action cannot be undone.",
		"",
		"Reply yes to confirm or no to cancel.",
	}, "\n")
	return h.sendTextToChatID(chatID, warning)
}

// executeDeleteGroup performs the actual group deletion after confirmation.
func (h *WebhookHandler) executeDeleteGroup(chatID int64, requester, accountID, groupName string, control ParsedControlCommand) error {
	if err := h.store.DeleteGroup(context.Background(), accountID, groupName); err != nil {
		log.Printf("DELETE_GROUP_FAILED: account=%s group=%s error=%v", accountID, groupName, err)
		if chatID != 0 {
			return h.sendCommandResultToGroup(chatID, requester, control, "Failed to delete group \""+groupName+"\". Please try again.")
		}
		return err
	}
	log.Printf("DELETE_GROUP_OK: account=%s group=%s by=%s", accountID, groupName, requester)
	if chatID != 0 {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group \""+groupName+"\" has been deleted.")
	}
	return nil
}

// handlePendingDeleteGroupConfirmation handles a Telegram user's yes/no reply
// to a pending koko:deleteGroup confirmation prompt.
func (h *WebhookHandler) handlePendingDeleteGroupConfirmation(chatID int64, userText string) (bool, error) {
	state := h.getOrCreateChatState(chatID)
	if !state.AwaitingDeleteGroupConfirmation {
		return false, nil
	}

	yes, no := parseYesNo(userText)
	if !yes && !no {
		_ = h.sendTextToChatID(chatID, "Reply yes or no to continue (you can also say: sure, ok, nope, cancel).")
		return true, nil
	}

	groupName := state.PendingDeleteGroupName
	accountID := state.PendingDeleteGroupAccountID
	state.AwaitingDeleteGroupConfirmation = false
	state.PendingDeleteGroupName = ""
	state.PendingDeleteGroupAccountID = ""

	if no {
		return true, h.sendTextToChatID(chatID, "Group deletion canceled.")
	}

	ctrl := ParsedControlCommand{Name: telegramDeleteGroupCommand, Argument: groupName}
	return true, h.executeDeleteGroup(chatID, "", accountID, groupName, ctrl)
}

// runFeatureAddUser adds a named user to the requester's active group.
func (h *WebhookHandler) runFeatureAddUser(chatID int64, requester, accountID, messengerType string, control ParsedControlCommand) error {
	targetUser := strings.TrimSpace(control.Argument)
	if targetUser == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "Usage: koko:addUser <username>")
	}
	if h.store == nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group management is not available right now.")
	}

	state := h.getOrCreateChatState(chatID)
	activeGroupName := strings.TrimSpace(state.ActiveGroupName)
	if activeGroupName == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "No active group selected. Use koko:group <name> to select a group first.")
	}

	// Resolve the group row.
	grp, gErr := h.store.FindGroupInAccount(context.Background(), accountID, activeGroupName)
	if gErr != nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Active group \""+activeGroupName+"\" not found. Use koko:group <name> to reselect.")
	}

	// Resolve the target user — auto-create with group's messenger_type if they
	// don't yet exist in this account (e.g. admin is pre-registering a new user
	// by name before they send their first message).
	user, uErr := h.store.FindUserInAccount(context.Background(), accountID, targetUser, "")
	if uErr != nil {
		// Auto-create the user with messenger_type inherited from the group.
		newUser, cErr := h.store.UpsertUser(context.Background(), User{
			AccountID:     accountID,
			UserName:      targetUser,
			MessengerType: grp.MessengerType,
		})
		if cErr != nil {
			log.Printf("ADD_USER_AUTO_CREATE_FAILED: account=%s user=%s error=%v", accountID, targetUser, cErr)
			return h.sendCommandResultToGroup(chatID, requester, control, "User \""+targetUser+"\" not found and could not be created. Check the username and try again.")
		}
		user = newUser
	}

	if _, err := h.store.AddGroupMembership(context.Background(), grp.ID, user.ID, time.Now().UTC()); err != nil {
		log.Printf("ADD_USER_MEMBERSHIP_FAILED: account=%s group=%s user=%s error=%v", accountID, activeGroupName, targetUser, err)
		return h.sendCommandResultToGroup(chatID, requester, control, "Failed to add user. Please try again.")
	}

	return h.sendCommandResultToGroup(chatID, requester, control,
		"User \""+user.UserName+"\" added to group \""+activeGroupName+"\".")
}

// runFeatureDeleteUser removes a named user from the requester's active group.
func (h *WebhookHandler) runFeatureDeleteUser(chatID int64, requester, accountID string, control ParsedControlCommand) error {
	targetUser := strings.TrimSpace(control.Argument)
	if targetUser == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "Usage: koko:deleteUser <username>")
	}
	if h.store == nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Group management is not available right now.")
	}

	state := h.getOrCreateChatState(chatID)
	activeGroupName := strings.TrimSpace(state.ActiveGroupName)
	if activeGroupName == "" {
		return h.sendCommandResultToGroup(chatID, requester, control, "No active group selected. Use koko:group <name> to select a group first.")
	}

	if err := h.store.RemoveGroupMembership(context.Background(), accountID, activeGroupName, targetUser); err != nil {
		log.Printf("DELETE_USER_MEMBERSHIP_FAILED: account=%s group=%s user=%s error=%v", accountID, activeGroupName, targetUser, err)
		return h.sendCommandResultToGroup(chatID, requester, control, "Failed to remove user. Please try again.")
	}

	return h.sendCommandResultToGroup(chatID, requester, control,
		"User \""+targetUser+"\" removed from group \""+activeGroupName+"\".")
}

// lookupUserGroupsByMessenger resolves accountID and groups for a user on any messenger platform.
func (h *WebhookHandler) lookupUserGroupsByMessenger(userName, messengerType string) (string, []Group, error) {
	if h.store == nil || strings.TrimSpace(userName) == "" {
		return "", nil, nil
	}
	return h.store.LookupUserGroupContext(context.Background(), userName, messengerType)
}

func buildKokoHelpText(domain string) string {
	sep := strings.Repeat("━", 50)
	sep2 := strings.Repeat("─", 50)
	lines := []string{
		sep,
		"🤖  koko  ·  AI Operations Assistant",
		"🎯 Turn conversations into insights, actions, and decisions",
		sep,
		"",
		"💡 WHAT IS KOKO?",
		"  • Summarize: Get quick digest of group messages",
		"  • Analyze: Deep insights + recommendations",
		"  • Act: Execute on recommendations",
		"  • Query: Ask anything about your data",
		"  • Learn: Audio, text, video supported",
		"",
		sep2,
		"📋 MAIN COMMANDS",
		"",
		" 1 or summarize",
		"   📊 Get a digest of recent group messages",
		"   Example: koko:summarize or just: 1",
		"",
		" 2 or analyze",
		"   🔍 Deep insights with action recommendations",
		"   Example: koko:analyze or just: 2",
		"",
		" 3 or act",
		"   ✅ Execute on recommendations from analyze",
		"   Example: koko:act or just: 3",
		"",
		"🤖 Ask AI: koko:<your question>",
		"   📝 Natural language query about your data",
		"   Example: koko:what decisions did we make?",
		"",
		sep2,
		"👥 WORKSPACE MANAGEMENT",
		"",
		" 9 addGroup <name>",
		"10 deleteGroup <name>",
		"11 addUser <name or phone>",
		"12 deleteUser <name or phone>",
		"",
		sep2,
		"🎤 AUDIO MODE GUIDE (Perfect for mobile!)",
		"",
		"  ✅ HOW TO USE AUDIO:",
		"   • Send voice message / audio file to koko",
		"   • Speak naturally in your accent (Indian, US, UK, AU all ok!)",
		"   • Koko transcribes and processes automatically",
		"",
		"  💬 AUDIO COMMAND EXAMPLES:",
		"   • Voice: 'Koko, summarize'",
		"   • Voice: 'Koko, analyze the revenue'",
		"   • Voice: 'What did we decide?'",
		"",
		"  📝 AUDIO MESSAGE TIPS:",
		"   • Speak clearly (no need to be perfect!)",
		"   • Use natural pausing between thoughts",
		"   • Koko understands Indian English accents perfectly",
		"   • Short messages work best (30-60 seconds)",
		"",
		sep2,
		"🗂️ DOMAIN FILTER (optional scope)",
		"   0 = all  1 = industrial  2 = electronics  3 = revenue  4 = consumer",
		"   Example: koko:analyze 3  (analyze revenue domain)",
		"",
		"📅 DATE RANGES  (required format: START END)",
		"  now-2d now               →  last 2 days",
		"  from:now-1w to:now       →  last 7 days",
		"",
		"ℹ️ For detailed instructions, visit koko app home page",
		sep,
	}

	body := strings.Join(lines, "\n")

	if strings.TrimSpace(domain) == "" {
		return body
	}

	details := map[string]string{
		featureModeIndustrial:  "⚙️  Industrial — shift handover, maintenance priorities, safety escalation.",
		featureModeElectronics: "🔌 Electronics — validation tracking, debug tickets, program risk.",
		featureModeRevenue:     "💰 Revenue — delivery blockers, customer risk, escalation reporting.",
		featureModeConsumer:    "👤 Consumer — personal task extraction, reminders, life-admin.",
	}

	if detail, ok := details[domain]; ok {
		return body + "\n\n" + detail
	}

	available := []string{
		featureModeIndustrial, featureModeElectronics, featureModeRevenue, featureModeConsumer,
	}
	return body + "\n\nUnknown domain: " + domain + "\nAvailable domains: " + strings.Join(available, " · ")
}

// buildKokoTermsAndConditions returns the one-time terms-of-use notice that is
// sent to every new user on their first koko interaction and included in admin
// API responses when an account or user is created/registered.
func buildKokoTermsAndConditions() string {
	sep := strings.Repeat("━", 44)
	return strings.Join([]string{
		sep,
		"KOKO APPLICATION — TERMS OF USE",
		sep,
		"By using koko you accept the following terms.",
		"",
		"DATA PROTECTION",
		"• All data you submit is encrypted at rest (AES-256) and in",
		"  transit (TLS/HTTPS).",
		"• Source messages are permanently and irreversibly deleted",
		"  once summarised. Summary records may be retained for a",
		"  limited period for operational and audit purposes.",
		"",
		"USER RESPONSIBILITY",
		"• You are solely responsible for all content you submit.",
		"• The koko application and its operators expressly disclaim",
		"  all legal responsibility for User-generated content.",
		"",
		"ACCEPTABLE USE",
		"• You must not post content that is harmful, threatening,",
		"  abusive, illegal, or that infringes any third-party rights.",
		"• Submission of plagiarised, copyrighted, or otherwise",
		"  misappropriated material is strictly prohibited.",
		"• Accounts or groups found in violation of these policies",
		"  may be suspended immediately and without prior notice.",
		"",
		"DISCLAIMER OF LIABILITY",
		"THE SERVICE IS PROVIDED \"AS IS\" WITHOUT WARRANTY OF ANY",
		"KIND. TO THE FULLEST EXTENT PERMITTED BY APPLICABLE LAW,",
		"KOKO'S OPERATORS SHALL NOT BE LIABLE FOR ANY INDIRECT,",
		"INCIDENTAL, SPECIAL, OR CONSEQUENTIAL DAMAGES ARISING",
		"FROM USE OF THIS APPLICATION.",
		"",
		"Continued use of koko constitutes your acceptance of these terms.",
		sep,
	}, "\n")
}

func buildKokoCommandMenu() string {
	return buildKokoHelpText("")
}

func buildKokoCommandMenuWithContext(accountID string, groups []Group) string {
	sep := strings.Repeat("━", 50)
	sep2 := strings.Repeat("─", 50)
	lines := []string{
		sep,
		"🤖  koko  ·  AI Operations Assistant",
		"🎯 Turn conversations into insights, actions, and decisions",
		sep,
	}

	// What Koko Does
	lines = append(lines, "")
	lines = append(lines, "💡 WHAT IS KOKO?")
	lines = append(lines, "  • Summarize: Get quick digest of group messages")
	lines = append(lines, "  • Analyze: Deep insights + recommendations")
	lines = append(lines, "  • Act: Execute on recommendations")
	lines = append(lines, "  • Query: Ask anything about your data")
	lines = append(lines, "  • Learn: Audio, text, video supported")

	if strings.TrimSpace(accountID) != "" {
		lines = append(lines, "")
		lines = append(lines, "🏢 Account: "+accountID)
	}

	if len(groups) > 0 {
		lines = append(lines, "")
		lines = append(lines, "👥 YOUR WORKSPACES")
		for _, g := range groups {
			entry := "  • " + g.GroupName
			if strings.TrimSpace(g.GroupID) != "" {
				entry += "  [" + g.GroupID + "]"
			}
			lines = append(lines, entry)
		}
		lines = append(lines, "")
		lines = append(lines, "  📌 Switch workspace: koko:group <name>")
		if len(groups) > 0 {
			lines = append(lines, "     Example: koko:group "+groups[0].GroupName)
		}
	}

	// All Available Commands
	lines = append(lines, "")
	lines = append(lines, sep2)
	lines = append(lines, "📋 MAIN COMMANDS")
	lines = append(lines, "")
	lines = append(lines, " 1 or summarize")
	lines = append(lines, "   📊 Get a digest of recent group messages")
	lines = append(lines, "   Best for: What happened? Quick update? Key decisions?")
	lines = append(lines, "   Example: koko:summarize or just: 1")
	lines = append(lines, "")
	lines = append(lines, " 2 or analyze")
	lines = append(lines, "   🔍 Deep insights with action recommendations")
	lines = append(lines, "   Best for: Why did this happen? What should we do?")
	lines = append(lines, "   Example: koko:analyze or just: 2")
	lines = append(lines, "")
	lines = append(lines, " 3 or act")
	lines = append(lines, "   ✅ Execute on recommendations from analyze")
	lines = append(lines, "   Best for: Do it! Confirm and implement actions")
	lines = append(lines, "   Example: koko:act or just: 3")
	lines = append(lines, "")
	lines = append(lines, "🤖 Ask AI: koko:<your question>")
	lines = append(lines, "   📝 Natural language query about your data")
	lines = append(lines, "   Best for: Anything you want to know")
	lines = append(lines, "   Example: koko:what decisions did we make?")
	lines = append(lines, "")

	// Group Management
	lines = append(lines, sep2)
	lines = append(lines, "👥 WORKSPACE MANAGEMENT")
	lines = append(lines, "")
	lines = append(lines, " 9 addGroup <name>")
	lines = append(lines, "   ➕ Create a new workspace/group")
	lines = append(lines, "   Example: 9 sales team")
	lines = append(lines, "")
	lines = append(lines, "10 deleteGroup <name>")
	lines = append(lines, "   🗑️ Remove a workspace")
	lines = append(lines, "   Example: 10 old projects")
	lines = append(lines, "")
	lines = append(lines, "11 addUser <name or phone>")
	lines = append(lines, "   👤 Add member to active workspace")
	lines = append(lines, "   Example: 11 john@company.com")
	lines = append(lines, "")
	lines = append(lines, "12 deleteUser <name or phone>")
	lines = append(lines, "   🚫 Remove member from workspace")
	lines = append(lines, "   Example: 12 john@company.com")
	lines = append(lines, "")

	// Domains
	lines = append(lines, sep2)
	lines = append(lines, "🗂️ DOMAIN FILTER (optional scope)")
	lines = append(lines, "   Use domain to focus analysis on specific area")
	lines = append(lines, "")
	lines = append(lines, "   0 = all          (everything)")
	lines = append(lines, "   1 = industrial   (operations, manufacturing)")
	lines = append(lines, "   2 = electronics  (hardware, components)")
	lines = append(lines, "   3 = revenue      (sales, finance, margins)")
	lines = append(lines, "   4 = consumer     (customer, experience, support)")
	lines = append(lines, "")
	lines = append(lines, "   Example: koko:analyze 3  (analyze revenue domain)")
	lines = append(lines, "")

	// Audio Mode Guide
	lines = append(lines, sep2)
	lines = append(lines, "🎤 AUDIO MODE GUIDE (Perfect for mobile!)")
	lines = append(lines, "")
	lines = append(lines, "  ✅ HOW TO USE AUDIO:")
	lines = append(lines, "   • Send voice message / audio file to koko")
	lines = append(lines, "   • Speak naturally in your accent (Indian, US, UK, AU all ok!)")
	lines = append(lines, "   • Koko transcribes and processes automatically")
	lines = append(lines, "")
	lines = append(lines, "  💬 AUDIO COMMAND EXAMPLES:")
	lines = append(lines, "   • Voice: 'Koko, summarize' → Same as typing 'koko:summarize'")
	lines = append(lines, "   • Voice: 'Koko, analyze the revenue' → Analytics on revenue data")
	lines = append(lines, "   • Voice: 'What did we decide?' → AI query")
	lines = append(lines, "")
	lines = append(lines, "  📝 AUDIO MESSAGE TIPS:")
	lines = append(lines, "   • Speak clearly (no need to be perfect!)")
	lines = append(lines, "   • Use natural pausing between thoughts")
	lines = append(lines, "   • Koko understands Indian English accents perfectly")
	lines = append(lines, "   • Short messages work best (30-60 seconds)")
	lines = append(lines, "")

	// Output Formats
	lines = append(lines, sep2)
	lines = append(lines, "📤 OUTPUT FORMATS")
	lines = append(lines, "   • Text chat: Inline message (default)")
	lines = append(lines, "   • Image: Visual summary (JPEG)")
	lines = append(lines, "   • Audio: Voice response (MP3)")
	lines = append(lines, "   • Custom: Flow chart, table, chart, etc.")
	lines = append(lines, "")

	// Quick Tips
	lines = append(lines, sep2)
	lines = append(lines, "⚡ QUICK TIPS FOR NEW USERS")
	lines = append(lines, "")
	lines = append(lines, "  Start simple:")
	lines = append(lines, "   1. Send a message to your workspace")
	lines = append(lines, "   2. Try: koko:summarize (to get digest)")
	lines = append(lines, "   3. Try: koko:analyze (to get insights)")
	lines = append(lines, "")
	lines = append(lines, "  Audio-first workflow:")
	lines = append(lines, "   • Send voice notes during meetings")
	lines = append(lines, "   • Koko transcribes and stores them")
	lines = append(lines, "   • Later, summarize/analyze the audio")
	lines = append(lines, "")
	lines = append(lines, "  Work across domains:")
	lines = append(lines, "   • Use 'koko:analyze 1' for industrial focus")
	lines = append(lines, "   • Use 'koko:analyze 3' for revenue focus")
	lines = append(lines, "   • Use 'koko:analyze 0' to see everything")
	lines = append(lines, "")

	lines = append(lines, sep)

	return strings.Join(lines, "\n")
}

func formatUserGroupMembershipResponse(accountID string, groups []Group) string {
	lines := []string{}
	if strings.TrimSpace(accountID) != "" {
		lines = append(lines, "Your Account: "+accountID)
	}
	if len(groups) == 0 {
		lines = append(lines, "No groups are currently linked to your Telegram user.")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "Groups you are added to:")
	for _, group := range groups {
		entry := "- " + group.GroupName
		if strings.TrimSpace(group.GroupID) != "" {
			entry += " (" + group.GroupID + ")"
		}
		lines = append(lines, entry)
	}
	return strings.Join(lines, "\n")
}

func isUserGroupListQuery(query string) bool {
	return buildKokoAssistantQueryPlan(query).Intent == kokoAssistantIntentListGroups
}

func isAccountIDQuery(query string) bool {
	return buildKokoAssistantQueryPlan(query).Intent == kokoAssistantIntentAccountID
}

func isActiveDomainQuery(query string) bool {
	return buildKokoAssistantQueryPlan(query).Intent == kokoAssistantIntentActiveDomain
}

func isShowActiveGroupQuery(query string) bool {
	return buildKokoAssistantQueryPlan(query).Intent == kokoAssistantIntentShowActiveGroup
}

func isClearActiveGroupQuery(query string) bool {
	return buildKokoAssistantQueryPlan(query).Intent == kokoAssistantIntentClearActiveGrp
}

func extractSwitchGroupReference(query string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return "", false
	}
	isBoundary := func(s string, idx int) bool {
		if idx <= 0 {
			return true
		}
		ch := s[idx-1]
		return !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_')
	}

	prefixes := []string{
		"switch to group ",
		"switch group to ",
		"switch group ",
		"change group to ",
		"set group to ",
		"set group ",
		"use group ",
		"select group ",
	}

	for _, prefix := range prefixes {
		idx := strings.Index(normalized, prefix)
		if idx < 0 {
			continue
		}
		if !isBoundary(normalized, idx) {
			continue
		}
		start := idx + len(prefix)
		if start >= len(query) {
			continue
		}
		ref := strings.TrimSpace(query[start:])
		ref = strings.Trim(ref, " \t\n\r\"'.,!?")
		if ref != "" {
			return ref, true
		}
	}

	patterns := []string{
		`(?i)\b(?:switch|change|set|use|select|activate)\b.*\bgroup\b(?:\s+to)?\s+([a-z0-9_\- ]+)`,
		`(?i)\b(?:switch|change|set|use|select|activate)\s+(?:to\s+)?([a-z0-9_\- ]+)\s+group\b`,
		`(?i)\bmake\s+([a-z0-9_\- ]+?)\s+(?:my\s+)?active\s+group\b`,
	}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(strings.TrimSpace(query))
		if len(matches) < 2 {
			continue
		}
		candidate := strings.TrimSpace(matches[1])
		candidate = strings.Trim(candidate, " \t\n\r\"'.,!?:;")
		if candidate != "" {
			return candidate, true
		}
	}

	return "", false
}

func matchGroupReference(groups []Group, ref string) (Group, bool) {
	normalizedRef := strings.ToLower(strings.TrimSpace(ref))
	if normalizedRef == "" {
		return Group{}, false
	}

	for _, group := range groups {
		if strings.EqualFold(strings.TrimSpace(group.GroupID), normalizedRef) {
			return group, true
		}
		if strings.EqualFold(strings.TrimSpace(group.GroupName), normalizedRef) {
			return group, true
		}
	}

	partialMatches := []Group{}
	for _, group := range groups {
		if strings.Contains(strings.ToLower(group.GroupName), normalizedRef) || strings.Contains(strings.ToLower(group.GroupID), normalizedRef) {
			partialMatches = append(partialMatches, group)
		}
	}
	if len(partialMatches) == 1 {
		return partialMatches[0], true
	}

	return Group{}, false
}

func (h *WebhookHandler) lookupTelegramUserGroups(userName string) (string, []Group, error) {
	if h.store == nil || strings.TrimSpace(userName) == "" {
		return "", nil, nil
	}
	return h.store.LookupUserGroupContext(context.Background(), userName, "telegram")
}

func (h *WebhookHandler) buildTelegramCommandMenuForUser(userName string) string {
	if h.store == nil || strings.TrimSpace(userName) == "" {
		if h.cachedHelpText != "" {
			return h.cachedHelpText
		}
		return buildKokoCommandMenu()
	}

	accountID, groups, err := h.lookupTelegramUserGroups(userName)
	if err != nil {
		log.Printf("TELEGRAM_CONTEXT_MENU_LOOKUP_FAILED: user=%s error=%v", userName, err)
		if h.cachedHelpText != "" {
			return h.cachedHelpText
		}
		return buildKokoCommandMenu()
	}
	if accountID == "" {
		if h.cachedHelpText != "" {
			return h.cachedHelpText
		}
		return buildKokoCommandMenu()
	}
	return buildKokoCommandMenuWithContext(accountID, groups)
}

func (h *WebhookHandler) sendKokoMenuIfFirstMessage(chatID int64) error {
	if chatID == 0 {
		return nil
	}

	h.menuMu.Lock()
	alreadySent := h.menuSentByChat[chatID]
	if !alreadySent {
		h.menuSentByChat[chatID] = true
	}
	h.menuMu.Unlock()

	if alreadySent {
		return nil
	}

	return h.sendTextToChatID(chatID, buildKokoCommandMenu())
}

func (h *WebhookHandler) buildFeatureContext(chatID int64, control ParsedControlCommand, reuseSummaryWhenAvailable bool) (FeatureContext, error) {
	state := h.getOrCreateChatState(chatID)
	domainKey := normalizedDomainKey(control.Domain)
	_ = domainKey // used for act domain inference via state fields set by callers

	// Always load fresh from DB — never serve response data from in-memory state.
	var summary string
	var err error
	if control.HasDateRange {
		summary, err = h.summarizeFromDBOrCacheWithDateRange(chatID, control.Domain, control.UserContext, state.ActiveAccountID, state.ActiveGroupName, control.DateStart, control.DateEnd)
	} else {
		summary, err = h.summarizeFromDBOrCache(chatID, control.Domain, control.UserContext, state.ActiveAccountID, state.ActiveGroupName)
	}
	if err != nil {
		return FeatureContext{}, err
	}

	return FeatureContext{Summary: summary, Cached: h.telegramCache.Snapshot()}, nil
}

type PublicExpert struct {
	Name   string
	Role   string
	Focus  string
	Source string
}

type KokoAssistantIntent string

const (
	kokoAssistantIntentUnknown         KokoAssistantIntent = "unknown"
	kokoAssistantIntentListGroups      KokoAssistantIntent = "list_groups"
	kokoAssistantIntentListGroupUsers  KokoAssistantIntent = "list_group_users"
	kokoAssistantIntentAccountID       KokoAssistantIntent = "account_id"
	kokoAssistantIntentActiveDomain    KokoAssistantIntent = "active_domain"
	kokoAssistantIntentShowActiveGroup KokoAssistantIntent = "show_active_group"
	kokoAssistantIntentClearActiveGrp  KokoAssistantIntent = "clear_active_group"
	kokoAssistantIntentSwitchGroup     KokoAssistantIntent = "switch_group"
)

type KokoAssistantQueryPlan struct {
	Intent        KokoAssistantIntent
	Canonical     string
	GroupRef      string
	RequestedMode string
	Confidence    int
}

func normalizeAssistantQuery(query string) string {
	lowered := strings.ToLower(strings.TrimSpace(query))
	if lowered == "" {
		return ""
	}
	re := regexp.MustCompile(`[^a-z0-9_\-\s]`)
	cleaned := re.ReplaceAllString(lowered, " ")
	return strings.Join(strings.Fields(cleaned), " ")
}

func containsAnyToken(tokenSet map[string]bool, tokens ...string) bool {
	for _, token := range tokens {
		if tokenSet[token] {
			return true
		}
	}
	return false
}

func extractRequestedDomainFromQuery(query string) string {
	normalized := normalizeAssistantQuery(query)
	if normalized == "" {
		return ""
	}

	words := strings.Fields(normalized)
	for _, w := range words {
		if mapped := mapDomainCodeOrName(w); mapped != "" {
			return mapped
		}
	}

	if strings.Contains(normalized, "all domains") || strings.Contains(normalized, "all domain") {
		return featureModeAll
	}

	for _, domain := range []string{featureModeIndustrial, featureModeElectronics, featureModeRevenue, featureModeConsumer} {
		if strings.Contains(normalized, domain+" domain") || strings.Contains(normalized, "domain "+domain) {
			return domain
		}
	}

	return ""
}

func buildKokoAssistantQueryPlan(query string) KokoAssistantQueryPlan {
	normalized := normalizeAssistantQuery(query)
	plan := KokoAssistantQueryPlan{
		Intent:        kokoAssistantIntentUnknown,
		Canonical:     normalized,
		RequestedMode: extractRequestedDomainFromQuery(query),
		Confidence:    0,
	}
	if normalized == "" {
		return plan
	}

	if groupRef, ok := extractSwitchGroupReference(query); ok {
		plan.Intent = kokoAssistantIntentSwitchGroup
		plan.GroupRef = groupRef
		plan.Confidence = 100
		return plan
	}

	tokens := strings.Fields(normalized)
	tokenSet := map[string]bool{}
	for _, token := range tokens {
		tokenSet[token] = true
	}

	hasGroup := containsAnyToken(tokenSet, "group", "groups", "team", "teams")
	hasShow := containsAnyToken(tokenSet, "show", "list", "display", "what", "which")
	hasOwnership := containsAnyToken(tokenSet, "my", "me", "i", "im")
	hasCurrent := containsAnyToken(tokenSet, "active", "current", "now")

	bestIntent := kokoAssistantIntentUnknown
	bestScore := 0

	scoreListGroups := 0
	if hasGroup {
		scoreListGroups += 3
	}
	if hasShow {
		scoreListGroups += 2
	}
	if hasOwnership {
		scoreListGroups += 2
	}
	if strings.Contains(normalized, "added to") || strings.Contains(normalized, "am i in") || strings.Contains(normalized, "part of") || strings.Contains(normalized, "belong to") {
		scoreListGroups += 4
	}
	if strings.Contains(normalized, "group of experts") || strings.Contains(normalized, "expert group") {
		scoreListGroups -= 4
	}
	if scoreListGroups > bestScore {
		bestIntent = kokoAssistantIntentListGroups
		bestScore = scoreListGroups
	}

	scoreListGroupUsers := 0
	if hasGroup || containsAnyToken(tokenSet, "here", "this") {
		scoreListGroupUsers += 2
	}
	if containsAnyToken(tokenSet, "users", "members", "people", "others", "who", "else", "everyone") {
		scoreListGroupUsers += 4
	}
	if strings.Contains(normalized, "other users are in this group") || strings.Contains(normalized, "are the other users in this group") || strings.Contains(normalized, "who else is in this group") || strings.Contains(normalized, "who are the other users") || strings.Contains(normalized, "other members in this group") {
		scoreListGroupUsers += 6
	}
	if scoreListGroupUsers > bestScore {
		bestIntent = kokoAssistantIntentListGroupUsers
		bestScore = scoreListGroupUsers
	}

	scoreAccountID := 0
	if containsAnyToken(tokenSet, "account") {
		scoreAccountID += 4
	}
	if containsAnyToken(tokenSet, "id", "identifier") {
		scoreAccountID += 3
	}
	if hasShow {
		scoreAccountID += 1
	}
	if strings.Contains(normalized, "which account") || strings.Contains(normalized, "account am i") {
		scoreAccountID += 3
	}
	if scoreAccountID > bestScore {
		bestIntent = kokoAssistantIntentAccountID
		bestScore = scoreAccountID
	}

	scoreDomain := 0
	if containsAnyToken(tokenSet, "domain") {
		scoreDomain += 4
	}
	if hasCurrent {
		scoreDomain += 3
	}
	if hasShow {
		scoreDomain += 1
	}
	if scoreDomain > bestScore {
		bestIntent = kokoAssistantIntentActiveDomain
		bestScore = scoreDomain
	}

	scoreShowActiveGroup := 0
	if hasGroup {
		scoreShowActiveGroup += 4
	}
	if hasCurrent {
		scoreShowActiveGroup += 3
	}
	if hasShow {
		scoreShowActiveGroup += 1
	}
	if scoreShowActiveGroup > bestScore {
		bestIntent = kokoAssistantIntentShowActiveGroup
		bestScore = scoreShowActiveGroup
	}

	scoreClearGroup := 0
	if hasGroup {
		scoreClearGroup += 3
	}
	if containsAnyToken(tokenSet, "clear", "reset", "remove", "unset") {
		scoreClearGroup += 5
	}
	if strings.Contains(normalized, "clear group selection") || strings.Contains(normalized, "reset group selection") {
		scoreClearGroup += 3
	}
	if scoreClearGroup > bestScore {
		bestIntent = kokoAssistantIntentClearActiveGrp
		bestScore = scoreClearGroup
	}

	if bestScore >= 5 {
		plan.Intent = bestIntent
		plan.Confidence = bestScore
	}

	return plan
}

func publicExpertsForDomain(domain string) []PublicExpert {
	switch normalizedDomainKey(domain) {
	case featureModeIndustrial:
		return []PublicExpert{
			{Name: "Nancy Leveson", Role: "MIT Professor", Focus: "system safety, incident analysis, resilient operations", Source: "MIT AeroAstro"},
			{Name: "Sidney Dekker", Role: "Safety Scientist", Focus: "human factors, operational failure recovery, safety leadership", Source: "sidneydekker.com"},
			{Name: "Jeffrey Liker", Role: "Industrial Operations Author", Focus: "lean operations, process improvement, manufacturing bottlenecks", Source: "University of Michigan / The Toyota Way"},
		}
	case featureModeElectronics:
		return []PublicExpert{
			{Name: "Limor Fried", Role: "Founder, Adafruit", Focus: "embedded systems, electronics debugging, hardware bring-up", Source: "Adafruit"},
			{Name: "Andrew Huang", Role: "Hardware Engineer", Focus: "electronics design, manufacturing, hardware diagnostics", Source: "bunniestudios.com"},
			{Name: "Chris Gammell", Role: "Hardware Consultant", Focus: "PCB debugging, validation workflow, electronics engineering teams", Source: "contextualelectronics.com"},
		}
	case featureModeRevenue:
		return []PublicExpert{
			{Name: "April Dunford", Role: "Positioning Consultant", Focus: "revenue messaging, market positioning, GTM blockers", Source: "aprildunford.com"},
			{Name: "Mark Roberge", Role: "Former CRO, HubSpot", Focus: "sales execution, pipeline operations, revenue forecasting", Source: "Harvard / Stage 2 Capital"},
			{Name: "Brent Adamson", Role: "B2B Revenue Advisor", Focus: "complex deal execution, buying friction, commercial strategy", Source: "Ecosystems"},
		}
	case featureModeConsumer:
		return []PublicExpert{
			{Name: "David Allen", Role: "Productivity Author", Focus: "workload triage, personal workflow cleanup, task systems", Source: "Getting Things Done"},
			{Name: "Julie Morgenstern", Role: "Organization Consultant", Focus: "personal operations, time management, execution backlog cleanup", Source: "juliemorgenstern.com"},
			{Name: "Tiago Forte", Role: "PKM Author", Focus: "knowledge organization, decision support, personal systems", Source: "Forte Labs"},
		}
	default:
		return []PublicExpert{
			{Name: "April Dunford", Role: "Operator / Consultant", Focus: "cross-functional prioritization and GTM problem framing", Source: "aprildunford.com"},
			{Name: "Andrew Huang", Role: "Engineer / Founder", Focus: "technical troubleshooting and hardware-oriented issue framing", Source: "bunniestudios.com"},
			{Name: "Nancy Leveson", Role: "MIT Professor", Focus: "systems thinking, risk analysis, root-cause investigation", Source: "MIT AeroAstro"},
		}
	}
}

func conciseIssueContext(text string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	selected := []string{}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		selected = append(selected, trimmed)
		if len(selected) == maxLines {
			break
		}
	}
	return strings.Join(selected, "\n")
}

func buildTemporaryExpertResponse(domain, query, summary, analysis string, experts []PublicExpert) string {
	sections := []string{
		"Temporary expert shortlist based on the current koko summary/analyze context.",
		"Question: " + strings.TrimSpace(query),
		"Domain: " + normalizedDomainKey(domain),
	}

	if snippet := conciseIssueContext(summary, 4); snippet != "" {
		sections = append(sections, "Summary signals:\n"+snippet)
	}
	if snippet := conciseIssueContext(analysis, 4); snippet != "" {
		sections = append(sections, "Analysis signals:\n"+snippet)
	}

	if len(experts) > 0 {
		lines := []string{"Publicly listed experts to review:"}
		for idx, expert := range experts {
			lines = append(lines, fmt.Sprintf("%d. %s — %s | Focus: %s | Public source: %s", idx+1, expert.Name, expert.Role, expert.Focus, expert.Source))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}

	sections = append(sections, "Temporary mode: this is a static public shortlist, not a live availability or referral check.")
	return strings.Join(sections, "\n\n")
}

func (h *WebhookHandler) runKokoNaturalLanguageAssistant(chatID int64, requester string, control ParsedControlCommand, query string) error {
	state := h.getOrCreateChatState(chatID)
	plan := buildKokoAssistantQueryPlan(query)

	assistantControl := ParsedControlCommand{Name: "koko:assistant", Domain: control.Domain}
	if assistantControl.Domain == "" && plan.RequestedMode != "" {
		assistantControl.Domain = plan.RequestedMode
	}

	accountID, groups, groupErr := h.lookupTelegramUserGroups(requester)
	if groupErr != nil {
		log.Printf("TELEGRAM_GROUP_CONTEXT_LOOKUP_FAILED: user=%s error=%v", requester, groupErr)
	}
	if accountID != "" {
		state.ActiveAccountID = accountID
	}

	switch plan.Intent {
	case kokoAssistantIntentListGroups:
		if groupErr != nil {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "Unable to load your group memberships right now. Please try again.")
		}
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, formatUserGroupMembershipResponse(accountID, groups))

	case kokoAssistantIntentListGroupUsers:
		if strings.TrimSpace(state.ActiveGroupName) == "" && accountID != "" && len(groups) == 1 {
			state.ActiveGroupName = groups[0].GroupName
			state.ActiveGroupID = groups[0].GroupID
			state.ActiveAccountID = accountID
		}
		if strings.TrimSpace(state.ActiveGroupName) == "" {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "No active group is currently selected. Please switch to a group first using 'koko:group <name>'.")
		}
		if h.store == nil {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "User lookup is not available right now.")
		}
		users, err := h.store.GetGroupUsers(context.Background(), state.ActiveAccountID, state.ActiveGroupName)
		if err != nil {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "Unable to load users for this group right now.")
		}
		if len(users) == 0 {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "There are no users in the active group: "+state.ActiveGroupName)
		}
		var userNames []string
		for _, u := range users {
			if u == requester {
				userNames = append(userNames, "  • "+u+" (you)")
			} else {
				userNames = append(userNames, "  • "+u)
			}
		}
		responseLines := []string{}
		if strings.TrimSpace(state.ActiveAccountID) != "" {
			responseLines = append(responseLines, "Account: "+state.ActiveAccountID)
		}
		groupLine := "Group: " + state.ActiveGroupName
		if strings.TrimSpace(state.ActiveGroupID) != "" {
			groupLine += " (" + state.ActiveGroupID + ")"
		}
		responseLines = append(responseLines, groupLine)
		responseLines = append(responseLines, "Users in group "+state.ActiveGroupName+":")
		responseLines = append(responseLines, strings.Join(userNames, "\n"))
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, strings.Join(responseLines, "\n"))

	case kokoAssistantIntentAccountID:
		if strings.TrimSpace(accountID) == "" {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "I could not resolve your account ID from current Telegram group memberships.")
		}
		response := "Your Account ID: " + accountID
		if strings.TrimSpace(state.ActiveGroupName) != "" {
			response += "\nActive Group: " + state.ActiveGroupName
			if strings.TrimSpace(state.ActiveGroupID) != "" {
				response += " (" + state.ActiveGroupID + ")"
			}
		}
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, response)

	case kokoAssistantIntentActiveDomain:
		activeDomain := normalizedDomainKey(state.DefaultDomain)
		response := "Active Domain: " + activeDomain
		if strings.TrimSpace(state.ActiveGroupName) != "" {
			response += "\nActive Group: " + state.ActiveGroupName
			if strings.TrimSpace(state.ActiveGroupID) != "" {
				response += " (" + state.ActiveGroupID + ")"
			}
		}
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, response)

	case kokoAssistantIntentShowActiveGroup:
		if strings.TrimSpace(state.ActiveGroupName) == "" {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "No active group is currently selected.")
		}
		response := "Active Group: " + state.ActiveGroupName
		if strings.TrimSpace(state.ActiveGroupID) != "" {
			response += " (" + state.ActiveGroupID + ")"
		}
		if strings.TrimSpace(state.ActiveAccountID) != "" {
			response += "\nAccount: " + state.ActiveAccountID
		}
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, response)

	case kokoAssistantIntentClearActiveGrp:
		state.ActiveGroupName = ""
		state.ActiveGroupID = ""
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, "Active group selection cleared. Use 'koko:switch to group <groupname|group_id>' to set it again.")

	case kokoAssistantIntentSwitchGroup:
		groupRef := plan.GroupRef
		if strings.TrimSpace(groupRef) == "" {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "I could not identify the group name/id. Try: koko:switch to group <groupname|group_id>")
		}
		if groupErr != nil {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "Unable to verify your groups right now. Please try again.")
		}
		if len(groups) == 0 {
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "I could not find any groups linked to your Telegram user.")
		}

		matchedGroup, found := matchGroupReference(groups, groupRef)
		if !found {
			available := []string{}
			for _, group := range groups {
				entry := group.GroupName
				if strings.TrimSpace(group.GroupID) != "" {
					entry += " (" + group.GroupID + ")"
				}
				available = append(available, entry)
			}
			return h.sendCommandResultToGroup(chatID, requester, assistantControl, "I could not match that group. Available groups:\n- "+strings.Join(available, "\n- "))
		}

		state.ActiveAccountID = matchedGroup.AccountID
		state.ActiveGroupID = matchedGroup.GroupID
		state.ActiveGroupName = matchedGroup.GroupName

		response := "Switched active group to: " + matchedGroup.GroupName
		if strings.TrimSpace(matchedGroup.GroupID) != "" {
			response += " (" + matchedGroup.GroupID + ")"
		}
		if strings.TrimSpace(matchedGroup.AccountID) != "" {
			response += "\nAccount: " + matchedGroup.AccountID
		}
		return h.sendCommandResultToGroup(chatID, requester, assistantControl, response)
	}
	// Route ALL unrecognized koko: inputs — text or transcribed audio — through
	// the NLQ engine. The old "expert shortlist" fallback is no longer used;
	// instead, we always attempt a DB data lookup and guide the user to refine
	// when nothing is found.
	nlqResult, nlqErr := h.executeNLQ(context.Background(), state.ActiveAccountID, requester, query, assistantControl.Domain)
	if nlqErr != nil {
		log.Printf("TELEGRAM_NLQ_FAILED: %v", nlqErr)
		return h.sendCommandResultToGroup(chatID, requester, assistantControl,
			"I could not process your query right now. Please try again.")
	}

	if nlqEmptyOrGeneral(nlqResult) {
		return h.sendCommandResultToGroup(chatID, requester, assistantControl,
			"No data found for your query.\n\n"+
				"Try refining with specific terms such as:\n"+
				"• Domain: revenue, industrial, electronics, consumer\n"+
				"• Time range: last month, this quarter, FY 2025\n"+
				"• Data type: summaries or act prompts\n"+
				"• Group name: use koko:group to see your available groups")
	}

	return h.sendCommandResultToGroup(chatID, requester, assistantControl, nlqResult)
}

func (h *WebhookHandler) executeDownstreamAction(featureMode string, action ActionCommand) CommandExecutionResult {
	endpoint := endpointForFeature(featureMode)
	payload := map[string]interface{}{
		"feature": featureMode,
		"command": action.Name,
		"params":  action.Params,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return CommandExecutionResult{Feature: featureMode, Command: action.Name, Status: "error", Error: err.Error()}
	}

	if endpoint == "" {
		log.Printf("DOWNSTREAM_COMMAND_LOG_ONLY: endpoint=unset payload=%s", string(jsonBody))
		return CommandExecutionResult{
			Feature: featureMode,
			Command: action.Name,
			Status:  "logged",
			Output:  "logged command payload (no downstream endpoint configured)",
		}
	}

	log.Printf("DOWNSTREAM_COMMAND_LOG_ONLY: endpoint=%s payload=%s", endpoint, string(jsonBody))
	return CommandExecutionResult{
		Feature: featureMode,
		Command: action.Name,
		Status:  "logged",
		Output:  "logged command payload for downstream adapter (execution disabled)",
	}
}

func endpointForFeature(mode string) string {
	switch mode {
	case featureModeIndustrial:
		return strings.TrimSpace(os.Getenv("INDUSTRIAL_ACTION_ENDPOINT"))
	case featureModeElectronics:
		return strings.TrimSpace(os.Getenv("ELECTRONICS_ACTION_ENDPOINT"))
	case featureModeRevenue:
		return strings.TrimSpace(os.Getenv("REVENUE_ACTION_ENDPOINT"))
	case featureModeConsumer:
		return strings.TrimSpace(os.Getenv("CONSUMER_ACTION_ENDPOINT"))
	default:
		return ""
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (h *WebhookHandler) runFeaturePlan(chatID int64, control ParsedControlCommand, requester string, accountID string, activeGroup Group) error {
	tierArg := strings.TrimSpace(control.Argument)

	// No argument: list available plans
	if tierArg == "" {
		if h.store == nil || !h.store.DBEnabled() {
			return h.sendCommandResultToGroup(chatID, requester, control, "Plan information is unavailable right now. Please try again later.")
		}
		tiers, err := h.store.ListPlanTiers(context.Background())
		if err != nil || len(tiers) == 0 {
			return h.sendCommandResultToGroup(chatID, requester, control, "No plan tiers are configured. Please contact your administrator.")
		}

		var currentTier PlanTier
		if strings.TrimSpace(accountID) != "" {
			currentTier, _ = h.store.GetPlanTierForAccount(context.Background(), accountID)
		}

		lines := []string{"Available plans:"}
		for _, t := range tiers {
			marker := "  "
			if t.ID == currentTier.ID {
				marker = "* "
			}
			lines = append(lines, fmt.Sprintf("%s%s (%s) — storage: %s, summarize: %d/mo, analyze: %d/mo, act: %d/mo — $%.2f/mo",
				marker, t.DisplayName, t.TierCode,
				formatBytes(t.StorageBytes),
				t.MonthlySummarizeLimit, t.MonthlyAnalyzeLimit, t.MonthlyActLimit,
				t.MonthlyPriceUSD,
			))
		}
		if currentTier.ID != 0 {
			lines = append(lines, "\n* = current plan")
		}
		lines = append(lines, "\nTo request a plan upgrade: koko:plan <tier_code>")
		return h.sendCommandResultToGroup(chatID, requester, control, strings.Join(lines, "\n"))
	}

	// Argument provided: record a plan change request
	if h.store == nil || !h.store.DBEnabled() {
		return h.sendCommandResultToGroup(chatID, requester, control, "Plan change requests are unavailable right now. Please try again later.")
	}
	tiers, err := h.store.ListPlanTiers(context.Background())
	if err != nil {
		return h.sendCommandResultToGroup(chatID, requester, control, "Unable to retrieve plan information. Please try again.")
	}

	var requestedTier PlanTier
	for _, t := range tiers {
		if strings.EqualFold(t.TierCode, tierArg) || strings.EqualFold(t.DisplayName, tierArg) {
			requestedTier = t
			break
		}
	}
	if requestedTier.ID == 0 {
		return h.sendCommandResultToGroup(chatID, requester, control, "Unknown plan tier: "+tierArg+"\nUse koko:plan to list available tiers.")
	}

	currentTier, _ := h.store.GetPlanTierForAccount(context.Background(), accountID)
	if requestedTier.ID == currentTier.ID {
		return h.sendCommandResultToGroup(chatID, requester, control, "Your account is already on the "+currentTier.DisplayName+" plan.")
	}

	req := PlanChangeRequest{
		AccountID:           accountID,
		RequestedByUserName: requester,
		CurrentPlanTierID:   currentTier.ID,
		RequestedPlanTierID: requestedTier.ID,
	}
	if _, err := h.store.RecordPlanChangeRequest(context.Background(), req); err != nil {
		log.Printf("PLAN_CHANGE_REQUEST_FAILED: account=%s requester=%s tier=%s error=%v", accountID, requester, tierArg, err)
		return h.sendCommandResultToGroup(chatID, requester, control, "Failed to record your plan change request. Please try again.")
	}

	confirmMsg := fmt.Sprintf("Plan upgrade request recorded.\nRequested by: %s\nCurrent plan: %s\nRequested plan: %s\n\nYour administrator will be notified to complete the switch.",
		requester,
		func() string {
			if currentTier.ID == 0 {
				return "none"
			}
			return currentTier.DisplayName
		}(),
		requestedTier.DisplayName,
	)

	if err := h.sendCommandResultToGroup(chatID, requester, control, confirmMsg); err != nil {
		return err
	}
	if strings.TrimSpace(accountID) != "" && strings.TrimSpace(activeGroup.GroupName) != "" {
		h.broadcastToGroupByMessenger(accountID, activeGroup.GroupName, h.messengerForChat(chatID), fmt.Sprintf("%d", chatID), "From "+requester+":\n\n"+formatKokoCommandResponse(control, confirmMsg))
	}
	return nil
}
