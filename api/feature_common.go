package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	featureModeAll         = "all"
	featureModeIndustrial  = "industrial"
	featureModeElectronics = "electronics"
	featureModeRevenue     = "revenue"
	featureModeFinance     = "finance"
	featureModeConsumer    = "consumer"
	featureModeAccounting  = "accounting"
	featureModeOperations  = "operations"
	featureModeRealEstate  = "real_estate"
)

type FeatureContext struct {
	Summary string
	Cached  []CachedTelegramMessage
}

type ActionCommand struct {
	Name            string
	Params          map[string]string
	Description     string
	Preconditions   string
	SuccessCriteria string
	Fallback        string
}

type CommandExecutionResult struct {
	Feature string
	Command string
	Status  string
	Output  string
	Error   string
}

type FeatureModule interface {
	Mode() string
	Title() string
	AgentSystemRole() string
	Analyze(ctx FeatureContext) string
	PlanActions(ctx FeatureContext) []ActionCommand
}

func featureModeFromEnv() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("KOKO_FEATURE_MODE")))
	if mode == "" {
		return featureModeAll
	}
	switch mode {
	case featureModeAll, featureModeIndustrial, featureModeElectronics, featureModeRevenue, featureModeConsumer:
		return mode
	default:
		return featureModeAll
	}
}

func selectedFeatureModules() []FeatureModule {
	all := []FeatureModule{
		IndustrialOpsFeature{},
		ElectronicsProgramFeature{},
		RevenueRiskFeature{},
		ConsumerLifeAdminFeature{},
	}

	mode := featureModeFromEnv()
	if mode == featureModeAll {
		return all
	}

	selected := make([]FeatureModule, 0, 1)
	for _, module := range all {
		if module.Mode() == mode {
			selected = append(selected, module)
		}
	}

	if len(selected) == 0 {
		return all
	}

	return selected
}

func buildActionPlanText(module FeatureModule, actions []ActionCommand) string {
	if len(actions) == 0 {
		return "No concrete downstream actions identified from current summarized context."
	}

	lines := []string{"Action Plan:"}
	for idx, action := range actions {
		lines = append(lines, fmt.Sprintf("%d. %s", idx+1, action.Name))
	}
	return strings.Join(lines, "\n")
}

func buildFlowChartText(module FeatureModule) string {
	return strings.Join([]string{
		"Flow Chart:",
		"Start -> Parse summarized chat -> " + module.Title() + " analysis -> Generate action plan -> Execute downstream actions (koko:act) -> Return status to user",
	}, "\n")
}

func hasAny(summary string, keys ...string) bool {
	lower := strings.ToLower(summary)
	for _, k := range keys {
		if strings.Contains(lower, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// buildEnterpriseAgentPrompt generates a structured AI agent prompt from koko:act context.
// The prompt is intended to be forwarded directly to an enterprise AI agent or MCP tool
// to execute the planned workflows.
func buildEnterpriseAgentPrompt(module FeatureModule, actions []ActionCommand, summary, analysis, accountID, groupName, userContext string) string {
	sections := []string{}

	// Header: domain + account metadata, no koko tags
	headerParts := []string{}
	if accountID != "" {
		meta := "Account: " + accountID
		if groupName != "" {
			meta += "  |  Group: " + groupName
		}
		headerParts = append(headerParts, meta)
	}
	if userContext != "" {
		headerParts = append(headerParts, "Note: "+userContext)
	}
	if len(headerParts) > 0 {
		sections = append(sections, strings.Join(headerParts, "\n"))
	}

	sections = append(sections, "ROLE\n"+module.AgentSystemRole())

	if strings.TrimSpace(summary) != "" {
		sections = append(sections, "CONTEXT\n"+strings.TrimSpace(summary))
	}

	if strings.TrimSpace(analysis) != "" {
		sections = append(sections, "FINDINGS\n"+strings.TrimSpace(analysis))
	}

	if len(actions) > 0 {
		actionLines := []string{"ACTIONS"}
		for idx, action := range actions {
			block := []string{fmt.Sprintf("  %d. %s", idx+1, action.Name)}
			if len(action.Params) > 0 {
				paramParts := []string{}
				for k, v := range action.Params {
					paramParts = append(paramParts, k+"="+v)
				}
				block = append(block, "     Parameters:    "+strings.Join(paramParts, ", "))
			}
			if action.Description != "" {
				block = append(block, "     Objective:     "+action.Description)
			}
			if action.Preconditions != "" {
				block = append(block, "     Preconditions: "+action.Preconditions)
			}
			if action.SuccessCriteria != "" {
				block = append(block, "     Success:       "+action.SuccessCriteria)
			}
			if action.Fallback != "" {
				block = append(block, "     If it fails:   "+action.Fallback)
			}
			actionLines = append(actionLines, strings.Join(block, "\n"))
		}
		sections = append(sections, strings.Join(actionLines, "\n\n"))
	}

	sections = append(sections, strings.Join([]string{
		"INSTRUCTIONS",
		"Execute the planned actions in order. For each action:",
		"1. Verify all preconditions are met before proceeding.",
		"2. Execute via the appropriate enterprise API or MCP tool endpoint.",
		"3. Validate the result against the stated success criteria.",
		"4. If an action fails, apply the fallback path and continue to the next action.",
		"5. Collect all results into a structured execution report.",
		"",
		"OUTPUT FORMAT",
		"Return a JSON execution report with the following keys:",
		"  executed:   list of completed actions with timestamp and result summary",
		"  skipped:    actions not executed with reason",
		"  errors:     failures with error message and fallback outcome",
		"  next_steps: recommended follow-up actions based on execution results",
	}, "\n"))

	return strings.Join(sections, "\n\n")
}
