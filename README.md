# Koko

Koko is an AI operations assistant that lives inside team chat. Teams send
messages, voice notes, and documents to a Koko bot on Telegram or WhatsApp.
Koko turns that conversation into summaries, analysis, and recommended
actions, and answers natural-language questions about it.

Commands start with `koko:`, for example `koko:summarize`, `koko:analyze 3`,
or `koko:what decisions did we make this week?`.

note: some code may need to be updated, originally added in the beginning of 2026

## What it does

- **Summarize**: a digest of recent group messages, including transcribed audio and video.
- **Analyze**: insights and recommendations, optionally scoped to a domain (industrial, electronics, revenue, consumer).
- **Act**: turns recommendations into downstream actions through configurable endpoints.
- **Ask**: natural-language queries over stored conversation data (NLQ).
- **Manage**: accounts, workspaces (groups), and members, managed by command or admin API.
- **Learn**: stores acronyms and phrase rewrites per account, group, and domain, and persists them in PostgreSQL.
- **Rich replies**: text, image, and audio (MP3) responses.

## Interfaces

| Interface | Purpose |
|-----------|---------|
| Telegram and WhatsApp webhooks | Chat ingestion and replies. Slack webhook routes exist as a scaffold. |
| `POST /api/input`, `GET /api/summaries`, `GET /api/act-prompts`, `POST /api/nlq` | REST API (requires an API key) |
| `POST /graphql` | GraphQL API (requires an API key) |
| `POST /mcp` | MCP server exposing Koko tools to AI clients |
| `POST /api/mcp-client/invoke` | Invoke Koko tools, or external MCP servers |
| `/api/config/*` | Admin API for accounts, groups, users, and memberships (`KOKO_ADMIN_API_TOKEN`) |
| `GET /health` | Health check |

## Architecture

```
Telegram / WhatsApp / REST / GraphQL / MCP
                  │
                  ▼
        Normalize to internal message schema
                  │
                  ▼
  Command parsing + NLP (domain, date, dialect context)
                  │
                  ▼
  Summarize / analyze / act / NLQ  ──►  Vertex AI (Gemini, Imagen, TTS)
                  │
                  ▼
   PostgreSQL (state, learned NLP artifacts) + optional Pub/Sub job queue
                  │
                  ▼
          Reply to chat, API caller, or group members
```

Details are in [README_ARCHITECTURE.md](README_ARCHITECTURE.md).

**Stack:** Go (standard library HTTP server), PostgreSQL, Google Cloud Vertex AI,
Google Cloud Pub/Sub (optional), Telegram Bot API, WhatsApp Cloud API.

## Repository layout

```
api/                 Go service (HTTP handlers, messaging adapters, NLP, storage)
api/db/schema.sql    PostgreSQL schema
api/db/seed_accounts.sql  Optional demo seed data (placeholder values only)
Dockerfile           Multi-stage container build
.env.example         Environment variable template
```

## Getting started

### Prerequisites

- Go 1.25 or later
- PostgreSQL
- Messaging bot credentials for at least one channel (see below)
- A Google Cloud project with Vertex AI enabled, if you use AI features

### 1. Configure the environment

```bash
cp .env.example .env
# Edit .env and fill in your values. Never commit .env.
```

Key variables:

| Variable | Purpose |
|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_SECRET_TOKEN` | Telegram bot and webhook secret |
| `WHATSAPP_ACCESS_TOKEN`, `WHATSAPP_APP_SECRET`, `WHATSAPP_VERIFY_TOKEN`, `WHATSAPP_PHONE_NUMBER_ID` | WhatsApp Cloud API |
| `SLACK_SIGNING_SECRET` | Slack request verification (scaffold) |
| `GOOGLE_CLOUD_PROJECT`, `VERTEX_AI_LOCATION`, `VERTEX_AI_MODEL` | Vertex AI |
| `KOKO_ADMIN_API_TOKEN` | Protects `/api/config/*` |
| `KOKO_FEATURE_MODE` | `all`, `industrial`, `electronics`, `revenue`, or `consumer` |
| `PORT` | HTTP port (default `8080`) |

Full messaging setup is in [README_MESSAGING_CONFIG.md](README_MESSAGING_CONFIG.md).

### 2. Create the database schema

```bash
psql "$DATABASE_URL" -f api/db/schema.sql
```

Optionally load demo data. The seed file uses placeholder names and phone numbers:

```bash
psql "$DATABASE_URL" -f api/db/seed_accounts.sql
```

### 3. Run

```bash
cd api
go run .
curl http://localhost:8080/health
```

Or build and run the container:

```bash
docker build -t koko .
docker run --env-file .env -p 8080:8080 koko
```

### 4. Register accounts and users

Create an account, its groups, and its members through the admin API, then
send `koko:` commands from a registered chat.

```bash
curl -X POST http://localhost:8080/api/config/accounts \
  -H "Authorization: Bearer $KOKO_ADMIN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"account_id": "demo_account_a", "display_name": "Demo Account A"}'
```

## Testing

```bash
cd api
go test ./...
```

## Security notes

- Webhooks are verified with each platform's secret or signature (WhatsApp app secret, Telegram secret token, Slack signing secret).
- The REST, GraphQL, and MCP endpoints require per-account API keys. Admin endpoints require `KOKO_ADMIN_API_TOKEN`.
- Keep all credentials in environment variables or a secrets manager. Do not commit `.env`.
- Read [SECURITY_HARDENING.md](SECURITY_HARDENING.md) and [DEPLOYMENT_HARDENING_CHECKLIST.md](DEPLOYMENT_HARDENING_CHECKLIST.md) before deploying to production.

## Documentation

- [Architecture and design](README_ARCHITECTURE.md)
- [Features and usage](README_FEATURES.md)
- [Messaging configuration](README_MESSAGING_CONFIG.md)
- [MCP client and server](README_MCP.md)
- [Dependencies](DEPENDENCIES.md)
- [Security hardening](SECURITY_HARDENING.md)
- [Deployment checklist](DEPLOYMENT_HARDENING_CHECKLIST.md)
- [Feature implementation notes](USP_FEATURES_IMPLEMENTATION.md)

## License

Copyright 2026 KokoHq Inc.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full text.
