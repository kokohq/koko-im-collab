# Koko Application: Production Security Hardening Guide

## Overview
This document outlines the production-grade security hardening applied to Koko and provides endpoint-by-endpoint threat models, deployment checklist, and configuration guidance.

---

## 1. Security Headers & CORS Policy

### Security Headers Applied
All endpoints enforce the following HTTP security headers:

| Header | Value | Purpose |
|--------|-------|---------|
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` | HSTS: Force HTTPS for 1 year |
| `X-Content-Type-Options` | `nosniff` | Prevent MIME-type sniffing attacks |
| `X-Frame-Options` | `DENY` | Prevent clickjacking/frame injection |
| `X-XSS-Protection` | `1; mode=block` | Legacy browser XSS filter (deprecated but harmless) |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'; sandbox` | Strict CSP: reject all inline/external content |
| `Referrer-Policy` | `no-referrer` | Don't leak referrer to external sites |
| `Permissions-Policy` | `geolocation=(), microphone=(), camera=(), payment=()` | Disable dangerous APIs |

### CORS Configuration
- **Default**: CORS disabled (no `Access-Control-Allow-Origin` header unless origin is whitelisted)
- **Development**: Allows `localhost` and `127.0.0.1` for local testing
- **Production**: Requires explicit configuration via `KOKO_CORS_ORIGINS` environment variable

**Configuration Example:**
```bash
export KOKO_CORS_ORIGINS="https://app.example.com,https://admin.example.com"
```

**CORS Preflight Response (OPTIONS):**
```
Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS
Access-Control-Allow-Headers: Content-Type, Authorization, X-API-Key
Access-Control-Max-Age: 3600
Access-Control-Allow-Credentials: false
```

---

## 2. Request Body Size Limits

All endpoints enforce strict request body size limits to prevent resource exhaustion and DoS attacks:

| Endpoint | Limit | Rationale |
|----------|-------|-----------|
| `/webhook` (WhatsApp) | 1 MB | Webhook payloads typically < 256 KB; 1 MB provides safety margin |
| `/slack-webhook` | 1 MB | Slack event payloads typically < 500 KB |
| `/telegram-webhook` | 1 MB | Telegram updates typically < 100 KB |
| `/api/*` (Config API) | 256 KB | Admin/config operations should be small JSON documents |
| `/health` | 1 KB | Health check: reject oversized requests immediately |
| `/metrics/cache` | 1 KB | Metrics are read-only, reject any request body |

**How Enforcement Works:**
```go
r.Body = http.MaxBytesReader(w, r.Body, maxWebhookPayloadBytes)
body, err := io.ReadAll(r.Body)
// If body exceeds limit, http.MaxBytesReader returns HTTP 413 Payload Too Large
```

---

## 3. Webhook Endpoint Threat Model

### POST /webhook (WhatsApp)
**Threat Model:**
- **Input sources:** External (WhatsApp Cloud API, potentially spoofed/MITM)
- **Key threats:**
  1. **Signature spoofing** — Attacker forges valid X-Hub-Signature-256 header
  2. **Replay attack** — Attacker replays captured valid webhook request
  3. **Payload tampering** — Body modified after signature verification
  4. **Resource exhaustion** — Large payloads (uses 1 MB limit)
  5. **Application error** — Invalid JSON crashes parser

**Mitigations Applied:**
- ✅ **HMAC-SHA256 signature verification** — [webhook.go L757](webhook.go#L757)
  - Constant-time comparison prevents timing attacks
  - Signature checked before body processing
- ✅ **Request body size limit** — 1 MB max
- ✅ **JSON parsing validation** — Error handling on malformed JSON
- ✅ **Rate limiting** — No explicit rate limit yet (TODO: implement Redis/sliding-window for DoS)
- ⚠️ **Replay protection** — None (WhatsApp includes timestamp but not validated)

**Security Headers:** HSTS, nosniff, X-Frame-Options

**Authentication:** None required (signature-based trust)

**Recommended Deployment Config:**
```bash
export WHATSAPP_VERIFY_TOKEN="<strong-random-token>"
export WHATSAPP_ACCESS_TOKEN="<your-whatsapp-token>"    # Kept in secrets store
export WHATSAPP_APP_SECRET="<strong-app-secret>"         # Kept in secrets store
```

---

### POST /slack-webhook (Slack Events)
**Threat Model:**
- **Input sources:** Slack API (trusted, but verify signature)
- **Key threats:**
  1. **Signature spoofing** — Attacker forges X-Slack-Signature header
  2. **Replay attack** — Attacker replays captured request (5-minute window)
  3. **Timestamp manipulation** — Old/future timestamps accepted
  4. **Resource exhaustion** — Large event payloads
  5. **Malicious content** — Script injection via message text

**Mitigations Applied:**
- ✅ **HMAC-SHA256 signature + timestamp verification** — [webhook.go L787](webhook.go#L787)
  - Signature combines timestamp + request body
  - Timestamp freshness check (±5-minute window) prevents replay — [webhook.go L797](webhook.go#L797)
  - Constant-time comparison
- ✅ **Request body size limit** — 1 MB max
- ✅ **URL verification handling** — [webhook.go L910](webhook.go#L910)
- ✅ **Content validation** — Message text sanitized by policy engine

**Security Headers:** HSTS, nosniff, X-Frame-Options, CSP

**Authentication:** None required (signature-based trust)

**Recommended Deployment Config:**
```bash
export SLACK_SIGNING_SECRET="<slack-signing-secret>"    # Kept in secrets store
```

---

### POST /telegram-webhook (Telegram Bot API)
**Threat Model:**
- **Input sources:** Telegram Bot API (HTTPS, trusted)
- **Key threats:**
  1. **Secret token spoofing** — Attacker forges X-Telegram-Bot-Api-Secret-Token
  2. **Webhook hijacking** — Attacker changes webhook URL to their server
  3. **Large media files** — Audio/video exceed memory/processing limits
  4. **Message injection** — Malicious `koko:*` commands
  5. **Rate limiting bypass** — Flood webhook with updates

**Mitigations Applied:**
- ✅ **Secret token verification** — [webhook.go L827](webhook.go#L827)
  - Constant-time comparison prevents timing attacks
- ✅ **Request body size limit** — 1 MB max
- ✅ **Media size validation** — Offline file download respects limits
- ✅ **Command parsing validation** — Recognized commands only
- ✅ **Policy enforcement** — Message content checked against moderation rules
- ⚠️ **Rate limiting** — None yet (TODO: per-user/per-group rate limits)

**Security Headers:** HSTS, nosniff, X-Frame-Options

**Authentication:** Secret token only (no signature)

**Recommended Deployment Config:**
```bash
export TELEGRAM_SECRET_TOKEN="<strong-random-secret>"      # Set when registering webhook
export TELEGRAM_BOT_TOKEN="<your-bot-token>"              # Kept in secrets store
```

---

## 4. Configuration API Endpoint Threat Model

### GET /api/plan-tiers, POST /api/plan-tiers
### GET /api/accounts, POST /api/accounts
### GET /api/groups, POST /api/groups
### Other /api/* endpoints

**Threat Model:**
- **Input sources:** Internal admin dashboard, GUI clients (trusted network only)
- **Key threats:**
  1. **Unauthorized data access** — Non-admin user queries account data
  2. **Data modification** — Attacker modifies plan tiers, account settings
  3. **SQL injection** — Malicious table/field names in query params (unlikely due to parameterized queries)
  4. **Privilege escalation** — User updates their own plan tier to premium
  5. **Data exfiltration** — Dump all accounts/users via `/api/accounts`

**Mitigations Applied:**
- ✅ **Authentication required** — KOKO_ADMIN_API_TOKEN
  - Supports `Authorization: Bearer <token>` or `X-API-Key: <token>`
  - Constant-time comparison prevents timing attacks
  - Gate before any database operations — [config_api.go L13](config_api.go#L13)
- ✅ **Request body size limit** — 256 KB max (prevents large payloads)
- ✅ **Content-Type validation** — JSON bodies only
- ✅ **Parameterized SQL queries** — All database calls use prepared statements (pq driver handles escaping)
- ⚠️ **Rate limiting** — None yet (TODO: implement per-token rate limits)
- ⚠️ **Audit logging** — Basic request logging to stdout (TODO: integrate structured logging)

**Security Headers:** HSTS, CSP (strict), nosniff, X-Frame-Options

**CORS:** Allowed only if origin in `KOKO_CORS_ORIGINS`

**Authentication:** Required (KOKO_ADMIN_API_TOKEN)

**Recommended Deployment Config:**
```bash
export KOKO_ADMIN_API_TOKEN="<64-char-random-token>"   # Use `openssl rand -hex 32`
export KOKO_CORS_ORIGINS="https://admin.example.com"
```

**Example Usage:**
```bash
# Query accounts
curl -H "Authorization: Bearer $KOKO_ADMIN_API_TOKEN" \
     https://api.example.com/api/accounts

# Create account
curl -X POST \
     -H "Authorization: Bearer $KOKO_ADMIN_API_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"account_id":"acct123"}' \
     https://api.example.com/api/accounts
```

---

## 5. Monitoring & Metrics Endpoint

### GET /metrics/cache
**Threat Model:**
- **Input sources:** Internal monitoring/observability systems
- **Key threats:**
  1. **Metrics data leakage** — Attacker queries cache stats to infer user data patterns
  2. **DoS via metrics** — Attacker floods metrics endpoint (very low cost)

**Mitigations Applied:**
- ✅ **Authentication optional** — KOKO_METRICS_TOKEN
  - If not configured, metrics are public (for Prometheus scraping)
  - If configured, only bearer token or X-API-Key allowed
- ✅ **Read-only** — No state modifications, only aggregated stats
- ✅ **Request body size limit** — 1 KB max
- ✅ **Method restriction** — GET only

**Security Headers:** HSTS (for integrity), no other sensitive headers needed

**Authentication:** Optional (KOKO_METRICS_TOKEN)

**Recommended Deployment Config:**
```bash
export KOKO_METRICS_TOKEN="<32-char-random-token>"     # Use for internal Prometheus only
```

---

## 6. Health Check Endpoint

### GET /health
**Threat Model:**
- **Input sources:** Load balancers, monitoring systems
- **Key threats:**
  1. **Service enumeration** — Attacker probes to detect running service
  2. **DoS** — Minimal, as /health is extremely fast

**Mitigations Applied:**
- ✅ **No authentication** — Intentionally public for LB health checks
- ✅ **Minimal response** — Single "OK" string, no sensitive data
- ✅ **Request body limit** — 1 KB max
- ✅ **Method restriction** — GET only
- ✅ **Very fast response** — No database queries

**Security Headers:** HSTS only (for integrity)

---

## 7. HTTP Server Hardening

### Timeouts (Slowloris / Resource Exhaustion Protection)
```go
ReadHeaderTimeout: 10 * time.Second    // Time to read request headers
ReadTimeout:       30 * time.Second    // Time to read complete request body
WriteTimeout:      30 * time.Second    // Time to write response
IdleTimeout:       120 * time.Second   // Keep-alive idle timeout
```

**Rationale:**
- **ReadHeaderTimeout: 10s** — Prevents slowloris header flooding
- **ReadTimeout: 30s** — Generous timeout for large webhook payloads
- **WriteTimeout: 30s** — Prevent slow clients from hogging connections
- **IdleTimeout: 120s** — Close long-idle keep-alive connections

### HTTP Method Validation
All endpoints explicitly validate allowed methods:
```go
if r.Method != http.MethodGet { ... }    // /health only allows GET
if r.Method != http.MethodPost { ... }   // /webhook allows GET (verify) and POST (messages)
```

---

## 8. Deployment Environment Variables (Checklist)

### Required (Production)
```bash
# Database
POSTGRES_URL="postgresql://user:pass@db.internal:5432/koko?sslmode=require"

# WhatsApp
WHATSAPP_VERIFY_TOKEN="<strong-random>"
WHATSAPP_ACCESS_TOKEN="<meta-issued-token>"
WHATSAPP_APP_SECRET="<meta-issued-secret>"

# Slack
SLACK_SIGNING_SECRET="<slack-issued-secret>"

# Telegram
TELEGRAM_BOT_TOKEN="<telegram-bot-token>"
TELEGRAM_SECRET_TOKEN="<strong-random>"

# Google Cloud
GOOGLE_CLOUD_PROJECT="<gcp-project-id>"

# Admin & Metrics Auth
KOKO_ADMIN_API_TOKEN="<64-char-random>"       # Use: openssl rand -hex 32
KOKO_METRICS_TOKEN="<32-char-random>"         # Use: openssl rand -hex 16
```

### Recommended (Production)
```bash
# CORS
KOKO_CORS_ORIGINS="https://admin.example.com,https://app.example.com"

# Feature Mode
KOKO_FEATURE_MODE="all"    # or: industrial, electronics, revenue, consumer

# Ports
PORT="8080"    # Internal port (use with reverse proxy)
```

### Not Set (Defaults Applied)
```bash
# If unset, these use defaults:
VERTEX_AI_LOCATION="us-west2"      # Default fallback
VERTEX_AI_MODEL="gemini-2.0-flash"
```

---

## 9. Deployment Security Checklist

### Before Deploying to Production
- [ ] **Secrets Management**
  - [ ] Rotate WHATSAPP_APP_SECRET, SLACK_SIGNING_SECRET, TELEGRAM_BOT_TOKEN
  - [ ] Generate new KOKO_ADMIN_API_TOKEN using `openssl rand -hex 32`
  - [ ] Generate new KOKO_METRICS_TOKEN using `openssl rand -hex 16`
  - [ ] Store all secrets in secure vault (HashiCorp Vault, AWS Secrets Manager, etc.)
  - [ ] Ensure secrets are never logged or committed to Git

- [ ] **Database Security**
  - [ ] Use SSL/TLS for PostgreSQL connection (`sslmode=require` in POSTGRES_URL)
  - [ ] Enable database encryption at rest
  - [ ] Restrict database network access to application servers only
  - [ ] Set strong password for database user
  - [ ] Enable audit logging on sensitive tables

- [ ] **Transport Security**
  - [ ] Deploy reverse proxy (nginx/Envoy) with TLS termination
  - [ ] Use valid SSL/TLS certificate (not self-signed)
  - [ ] Enforce HTTPS only (`Strict-Transport-Security` header already set)
  - [ ] Disable TLS 1.0, 1.1 (require TLS 1.2+)

- [ ] **Network Security**
  - [ ] Restrict webhook endpoints to known IP ranges (WhatsApp, Slack, Telegram)
  - [ ] Run behind reverse proxy / load balancer
  - [ ] Enable WAF (Web Application Firewall) if available
  - [ ] Disable public access to /api/* and /metrics/cache (firewall rules)
  - [ ] Ensure cloud networking allows only necessary traffic

- [ ] **Container Security**
  - [ ] Use pinned Alpine image (e.g., `alpine:3.22` not `alpine:latest`)
  - [ ] Run container as non-root user
  - [ ] Enable read-only filesystem where possible
  - [ ] Drop unnecessary Linux capabilities
  - [ ] Scan container image for vulnerabilities (trivy, Snyk)

- [ ] **Monitoring & Alerting**
  - [ ] Enable structured logging (JSON format for log aggregation)
  - [ ] Set up log aggregation (ELK, Splunk, CloudWatch)
  - [ ] Alert on authentication failures (400+ /api calls)
  - [ ] Alert on payload size limit violations (HTTP 413)
  - [ ] Alert on error rate spikes
  - [ ] Monitor webhook latency (target: < 200ms)

- [ ] **Rate Limiting (TODO: Implement)**
  - [ ] Add per-webhook rate limits (e.g., WhatsApp: 1000 req/min per phone number)
  - [ ] Add per-admin-token rate limits (e.g., 100 req/min for config API)
  - [ ] Consider Redis for distributed rate limiting

- [ ] **Compliance**
  - [ ] Ensure GDPR/data privacy compliance (if EU users)
  - [ ] Implement data retention policies (delete old webhook logs)
  - [ ] Document data processing (privacy policy)
  - [ ] Enable audit logging for `/api/*` (admin operations)

---

## 10. Threat Model Summary Table

| Endpoint | Auth | Auth Method | Body Limit | Threats | Mitigations |
|----------|------|-------------|-----------|---------|------------|
| POST /webhook | No | Signature (HMAC-SHA256) | 1 MB | Spoofing, tampering, resource exhaustion | Signature verification, size limit |
| POST /slack-webhook | No | Signature + timestamp | 1 MB | Replay, spoofing | Signature + 5-min freshness check |
| POST /telegram-webhook | No | Secret token | 1 MB | Token spoofing, large uploads | Token comparison, size limit |
| GET /health | No | None | 1 KB | Service enumeration | Minimal response, GET-only |
| GET /metrics/cache | Optional | Token (Bearer/X-API-Key) | 1 KB | Data leakage, DoS | Optional auth, GET-only |
| GET/POST /api/* | Required | Token (Bearer/X-API-Key) | 256 KB | Unauthorized access, privilege escalation, SQL injection | Token auth, parameterized queries, size limit |

---

## 11. Incident Response

### Suspected Webhook Spoofing
```bash
# Check signature verification logs
tail -f /var/log/koko/app.log | grep SIGNATURE_VERIFICATION_FAILED

# Disable webhook temporarily
export WHATSAPP_APP_SECRET=""  # Forces all requests to be rejected

# Investigate: Did signature verification ever fail legitimately?
# If yes, verify WhatsApp sent the secret correctly
```

### Suspected Replay Attack
- Already mitigated for Slack (5-min freshness check)
- Implement message deduplication by `message_id` in future

### Suspected Rate Limiting Attack
```bash
# Check inbound request rate
tail -f /var/log/koko/app.log | grep "^TELEGRAM_USER_MESSAGE" | wc -l

# Temporarily restrict webhook IPs at firewall level
# Then implement Redis-based rate limiting
```

### Data Breach (JWT/Admin Token Leaked)
```bash
# Immediately rotate token
export KOKO_ADMIN_API_TOKEN="<new-strong-token>"

# Audit /api/* for suspicious activity
grep "ADMIN_API" /var/log/koko/app.log | grep -E "POST|PATCH|DELETE"

# Check account modifications around breach time window
```

---

## 12. References

- [OWASP Top 10 API Security](https://owasp.org/www-project-api-security/)
- [WhatsApp Cloud API Security](https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks#webhook-security)
- [Slack API Security](https://api.slack.com/authentication/verifying-requests-from-slack)
- [HTTP Security Headers](https://cheatsheetseries.owasp.org/cheatsheets/HTTP_Headers_Cheat_Sheet.html)
- [NIST Cybersecurity Framework](https://www.nist.gov/cyberframework)

---

**Last Updated:** March 29, 2026  
**Version:** 1.0 (Production Ready)
