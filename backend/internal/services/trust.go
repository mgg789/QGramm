package services

import (
    "context"
    "database/sql"
    "fmt"
    "time"

    "github.com/google/uuid"
)

func (s *Service) RecalculateTrustLevel(ctx context.Context, userID uuid.UUID) error {
    var (
        createdAt     time.Time
        tgVerifiedAt  sql.NullTime
        isBanned      bool
        overrideLevel sql.NullInt16
    )

    err := s.pool.QueryRow(ctx,
        `SELECT created_at, tg_verified_at, is_banned, trust_level_override
         FROM users
         WHERE id = $1`,
        userID,
    ).Scan(&createdAt, &tgVerifiedAt, &isBanned, &overrideLevel)
    if err != nil {
        return err
    }

    if isBanned {
        _, _ = s.pool.Exec(ctx,
            `UPDATE users SET trust_level = 1, suspicious_score = suspicious_score + 1, updated_at = NOW() WHERE id = $1`,
            userID,
        )
        return nil
    }

    if overrideLevel.Valid {
        _, _ = s.pool.Exec(ctx,
            `UPDATE users SET trust_level = $2, updated_at = NOW() WHERE id = $1`,
            userID,
            clampTrustLevel(int(overrideLevel.Int16)),
        )
        return nil
    }

    now := time.Now().UTC()
    accountAgeDays := int(now.Sub(createdAt).Hours() / 24)

    activeDays, err := s.activeDaysCount(ctx, userID, s.cfg.Runtime.Trust.QualifiedDayMinActiveSecs)
    if err != nil {
        return err
    }

    reports14, err := s.reportCountInDays(ctx, userID, 14)
    if err != nil {
        return err
    }

    reports60, err := s.reportCountInDays(ctx, userID, 60)
    if err != nil {
        return err
    }

    uniqueRecipientsHour, err := s.uniqueRecipientsInLastHour(ctx, userID)
    if err != nil {
        return err
    }
    suspicious := uniqueRecipientsHour > s.cfg.Limits.MaxUniqueRecipientsPerHour

    level := 1

    if tgVerifiedAt.Valid &&
        accountAgeDays >= s.cfg.Runtime.Trust.Level2MinAccountAgeDays &&
        activeDays >= s.cfg.Runtime.Trust.Level2MinActiveDays {
        level = 2
    }

    if accountAgeDays >= s.cfg.Runtime.Trust.Level3MinAccountAgeDays &&
        reports14 == 0 &&
        !suspicious {
        if level < 3 {
            level = 3
        }
    }

    if activeDays >= s.cfg.Runtime.Trust.Level4MinQualifiedDays {
        if level < 4 {
            level = 4
        }
    }

    if accountAgeDays >= s.cfg.Runtime.Trust.Level5MinAccountAgeDays &&
        reports60 == 0 &&
        activeDays >= s.cfg.Runtime.Trust.Level5MinActiveDays {
        level = 5
    }

    suspiciousScore := 0
    if suspicious {
        suspiciousScore = 1
    }

    _, err = s.pool.Exec(ctx,
        `UPDATE users
         SET trust_level = $2,
             active_days_count = $3,
             suspicious_score = $4,
             updated_at = NOW()
         WHERE id = $1`,
        userID,
        level,
        activeDays,
        suspiciousScore,
    )
    if err != nil {
        return err
    }

    return nil
}

func (s *Service) activeDaysCount(ctx context.Context, userID uuid.UUID, minSeconds int) (int, error) {
    if minSeconds <= 0 {
        minSeconds = 300
    }

    var count int
    err := s.pool.QueryRow(ctx,
        `SELECT COUNT(*)
         FROM daily_activity
         WHERE user_id = $1 AND active_seconds >= $2`,
        userID,
        minSeconds,
    ).Scan(&count)
    if err != nil {
        return 0, err
    }
    return count, nil
}

func (s *Service) reportCountInDays(ctx context.Context, userID uuid.UUID, days int) (int, error) {
    if days <= 0 {
        return 0, fmt.Errorf("days must be > 0")
    }

    var count int
    err := s.pool.QueryRow(ctx,
        `SELECT COUNT(*)
         FROM reports
         WHERE accused_user_id = $1
           AND created_at >= NOW() - ($2 || ' days')::interval`,
        userID,
        days,
    ).Scan(&count)
    if err != nil {
        return 0, err
    }
    return count, nil
}

func (s *Service) uniqueRecipientsInLastHour(ctx context.Context, userID uuid.UUID) (int, error) {
    var count int
    err := s.pool.QueryRow(ctx,
        `SELECT COUNT(DISTINCT cm.user_id)
         FROM messages m
         JOIN conversations c ON c.id = m.conversation_id
         JOIN conversation_members cm ON cm.conversation_id = c.id
         WHERE m.sender_user_id = $1
           AND m.created_at >= NOW() - INTERVAL '1 hour'
           AND c.kind = 'direct'
           AND cm.user_id <> $1`,
        userID,
    ).Scan(&count)
    if err != nil {
        return 0, err
    }
    return count, nil
}

func clampTrustLevel(level int) int {
    if level < 1 {
        return 1
    }
    if level > 5 {
        return 5
    }
    return level
}

func (s *Service) SetTelegramVerified(ctx context.Context, userID uuid.UUID, verified bool) error {
    if verified {
        _, err := s.pool.Exec(ctx,
            `UPDATE users SET tg_verified_at = NOW(), updated_at = NOW() WHERE id = $1`,
            userID,
        )
        if err != nil {
            return err
        }
    } else {
        _, err := s.pool.Exec(ctx,
            `UPDATE users SET tg_verified_at = NULL, updated_at = NOW() WHERE id = $1`,
            userID,
        )
        if err != nil {
            return err
        }
    }

    return s.RecalculateTrustLevel(ctx, userID)
}
