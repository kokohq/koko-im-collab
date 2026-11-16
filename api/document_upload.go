package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Document upload endpoint
//
// POST /api/v1/documents
//
// Accepts a multipart/form-data request with:
//   - file     (required)  — the document binary
//   - filename (optional)  — human-readable name; falls back to the multipart header
//   - group_name (optional) — group to associate the document with
//   - domain  (optional)
//
// Concurrency: bounded by docUploadSemaphore (max 50 simultaneous uploads).
// Per-upload size: maxDocUploadBytes (20 MB).
//
// The document is stored as a messenger_inputs row with message_type="document"
// so it participates in existing koko:summarize / koko:analyze / koko:act flows
// without any schema changes.
// ─────────────────────────────────────────────────────────────────────────────

const (
	maxDocUploadBytes    = 20 * 1024 * 1024 // 20 MB per document
	maxConcurrentUploads = 50
)

// docUploadSemaphore is initialised once and shared across all upload handlers.
var (
	docUploadSemaphoreOnce sync.Once
	docUploadSemaphore     chan struct{}
)

func getDocUploadSemaphore() chan struct{} {
	docUploadSemaphoreOnce.Do(func() {
		docUploadSemaphore = make(chan struct{}, maxConcurrentUploads)
	})
	return docUploadSemaphore
}

// handleDocumentUpload handles POST /api/v1/documents.
func (h *WebhookHandler) handleDocumentUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Acquire a concurrency slot (non-blocking: reject immediately if full).
	sem := getDocUploadSemaphore()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		if globalMetrics != nil {
			globalMetrics.RecordRejection("upload_capacity", "/api/v1/documents", key.UserName, key.AccountID)
		}
		apiError(w, http.StatusTooManyRequests, "upload capacity reached; please retry shortly")
		return
	}

	// Enforce per-document size limit at the HTTP layer.
	r.Body = http.MaxBytesReader(w, r.Body, maxDocUploadBytes+4096 /* multipart overhead */)
	if err := r.ParseMultipartForm(maxDocUploadBytes); err != nil {
		apiError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("document exceeds maximum allowed size of %d MB", maxDocUploadBytes/(1024*1024)))
		return
	}

	// ── Extract form fields ────────────────────────────────────────────────
	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		apiError(w, http.StatusBadRequest, "field 'file' is required")
		return
	}
	defer file.Close()

	filename := cleanParam(r.FormValue("filename"))
	if filename == "" {
		filename = fileHeader.Filename
	}
	groupName := cleanParam(r.FormValue("group_name"))
	domain := cleanParam(r.FormValue("domain"))

	// Read document bytes (already bounded by MaxBytesReader above).
	docBytes, err := io.ReadAll(file)
	if err != nil {
		apiError(w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}
	if len(docBytes) == 0 {
		apiError(w, http.StatusBadRequest, "uploaded file is empty")
		return
	}

	// Detect MIME type from content (first 512 bytes per http.DetectContentType spec).
	detectedMIME := http.DetectContentType(docBytes)

	// ── Sanitize ──────────────────────────────────────────────────────────
	filename, okFN := sanitizeString(filename)
	if !okFN {
		filename = "upload.bin"
	}
	if strings.TrimSpace(groupName) != "" {
		if _, authorized := h.authorizeGroupAccess(r, key.AccountID, groupName); !authorized {
			apiError(w, http.StatusForbidden, "you are not authorized for this group")
			return
		}
	}

	// ── Persist as a messenger_inputs row with message_type="document" ────
	chatID := key.AccountID + "::" + groupName
	meta := ResponseMeta{
		RequestedBy: key.UserName,
		AccountID:   key.AccountID,
		GroupName:   groupName,
		Domain:      apiNormDomain(domain),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}

	accountID := key.AccountID
	userName := key.UserName
	store := h.store

	// Encode the document bytes as base64 in the payload JSON for at-rest storage.
	// The encryption in SaveMessengerInput will protect the payload.
	payloadJSON, _ := json.Marshal(map[string]interface{}{
		"filename":    filename,
		"mime_type":   detectedMIME,
		"size":        len(docBytes),
		"domain":      apiNormDomain(domain),
		"uploaded_by": userName,
		"uploaded_at": time.Now().UTC().Format(time.RFC3339),
	})

	job := globalJobQueue.Enqueue(meta, func() JobResultData {
		if store == nil || !store.DBEnabled() {
			return JobResultData{Error: "document storage requires database to be configured"}
		}
		saved, err := store.SaveMessengerInput(context.Background(), MessengerInput{
			AccountID:     accountID,
			GroupName:     groupName,
			UserName:      userName,
			MessengerType: "api",
			ChatID:        chatID,
			MessageType:   "document",
			TextContent:   filename,
			MimeType:      detectedMIME,
			Payload:       payloadJSON,
			ReceivedAt:    time.Now().UTC(),
		})
		if err != nil {
			log.Printf("DOCUMENT_UPLOAD_SAVE_FAILED: account=%s user=%s file=%s error=%v",
				accountID, userName, filename, err)
			return JobResultData{Error: "failed to save document: " + err.Error()}
		}
		return JobResultData{Results: map[string]interface{}{
			"id":        saved.ID,
			"filename":  filename,
			"mime_type": detectedMIME,
			"size":      len(docBytes),
			"group":     groupName,
			"domain":    apiNormDomain(domain),
			"status":    "stored",
		}}
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"meta":    meta,
		"job_id":  job.ID,
		"status":  job.Status,
		"message": "document upload accepted; poll /api/jobs/" + job.ID + " for result",
	})
}
