// Copyright 2026 Harish, KokoHq Inc.
//
// Licensed under the MIT License. See LICENSE file for details.

package main

import "strings"

type RevenueRiskFeature struct{}

func (RevenueRiskFeature) Mode() string {
	return featureModeRevenue
}

func (RevenueRiskFeature) Title() string {
	return "Revenue Risk"
}

func (RevenueRiskFeature) AgentSystemRole() string {
	return strings.Join([]string{
		"You are a revenue operations AI agent embedded in a CRM, sales execution, or deal management system.",
		"Your role is to execute pipeline health, escalation, and customer-risk workflows triggered by delivery blockers,",
		"deal velocity signals, and customer churn or contract risk indicators identified from sales team communications.",
		"You have access to the CRM, forecasting system, customer success platform, and executive escalation channels.",
		"Do not modify deal stage or close dates without explicit human confirmation. Flag for review instead.",
	}, " ")
}

func (RevenueRiskFeature) Analyze(ctx FeatureContext) string {
	insights := []string{}
	if hasAny(ctx.Summary, "delay", "blocked", "need more", "resource gap") {
		insights = append(insights, "Potential delivery risk detected due to dependency/resource constraints.")
	}
	if hasAny(ctx.Summary, "customer", "renewal", "contract", "report") {
		insights = append(insights, "Potential customer or contract impact indicators found in summarized chat.")
	}
	if len(insights) == 0 {
		insights = append(insights, "No direct revenue-risk indicators found; continue opportunity monitoring.")
	}
	return strings.Join(insights, "\n")
}

func (RevenueRiskFeature) PlanActions(ctx FeatureContext) []ActionCommand {
	actions := []ActionCommand{}
	if hasAny(ctx.Summary, "blocked", "delay", "need more") {
		actions = append(actions, ActionCommand{
			Name:            "create_exec_escalation",
			Params:          map[string]string{"priority": "high"},
			Description:     "Create a high-priority executive escalation record referencing the identified delivery blocker, with the deal name, the blocking party, and the revenue at risk.",
			Preconditions:   "At least one named blocker and associated deal or customer is identifiable from the summarized chat.",
			SuccessCriteria: "Escalation logged in the CRM, executive sponsor notified, and a resolution timeline requested within 24 hours.",
			Fallback:        "Draft an escalation email to the revenue lead with the blocker details and flag the deal for manager review in the CRM.",
		})
	}
	if hasAny(ctx.Summary, "customer", "report") {
		actions = append(actions, ActionCommand{
			Name:            "generate_customer_update_report",
			Params:          map[string]string{"format": "summary"},
			Description:     "Generate a concise customer-facing status update report from the current deal signals, suitable for delivery to the account contact or customer success manager.",
			Preconditions:   "Customer name or account ID is present in the summarized chat context.",
			SuccessCriteria: "Report generated, reviewed by the account owner, and queued for delivery to the customer contact.",
			Fallback:        "Deliver a plain-text deal status summary to the account executive for manual review and forwarding.",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, ActionCommand{
			Name:            "refresh_pipeline_health",
			Params:          map[string]string{"window": "7d"},
			Description:     "Query the CRM for deals with no activity in the past 7 days, stalled stage durations, and forecast variance against current period target. Produce a pipeline health digest.",
			Preconditions:   "CRM API is accessible and deal data is current.",
			SuccessCriteria: "Pipeline health digest generated, distributed to the sales manager, and at-risk deals flagged for immediate follow-up.",
			Fallback:        "Pull a static pipeline export and surface the top 5 at-risk deals by last-activity date.",
		})
	}
	return actions
}
