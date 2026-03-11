package services

import (
    "context"
    "fmt"

    "github.com/google/uuid"
)

type Identity struct {
    UserID    uuid.UUID
    SessionID uuid.UUID
    IsRoot    bool
}

func (s *Service) AuthenticateAccessToken(ctx context.Context, token string) (Identity, error) {
    claims, err := s.jwt.Parse(token)
    if err != nil {
        return Identity{}, fmt.Errorf("%w: invalid access token", ErrUnauthorized)
    }

    userID, err := uuid.Parse(claims.UserID)
    if err != nil {
        return Identity{}, fmt.Errorf("%w: invalid user in token", ErrUnauthorized)
    }
    sessionID, err := uuid.Parse(claims.SessionID)
    if err != nil {
        return Identity{}, fmt.Errorf("%w: invalid session in token", ErrUnauthorized)
    }

    var isRoot bool
    var isBanned bool
    var isSessionOpen bool

    err = s.pool.QueryRow(ctx,
        `SELECT u.is_root,
                u.is_banned,
                EXISTS (
                    SELECT 1 FROM user_sessions us
                    WHERE us.id = $2 AND us.user_id = $1 AND us.closed_at IS NULL
                )
         FROM users u
         WHERE u.id = $1`,
        userID,
        sessionID,
    ).Scan(&isRoot, &isBanned, &isSessionOpen)
    if err != nil {
        return Identity{}, fmt.Errorf("%w: session/user not found", ErrUnauthorized)
    }

    if !isSessionOpen || isBanned {
        return Identity{}, fmt.Errorf("%w: session is not active", ErrUnauthorized)
    }

    return Identity{
        UserID:    userID,
        SessionID: sessionID,
        IsRoot:    isRoot,
    }, nil
}
