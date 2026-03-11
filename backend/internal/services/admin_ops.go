package services

import (
    "context"
    "encoding/json"
    "fmt"
    "strings"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
)

type QGrammBroadcastInput struct {
    BodyCiphertext string         `json:"body_ciphertext"`
    BodyNonce      string         `json:"body_nonce"`
    BodyTag        string         `json:"body_tag"`
    Metadata       map[string]any `json:"metadata"`
}

type QGrammBroadcastResult struct {
    DeliveredTo int `json:"delivered_to"`
}

func (s *Service) BroadcastQGramm(ctx context.Context, adminID uuid.UUID, in QGrammBroadcastInput) (QGrammBroadcastResult, error) {
    var isRoot bool
    if err := s.pool.QueryRow(ctx, `SELECT is_root FROM users WHERE id = $1`, adminID).Scan(&isRoot); err != nil {
        return QGrammBroadcastResult{}, err
    }
    if !isRoot {
        return QGrammBroadcastResult{}, ErrForbidden
    }

    var qgrammID uuid.UUID
    if err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, s.cfg.Root.QGrammServiceEmail).Scan(&qgrammID); err != nil {
        return QGrammBroadcastResult{}, err
    }

    rows, err := s.pool.Query(ctx,
        `SELECT id
         FROM users
         WHERE is_banned = FALSE
           AND id <> $1`,
        qgrammID,
    )
    if err != nil {
        return QGrammBroadcastResult{}, err
    }
    defer rows.Close()

    delivered := 0
    for rows.Next() {
        var userID uuid.UUID
        if scanErr := rows.Scan(&userID); scanErr != nil {
            return QGrammBroadcastResult{}, scanErr
        }

        if userID == adminID || userID == qgrammID {
            continue
        }

        tx, txErr := s.pool.BeginTx(ctx, pgx.TxOptions{})
        if txErr != nil {
            return QGrammBroadcastResult{}, txErr
        }

        conversationID, ensureErr := s.ensureDirectConversationTx(ctx, tx, qgrammID, userID, "QGramm")
        if ensureErr != nil {
            _ = tx.Rollback(ctx)
            return QGrammBroadcastResult{}, ensureErr
        }

        metaRaw, _ := json.Marshal(in.Metadata)
        _, insertErr := tx.Exec(ctx,
            `INSERT INTO messages (
                id, conversation_id, sender_user_id, message_type,
                body_ciphertext, body_nonce, body_tag, metadata
             ) VALUES (
                $1, $2, $3, 'system',
                NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), COALESCE($7::jsonb,'{}'::jsonb)
             )`,
            uuid.New(),
            conversationID,
            qgrammID,
            strings.TrimSpace(in.BodyCiphertext),
            strings.TrimSpace(in.BodyNonce),
            strings.TrimSpace(in.BodyTag),
            string(metaRaw),
        )
        if insertErr != nil {
            _ = tx.Rollback(ctx)
            return QGrammBroadcastResult{}, insertErr
        }

        _, _ = tx.Exec(ctx,
            `UPDATE conversations SET updated_at = NOW() WHERE id = $1`,
            conversationID,
        )
        _, _ = tx.Exec(ctx,
            `UPDATE conversation_members
             SET unread_count = CASE WHEN user_id = $2 THEN unread_count ELSE unread_count + 1 END
             WHERE conversation_id = $1`,
            conversationID,
            qgrammID,
        )

        if commitErr := tx.Commit(ctx); commitErr != nil {
            return QGrammBroadcastResult{}, commitErr
        }

        delivered++

        s.hub.SendToUser(userID, realtimeEvent("qgramm.broadcast", map[string]any{
            "conversation_id": conversationID.String(),
            "from":            "@qgramm",
            "sent_at":         time.Now().UTC(),
        }))
    }

    if rowsErr := rows.Err(); rowsErr != nil {
        return QGrammBroadcastResult{}, rowsErr
    }

    return QGrammBroadcastResult{DeliveredTo: delivered}, nil
}

func (s *Service) EnsureRootAccess(ctx context.Context, userID uuid.UUID) error {
    var isRoot bool
    err := s.pool.QueryRow(ctx,
        `SELECT is_root FROM users WHERE id = $1`,
        userID,
    ).Scan(&isRoot)
    if err != nil {
        if err == pgx.ErrNoRows {
            return ErrNotFound
        }
        return err
    }
    if !isRoot {
        return fmt.Errorf("%w: root access required", ErrForbidden)
    }
    return nil
}
