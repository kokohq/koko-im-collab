// Copyright 2026 Harish, KokoHq Inc.
//
// Licensed under the MIT License. See LICENSE file for details.

package main

import "strings"

type ConsumerLifeAdminFeature struct{}

func (ConsumerLifeAdminFeature) Mode() string {
	return featureModeConsumer
}

func (ConsumerLifeAdminFeature) Title() string {
	return "Consumer Life Admin Assistant"
}

func (ConsumerLifeAdminFeature) AgentSystemRole() string {
	return strings.Join([]string{
		"You are a personal productivity AI agent operating within a life-admin or personal task management system.",
		"Your role is to extract actionable tasks, schedule follow-ups, and organize personal commitments",
		"identified from individual or small-group personal communications.",
		"You have access to personal task managers, calendar systems, and reminder services.",
		"Prioritize privacy: do not forward personal data beyond the user's own connected systems.",
	}, " ")
}

func (ConsumerLifeAdminFeature) Analyze(ctx FeatureContext) string {
	insights := []string{}
	if hasAny(ctx.Summary, "need", "todo", "action") {
		insights = append(insights, "Detected actionable personal tasks in summarized chat.")
	}
	if hasAny(ctx.Summary, "date", "schedule", "meeting", "project") {
		insights = append(insights, "Detected schedule planning opportunities from summarized context.")
	}
	if len(insights) == 0 {
		insights = append(insights, "No immediate personal planning risks detected; summarize into reminders for follow-up.")
	}
	return strings.Join(insights, "\n")
}

func (ConsumerLifeAdminFeature) PlanActions(ctx FeatureContext) []ActionCommand {
	actions := []ActionCommand{}
	if hasAny(ctx.Summary, "need", "action", "todo") {
		actions = append(actions, ActionCommand{
			Name:            "create_personal_task_list",
			Params:          map[string]string{"priority": "normal"},
			Description:     "Extract all actionable tasks mentioned in the summarized chat and create a personal task list with owner, deadline, and context for each item.",
			Preconditions:   "At least one actionable item is identifiable in the summarized chat.",
			SuccessCriteria: "Task list created in the personal task manager with each item confirmed as discrete and actionable.",
			Fallback:        "Deliver a formatted plain-text task list to the user via the current messenger channel.",
		})
	}
	if hasAny(ctx.Summary, "date", "meeting", "schedule") {
		actions = append(actions, ActionCommand{
			Name:            "schedule_follow_up_reminder",
			Params:          map[string]string{"window": "24h"},
			Description:     "Create calendar reminders for any scheduled events, meetings, or deadlines identified in the summarized chat, set 24 hours in advance.",
			Preconditions:   "At least one date, time, or scheduling reference is present in the summarized chat.",
			SuccessCriteria: "Reminder created in the user's calendar and confirmed with the user.",
			Fallback:        "Send a direct reminder message to the user on the current messenger channel at the appropriate time.",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, ActionCommand{
			Name:            "save_chat_digest",
			Params:          map[string]string{"bucket": "weekly"},
			Description:     "Archive a structured weekly digest of the summarized chat into the user's personal knowledge base for future retrieval.",
			Preconditions:   "Chat summary is non-empty and covers meaningful personal content.",
			SuccessCriteria: "Digest saved and indexed in the weekly archive bucket with tags for retrieval.",
			Fallback:        "Send the digest as a formatted message to the user for manual saving.",
		})
	}
	return actions
}
