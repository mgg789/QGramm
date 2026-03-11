package services

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "strings"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
)

func (s *Service) GetUserByID(ctx context.Context, userID uuid.UUID) (AuthUser, error) {
    var user AuthUser
    var invitedBy sql.NullString
    var lastSeen sql.NullTime
    err := s.pool.QueryRow(ctx,
        `SELECT id, uid, email, first_name, last_name, nickname,
                trust_level, is_root, is_banned, invited_by_user_id::text,
                created_at, last_seen_at, COALESCE(avatar_path, '')
         FROM users
         WHERE id = $1`,
        userID,
    ).Scan(
        &user.ID,
        &user.UID,
        &user.Email,
        &user.FirstName,
        &user.LastName,
        &user.Nickname,
        &user.TrustLevel,
        &user.IsRoot,
        &user.IsBanned,
        &invitedBy,
        &user.CreatedAt,
        &lastSeen,
        &user.AvatarURL,
    )
    if err != nil {
        if err == pgx.ErrNoRows {
            return AuthUser{}, ErrNotFound
        }
        return AuthUser{}, err
    }

    if invitedBy.Valid {
        value := invitedBy.String
        user.InvitedByID = &value
    }
    user.LastSeenAt = nullableTime(lastSeen)

    return user, nil
}

func (s *Service) GetUserByNickname(ctx context.Context, nickname string) (AuthUser, error) {
    normalized := normalizeNickname(nickname)
    if normalized == "" {
        return AuthUser{}, fmt.Errorf("%w: nickname is required", ErrBadRequest)
    }

    var userID uuid.UUID
    err := s.pool.QueryRow(ctx,
        `SELECT id FROM users WHERE LOWER(nickname::text) = LOWER($1)`,
        normalized,
    ).Scan(&userID)
    if err != nil {
        if err == pgx.ErrNoRows {
            return AuthUser{}, ErrNotFound
        }
        return AuthUser{}, err
    }

    return s.GetUserByID(ctx, userID)
}

func (s *Service) UpdateUserProfile(ctx context.Context, userID uuid.UUID, in UserUpdateInput) (AuthUser, error) {
    firstName := strings.TrimSpace(in.FirstName)
    lastName := strings.TrimSpace(in.LastName)
    nickname := normalizeNickname(in.Nickname)

    if firstName == "" || lastName == "" || nickname == "" {
        return AuthUser{}, fmt.Errorf("%w: first_name, last_name and nickname are required", ErrBadRequest)
    }

    metadataBytes, _ := json.Marshal(in.Metadata)

    cmd, err := s.pool.Exec(ctx,
        `UPDATE users
         SET first_name = $2,
             last_name = $3,
             nickname = $4,
             avatar_path = CASE WHEN $5 = '' THEN avatar_path ELSE $5 END,
             metadata = COALESCE(metadata, '{}'::jsonb) || COALESCE($6::jsonb, '{}'::jsonb),
             updated_at = NOW()
         WHERE id = $1`,
        userID,
        firstName,
        lastName,
        nickname,
        strings.TrimSpace(in.AvatarPath),
        string(metadataBytes),
    )
    if err != nil {
        if strings.Contains(strings.ToLower(err.Error()), "idx_users_nickname_unique") {
            return AuthUser{}, fmt.Errorf("%w: nickname is already in use", ErrBadRequest)
        }
        return AuthUser{}, err
    }
    if cmd.RowsAffected() == 0 {
        return AuthUser{}, ErrNotFound
    }

    return s.GetUserByID(ctx, userID)
}

func (s *Service) SetRecoveryBundle(ctx context.Context, userID uuid.UUID, bundle RecoveryBundle) error {
    if strings.TrimSpace(bundle.Ciphertext) == "" {
        return fmt.Errorf("%w: ciphertext is required", ErrBadRequest)
    }

    metaBytes, _ := json.Marshal(bundle.Meta)
    _, err := s.pool.Exec(ctx,
        `UPDATE users
         SET recovery_bundle_ciphertext = $2,
             recovery_bundle_meta = COALESCE($3::jsonb, '{}'::jsonb),
             updated_at = NOW()
         WHERE id = $1`,
        userID,
        strings.TrimSpace(bundle.Ciphertext),
        string(metaBytes),
    )
    return err
}

func (s *Service) GetRecoveryBundle(ctx context.Context, userID uuid.UUID) (RecoveryBundle, error) {
    var ciphertext string
    var metaRaw []byte
    err := s.pool.QueryRow(ctx,
        `SELECT COALESCE(recovery_bundle_ciphertext, ''), COALESCE(recovery_bundle_meta, '{}'::jsonb)
         FROM users
         WHERE id = $1`,
        userID,
    ).Scan(&ciphertext, &metaRaw)
    if err != nil {
        if err == pgx.ErrNoRows {
            return RecoveryBundle{}, ErrNotFound
        }
        return RecoveryBundle{}, err
    }

    return RecoveryBundle{
        Ciphertext: ciphertext,
        Meta:       parseJSONMap(metaRaw),
    }, nil
}

type AdminUserView struct {
    ID             string     `json:"id"`
    UID            string     `json:"uid"`
    Email          string     `json:"email"`
    Nickname       string     `json:"nickname"`
    FirstName      string     `json:"first_name"`
    LastName       string     `json:"last_name"`
    TrustLevel     int        `json:"trust_level"`
    IsRoot         bool       `json:"is_root"`
    IsBanned       bool       `json:"is_banned"`
    InvitedByUserID *string   `json:"invited_by_user_id,omitempty"`
    CreatedAt      time.Time  `json:"created_at"`
}

func (s *Service) ListUsersAdmin(ctx context.Context, limit int) ([]AdminUserView, error) {
    if limit <= 0 || limit > 500 {
        limit = 200
    }

    rows, err := s.pool.Query(ctx,
        `SELECT id::text, uid, email::text, nickname::text, first_name, last_name,
                trust_level, is_root, is_banned, invited_by_user_id::text, created_at
         FROM users
         ORDER BY created_at DESC
         LIMIT $1`,
        limit,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]AdminUserView, 0, limit)
    for rows.Next() {
        var item AdminUserView
        var invitedBy sql.NullString
        if scanErr := rows.Scan(
            &item.ID,
            &item.UID,
            &item.Email,
            &item.Nickname,
            &item.FirstName,
            &item.LastName,
            &item.TrustLevel,
            &item.IsRoot,
            &item.IsBanned,
            &invitedBy,
            &item.CreatedAt,
        ); scanErr != nil {
            return nil, scanErr
        }
        if invitedBy.Valid {
            v := invitedBy.String
            item.InvitedByUserID = &v
        }
        out = append(out, item)
    }

    return out, rows.Err()
}

func (s *Service) SetUserBlocked(ctx context.Context, targetUserID uuid.UUID, blocked bool) error {
    cmd, err := s.pool.Exec(ctx,
        `UPDATE users SET is_banned = $2, updated_at = NOW() WHERE id = $1`,
        targetUserID, blocked,
    )
    if err != nil {
        return err
    }
    if cmd.RowsAffected() == 0 {
        return ErrNotFound
    }

    if blocked {
        _ = s.BanInviteBranch(ctx, targetUserID)
    }

    return nil
}

func (s *Service) SetUserTrustAdmin(ctx context.Context, targetUserID uuid.UUID, trustLevel int) error {
    if trustLevel < 1 || trustLevel > 5 {
        return fmt.Errorf("%w: trust level must be between 1 and 5", ErrBadRequest)
    }

    cmd, err := s.pool.Exec(ctx,
        `UPDATE users
         SET trust_level = $2,
             trust_level_override = $2,
             updated_at = NOW()
         WHERE id = $1`,
        targetUserID,
        trustLevel,
    )
    if err != nil {
        return err
    }
    if cmd.RowsAffected() == 0 {
        return ErrNotFound
    }

    return nil
}

func (s *Service) BanInviteBranch(ctx context.Context, rootUserID uuid.UUID) error {
    query := `
WITH RECURSIVE branch AS (
    SELECT id FROM users WHERE id = $1
    UNION ALL
    SELECT u.id
    FROM users u
    JOIN branch b ON u.invited_by_user_id = b.id
)
UPDATE users
SET is_banned = TRUE,
    updated_at = NOW()
WHERE id IN (SELECT id FROM branch)
`

    _, err := s.pool.Exec(ctx, query, rootUserID)
    return err
}
