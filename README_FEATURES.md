# Koko Features and Usage

## Accounts, Groups, and Users
- Account-scoped access control using API keys.
- Group-scoped collaboration and broadcasts.
- Multi-user membership model with messenger identity mapping.

## Messaging and API Input
- Messaging channels: Telegram and WhatsApp (Slack scaffold exists).
- REST ingestion endpoint for text/audio/video/doc flows.
- Unified internal message representation for cross-channel processing.

## Query Interfaces
- REST endpoints for summaries, act prompts, NLQ, and jobs.
- GraphQL endpoint for flexible querying.
- MCP server endpoint for AI-tool style access.

## NLP in Koko Commands
- Prefix-based command model (`koko:`).
- Natural language fallback for unstructured `koko:` inputs.
- English support across UK/AU/US dialect and official terms.
- Domain-aware acronym expansion:
  - revenue, finance, industrial
  - common-language fallback when domain context is absent
- Spoken command path supported via audio transcription and NLP routing.

## Koko Command Families
- summarize
- analyze
- act
- plan
- group
- help
- addGroup / deleteGroup
- addUser / deleteUser

## How Help Works
- Bare `koko:` or `koko:help` returns command menu.
- Contextual help can include active account/group details.
- Unknown `koko:` input routes to NLP help/query handling.

## Learning Over Time
- Learned acronyms and phrase rewrites can be submitted via MCP client invoke API.
- Learning is persisted in PostgreSQL and loaded lazily by context cache.
- Restart-safe behavior via DB fallback.
