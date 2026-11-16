package main

import (
	"net/http"
	"strings"
)

const kokoPrivacyPolicyHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Koko Privacy Policy</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Arial, sans-serif; margin: 0; background: #f6f8fb; color: #101828; }
    .wrap { max-width: 900px; margin: 0 auto; padding: 28px 20px 56px; }
    .card { background: #ffffff; border: 1px solid #e4e7ec; border-radius: 12px; padding: 28px; box-shadow: 0 8px 24px rgba(16,24,40,0.05); }
    h1 { margin: 0 0 8px; font-size: 30px; }
    h2 { margin: 28px 0 10px; font-size: 20px; }
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
      <h1>Koko Privacy Policy</h1>
      <p class="muted"><strong>Effective date:</strong> April 5, 2026</p>
      <p class="muted"><strong>Service:</strong> Koko app (messaging intelligence and automation assistant)</p>

      <h2>1. Scope</h2>
      <p>
        This Privacy Policy explains how Koko collects, uses, stores, and protects data received through
        messaging integrations, including the WhatsApp Business Platform webhook integration provided via Meta.
      </p>

      <h2>2. Data We Receive From WhatsApp Webhooks</h2>
      <p>When your WhatsApp Business webhook is connected, Koko may receive and process:</p>
      <ul>
        <li>Message metadata (message ID, chat ID, timestamps, sender identifiers)</li>
        <li>Message content (text and, where enabled, supported media metadata)</li>
        <li>Webhook delivery metadata required for reliability and audit logs</li>
      </ul>

      <h2>3. How We Use Webhook Data</h2>
      <p>Koko uses webhook data only for legitimate business functions requested by customers, including:</p>
      <ul>
        <li>Message processing, summarization, analysis, and workflow actions initiated by users</li>
        <li>Operational reliability, troubleshooting, security monitoring, and abuse prevention</li>
        <li>Maintaining account, group, and user access controls</li>
      </ul>
      <p>
        Koko does <strong>not</strong> sell personal data and does not use WhatsApp webhook data for unrelated advertising purposes.
      </p>

      <h2>4. Data Sharing</h2>
      <p>We share data only when necessary to provide the service, for example with:</p>
      <ul>
        <li>Infrastructure providers (hosting, databases, and monitoring)</li>
        <li>Sub-processors required to execute customer-requested AI features</li>
        <li>Authorities where required by applicable law</li>
      </ul>

      <h2>5. Data Retention and Deletion</h2>
      <ul>
        <li>Source messages may be deleted after summarization or according to configured retention policies.</li>
        <li>Summary and audit records may be retained for operational, billing, and compliance requirements.</li>
        <li>Customers can request deletion of account-linked data, subject to legal obligations.</li>
      </ul>

      <h2>6. Security Controls</h2>
      <ul>
        <li>HTTPS/TLS in transit and encrypted storage at rest</li>
        <li>Access controls and authentication for API operations</li>
        <li>Webhook verification controls, including optional mTLS enforcement for WhatsApp webhooks</li>
      </ul>

      <h2>7. Meta and WhatsApp Platform Compliance</h2>
      <p>
        Koko processes WhatsApp Business Platform data in accordance with applicable Meta platform requirements,
        including restrictions on use, sharing, and protection of messaging data.
      </p>

      <h2>8. User Rights and Requests</h2>
      <p>
        Depending on applicable law, users may have rights to access, correct, export, or delete their personal data.
        To submit a request, contact: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a>
      </p>

      <h2>9. Contact</h2>
      <p>
        Privacy and data protection inquiries: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a><br />
        Support: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a>
      </p>

      <h2>10. Updates</h2>
      <p>
        We may update this policy from time to time. Updated versions will be published at
        <code>/privacy-policy</code> with a revised effective date.
      </p>
    </div>
  </div>
</body>
</html>
`

func handlePrivacyPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(kokoPrivacyPolicyHTML))
}

func handlePrivacyPolicyText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	plain := strings.NewReplacer("<br />", "\n", "<br>", "\n").Replace(kokoPrivacyPolicyHTML)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(plain))
}
