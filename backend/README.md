# QGramm Backend (V1)

Production-oriented backend for the QGramm iOS client.

Implemented core features:
- Invite-only registration with device activation memory.
- Email + verification code auth + captcha gate.
- Root account and QGramm system account bootstrap.
- Unique user IDs (`uid`) and unique nicknames (`@nickname`).
- Direct chat creation by nickname.
- Encrypted message payload storage (server stores ciphertext/nonce/tag only).
- Chat history persistence and recovery bundle storage for recovery-key flow.
- File/voice/video-note/avatar upload via chunk sessions.
- Reactions, message reports, root moderation actions (reject/block).
- Invite graph persistence and admin visibility.
- Trust level engine (L1..L5) based on usage/verification/activity/reports.
- Audio call history + websocket signaling channel.
- QGramm broadcast channel for system/support/news messages.
- Storage and operational limits loaded from JSON configs.

## Stack
- Go 1.24
- PostgreSQL 16
- WebSocket for realtime and call signaling
- Coturn container for WebRTC TURN fallback

## Directory
- `cmd/server`: entrypoint.
- `internal/*`: config, db, services, API, realtime hub, storage.
- `migrations`: SQL schema.
- `configs`: runtime/limits/storage/root settings (JSON).
- `docker-compose.yml`: backend + postgres + coturn.

## Environment
Use `.env.example` values (or compose env vars):
- `QGRAMM_DATABASE_DSN`
- `QGRAMM_JWT_SECRET`
- `QGRAMM_CONFIG_DIR`

## Run with Docker Compose
```bash
cd backend
docker compose up -d --build
```

## Main API

Public:
- `POST /v1/auth/invites/activate`
- `POST /v1/auth/device/check`
- `POST /v1/auth/send-code`
- `POST /v1/auth/register`
- `POST /v1/auth/login`

Auth required:
- `POST /v1/auth/logout`
- `POST /v1/auth/heartbeat`
- `GET /v1/auth/ws`
- `GET /v1/me`
- `PATCH /v1/me`
- `PUT /v1/me/recovery-bundle`
- `GET /v1/me/recovery-bundle`
- `GET /v1/notifications`
- `POST /v1/notifications/{notificationID}/read`
- `GET /v1/users/by-nickname/{nickname}`
- `POST /v1/invites`
- `GET /v1/invites`
- `GET /v1/invites/graph`
- `POST /v1/chats/direct/by-nickname`
- `GET /v1/chats`
- `GET /v1/chats/{conversationID}/messages`
- `POST /v1/chats/{conversationID}/messages`
- `POST /v1/chats/{conversationID}/messages/{messageID}/reactions`
- `DELETE /v1/chats/{conversationID}/messages/{messageID}/reactions/{emoji}`
- `POST /v1/chats/{conversationID}/messages/{messageID}/report`
- `POST /v1/uploads`
- `PUT /v1/uploads/{uploadID}/chunks/{chunkIndex}`
- `POST /v1/uploads/{uploadID}/complete`
- `POST /v1/uploads/{uploadID}/cancel`
- `GET /v1/uploads/{uploadID}`
- `GET /v1/attachments/{attachmentID}`
- `GET /v1/attachments/{attachmentID}/download`
- `POST /v1/calls/start`
- `POST /v1/calls/{callID}/end`
- `GET /v1/calls`

Root-only:
- `GET /v1/admin/users`
- `POST /v1/admin/users/{userID}/block`
- `POST /v1/admin/users/{userID}/trust`
- `POST /v1/admin/users/{userID}/telegram-verified`
- `GET /v1/admin/reports`
- `POST /v1/admin/reports/{reportID}/reject`
- `POST /v1/admin/reports/{reportID}/block`
- `POST /v1/admin/qgramm/broadcast`
- `POST /v1/admin/invites/{inviteID}/revoke`

## E2E Data Model
For messages backend stores encrypted fields as-is:
- `body_ciphertext`
- `body_nonce`
- `body_tag`
- `quoted_ciphertext`
- `quoted_nonce`
- `quoted_tag`

Server does not decrypt message content.

## Captcha Modes
- `mock` (default for local/dev): token must match `mock_valid_token`.
- `disabled`: captcha check is skipped (not recommended outside controlled environments).
- `turnstile`: validates token via Cloudflare Turnstile (`turnstile_secret`, optional `verify_url`).

## Trust Logic (implemented)
- Level 1: immediately after registration.
- Level 2: Telegram verified + account age >= 5 days + active usage days >= 5.
- Level 3: account age >= 14 days + no complaints in last 14 days + no suspicious burst.
- Level 4: >= 25 qualified active days (>= 5 minutes/day).
- Level 5: account age >= 60 days + no complaints in 60 days + active days threshold.

Thresholds are configurable via `configs/runtime.json` and `configs/limits.json`.

## Notes for iOS integration
- Keep local encryption/decryption in app; send encrypted envelopes to backend.
- For file transfer use upload-session flow (`create -> chunks -> complete`) in background tasks.
- Use websocket (`/v1/auth/ws`) for:
  - message events,
  - reaction events,
  - reports to root,
  - call signaling.
