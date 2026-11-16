package main

import (
	"strings"
	"testing"
)

func TestParseTelegramControlCommandSupportsKokoPrefixedNumericShortcut(t *testing.T) {
	control := parseTelegramControlCommand(`koko:1 3 "Daily Revenue Standup Flood (Short Bursts, Many Threads)"`)

	if control.Name != telegramSummarizeCommand {
		t.Fatalf("expected summarize command, got %q", control.Name)
	}
	if control.Domain != featureModeRevenue {
		t.Fatalf("expected revenue domain, got %q", control.Domain)
	}
	if control.UserContext == "" {
		t.Fatal("expected parsed context for prefixed numeric summarize command")
	}
}

func TestParseTelegramControlCommandSupportsKokoPrefixedActShortcut(t *testing.T) {
	control := parseTelegramControlCommand("koko:3")

	if control.Name != telegramActCommand {
		t.Fatalf("expected act command, got %q", control.Name)
	}
}

func TestParseTelegramControlCommandSupportsGroupSelection(t *testing.T) {
	control := parseTelegramControlCommand("koko:group revenue")

	if control.Name != telegramGroupCommand {
		t.Fatalf("expected group command, got %q", control.Name)
	}
	if control.Argument != "revenue" {
		t.Fatalf("expected group argument revenue, got %q", control.Argument)
	}
}

func TestExtractKokoNaturalLanguageQuerySkipsGroupCommand(t *testing.T) {
	if _, ok := extractKokoNaturalLanguageQuery("koko:group revenue"); ok {
		t.Fatal("expected koko:group to be treated as a control command, not NLP query")
	}
}

func TestParseYesNoTreatsKokoActShortcutAsConfirmation(t *testing.T) {
	yes, no := parseYesNo("koko:3")
	if !yes || no {
		t.Fatalf("expected koko:3 to confirm act, got yes=%v no=%v", yes, no)
	}
}

func TestFeatureModulesForControlFiltersToRequestedDomain(t *testing.T) {
	h := &WebhookHandler{featureModules: selectedFeatureModules()}
	control := ParsedControlCommand{Name: telegramActCommand, Domain: featureModeRevenue}

	modules := h.featureModulesForControl(control)
	if len(modules) != 1 {
		t.Fatalf("expected 1 module for revenue, got %d", len(modules))
	}
	if modules[0].Mode() != featureModeRevenue {
		t.Fatalf("expected revenue module, got %q", modules[0].Mode())
	}
}

func TestIsUserGroupListQueryRecognizesCommonPhrasing(t *testing.T) {
	queries := []string{
		"display all groups i am added to",
		"what groups am i in?",
		"list my groups",
	}
	for _, query := range queries {
		if !isUserGroupListQuery(query) {
			t.Fatalf("expected query to be recognized: %q", query)
		}
	}

	if isUserGroupListQuery("suggest a few experts in this area") {
		t.Fatal("did not expect expert query to be classified as group membership lookup")
	}
}

func TestBuildTelegramCommandMenuForUserIncludesSeededGroups(t *testing.T) {
	h := &WebhookHandler{store: newSeededInMemoryStore()}
	menu := h.buildTelegramCommandMenuForUser("demo_user_1")

	if !strings.Contains(menu, "demo_account_a") {
		t.Fatalf("expected account context in menu, got %q", menu)
	}
	if !strings.Contains(menu, "YOUR GROUPS") && !strings.Contains(menu, "Your Groups:") {
		t.Fatalf("expected group listing in menu, got %q", menu)
	}
	if !strings.Contains(menu, "revenue") {
		t.Fatalf("expected seeded group name in menu, got %q", menu)
	}
	if !strings.Contains(menu, "grp_") {
		t.Fatalf("expected generated group id in menu, got %q", menu)
	}
	if !strings.Contains(menu, "koko:group") {
		t.Fatalf("expected group selection guidance in menu, got %q", menu)
	}
}

func TestFormatUserGroupMembershipResponseIncludesAccountAndGroups(t *testing.T) {
	response := formatUserGroupMembershipResponse("demo_account_a", []Group{{GroupName: "revenue", GroupID: "grp_123"}})
	if !strings.Contains(response, "Your Account: demo_account_a") {
		t.Fatalf("expected account id in response, got %q", response)
	}
	if !strings.Contains(response, "revenue (grp_123)") {
		t.Fatalf("expected group entry in response, got %q", response)
	}
	if !strings.Contains(response, "Groups you are added to:") {
		t.Fatalf("expected group list heading in response, got %q", response)
	}
}

func TestIsAccountIDQueryRecognizesCommonPhrasing(t *testing.T) {
	if !isAccountIDQuery("show my account id") {
		t.Fatal("expected account-id query to be recognized")
	}
	if !isAccountIDQuery("which account am i on") {
		t.Fatal("expected account lookup phrasing to be recognized")
	}
	if isAccountIDQuery("show my groups") {
		t.Fatal("did not expect groups query to match account-id intent")
	}
}

func TestIsActiveDomainQueryRecognizesCommonPhrasing(t *testing.T) {
	if !isActiveDomainQuery("which domain is active") {
		t.Fatal("expected active-domain query to be recognized")
	}
	if !isActiveDomainQuery("show active domain") {
		t.Fatal("expected active-domain query variant to be recognized")
	}
	if isActiveDomainQuery("show my account id") {
		t.Fatal("did not expect account-id query to match active-domain intent")
	}
}

func TestExtractSwitchGroupReference(t *testing.T) {
	ref, ok := extractSwitchGroupReference("switch to group revenue")
	if !ok || ref != "revenue" {
		t.Fatalf("expected group reference revenue, got ref=%q ok=%v", ref, ok)
	}

	ref, ok = extractSwitchGroupReference("please set group to grp_abcd1234")
	if !ok || ref != "grp_abcd1234" {
		t.Fatalf("expected group id reference, got ref=%q ok=%v", ref, ok)
	}

	if _, ok := extractSwitchGroupReference("show my account id"); ok {
		t.Fatal("did not expect non-switch query to produce group reference")
	}
}

func TestMatchGroupReferenceSupportsNameAndID(t *testing.T) {
	groups := []Group{
		{GroupName: "revenue", GroupID: "grp_1"},
		{GroupName: "electronics", GroupID: "grp_2"},
	}

	match, ok := matchGroupReference(groups, "revenue")
	if !ok || match.GroupID != "grp_1" {
		t.Fatalf("expected to match revenue group, got %+v ok=%v", match, ok)
	}

	match, ok = matchGroupReference(groups, "grp_2")
	if !ok || match.GroupName != "electronics" {
		t.Fatalf("expected to match group id grp_2, got %+v ok=%v", match, ok)
	}

	if _, ok := matchGroupReference(groups, "unknown"); ok {
		t.Fatal("did not expect unknown reference to match")
	}
}

func TestShowAndClearActiveGroupIntentDetection(t *testing.T) {
	if !isShowActiveGroupQuery("show active group") {
		t.Fatal("expected show-active-group query to be recognized")
	}
	if !isShowActiveGroupQuery("which group is active right now") {
		t.Fatal("expected active-group variant to be recognized")
	}
	if isShowActiveGroupQuery("show active domain") {
		t.Fatal("did not expect domain query to match active-group intent")
	}

	if !isClearActiveGroupQuery("clear active group") {
		t.Fatal("expected clear-active-group query to be recognized")
	}
	if !isClearActiveGroupQuery("reset group selection") {
		t.Fatal("expected reset group selection query to be recognized")
	}
	if isClearActiveGroupQuery("show my groups") {
		t.Fatal("did not expect show groups query to match clear-active-group intent")
	}
}

func TestBuildKokoAssistantQueryPlanSemanticGroupList(t *testing.T) {
	plan := buildKokoAssistantQueryPlan("Could you tell me which groups I currently belong to?")
	if plan.Intent != kokoAssistantIntentListGroups {
		t.Fatalf("expected list_groups intent, got %q", plan.Intent)
	}
}

func TestBuildKokoAssistantQueryPlanSyntacticSwitchGroup(t *testing.T) {
	plan := buildKokoAssistantQueryPlan("Please make revenue my active group")
	if plan.Intent != kokoAssistantIntentSwitchGroup {
		t.Fatalf("expected switch_group intent, got %q", plan.Intent)
	}
	if plan.GroupRef != "revenue" {
		t.Fatalf("expected group ref revenue, got %q", plan.GroupRef)
	}
}

func TestBuildKokoAssistantQueryPlanAvoidsExpertGroupFalsePositive(t *testing.T) {
	plan := buildKokoAssistantQueryPlan("suggest a group of experts for this issue")
	if plan.Intent == kokoAssistantIntentListGroups {
		t.Fatalf("did not expect list_groups intent for expert-group query, got %q", plan.Intent)
	}
}

func TestNLPInterpretKokoCommandSummarizeFromNaturalEnglish(t *testing.T) {
	got := nlpInterpretKokoCommand("koko: gimme a summary for gst updates", featureModeRevenue)
	if got.Command != telegramSummarizeCommand {
		t.Fatalf("expected summarize command, got %q", got.Command)
	}
	if got.Confidence < 0.65 {
		t.Fatalf("expected confident interpretation, got %.2f", got.Confidence)
	}
}

func TestNLPExpandAcronymsPrefersDomainDictionary(t *testing.T) {
	rev := nlpExpandAcronyms("please review pnl and gst", featureModeRevenue)
	if !strings.Contains(rev, "profit and loss") {
		t.Fatalf("expected revenue expansion for pnl, got %q", rev)
	}
	if !strings.Contains(rev, "goods and services tax") {
		t.Fatalf("expected revenue expansion for gst, got %q", rev)
	}

	common := nlpExpandAcronyms("fyi send eod update", featureModeAll)
	if !strings.Contains(common, "for your information") {
		t.Fatalf("expected common expansion for fyi, got %q", common)
	}
	if !strings.Contains(common, "end of day") {
		t.Fatalf("expected common expansion for eod, got %q", common)
	}
}

func TestNLPInterpretKokoCommandUKOfficialTerms(t *testing.T) {
	got := nlpInterpretKokoCommand("koko: summarise vat and hmrc updates", featureModeRevenue)
	if got.Command != telegramSummarizeCommand {
		t.Fatalf("expected summarize command, got %q", got.Command)
	}
	if got.LanguageCode != "en-GB" {
		t.Fatalf("expected en-GB locale, got %q", got.LanguageCode)
	}
	if !strings.Contains(got.ExpandedText, "value added tax") {
		t.Fatalf("expected VAT expansion, got %q", got.ExpandedText)
	}
}

func TestNLPInterpretKokoCommandAUOfficialTerms(t *testing.T) {
	got := nlpInterpretKokoCommand("koko: pls analyse payg and bas risk", featureModeFinance)
	if got.Command != telegramAnalyzeCommand {
		t.Fatalf("expected analyze command, got %q", got.Command)
	}
	if got.LanguageCode != "en-AU" {
		t.Fatalf("expected en-AU locale, got %q", got.LanguageCode)
	}
	if !strings.Contains(got.ExpandedText, "pay as you go") {
		t.Fatalf("expected PAYG expansion, got %q", got.ExpandedText)
	}
}

func TestNLPPreprocessEnglishUSLocale(t *testing.T) {
	processed, locale := nlpPreprocessEnglish("show IRS sales tax summary", featureModeRevenue)
	if locale != "en-US" {
		t.Fatalf("expected en-US locale, got %q", locale)
	}
	if !strings.Contains(processed, "internal revenue service") {
		t.Fatalf("expected IRS expansion, got %q", processed)
	}
}

func TestBuildEnterpriseAgentPromptStructure(t *testing.T) {
	module := RevenueRiskFeature{}
	actions := []ActionCommand{
		{
			Name:            "create_exec_escalation",
			Params:          map[string]string{"priority": "high"},
			Description:     "Escalate the blocker to the exec team.",
			Preconditions:   "Blocker is named and deal is identified.",
			SuccessCriteria: "Escalation logged and sponsor notified.",
			Fallback:        "Draft email to revenue lead.",
		},
	}

	prompt := buildEnterpriseAgentPrompt(module, actions, "Deal blocked on legal.", "Revenue risk: high.", "acme_corp", "revenue_q2", "quarter-end blockers")

	for _, expected := range []string{
		"Account: acme_corp",
		"Group: revenue_q2",
		"quarter-end blockers",
		"ROLE",
		"CONTEXT",
		"Deal blocked on legal.",
		"FINDINGS",
		"Revenue risk: high.",
		"ACTIONS",
		"create_exec_escalation",
		"priority=high",
		"Objective:     Escalate the blocker",
		"Preconditions: Blocker is named",
		"Success:       Escalation logged",
		"If it fails:   Draft email",
		"INSTRUCTIONS",
		"OUTPUT FORMAT",
		"executed",
		"next_steps",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain %q\n\nGot:\n%s", expected, prompt)
		}
	}
}

func TestAllFeatureModulesImplementAgentSystemRole(t *testing.T) {
	modules := selectedFeatureModules()
	for _, module := range modules {
		role := module.AgentSystemRole()
		if strings.TrimSpace(role) == "" {
			t.Fatalf("module %q returned empty AgentSystemRole()", module.Mode())
		}
	}
}

func TestAllFeatureModulePlanActionsEnrichedFields(t *testing.T) {
	ctx := FeatureContext{Summary: "fault alarm maintenance blocked customer report test defect need todo date meeting"}
	modules := selectedFeatureModules()
	for _, module := range modules {
		actions := module.PlanActions(ctx)
		if len(actions) == 0 {
			t.Fatalf("module %q returned no actions for a rich summary context", module.Mode())
		}
		for _, action := range actions {
			if strings.TrimSpace(action.Description) == "" {
				t.Fatalf("module %q action %q has empty Description", module.Mode(), action.Name)
			}
			if strings.TrimSpace(action.SuccessCriteria) == "" {
				t.Fatalf("module %q action %q has empty SuccessCriteria", module.Mode(), action.Name)
			}
			if strings.TrimSpace(action.Fallback) == "" {
				t.Fatalf("module %q action %q has empty Fallback", module.Mode(), action.Name)
			}
		}
	}
}
