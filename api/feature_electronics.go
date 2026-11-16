// Copyright 2026 Harish, KokoHq Inc.
//
// Licensed under the MIT License. See LICENSE file for details.

package main

import "strings"

type ElectronicsProgramFeature struct{}

func (ElectronicsProgramFeature) Mode() string {
	return featureModeElectronics
}

func (ElectronicsProgramFeature) Title() string {
	return "Electronics Program Intelligence"
}

func (ElectronicsProgramFeature) AgentSystemRole() string {
	return strings.Join([]string{
		"You are a hardware program intelligence agent embedded in an electronics engineering or product validation system.",
		"Your role is to execute test tracking, debug ticket creation, and program risk escalation workflows",
		"triggered by validation failures, firmware regression signals, or hardware bring-up blockers",
		"identified from engineering group communications.",
		"You have access to test management systems, issue trackers, firmware CI pipelines, and program status dashboards.",
		"Execute only the listed actions. Do not modify test results or close tickets autonomously.",
	}, " ")
}

func (ElectronicsProgramFeature) Analyze(ctx FeatureContext) string {
	insights := []string{}
	if hasAny(ctx.Summary, "molecule", "lab", "test") {
		insights = append(insights, "Detected R&D experiment context; recommend test artifact tracking and validation checkpoints.")
	}
	if hasAny(ctx.Summary, "firmware", "board", "sensor", "chip", "signal") {
		insights = append(insights, "Detected hardware/firmware dependency risk from summarized chat.")
	}
	if len(insights) == 0 {
		insights = append(insights, "No explicit electronics fault signatures found; maintain bring-up checklist and test coverage review.")
	}
	return strings.Join(insights, "\n")
}

func (ElectronicsProgramFeature) PlanActions(ctx FeatureContext) []ActionCommand {
	actions := []ActionCommand{}
	if hasAny(ctx.Summary, "test", "validation", "failure") {
		actions = append(actions, ActionCommand{
			Name:            "open_validation_task",
			Params:          map[string]string{"owner": "qa"},
			Description:     "Create a validation tracking task in the test management system for the identified test failure or coverage gap, assigned to QA.",
			Preconditions:   "Test case ID or failure description is extractable from the summarized chat.",
			SuccessCriteria: "Task created in the test management system with the failure details attached and QA owner notified.",
			Fallback:        "Log validation gap in the program risk register and notify the hardware lead.",
		})
	}
	if hasAny(ctx.Summary, "bug", "defect", "issue") {
		actions = append(actions, ActionCommand{
			Name:            "create_debug_ticket",
			Params:          map[string]string{"priority": "p1"},
			Description:     "Open a P1 debug ticket in the issue tracker with the defect description, affected hardware revision, and firmware version from the chat context.",
			Preconditions:   "Defect description is present in the summarized chat; hardware rev and firmware version are identifiable.",
			SuccessCriteria: "Ticket created, triaged to the correct engineering sub-team, and acknowledged within 2 hours.",
			Fallback:        "Send a structured defect report via the engineering escalation channel.",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, ActionCommand{
			Name:            "sync_program_status_report",
			Params:          map[string]string{"cadence": "daily"},
			Description:     "Generate and distribute the daily program status report based on the current validation, debug, and milestone state from summarized chat.",
			Preconditions:   "Chat summary contains at least one program milestone, test status, or team update.",
			SuccessCriteria: "Status report distributed to all program stakeholders and filed in the program tracker.",
			Fallback:        "Post a plain summary to the program channel with a note that the automated report sync failed.",
		})
	}
	return actions
}
