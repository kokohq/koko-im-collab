package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// richMediaRateLimitDuration is the minimum interval between accepted rich-media
	// requests for the same group (any command / mode combination).
	richMediaRateLimitDuration = 2 * time.Minute

	// richMediaQueueCapacity is the maximum number of requests that can queue up
	// waiting for the worker goroutine.
	richMediaQueueCapacity = 64

	// ddosWindowDuration / ddosMaxRequestsPerSender define the per-sender
	// sliding-window guard against excessive rich-media submissions.
	ddosWindowDuration       = 1 * time.Minute
	ddosMaxRequestsPerSender = 10

	// googleTTSEndpoint is the Cloud Text-to-Speech REST API.
	googleTTSEndpoint = "https://texttospeech.googleapis.com/v1/text:synthesize"

	// googleTTSMaxChars is the safe character budget to stay under the TTS 5 000-byte limit.
	googleTTSMaxChars = 4500

	// googleTTSSSMLMaxChars is the safe spoken-text budget when wrapping in SSML.
	// SSML break/prosody tags add overhead; keep actual prose well under 5 000 bytes total.
	googleTTSSSMLMaxChars = 3000

	// richMediaMaxTextBytes is the maximum input length passed to the visual renderer.
	richMediaMaxTextBytes = 4000

	// pubsubPullMaxMessages is how many messages are requested per pull call.
	pubsubPullMaxMessages = 10
	// pubsubPullInterval is the sleep between pull polls when the subscription is idle.
	pubsubPullInterval = 2 * time.Second
	// pubsubPullBackoffMax caps exponential backoff on repeated pull errors.
	pubsubPullBackoffMax = 30 * time.Second
)

// RichMediaRequest represents a single queued ui/voice output request.
// Multiple same-group same-command requests within the rate-limit window are
// deduplicated into one entry (all requesters are listed in the final response).
type RichMediaRequest struct {
	// ChatIDs and Requesters are aligned slices; dedup may append to both.
	ChatIDs    []int64
	Requesters []string

	AccountID   string
	GroupName   string
	CommandName string // e.g. telegramSummarizeCommand
	TextContent string // pre-computed text result to transform
	OutputMode  string // "ui" or "voice"
	EnqueuedAt  time.Time
}

// RichMediaQueue manages rate-limited, deduplicated, and sequentially processed
// rich-media (ui/voice) requests across all groups.
type RichMediaQueue struct {
	mu sync.Mutex

	// Per-group rate limit: richMediaRateKey → time of last accepted request.
	lastAccepted map[string]time.Time

	// In-flight dedup map: richMediaDedupKey → pointer to the queued request.
	// Removed when the worker starts processing that request.
	inQueue map[string]*RichMediaRequest

	// Per-sender DDOS guard: senderKey → recent request timestamps within ddosWindowDuration.
	senderTimes map[string][]time.Time

	// Buffered work channel consumed by the in-memory worker goroutine.
	// Only used when Pub/Sub is not configured.
	ch chan *RichMediaRequest

	// Pub/Sub fields: populated by Start when PUBSUB_RICH_MEDIA_TOPIC is set.
	pubsubTopic        string
	pubsubSubscription string
	pubsub             *pubSubClient
}

// newRichMediaQueue allocates and returns an idle RichMediaQueue.
// Call Start to launch the worker goroutine before enqueuing any requests.
func newRichMediaQueue() *RichMediaQueue {
	return &RichMediaQueue{
		lastAccepted:       make(map[string]time.Time),
		inQueue:            make(map[string]*RichMediaRequest),
		senderTimes:        make(map[string][]time.Time),
		ch:                 make(chan *RichMediaRequest, richMediaQueueCapacity),
		pubsubTopic:        os.Getenv(envPubSubRichMediaTopic),
		pubsubSubscription: os.Getenv(envPubSubRichMediaSub),
	}
}

func richMediaRateKey(accountID, groupName string) string {
	return accountID + "/" + groupName
}

func richMediaDedupKey(accountID, groupName, commandName, outputMode string) string {
	return accountID + "/" + groupName + "/" + commandName + "/" + outputMode
}

// TryEnqueue attempts to enqueue req for asynchronous rich-media processing.
//
//   - senderKey uniquely identifies the individual requester (e.g. Telegram username).
//   - Returns (queued bool, ackMsg string).  When queued=false but ackMsg is non-empty
//     the request was either merged with an in-flight duplicate or rate-limited; send
//     ackMsg back to the user.  An empty ackMsg means a silent internal failure.
func (q *RichMediaQueue) TryEnqueue(req *RichMediaRequest, senderKey string) (bool, string) {
	req.EnqueuedAt = time.Now().UTC()
	now := req.EnqueuedAt

	q.mu.Lock()

	// ── DDOS protection: per-sender sliding-window counter ──────────────────
	windowStart := now.Add(-ddosWindowDuration)
	times := q.senderTimes[senderKey]
	filtered := times[:0]
	for _, t := range times {
		if t.After(windowStart) {
			filtered = append(filtered, t)
		}
	}
	filtered = append(filtered, now)
	q.senderTimes[senderKey] = filtered
	if len(filtered) > ddosMaxRequestsPerSender {
		q.mu.Unlock()
		return false, "Too many requests. Please wait a moment before trying again."
	}

	// ── Deduplication: merge with an in-flight request for same group+cmd+mode ──
	dk := richMediaDedupKey(req.AccountID, req.GroupName, req.CommandName, req.OutputMode)
	if existing, found := q.inQueue[dk]; found {
		existing.ChatIDs = append(existing.ChatIDs, req.ChatIDs...)
		existing.Requesters = append(existing.Requesters, req.Requesters...)
		q.mu.Unlock()
		return false, "Your request has been noted. You'll receive the " + req.OutputMode + " response when it's ready."
	}

	// ── Per-group rate limit: 1 rich-media request per 2 minutes ────────────
	rk := richMediaRateKey(req.AccountID, req.GroupName)
	if last, ok := q.lastAccepted[rk]; ok && now.Sub(last) < richMediaRateLimitDuration {
		remaining := richMediaRateLimitDuration - now.Sub(last)
		secs := int(remaining.Seconds()) + 1
		q.mu.Unlock()
		return false, fmt.Sprintf("Rich media requests for this group are limited to one every 2 minutes. Please try again in about %d second(s).", secs)
	}

	// ── Queue capacity guard (in-memory mode only) ──────────────────────────
	if q.pubsub == nil && len(q.ch) >= cap(q.ch) {
		q.mu.Unlock()
		return false, "Our servers are currently at capacity. Please try again in a moment."
	}

	// Commit the enqueue state before releasing the lock.
	q.lastAccepted[rk] = now
	q.inQueue[dk] = req
	q.mu.Unlock()

	// Route to Cloud Pub/Sub when configured; fall back to in-memory channel.
	if q.pubsub != nil {
		data, marshalErr := json.Marshal(req)
		if marshalErr != nil {
			log.Printf("RICH_MEDIA_PUBSUB_MARSHAL_FAILED: group=%s err=%v", req.GroupName, marshalErr)
			q.mu.Lock()
			delete(q.inQueue, dk)
			q.mu.Unlock()
			return false, "Our servers are currently at capacity. Please try again in a moment."
		}
		if pubErr := q.pubsub.publish(q.pubsubTopic, data, map[string]string{
			"outputMode":  req.OutputMode,
			"commandName": req.CommandName,
		}); pubErr != nil {
			log.Printf("RICH_MEDIA_PUBSUB_PUBLISH_FAILED: group=%s err=%v", req.GroupName, pubErr)
			q.mu.Lock()
			delete(q.inQueue, dk)
			q.mu.Unlock()
			return false, "Our servers are currently at capacity. Please try again in a moment."
		}
		return true, "⏳ Your " + req.OutputMode + " response is being prepared. You'll receive it shortly."
	}

	// In-memory channel path.
	select {
	case q.ch <- req:
		return true, "⏳ Your " + req.OutputMode + " response is being prepared. You'll receive it shortly."
	default:
		// Extremely rare race between the capacity check and this send; revert state.
		q.mu.Lock()
		delete(q.inQueue, dk)
		q.mu.Unlock()
		return false, "Our servers are currently at capacity. Please try again in a moment."
	}
}

// Start initialises the worker for rich-media processing.  When the
// PUBSUB_RICH_MEDIA_TOPIC and PUBSUB_RICH_MEDIA_SUBSCRIPTION environment
// variables are set it creates (or verifies) the Pub/Sub topic + subscription
// and starts a pull-based worker goroutine.  If Pub/Sub cannot be reached at
// startup, or if the env vars are absent, it falls back to the in-memory
// buffered channel.  Call exactly once from main after NewWebhookHandler.
func (q *RichMediaQueue) Start(h *WebhookHandler) {
	if q.pubsubTopic != "" && q.pubsubSubscription != "" {
		projectID, projErr := h.googleProjectID()
		if projErr != nil {
			log.Printf("RICH_MEDIA_PUBSUB_INIT: cannot get project ID (%v) — using in-memory queue", projErr)
		} else {
			ps := newPubSubClient(projectID, h.httpClient, h.googleAccessToken)
			if topicErr := ps.ensureTopicExists(q.pubsubTopic); topicErr != nil {
				log.Printf("RICH_MEDIA_PUBSUB_INIT: ensure topic failed (%v) — using in-memory queue", topicErr)
			} else if subErr := ps.ensureSubscriptionExists(q.pubsubSubscription, q.pubsubTopic); subErr != nil {
				log.Printf("RICH_MEDIA_PUBSUB_INIT: ensure subscription failed (%v) — using in-memory queue", subErr)
			} else {
				q.mu.Lock()
				q.pubsub = ps
				q.mu.Unlock()
				log.Printf("RICH_MEDIA_PUBSUB_ENABLED: topic=%s subscription=%s project=%s",
					q.pubsubTopic, q.pubsubSubscription, projectID)
				go q.runPubSubWorker(h)
				return
			}
		}
	}
	go q.runChannelWorker(h)
}

// runChannelWorker processes rich-media requests from the in-memory buffered
// channel.  Used when Pub/Sub is not configured.
func (q *RichMediaQueue) runChannelWorker(h *WebhookHandler) {
	for req := range q.ch {
		dk := richMediaDedupKey(req.AccountID, req.GroupName, req.CommandName, req.OutputMode)
		q.mu.Lock()
		delete(q.inQueue, dk)
		q.mu.Unlock()
		processRichMediaRequest(h, req)
	}
}

// runPubSubWorker pulls rich-media requests from Cloud Pub/Sub and processes
// them sequentially.  Runs indefinitely; applies exponential backoff on pull
// errors.  Each message is acknowledged only after successful processing so
// that transient failures trigger automatic Pub/Sub redelivery.
func (q *RichMediaQueue) runPubSubWorker(h *WebhookHandler) {
	backoff := pubsubPullInterval
	for {
		msgs, pullErr := q.pubsub.pull(q.pubsubSubscription, pubsubPullMaxMessages)
		if pullErr != nil {
			log.Printf("RICH_MEDIA_PUBSUB_PULL_FAILED: %v", pullErr)
			time.Sleep(backoff)
			if backoff < pubsubPullBackoffMax {
				backoff *= 2
			}
			continue
		}
		backoff = pubsubPullInterval // reset on success

		if len(msgs) == 0 {
			time.Sleep(pubsubPullInterval)
			continue
		}

		ackIDs := make([]string, 0, len(msgs))
		for _, msg := range msgs {
			rawData, decErr := base64.StdEncoding.DecodeString(msg.Message.Data)
			if decErr != nil {
				log.Printf("RICH_MEDIA_PUBSUB_MSG_CORRUPTED: msgId=%s err=%v — acking to prevent redelivery",
					msg.Message.MessageID, decErr)
				ackIDs = append(ackIDs, msg.AckID)
				continue
			}
			var req RichMediaRequest
			if unmarshalErr := json.Unmarshal(rawData, &req); unmarshalErr != nil {
				log.Printf("RICH_MEDIA_PUBSUB_MSG_UNMARSHAL_FAILED: msgId=%s err=%v — acking to prevent redelivery",
					msg.Message.MessageID, unmarshalErr)
				ackIDs = append(ackIDs, msg.AckID)
				continue
			}
			dk := richMediaDedupKey(req.AccountID, req.GroupName, req.CommandName, req.OutputMode)
			q.mu.Lock()
			delete(q.inQueue, dk)
			q.mu.Unlock()

			processRichMediaRequest(h, &req)
			ackIDs = append(ackIDs, msg.AckID)
		}

		if len(ackIDs) > 0 {
			if ackErr := q.pubsub.acknowledge(q.pubsubSubscription, ackIDs); ackErr != nil {
				log.Printf("RICH_MEDIA_PUBSUB_ACK_FAILED: %v", ackErr)
			}
		}
	}
}

// processRichMediaRequest performs the ui/voice rendering and delivers results
// to every requester listed in req.
func processRichMediaRequest(h *WebhookHandler, req *RichMediaRequest) {
	label := buildIssuedCommandLabel(ParsedControlCommand{
		Name:       req.CommandName,
		OutputMode: req.OutputMode,
	})
	requestersList := strings.Join(req.Requesters, ", ")

	switch req.OutputMode {
	case "ui":
		jpegData, err := generateJPEGFromText(h, req.TextContent)
		if err != nil {
			// Fall back to formatted Markdown text message on image generation failure.
			log.Printf("RICH_MEDIA_UI_JPEG_FAILED: group=%s err=%v — sending formatted text instead", req.GroupName, err)
			visual, textErr := generateVisualRepresentation(h, req.TextContent)
			if textErr != nil {
				visual = req.TextContent
			}
			msg := label + " [visual]\n\nRequested by: " + requestersList + "\n\n" + visual
			for _, chatID := range req.ChatIDs {
				_ = h.sendTelegramMessage(chatID, msg)
			}
			return
		}
		caption := label + " — Requested by: " + requestersList
		for _, chatID := range req.ChatIDs {
			if photoErr := h.sendTelegramPhoto(chatID, jpegData, caption); photoErr != nil {
				log.Printf("RICH_MEDIA_PHOTO_SEND_FAILED: chatID=%d err=%v", chatID, photoErr)
				_ = h.sendTelegramMessage(chatID, label+" [image delivery failed — text version was already delivered]")
			}
		}

	case "voice":
		mp3, err := generateVoiceSummary(h, req.TextContent)
		if err != nil {
			log.Printf("RICH_MEDIA_VOICE_FAILED: group=%s err=%v", req.GroupName, err)
			msg := label + " [voice render failed — text version was already delivered]\n\nRequested by: " + requestersList
			for _, chatID := range req.ChatIDs {
				_ = h.sendTelegramMessage(chatID, msg)
			}
			return
		}
		caption := label + " — Requested by: " + requestersList
		for _, chatID := range req.ChatIDs {
			if err := h.sendTelegramAudio(chatID, mp3, caption); err != nil {
				log.Printf("RICH_MEDIA_AUDIO_SEND_FAILED: chatID=%d err=%v", chatID, err)
				_ = h.sendTelegramMessage(chatID, label+" [audio delivery failed — text version was already delivered]")
			}
		}

	default:
		log.Printf("RICH_MEDIA_UNKNOWN_MODE: mode=%q group=%s", req.OutputMode, req.GroupName)
	}
}

// ── Rendering helpers ────────────────────────────────────────────────────────

// generateVisualRepresentation asks Gemini to reformat text as a rich
// Markdown document with tables, ASCII bar charts, and structured layout.
func generateVisualRepresentation(h *WebhookHandler, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("empty text for visual representation")
	}

	// Trim to reasonable length before sending to the model.
	truncated := text
	if len(truncated) > richMediaMaxTextBytes {
		truncated = truncated[:richMediaMaxTextBytes]
	}

	prompt := strings.Join([]string{
		"Transform the following business report into a rich, structured visual representation.",
		"Use Markdown formatting: tables for tabular data, bullet-point hierarchies for lists,",
		"ASCII bar charts where appropriate (using '█' and '░' characters), bold section headers,",
		"and clear visual separators (───).",
		"Keep all factual data intact. Do not add information that is not in the source text.",
		"Output only the formatted report — no preamble, no commentary.",
		"",
		"SOURCE TEXT:",
		truncated,
	}, "\n")

	return h.callVertexAIGenerateContent([]map[string]interface{}{
		{"text": prompt},
	})
}

// buildVoiceScript asks Gemini to rewrite a business report as a broadcast-quality
// spoken briefing: no markdown, no bullet points, natural professional prose.
func buildVoiceScript(h *WebhookHandler, report string) (string, error) {
	truncated := report
	if len(truncated) > richMediaMaxTextBytes {
		truncated = truncated[:richMediaMaxTextBytes]
	}
	prompt := strings.Join([]string{
		"You are a professional broadcast journalist preparing a business intelligence audio briefing.",
		"Rewrite the following business report as a spoken script for a professional business podcast.",
		"",
		"STRICT RULES:",
		"- Write in complete, flowing sentences only. No bullet points, numbered lists, or tables.",
		"- Remove ALL Markdown: no *, **, #, ──, ━, backticks, square brackets, or dashes as list markers.",
		"- Spell out every symbol and abbreviation: ₹ → rupees, % → percent, Cr → crore, L → lakh,",
		"  k → thousand, MoM → month over month, YoY → year over year, Q1/Q2/Q3/Q4 → first/second/third/fourth quarter,",
		"  FY → financial year, GST → G.S.T., ITC → I.T.C., B2B → business-to-business, BIS → B.I.S.",
		"- Use professional broadcast transitions: 'Turning to...', 'On the subject of...', 'Notably,',",
		"  'It is worth highlighting that...', 'Meanwhile,', 'In closing,'.",
		"- Open EXACTLY with: 'Good day. This is your business intelligence briefing.'",
		"- Close EXACTLY with: 'That concludes today's briefing. Thank you.'",
		"- Target 250 to 320 words total. This is a timed slot.",
		"- Output ONLY the spoken script. No preamble, no notes, no commentary.",
		"",
		"REPORT TO CONVERT:",
		truncated,
	}, "\n")
	return h.callVertexAIGenerateContent([]map[string]interface{}{
		{"text": prompt},
	})
}

// spokenScriptToSSML wraps a plain spoken script in SSML markup with natural
// sentence-level pauses and a slightly measured cadence, tuned for Journey voices.
func spokenScriptToSSML(script string) string {
	text := strings.TrimSpace(script)
	if len(text) > googleTTSSSMLMaxChars {
		text = text[:googleTTSSSMLMaxChars]
		if idx := strings.LastIndex(text, "."); idx > googleTTSSSMLMaxChars/2 {
			text = text[:idx+1]
		}
		text += " This briefing has been shortened."
	}

	var sb strings.Builder
	sb.WriteString("<speak>")

	paragraphs := strings.Split(text, "\n")
	first := true
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Escape user/model text before embedding in SSML to avoid XML parse errors.
		para = escapeSSMLText(para)
		if !first {
			sb.WriteString(`<break time="700ms"/>`)
		}
		first = false
		// Add a short pause after each sentence-ending punctuation mark.
		para = strings.ReplaceAll(para, ". ", `.<break time="400ms"/> `)
		para = strings.ReplaceAll(para, "! ", `!<break time="400ms"/> `)
		para = strings.ReplaceAll(para, "? ", `?<break time="400ms"/> `)
		sb.WriteString(para)
	}

	sb.WriteString("</speak>")
	return sb.String()
}

func escapeSSMLText(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

// generateVoiceSummary rewrites the report as a professional spoken briefing
// using Gemini, wraps it in SSML, then synthesizes audio with Google Cloud
// Text-to-Speech (Journey voice) and returns the raw MP3 bytes.
// Reference: https://cloud.google.com/text-to-speech/docs/reference/rest/v1/text/synthesize
func generateVoiceSummary(h *WebhookHandler, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text for voice synthesis")
	}

	// Step 1: rewrite as broadcast-quality spoken script.
	script, err := buildVoiceScript(h, text)
	if err != nil {
		log.Printf("VOICE_SCRIPT_BUILD_FAILED: %v — falling back to raw text", err)
		script = text
	}

	// Step 2: convert to SSML with natural pauses.
	ssml := spokenScriptToSSML(script)
	// Safety valve: if the SSML is somehow still too large, shorten and rebuild.
	if len(ssml) > 4800 {
		shortened := script
		if len(shortened) > 2000 {
			shortened = shortened[:2000]
			if idx := strings.LastIndex(shortened, "."); idx > 1000 {
				shortened = shortened[:idx+1]
			}
			shortened += " This briefing has been shortened."
		}
		ssml = spokenScriptToSSML(shortened)
	}

	// Step 3: synthesize via TTS.
	accessToken, err := h.googleAccessToken()
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: failed to get access token: %w", err)
	}

	ssmlReq := map[string]interface{}{
		"input": map[string]string{"ssml": ssml},
		"voice": map[string]interface{}{
			"languageCode": "en-US",
			"name":         "en-US-Journey-F",
		},
		"audioConfig": map[string]interface{}{
			"audioEncoding": "MP3",
		},
	}

	mp3Bytes, synthErr := h.synthesizeVoiceMP3(accessToken, ssmlReq)
	if synthErr == nil {
		return mp3Bytes, nil
	}

	// Recovery path: if Journey+SSML is rejected (400 INVALID_ARGUMENT), retry
	// with plain text on Neural2 which is more permissive.
	if strings.Contains(synthErr.Error(), "TTS API returned 400") {
		log.Printf("VOICE_TTS_SSML_REJECTED: %v — retrying with plain text fallback", synthErr)

		plainText := script
		if len(plainText) > googleTTSMaxChars {
			plainText = plainText[:googleTTSMaxChars]
			if idx := strings.LastIndex(plainText, " "); idx > googleTTSMaxChars/2 {
				plainText = plainText[:idx]
			}
			plainText += " This briefing has been shortened."
		}

		fallbackReq := map[string]interface{}{
			"input": map[string]string{"text": plainText},
			"voice": map[string]interface{}{
				"languageCode": "en-US",
				"name":         "en-US-Neural2-F",
			},
			"audioConfig": map[string]interface{}{
				"audioEncoding": "MP3",
			},
		}

		if fallbackMP3, fallbackErr := h.synthesizeVoiceMP3(accessToken, fallbackReq); fallbackErr == nil {
			return fallbackMP3, nil
		} else {
			return nil, fallbackErr
		}
	}

	return nil, synthErr
}

func (h *WebhookHandler) synthesizeVoiceMP3(accessToken string, reqBody map[string]interface{}) ([]byte, error) {
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: marshal failed: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		googleTTSEndpoint,
		bytes.NewBuffer(jsonBody),
	)
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: create request failed: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: read response failed: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("voice synthesis: TTS API returned %d: %s",
			resp.StatusCode,
			truncateForLog(string(respBody), 400),
		)
	}

	var ttsResp struct {
		AudioContent string `json:"audioContent"` // base64-encoded MP3
	}
	if err := json.Unmarshal(respBody, &ttsResp); err != nil {
		return nil, fmt.Errorf("voice synthesis: unmarshal failed: %w", err)
	}
	if ttsResp.AudioContent == "" {
		return nil, fmt.Errorf("voice synthesis: empty audio content in TTS response")
	}

	mp3Bytes, err := base64.StdEncoding.DecodeString(ttsResp.AudioContent)
	if err != nil {
		return nil, fmt.Errorf("voice synthesis: base64 decode failed: %w", err)
	}
	return mp3Bytes, nil
}

// ── Telegram delivery helpers ────────────────────────────────────────────────

// sendTelegramAudio uploads an MP3 file to a Telegram chat using the sendAudio endpoint.
// Reference: https://core.telegram.org/bots/api#sendaudio
func (h *WebhookHandler) sendTelegramAudio(chatID int64, audioData []byte, caption string) error {
	apiURL := "https://api.telegram.org/bot" + h.telegramBotToken + "/sendAudio"

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("sendTelegramAudio: write chat_id: %w", err)
	}
	if c := strings.TrimSpace(caption); c != "" {
		if err := w.WriteField("caption", c); err != nil {
			return fmt.Errorf("sendTelegramAudio: write caption: %w", err)
		}
	}

	part, err := w.CreateFormFile("audio", "koko-summary.mp3")
	if err != nil {
		return fmt.Errorf("sendTelegramAudio: create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(audioData)); err != nil {
		return fmt.Errorf("sendTelegramAudio: copy audio data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("sendTelegramAudio: close writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, &body)
	if err != nil {
		return fmt.Errorf("sendTelegramAudio: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendTelegramAudio: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sendTelegramAudio: Telegram returned %d: %s",
			resp.StatusCode,
			truncateForLog(string(respBody), 400),
		)
	}
	return nil
}

// generateJPEGFromText calls Vertex AI Imagen to render a business intelligence
// dashboard image from the given text report and returns raw JPEG bytes.
// Reference: https://cloud.google.com/vertex-ai/generative-ai/docs/image/generate-images
func generateJPEGFromText(h *WebhookHandler, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("generateJPEGFromText: empty input text")
	}

	projectID, err := h.googleProjectID()
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: get project ID: %w", err)
	}
	accessToken, err := h.googleAccessToken()
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: get access token: %w", err)
	}

	// Trim text to a safe length for the image prompt.
	truncated := text
	if len(truncated) > richMediaMaxTextBytes {
		truncated = truncated[:richMediaMaxTextBytes]
	}

	imagePrompt := strings.Join([]string{
		"Create a professional business intelligence dashboard image.",
		"Style: clean white background, dark navy headers, colour-coded status indicators",
		"(green=good, amber=caution, red=risk), structured grid layout with clear section titles.",
		"Data sections to visualise:",
		truncated,
	}, " ")

	reqBody := map[string]interface{}{
		"instances": []map[string]interface{}{
			{"prompt": imagePrompt},
		},
		"parameters": map[string]interface{}{
			"sampleCount": 1,
			"aspectRatio": "4:3",
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: marshal request: %w", err)
	}

	// Imagen is only available in us-central1.
	endpoint := fmt.Sprintf(
		"https://us-central1-aiplatform.googleapis.com/v1/projects/%s/locations/us-central1/publishers/google/models/imagen-3.0-generate-001:predict",
		projectID,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("generateJPEGFromText: Imagen API returned %d: %s",
			resp.StatusCode, truncateForLog(string(respBody), 400))
	}

	var imagenResp struct {
		Predictions []struct {
			BytesBase64Encoded string `json:"bytesBase64Encoded"`
		} `json:"predictions"`
	}
	if err := json.Unmarshal(respBody, &imagenResp); err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: unmarshal response: %w", err)
	}
	if len(imagenResp.Predictions) == 0 || imagenResp.Predictions[0].BytesBase64Encoded == "" {
		return nil, fmt.Errorf("generateJPEGFromText: no image data in Imagen response")
	}

	jpegBytes, err := base64.StdEncoding.DecodeString(imagenResp.Predictions[0].BytesBase64Encoded)
	if err != nil {
		return nil, fmt.Errorf("generateJPEGFromText: base64 decode: %w", err)
	}
	return jpegBytes, nil
}

// sendTelegramPhoto uploads a JPEG image to a Telegram chat using the sendPhoto endpoint.
// Reference: https://core.telegram.org/bots/api#sendphoto
func (h *WebhookHandler) sendTelegramPhoto(chatID int64, jpegData []byte, caption string) error {
	apiURL := "https://api.telegram.org/bot" + h.telegramBotToken + "/sendPhoto"

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if err := w.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return fmt.Errorf("sendTelegramPhoto: write chat_id: %w", err)
	}
	if c := strings.TrimSpace(caption); c != "" {
		if err := w.WriteField("caption", c); err != nil {
			return fmt.Errorf("sendTelegramPhoto: write caption: %w", err)
		}
	}

	part, err := w.CreateFormFile("photo", "koko-visual.jpg")
	if err != nil {
		return fmt.Errorf("sendTelegramPhoto: create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(jpegData)); err != nil {
		return fmt.Errorf("sendTelegramPhoto: copy image data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("sendTelegramPhoto: close writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, &body)
	if err != nil {
		return fmt.Errorf("sendTelegramPhoto: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendTelegramPhoto: HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sendTelegramPhoto: Telegram returned %d: %s",
			resp.StatusCode, truncateForLog(string(respBody), 400))
	}
	return nil
}
