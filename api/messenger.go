package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Messenger-agnostic adapter layer
//
// MessengerAdapter abstracts the outbound operations needed to communicate with
// any messaging platform.  Platform-specific connection setup (bot tokens,
// webhook secrets, etc.) is kept inside each concrete adapter; callers in the
// application layer use only this interface.
//
// Supported platforms:
//   - Telegram  (TelegramAdapter)
//   - WhatsApp  (WhatsAppAdapter — stub, ready to implement)
//   - Slack     (SlackAdapter — stub, ready to implement)
// ─────────────────────────────────────────────────────────────────────────────

// MessengerAdapter is the outbound-side contract for all messaging platforms.
type MessengerAdapter interface {
	// MessengerType returns a stable identifier for the platform ("telegram", "whatsapp", "slack", etc.).
	MessengerType() string

	// SendText sends a plain-text message to the identified chat/channel.
	// chatID is the platform-native string representation (Telegram uses numeric IDs,
	// WhatsApp uses phone numbers, Slack uses channel IDs).
	SendText(chatID string, text string) error

	// SendAudio sends an MP3 audio clip to the identified chat/channel.
	// caption is an optional label shown alongside the clip.
	SendAudio(chatID string, audioData []byte, caption string) error

	// GetFileURL resolves a platform-native file identifier to a transient
	// download URL.  Not all platforms require this step; those that don't
	// may return the fileID unchanged.
	GetFileURL(fileID string) (string, error)

	// DownloadFile fetches raw bytes from a URL previously returned by GetFileURL.
	DownloadFile(downloadURL string) ([]byte, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// TelegramAdapter
// ─────────────────────────────────────────────────────────────────────────────

// TelegramAdapter implements MessengerAdapter for the Telegram Bot API.
type TelegramAdapter struct {
	botToken   string
	httpClient *http.Client
}

// NewTelegramAdapter creates a TelegramAdapter.  If httpClient is nil the
// package-level http.DefaultClient is used.
func NewTelegramAdapter(botToken string, httpClient *http.Client) *TelegramAdapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TelegramAdapter{botToken: botToken, httpClient: httpClient}
}

func (a *TelegramAdapter) MessengerType() string { return "telegram" }

func (a *TelegramAdapter) SendText(chatID string, text string) error {
	if a.botToken == "" {
		return fmt.Errorf("TelegramAdapter: bot token not configured")
	}
	cid, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		return fmt.Errorf("TelegramAdapter: invalid chatID %q: %w", chatID, err)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("TelegramAdapter: empty text")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", a.botToken)
	chunks := splitTelegramMessageChunks(text, maxTelegramSendChunkBytes)
	for _, chunk := range chunks {
		payload, _ := json.Marshal(map[string]interface{}{
			"chat_id": cid,
			"text":    chunk,
		})
		if err := a.postJSON(apiURL, payload); err != nil {
			return fmt.Errorf("TelegramAdapter.SendText: %w", err)
		}
	}
	return nil
}

func (a *TelegramAdapter) SendAudio(chatID string, audioData []byte, caption string) error {
	if a.botToken == "" {
		return fmt.Errorf("TelegramAdapter: bot token not configured")
	}
	apiURL := "https://api.telegram.org/bot" + a.botToken + "/sendAudio"

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", chatID); err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: write chat_id: %w", err)
	}
	if strings.TrimSpace(caption) != "" {
		if err := mw.WriteField("caption", caption); err != nil {
			return fmt.Errorf("TelegramAdapter.SendAudio: write caption: %w", err)
		}
	}
	fw, err := mw.CreateFormFile("audio", "summary.mp3")
	if err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: create form file: %w", err)
	}
	if _, err := io.Copy(fw, bytes.NewReader(audioData)); err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: copy audio: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: close writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, &body)
	if err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: create request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("TelegramAdapter.SendAudio: HTTP request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TelegramAdapter.SendAudio: Telegram returned %d: %s", resp.StatusCode, truncateForLog(string(b), 200))
	}
	return nil
}

func (a *TelegramAdapter) GetFileURL(fileID string) (string, error) {
	if strings.TrimSpace(fileID) == "" {
		return "", fmt.Errorf("TelegramAdapter: empty file_id")
	}
	q := url.Values{}
	q.Set("file_id", fileID)
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?%s", a.botToken, q.Encode())

	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("TelegramAdapter.GetFileURL: %w",
			fmt.Errorf("%s", sanitizeTelegramError(err, a.botToken)))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("TelegramAdapter.GetFileURL: status %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if !result.OK || result.Result.FilePath == "" {
		return "", fmt.Errorf("TelegramAdapter.GetFileURL: no file_path in response")
	}
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", a.botToken, result.Result.FilePath), nil
}

func (a *TelegramAdapter) DownloadFile(downloadURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TelegramAdapter.DownloadFile: %w",
			fmt.Errorf("%s", sanitizeTelegramError(err, a.botToken)))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TelegramAdapter.DownloadFile: status %d: %s", resp.StatusCode, truncateForLog(string(b), 200))
	}
	return io.ReadAll(resp.Body)
}

// postJSON is a small helper that POSTs a pre-marshalled JSON body and checks
// for a non-2xx status.
func (a *TelegramAdapter) postJSON(apiURL string, body []byte) error {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s", sanitizeTelegramError(err, a.botToken))
			if attempt < maxAttempts {
				time.Sleep(telegramRetryBackoff(attempt))
			}
			continue
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(rb), 200))
			if attempt < maxAttempts && shouldRetryTelegramRequest(nil, resp.StatusCode) {
				time.Sleep(telegramRetryBackoff(attempt))
				continue
			}
			return lastErr
		}
		return nil
	}
	return lastErr
}

// ─────────────────────────────────────────────────────────────────────────────
// WhatsAppAdapter — stub implementation
// ─────────────────────────────────────────────────────────────────────────────

// WhatsAppAdapter implements MessengerAdapter for the WhatsApp Business Cloud API.
// Replace the stub bodies with real HTTP calls to graph.facebook.com/v*/messages.
type WhatsAppAdapter struct {
	phoneNumberID string
	bearerToken   string
	httpClient    *http.Client
}

func NewWhatsAppAdapter(phoneNumberID, bearerToken string, httpClient *http.Client) *WhatsAppAdapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &WhatsAppAdapter{phoneNumberID: phoneNumberID, bearerToken: bearerToken, httpClient: httpClient}
}

func (a *WhatsAppAdapter) MessengerType() string { return "whatsapp" }

func (a *WhatsAppAdapter) SendText(chatID string, text string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("WhatsAppAdapter.SendText: empty chatID")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("WhatsAppAdapter.SendText: empty text")
	}
	if strings.TrimSpace(a.phoneNumberID) == "" {
		return fmt.Errorf("WhatsAppAdapter.SendText: phoneNumberID not configured")
	}
	if strings.TrimSpace(a.bearerToken) == "" {
		return fmt.Errorf("WhatsAppAdapter.SendText: bearer token not configured")
	}

	apiURL := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", strings.TrimSpace(a.phoneNumberID))
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
		if err := a.postJSONWithAuth(apiURL, payload); err != nil {
			return fmt.Errorf("WhatsAppAdapter.SendText: %w", err)
		}
	}
	return nil
}

func (a *WhatsAppAdapter) SendAudio(chatID string, audioData []byte, caption string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: empty chatID")
	}
	if len(audioData) == 0 {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: empty audioData")
	}
	if strings.TrimSpace(a.phoneNumberID) == "" {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: phoneNumberID not configured")
	}
	if strings.TrimSpace(a.bearerToken) == "" {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: bearer token not configured")
	}

	mediaID, err := a.uploadAudioMedia(audioData)
	if err != nil {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: upload media: %w", err)
	}

	apiURL := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/messages", strings.TrimSpace(a.phoneNumberID))
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                chatID,
		"type":              "audio",
		"audio": map[string]interface{}{
			"id": mediaID,
		},
	}
	if err := a.postJSONWithAuth(apiURL, payload); err != nil {
		return fmt.Errorf("WhatsAppAdapter.SendAudio: send audio: %w", err)
	}

	if strings.TrimSpace(caption) != "" {
		if err := a.SendText(chatID, caption); err != nil {
			return fmt.Errorf("WhatsAppAdapter.SendAudio: send caption: %w", err)
		}
	}
	return nil
}

func (a *WhatsAppAdapter) GetFileURL(fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", fmt.Errorf("WhatsAppAdapter.GetFileURL: empty fileID")
	}
	if strings.TrimSpace(a.bearerToken) == "" {
		return "", fmt.Errorf("WhatsAppAdapter.GetFileURL: bearer token not configured")
	}

	apiURL := fmt.Sprintf("https://graph.facebook.com/v21.0/%s", url.PathEscape(fileID))
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.bearerToken))

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("WhatsAppAdapter.GetFileURL: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("WhatsAppAdapter.GetFileURL: status %d: %s", resp.StatusCode, truncateForLog(string(body), 300))
	}

	var parsed struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.URL) == "" {
		return "", fmt.Errorf("WhatsAppAdapter.GetFileURL: response missing url")
	}

	return parsed.URL, nil
}

func (a *WhatsAppAdapter) DownloadFile(downloadURL string) ([]byte, error) {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: empty downloadURL")
	}
	if strings.TrimSpace(a.bearerToken) == "" {
		return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: bearer token not configured")
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.bearerToken))

		resp, err := a.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: request failed: %w", err)
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < maxAttempts {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: read failed: %w", readErr)
		}

		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(body), 300))
			if attempt < maxAttempts && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: %w", lastErr)
		}

		return body, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: %w", lastErr)
	}
	return nil, fmt.Errorf("WhatsAppAdapter.DownloadFile: unknown error")
}

func (a *WhatsAppAdapter) uploadAudioMedia(audioData []byte) (string, error) {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v21.0/%s/media", strings.TrimSpace(a.phoneNumberID))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("messaging_product", "whatsapp"); err != nil {
		return "", err
	}
	if err := mw.WriteField("type", "audio/mpeg"); err != nil {
		return "", err
	}
	fw, err := mw.CreateFormFile("file", "summary.mp3")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, bytes.NewReader(audioData)); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.bearerToken))
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(rb), 300))
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rb, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.ID) == "" {
		return "", fmt.Errorf("upload response missing media id")
	}

	return parsed.ID, nil
}

func (a *WhatsAppAdapter) postJSONWithAuth(apiURL string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.bearerToken))

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(string(rb), 300))
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SlackAdapter — stub implementation
// ─────────────────────────────────────────────────────────────────────────────

// SlackAdapter implements MessengerAdapter for the Slack Web API.
type SlackAdapter struct {
	botToken   string
	httpClient *http.Client
}

func NewSlackAdapter(botToken string, httpClient *http.Client) *SlackAdapter {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &SlackAdapter{botToken: botToken, httpClient: httpClient}
}

func (a *SlackAdapter) MessengerType() string { return "slack" }

func (a *SlackAdapter) SendText(chatID string, text string) error {
	// TODO: POST https://slack.com/api/chat.postMessage
	return fmt.Errorf("SlackAdapter.SendText: not yet implemented")
}

func (a *SlackAdapter) SendAudio(chatID string, audioData []byte, caption string) error {
	// TODO: POST https://slack.com/api/files.upload
	return fmt.Errorf("SlackAdapter.SendAudio: not yet implemented")
}

func (a *SlackAdapter) GetFileURL(fileID string) (string, error) {
	return fileID, nil // Slack file URLs come pre-formed
}

func (a *SlackAdapter) DownloadFile(downloadURL string) ([]byte, error) {
	// TODO: GET downloadURL with Bearer bot token
	return nil, fmt.Errorf("SlackAdapter.DownloadFile: not yet implemented")
}

// ─────────────────────────────────────────────────────────────────────────────
// Adapter registry on WebhookHandler
// ─────────────────────────────────────────────────────────────────────────────

// initMessengerAdapters wires up all available adapters from the current handler
// configuration.  Call this once from NewWebhookHandler.
func (h *WebhookHandler) initMessengerAdapters() {
	h.messengerAdapters = make(map[string]MessengerAdapter)

	if h.telegramBotToken != "" {
		h.messengerAdapters["telegram"] = NewTelegramAdapter(h.telegramBotToken, h.httpClient)
	}

	// WhatsApp / Slack adapters are registered here when their credentials are
	// added to the environment (no-op currently; stubs prevent nil panics).
	waPhoneID := os.Getenv("WHATSAPP_PHONE_NUMBER_ID")
	waToken := strings.TrimSpace(os.Getenv("WHATSAPP_ACCESS_TOKEN"))
	if waToken == "" {
		waToken = strings.TrimSpace(os.Getenv("WHATSAPP_BEARER_TOKEN"))
	}
	if waPhoneID != "" && waToken != "" {
		h.messengerAdapters["whatsapp"] = NewWhatsAppAdapter(waPhoneID, waToken, h.httpClient)
	}

	slackToken := os.Getenv("SLACK_BOT_TOKEN")
	if slackToken != "" {
		h.messengerAdapters["slack"] = NewSlackAdapter(slackToken, h.httpClient)
	}
}

// adapterFor returns the MessengerAdapter for the given platform, or nil if none
// is registered.  Callers should guard with a nil check.
func (h *WebhookHandler) adapterFor(messengerType string) MessengerAdapter {
	if h.messengerAdapters == nil {
		return nil
	}
	return h.messengerAdapters[strings.ToLower(messengerType)]
}

// SendMessengerText sends a text message via the adapter for the given platform.
// It falls back to the Telegram helper when messengerType == "telegram" and the
// chatID is numeric (preserving backward compatibility with existing callers).
func (h *WebhookHandler) SendMessengerText(messengerType, chatID, text string) error {
	adapter := h.adapterFor(messengerType)
	if adapter == nil {
		return fmt.Errorf("SendMessengerText: no adapter for messenger_type=%q", messengerType)
	}
	return adapter.SendText(chatID, text)
}

// SendMessengerAudio sends an audio file via the adapter for the given platform.
func (h *WebhookHandler) SendMessengerAudio(messengerType, chatID string, audio []byte, caption string) error {
	adapter := h.adapterFor(messengerType)
	if adapter == nil {
		return fmt.Errorf("SendMessengerAudio: no adapter for messenger_type=%q", messengerType)
	}
	return adapter.SendAudio(chatID, audio, caption)
}

// GetMessengerFileURL resolves a file ID to a download URL via the appropriate adapter.
func (h *WebhookHandler) GetMessengerFileURL(messengerType, fileID string) (string, error) {
	adapter := h.adapterFor(messengerType)
	if adapter == nil {
		return "", fmt.Errorf("GetMessengerFileURL: no adapter for messenger_type=%q", messengerType)
	}
	return adapter.GetFileURL(fileID)
}

// DownloadMessengerFile downloads raw bytes from a URL via the appropriate adapter.
func (h *WebhookHandler) DownloadMessengerFile(messengerType, downloadURL string) ([]byte, error) {
	adapter := h.adapterFor(messengerType)
	if adapter == nil {
		return nil, fmt.Errorf("DownloadMessengerFile: no adapter for messenger_type=%q", messengerType)
	}
	return adapter.DownloadFile(downloadURL)
}
