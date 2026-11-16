CREATE TABLE IF NOT EXISTS plan_tiers (
    id BIGSERIAL PRIMARY KEY,
    tier_code TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    monthly_price_usd NUMERIC(12,2) NOT NULL,
    storage_bytes BIGINT NOT NULL,
    monthly_summarize_limit INTEGER NOT NULL DEFAULT 200,
    monthly_analyze_limit INTEGER NOT NULL DEFAULT 100,
    monthly_act_limit INTEGER NOT NULL DEFAULT 50,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE plan_tiers ADD COLUMN IF NOT EXISTS monthly_summarize_limit INTEGER NOT NULL DEFAULT 200;
ALTER TABLE plan_tiers ADD COLUMN IF NOT EXISTS monthly_analyze_limit INTEGER NOT NULL DEFAULT 100;
ALTER TABLE plan_tiers ADD COLUMN IF NOT EXISTS monthly_act_limit INTEGER NOT NULL DEFAULT 50;

CREATE TABLE IF NOT EXISTS accounts (
    account_id TEXT PRIMARY KEY,
    display_name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    subscribed_plan_tier_id BIGINT REFERENCES plan_tiers(id),
    total_storage_used BIGINT NOT NULL DEFAULT 0
);

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS display_name TEXT;

CREATE TABLE IF NOT EXISTS account_groups (
    id BIGSERIAL PRIMARY KEY,
    group_id TEXT NOT NULL UNIQUE,
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    messenger_type TEXT NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, group_name, messenger_type)
);

ALTER TABLE account_groups ADD COLUMN IF NOT EXISTS group_id TEXT UNIQUE;

CREATE TABLE IF NOT EXISTS account_users (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    user_name TEXT NOT NULL,
    messenger_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, user_name, messenger_type)
);

ALTER TABLE account_users ADD COLUMN IF NOT EXISTS tou_sent_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS group_memberships (
    group_id BIGINT NOT NULL REFERENCES account_groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES account_users(id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS plan_change_requests (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    requested_by_user_id BIGINT REFERENCES account_users(id),
    requested_by_user_name TEXT NOT NULL,
    current_plan_tier_id BIGINT REFERENCES plan_tiers(id),
    requested_plan_tier_id BIGINT NOT NULL REFERENCES plan_tiers(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS messenger_inputs (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT,
    user_name TEXT,
    messenger_type TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    message_id TEXT,
    message_type TEXT NOT NULL,
    text_content TEXT,
    media_id TEXT,
    mime_type TEXT,
    payload JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messenger_inputs_lookup
    ON messenger_inputs (messenger_type, chat_id, received_at DESC);

-- sent_at stores the original message timestamp from the sender's platform
-- (Unix epoch converted to UTC). received_at remains the server ingest time.
-- Ordering and range queries should prefer sent_at when available.
ALTER TABLE messenger_inputs ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_messenger_inputs_sent_at
    ON messenger_inputs (messenger_type, chat_id, sent_at ASC)
    WHERE sent_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS messenger_summaries (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    messenger_type TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    domain TEXT,
    window_start TIMESTAMPTZ NOT NULL,
    window_end TIMESTAMPTZ NOT NULL,
    source_message_count INTEGER NOT NULL,
    summary_text TEXT NOT NULL,
    summarized_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messenger_summaries_lookup
    ON messenger_summaries (messenger_type, chat_id, summarized_at DESC);

CREATE TABLE IF NOT EXISTS command_usage_events (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    command_name TEXT NOT NULL,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_command_usage_lookup
    ON command_usage_events (account_id, group_name, command_name, executed_at DESC);

CREATE TABLE IF NOT EXISTS policy_audit_logs (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT,
    group_name TEXT,
    user_name TEXT,
    messenger_type TEXT,
    chat_id TEXT,
    message_id TEXT,
    message_type TEXT,
    action TEXT NOT NULL,
    reason_code TEXT NOT NULL,
    warning_message TEXT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_policy_audit_lookup
    ON policy_audit_logs (created_at DESC, action, reason_code);

CREATE INDEX IF NOT EXISTS idx_policy_audit_actor
    ON policy_audit_logs (account_id, group_name, user_name, created_at DESC);

-- =============== USP FEATURES: Sentiment, Topics, Todos, Knowledge Base ===============

CREATE TABLE IF NOT EXISTS message_sentiment (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    sentiment_score NUMERIC(3,2) NOT NULL,  -- -1.0 to 1.0
    sentiment_label TEXT NOT NULL,  -- 'very_negative', 'negative', 'neutral', 'positive', 'very_positive'
    emotion_tags TEXT[] NOT NULL DEFAULT '{}',  -- ['happy', 'frustrated', 'urgent', 'concerned', 'excited']
    confidence NUMERIC(3,2) NOT NULL,  -- 0.0 to 1.0
    analyzed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_message_sentiment_lookup
    ON message_sentiment (account_id, group_name, analyzed_at DESC);

CREATE TABLE IF NOT EXISTS message_topics (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    topic_name TEXT NOT NULL,
    topic_category TEXT,  -- 'work', 'personal', 'technical', 'urgent', 'decision', 'fyi', etc.
    relevance_score NUMERIC(3,2) NOT NULL,  -- 0.0 to 1.0
    tagged_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_message_topics_lookup
    ON message_topics (account_id, group_name, topic_name);

CREATE TABLE IF NOT EXISTS extracted_todos (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    message_id TEXT NOT NULL,
    extracted_from_user TEXT,
    todo_text TEXT NOT NULL,
    assigned_to TEXT,  -- who should do it (null if unassigned)
    due_date DATE,  -- extracted deadline
    priority TEXT NOT NULL DEFAULT 'medium',  -- 'low', 'medium', 'high', 'critical'
    status TEXT NOT NULL DEFAULT 'pending',  -- 'pending', 'in_progress', 'completed', 'cancelled'
    confidence NUMERIC(3,2) NOT NULL,  -- 0.0 to 1.0
    tags TEXT[] NOT NULL DEFAULT '{}',  -- user-defined tags
    extracted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_extracted_todos_lookup
    ON extracted_todos (account_id, group_name, status, due_date);

CREATE TABLE IF NOT EXISTS knowledge_base_articles (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT,  -- NULL if account-wide KB
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    auto_generated BOOLEAN NOT NULL DEFAULT true,
    source_messages TEXT[] NOT NULL,  -- array of message IDs that contributed
    category TEXT,
    tags TEXT[] NOT NULL DEFAULT '{}',
    view_count INTEGER NOT NULL DEFAULT 0,
    helpful_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_knowledge_base_lookup
    ON knowledge_base_articles (account_id, group_name);

CREATE TABLE IF NOT EXISTS group_analytics (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    analysis_date DATE NOT NULL,
    message_count INTEGER NOT NULL,
    unique_participants INTEGER NOT NULL,
    avg_sentiment_score NUMERIC(3,2),
    dominant_sentiment TEXT,
    top_topics TEXT[] NOT NULL DEFAULT '{}',
    todo_count INTEGER NOT NULL DEFAULT 0,
    todo_completed INTEGER NOT NULL DEFAULT 0,
    engagement_score NUMERIC(3,2),  -- 0.0 to 1.0
    health_score NUMERIC(3,2),  -- 0.0 to 1.0
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_group_analytics_lookup
    ON group_analytics (account_id, group_name, analysis_date DESC);

CREATE TABLE IF NOT EXISTS user_preferences (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    user_name TEXT,  -- NULL if account-wide
    preferred_language TEXT NOT NULL DEFAULT 'en',
    email_digest_frequency TEXT DEFAULT 'daily',  -- 'none', 'daily', 'weekly'
    enable_sentiment_analysis BOOLEAN NOT NULL DEFAULT true,
    enable_topic_extraction BOOLEAN NOT NULL DEFAULT true,
    enable_todo_extraction BOOLEAN NOT NULL DEFAULT true,
    enable_voice_summaries BOOLEAN NOT NULL DEFAULT true,
    dark_mode BOOLEAN NOT NULL DEFAULT false,
    timezone TEXT DEFAULT 'UTC',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_preferences_lookup
    ON user_preferences (account_id, user_name);

CREATE TABLE IF NOT EXISTS conversation_timeline (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL,
    event_time TIMESTAMPTZ NOT NULL,
    event_type TEXT NOT NULL,  -- 'decision', 'conflict', 'agreement', 'milestone', 'deadline'
    event_description TEXT NOT NULL,
    source_message_ids TEXT[] NOT NULL,
    sentiment_context TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_conversation_timeline_lookup
    ON conversation_timeline (account_id, group_name, event_time DESC);

-- Optional columnar tuning for environments that support it (for example Citus columnar):
-- ALTER TABLE messenger_inputs SET (columnar = true);
-- ALTER TABLE messenger_summaries SET (columnar = true);

-- =============== BROADCAST: per-user Telegram chat ID ===============

ALTER TABLE account_users ADD COLUMN IF NOT EXISTS messenger_chat_id TEXT;

CREATE INDEX IF NOT EXISTS idx_account_users_chat_id
    ON account_users (messenger_type, messenger_chat_id)
    WHERE messenger_chat_id IS NOT NULL;

-- =============== COMMAND AUDIT: koko:summarize / analyze / act ===============

CREATE TABLE IF NOT EXISTS koko_command_audit (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_id TEXT NOT NULL DEFAULT '',
    group_name TEXT NOT NULL,
    command_name TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- NULL = no confirmation step; 'confirmed' or 'declined' when the act confirmation is answered
    confirmation_response TEXT,
    confirmation_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_koko_command_audit_lookup
    ON koko_command_audit (account_id, group_name, command_name, requested_at DESC);

-- =============== API KEYS: per-account REST/GraphQL auth tokens ===============

CREATE TABLE IF NOT EXISTS koko_api_keys (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    user_name TEXT NOT NULL,
    api_key TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_koko_api_keys_lookup
    ON koko_api_keys (api_key);

-- =============== ACT PROMPTS: AI prompts generated by koko:act ===============

CREATE TABLE IF NOT EXISTS koko_act_prompts (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    user_name TEXT NOT NULL,
    group_id TEXT NOT NULL DEFAULT '',
    group_name TEXT NOT NULL,
    domain TEXT NOT NULL DEFAULT '',
    prompt_text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_koko_act_prompts_lookup
    ON koko_act_prompts (account_id, group_name, domain, created_at DESC);

-- =============== APP METRICS: runtime performance telemetry ===============

-- Periodic runtime snapshots (every 60 seconds): CPU, memory, goroutines,
-- in-flight requests, and cumulative message counters since process start.
CREATE TABLE IF NOT EXISTS app_metrics_samples (
    id                BIGSERIAL    PRIMARY KEY,
    sampled_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    heap_alloc_bytes  BIGINT       NOT NULL DEFAULT 0,
    heap_sys_bytes    BIGINT       NOT NULL DEFAULT 0,
    sys_bytes         BIGINT       NOT NULL DEFAULT 0,
    goroutines        INT          NOT NULL DEFAULT 0,
    cpu_pct           NUMERIC(8,2) NOT NULL DEFAULT 0,
    concurrent_reqs   INT          NOT NULL DEFAULT 0,
    msgs_total_cum    BIGINT       NOT NULL DEFAULT 0,
    msgs_koko_cum     BIGINT       NOT NULL DEFAULT 0,
    msgs_non_koko_cum BIGINT       NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_app_metrics_samples_time
    ON app_metrics_samples (sampled_at DESC);

-- One row per rejected request (rate limit, capacity, auth failure, etc.).
CREATE TABLE IF NOT EXISTS app_rejection_events (
    id          BIGSERIAL   PRIMARY KEY,
    rejected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason      TEXT        NOT NULL DEFAULT '',
    endpoint    TEXT        NOT NULL DEFAULT '',
    user_name   TEXT        NOT NULL DEFAULT '',
    account_id  TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_app_rejection_events_time
    ON app_rejection_events (rejected_at DESC);

-- Service lifecycle records: startup, shutdown, health-check failures.
CREATE TABLE IF NOT EXISTS app_lifecycle_events (
    id         BIGSERIAL   PRIMARY KEY,
    event_type TEXT        NOT NULL,
    event_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    details    TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_app_lifecycle_events_time
    ON app_lifecycle_events (event_at DESC);

-- =============== NLP LEARNING: acronyms and phrase rewrites ===============

CREATE TABLE IF NOT EXISTS nlp_acronym_expansions (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    domain TEXT NOT NULL DEFAULT 'all',
    locale TEXT NOT NULL DEFAULT 'en',
    short_form TEXT NOT NULL,
    expanded_form TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'user',
    learned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, group_name, domain, locale, short_form)
);

CREATE INDEX IF NOT EXISTS idx_nlp_acronym_lookup
    ON nlp_acronym_expansions (account_id, group_name, domain, locale, updated_at DESC);

CREATE TABLE IF NOT EXISTS nlp_phrase_rewrites (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL,
    group_name TEXT NOT NULL DEFAULT '',
    domain TEXT NOT NULL DEFAULT 'all',
    locale TEXT NOT NULL DEFAULT 'en',
    source_phrase TEXT NOT NULL,
    normalized_phrase TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'user',
    learned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, group_name, domain, locale, source_phrase)
);

CREATE INDEX IF NOT EXISTS idx_nlp_phrase_lookup
    ON nlp_phrase_rewrites (account_id, group_name, domain, locale, updated_at DESC);
