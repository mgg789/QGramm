package services

import (
    "context"
    "database/sql"
    "fmt"
    "strings"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
)

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

type InviteEdgeNode struct {
    UserID        string `json:"user_id"`
    UID           string `json:"uid"`
    Nickname      string `json:"nickname"`
    InvitedByID   *string `json:"invited_by_user_id,omitempty"`
    InviteID      *string `json:"invite_id,omitempty"`
    CreatedAt     time.Time `json:"created_at"`
    IsBanned      bool   `json:"is_banned"`
    TrustLevel    int    `json:"trust_level"`
}

func (s *Service) CreateInvite(ctx context.Context, userID uuid.UUID, in CreateInviteInput) (InviteView, error) {
    user, err := s.GetUserByID(ctx, userID)
    if err != nil {
        return InviteView{}, err
    }

    if user.IsBanned {
        return InviteView{}, fmt.Errorf("%w: banned users cannot create invites", ErrForbidden)
    }

    weeklyLimit := s.weeklyInviteLimit(user.TrustLevel, user.IsRoot)
    if weeklyLimit >= 0 {
        usedCount, usedErr := s.usedInvitesThisWeek(ctx, userID)
        if usedErr != nil {
            return InviteView{}, usedErr
        }
        if usedCount >= weeklyLimit {
            return InviteView{}, fmt.Errorf("%w: weekly invite limit reached", ErrForbidden)
        }
    }

    maxUses := in.MaxUses
    if maxUses <= 0 {
        maxUses = 1
    }
    if !user.IsRoot && maxUses > 1 {
        return InviteView{}, fmt.Errorf("%w: non-root invites are single-use", ErrForbidden)
    }

    expiresAt := time.Now().UTC().Add(time.Duration(s.cfg.Runtime.Invite.InviteLifetimeHours) * time.Hour)
    if in.ExpiresInHours > 0 && user.IsRoot {
        expiresAt = time.Now().UTC().Add(time.Duration(in.ExpiresInHours) * time.Hour)
    }

    var inviteID uuid.UUID
    var code string
    for attempt := 0; attempt < 5; attempt++ {
        inviteID = uuid.New()
        code = randomInviteCode()
        _, err = s.pool.Exec(ctx,
            `INSERT INTO invites (id, code, inviter_user_id, created_at, expires_at, max_uses)
             VALUES ($1, $2, $3, NOW(), $4, $5)`,
            inviteID,
            code,
            userID,
            expiresAt,
            maxUses,
        )
        if err == nil {
            break
        }
        if !strings.Contains(strings.ToLower(err.Error()), "invites_code_key") {
            return InviteView{}, err
        }
    }
    if err != nil {
        return InviteView{}, err
    }

    return InviteView{
        ID:        inviteID.String(),
        Code:      code,
        InviterID: userID.String(),
        CreatedAt: time.Now().UTC(),
        ExpiresAt: expiresAt,
        MaxUses:   maxUses,
        UsedCount: 0,
        IsRevoked: false,
        IsActive:  true,
    }, nil
}

func (s *Service) ListInvites(ctx context.Context, userID uuid.UUID, includeRevoked bool) ([]InviteView, error) {
    query := `
SELECT id::text, code, inviter_user_id::text, created_at, expires_at, max_uses, used_count, is_revoked,
       (NOT is_revoked AND expires_at > NOW() AND used_count < max_uses) AS is_active
FROM invites
WHERE inviter_user_id = $1
`
    if !includeRevoked {
        query += "AND is_revoked = FALSE\n"
    }
    query += `ORDER BY created_at DESC`

    rows, err := s.pool.Query(ctx, query, userID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]InviteView, 0)
    for rows.Next() {
        var item InviteView
        if scanErr := rows.Scan(
            &item.ID,
            &item.Code,
            &item.InviterID,
            &item.CreatedAt,
            &item.ExpiresAt,
            &item.MaxUses,
            &item.UsedCount,
            &item.IsRevoked,
            &item.IsActive,
        ); scanErr != nil {
            return nil, scanErr
        }
        out = append(out, item)
    }

    return out, rows.Err()
}

func (s *Service) GetInviteGraph(ctx context.Context) ([]InviteEdgeNode, error) {
    rows, err := s.pool.Query(ctx,
        `SELECT u.id::text,
                u.uid,
                u.nickname::text,
                u.invited_by_user_id::text,
                ie.invite_id::text,
                u.created_at,
                u.is_banned,
                u.trust_level
         FROM users u
         LEFT JOIN invite_edges ie ON ie.invitee_user_id = u.id
         ORDER BY u.created_at ASC`,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    nodes := make([]InviteEdgeNode, 0)
    for rows.Next() {
        var n InviteEdgeNode
        var invitedBy, inviteID sql.NullString
        if scanErr := rows.Scan(
            &n.UserID,
            &n.UID,
            &n.Nickname,
            &invitedBy,
            &inviteID,
            &n.CreatedAt,
            &n.IsBanned,
            &n.TrustLevel,
        ); scanErr != nil {
            return nil, scanErr
        }
        if invitedBy.Valid {
            v := invitedBy.String
            n.InvitedByID = &v
        }
        if inviteID.Valid {
            v := inviteID.String
            n.InviteID = &v
        }
        nodes = append(nodes, n)
    }

    return nodes, rows.Err()
}

func (s *Service) usedInvitesThisWeek(ctx context.Context, userID uuid.UUID) (int, error) {
    var count int
    err := s.pool.QueryRow(ctx,
        `SELECT COUNT(*)
         FROM invite_edges
         WHERE inviter_user_id = $1
           AND created_at >= date_trunc('week', NOW())`,
        userID,
    ).Scan(&count)
    if err != nil {
        return 0, err
    }
    return count, nil
}

func (s *Service) weeklyInviteLimit(trustLevel int, isRoot bool) int {
    if isRoot {
        return -1
    }
    if limit, ok := s.cfg.Limits.InviteWeeklyLimits[trustLevel]; ok {
        return limit
    }
    return 0
}

func (s *Service) RevokeInviteAdmin(ctx context.Context, inviteID uuid.UUID) error {
    cmd, err := s.pool.Exec(ctx,
        `UPDATE invites SET is_revoked = TRUE WHERE id = $1`,
        inviteID,
    )
    if err != nil {
        return err
    }
    if cmd.RowsAffected() == 0 {
        return ErrNotFound
    }
    return nil
}

func (s *Service) GetInviterForUser(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
    var inviterID uuid.UUID
    err := s.pool.QueryRow(ctx,
        `SELECT invited_by_user_id FROM users WHERE id = $1`,
        userID,
    ).Scan(&inviterID)
    if err != nil {
        if err == pgx.ErrNoRows {
            return uuid.Nil, ErrNotFound
        }
        return uuid.Nil, err
    }
    return inviterID, nil
}
