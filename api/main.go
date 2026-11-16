// Copyright 2026 Harish, KokoHq Inc.
//
// Licensed under the MIT License. See LICENSE file for details.

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"
)

//docker tag whatsapp-webhook-test gcr.io/<your-gcp-project-id>/whatsapp-webhook:latest

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	handler := NewWebhookHandler()
	handler.StartQuotaChecker()
	handler.store.StartRetentionEnforcer()
	handler.richMediaQueue.Start(handler)

	// Metrics collector — samples CPU/memory/message counts every 60 s.
	globalMetrics = NewMetricsCollector(handler.store)
	globalMetrics.StartSampler()

	// Record this service startup so the daily report can identify cold starts.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := handler.store.InsertLifecycleEvent(ctx, "startup", "koko service started"); err != nil {
			log.Printf("LIFECYCLE_EVENT_PERSIST_FAILED: %v", err)
		}
	}()

	// Daily usage report — emailed to REPORT_EMAIL_TO at REPORT_HOUR_UTC (default 07:00 UTC).
	StartDailyReporter(handler.store)

	// Pre-warm the DB connection pool so the first koko:help (or any koko: command)
	// responds immediately rather than incurring cold-start connection latency.
	go handler.warmupDBConnection()

	// Async job queue — 16 workers, 256-item buffer, completed jobs expire after 24 h
	// Sized for 50 concurrent requests × sustained load; workers process Vertex AI + DB tasks.
	globalJobQueue = NewJobQueue(16, 256)
	globalJobQueue.StartExpirer(context.Background(), 24*time.Hour)

	// GraphQL schema (must come after handler/store is ready)
	initGraphQLSchema(handler)

	// ── Messenger webhook routes (provider-scoped policy) ─────────────────────
	// Primary policy:
	//   WhatsApp/Meta: /meta/whatsapp/webhook
	//   Telegram:      /telegram/webhook
	//   Slack:         /slack/webhook
	// Legacy aliases are kept for backward compatibility.

	// WhatsApp (Meta) primary webhook route.
	http.HandleFunc("/meta/whatsapp/webhook", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.VerifyWebhook(w, r)
		case http.MethodPost:
			handler.HandleMessages(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Legacy/short WhatsApp aliases.
	http.HandleFunc("/whatsapp/webhook", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.VerifyWebhook(w, r)
		case http.MethodPost:
			handler.HandleMessages(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.VerifyWebhook(w, r)
		case http.MethodPost:
			handler.HandleMessages(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Dedicated WhatsApp listener path for Meta webhook configuration.
	// Kept in addition to /webhook for backward compatibility.
	http.HandleFunc("/whatsapp-webhook", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.VerifyWebhook(w, r)
		case http.MethodPost:
			handler.HandleMessages(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Telegram primary webhook route.
	http.HandleFunc("/telegram/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.HandleTelegramWebhook(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Slack primary webhook route.
	http.HandleFunc("/slack/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.HandleSlackWebhook(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Snapchat webhook route (reserved for upcoming integration).
	// Keeps provider-specific URL naming consistent across integrations.
	http.HandleFunc("/snapchat/webhook", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"status":"not_implemented","provider":"snapchat"}`))
	})

	http.HandleFunc("/slack-webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.HandleSlackWebhook(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/telegram-webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.HandleTelegramWebhook(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// ── REST API routes (auth + rate limit middleware applied) ─────────────────

	http.HandleFunc("/api/input", handler.apiAuthMiddleware(handler.handleAPIInput))
	http.HandleFunc("/api/summaries", handler.apiAuthMiddleware(handler.handleGetSummaries))
	http.HandleFunc("/api/act-prompts", handler.apiAuthMiddleware(handler.handleGetActPrompts))
	http.HandleFunc("/api/v1/documents", handler.apiAuthMiddleware(handler.handleDocumentUpload))
	http.HandleFunc("/api/config", handler.HandleConfigAPI)
	http.HandleFunc("/api/config/", handler.HandleConfigAPI)

	// Natural language query endpoint — accepts free-form English questions
	http.HandleFunc("/api/nlq", handler.apiAuthMiddleware(handler.handleAPIQuery))

	// Job polling — no auth required (job IDs are unguessable 24-hex strings)
	http.HandleFunc("/api/jobs/", handleGetJob)

	// ── GraphQL endpoint ───────────────────────────────────────────────────────

	http.HandleFunc("/graphql", handler.apiAuthMiddleware(handler.handleGraphQL))

	// ── MCP (Model Context Protocol) endpoint ─────────────────────────────────
	// Exposes koko as an MCP server — connects to AI clients (Claude Desktop,
	// Cursor, custom agents, etc.) via JSON-RPC 2.0 over HTTP.
	// Tools: query_summaries, query_act_prompts, natural_language_query, get_koko_help

	http.HandleFunc("/mcp", handler.apiAuthMiddleware(handler.handleMCP))
	http.HandleFunc("/api/mcp-client/invoke", handler.apiAuthMiddleware(handler.handleMCPClientInvoke))

	// ── Health check ──────────────────────────────────────────────────────────

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// ── Public legal pages (for platform compliance/app review) ──────────────
	http.HandleFunc("/privacy-policy", handlePrivacyPolicy)
	http.HandleFunc("/legal/privacy", handlePrivacyPolicy)
	http.HandleFunc("/privacy-policy.txt", handlePrivacyPolicyText)
	http.HandleFunc("/telegram/privacy-policy", handlePrivacyPolicy)
	http.HandleFunc("/terms-of-service", handleTermsOfService)
	http.HandleFunc("/legal/terms", handleTermsOfService)
	http.HandleFunc("/telegram/terms-of-service", handleTermsOfService)
	http.HandleFunc("/user-data-deletion", handleUserDataDeletionInfo)
	http.HandleFunc("/legal/data-deletion", handleUserDataDeletionInfo)
	http.HandleFunc("/telegram/user-data-deletion", handleUserDataDeletionInfo)
	http.HandleFunc("/meta/data-deletion", handleMetaDataDeletionCallback)

	log.Printf("Server starting on port %s", port)
	log.Printf("API endpoints: POST /api/input  GET /api/summaries  GET /api/act-prompts  POST /graphql  POST /api/v1/documents")
	log.Printf("TLS: handled by Cloud Run platform (HTTPS termination); Telegram secret-token validation enforced")

	log.Fatal(http.ListenAndServe(":"+port, nil))
}
