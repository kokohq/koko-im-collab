package main

import "strings"

type IndustrialOpsFeature struct{}

func (IndustrialOpsFeature) Mode() string {
	return featureModeIndustrial
}

func (IndustrialOpsFeature) Title() string {
	return "Industrial Ops"
}

func (IndustrialOpsFeature) AgentSystemRole() string {
	return strings.Join([]string{
		"You are an industrial operations AI agent embedded in a plant management or manufacturing execution system.",
		"Your role is to execute time-sensitive operational workflows triggered by safety alerts, equipment faults,",
		"maintenance escalations, and shift handover gaps identified from group communications.",
		"You have access to CMMS, SCADA event logs, ERP work order APIs, and safety incident reporting systems.",
		"Always apply the principle of least privilege: execute only the actions listed. Do not infer or expand scope.",
	}, " ")
}

func (IndustrialOpsFeature) Analyze(ctx FeatureContext) string {
	issues := []string{}
	if hasAny(ctx.Summary, "downtime", "fault", "alarm", "safety", "incident", "maintenance") {
		issues = append(issues, "Detected operational risk and maintenance or safety signals in summarized chat.")
	}
	if hasAny(ctx.Summary, "telescope", "scientist", "resource", "staff", "operator") {
		issues = append(issues, "Detected staffing/resource gap impacting execution.")
	}
	if len(issues) == 0 {
		issues = append(issues, "No explicit industrial fault keywords found; continue monitoring line status and pending tasks.")
	}
	return strings.Join(issues, "\n")
}

func (IndustrialOpsFeature) PlanActions(ctx FeatureContext) []ActionCommand {
	actions := []ActionCommand{}
	if hasAny(ctx.Summary, "fault", "alarm", "downtime") {
		actions = append(actions, ActionCommand{
			Name:            "activate_diagnostic_sequence",
			Params:          map[string]string{"priority": "high"},
			Description:     "Initiate a structured equipment diagnostic sequence to isolate the root cause of the reported fault or alarm.",
			Preconditions:   "Equipment is in a safe-to-diagnose state; no active lockout/tagout in progress.",
			SuccessCriteria: "Diagnostic sequence completes and returns a fault classification with severity level.",
			Fallback:        "Escalate to maintenance supervisor and log a manual inspection request in the CMMS.",
		})
	}
	if hasAny(ctx.Summary, "maintenance", "repair") {
		actions = append(actions, ActionCommand{
			Name:            "create_maintenance_work_order",
			Params:          map[string]string{"sla": "4h"},
			Description:     "Create a corrective maintenance work order in the CMMS with 4-hour SLA, linking the fault signal from the summarized chat.",
			Preconditions:   "Asset ID is resolvable from the group context or fault code.",
			SuccessCriteria: "Work order created, assigned to on-call maintenance technician, and acknowledged within 30 minutes.",
			Fallback:        "Send a direct maintenance request to the shift supervisor's contact channel with fault details.",
		})
	}
	if hasAny(ctx.Summary, "safety", "incident", "risk") {
		actions = append(actions, ActionCommand{
			Name:            "trigger_safety_inspection",
			Params:          map[string]string{"severity": "critical"},
			Description:     "Initiate a formal safety inspection workflow for the affected area or equipment, flagged at critical severity.",
			Preconditions:   "Incident or risk signal is confirmed from at least one named source in the summarized chat.",
			SuccessCriteria: "Inspection request is logged in the safety incident system and assigned to a qualified safety officer.",
			Fallback:        "Notify HSE team lead directly and create a manual incident report entry.",
		})
	}
	if len(actions) == 0 {
		actions = append(actions, ActionCommand{
			Name:            "capture_shift_handover",
			Params:          map[string]string{"mode": "monitor"},
			Description:     "Generate a structured shift handover document from the summarized chat, capturing open items, watch items, and confirmed decisions.",
			Preconditions:   "Chat summary is non-empty and covers at least one operational shift period.",
			SuccessCriteria: "Handover document posted to the ops channel and acknowledged by the incoming shift lead.",
			Fallback:        "Deliver handover summary as a plain-text message to the incoming shift supervisor.",
		})
	}
	return actions
}
