# Koko MCP Client and Server

## MCP Server
Endpoint:
- POST /mcp

Implements JSON-RPC style tool serving for:
- query_summaries
- query_act_prompts
- natural_language_query
- get_koko_help
- revenue_insights
- finance_insights
- industrial_insights
- accounting_placeholder
- operations_placeholder
- real_estate_placeholder

## MCP Client
Endpoint:
- POST /api/mcp-client/invoke

Capabilities:
- Invoke internal Koko MCP tools.
- Optionally call external MCP servers.
- Pass external model hint and language code.
- Learn acronyms and phrase rewrites via request payload.

## Internal vs External LLM Usage
Internal path:
- Use Koko's built-in orchestration and model-provider configuration.

External path:
- Set `use_external=true` and provide `external_mcp_url`.
- Optional bearer token for external MCP auth.
- Optional `external_model` hint passed in tool arguments.

## Learning Persistence
MCP client supports persistence-oriented learning fields:
- learn_acronym
- acronym_expanded
- learn_phrase
- phrase_normalized

These are stored in PostgreSQL and loaded lazily into context cache at runtime.
