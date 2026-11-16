# Koko Messaging Configuration

## WhatsApp Configuration
Required values:
- WHATSAPP_VERIFY_TOKEN
- WHATSAPP_APP_SECRET
- WHATSAPP_PHONE_NUMBER_ID
- WHATSAPP_ACCESS_TOKEN

Webhook routes:
- /meta/whatsapp/webhook (recommended)
- aliases: /whatsapp/webhook, /whatsapp-webhook, /webhook

Notes:
- `WHATSAPP_PHONE_NUMBER_ID` is Meta phone-number ID, not the E.164 number.
- Sender registration is number-based (`user_name` must match incoming sender format).

## Telegram Configuration
Required values:
- TELEGRAM_BOT_TOKEN
- TELEGRAM_SECRET_TOKEN

Webhook routes:
- /telegram/webhook
- alias: /telegram-webhook

## Group/User Registration APIs
Admin endpoints:
- POST /api/config/accounts
- POST /api/config/groups
- POST /api/config/users
- POST /api/config/group-memberships

Optional admin protection:
- KOKO_ADMIN_API_TOKEN

## Multi-Messenger Usage
- Same account/group model can support multiple messenger identities.
- Broadcasts can fan out across registered group member destinations.
