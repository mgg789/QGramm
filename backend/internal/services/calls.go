package services

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
)

type CallView struct {
    ID            string         `json:"id"`
    CallerUserID  string         `json:"caller_user_id"`
    CalleeUserID  string         `json:"callee_user_id"`
    Kind          string         `json:"kind"`
    Status        string         `json:"status"`
    StartedAt     time.Time      `json:"started_at"`
    EndedAt       *time.Time     `json:"ended_at,omitempty"`
    Duration      *int           `json:"duration_seconds,omitempty"`
    Metadata      map[string]any `json:"metadata,omitempty"`
}

func (s *Service) StartCall(ctx context.Context, callerID uuid.UUID, in CallStartInput) (CallView, error) {
    calleeID, err := uuid.Parse(in.CalleeUserID)
    if err != nil {
        return CallView{}, fmt.Errorf("%w: invalid callee_user_id", ErrBadRequest)
    }
    if callerID == calleeID {
        return CallView{}, fmt.Errorf("%w: cannot call yourself", ErrBadRequest)
    }

    kind := in.Kind
    switch kind {
    case "audio", "video", "group":
    default:
        return CallView{}, fmt.Errorf("%w: invalid call kind", ErrBadRequest)
    }

    metaRaw, _ := json.Marshal(in.Metadata)
    callID := uuid.New()

    _, err = s.pool.Exec(ctx,
        `INSERT INTO calls (id, caller_user_id, callee_user_id, kind, status, metadata)
         VALUES ($1, $2, $3, $4, 'ringing', COALESCE($5::jsonb, '{}'::jsonb))`,
        callID,
        callerID,
        calleeID,
        kind,
        string(metaRaw),
    )
    if err != nil {
        return CallView{}, err
    }

    call, err := s.GetCallByID(ctx, callerID, callID)
    if err != nil {
        return CallView{}, err
    }

    s.hub.SendToUser(calleeID, realtimeEvent("call.incoming", map[string]any{
        "call": call,
    }))

    return call, nil
}

func (s *Service) EndCall(ctx context.Context, actorID uuid.UUID, callID uuid.UUID, in CallEndInput) (CallView, error) {
    status := in.Status
    switch status {
    case "ended", "missed", "rejected":
    default:
        status = "ended"
    }

    cmd, err := s.pool.Exec(ctx,
        `UPDATE calls
         SET status = $3,
             ended_at = NOW(),
             duration_seconds = CASE WHEN $4 > 0 THEN $4 ELSE duration_seconds END
         WHERE id = $1
           AND (caller_user_id = $2 OR callee_user_id = $2)
           AND ended_at IS NULL`,
        callID,
        actorID,
        status,
        in.Duration,
    )
    if err != nil {
        return CallView{}, err
    }
    if cmd.RowsAffected() == 0 {
        return CallView{}, ErrNotFound
    }

    call, err := s.GetCallByID(ctx, actorID, callID)
    if err != nil {
        return CallView{}, err
    }

    callerID, _ := uuid.Parse(call.CallerUserID)
    calleeID, _ := uuid.Parse(call.CalleeUserID)
    s.hub.BroadcastToUsers([]uuid.UUID{callerID, calleeID}, realtimeEvent("call.ended", map[string]any{
        "call": call,
    }))

    return call, nil
}

func (s *Service) GetCallByID(ctx context.Context, userID uuid.UUID, callID uuid.UUID) (CallView, error) {
    var out CallView
    var endedAt sql.NullTime
    var duration sql.NullInt32
    var metaRaw []byte

    err := s.pool.QueryRow(ctx,
        `SELECT id::text, caller_user_id::text, callee_user_id::text, kind, status,
                started_at, ended_at, duration_seconds, metadata
         FROM calls
         WHERE id = $1
           AND (caller_user_id = $2 OR callee_user_id = $2)`,
        callID,
        userID,
    ).Scan(
        &out.ID,
        &out.CallerUserID,
        &out.CalleeUserID,
        &out.Kind,
        &out.Status,
        &out.StartedAt,
        &endedAt,
        &duration,
        &metaRaw,
    )
    if err != nil {
        if err == pgx.ErrNoRows {
            return CallView{}, ErrNotFound
        }
        return CallView{}, err
    }

    out.EndedAt = nullableTime(endedAt)
    if duration.Valid {
        v := int(duration.Int32)
        out.Duration = &v
    }
    out.Metadata = parseJSONMap(metaRaw)

    return out, nil
}

func (s *Service) ListCalls(ctx context.Context, userID uuid.UUID, limit int) ([]CallView, error) {
    if limit <= 0 || limit > 200 {
        limit = 100
    }

    rows, err := s.pool.Query(ctx,
        `SELECT id::text, caller_user_id::text, callee_user_id::text, kind, status,
                started_at, ended_at, duration_seconds, metadata
         FROM calls
         WHERE caller_user_id = $1 OR callee_user_id = $1
         ORDER BY started_at DESC
         LIMIT $2`,
        userID,
        limit,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]CallView, 0, limit)
    for rows.Next() {
        var item CallView
        var endedAt sql.NullTime
        var duration sql.NullInt32
        var metaRaw []byte
        if scanErr := rows.Scan(
            &item.ID,
            &item.CallerUserID,
            &item.CalleeUserID,
            &item.Kind,
            &item.Status,
            &item.StartedAt,
            &endedAt,
            &duration,
            &metaRaw,
        ); scanErr != nil {
            return nil, scanErr
        }
        item.EndedAt = nullableTime(endedAt)
        if duration.Valid {
            v := int(duration.Int32)
            item.Duration = &v
        }
        item.Metadata = parseJSONMap(metaRaw)
        out = append(out, item)
    }

    return out, rows.Err()
}

func (s *Service) HandleRealtimeMessage(ctx context.Context, userID uuid.UUID, raw map[string]any) {
    kind, _ := raw["type"].(string)
    switch kind {
    case "call.signal":
        toUserID, _ := raw["to_user_id"].(string)
        if toUserID == "" {
            return
        }
        target, err := uuid.Parse(toUserID)
        if err != nil {
            return
        }

        payload := map[string]any{}
        if incoming, ok := raw["payload"].(map[string]any); ok {
            for k, v := range incoming {
                payload[k] = v
            }
        }
        payload["from_user_id"] = userID.String()

        s.hub.SendToUser(target, realtimeEvent("call.signal", payload))
    case "presence.ping":
        _, _ = s.pool.Exec(ctx,
            `UPDATE users SET last_seen_at = NOW() WHERE id = $1`,
            userID,
        )
    default:
        // ignore unknown events
    }
}
