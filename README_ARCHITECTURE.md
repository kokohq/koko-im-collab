# Koko Architecture and Design

## Overview
Koko is a multi-channel conversational intelligence platform with an API-first architecture.

Core design goals:
- Unified ingestion across messaging platforms and APIs.
- Domain-aware NLP and action orchestration.
- Async-first workload model for resilience.

## High-Level Components
- Ingress: webhooks for Telegram/WhatsApp/Slack and REST input APIs.
- Orchestration: command parsing, NLP intent interpretation, policy checks.
- Intelligence: summarization, analysis, action recommendation, NLQ.
- Storage: PostgreSQL for transactional and NLP learning persistence.
- Interface layer: REST, GraphQL, MCP server, MCP client.
- Async runtime: job queue and optional Pub/Sub transport.

## Runtime Flow
1. Input arrives through webhook or REST.
2. Message is normalized into common internal schema.
3. NLP layer parses command/intent with domain and date context.
4. Query/analysis executed via DB and model providers.
5. Results returned to caller and optionally broadcast to group members.
6. Learning artifacts (acronyms, phrase rewrites) persist to DB.

## NLP Learning Design
- Built-in English dialect and acronym packs (UK/AU/US + domain terms).
- Learned acronyms and phrase rewrites are stored in PostgreSQL.
- Context-aware lazy cache in memory:
  - cache key: account + group + domain + locale
  - TTL-based cache with DB fallback
- DB remains source of truth after app restarts.

## Extensibility
- Domain modules can be added without changing transport layers.
- MCP tools are schema-versioned and domain-scoped.
- External MCP/LLM routing can be enabled per request.
