package services

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/google/uuid"
)

type NotificationView struct {
    ID        string         `json:"id"`
    Kind      string         `json:"kind"`
    Payload   map[string]any `json:"payload"`
    CreatedAt time.Time      `json:"created_at"`
    ReadAt    *time.Time     `json:"read_at,omitempty"`
}

func (s *Service) CreateNotification(ctx context.Context, userID uuid.UUID, kind string, payload map[string]any) error {
    payloadRaw, _ := json.Marshal(payload)
    _, err := s.pool.Exec(ctx,
        `INSERT INTO system_notifications (id, user_id, kind, payload)
         VALUES ($1, $2, $3, COALESCE($4::jsonb, '{}'::jsonb))`,
        uuid.New(),
        userID,
        kind,
        string(payloadRaw),
    )
    return err
}

func (s *Service) ListNotifications(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit int) ([]NotificationView, error) {
    if limit <= 0 || limit > 200 {
        limit = 100
    }

    query := `
SELECT id::text, kind, payload, created_at, read_at
FROM system_notifications
WHERE user_id = $1
`
    args := []any{userID}
    if unreadOnly {
        query += `AND read_at IS NULL\n`
    }
    query += `ORDER BY created_at DESC LIMIT ` + fmt.Sprintf("%d", limit)

    rows, err := s.pool.Query(ctx, query, args...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]NotificationView, 0, limit)
    for rows.Next() {
        var item NotificationView
        var payloadRaw []byte
        var readAt sql.NullTime
        if scanErr := rows.Scan(
            &item.ID,
            &item.Kind,
            &payloadRaw,
            &item.CreatedAt,
            &readAt,
        ); scanErr != nil {
            return nil, scanErr
        }

        item.Payload = parseJSONMap(payloadRaw)
        item.ReadAt = nullableTime(readAt)
        out = append(out, item)
    }

    return out, rows.Err()
}

func (s *Service) MarkNotificationRead(ctx context.Context, userID uuid.UUID, notificationID uuid.UUID) error {
    cmd, err := s.pool.Exec(ctx,
        `UPDATE system_notifications
         SET read_at = NOW()
         WHERE id = $1 AND user_id = $2`,
        notificationID,
        userID,
    )
    if err != nil {
        return err
    }
    if cmd.RowsAffected() == 0 {
        return ErrNotFound
    }
    return nil
}
