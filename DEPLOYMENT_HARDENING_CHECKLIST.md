# Production Deployment Hardening Checklist

**Version:** 1.0 (March 29, 2026)

---

## Hardening Changes Implemented

### 1. Security Headers (✅ Applied)
**Location:** [api/main.go](api/main.go#L155)

All responses include production-grade security headers:
- `Strict-Transport-Security: max-age=31536000` — Force HTTPS (1 year)
- `X-Content-Type-Options: nosniff` — Prevent MIME-sniffing
- `X-Frame-Options: DENY` — Prevent clickjacking
- `CSP: default-src 'none'; sandbox` — Strict content security policy
- Additional headers: X-XSS-Protection, Referrer-Policy, Permissions-Policy

**Applied to:** All API endpoints, health checks, metrics

---

### 2. CORS Policy (✅ Applied)
**Location:** [api/main.go](api/main.go#L167) & [api/main.go](api/main.go#L200)

- **Default:** CORS disabled (no preflight headers unless origin whitelisted)
- **Localhost support:** Auto-allows `localhost` and `127.0.0.1` for development
- **Production:** Requires explicit `KOKO_CORS_ORIGINS` configuration

**Configuration:**
```bash
export KOKO_CORS_ORIGINS="https://admin.example.com,https://app.example.com"
```

---

### 3. Request Body Size Limits (✅ Applied)
**Location:** [api/webhook.go](api/webhook.go#L40) & [api/main.go](api/main.go#L47)

| Endpoint | Limit | Applied |
|----------|-------|---------|
| `/webhook` (WhatsApp) | 1 MB | ✅ [main.go L47](api/main.go#L47) |
| `/slack-webhook` | 1 MB | ✅ [main.go L62](api/main.go#L62) |
| `/telegram-webhook` | 1 MB | ✅ [main.go L74](api/main.go#L74) |
| `/api/*` (Config) | 256 KB | ✅ [main.go L121](api/main.go#L121) |
| `/health` | 1 KB | ✅ [main.go L85](api/main.go#L85) |
| `/metrics/cache` | 1 KB | ✅ [main.go L97](api/main.go#L97) |

**Enforcement:** Uses `http.MaxBytesReader` to automatically reject oversized payloads with HTTP 413

**Error Logging:** Oversized payloads logged as `WEBHOOK_PAYLOAD_TOO_LARGE: messenger={whatsapp|slack|telegram}`

---

### 4. HTTP Server Timeouts (✅ Applied)
**Location:** [api/main.go](api/main.go#L104)

```go
ReadHeaderTimeout: 10 * time.Second    // Slowloris protection
ReadTimeout:       30 * time.Second    // Complete request body
WriteTimeout:      30 * time.Second    // Response writing
IdleTimeout:       120 * time.Second   // Keep-alive timeout
```

**Protection Against:** Slowloris attacks, resource exhaustion, hang clients

---

### 5. Webhook Security Validation (✅ Applied)
**Location:** [api/webhook.go](api/webhook.go#L706), [L827](api/webhook.go#L827), [L797](api/webhook.go#L797)

#### WhatsApp Signature Verification
- ✅ HMAC-SHA256 signature check
- ✅ Constant-time comparison (prevents timing attacks)
- ✅ Body must match signature exactly

#### Slack Signature + Replay Protection
- ✅ HMAC-SHA256 signature with timestamp
- ✅ **NEW:** 5-minute freshness window (replay protection)
- ✅ Constant-time comparison

#### Telegram Secret Token
- ✅ Secret token header validation
- ✅ Constant-time comparison

---

### 6. Admin API Authentication (✅ Applied)
**Location:** [api/config_api.go](api/config_api.go#L13) & [L268](api/config_api.go#L268)

- ✅ Required token authentication
- ✅ Supports `Authorization: Bearer <token>` or `X-API-Key: <token>`
- ✅ Constant-time comparison (no timing attacks)
- ✅ Applied before any database operations

**Configuration:**
```bash
export KOKO_ADMIN_API_TOKEN="<64-char-random-token>"   # Use: openssl rand -hex 32
```

---

### 7. Metrics Endpoint Authentication (✅ Applied)
**Location:** [api/main.go](api/main.go#L112)

- ✅ Optional token authentication
- ✅ If `KOKO_METRICS_TOKEN` is unset, metrics are public (for Prometheus)
- ✅ If set, requires Bearer token or X-API-Key
- ✅ Constant-time comparison

**Configuration:**
```bash
export KOKO_METRICS_TOKEN="<32-char-random-token>"   # Use: openssl rand -hex 16
```

---

### 8. Docker Image Hardening (✅ Applied)
**Location:** [Dockerfile](Dockerfile#L2)

**Before:**
```dockerfile
FROM golang:1.21-alpine AS builder
FROM alpine:latest
```

**After (Pinned versions):**
```dockerfile
FROM golang:1.25-alpine3.22 AS builder
FROM alpine:3.22
```

**Benefits:**
- No surprise base image updates
- Pick up security patches when explicitly rebuilt
- Deterministic builds across environments

---

### 9. Error Handling for Oversized Payloads (✅ Applied)
**Location:** [api/webhook.go](api/webhook.go#L740), [L932](api/webhook.go#L932), [L1032](api/webhook.go#L1032)

All webhook handlers properly catch and log size violations:
```go
if strings.Contains(err.Error(), "request body too large") {
    log.Printf("WEBHOOK_PAYLOAD_TOO_LARGE: messenger=whatsapp")
    http.Error(w, "Payload Too Large", http.StatusRequestEntityTooLarge)
}
```

---

## Documentation Generated

### Production Security Hardening Guide
**File:** [SECURITY_HARDENING.md](../SECURITY_HARDENING.md)

Comprehensive guide including:
- endpoint threat model (WhatsApp, Slack, Telegram, Config API, Metrics, Health)
- detailed mitigations for each threat
- deployment environment variables checklist
- incident response procedures
- compliance checklist
- references to OWASP/NIST standards

---

## Endpoint Summary

### Public Endpoints (No Auth Required)
| Endpoint | Auth | Rate Limit | Body Limit | Threats |
|----------|------|-----------|-----------|---------|
| POST /webhook | Signature (HMAC-SHA256) | None | 1 MB | Spoofing, tampering |
| POST /slack-webhook | Signature + 5-min timestamp | None | 1 MB | Replay |
| POST /telegram-webhook | Secret token | None | 1 MB | Token spoofing |
| GET /health | None | None | 1 KB | Service enumeration |

### Authenticated Endpoints
| Endpoint | Auth | Rate Limit | Body Limit | Threats |
|----------|------|-----------|-----------|---------|
| GET /metrics/cache | Optional token | None | 1 KB | Data leakage |
| /api/* | Required token | None | 256 KB | Unauthorized access |

---

## Pre-Deployment Verification

#### Run Tests
```bash
cd api
go test ./...
```

**Expected:** All tests pass (should see `ok  webhook-handler X.XXXs`)

#### Build Docker Image
```bash
docker build -t koko:1.0 .
```

**Verify:** Uses golang:1.25-alpine3.22 and alpine:3.22

#### Scan for Vulnerabilities
```bash
# If trivy is installed:
trivy image koko:1.0

# Check Go dependencies:
cd api && go list -m all
```

---

## Secrets to Configure (Production)

### Required Environment Variables
```bash
# Database
POSTGRES_URL="postgresql://user:pass@db.internal:5432/koko?sslmode=require"

# WhatsApp Integration
WHATSAPP_VERIFY_TOKEN="<strong-random>"
WHATSAPP_ACCESS_TOKEN="<meta-issued>"
WHATSAPP_APP_SECRET="<meta-issued>"

# Slack Integration
SLACK_SIGNING_SECRET="<slack-issued>"

# Telegram Integration
TELEGRAM_BOT_TOKEN="<telegram-issued>"
TELEGRAM_SECRET_TOKEN="<strong-random>"

# Google Cloud
GOOGLE_CLOUD_PROJECT="<gcp-project-id>"

# API Authentication
KOKO_ADMIN_API_TOKEN="<64-char-random>"      # openssl rand -hex 32
KOKO_METRICS_TOKEN="<32-char-random>"        # openssl rand -hex 16

# CORS
KOKO_CORS_ORIGINS="https://admin.example.com"

# Server
PORT="8080"    # Or behind reverse proxy
```

### Generate Random Tokens
```bash
# Admin API Token (64 chars)
openssl rand -hex 32

# Metrics Token (32 chars)
openssl rand -hex 16

# Webhook Secrets (strong random)
openssl rand -hex 32
```

---

## Testing Hardening in Development

### Test Security Headers
```bash
curl -i http://localhost:8080/health | grep -E "Strict-Transport-Security|X-Content-Type|X-Frame-Options"
```

**Expected:**
```
Strict-Transport-Security: max-age=31536000; includeSubDomains
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
```

### Test Request Body Limit
```bash
# Create 2MB payload
dd if=/dev/zero bs=1M count=2 | base64 > oversized.json

# Send to webhook
curl -X POST \
     -H "Content-Type: application/json" \
     --data-binary "@oversized.json" \
     http://localhost:8080/webhook

# Expected: HTTP 413 Payload Too Large
```

### Test CORS Preflight
```bash
curl -X OPTIONS \
     -H "Origin: https://admin.example.com" \
     -H "Access-Control-Request-Method: POST" \
     http://localhost:8080/api/accounts

# Expected: 200 with Access-Control-Allow-* headers
```

### Test Admin API Authentication
```bash
# Without token
curl http://localhost:8080/api/accounts
# Expected: 401 Unauthorized

# With token
curl -H "Authorization: Bearer $KOKO_ADMIN_API_TOKEN" \
     http://localhost:8080/api/accounts
# Expected: 200 with JSON response
```

---

## Monitoring & Alerting

### Logs to Monitor
```bash
# Authentication failures
grep "WEBHOOK_VERIFICATION_FAILED\|SIGNATURE_VERIFICATION_FAILED\|TELEGRAM_SECRET_VALIDATION_FAILED" logs

# Size limit violations
grep "WEBHOOK_PAYLOAD_TOO_LARGE" logs

# Unauthorized API access
grep "401\|Unauthorized" logs

# Errors
grep "ERROR" logs
```

### Metrics to Alert On
- Error rate > 1% (query `/metrics/cache`)
- Webhook latency > 5 seconds
- Authentication failures > 10/minute
- Payload size violations > 0/hour (should be rare)

---

## Rollback Plan

If issues arise after deployment:

1. **Reduce HSTS max-age** to detect issues faster:
   ```go
   "Strict-Transport-Security", "max-age=300"  // 5 minutes instead of 1 year
   ```

2. **Disable CORS restrictions** temporarily:
   ```bash
   unset KOKO_CORS_ORIGINS  # Falls back to localhost only
   ```

3. **Increase request body limits** if legitimate traffic is rejected:
   ```go
   maxWebhookPayloadBytes = 5 * 1024 * 1024  // 5 MB instead of 1 MB
   ```

4. **Disable optional metrics auth**:
   ```bash
   unset KOKO_METRICS_TOKEN  # Metrics become public
   ```

---

## Summary

✅ **All production hardening is in place and tested.**

The application is now ready for enterprise deployment with:
- Security headers on all responses
- CORS validation for cross-origin requests
- Request body size limits with proper error handling
- HTTP server timeouts against slowloris/resource exhaustion
- Webhook signature verification with replay protection
- Authenticated admin/metrics endpoints
- Pinned Docker images for reproducible builds
- Comprehensive threat model documentation

**Next Steps:**
1. Configure all required environment variables per [SECURITY_HARDENING.md](../SECURITY_HARDENING.md#8-deployment-environment-variables-checklist)
2. Deploy behind reverse proxy with TLS termination
3. Enable monitoring and alerting per section above
4. Run penetration testing before customer exposure
