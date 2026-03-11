package services

import "time"

type ActivateInviteInput struct {
    Code             string
    DeviceFingerprint string
}

type DeviceActivationStatus struct {
    Allowed         bool      `json:"allowed"`
    InviteCode      string    `json:"invite_code,omitempty"`
    ActivatedAt     time.Time `json:"activated_at,omitempty"`
    ExpiresAt       time.Time `json:"expires_at,omitempty"`
}

type SendCodeInput struct {
    Email             string
    Purpose           string
    CaptchaToken      string
    DeviceFingerprint string
    IPAddress         string
}

type SendCodeResult struct {
    ExpiresAt time.Time `json:"expires_at"`
    Delivery  string    `json:"delivery"`
    DebugCode string    `json:"debug_code,omitempty"`
}

type RegisterInput struct {
    Email             string
    VerificationCode  string
    DeviceFingerprint string
    FirstName         string
    LastName          string
    Nickname          string
    RecoveryCiphertext string
    RecoveryMeta      map[string]any
}

type LoginInput struct {
    Email             string
    VerificationCode  string
    DeviceFingerprint string
    IPAddress         string
    UserAgent         string
}

type AuthUser struct {
    ID          string     `json:"id"`
    UID         string     `json:"uid"`
    Email       string     `json:"email"`
    FirstName   string     `json:"first_name"`
    LastName    string     `json:"last_name"`
    Nickname    string     `json:"nickname"`
    TrustLevel  int        `json:"trust_level"`
    IsRoot      bool       `json:"is_root"`
    IsBanned    bool       `json:"is_banned"`
    InvitedByID *string    `json:"invited_by_user_id,omitempty"`
    CreatedAt   time.Time  `json:"created_at"`
    LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
    AvatarURL   string     `json:"avatar_url,omitempty"`
}

type AuthResult struct {
    AccessToken string    `json:"access_token"`
    ExpiresAt   time.Time `json:"expires_at"`
    User        AuthUser  `json:"user"`
}

type UserUpdateInput struct {
    FirstName string
    LastName  string
    Nickname  string
    AvatarPath string
    Metadata  map[string]any
}

type RecoveryBundle struct {
    Ciphertext string         `json:"ciphertext"`
    Meta       map[string]any `json:"meta"`
}

type CreateInviteInput struct {
    ExpiresInHours int `json:"expires_in_hours"`
    MaxUses        int `json:"max_uses"`
}

type CreateUploadInput struct {
    Kind      string `json:"kind"`
    FileName  string `json:"file_name"`
    MimeType  string `json:"mime_type"`
    TotalSize int64  `json:"total_size"`
    ChunkSize int    `json:"chunk_size"`
}

type UploadChunkInput struct {
    UploadID   string
    ChunkIndex int
    Payload    []byte
}

type CompleteUploadInput struct {
    UploadID string
}

type SendMessageInput struct {
    ConversationID              string         `json:"conversation_id"`
    MessageType                 string         `json:"message_type"`
    BodyCiphertext              string         `json:"body_ciphertext"`
    BodyNonce                   string         `json:"body_nonce"`
    BodyTag                     string         `json:"body_tag"`
    QuotedCiphertext            string         `json:"quoted_ciphertext"`
    QuotedNonce                 string         `json:"quoted_nonce"`
    QuotedTag                   string         `json:"quoted_tag"`
    ReplyToMessageID            *string        `json:"reply_to_message_id"`
    ForwardedFromMessageID      *string        `json:"forwarded_from_message_id"`
    ForwardedFromConversationID *string        `json:"forwarded_from_conversation_id"`
    AttachmentID                *string        `json:"attachment_id"`
    Metadata                    map[string]any `json:"metadata"`
}

type ReportInput struct {
    MessageID      string `json:"message_id"`
    ConversationID string `json:"conversation_id"`
    Reason         string `json:"reason"`
    Note           string `json:"note"`
}

type CallStartInput struct {
    CalleeUserID string `json:"callee_user_id"`
    Kind         string `json:"kind"`
    Metadata     map[string]any `json:"metadata"`
}

type CallEndInput struct {
    Status   string `json:"status"`
    Duration int    `json:"duration_seconds"`
}

type HeartbeatInput struct {
    ActiveSeconds int `json:"active_seconds"`
}
