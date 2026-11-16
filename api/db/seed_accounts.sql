-- Seed script for Demo Account C and D accounts
-- This script creates accounts, groups, users, and group memberships

-- ======================== PLAN TIER: Standard tier with storage limits ========================

INSERT INTO plan_tiers (tier_code, display_name, monthly_price_usd, storage_bytes, monthly_summarize_limit, monthly_analyze_limit, monthly_act_limit)
VALUES ('standard', 'Standard Plan', 29.99, 5368709120, 200, 100, 50)  -- 5 GB storage
ON CONFLICT (tier_code) DO NOTHING;

-- ======================== ACCOUNT 1: Demo Account C ========================

-- Insert account
INSERT INTO accounts (account_id, display_name, created_at, subscribed_plan_tier_id, total_storage_used)
VALUES (
  'demo_account_c',
  'Demo Account C',
  NOW(),
  (SELECT id FROM plan_tiers WHERE tier_code = 'standard'),
  0
)
ON CONFLICT (account_id) DO UPDATE SET display_name = COALESCE(EXCLUDED.display_name, accounts.display_name);

-- Insert account user: Demo User 4
INSERT INTO account_users (account_id, user_name, messenger_type, messenger_chat_id, created_at)
VALUES ('demo_account_c', 'demo_user_4', 'telegram', '+15555550104', NOW())
ON CONFLICT DO NOTHING;

-- Insert groups for Demo Account C
INSERT INTO account_groups (account_id, group_id, group_name, messenger_type, added_at)
VALUES 
  ('demo_account_c', 'grp_demo_c_consumer', 'consumer', 'telegram', NOW()),
  ('demo_account_c', 'grp_demo_c_electronics', 'electronics', 'telegram', NOW()),
  ('demo_account_c', 'grp_demo_c_industrial', 'industrial', 'telegram', NOW()),
  ('demo_account_c', 'grp_demo_c_revenue', 'revenue', 'telegram', NOW())
ON CONFLICT ON CONSTRAINT account_groups_account_id_group_name_messenger_type_key DO NOTHING;

-- Add Demo User 4 to all groups
INSERT INTO group_memberships (group_id, user_id, added_at)
SELECT ag.id, au.id, NOW()
FROM account_groups ag
JOIN account_users au ON ag.account_id = au.account_id
WHERE ag.account_id = 'demo_account_c'
  AND au.account_id = 'demo_account_c'
  AND au.user_name = 'demo_user_4'
  AND au.messenger_type = 'telegram'
ON CONFLICT DO NOTHING;

-- ======================== ACCOUNT 2: Demo Account D, India ========================

-- Insert account
INSERT INTO accounts (account_id, display_name, created_at, subscribed_plan_tier_id, total_storage_used)
VALUES (
  'demo_account_d',
  'Demo Account D, India',
  NOW(),
  (SELECT id FROM plan_tiers WHERE tier_code = 'standard'),
  0
)
ON CONFLICT (account_id) DO UPDATE SET display_name = COALESCE(EXCLUDED.display_name, accounts.display_name);

-- Insert account user: Neeraj Mehta
INSERT INTO account_users (account_id, user_name, messenger_type, messenger_chat_id, created_at)
VALUES ('demo_account_d', 'demo_user_5', 'telegram', '+15555550105', NOW())
ON CONFLICT DO NOTHING;

-- Insert groups for Demo Account D
INSERT INTO account_groups (account_id, group_id, group_name, messenger_type, added_at)
VALUES 
  ('demo_account_d', 'grp_demo_d_consumer', 'consumer', 'telegram', NOW()),
  ('demo_account_d', 'grp_demo_d_electronics', 'electronics', 'telegram', NOW()),
  ('demo_account_d', 'grp_demo_d_industrial', 'industrial', 'telegram', NOW()),
  ('demo_account_d', 'grp_demo_d_revenue', 'revenue', 'telegram', NOW())
ON CONFLICT ON CONSTRAINT account_groups_account_id_group_name_messenger_type_key DO NOTHING;

-- Add Neeraj to all groups
INSERT INTO group_memberships (group_id, user_id, added_at)
SELECT ag.id, au.id, NOW()
FROM account_groups ag
JOIN account_users au ON ag.account_id = au.account_id
WHERE ag.account_id = 'demo_account_d'
  AND au.account_id = 'demo_account_d'
  AND au.user_name = 'demo_user_5'
  AND au.messenger_type = 'telegram'
ON CONFLICT DO NOTHING;

-- ======================== USER PREFERENCES: Audio-first ========================

-- Set preferences for Demo User 4 - audio enabled
INSERT INTO user_preferences (account_id, user_name, enable_voice_summaries, preferred_language, timezone)
VALUES ('demo_account_c', 'demo_user_4', true, 'en', 'Asia/Kolkata')
ON CONFLICT DO NOTHING;

-- Set preferences for Neeraj Mehta - audio enabled
INSERT INTO user_preferences (account_id, user_name, enable_voice_summaries, preferred_language, timezone)
VALUES ('demo_account_d', 'demo_user_5', true, 'en', 'Asia/Kolkata')
ON CONFLICT DO NOTHING;

-- ======================== ACCOUNT 3: Demo Account A ========================

-- Insert account
INSERT INTO accounts (account_id, display_name, created_at, subscribed_plan_tier_id, total_storage_used)
VALUES (
  'demo_account_a',
  'Demo Account A',
  NOW(),
  (SELECT id FROM plan_tiers WHERE tier_code = 'standard'),
  0
)
ON CONFLICT (account_id) DO UPDATE SET display_name = COALESCE(EXCLUDED.display_name, accounts.display_name);

-- Insert account users
INSERT INTO account_users (account_id, user_name, messenger_type, messenger_chat_id, created_at)
VALUES 
  ('demo_account_a', 'demo_user_1', 'telegram', '+15555550101', NOW()),
  ('demo_account_a', 'demo_user_6', 'telegram', '', NOW()),
  ('demo_account_a', 'demo_user_2', 'telegram', '+15555550102', NOW())
ON CONFLICT DO NOTHING;

-- Insert groups for Demo Account A
INSERT INTO account_groups (account_id, group_id, group_name, messenger_type, added_at)
VALUES 
  ('demo_account_a', 'grp_demo_a_consumer', 'consumer', 'telegram', NOW()),
  ('demo_account_a', 'grp_demo_a_electronics', 'electronics', 'telegram', NOW()),
  ('demo_account_a', 'grp_demo_a_industrial', 'industrial', 'telegram', NOW()),
  ('demo_account_a', 'grp_demo_a_revenue', 'revenue', 'telegram', NOW())
ON CONFLICT ON CONSTRAINT account_groups_account_id_group_name_messenger_type_key DO NOTHING;

-- Add users to all groups
INSERT INTO group_memberships (group_id, user_id, added_at)
SELECT ag.id, au.id, NOW()
FROM account_groups ag
JOIN account_users au ON ag.account_id = au.account_id
WHERE ag.account_id = 'demo_account_a'
  AND au.account_id = 'demo_account_a'
  AND au.user_name IN ('demo_user_1', 'demo_user_6', 'demo_user_2')
  AND au.messenger_type = 'telegram'
ON CONFLICT DO NOTHING;

-- ======================== ACCOUNT 4: Demo Account B ========================

-- Insert account
INSERT INTO accounts (account_id, display_name, created_at, subscribed_plan_tier_id, total_storage_used)
VALUES (
  'demo_account_b',
  'Demo Account B',
  NOW(),
  (SELECT id FROM plan_tiers WHERE tier_code = 'standard'),
  0
)
ON CONFLICT (account_id) DO UPDATE SET display_name = COALESCE(EXCLUDED.display_name, accounts.display_name);

-- Insert account users
INSERT INTO account_users (account_id, user_name, messenger_type, messenger_chat_id, created_at)
VALUES 
  ('demo_account_b', 'demo_user_3', 'telegram', '+15555550103', NOW()),
  ('demo_account_b', 'demo_user_1', 'telegram', '+15555550101', NOW())
ON CONFLICT DO NOTHING;

-- Insert groups for Demo Account B
INSERT INTO account_groups (account_id, group_id, group_name, messenger_type, added_at)
VALUES 
  ('demo_account_b', 'grp_demo_b_consumer', 'consumer', 'telegram', NOW()),
  ('demo_account_b', 'grp_demo_b_electronics', 'electronics', 'telegram', NOW()),
  ('demo_account_b', 'grp_demo_b_industrial', 'industrial', 'telegram', NOW()),
  ('demo_account_b', 'grp_demo_b_revenue', 'revenue', 'telegram', NOW())
ON CONFLICT ON CONSTRAINT account_groups_account_id_group_name_messenger_type_key DO NOTHING;

-- Add users to all groups
INSERT INTO group_memberships (group_id, user_id, added_at)
SELECT ag.id, au.id, NOW()
FROM account_groups ag
JOIN account_users au ON ag.account_id = au.account_id
WHERE ag.account_id = 'demo_account_b'
  AND au.account_id = 'demo_account_b'
  AND au.user_name IN ('demo_user_3', 'demo_user_1')
  AND au.messenger_type = 'telegram'
ON CONFLICT DO NOTHING;
