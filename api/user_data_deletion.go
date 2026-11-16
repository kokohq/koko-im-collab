package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const kokoUserDataDeletionHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Koko User Data Deletion</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Arial, sans-serif; margin: 0; background: #f6f8fb; color: #101828; }
    .wrap { max-width: 900px; margin: 0 auto; padding: 28px 20px 56px; }
    .card { background: #ffffff; border: 1px solid #e4e7ec; border-radius: 12px; padding: 28px; box-shadow: 0 8px 24px rgba(16,24,40,0.05); }
    h1 { margin: 0 0 8px; font-size: 30px; }
    h2 { margin: 26px 0 10px; font-size: 20px; }
    p, li { line-height: 1.6; }
    .muted { color: #475467; }
    code { background: #f2f4f7; padding: 2px 6px; border-radius: 6px; }
    a { color: #175cd3; text-decoration: none; }
    a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="card">
      <h1>Koko User Data Deletion Instructions</h1>
      <p class="muted"><strong>Effective date:</strong> April 5, 2026</p>

      <p>
        If you want your data deleted from Koko, you can request deletion using one of the methods below.
        These instructions are provided for Meta/Facebook developer compliance for apps that integrate
        with WhatsApp webhooks.
      </p>

      <h2>1. Email Request</h2>
      <p>Send your request to: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a></p>
      <p>Include the following details for faster processing:</p>
      <ul>
        <li>Your full name</li>
        <li>Your WhatsApp phone number used with the connected business app</li>
        <li>Organization/account name (if applicable)</li>
        <li>Request type: full account deletion or specific data deletion</li>
      </ul>

      <h2>2. In-App / Admin Request</h2>
      <p>
        Workspace administrators may request deletion of account-linked user data by contacting
        support at <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a>.
      </p>

      <h2>3. Processing Timeline</h2>
      <ul>
        <li>Request acknowledgement: typically within 3 business days</li>
        <li>Deletion completion target: within 30 days, subject to legal/contractual retention obligations</li>
      </ul>

      <h2>4. What Gets Deleted</h2>
      <ul>
        <li>Message content and associated records tied to the requested identity/account (where applicable)</li>
        <li>User profile mappings and related integration links (subject to verification)</li>
      </ul>

      <h2>5. What May Be Retained</h2>
      <p>
        Koko may retain limited records where required for legal compliance, fraud prevention,
        security investigation, billing disputes, or enforcement of agreements.
      </p>

      <h2>6. Verification</h2>
      <p>
        To protect user privacy, we may request additional information to verify identity
        before executing deletion requests.
      </p>

      <h2>7. Related Policy</h2>
      <p>
        See our Privacy Policy: <a href="/privacy-policy">/privacy-policy</a>
      </p>
    </div>
  </div>
</body>
</html>
`

type deletionStatusResponse struct {
	URL          string `json:"url"`
	Confirmation string `json:"confirmation_code"`
}

func handleUserDataDeletionInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(kokoUserDataDeletionHTML))
}

func handleMetaDataDeletionCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Meta may send signed_request for automated deletion callbacks.
	// We return a deterministic confirmation code and a status URL.
	signedRequest := strings.TrimSpace(r.FormValue("signed_request"))
	seed := signedRequest + "|" + time.Now().UTC().Format("200601021504")
	sum := sha256.Sum256([]byte(seed))
	code := strings.ToUpper(hex.EncodeToString(sum[:]))[:16]

	resp := deletionStatusResponse{
		URL:          "https://your-service.example.com/user-data-deletion",
		Confirmation: code,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
