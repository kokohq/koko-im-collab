// Copyright 2026 Harish, KokoHq Inc.
//
// Licensed under the MIT License. See LICENSE file for details.
//
// This file implements webhook handlers for WhatsApp Business API and Slack Events API.
// Implementation follows standard webhook security practices and API specifications.
// Citations for specific API implementations are provided inline with relevant functions.

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxLoggedWebhookBodyBytes = 4096

const (
	maxTelegramCacheMessages  = 103
	maxTelegramCacheBytes     = 2 * 1024 * 1024
	maxTelegramTextMessages   = 100
	maxTelegramAudioMessages  = 2
	maxTelegramVideoMessages  = 1
	maxSummaryLogChunkBytes   = 3500
	maxTelegramSendChunkBytes = 3800
	maxVertexAIInlineBytes    = 15 * 1024 * 1024
	maxSummaryPromptEvents    = 60
	maxTelegramFetchAttempts  = 4
	telegramSummarizeCommand  = "koko:summarize"
	telegramSummaryAlias      = "koko:summary"
	telegramSummarizeLegacy   = "koko:summ"
	vertexAIDefaultLocation   = "us-west2"
	vertexAIDefaultModel      = "gemini-2.0-flash"
	googleMetadataTokenURL    = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	googleMetadataProjectURL  = "http://metadata.google.internal/computeMetadata/v1/project/project-id"
)

type CachedTelegramMessage struct {
	Type      string
	Content   string
	MediaID   string
	MimeType  string
	Timestamp time.Time
}

type encryptedCachedTelegramMessage struct {
	Type             string
	EncryptedContent string
	EncryptedMediaID string
	EncryptedMime    string
	Timestamp        time.Time
}

type TelegramMessageCache struct {
	mu         sync.Mutex
	messages   []encryptedCachedTelegramMessage
	totalBytes int
}

func estimateCachedTelegramMessageBytes(msg CachedTelegramMessage) int {
	return len(msg.Type) + len(msg.Content) + len(msg.MediaID) + len(msg.MimeType) + 64
}

func estimateEncryptedCachedTelegramMessageBytes(msg encryptedCachedTelegramMessage) int {
	return len(msg.Type) + len(msg.EncryptedContent) + len(msg.EncryptedMediaID) + len(msg.EncryptedMime) + 64
}

func (c *TelegramMessageCache) Add(msg CachedTelegramMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	encryptedContent, err := encryptStringValue(msg.Content)
	if err != nil {
		return err
	}
	encryptedMediaID, err := encryptStringValue(msg.MediaID)
	if err != nil {
		return err
	}
	encryptedMime, err := encryptStringValue(msg.MimeType)
	if err != nil {
		return err
	}

	encMsg := encryptedCachedTelegramMessage{
		Type:             msg.Type,
		EncryptedContent: encryptedContent,
		EncryptedMediaID: encryptedMediaID,
		EncryptedMime:    encryptedMime,
		Timestamp:        msg.Timestamp,
	}

	msgSize := estimateCachedTelegramMessageBytes(msg)
	c.messages = append(c.messages, encMsg)
	c.totalBytes += msgSize

	c.pruneToPolicyUnsafe()
	return nil
}

func (c *TelegramMessageCache) pruneToPolicyUnsafe() {
	for {
		textCount := 0
		audioCount := 0
		videoCount := 0
		textOverflowIndex := -1
		audioOverflowIndex := -1
		videoOverflowIndex := -1

		for idx, msg := range c.messages {
			switch msg.Type {
			case "text":
				textCount++
				if textCount > maxTelegramTextMessages && textOverflowIndex == -1 {
					textOverflowIndex = idx
				}
			case "audio":
				audioCount++
				if audioCount > maxTelegramAudioMessages && audioOverflowIndex == -1 {
					audioOverflowIndex = idx
				}
			case "video":
				videoCount++
				if videoCount > maxTelegramVideoMessages && videoOverflowIndex == -1 {
					videoOverflowIndex = idx
				}
			}
		}

		removeIndex := -1
		switch {
		case textOverflowIndex >= 0:
			removeIndex = textOverflowIndex
		case audioOverflowIndex >= 0:
			removeIndex = audioOverflowIndex
		case videoOverflowIndex >= 0:
			removeIndex = videoOverflowIndex
		case len(c.messages) > maxTelegramCacheMessages:
			removeIndex = 0
		case c.totalBytes > maxTelegramCacheBytes:
			removeIndex = 0
		default:
			return
		}

		removed := c.messages[removeIndex]
		c.messages = append(c.messages[:removeIndex], c.messages[removeIndex+1:]...)
		c.totalBytes -= estimateEncryptedCachedTelegramMessageBytes(removed)
		if c.totalBytes < 0 {
			c.totalBytes = 0
		}
	}
}

func (c *TelegramMessageCache) Snapshot() []CachedTelegramMessage {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]CachedTelegramMessage, 0, len(c.messages))
	for _, msg := range c.messages {
		decryptedContent, err := decryptStringValue(msg.EncryptedContent)
		if err != nil {
			decryptedContent = ""
		}
		decryptedMediaID, err := decryptStringValue(msg.EncryptedMediaID)
		if err != nil {
			decryptedMediaID = ""
		}
		decryptedMime, err := decryptStringValue(msg.EncryptedMime)
		if err != nil {
			decryptedMime = ""
		}
		result = append(result, CachedTelegramMessage{
			Type:      msg.Type,
			Content:   decryptedContent,
			MediaID:   decryptedMediaID,
			MimeType:  decryptedMime,
			Timestamp: msg.Timestamp,
		})
	}
	return result
}

func (c *TelegramMessageCache) Stats() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.messages), c.totalBytes
}

// WhatsAppWebhookPayload represents the incoming webhook payload
// Based on WhatsApp Business API webhook documentation:
// https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks
type WhatsAppWebhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Value struct {
				MessagingProduct string `json:"messaging_product"`
				Metadata         struct {
					DisplayPhoneNumber string `json:"display_phone_number"`
					PhoneNumberID      string `json:"phone_number_id"`
				} `json:"metadata"`
				Contacts []struct {
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
					WAID string `json:"wa_id"`
				} `json:"contacts"`
				Messages []WhatsAppMessage `json:"messages"`
			} `json:"value"`
			Field string `json:"field"`
		} `json:"changes"`
	} `json:"entry"`
}

// WhatsAppMessage represents a WhatsApp message
type WhatsAppMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Context   *struct {
		From string `json:"from"`
		ID   string `json:"id"`
	} `json:"context,omitempty"`
	Text *struct {
		Body string `json:"body"`
	} `json:"text,omitempty"`
	Audio *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
	} `json:"audio,omitempty"`
	Video *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		Caption  string `json:"caption,omitempty"`
	} `json:"video,omitempty"`
	Image *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		Caption  string `json:"caption,omitempty"`
	} `json:"image,omitempty"`
	Document *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		Caption  string `json:"caption,omitempty"`
		Filename string `json:"filename,omitempty"`
	} `json:"document,omitempty"`
}

// SlackWebhookPayload represents the incoming Slack webhook payload
// Based on Slack Events API documentation:
// https://api.slack.com/events-api
type SlackWebhookPayload struct {
	Token     string     `json:"token"`
	TeamID    string     `json:"team_id"`
	APIAppID  string     `json:"api_app_id"`
	Event     SlackEvent `json:"event"`
	Type      string     `json:"type"`
	EventID   string     `json:"event_id"`
	EventTime int64      `json:"event_time"`
	Challenge string     `json:"challenge,omitempty"` // For URL verification
}

// SlackEvent represents a Slack event
type SlackEvent struct {
	Type      string      `json:"type"`
	Channel   string      `json:"channel"`
	User      string      `json:"user"`
	Text      string      `json:"text"`
	Timestamp string      `json:"ts"`
	EventTS   string      `json:"event_ts"`
	Files     []SlackFile `json:"files,omitempty"`
}

// SlackFile represents a file attachment in Slack
type SlackFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	Mimetype   string `json:"mimetype"`
	Filetype   string `json:"filetype"`
	URLPrivate string `json:"url_private"`
	Size       int    `json:"size"`
}

// TelegramUpdate represents Telegram update webhook payload
// Based on Telegram Bot API documentation: https://core.telegram.org/bots/api#update
type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message,omitempty"`
}

// TelegramMessage represents Telegram message object
type TelegramMessage struct {
	MessageID int64 `json:"message_id"`
	From      struct {
		ID           int64  `json:"id"`
		IsBot        bool   `json:"is_bot"`
		FirstName    string `json:"first_name"`
		LastName     string `json:"last_name,omitempty"`
		Username     string `json:"username,omitempty"`
		LanguageCode string `json:"language_code,omitempty"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Date     int64             `json:"date"`
	Text     string            `json:"text,omitempty"`
	Audio    *TelegramAudio    `json:"audio,omitempty"`
	Video    *TelegramVideo    `json:"video,omitempty"`
	Voice    *TelegramVoice    `json:"voice,omitempty"`
	Document *TelegramDocument `json:"document,omitempty"`
}

// Telegram media structs

type TelegramAudio struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type TelegramVideo struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type TelegramVoice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

type TelegramDocument struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

// ProcessedMessage represents a processed message for internal use
type ProcessedMessage struct {
	ID            string
	From          string
	AccountID     string
	GroupName     string
	UserName      string
	ChatID        string
	MessengerType string
	Type          string
	Content       string
	Timestamp     time.Time
	MediaID       string
	MimeType      string
	Caption       string
	Filename      string
	Files         []SlackFile
}

// WebhookHandler handles WhatsApp webhook requests
type WebhookHandler struct {
	verifyToken         string
	accessToken         string
	appSecret           string
	whatsappPhoneID     string
	whatsappMTLSRequire bool
	whatsappMTLSCN      string
	slackSigningSecret  string
	telegramSecretToken string
	telegramBotToken    string
	googleCloudProject  string
	vertexAILocation    string
	vertexAIModel       string
	httpClient          *http.Client
	store               *AppStore
	telegramCache       *TelegramMessageCache
	featureModules      []FeatureModule
	menuMu              sync.Mutex
	menuSentByChat      map[int64]bool
	tcNoticeMu          sync.Mutex
	tcNoticeSentByChat  map[int64]bool
	tcNoticeSentByKey   map[string]bool // non-Telegram messengers keyed by sender ID
	chatStateMu         sync.Mutex
	chatStateByChat     map[int64]*ChatCommandState
	chatMessengerByChat map[int64]string
	groupLastDomain     map[string]string
	cachedHelpText      string
	richMediaQueue      *RichMediaQueue
	messengerAdapters   map[string]MessengerAdapter
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler() *WebhookHandler {
	googleCloudProject := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if googleCloudProject == "" {
		googleCloudProject = os.Getenv("GCP_PROJECT")
	}

	vertexAILocation := os.Getenv("VERTEX_AI_LOCATION")
	if vertexAILocation == "" {
		vertexAILocation = vertexAIDefaultLocation
	}

	vertexAIModel := os.Getenv("VERTEX_AI_MODEL")
	if vertexAIModel == "" {
		vertexAIModel = vertexAIDefaultModel
	}

	h := &WebhookHandler{
		verifyToken:         os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		accessToken:         os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		appSecret:           os.Getenv("WHATSAPP_APP_SECRET"),
		whatsappPhoneID:     os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		whatsappMTLSRequire: strings.EqualFold(strings.TrimSpace(os.Getenv("WHATSAPP_MTLS_REQUIRED")), "true") || strings.TrimSpace(os.Getenv("WHATSAPP_MTLS_REQUIRED")) == "1",
		whatsappMTLSCN:      defaultOrValue(strings.TrimSpace(os.Getenv("WHATSAPP_MTLS_CLIENT_CN")), "client.webhooks.fbclientcerts.com"),
		slackSigningSecret:  os.Getenv("SLACK_SIGNING_SECRET"),
		telegramSecretToken: os.Getenv("TELEGRAM_SECRET_TOKEN"),
		telegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		googleCloudProject:  googleCloudProject,
		vertexAILocation:    vertexAILocation,
		vertexAIModel:       vertexAIModel,
		httpClient:          &http.Client{Timeout: 60 * time.Second},
		store:               NewAppStoreFromEnv(),
		telegramCache:       &TelegramMessageCache{},
		featureModules:      selectedFeatureModules(),
		menuSentByChat:      map[int64]bool{},
		tcNoticeSentByChat:  map[int64]bool{},
		tcNoticeSentByKey:   map[string]bool{},
		chatStateByChat:     map[int64]*ChatCommandState{},
		chatMessengerByChat: map[int64]string{},
		groupLastDomain:     map[string]string{},
		cachedHelpText:      buildKokoCommandMenu(),
		richMediaQueue:      newRichMediaQueue(),
	}
	h.initMessengerAdapters()
	return h
}

// VerifyWebhook handles GET requests for webhook verification
// Based on WhatsApp Business API webhook setup documentation:
// https://developers.facebook.com/docs/whatsapp/cloud-api/get-started#webhook-setup
func (h *WebhookHandler) VerifyWebhook(w http.ResponseWriter, r *http.Request) {
	if err := h.verifyWhatsAppMTLS(r); err != nil {
		log.Printf("WHATSAPP_MTLS_VERIFY_FAILED: %v", err)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	if mode == "subscribe" && token == h.verifyToken {
		log.Printf("WEBHOOK_VERIFIED")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(challenge))
		return
	}

	log.Printf("WEBHOOK_VERIFICATION_FAILED")
	http.Error(w, "Forbidden", http.StatusForbidden)
}

// warmupDBConnection fires a lightweight DB lookup shortly after startup to
// pre-establish the connection pool and warm the PostgreSQL query-plan cache
// for the user-group JOIN. This prevents the first "koko:" help request from
// experiencing cold-start latency.
func (h *WebhookHandler) warmupDBConnection() {
	time.Sleep(3 * time.Second)
	if h.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Run the exact query used by buildTelegramCommandMenuForUser.
	// The user "__warmup__" will not match anything; the round-trip is for
	// connection + query-plan warming only.
	_, _, _ = h.store.LookupUserGroupContext(ctx, "__warmup__", "telegram")
	log.Printf("KOKO_DB_WARMUP_COMPLETE")
}

// sendFirstTimeTermsNotice sends the koko Terms of Use followed by the
// personalised command menu to a chat on their very first message. It
// uses tcNoticeSentByChat as a fast-path in-memory cache and falls back
// to a DB flag (account_users.tou_sent_at) so the notice survives server
// restarts and multiple instances.
// Returns true if the notice was actually sent (first interaction).
// userName, accountID and groups are passed in from the already-resolved
// auth result to avoid a second DB round-trip.
func (h *WebhookHandler) sendFirstTimeTermsNotice(chatID int64, userName, accountID string, groups []Group) bool {
	// ── Fast path: in-memory cache (same instance, same boot) ───────────────
	h.tcNoticeMu.Lock()
	alreadySent := h.tcNoticeSentByChat[chatID]
	h.tcNoticeMu.Unlock()
	if alreadySent {
		return false
	}

	// ── Persistent path: check DB flag so restarts/multi-instances are safe ─
	if h.store != nil && strings.TrimSpace(accountID) != "" && strings.TrimSpace(userName) != "" {
		dbSent, _ := h.store.HasUserTOUSent(context.Background(), accountID, userName)
		if dbSent {
			// Warm the in-memory cache to skip DB on next message.
			h.tcNoticeMu.Lock()
			h.tcNoticeSentByChat[chatID] = true
			h.tcNoticeMu.Unlock()
			return false
		}
	}

	// ── Mark as sent in memory and DB before delivering ─────────────────────
	h.tcNoticeMu.Lock()
	h.tcNoticeSentByChat[chatID] = true
	h.menuMu.Lock()
	h.menuSentByChat[chatID] = true
	h.menuMu.Unlock()
	h.tcNoticeMu.Unlock()

	if h.store != nil && strings.TrimSpace(accountID) != "" && strings.TrimSpace(userName) != "" {
		_ = h.store.MarkUserTOUSent(context.Background(), accountID, userName)
	}

	// Combine T&C + command menu into one message to reduce chat noise.
	notice := buildKokoTermsAndConditions() + "\n\n" + buildKokoCommandMenuWithContext(accountID, groups)
	_ = h.sendTelegramMessage(chatID, notice)
	return true
}

// sendFirstTimeTermsNoticeByKey is the string-keyed equivalent of
// sendFirstTimeTermsNotice for non-Telegram messengers (WhatsApp, Slack).
// Since there is no outbound API for those platforms, the notice is only
// logged; callers can use the return value to suppress redundant processing.
func (h *WebhookHandler) sendFirstTimeTermsNoticeByKey(key, accountID string, groups []Group) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	h.tcNoticeMu.Lock()
	alreadySent := h.tcNoticeSentByKey[key]
	if !alreadySent {
		h.tcNoticeSentByKey[key] = true
	}
	h.tcNoticeMu.Unlock()

	if alreadySent {
		return false
	}
	log.Printf("TC_NOTICE_FIRST_INTERACTION: key=%s account=%s", key, accountID)
	return true
}

// HandleMessages handles POST requests with incoming messages
// Based on WhatsApp Business API webhook implementation guide:
// https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks#receive-message
func (h *WebhookHandler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	if err := h.verifyWhatsAppMTLS(r); err != nil {
		log.Printf("WHATSAPP_MTLS_VERIFY_FAILED: %v", err)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Verify signature if app secret is provided
	if h.appSecret != "" {
		if !h.verifySignature(r) {
			log.Printf("SIGNATURE_VERIFICATION_FAILED")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("ERROR reading request body: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse webhook payload
	var payload WhatsAppWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("ERROR parsing webhook payload: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Respond immediately with 200 OK
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))

	// Process messages asynchronously; track concurrent processing goroutines.
	if globalMetrics != nil {
		globalMetrics.IncConcurrent()
	}
	go func() {
		if globalMetrics != nil {
			defer globalMetrics.DecConcurrent()
		}
		h.processMessages(payload)
	}()
}

func parseBoolHeader(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "true" || normalized == "1" || normalized == "yes"
}

func extractCNFromXFCC(xfcc string) string {
	trimmed := strings.TrimSpace(xfcc)
	if trimmed == "" {
		return ""
	}

	// Common Envoy/XFCC shape: Subject="CN=client.webhooks.fbclientcerts.com"
	key := "CN="
	idx := strings.Index(trimmed, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	if start >= len(trimmed) {
		return ""
	}

	end := start
	for end < len(trimmed) {
		ch := trimmed[end]
		if ch == ',' || ch == ';' || ch == '"' {
			break
		}
		end++
	}

	return strings.TrimSpace(trimmed[start:end])
}

func (h *WebhookHandler) verifyWhatsAppMTLS(r *http.Request) error {
	if !h.whatsappMTLSRequire {
		return nil
	}

	// Expected to be injected by an mTLS-terminating load balancer.
	present := parseBoolHeader(r.Header.Get("X-Client-Cert-Present"))
	verified := parseBoolHeader(r.Header.Get("X-Client-Cert-Verified"))
	cn := strings.TrimSpace(r.Header.Get("X-Client-Cert-CN"))
	if cn == "" {
		cn = extractCNFromXFCC(r.Header.Get("X-Forwarded-Client-Cert"))
	}

	if !present {
		return fmt.Errorf("client certificate not present")
	}
	if !verified {
		return fmt.Errorf("client certificate not verified")
	}
	if !strings.EqualFold(cn, h.whatsappMTLSCN) {
		return fmt.Errorf("unexpected client certificate CN: got=%q expected=%q", cn, h.whatsappMTLSCN)
	}

	return nil
}

// verifySignature verifies the X-Hub-Signature-256 header
// Based on WhatsApp Business API webhook security documentation:
// https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks#webhook-security
func (h *WebhookHandler) verifySignature(r *http.Request) bool {
	signature := r.Header.Get("X-Hub-Signature-256")
	if signature == "" {
		return false
	}

	// Remove "sha256=" prefix
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	signature = strings.TrimPrefix(signature, "sha256=")

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	// Restore body for later reading
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Calculate expected signature
	mac := hmac.New(sha256.New, []byte(h.appSecret))
	mac.Write(body)
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

// verifySlackSignature verifies the X-Slack-Signature header
// Based on Slack Events API security documentation:
// https://api.slack.com/authentication/verifying-requests-from-slack
func (h *WebhookHandler) verifySlackSignature(r *http.Request) bool {
	signature := r.Header.Get("X-Slack-Signature")
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")

	if signature == "" || timestamp == "" {
		return false
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	// Restore body for later reading
	r.Body = io.NopCloser(bytes.NewReader(body))

	// Create the basestring
	basestring := "v0:" + timestamp + ":" + string(body)

	// Calculate expected signature
	mac := hmac.New(sha256.New, []byte(h.slackSigningSecret))
	mac.Write([]byte(basestring))
	expectedSignature := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

// verifyTelegramSecret verifies X-Telegram-Bot-Api-Secret-Token
// Based on Telegram Bot API security docs:
// https://core.telegram.org/bots/api#setwebhook
func (h *WebhookHandler) verifyTelegramSecret(r *http.Request) bool {
	if h.telegramSecretToken == "" {
		return true
	}
	return r.Header.Get("X-Telegram-Bot-Api-Secret-Token") == h.telegramSecretToken
}

func truncateForLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}

	return value[:limit] + "...[truncated]"
}

func isTelegramSummarizeCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), telegramSummarizeCommand)
}

func logTelegramUserMessage(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	// Escape newlines for single-line log output while preserving content in cache
	escapedText := strings.ReplaceAll(trimmed, "\n", "\\n")
	log.Printf("TELEGRAM_USER_MESSAGE: %s", escapedText)
}

func logTelegramMediaInput(messageType string, contentSize int, timestamp int64, user string) {
	log.Printf(
		"TELEGRAM_MEDIA_INPUT: type=%s content_size=%d date=%s user=%s",
		messageType,
		contentSize,
		time.Unix(timestamp, 0).UTC().Format(time.RFC3339),
		user,
	)
}

func (h *WebhookHandler) logSummaryResult(summary string) {
	if summary == "" {
		log.Printf("TELEGRAM_SUMMARIZATION_RESULT: <empty>")
		return
	}

	for i := 0; i < len(summary); i += maxSummaryLogChunkBytes {
		end := i + maxSummaryLogChunkBytes
		if end > len(summary) {
			end = len(summary)
		}

		chunkIndex := (i / maxSummaryLogChunkBytes) + 1
		chunkTotal := (len(summary) + maxSummaryLogChunkBytes - 1) / maxSummaryLogChunkBytes
		chunk := strings.ReplaceAll(summary[i:end], "\n", "\\n")
		log.Printf("TELEGRAM_SUMMARIZATION_RESULT_CHUNK %d/%d: %s", chunkIndex, chunkTotal, chunk)
	}
}

// HandleSlackWebhook handles POST requests with incoming Slack events
// Based on Slack Events API receiving events documentation:
// https://api.slack.com/events-api#receiving_events
func (h *WebhookHandler) HandleSlackWebhook(w http.ResponseWriter, r *http.Request) {
	// Verify signature if signing secret is provided
	if h.slackSigningSecret != "" {
		if !h.verifySlackSignature(r) {
			log.Printf("SLACK_SIGNATURE_VERIFICATION_FAILED")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("ERROR reading request body: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse webhook payload
	var payload SlackWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("ERROR parsing Slack webhook payload: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Handle URL verification
	// Based on Slack Events API URL verification documentation:
	// https://api.slack.com/events-api#the-events-api__subscribing-to-event-types__events-api-url-verification
	if payload.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := map[string]string{"challenge": payload.Challenge}
		json.NewEncoder(w).Encode(response)
		return
	}

	// Respond immediately with 200 OK for events
	w.WriteHeader(http.StatusOK)

	// Process events asynchronously; track concurrent processing goroutines.
	if globalMetrics != nil {
		globalMetrics.IncConcurrent()
	}
	go func() {
		if globalMetrics != nil {
			defer globalMetrics.DecConcurrent()
		}
		h.processSlackEvent(payload)
	}()
}

// processSlackEvent processes the Slack event payload asynchronously
func (h *WebhookHandler) processSlackEvent(payload SlackWebhookPayload) {
	if payload.Event.Type == "message" && payload.Event.User != "" {
		// Ignore bot messages (messages without user or from bots)
		processedMsg := h.processSlackMessage(payload.Event)
		if processedMsg != nil {
			h.handleProcessedMessage(*processedMsg)
		}
	}
}

// processSlackMessage converts a Slack event to a ProcessedMessage
func (h *WebhookHandler) processSlackMessage(event SlackEvent) *ProcessedMessage {
	timestamp, _ := strconv.ParseFloat(event.Timestamp, 64)
	processed := &ProcessedMessage{
		ID:            event.EventTS,
		From:          event.User,
		MessengerType: "slack",
		Type:          "text",
		Content:       event.Text,
		Timestamp:     time.Unix(int64(timestamp), 0).UTC(),
		Files:         event.Files,
	}

	// Check if there are files
	if len(event.Files) > 0 {
		// Determine type based on first file
		file := event.Files[0]
		switch {
		case strings.Contains(file.Mimetype, "audio"):
			processed.Type = "audio"
			processed.MediaID = file.ID
			processed.MimeType = file.Mimetype
			processed.Filename = file.Name
		case strings.Contains(file.Mimetype, "video"):
			processed.Type = "video"
			processed.MediaID = file.ID
			processed.MimeType = file.Mimetype
			processed.Filename = file.Name
			processed.Caption = file.Title
		default:
			processed.Type = "document" // Or keep as text with files
		}
	}

	return processed
}

// HandleTelegramWebhook handles POST requests with incoming Telegram updates
// Based on Telegram Bot API webhook docs: https://core.telegram.org/bots/api#telegrampassportelement
func (h *WebhookHandler) HandleTelegramWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.verifyTelegramSecret(r) {
		log.Printf("TELEGRAM_SECRET_VALIDATION_FAILED")
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("ERROR reading request body: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var update TelegramUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		log.Printf("ERROR parsing Telegram webhook payload: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	if globalMetrics != nil {
		globalMetrics.IncConcurrent()
	}
	go func() {
		if globalMetrics != nil {
			defer globalMetrics.DecConcurrent()
		}
		if update.Message != nil {
			if update.Message.From.IsBot {
				return
			}
			h.setChatMessenger(update.Message.Chat.ID, "telegram")

			processed := h.processTelegramMessage(update.Message)
			if processed != nil {
				if processed.Type == "text" {
					handled, confirmErr := h.handlePendingActConfirmation(update.Message.Chat.ID, processed.Content)
					if confirmErr != nil {
						log.Printf("TELEGRAM_ACT_CONFIRMATION_FAILED: %v", confirmErr)
					}
					if handled {
						return
					}

					handled, confirmErr = h.handlePendingDeleteGroupConfirmation(update.Message.Chat.ID, processed.Content)
					if confirmErr != nil {
						log.Printf("TELEGRAM_DELETE_GROUP_CONFIRMATION_FAILED: %v", confirmErr)
					}
					if handled {
						return
					}
				}

				control := parseTelegramControlCommand(processed.Content)
				if control.Name == "" && processed.Type == "audio" {
					audioCommand, audioErr := h.detectControlCommandFromAudio(*processed)
					if audioErr != nil {
						log.Printf("TELEGRAM_AUDIO_COMMAND_DETECTION_FAILED: %v", audioErr)
					} else {
						// audioCommand may be a standard command token OR a koko: NLQ transcription.
						// Try parsing as a standard command first; if it doesn't match, store the
						// transcribed text in processed.Content so the NLQ path can use it.
						parsed := parseTelegramControlCommand(audioCommand)
						if parsed.Name != "" {
							control = parsed
						} else if _, ok := trimKokoPrefix(audioCommand); ok && strings.TrimSpace(audioCommand) != "" {
							// Audio contained a koko:-prefixed NLQ — set content so default path picks it up.
							processed.Content = audioCommand
							processed.Type = "text" // treat transcribed koko: audio as text for NLQ routing
						}
					}
				}

				// Send first-time welcome menu only for non-command messages
				// NOTE: for registered users, this is superseded by sendFirstTimeTermsNotice
				// below which also sets menuSentByChat. This call remains as a safety net
				// for any edge case where auth succeeds but T&C logic is bypassed.
				if control.Name == "" {
					if err := h.sendKokoMenuIfFirstMessage(update.Message.Chat.ID); err != nil {
						log.Printf("TELEGRAM_MENU_SEND_FAILED: %v", err)
					}
				}

				control = h.applyCommandDefaults(update.Message.Chat.ID, control)
				requester := strings.TrimSpace(processed.From)
				if requester == "" {
					requester = strings.TrimSpace(update.Message.From.FirstName)
				}

				accountID, groups, registrationErr := h.lookupTelegramUserGroups(requester)
				if registrationErr != nil {
					log.Printf("TELEGRAM_REGISTRATION_LOOKUP_FAILED: user=%s error=%v", requester, registrationErr)
					_ = h.sendTelegramMessage(update.Message.Chat.ID, "Access denied: unable to verify registration.")
					return
				}
				if strings.TrimSpace(accountID) == "" || len(groups) == 0 {
					log.Printf("TELEGRAM_UNREGISTERED_USER_REJECTED: user=%s messenger=telegram", requester)
					_ = h.sendTelegramMessage(update.Message.Chat.ID, "Access denied: your Telegram user is not registered to any account/group.")
					return
				}

				// Persist this user's Telegram chat_id for future broadcasts, and
				// backfill the messenger_type for users registered without one.
				if h.store != nil {
					chatIDStr := strconv.FormatInt(update.Message.Chat.ID, 10)
					if err := h.store.UpsertUserChatID(context.Background(), accountID, requester, "telegram", chatIDStr); err != nil {
						log.Printf("TELEGRAM_CHAT_ID_PERSIST_FAILED: user=%s error=%v", requester, err)
					}
					// Backfill messenger_type for placeholder-registered users.
					if err := h.store.BackfillUserMessengerType(context.Background(), accountID, requester, "telegram", chatIDStr); err != nil {
						log.Printf("TELEGRAM_MESSENGER_TYPE_BACKFILL_FAILED: user=%s error=%v", requester, err)
					}
				}

				state := h.getOrCreateChatState(update.Message.Chat.ID)
				state.ActiveAccountID = accountID
				if strings.TrimSpace(state.ActiveGroupName) == "" {
					state.ActiveGroupName = groups[0].GroupName
					state.ActiveGroupID = groups[0].GroupID
				}

				matchedActiveGroup := groups[0]
				for _, g := range groups {
					if g.GroupID == state.ActiveGroupID || strings.EqualFold(g.GroupName, state.ActiveGroupName) {
						matchedActiveGroup = g
						break
					}
				}

				processed.AccountID = accountID
				processed.GroupName = matchedActiveGroup.GroupName
				processed.UserName = requester
				processed.ChatID = strconv.FormatInt(update.Message.Chat.ID, 10)

				// Send Terms of Use + command menu on this user's very first message.
				// Returns true only once per user/chat (DB-persistent); if it fires we skip a
				// redundant menu for bare koko: / koko:help so the user only sees
				// the menu once.
				isFirstInteraction := h.sendFirstTimeTermsNotice(update.Message.Chat.ID, requester, accountID, groups)

				switch control.Name {
				case telegramSummarizeCommand:
					// Record audit
					if h.store != nil {
						_, _ = h.store.RecordKokoCommandAudit(context.Background(), KokoCommandAudit{
							AccountID:   accountID,
							GroupID:     matchedActiveGroup.GroupID,
							GroupName:   matchedActiveGroup.GroupName,
							CommandName: telegramSummarizeCommand,
							RequestedBy: requester,
							RequestedAt: time.Now().UTC(),
						})
					}
					var summary string
					var summarizeErr error
					if !control.HasDateRange {
						summary, summarizeErr = h.summarizeFromDBOrCache(update.Message.Chat.ID, control.Domain, control.UserContext, accountID, matchedActiveGroup.GroupName)
					} else {
						summary, summarizeErr = h.summarizeFromDBOrCacheWithDateRange(update.Message.Chat.ID, control.Domain, control.UserContext, accountID, matchedActiveGroup.GroupName, control.DateStart, control.DateEnd)
					}
					if summarizeErr != nil {
						log.Printf("TELEGRAM_SUMMARIZATION_FAILED: %v", summarizeErr)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Summarization failed. Please try again."))
					} else {
						state := h.getOrCreateChatState(update.Message.Chat.ID)
						state.LastSummary = summary
						state.LastSummaryDomain = normalizedDomainKey(control.Domain)
						state.LastSummaryAt = time.Now().UTC()
						state.LastSummaryHasDateRange = control.HasDateRange
						state.LastSummaryStart = control.DateStart
						state.LastSummaryEnd = control.DateEnd
						// Track group-level domain
						if d := normalizedDomainKey(control.Domain); d != featureModeAll {
							h.setGroupLastDomain(accountID, matchedActiveGroup.GroupName, d)
						}
						h.logSummaryResult(summary)
						kokoResp := formatKokoCommandResponse(control, summary)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, kokoResp)
						// Broadcast to other group members across messenger platforms.
						broadcastText := "From " + requester + ":\n\n" + kokoResp
						h.broadcastToGroupByMessenger(accountID, matchedActiveGroup.GroupName, "telegram", strconv.FormatInt(update.Message.Chat.ID, 10), broadcastText)
						// Enqueue rich media (ui/voice) rendering on top of the text result
						if control.OutputMode == "ui" || control.OutputMode == "voice" {
							richReq := &RichMediaRequest{
								ChatIDs:     []int64{update.Message.Chat.ID},
								Requesters:  []string{requester},
								AccountID:   accountID,
								GroupName:   matchedActiveGroup.GroupName,
								CommandName: telegramSummarizeCommand,
								TextContent: summary,
								OutputMode:  control.OutputMode,
							}
							senderKey := requester + "/" + strconv.FormatInt(update.Message.Chat.ID, 10)
							if _, ackMsg := h.richMediaQueue.TryEnqueue(richReq, senderKey); ackMsg != "" {
								_ = h.sendTelegramMessage(update.Message.Chat.ID, ackMsg)
							}
						}
						go h.checkAndWarnQuota(accountID)
					}
				case telegramAnalyzeCommand:
					// Record audit
					if h.store != nil {
						_, _ = h.store.RecordKokoCommandAudit(context.Background(), KokoCommandAudit{
							AccountID:   accountID,
							GroupID:     matchedActiveGroup.GroupID,
							GroupName:   matchedActiveGroup.GroupName,
							CommandName: telegramAnalyzeCommand,
							RequestedBy: requester,
							RequestedAt: time.Now().UTC(),
						})
					}
					if err := h.runFeatureAnalyze(update.Message.Chat.ID, control, requester, accountID, matchedActiveGroup); err != nil {
						log.Printf("TELEGRAM_ANALYZE_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Analyze command failed. Please try again."))
					} else {
						// Rich media (ui/voice) dispatch is handled inside runFeatureAnalyze.
						go h.checkAndWarnQuota(accountID)
					}
				case telegramActCommand:
					if err := h.runFeatureAct(update.Message.Chat.ID, control, requester, false); err != nil {
						log.Printf("TELEGRAM_ACT_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Act command failed. Please check downstream endpoints and retry."))
					} else {
						go h.checkAndWarnQuota(accountID)
						// Rich media dispatch for act is handled inside runFeatureAct
						// via the control.OutputMode field passed through.
					}
				case telegramGroupCommand:
					if err := h.runFeatureGroupCommand(update.Message.Chat.ID, requester, control); err != nil {
						log.Printf("TELEGRAM_GROUP_SELECT_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Group selection failed. Please try again."))
					}
				case telegramPlanCommand:
					if err := h.runFeaturePlan(update.Message.Chat.ID, control, requester, accountID, matchedActiveGroup); err != nil {
						log.Printf("TELEGRAM_PLAN_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Plan command failed. Please try again."))
					}
				case telegramAddGroupCommand:
					if err := h.runFeatureAddGroup(update.Message.Chat.ID, requester, accountID, "telegram", control); err != nil {
						log.Printf("TELEGRAM_ADD_GROUP_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Add-group command failed. Please try again."))
					}
				case telegramDeleteGroupCommand:
					if err := h.runFeatureDeleteGroup(update.Message.Chat.ID, requester, accountID, control); err != nil {
						log.Printf("TELEGRAM_DELETE_GROUP_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Delete-group command failed. Please try again."))
					}
				case telegramAddUserCommand:
					if err := h.runFeatureAddUser(update.Message.Chat.ID, requester, accountID, "telegram", control); err != nil {
						log.Printf("TELEGRAM_ADD_USER_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Add-user command failed. Please try again."))
					}
				case telegramDeleteUserCommand:
					if err := h.runFeatureDeleteUser(update.Message.Chat.ID, requester, accountID, control); err != nil {
						log.Printf("TELEGRAM_DELETE_USER_FAILED: %v", err)
						_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(control, "Delete-user command failed. Please try again."))
					}
				case telegramHelpCommand:
					// If this is the first interaction, the T&C notice already included
					// the full command menu, so skip showing it again. For subsequent
					// help requests (or help with a domain argument) respond as normal.
					if isFirstInteraction && strings.TrimSpace(control.Argument) == "" {
						// T&C+menu already sent; nothing more to do.
					} else if strings.TrimSpace(control.Argument) == "" {
						if err := h.sendTelegramMessage(update.Message.Chat.ID, h.buildTelegramCommandMenuForUser(requester)); err != nil {
							log.Printf("TELEGRAM_HELP_MENU_SEND_FAILED: %v", err)
						}
					} else if err := h.runFeatureHelp(update.Message.Chat.ID, control.Argument); err != nil {
						log.Printf("TELEGRAM_HELP_FAILED: %v", err)
					}
				default:
					if processed.Type == "text" {
						if query, ok := extractKokoNaturalLanguageQuery(processed.Content); ok {
							if err := h.runKokoNaturalLanguageAssistant(update.Message.Chat.ID, requester, control, query); err != nil {
								log.Printf("TELEGRAM_ASSISTANT_FAILED: %v", err)
								_ = h.sendTelegramMessage(update.Message.Chat.ID, formatKokoCommandResponse(ParsedControlCommand{Name: "koko:assistant", Domain: control.Domain}, "Temporary expert lookup failed. Please try again."))
							}
						} else if looksLikeKokoPrefixedInput(processed.Content) {
							// Unknown koko: command — serve cached help immediately
							helpText := h.buildTelegramCommandMenuForUser(requester)
							_ = h.sendTelegramMessage(update.Message.Chat.ID, helpText)
						} else {
							// Non-koko message: deliver to all other group members across messengers.
							h.addTelegramMessageToCache(*processed)
							broadcastText := requester + ": " + processed.Content
							h.broadcastToGroupByMessenger(accountID, matchedActiveGroup.GroupName, "telegram", strconv.FormatInt(update.Message.Chat.ID, 10), broadcastText)
						}
					} else {
						h.addTelegramMessageToCache(*processed)
					}
				}

				h.handleProcessedMessage(*processed)
			}
		}
	}()
}

func (h *WebhookHandler) addTelegramMessageToCache(msg ProcessedMessage) {
	if h.telegramCache == nil {
		return
	}

	if msg.Type != "text" && msg.Type != "audio" && msg.Type != "video" {
		return
	}

	if err := h.telegramCache.Add(CachedTelegramMessage{
		Type:      msg.Type,
		Content:   msg.Content,
		MediaID:   msg.MediaID,
		MimeType:  msg.MimeType,
		Timestamp: msg.Timestamp,
	}); err != nil {
		log.Printf("TELEGRAM_CACHE_ENCRYPTED_ADD_FAILED: %v", err)
	}
}

func (h *WebhookHandler) summarizeTelegramCache() (string, error) {
	return h.summarizeTelegramCacheWithOptions("", "", false, time.Time{}, time.Time{})
}

func (h *WebhookHandler) summarizeTelegramCacheWithOptions(domain, userContext string, hasDateRange bool, dateStart, dateEnd time.Time) (string, error) {
	cached := h.telegramCache.Snapshot()
	if len(cached) == 0 {
		return "", fmt.Errorf("no cached telegram messages available for summarization")
	}

	start := dateStart
	end := dateEnd
	if !hasDateRange {
		end = time.Now().UTC()
		start = end.Add(-24 * time.Hour)
	}

	filtered := make([]CachedTelegramMessage, 0, len(cached))
	for _, msg := range cached {
		if msg.Timestamp.Before(start) || !msg.Timestamp.Before(end) {
			continue
		}
		filtered = append(filtered, msg)
	}

	if len(filtered) == 0 {
		return "", fmt.Errorf("no cached telegram messages in requested date range")
	}

	recent := filtered
	if len(recent) > maxSummaryPromptEvents {
		recent = recent[len(recent)-maxSummaryPromptEvents:]
	}

	var promptEvents []string
	var mediaNotes []string
	var requestParts []map[string]interface{}
	mediaInputCount := 0

	for _, msg := range recent {
		switch msg.Type {
		case "text":
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			promptEvents = append(promptEvents, fmt.Sprintf("[%s] text: %s", msg.Timestamp.Format(time.RFC3339), msg.Content))
		case "audio", "video":
			promptEvents = append(promptEvents, fmt.Sprintf("[%s] %s: mime=%s", msg.Timestamp.Format(time.RFC3339), msg.Type, defaultOrValue(msg.MimeType, "unknown")))

			if msg.MediaID == "" {
				mediaNotes = append(mediaNotes, fmt.Sprintf("%s at %s skipped: missing media id", msg.Type, msg.Timestamp.Format(time.RFC3339)))
				continue
			}

			mediaURL, err := h.getTelegramFileDownloadURL(msg.MediaID)
			if err != nil {
				log.Printf("TELEGRAM_MEDIA_SUMMARIZATION_FAILED: Type=%s Error=%v", msg.Type, err)
				mediaNotes = append(mediaNotes, fmt.Sprintf("%s at %s skipped: unable to resolve media URL", msg.Type, msg.Timestamp.Format(time.RFC3339)))
				continue
			}

			mediaBytes, err := h.downloadTelegramMedia(mediaURL)
			if err != nil {
				log.Printf("TELEGRAM_MEDIA_SUMMARIZATION_FAILED: Type=%s Error=%v", msg.Type, err)
				mediaNotes = append(mediaNotes, fmt.Sprintf("%s at %s skipped: media download failed", msg.Type, msg.Timestamp.Format(time.RFC3339)))
				continue
			}

			if len(mediaBytes) == 0 {
				mediaNotes = append(mediaNotes, fmt.Sprintf("%s at %s skipped: media was empty", msg.Type, msg.Timestamp.Format(time.RFC3339)))
				continue
			}

			if len(mediaBytes) > maxVertexAIInlineBytes {
				mediaNotes = append(mediaNotes, fmt.Sprintf("%s at %s skipped: media size %d exceeds inline limit", msg.Type, msg.Timestamp.Format(time.RFC3339), len(mediaBytes)))
				continue
			}

			requestParts = append(requestParts, map[string]interface{}{
				"inline_data": map[string]string{
					"mime_type": defaultOrValue(msg.MimeType, defaultTelegramMimeType(msg.Type)),
					"data":      base64.StdEncoding.EncodeToString(mediaBytes),
				},
			})
			mediaInputCount++
		}
	}

	if len(promptEvents) == 0 && mediaInputCount == 0 {
		return h.buildLocalCacheFallbackSummary(filtered), nil
	}

	prompt := strings.Builder{}
	prompt.WriteString("Summarize this Telegram conversation across text, audio, and video inputs. ")
	prompt.WriteString("Give concise sections: Main Points, Decisions, Action Items, Open Questions, Prioritized Actions (P1/P2/P3 with rationale). ")
	prompt.WriteString("If media context is partial, mention that clearly.\n\n")
	prompt.WriteString("Time Window: ")
	prompt.WriteString(start.Format(time.RFC3339))
	prompt.WriteString(" to ")
	prompt.WriteString(end.Format(time.RFC3339))
	prompt.WriteString("\n")
	if strings.TrimSpace(domain) != "" && strings.ToLower(strings.TrimSpace(domain)) != featureModeAll {
		prompt.WriteString("Domain filter: ")
		prompt.WriteString(strings.TrimSpace(domain))
		prompt.WriteString("\n")
		prompt.WriteString("IMPORTANT: Include ONLY content relevant to the \"")
		prompt.WriteString(strings.TrimSpace(domain))
		prompt.WriteString("\" domain. Ignore all messages unrelated to this domain.\n")
	}
	if strings.TrimSpace(userContext) != "" {
		prompt.WriteString("Operational Context: ")
		prompt.WriteString(strings.TrimSpace(userContext))
		prompt.WriteString("\n")
	}
	prompt.WriteString("\n")
	prompt.WriteString("Conversation timeline:\n")
	prompt.WriteString(strings.Join(promptEvents, "\n"))
	if len(mediaNotes) > 0 {
		prompt.WriteString("\n\nMedia processing notes:\n- ")
		prompt.WriteString(strings.Join(mediaNotes, "\n- "))
	}

	requestParts = append([]map[string]interface{}{{"text": prompt.String()}}, requestParts...)

	summary, err := h.callVertexAIGenerateContent(requestParts)
	if err != nil {
		log.Printf("TELEGRAM_TEXT_SUMMARIZATION_FAILED: %v", err)
		var textMessages []string
		for _, event := range promptEvents {
			if strings.Contains(event, "] text:") {
				textMessages = append(textMessages, event)
			}
		}
		if len(textMessages) > 0 {
			return h.buildLocalTextFallbackSummary(textMessages), nil
		}
		return h.buildLocalCacheFallbackSummary(cached), nil
	}

	if strings.TrimSpace(summary) == "" {
		return h.buildLocalCacheFallbackSummary(cached), nil
	}

	if looksTruncatedSummary(summary) {
		repairedSummary, repairErr := h.repairTelegramSummary(promptEvents, mediaNotes, summary)
		if repairErr != nil {
			log.Printf("TELEGRAM_SUMMARY_REPAIR_FAILED: %v", repairErr)
		} else if strings.TrimSpace(repairedSummary) != "" {
			summary = repairedSummary
		}
	}

	if len(mediaNotes) > 0 {
		summary = summary + "\n\nMedia Processing Notes:\n- " + strings.Join(mediaNotes, "\n- ")
	}

	return summary, nil
}

func looksTruncatedSummary(summary string) bool {
	trimmed := strings.TrimSpace(summary)
	if len(trimmed) < 80 {
		return false
	}

	if strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?") {
		return false
	}

	if strings.HasSuffix(trimmed, "**") {
		return false
	}

	lastLineStart := strings.LastIndex(trimmed, "\n")
	lastLine := trimmed
	if lastLineStart >= 0 {
		lastLine = strings.TrimSpace(trimmed[lastLineStart+1:])
	}

	if strings.HasPrefix(lastLine, "-") {
		return false
	}

	return true
}

func (h *WebhookHandler) repairTelegramSummary(promptEvents, mediaNotes []string, draft string) (string, error) {
	prompt := strings.Builder{}
	prompt.WriteString("The summary below appears truncated. Rewrite it as a complete summary with full sentences and no cut-off endings. ")
	prompt.WriteString("Use markdown sections: Main Points, Decisions, Action Items, Open Questions. Keep it concise.\n\n")
	prompt.WriteString("Conversation timeline:\n")
	prompt.WriteString(strings.Join(promptEvents, "\n"))
	if len(mediaNotes) > 0 {
		prompt.WriteString("\n\nMedia processing notes:\n- ")
		prompt.WriteString(strings.Join(mediaNotes, "\n- "))
	}
	prompt.WriteString("\n\nTruncated draft:\n")
	prompt.WriteString(draft)

	return h.callVertexAIGenerateContent([]map[string]interface{}{{
		"text": prompt.String(),
	}})
}

func defaultOrValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (h *WebhookHandler) buildLocalTextFallbackSummary(textMessages []string) string {
	maxLines := 5
	if len(textMessages) < maxLines {
		maxLines = len(textMessages)
	}

	recent := textMessages[len(textMessages)-maxLines:]
	return fmt.Sprintf(
		"Text Summary (fallback): external summarization unavailable. Recent messages:\n- %s",
		strings.Join(recent, "\n- "),
	)
}

func (h *WebhookHandler) buildLocalCacheFallbackSummary(cached []CachedTelegramMessage) string {
	textCount := 0
	audioCount := 0
	videoCount := 0

	for _, msg := range cached {
		switch msg.Type {
		case "text":
			textCount++
		case "audio":
			audioCount++
		case "video":
			videoCount++
		}
	}

	return fmt.Sprintf(
		"Summary (fallback): external summarization unavailable. Cached messages: text=%d, audio=%d, video=%d, total=%d",
		textCount,
		audioCount,
		videoCount,
		len(cached),
	)
}

func (h *WebhookHandler) summarizeTextWithGoogleAI(text string) (string, error) {
	prompt := "Summarize the following Telegram conversation clearly and concisely. Highlight the main decisions, action items, and any unresolved questions.\n\n" + text
	return h.callVertexAIGenerateContent([]map[string]interface{}{
		{
			"text": prompt,
		},
	})
}

func (h *WebhookHandler) summarizeTelegramMediaWithGoogleAI(msg CachedTelegramMessage) (string, error) {
	if msg.MediaID == "" {
		return "", fmt.Errorf("missing media id for %s message", msg.Type)
	}
	if h.telegramBotToken == "" {
		return "", fmt.Errorf("TELEGRAM_BOT_TOKEN is not configured")
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
		return "", fmt.Errorf("downloaded telegram %s media was empty", msg.Type)
	}
	if len(mediaBytes) > maxVertexAIInlineBytes {
		return "", fmt.Errorf("telegram %s media exceeds inline size limit: %d bytes", msg.Type, len(mediaBytes))
	}

	mimeType := msg.MimeType
	if mimeType == "" {
		mimeType = defaultTelegramMimeType(msg.Type)
	}

	prompt := fmt.Sprintf(
		"Summarize this Telegram %s message. Include the main points, notable details, and likely intent. Please ensure you accurately transcribe and understand various English accents including US, UK, Australian, and Indian/South Asian accents. Metadata: timestamp=%s, format=%s.",
		msg.Type,
		msg.Timestamp.Format(time.RFC3339),
		mimeType,
	)

	return h.callVertexAIGenerateContent([]map[string]interface{}{
		{
			"text": prompt,
		},
		{
			"inline_data": map[string]string{
				"mime_type": mimeType,
				"data":      base64.StdEncoding.EncodeToString(mediaBytes),
			},
		},
	})
}

func (h *WebhookHandler) getTelegramFileDownloadURL(fileID string) (string, error) {
	if strings.TrimSpace(fileID) == "" {
		return "", fmt.Errorf("empty telegram file_id")
	}

	query := url.Values{}
	query.Set("file_id", fileID)
	requestURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?%s", h.telegramBotToken, query.Encode())
	var lastErr error
	for attempt := 1; attempt <= maxTelegramFetchAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, requestURL, nil)
		if err != nil {
			return "", err
		}

		resp, err := h.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("telegram getFile request failed: %s", sanitizeTelegramError(err, h.telegramBotToken))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(err, 0) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return "", lastErr
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("telegram getFile response read failed: %s", sanitizeTelegramError(readErr, h.telegramBotToken))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(readErr, resp.StatusCode) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return "", lastErr
		}

		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("telegram getFile request failed: status=%d body=%s", resp.StatusCode, string(body))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(nil, resp.StatusCode) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return "", lastErr
		}

		var getFileResp struct {
			OK     bool `json:"ok"`
			Result struct {
				FilePath string `json:"file_path"`
			} `json:"result"`
		}

		if err := json.Unmarshal(body, &getFileResp); err != nil {
			return "", err
		}

		if !getFileResp.OK || getFileResp.Result.FilePath == "" {
			return "", fmt.Errorf("telegram getFile did not return file_path")
		}

		return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", h.telegramBotToken, getFileResp.Result.FilePath), nil
	}

	if lastErr != nil {
		return "", lastErr
	}

	return "", fmt.Errorf("telegram getFile request failed after retries")
}

func (h *WebhookHandler) downloadTelegramMedia(downloadURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxTelegramFetchAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := h.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("telegram media download failed: %s", sanitizeTelegramError(err, h.telegramBotToken))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(err, 0) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return nil, lastErr
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("telegram media response read failed: %s", sanitizeTelegramError(readErr, h.telegramBotToken))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(readErr, resp.StatusCode) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return nil, lastErr
		}

		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("telegram media download failed: status=%d body=%s", resp.StatusCode, string(body))
			if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(nil, resp.StatusCode) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return nil, lastErr
		}

		return body, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, fmt.Errorf("telegram media download failed after retries")
}

func shouldRetryTelegramRequest(err error, statusCode int) bool {
	if statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests || statusCode >= 500 {
		return true
	}

	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "tls handshake timeout") ||
		strings.Contains(errText, "connection reset by peer") ||
		strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "temporarily unavailable")
}

func telegramRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	backoff := 400 * time.Millisecond
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff > 4*time.Second {
			return 4 * time.Second
		}
	}

	return backoff
}

func sanitizeTelegramError(err error, botToken string) string {
	if err == nil {
		return ""
	}

	errText := err.Error()
	if strings.TrimSpace(botToken) != "" {
		errText = strings.ReplaceAll(errText, botToken, "<redacted>")
	}

	return errText
}

func defaultTelegramMimeType(messageType string) string {
	switch messageType {
	case "audio":
		return "audio/ogg"
	case "video":
		return "video/mp4"
	case "image":
		return "image/jpeg"
	case "document":
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

func normalizeWhatsAppSender(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (h *WebhookHandler) callVertexAIGenerateContent(parts []map[string]interface{}) (string, error) {
	projectID, err := h.googleProjectID()
	if err != nil {
		return "", err
	}

	accessToken, err := h.googleAccessToken()
	if err != nil {
		return "", err
	}

	locations := orderedUniqueStrings([]string{h.vertexAILocation, vertexAIDefaultLocation, "us-central1", "global"})
	models := orderedUniqueStrings([]string{h.vertexAIModel, vertexAIDefaultModel, "gemini-2.5-flash", "gemini-2.0-flash", "gemini-1.5-flash"})

	var lastErr error
	for _, location := range locations {
		for _, model := range models {
			summary, callErr := h.callVertexAIGenerateContentWithModel(parts, projectID, accessToken, location, model)
			if callErr == nil {
				return summary, nil
			}

			lastErr = callErr
		}
	}

	if lastErr != nil {
		return "", lastErr
	}

	return "", fmt.Errorf("vertex ai request failed without a specific error")
}

func (h *WebhookHandler) callVertexAIGenerateContentWithModel(parts []map[string]interface{}, projectID, accessToken, location, model string) (string, error) {

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.2,
			"maxOutputTokens": 2048,
		},
	}

	// Use a pooled buffer for the request body to avoid per-call heap allocation.
	buf := getBuf()
	if err := json.NewEncoder(buf).Encode(requestBody); err != nil {
		putBuf(buf)
		return "", err
	}
	reqBody := bytes.NewReader(buf.Bytes())

	endpoint := h.vertexAIEndpoint(projectID, location, model)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reqBody)
	if err != nil {
		putBuf(buf)
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	putBuf(buf) // safe to release now — request body has been consumed
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf(
			"vertex ai generateContent failed: model=%s location=%s status=%d body=%s",
			model,
			location,
			resp.StatusCode,
			truncateForLog(string(body), 800),
		)
	}

	// Stream-decode the JSON response instead of reading all bytes into memory.
	var vertexResp struct {
		Choices []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&vertexResp); err != nil {
		return "", err
	}

	if len(vertexResp.Choices) == 0 || len(vertexResp.Choices[0].Content.Parts) == 0 {
		return "", fmt.Errorf("vertex ai response did not include summary content")
	}

	var textParts []string
	for _, part := range vertexResp.Choices[0].Content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			textParts = append(textParts, part.Text)
		}
	}

	if len(textParts) == 0 {
		return "", fmt.Errorf("vertex ai response did not include text summary content")
	}

	return strings.Join(textParts, "\n"), nil
}

func (h *WebhookHandler) vertexAIEndpoint(projectID, location, model string) string {
	baseURL := "https://aiplatform.googleapis.com"
	if location != "global" {
		baseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com", location)
	}

	return fmt.Sprintf(
		"%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		baseURL,
		projectID,
		location,
		model,
	)
}

func orderedUniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))

	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}

		seen[trimmed] = true
		result = append(result, trimmed)
	}

	return result
}

func (h *WebhookHandler) googleProjectID() (string, error) {
	if h.googleCloudProject != "" {
		return h.googleCloudProject, nil
	}

	projectID, err := h.googleMetadataText(googleMetadataProjectURL)
	if err != nil {
		return "", fmt.Errorf("google cloud project is not configured and metadata lookup failed: %w", err)
	}

	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("google cloud project id is empty")
	}

	return projectID, nil
}

func (h *WebhookHandler) googleAccessToken() (string, error) {
	req, err := http.NewRequest(http.MethodGet, googleMetadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata token request failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}

	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("metadata token response did not include an access token")
	}

	return tokenResp.AccessToken, nil
}

func (h *WebhookHandler) googleMetadataText(requestURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("metadata request failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	return string(body), nil
}

func (h *WebhookHandler) processTelegramMessage(msg *TelegramMessage) *ProcessedMessage {
	processed := &ProcessedMessage{
		ID:            strconv.FormatInt(msg.MessageID, 10),
		From:          msg.From.Username,
		UserName:      msg.From.Username,
		ChatID:        strconv.FormatInt(msg.Chat.ID, 10),
		MessengerType: "telegram",
		Type:          "text",
		Content:       msg.Text,
		Timestamp:     time.Unix(msg.Date, 0).UTC(),
	}

	if msg.Text != "" {
		processed.Type = "text"
		logTelegramUserMessage(msg.Text)
		return processed
	}

	if msg.Voice != nil {
		processed.Type = "audio"
		processed.MediaID = msg.Voice.FileID
		processed.MimeType = msg.Voice.MimeType
		logTelegramMediaInput("audio", msg.Voice.FileSize, msg.Date, processed.From)
		return processed
	}

	if msg.Audio != nil {
		processed.Type = "audio"
		processed.MediaID = msg.Audio.FileID
		processed.MimeType = msg.Audio.MimeType
		processed.Filename = msg.Audio.FileName
		logTelegramMediaInput("audio", msg.Audio.FileSize, msg.Date, processed.From)
		return processed
	}

	if msg.Video != nil {
		processed.Type = "video"
		processed.MediaID = msg.Video.FileID
		processed.MimeType = msg.Video.MimeType
		processed.Filename = msg.Video.FileName
		logTelegramMediaInput("video", msg.Video.FileSize, msg.Date, processed.From)
		return processed
	}

	if msg.Document != nil {
		processed.Type = "document"
		processed.MediaID = msg.Document.FileID
		processed.MimeType = msg.Document.MimeType
		processed.Filename = msg.Document.FileName
		return processed
	}

	return processed
}

func (h *WebhookHandler) processMessages(payload WhatsAppWebhookPayload) {
	if strings.TrimSpace(payload.Object) != "" && !strings.EqualFold(payload.Object, "whatsapp_business_account") {
		log.Printf("WHATSAPP_WEBHOOK_IGNORED_OBJECT: object=%s", payload.Object)
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if !strings.EqualFold(strings.TrimSpace(change.Field), "messages") {
				continue
			}

			incomingPhoneID := strings.TrimSpace(change.Value.Metadata.PhoneNumberID)
			if strings.TrimSpace(h.whatsappPhoneID) != "" && incomingPhoneID != "" && incomingPhoneID != strings.TrimSpace(h.whatsappPhoneID) {
				log.Printf("WHATSAPP_WEBHOOK_IGNORED_PHONE_ID: got=%s expected=%s", incomingPhoneID, h.whatsappPhoneID)
				continue
			}

			for _, message := range change.Value.Messages {
				processedMsg := h.processMessage(message)
				if processedMsg != nil {
					rawSender := strings.TrimSpace(message.From)
					normalizedSender := normalizeWhatsAppSender(rawSender)
					if normalizedSender == "" {
						normalizedSender = rawSender
					}

					processedMsg.ChatID = normalizedSender
					processedMsg.UserName = normalizedSender

					if processedMsg.UserName != "" {
						accountID, groups, err := h.lookupUserGroupsByMessenger(processedMsg.UserName, "whatsapp")
						if err == nil && strings.TrimSpace(accountID) == "" && normalizedSender != rawSender {
							// Fallback for legacy rows keyed with raw sender format.
							accountID, groups, err = h.lookupUserGroupsByMessenger(rawSender, "whatsapp")
							if err == nil && strings.TrimSpace(accountID) != "" {
								processedMsg.UserName = rawSender
								processedMsg.ChatID = rawSender
							}
						}
						if err != nil {
							log.Printf("WHATSAPP_REGISTRATION_LOOKUP_FAILED: raw_user=%s normalized_user=%s error=%v", rawSender, normalizedSender, err)
						} else if strings.TrimSpace(accountID) != "" && len(groups) > 0 {
							processedMsg.AccountID = accountID
							processedMsg.GroupName = groups[0].GroupName
							if h.store != nil {
								if err := h.store.UpsertUserChatID(context.Background(), accountID, processedMsg.UserName, "whatsapp", processedMsg.ChatID); err != nil {
									log.Printf("WHATSAPP_CHAT_ID_PERSIST_FAILED: user=%s error=%v", processedMsg.UserName, err)
								}
								if err := h.store.BackfillUserMessengerType(context.Background(), accountID, processedMsg.UserName, "whatsapp", processedMsg.ChatID); err != nil {
									log.Printf("WHATSAPP_MESSENGER_TYPE_BACKFILL_FAILED: user=%s error=%v", processedMsg.UserName, err)
								}
							}
						}
					}

					if chatIDInt, err := strconv.ParseInt(processedMsg.ChatID, 10, 64); err == nil {
						h.setChatMessenger(chatIDInt, "whatsapp")
					}

					h.handleProcessedMessage(*processedMsg)
				}
			}
		}
	}
}

func (h *WebhookHandler) setChatMessenger(chatID int64, messengerType string) {
	h.chatStateMu.Lock()
	defer h.chatStateMu.Unlock()
	h.chatMessengerByChat[chatID] = strings.ToLower(strings.TrimSpace(messengerType))
}

func (h *WebhookHandler) messengerForChat(chatID int64) string {
	h.chatStateMu.Lock()
	defer h.chatStateMu.Unlock()
	if m := strings.TrimSpace(h.chatMessengerByChat[chatID]); m != "" {
		return m
	}
	return "telegram"
}

func (h *WebhookHandler) sendWhatsAppMessage(chatID, text string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("invalid whatsapp chat_id")
	}
	if strings.TrimSpace(h.whatsappPhoneID) == "" {
		return fmt.Errorf("WHATSAPP_PHONE_NUMBER_ID is not configured")
	}
	if strings.TrimSpace(h.accessToken) == "" {
		return fmt.Errorf("WHATSAPP_ACCESS_TOKEN is not configured")
	}

	apiURL := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", strings.TrimSpace(h.whatsappPhoneID))
	chunks := splitTelegramMessageChunks(text, maxTelegramSendChunkBytes)
	for _, chunk := range chunks {
		payload := map[string]interface{}{
			"messaging_product": "whatsapp",
			"to":                chatID,
			"type":              "text",
			"text": map[string]interface{}{
				"preview_url": false,
				"body":        chunk,
			},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal whatsapp payload: %w", err)
		}

		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to create whatsapp request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(h.accessToken))

		resp, err := h.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("whatsapp send request failed: %w", err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("whatsapp send failed: status=%d body=%s", resp.StatusCode, truncateForLog(string(respBody), 300))
		}
	}

	return nil
}

func (h *WebhookHandler) sendTextByMessenger(messengerType, chatID, text string) error {
	switch strings.ToLower(strings.TrimSpace(messengerType)) {
	case "telegram":
		cid, err := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid telegram chat_id %q: %w", chatID, err)
		}
		return h.sendTelegramMessage(cid, text)
	case "whatsapp":
		return h.sendWhatsAppMessage(chatID, text)
	default:
		return fmt.Errorf("unsupported messenger type for outbound text: %s", messengerType)
	}
}

func (h *WebhookHandler) sendTextToChatID(chatID int64, text string) error {
	return h.sendTextByMessenger(h.messengerForChat(chatID), strconv.FormatInt(chatID, 10), text)
}

func (h *WebhookHandler) broadcastToGroupByMessenger(accountID, groupName, excludeMessengerType, excludeChatID, text string) {
	if h.store == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(groupName) == "" {
		return
	}

	dests, err := h.store.GetGroupMemberDestinations(context.Background(), accountID, groupName)
	if err != nil {
		log.Printf("BROADCAST_DEST_LOOKUP_FAILED: account=%s group=%s error=%v", accountID, groupName, err)
		return
	}

	excludeMessengerType = strings.ToLower(strings.TrimSpace(excludeMessengerType))
	excludeChatID = strings.TrimSpace(excludeChatID)
	for _, d := range dests {
		if d.MessengerType == excludeMessengerType && strings.TrimSpace(d.ChatID) == excludeChatID {
			continue
		}
		if err := h.sendTextByMessenger(d.MessengerType, d.ChatID, text); err != nil {
			log.Printf("BROADCAST_SEND_FAILED: account=%s group=%s messenger=%s chat_id=%s error=%v", accountID, groupName, d.MessengerType, d.ChatID, err)
		}
	}
}

func (h *WebhookHandler) persistTelegramSummary(chatID int64, state *ChatCommandState, domain, summaryText string) {
	if h.store == nil || state == nil || strings.TrimSpace(summaryText) == "" {
		return
	}
	accountID := strings.TrimSpace(state.ActiveAccountID)
	groupName := strings.TrimSpace(state.ActiveGroupName)
	if accountID == "" || groupName == "" {
		return
	}
	now := time.Now().UTC()
	_, err := h.store.SaveSummaryAndDeleteInputs(context.Background(), MessengerSummary{
		AccountID:          accountID,
		MessengerType:      "telegram",
		ChatID:             strconv.FormatInt(chatID, 10),
		Domain:             normalizedDomainKey(domain),
		WindowStart:        now.Add(-24 * time.Hour),
		WindowEnd:          now,
		SourceMessageCount: len(h.telegramCache.Snapshot()),
		SummaryText:        summaryText,
	}, nil)
	if err != nil {
		log.Printf("TELEGRAM_SUMMARY_PERSIST_FAILED: account=%s group=%s error=%v", accountID, groupName, err)
	}
}

// broadcastToGroup sends text to all group members who have a stored chat_id, excluding excludeChatID.
func (h *WebhookHandler) broadcastToGroup(accountID, groupName string, excludeChatID int64, text string) {
	if h.store == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(groupName) == "" {
		return
	}
	chatIDs, err := h.store.GetGroupMemberChatIDs(context.Background(), accountID, groupName)
	if err != nil {
		log.Printf("BROADCAST_LOOKUP_FAILED: account=%s group=%s error=%v", accountID, groupName, err)
		return
	}
	for _, cid := range chatIDs {
		if cid == excludeChatID {
			continue
		}
		if err := h.sendTelegramMessage(cid, text); err != nil {
			log.Printf("BROADCAST_SEND_FAILED: account=%s group=%s cid=%d error=%v", accountID, groupName, cid, err)
		}
	}
}

// groupDomainKey returns the map key for the group-level domain tracker.
func groupDomainKey(accountID, groupName string) string {
	return accountID + "::" + groupName
}

// setGroupLastDomain records the most-recently used domain for any member in a group.
func (h *WebhookHandler) setGroupLastDomain(accountID, groupName, domain string) {
	if strings.TrimSpace(accountID) == "" || strings.TrimSpace(groupName) == "" || strings.TrimSpace(domain) == "" {
		return
	}
	h.chatStateMu.Lock()
	defer h.chatStateMu.Unlock()
	h.groupLastDomain[groupDomainKey(accountID, groupName)] = domain
}

// getGroupLastDomain returns the most-recently used domain for any member in a group.
func (h *WebhookHandler) getGroupLastDomain(accountID, groupName string) string {
	h.chatStateMu.Lock()
	defer h.chatStateMu.Unlock()
	return h.groupLastDomain[groupDomainKey(accountID, groupName)]
}

// broadcastToAllAccountGroups sends text to every unique Telegram chat across all groups in the account.
func (h *WebhookHandler) broadcastToAllAccountGroups(accountID, text string) {
	if h.store == nil || strings.TrimSpace(accountID) == "" {
		return
	}
	groups, err := h.store.ListGroups(context.Background(), accountID)
	if err != nil {
		log.Printf("BROADCAST_ALL_GROUPS_LOOKUP_FAILED: account=%s error=%v", accountID, err)
		return
	}
	seen := map[int64]bool{}
	for _, g := range groups {
		chatIDs, err := h.store.GetGroupMemberChatIDs(context.Background(), accountID, g.GroupName)
		if err != nil {
			log.Printf("BROADCAST_ALL_GROUPS_MEMBER_LOOKUP_FAILED: account=%s group=%s error=%v", accountID, g.GroupName, err)
			continue
		}
		for _, cid := range chatIDs {
			if seen[cid] {
				continue
			}
			seen[cid] = true
			if err := h.sendTelegramMessage(cid, text); err != nil {
				log.Printf("BROADCAST_ALL_GROUPS_SEND_FAILED: account=%s cid=%d error=%v", accountID, cid, err)
			}
		}
	}
}

// summarizeFromDBOrCacheWithDateRange handles summarize requests with an explicit date range.
// Priority order:
//  1. Return a stored DB summary that already covers the requested window.
//  2. Load raw inputs from DB for that window, summarize via AI, save to DB.
//  3. Fall back to in-memory cache if DB is unavailable or has no inputs.
func (h *WebhookHandler) summarizeFromDBOrCacheWithDateRange(chatID int64, domain, userContext, accountID, groupName string, dateStart, dateEnd time.Time) (string, error) {
	chatIDStr := strconv.FormatInt(chatID, 10)

	if h.store != nil && h.store.DBEnabled() {
		// 1. Return existing summary if the requested range is already covered.
		existing, err := h.store.SummaryForRange(context.Background(), "telegram", chatIDStr, normalizedDomainKey(domain), dateStart, dateEnd)
		if err == nil && strings.TrimSpace(existing.SummaryText) != "" {
			note := fmt.Sprintf("(Stored summary for %s – %s, generated %s)\n\n",
				existing.WindowStart.UTC().Format("Jan 2, 2006 15:04 UTC"),
				existing.WindowEnd.UTC().Format("Jan 2, 2006 15:04 UTC"),
				existing.SummarizedAt.UTC().Format("Jan 2, 2006 15:04 UTC"))
			return note + existing.SummaryText, nil
		}

		// 2. Load raw inputs for the date range from DB.
		inputs, loadErr := h.store.LoadMessengerInputs(context.Background(), "telegram", chatIDStr, dateStart, dateEnd, maxSummaryPromptEvents)
		if loadErr != nil {
			log.Printf("TELEGRAM_DB_INPUTS_LOAD_FAILED: %v", loadErr)
			// fall through to cache
		} else if len(inputs) > 0 {
			var promptEvents []string
			var inputIDs []int64
			for _, in := range inputs {
				inputIDs = append(inputIDs, in.ID)
				if strings.TrimSpace(in.TextContent) != "" {
					promptEvents = append(promptEvents, fmt.Sprintf("[%s] %s: %s", in.EffectiveTime().Format(time.RFC3339), in.UserName, in.TextContent))
				}
			}

			if len(promptEvents) > 0 {
				prompt := strings.Builder{}
				prompt.WriteString("Summarize this group conversation. Give concise sections: Main Points, Decisions, Action Items, Open Questions, Prioritized Actions (P1/P2/P3 with rationale).\n\n")
				prompt.WriteString("Time Window: " + dateStart.Format(time.RFC3339) + " to " + dateEnd.Format(time.RFC3339) + "\n")
				if strings.TrimSpace(domain) != "" && strings.ToLower(strings.TrimSpace(domain)) != featureModeAll {
					prompt.WriteString("Domain filter: " + strings.TrimSpace(domain) + "\n")
					prompt.WriteString("IMPORTANT: Include ONLY content relevant to the \"" + strings.TrimSpace(domain) + "\" domain. Ignore all messages unrelated to this domain.\n")
				}
				if strings.TrimSpace(userContext) != "" {
					prompt.WriteString("Operational Context: " + strings.TrimSpace(userContext) + "\n")
				}
				prompt.WriteString("\nConversation timeline:\n")
				prompt.WriteString(strings.Join(promptEvents, "\n"))

				summary, aiErr := h.callVertexAIGenerateContent([]map[string]interface{}{{"text": prompt.String()}})
				if aiErr != nil {
					log.Printf("TELEGRAM_DB_SUMMARIZATION_AI_FAILED: %v", aiErr)
					summary = "Summary of recent activity:\n" + strings.Join(promptEvents[max(0, len(promptEvents)-10):], "\n")
				}
				if strings.TrimSpace(summary) == "" {
					summary = "Summarization returned empty response."
				}

				// Save summary and delete source inputs in one transaction.
				_, saveErr := h.store.SaveSummaryAndDeleteInputs(context.Background(), MessengerSummary{
					AccountID:          accountID,
					MessengerType:      "telegram",
					ChatID:             chatIDStr,
					Domain:             normalizedDomainKey(domain),
					WindowStart:        dateStart,
					WindowEnd:          dateEnd,
					SourceMessageCount: len(inputs),
					SummaryText:        summary,
				}, inputIDs)
				if saveErr != nil {
					log.Printf("TELEGRAM_SUMMARY_SAVE_FAILED: account=%s group=%s error=%v", accountID, groupName, saveErr)
				}
				return summary, nil
			}
			// Inputs were all media (no text) — fall through to cache.
		} else {
			// No inputs in DB for this range — return most recent stored summary if one exists.
			latest, latestErr := h.store.LatestSummary(context.Background(), "telegram", chatIDStr, normalizedDomainKey(domain))
			if latestErr == nil && strings.TrimSpace(latest.SummaryText) != "" {
				note := fmt.Sprintf("No messages found in the requested range. Most recent summary (%s – %s, generated %s):\n\n",
					latest.WindowStart.UTC().Format("Jan 2, 2006 15:04 UTC"),
					latest.WindowEnd.UTC().Format("Jan 2, 2006 15:04 UTC"),
					latest.SummarizedAt.UTC().Format("Jan 2, 2006 15:04 UTC"))
				return note + latest.SummaryText, nil
			}
		}
	}

	// Fallback: in-memory cache. If cache is also empty, return a friendly no-data message.
	cachedSummary, cacheErr := h.summarizeTelegramCacheWithOptions(domain, userContext, true, dateStart, dateEnd)
	if cacheErr != nil {
		log.Printf("TELEGRAM_SUMMARIZATION_NO_DATA: domain=%s group=%s range=%s..%s", domain, groupName, dateStart.Format(time.RFC3339), dateEnd.Format(time.RFC3339))
		return "No messages found in the requested time range. Please verify the date range and try again.", nil
	}
	return cachedSummary, nil
}

// summarizeFromDBOrCache summarizes unsummarized DB messages when no date range is requested.
// - If there are unprocessed messages in DB: summarize them, save the summary, and delete those messages.
// - If no unprocessed messages remain: return last stored summary with a note.
// - If DB is unavailable: fall back to in-memory cache.
func (h *WebhookHandler) summarizeFromDBOrCache(chatID int64, domain, userContext, accountID, groupName string) (string, error) {
	chatIDStr := strconv.FormatInt(chatID, 10)

	if h.store != nil && h.store.DBEnabled() {
		inputs, sinceTime, err := h.store.LoadInputsSinceLastSummary(context.Background(), "telegram", chatIDStr, maxSummaryPromptEvents)
		if err != nil {
			log.Printf("TELEGRAM_DB_INPUTS_LOAD_FAILED: %v", err)
			// fall through to cache
		} else if len(inputs) > 0 {
			// Build summarization prompt from DB inputs
			var promptEvents []string
			var inputIDs []int64
			for _, in := range inputs {
				inputIDs = append(inputIDs, in.ID)
				if strings.TrimSpace(in.TextContent) != "" {
					promptEvents = append(promptEvents, fmt.Sprintf("[%s] %s: %s", in.EffectiveTime().Format(time.RFC3339), in.UserName, in.TextContent))
				}
			}

			if len(promptEvents) == 0 {
				// Only media inputs in DB — fall back to cache
			} else {
				var start, end time.Time
				if sinceTime.IsZero() {
					end = time.Now().UTC()
					start = inputs[0].EffectiveTime()
				} else {
					start = sinceTime
					end = time.Now().UTC()
				}

				prompt := strings.Builder{}
				prompt.WriteString("Summarize this group conversation. Give concise sections: Main Points, Decisions, Action Items, Open Questions, Prioritized Actions (P1/P2/P3 with rationale).\n\n")
				prompt.WriteString("Time Window: " + start.Format(time.RFC3339) + " to " + end.Format(time.RFC3339) + "\n")
				if strings.TrimSpace(domain) != "" && strings.ToLower(strings.TrimSpace(domain)) != featureModeAll {
					prompt.WriteString("Domain filter: " + strings.TrimSpace(domain) + "\n")
					prompt.WriteString("IMPORTANT: Include ONLY content relevant to the \"" + strings.TrimSpace(domain) + "\" domain. Ignore all messages unrelated to this domain.\n")
				}
				if strings.TrimSpace(userContext) != "" {
					prompt.WriteString("Operational Context: " + strings.TrimSpace(userContext) + "\n")
				}
				prompt.WriteString("\nConversation timeline:\n")
				prompt.WriteString(strings.Join(promptEvents, "\n"))

				summary, aiErr := h.callVertexAIGenerateContent([]map[string]interface{}{{"text": prompt.String()}})
				if aiErr != nil {
					log.Printf("TELEGRAM_DB_SUMMARIZATION_AI_FAILED: %v", aiErr)
					// localfallback: simple join of recent lines
					if len(promptEvents) > 0 {
						summary = "Summary of recent activity:\n" + strings.Join(promptEvents[max(0, len(promptEvents)-10):], "\n")
					} else {
						summary = "No text content available to summarize."
					}
				}
				if strings.TrimSpace(summary) == "" {
					summary = "Summarization returned empty response."
				}

				// Save summary and delete source messages from DB
				if h.store != nil {
					state := h.getOrCreateChatState(chatID)
					_, saveErr := h.store.SaveSummaryAndDeleteInputs(context.Background(), MessengerSummary{
						AccountID:          accountID,
						MessengerType:      "telegram",
						ChatID:             chatIDStr,
						Domain:             normalizedDomainKey(domain),
						WindowStart:        start,
						WindowEnd:          end,
						SourceMessageCount: len(inputs),
						SummaryText:        summary,
					}, inputIDs)
					if saveErr != nil {
						log.Printf("TELEGRAM_SUMMARY_SAVE_FAILED: account=%s group=%s error=%v", state.ActiveAccountID, state.ActiveGroupName, saveErr)
					}
				}
				return summary, nil
			}
		} else {
			// No new messages in DB — return last stored summary
			lastSummary, sErr := h.store.LatestSummary(context.Background(), "telegram", chatIDStr, normalizedDomainKey(domain))
			if sErr == nil && strings.TrimSpace(lastSummary.SummaryText) != "" {
				note := "No new messages since last summarization (" + lastSummary.SummarizedAt.UTC().Format("Jan 2, 2006 15:04 UTC") + "). Returning last summary:\n\n"
				return note + lastSummary.SummaryText, nil
			}
		}
	}

	// Fallback: in-memory cache (for single-instance / non-DB sessions).
	// If the cache is also empty (e.g. after a Cloud Run restart), return a
	// friendly no-data message rather than an opaque error.
	cachedSummary, cacheErr := h.summarizeTelegramCacheWithOptions(domain, userContext, false, time.Time{}, time.Time{})
	if cacheErr != nil {
		log.Printf("TELEGRAM_SUMMARIZATION_NO_DATA: domain=%s group=%s", domain, groupName)
		return "No summarizable messages found. Please send messages to the group and try again.", nil
	}
	return cachedSummary, nil
}

// processMessage converts a WhatsApp message to a ProcessedMessage
func (h *WebhookHandler) processMessage(msg WhatsAppMessage) *ProcessedMessage {
	timestamp, _ := strconv.ParseInt(msg.Timestamp, 10, 64)
	processed := &ProcessedMessage{
		ID:            msg.ID,
		From:          msg.From,
		MessengerType: "whatsapp",
		Type:          msg.Type,
		Timestamp:     time.Unix(timestamp, 0).UTC(),
	}

	switch msg.Type {
	case "text":
		if msg.Text != nil {
			processed.Content = msg.Text.Body
		}
	case "audio":
		if msg.Audio != nil {
			processed.MediaID = msg.Audio.ID
			processed.MimeType = msg.Audio.MimeType
		}
	case "video":
		if msg.Video != nil {
			processed.MediaID = msg.Video.ID
			processed.MimeType = msg.Video.MimeType
			processed.Caption = msg.Video.Caption
		}
	case "image":
		if msg.Image != nil {
			processed.MediaID = msg.Image.ID
			processed.MimeType = msg.Image.MimeType
			processed.Caption = msg.Image.Caption
		}
	case "document":
		if msg.Document != nil {
			processed.MediaID = msg.Document.ID
			processed.MimeType = msg.Document.MimeType
			processed.Caption = msg.Document.Caption
			processed.Filename = msg.Document.Filename
		}
	}

	return processed
}

// handleProcessedMessage handles the processed message (placeholder for your business logic)
func (h *WebhookHandler) handleProcessedMessage(msg ProcessedMessage) {
	// Track message counter: koko: prefix → koko command, else broadcast/chat.
	_, isKoko := trimKokoPrefix(msg.Content)
	if globalMetrics != nil {
		globalMetrics.IncrMessage(isKoko)
	}

	// Do NOT persist koko: commands to the database. Only store user chat messages.
	// This prevents command invocations from being included in summarization data.
	if !isKoko && h.store != nil && strings.TrimSpace(msg.AccountID) != "" && strings.TrimSpace(msg.GroupName) != "" && strings.TrimSpace(msg.UserName) != "" {
		payload, err := json.Marshal(msg)
		if err != nil {
			payload = []byte(`{"error":"marshal_failed"}`)
		}

		// Check storage before saving
		estimatedSize := len(payload) + len(msg.Content) + len(msg.MediaID) + 500 // rough estimate
		storageWarning := h.store.CheckStorageBeforeSave(context.Background(), msg.AccountID, estimatedSize)
		if strings.Contains(storageWarning, "EXCEEDED") {
			log.Printf("STORAGE_LIMIT_EXCEEDED: account=%s user=%s", msg.AccountID, msg.UserName)
			if chatIDInt, err := strconv.ParseInt(msg.ChatID, 10, 64); err == nil {
				_ = h.sendTelegramMessage(chatIDInt, storageWarning)
			}
			return
		}

		_, err = h.store.SaveMessengerInput(context.Background(), MessengerInput{
			AccountID:     msg.AccountID,
			GroupName:     msg.GroupName,
			UserName:      msg.UserName,
			MessengerType: defaultOrValue(msg.MessengerType, "telegram"),
			ChatID:        msg.ChatID,
			MessageID:     msg.ID,
			MessageType:   msg.Type,
			TextContent:   msg.Content,
			MediaID:       msg.MediaID,
			MimeType:      msg.MimeType,
			Payload:       payload,
			SentAt:        msg.Timestamp,
		})
		if err != nil {
			log.Printf("MESSAGE_PERSIST_FAILED: messenger=%s account=%s group=%s user=%s error=%v", msg.MessengerType, msg.AccountID, msg.GroupName, msg.UserName, err)
		} else if storageWarning != "" {
			// Send storage warning after successful save
			log.Printf("STORAGE_WARNING: account=%s usage_warning=%s", msg.AccountID, storageWarning)
			if chatIDInt, err := strconv.ParseInt(msg.ChatID, 10, 64); err == nil {
				_ = h.sendTelegramMessage(chatIDInt, storageWarning)
			}
		}
	}

	switch msg.Type {
	case "text":
		h.handleTextMessage(msg)
	case "audio", "video", "image", "document":
		h.handleMediaMessage(msg)
	}
}

// handleTextMessage routes koko commands from WhatsApp and Slack messages.
// Telegram messages are already fully handled in HandleTelegramWebhook; this
// function is a no-op for that platform.
func (h *WebhookHandler) handleTextMessage(msg ProcessedMessage) {
	// Telegram routing is already done in HandleTelegramWebhook.
	if msg.MessengerType == "telegram" || msg.MessengerType == "" {
		return
	}
	if msg.MessengerType != "whatsapp" {
		return
	}

	sender := strings.TrimSpace(msg.From)
	if sender == "" {
		return
	}
	rawSender := sender
	normalizedSender := normalizeWhatsAppSender(sender)
	if normalizedSender == "" {
		normalizedSender = sender
	}
	sender = normalizedSender

	chatID := strings.TrimSpace(msg.ChatID)
	if chatID == "" {
		chatID = sender
	}
	chatID = normalizeWhatsAppSender(chatID)
	if chatID == "" {
		chatID = sender
	}
	chatIDInt, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("WHATSAPP_CHAT_ID_PARSE_FAILED: chat_id=%s error=%v", chatID, err)
		return
	}
	h.setChatMessenger(chatIDInt, "whatsapp")

	// Resolve this sender's account and groups.
	accountID, groups, err := h.lookupUserGroupsByMessenger(sender, msg.MessengerType)
	if err == nil && strings.TrimSpace(accountID) == "" && sender != rawSender {
		accountID, groups, err = h.lookupUserGroupsByMessenger(rawSender, msg.MessengerType)
		if err == nil && strings.TrimSpace(accountID) != "" {
			sender = rawSender
		}
	}
	if err != nil {
		log.Printf("%s_LOOKUP_FAILED: raw_user=%s normalized_user=%s error=%v", strings.ToUpper(msg.MessengerType), rawSender, normalizedSender, err)
		_ = h.sendWhatsAppMessage(chatID, "Access denied: unable to verify registration.")
		return
	}
	if strings.TrimSpace(accountID) == "" || len(groups) == 0 {
		log.Printf("%s_UNREGISTERED_USER: raw_user=%s normalized_user=%s", strings.ToUpper(msg.MessengerType), rawSender, normalizedSender)
		_ = h.sendWhatsAppMessage(chatID, "Access denied: this WhatsApp number is not registered. Ask your Koko admin to add user 15108595150 with messenger type whatsapp to your account group.")
		return
	}
	if h.store != nil {
		if err := h.store.UpsertUserChatID(context.Background(), accountID, sender, "whatsapp", chatID); err != nil {
			log.Printf("WHATSAPP_CHAT_ID_PERSIST_FAILED: user=%s error=%v", sender, err)
		}
		if err := h.store.BackfillUserMessengerType(context.Background(), accountID, sender, "whatsapp", chatID); err != nil {
			log.Printf("WHATSAPP_MESSENGER_TYPE_BACKFILL_FAILED: user=%s error=%v", sender, err)
		}
	}

	state := h.getOrCreateChatState(chatIDInt)
	state.ActiveAccountID = accountID
	if strings.TrimSpace(state.ActiveGroupName) == "" {
		state.ActiveGroupName = groups[0].GroupName
		state.ActiveGroupID = groups[0].GroupID
	}

	matchedActiveGroup := groups[0]
	for _, g := range groups {
		if g.GroupID == state.ActiveGroupID || strings.EqualFold(g.GroupName, state.ActiveGroupName) {
			matchedActiveGroup = g
			break
		}
	}

	msg.AccountID = accountID
	msg.GroupName = matchedActiveGroup.GroupName
	msg.UserName = sender
	msg.ChatID = chatID

	// Record first interaction (for T&C tracking).
	h.sendFirstTimeTermsNoticeByKey(msg.MessengerType+":"+sender, accountID, groups)

	control := h.applyCommandDefaults(chatIDInt, parseTelegramControlCommand(msg.Content))
	if control.Name == "" {
		if query, ok := extractKokoNaturalLanguageQuery(msg.Content); ok {
			if err := h.runKokoNaturalLanguageAssistant(chatIDInt, sender, control, query); err != nil {
				log.Printf("WHATSAPP_ASSISTANT_FAILED: %v", err)
				_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(ParsedControlCommand{Name: "koko:assistant", Domain: control.Domain}, "Temporary expert lookup failed. Please try again."))
			}
		} else {
			// Regular group chat message: broadcast to same group across messenger platforms.
			broadcastText := sender + ": " + msg.Content
			h.broadcastToGroupByMessenger(accountID, matchedActiveGroup.GroupName, "whatsapp", chatID, broadcastText)
		}
		return
	}

	switch control.Name {
	case telegramSummarizeCommand:
		var summary string
		var summarizeErr error
		if !control.HasDateRange {
			summary, summarizeErr = h.summarizeFromDBOrCache(chatIDInt, control.Domain, control.UserContext, accountID, matchedActiveGroup.GroupName)
		} else {
			summary, summarizeErr = h.summarizeFromDBOrCacheWithDateRange(chatIDInt, control.Domain, control.UserContext, accountID, matchedActiveGroup.GroupName, control.DateStart, control.DateEnd)
		}
		if summarizeErr != nil {
			log.Printf("WHATSAPP_SUMMARIZATION_FAILED: %v", summarizeErr)
			_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(control, "Summarization failed. Please try again."))
			return
		}
		state := h.getOrCreateChatState(chatIDInt)
		state.LastSummary = summary
		state.LastSummaryDomain = normalizedDomainKey(control.Domain)
		state.LastSummaryAt = time.Now().UTC()
		state.LastSummaryHasDateRange = control.HasDateRange
		state.LastSummaryStart = control.DateStart
		state.LastSummaryEnd = control.DateEnd
		if d := normalizedDomainKey(control.Domain); d != featureModeAll {
			h.setGroupLastDomain(accountID, matchedActiveGroup.GroupName, d)
		}
		kokoResp := formatKokoCommandResponse(control, summary)
		_ = h.sendWhatsAppMessage(chatID, kokoResp)
		h.broadcastToGroupByMessenger(accountID, matchedActiveGroup.GroupName, "whatsapp", chatID, "From "+sender+":\n\n"+kokoResp)
	case telegramAnalyzeCommand:
		if err := h.runFeatureAnalyze(chatIDInt, control, sender, accountID, matchedActiveGroup); err != nil {
			log.Printf("WHATSAPP_ANALYZE_FAILED: %v", err)
			_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(control, "Analyze command failed. Please try again."))
		}
	case telegramActCommand:
		if err := h.runFeatureAct(chatIDInt, control, sender, false); err != nil {
			log.Printf("WHATSAPP_ACT_FAILED: %v", err)
			_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(control, "Act command failed. Please try again."))
		}
	case telegramGroupCommand:
		if err := h.runFeatureGroupCommand(chatIDInt, sender, control); err != nil {
			log.Printf("WHATSAPP_GROUP_SELECT_FAILED: %v", err)
			_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(control, "Group selection failed. Please try again."))
		}
	case telegramPlanCommand:
		if err := h.runFeaturePlan(chatIDInt, control, sender, accountID, matchedActiveGroup); err != nil {
			log.Printf("WHATSAPP_PLAN_FAILED: %v", err)
			_ = h.sendWhatsAppMessage(chatID, formatKokoCommandResponse(control, "Plan command failed. Please try again."))
		}
	case telegramAddGroupCommand:
		if err := h.runFeatureAddGroup(chatIDInt, sender, accountID, msg.MessengerType, control); err != nil {
			log.Printf("%s_ADD_GROUP_FAILED: user=%s error=%v", strings.ToUpper(msg.MessengerType), sender, err)
		}
	case telegramDeleteGroupCommand:
		if err := h.runFeatureDeleteGroup(chatIDInt, sender, accountID, control); err != nil {
			log.Printf("%s_DELETE_GROUP_FAILED: user=%s error=%v", strings.ToUpper(msg.MessengerType), sender, err)
		}
	case telegramAddUserCommand:
		if err := h.runFeatureAddUser(chatIDInt, sender, accountID, msg.MessengerType, control); err != nil {
			log.Printf("%s_ADD_USER_FAILED: user=%s error=%v", strings.ToUpper(msg.MessengerType), sender, err)
		}
	case telegramDeleteUserCommand:
		if err := h.runFeatureDeleteUser(chatIDInt, sender, accountID, control); err != nil {
			log.Printf("%s_DELETE_USER_FAILED: user=%s error=%v", strings.ToUpper(msg.MessengerType), sender, err)
		}
	case telegramHelpCommand:
		if strings.TrimSpace(control.Argument) == "" {
			_ = h.sendWhatsAppMessage(chatID, h.buildTelegramCommandMenuForUser(sender))
		} else if err := h.runFeatureHelp(chatIDInt, control.Argument); err != nil {
			log.Printf("WHATSAPP_HELP_FAILED: %v", err)
		}
	default:
		if looksLikeKokoPrefixedInput(msg.Content) {
			helpText := h.buildTelegramCommandMenuForUser(sender)
			_ = h.sendWhatsAppMessage(chatID, helpText)
		}
	}
}

// handleMediaMessage handles media messages
func (h *WebhookHandler) handleMediaMessage(msg ProcessedMessage) {
	// Handle Slack files
	if len(msg.Files) > 0 {
		for range msg.Files {
			// TODO: Download file using Slack API
			// You can download files using the URL with Authorization: Bearer {bot-token}
		}
		return
	}

	if msg.MessengerType != "whatsapp" {
		return
	}

	if strings.TrimSpace(msg.MediaID) == "" {
		return
	}

	downloadURL, err := h.GetMessengerFileURL("whatsapp", msg.MediaID)
	if err != nil {
		log.Printf("WHATSAPP_MEDIA_URL_RESOLVE_FAILED: media_id=%s type=%s error=%v", msg.MediaID, msg.Type, err)
		_ = h.sendWhatsAppMessage(msg.ChatID, "I could not access your media right now. Please resend in a moment.")
		return
	}

	mediaBytes, err := h.DownloadMessengerFile("whatsapp", downloadURL)
	if err != nil {
		log.Printf("WHATSAPP_MEDIA_DOWNLOAD_FAILED: media_id=%s type=%s error=%v", msg.MediaID, msg.Type, err)
		_ = h.sendWhatsAppMessage(msg.ChatID, "I could not download your media right now. Please resend in a moment.")
		return
	}

	if len(mediaBytes) == 0 {
		_ = h.sendWhatsAppMessage(msg.ChatID, "Your media appears empty. Please try again.")
		return
	}

	if len(mediaBytes) > maxVertexAIInlineBytes {
		ack := fmt.Sprintf("Received your %s message (%d bytes). It is too large for inline AI processing right now.", msg.Type, len(mediaBytes))
		_ = h.sendWhatsAppMessage(msg.ChatID, ack)
		return
	}

	mimeType := strings.TrimSpace(msg.MimeType)
	if mimeType == "" {
		mimeType = defaultTelegramMimeType(msg.Type)
	}

	prompt := fmt.Sprintf("Summarize this WhatsApp %s content for operational follow-up. Return concise bullet points and action items.", msg.Type)
	if msg.Filename != "" {
		prompt += " Filename: " + msg.Filename + "."
	}

	summary, err := h.callVertexAIGenerateContent([]map[string]interface{}{
		{"text": prompt},
		{
			"inline_data": map[string]string{
				"mime_type": mimeType,
				"data":      base64.StdEncoding.EncodeToString(mediaBytes),
			},
		},
	})
	if err != nil {
		log.Printf("WHATSAPP_MEDIA_SUMMARY_FAILED: media_id=%s type=%s error=%v", msg.MediaID, msg.Type, err)
		_ = h.sendWhatsAppMessage(msg.ChatID, "Received your media, but AI processing is temporarily unavailable. Please try again shortly.")
		return
	}

	if strings.TrimSpace(summary) == "" {
		_ = h.sendWhatsAppMessage(msg.ChatID, "Received your media. No summary content was generated.")
		return
	}

	_ = h.sendWhatsAppMessage(msg.ChatID, "Media summary:\n"+summary)
	if strings.TrimSpace(msg.AccountID) != "" && strings.TrimSpace(msg.GroupName) != "" {
		broadcastText := msg.UserName + " shared " + msg.Type + ".\n\nMedia summary:\n" + summary
		h.broadcastToGroupByMessenger(msg.AccountID, msg.GroupName, "whatsapp", strings.TrimSpace(msg.ChatID), broadcastText)
	}
}

// sendTelegramMessage sends a message to a Telegram chat
// Uses Telegram Bot API sendMessage endpoint with Markdown formatting
func (h *WebhookHandler) sendTelegramMessage(chatID int64, text string) error {
	if h.telegramBotToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is not configured")
	}

	if chatID == 0 {
		return fmt.Errorf("invalid chat_id: 0")
	}

	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty message text")
	}

	requestURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", h.telegramBotToken)
	chunks := splitTelegramMessageChunks(text, maxTelegramSendChunkBytes)
	for _, chunk := range chunks {
		payload := map[string]interface{}{
			"chat_id": chatID,
			"text":    chunk,
		}

		// Reuse a pooled buffer for JSON encoding.
		buf := getBuf()
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			putBuf(buf)
			return fmt.Errorf("failed to marshal telegram message: %w", err)
		}
		jsonBody := make([]byte, buf.Len())
		copy(jsonBody, buf.Bytes())
		putBuf(buf)

		var lastErr error
		for attempt := 1; attempt <= maxTelegramFetchAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(jsonBody))
			if err != nil {
				cancel()
				return fmt.Errorf("failed to create telegram request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := h.httpClient.Do(req)
			cancel()
			if err != nil {
				lastErr = fmt.Errorf("telegram sendMessage request failed: %s", sanitizeTelegramError(err, h.telegramBotToken))
				if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(err, 0) {
					time.Sleep(telegramRetryBackoff(attempt))
					continue
				}
				return lastErr
			}

			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = fmt.Errorf("failed to read telegram response: %s", sanitizeTelegramError(readErr, h.telegramBotToken))
				if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(readErr, resp.StatusCode) {
					time.Sleep(telegramRetryBackoff(attempt))
					continue
				}
				return lastErr
			}

			if resp.StatusCode >= 300 {
				lastErr = fmt.Errorf("telegram sendMessage failed: status=%d body=%s", resp.StatusCode, string(body))
				if attempt < maxTelegramFetchAttempts && shouldRetryTelegramRequest(nil, resp.StatusCode) {
					time.Sleep(telegramRetryBackoff(attempt))
					continue
				}
				return lastErr
			}

			lastErr = nil
			break
		}

		if lastErr != nil {
			return lastErr
		}
	}

	log.Printf("TELEGRAM_MESSAGE_SENT: chat_id=%d", chatID)
	return nil
}

func splitTelegramMessageChunks(text string, maxChunkBytes int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return []string{text}
	}

	if len(trimmed) <= maxChunkBytes {
		return []string{trimmed}
	}

	chunks := []string{}
	remaining := trimmed
	for len(remaining) > maxChunkBytes {
		splitAt := strings.LastIndex(remaining[:maxChunkBytes], "\n")
		if splitAt <= 0 {
			splitAt = maxChunkBytes
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:splitAt]))
		remaining = strings.TrimSpace(remaining[splitAt:])
	}

	if remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks
}

const quotaWarnThreshold = 0.80

// checkAndWarnQuota inspects the account's resource usage and broadcasts a warning to all
// group members if storage or monthly command quotas are at or above quotaWarnThreshold.
func (h *WebhookHandler) checkAndWarnQuota(accountID string) {
	if h.store == nil || !h.store.DBEnabled() || strings.TrimSpace(accountID) == "" {
		return
	}

	account, err := h.store.GetAccount(context.Background(), accountID)
	if err != nil {
		log.Printf("QUOTA_CHECK_GET_ACCOUNT_FAILED: account=%s error=%v", accountID, err)
		return
	}

	tier, err := h.store.GetPlanTierForAccount(context.Background(), accountID)
	if err != nil {
		log.Printf("QUOTA_CHECK_GET_TIER_FAILED: account=%s error=%v", accountID, err)
		return
	}

	var warnings []string

	// Storage quota
	if tier.StorageBytes > 0 {
		pct := float64(account.TotalStorageUsed) / float64(tier.StorageBytes)
		if pct >= quotaWarnThreshold {
			warnings = append(warnings, fmt.Sprintf("Storage: %s / %s used (%.0f%%)",
				formatBytes(account.TotalStorageUsed), formatBytes(tier.StorageBytes), pct*100))
		}
	}

	// Monthly command quotas
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	type cmdCheck struct {
		name  string
		limit int
	}
	cmdChecks := []cmdCheck{
		{telegramSummarizeCommand, tier.MonthlySummarizeLimit},
		{telegramAnalyzeCommand, tier.MonthlyAnalyzeLimit},
		{telegramActCommand, tier.MonthlyActLimit},
	}
	for _, cc := range cmdChecks {
		if cc.limit <= 0 {
			continue
		}
		count, err := h.store.CountCommandUsage(context.Background(), accountID, "", cc.name, monthStart, monthEnd)
		if err != nil {
			continue
		}
		pct := float64(count) / float64(cc.limit)
		if pct >= quotaWarnThreshold {
			warnings = append(warnings, fmt.Sprintf("%s: %d / %d uses this month (%.0f%%)",
				cc.name, count, cc.limit, pct*100))
		}
	}

	if len(warnings) == 0 {
		return
	}

	var tierName string
	if tier.ID != 0 {
		tierName = tier.TierCode
	}
	msgLines := []string{
		"koko: quota warning",
		"Your account is nearing its resource limits:",
	}
	msgLines = append(msgLines, warnings...)
	if tierName != "" {
		msgLines = append(msgLines, "\nTo request a plan upgrade: koko:plan <tier_code>")
		msgLines = append(msgLines, "(Use koko:plan to see available tiers.)")
	}
	msg := strings.Join(msgLines, "\n")
	h.broadcastToAllAccountGroups(accountID, msg)
}

// StartQuotaChecker launches a background goroutine that periodically checks all accounts
// for resource quota usage and warns users if limits are approaching.
func (h *WebhookHandler) StartQuotaChecker() {
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if h.store == nil || !h.store.DBEnabled() {
				continue
			}
			accounts, err := h.store.ListAccounts(context.Background())
			if err != nil {
				log.Printf("QUOTA_CHECKER_LIST_ACCOUNTS_FAILED: %v", err)
				continue
			}
			for _, acc := range accounts {
				h.checkAndWarnQuota(acc.AccountID)
			}
		}
	}()
}
