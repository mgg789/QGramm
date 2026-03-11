CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    uid TEXT NOT NULL UNIQUE,
    email CITEXT NOT NULL UNIQUE,
    first_name TEXT NOT NULL,
    last_name TEXT NOT NULL,
    nickname CITEXT NOT NULL,
    trust_level SMALLINT NOT NULL DEFAULT 1,
    trust_level_override SMALLINT,
    tg_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ,
    is_root BOOLEAN NOT NULL DEFAULT FALSE,
    is_banned BOOLEAN NOT NULL DEFAULT FALSE,
    invited_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    suspicious_score INTEGER NOT NULL DEFAULT 0,
    active_days_count INTEGER NOT NULL DEFAULT 0,
    recovery_bundle_ciphertext TEXT,
    recovery_bundle_meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    avatar_path TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT users_trust_level_range CHECK (trust_level BETWEEN 1 AND 5)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_nickname_unique ON users ((LOWER(nickname::TEXT)));
CREATE INDEX IF NOT EXISTS idx_users_invited_by ON users (invited_by_user_id);
CREATE INDEX IF NOT EXISTS idx_users_created_at ON users (created_at);

CREATE TABLE IF NOT EXISTS invites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL UNIQUE,
    inviter_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    max_uses INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    is_revoked BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT invites_usage_check CHECK (max_uses >= 1),
    CONSTRAINT invites_used_count_check CHECK (used_count >= 0)
);

CREATE INDEX IF NOT EXISTS idx_invites_inviter ON invites (inviter_user_id);
CREATE INDEX IF NOT EXISTS idx_invites_expires_at ON invites (expires_at);

CREATE TABLE IF NOT EXISTS invite_activations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invite_id UUID NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    device_hash TEXT NOT NULL,
    activated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE (device_hash)
);

CREATE INDEX IF NOT EXISTS idx_invite_activations_invite_id ON invite_activations (invite_id);
CREATE INDEX IF NOT EXISTS idx_invite_activations_used_by ON invite_activations (used_by_user_id);
CREATE INDEX IF NOT EXISTS idx_invite_activations_expires_at ON invite_activations (expires_at);

CREATE TABLE IF NOT EXISTS verification_codes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email CITEXT NOT NULL,
    purpose TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    device_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ip_address TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_verification_codes_email_purpose ON verification_codes (email, purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_verification_codes_device ON verification_codes (device_hash, created_at DESC);

CREATE TABLE IF NOT EXISTS user_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_hash TEXT NOT NULL,
    jwt_id UUID NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat_at TIMESTAMPTZ,
    total_active_seconds BIGINT NOT NULL DEFAULT 0,
    closed_at TIMESTAMPTZ,
    ip_address TEXT,
    user_agent TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_sessions_active ON user_sessions (closed_at);

CREATE TABLE IF NOT EXISTS daily_activity (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day DATE NOT NULL,
    active_seconds BIGINT NOT NULL DEFAULT 0,
    sessions_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);

CREATE TABLE IF NOT EXISTS conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    is_qgramm BOOLEAN NOT NULL DEFAULT FALSE,
    created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT conversations_kind_check CHECK (kind IN ('direct','group','channel','thread','system'))
);

CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversations_is_qgramm ON conversations (is_qgramm);

CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    role TEXT NOT NULL DEFAULT 'member',
    last_read_message_id UUID,
    unread_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (conversation_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_conversation_members_user ON conversation_members (user_id);

CREATE TABLE IF NOT EXISTS attachments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    storage_path TEXT NOT NULL,
    preview_path TEXT,
    file_name TEXT NOT NULL,
    mime_type TEXT,
    size_bytes BIGINT NOT NULL,
    duration_seconds INTEGER,
    checksum_sha256 TEXT,
    encryption_meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT attachments_kind_check CHECK (kind IN ('file','media','voice_note','circular_video','avatar')),
    CONSTRAINT attachments_size_check CHECK (size_bytes >= 0)
);

CREATE INDEX IF NOT EXISTS idx_attachments_owner ON attachments (owner_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS uploads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    file_name TEXT NOT NULL,
    mime_type TEXT,
    total_size BIGINT NOT NULL,
    chunk_size INTEGER NOT NULL,
    expected_chunks INTEGER NOT NULL,
    received_chunks INTEGER NOT NULL DEFAULT 0,
    received_bytes BIGINT NOT NULL DEFAULT 0,
    temp_dir TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'created',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finalized_attachment_id UUID REFERENCES attachments(id) ON DELETE SET NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uploads_kind_check CHECK (kind IN ('file','media','voice_note','circular_video','avatar')),
    CONSTRAINT uploads_status_check CHECK (status IN ('created','uploading','assembled','failed')),
    CONSTRAINT uploads_size_check CHECK (total_size >= 0),
    CONSTRAINT uploads_chunk_size_check CHECK (chunk_size > 0),
    CONSTRAINT uploads_expected_chunks_check CHECK (expected_chunks > 0)
);

CREATE INDEX IF NOT EXISTS idx_uploads_owner_status ON uploads (owner_user_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS upload_chunks (
    upload_id UUID NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    size_bytes INTEGER NOT NULL,
    checksum_sha256 TEXT,
    stored_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (upload_id, chunk_index),
    CONSTRAINT upload_chunks_size_check CHECK (size_bytes >= 0)
);

CREATE TABLE IF NOT EXISTS messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    message_type TEXT NOT NULL,
    body_ciphertext TEXT,
    body_nonce TEXT,
    body_tag TEXT,
    quoted_ciphertext TEXT,
    quoted_nonce TEXT,
    quoted_tag TEXT,
    reply_to_message_id UUID REFERENCES messages(id) ON DELETE SET NULL,
    forwarded_from_message_id UUID REFERENCES messages(id) ON DELETE SET NULL,
    forwarded_from_conversation_id UUID REFERENCES conversations(id) ON DELETE SET NULL,
    attachment_id UUID REFERENCES attachments(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    report_count INTEGER NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT messages_type_check CHECK (message_type IN ('text','voice_note','circular_video','media','file','system'))
);

CREATE INDEX IF NOT EXISTS idx_messages_conversation_created ON messages (conversation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages (sender_user_id, created_at DESC);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'conversation_members_last_read_fk'
    ) THEN
        ALTER TABLE conversation_members
            ADD CONSTRAINT conversation_members_last_read_fk
            FOREIGN KEY (last_read_message_id) REFERENCES messages(id) ON DELETE SET NULL;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    emoji TEXT NOT NULL,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, emoji, user_id)
);

CREATE TABLE IF NOT EXISTS reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    reporter_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    accused_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason TEXT NOT NULL,
    note TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    reviewed_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    review_note TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT reports_status_check CHECK (status IN ('pending','rejected','blocked')),
    CONSTRAINT reports_reason_check CHECK (reason IN ('spam','insult','virus','scam','other')),
    UNIQUE (message_id, reporter_user_id)
);

CREATE INDEX IF NOT EXISTS idx_reports_status ON reports (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reports_accused ON reports (accused_user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS invite_edges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inviter_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invite_id UUID NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (invitee_user_id)
);

CREATE INDEX IF NOT EXISTS idx_invite_edges_inviter ON invite_edges (inviter_user_id);

CREATE TABLE IF NOT EXISTS conversation_member_keys (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_ciphertext TEXT,
    key_nonce TEXT,
    key_tag TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (conversation_id, user_id)
);

CREATE TABLE IF NOT EXISTS calls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    caller_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    callee_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ,
    duration_seconds INTEGER,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT calls_kind_check CHECK (kind IN ('audio','video','group')),
    CONSTRAINT calls_status_check CHECK (status IN ('ringing','active','ended','missed','rejected'))
);

CREATE INDEX IF NOT EXISTS idx_calls_caller ON calls (caller_user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_calls_callee ON calls (callee_user_id, started_at DESC);

CREATE TABLE IF NOT EXISTS system_notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    read_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_system_notifications_user ON system_notifications (user_id, created_at DESC);

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
CREATE TRIGGER trg_users_updated_at
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_conversations_updated_at ON conversations;
CREATE TRIGGER trg_conversations_updated_at
BEFORE UPDATE ON conversations
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_uploads_updated_at ON uploads;
CREATE TRIGGER trg_uploads_updated_at
BEFORE UPDATE ON uploads
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();
