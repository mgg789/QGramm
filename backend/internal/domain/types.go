package domain

import "time"

type APIError struct {
    Error string `json:"error"`
}

type AuthResponse struct {
    AccessToken string    `json:"access_token"`
    ExpiresAt   time.Time `json:"expires_at"`
    User        UserView  `json:"user"`
}

type UserView struct {
    ID            string    `json:"id"`
    UID           string    `json:"uid"`
    Email         string    `json:"email"`
    FirstName     string    `json:"first_name"`
    LastName      string    `json:"last_name"`
    Nickname      string    `json:"nickname"`
    TrustLevel    int       `json:"trust_level"`
    IsRoot        bool      `json:"is_root"`
    IsBanned      bool      `json:"is_banned"`
    InvitedByID   *string   `json:"invited_by_user_id,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
    LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
    AvatarURL     string    `json:"avatar_url,omitempty"`
}

type InviteView struct {
    ID         string    `json:"id"`
    Code       string    `json:"code"`
    InviterID  string    `json:"inviter_user_id"`
    CreatedAt  time.Time `json:"created_at"`
    ExpiresAt  time.Time `json:"expires_at"`
    MaxUses    int       `json:"max_uses"`
    UsedCount  int       `json:"used_count"`
    IsRevoked  bool      `json:"is_revoked"`
    IsActive   bool      `json:"is_active"`
}

type ConversationView struct {
    ID             string    `json:"id"`
    Kind           string    `json:"kind"`
    Title          string    `json:"title"`
    IsQGramm       bool      `json:"is_qgramm"`
    UpdatedAt      time.Time `json:"updated_at"`
    ParticipantIDs []string  `json:"participant_ids"`
    UnreadCount    int       `json:"unread_count"`
}

type MessageView struct {
    ID                        string     `json:"id"`
    ConversationID            string     `json:"conversation_id"`
    SenderUserID              *string    `json:"sender_user_id,omitempty"`
    MessageType               string     `json:"message_type"`
    BodyCiphertext            string     `json:"body_ciphertext,omitempty"`
    BodyNonce                 string     `json:"body_nonce,omitempty"`
    BodyTag                   string     `json:"body_tag,omitempty"`
    QuotedCiphertext          string     `json:"quoted_ciphertext,omitempty"`
    QuotedNonce               string     `json:"quoted_nonce,omitempty"`
    QuotedTag                 string     `json:"quoted_tag,omitempty"`
    ReplyToMessageID          *string    `json:"reply_to_message_id,omitempty"`
    ForwardedFromMessageID    *string    `json:"forwarded_from_message_id,omitempty"`
    ForwardedFromConversationID *string  `json:"forwarded_from_conversation_id,omitempty"`
    AttachmentID              *string    `json:"attachment_id,omitempty"`
    CreatedAt                 time.Time  `json:"created_at"`
    ReportCount               int        `json:"report_count"`
    Metadata                  any        `json:"metadata,omitempty"`
}

type UploadView struct {
    ID              string    `json:"id"`
    OwnerUserID     string    `json:"owner_user_id"`
    Kind            string    `json:"kind"`
    FileName        string    `json:"file_name"`
    MimeType        string    `json:"mime_type,omitempty"`
    TotalSize       int64     `json:"total_size"`
    ChunkSize       int       `json:"chunk_size"`
    ExpectedChunks  int       `json:"expected_chunks"`
    ReceivedChunks  int       `json:"received_chunks"`
    ReceivedBytes   int64     `json:"received_bytes"`
    Status          string    `json:"status"`
    AttachmentID    *string   `json:"finalized_attachment_id,omitempty"`
    CreatedAt       time.Time `json:"created_at"`
    UpdatedAt       time.Time `json:"updated_at"`
}
