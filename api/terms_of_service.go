package main

import "net/http"

const kokoTermsOfServiceHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Koko Terms of Service</title>
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
      <h1>Koko Terms of Service</h1>
      <p class="muted"><strong>Effective date:</strong> April 5, 2026</p>
      <p class="muted"><strong>Service:</strong> Koko app (messaging intelligence and automation assistant)</p>

      <h2>1. Acceptance of Terms</h2>
      <p>
        By accessing or using Koko, you agree to these Terms of Service.
        If you use Koko on behalf of an organization, you represent that you are
        authorized to bind that organization to these terms.
      </p>

      <h2>2. Service Description</h2>
      <p>
        Koko provides messaging workflow features such as ingestion, summarization,
        analysis, and operational actions across integrated platforms including the
        WhatsApp Business Platform.
      </p>

      <h2>3. Meta / WhatsApp Platform Use</h2>
      <ul>
        <li>You must comply with Meta and WhatsApp Business Platform terms, policies, and applicable law.</li>
        <li>You may use Koko only for authorized business messaging use cases.</li>
        <li>You must not use Koko to send spam, unlawful content, or abusive communications.</li>
      </ul>

      <h2>4. Customer Responsibilities</h2>
      <ul>
        <li>You are responsible for your account, configuration, and all activity under your credentials.</li>
        <li>You are responsible for obtaining any required user notices or consents for processing message data.</li>
        <li>You must keep your tokens, secrets, and webhook settings secure.</li>
      </ul>

      <h2>5. Data Usage and Privacy</h2>
      <p>
        Our handling of personal data is described in the Privacy Policy available at
        <a href="/privacy-policy">/privacy-policy</a>.
      </p>

      <h2>6. Security</h2>
      <ul>
        <li>Koko uses encryption in transit and at rest and applies access controls.</li>
        <li>Webhook integrity checks and optional mTLS controls are supported for WhatsApp integrations.</li>
        <li>You acknowledge that no system is entirely risk-free and agree to use reasonable security practices.</li>
      </ul>

      <h2>7. Prohibited Use</h2>
      <p>You must not use Koko to:</p>
      <ul>
        <li>Violate laws, regulations, third-party rights, or platform policies</li>
        <li>Transmit malware, harmful code, or deceptive content</li>
        <li>Attempt unauthorized access, reverse engineering, or abuse of service limits</li>
      </ul>

      <h2>8. Service Availability and Changes</h2>
      <p>
        We may modify, suspend, or discontinue features to maintain security,
        compliance, or performance. We may update these terms periodically.
      </p>

      <h2>9. Intellectual Property</h2>
      <p>
        Koko and related software, branding, and documentation are protected by
        applicable intellectual property laws. Except as expressly granted,
        no rights are transferred.
      </p>

      <h2>10. Disclaimers</h2>
      <p>
        Koko is provided on an "as is" and "as available" basis. To the fullest
        extent permitted by law, we disclaim all warranties, express or implied,
        including fitness for a particular purpose and non-infringement.
      </p>

      <h2>11. Limitation of Liability</h2>
      <p>
        To the maximum extent permitted by law, Koko and its operators are not
        liable for indirect, incidental, special, consequential, exemplary,
        or punitive damages, or for loss of data, revenue, or profits.
      </p>

      <h2>12. Termination</h2>
      <p>
        We may suspend or terminate access for violations of these terms,
        platform policies, or legal requirements.
      </p>

      <h2>13. Governing Law</h2>
      <p>
        These terms are governed by applicable laws in the operating jurisdiction,
        unless otherwise required by mandatory law.
      </p>

      <h2>14. Contact</h2>
      <p>
        Legal and compliance inquiries: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a><br />
        Privacy inquiries: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a><br />
        Support: <a href="mailto:harishankar@kokohq.com">harishankar@kokohq.com</a>
      </p>

      <h2>15. Updates to These Terms</h2>
      <p>
        Updated terms will be posted at <code>/terms-of-service</code> with a revised effective date.
      </p>
    </div>
  </div>
</body>
</html>
`

func handleTermsOfService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(kokoTermsOfServiceHTML))
}
