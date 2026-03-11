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

type ReportView struct {
    ID             string     `json:"id"`
    MessageID      string     `json:"message_id"`
    ConversationID string     `json:"conversation_id"`
    ReporterUserID string     `json:"reporter_user_id"`
    AccusedUserID  string     `json:"accused_user_id"`
    Reason         string     `json:"reason"`
    Note           string     `json:"note,omitempty"`
    Status         string     `json:"status"`
    CreatedAt      time.Time  `json:"created_at"`
    ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
    ReviewedByID   *string    `json:"reviewed_by_user_id,omitempty"`
    ReviewNote     string     `json:"review_note,omitempty"`
}

func (s *Service) CreateReport(ctx context.Context, reporterID uuid.UUID, in ReportInput) (ReportView, error) {
    messageID, err := uuid.Parse(in.MessageID)
    if err != nil {
        return ReportView{}, fmt.Errorf("%w: invalid message_id", ErrBadRequest)
    }
    conversationID, err := uuid.Parse(in.ConversationID)
    if err != nil {
        return ReportView{}, fmt.Errorf("%w: invalid conversation_id", ErrBadRequest)
    }

    reason := stringsToReportReason(in.Reason)
    if reason == "" {
        return ReportView{}, fmt.Errorf("%w: invalid reason", ErrBadRequest)
    }

    if err = s.ensureConversationMembership(ctx, reporterID, conversationID); err != nil {
        return ReportView{}, err
    }

    var accusedRaw sql.NullString
    err = s.pool.QueryRow(ctx,
        `SELECT sender_user_id
         FROM messages
         WHERE id = $1 AND conversation_id = $2`,
        messageID,
        conversationID,
    ).Scan(&accusedRaw)
    if err != nil {
        if err == pgx.ErrNoRows {
            return ReportView{}, ErrNotFound
        }
        return ReportView{}, err
    }

    if !accusedRaw.Valid {
        return ReportView{}, fmt.Errorf("%w: system message cannot be reported", ErrBadRequest)
    }

    accusedID, parseErr := uuid.Parse(accusedRaw.String)
    if parseErr != nil {
        return ReportView{}, parseErr
    }

    if accusedID == reporterID {
        return ReportView{}, fmt.Errorf("%w: cannot report your own message", ErrBadRequest)
    }

    reportID := uuid.New()
    _, err = s.pool.Exec(ctx,
        `INSERT INTO reports (id, message_id, conversation_id, reporter_user_id, accused_user_id, reason, note)
         VALUES ($1,$2,$3,$4,$5,$6,$7)`,
        reportID,
        messageID,
        conversationID,
        reporterID,
        accusedID,
        reason,
        in.Note,
    )
    if err != nil {
        if stringsContainsFold(err.Error(), "reports_message_id_reporter_user_id_key") {
            return ReportView{}, fmt.Errorf("%w: report already exists", ErrBadRequest)
        }
        return ReportView{}, err
    }

    _, _ = s.pool.Exec(ctx,
        `UPDATE messages SET report_count = report_count + 1 WHERE id = $1`,
        messageID,
    )

    _ = s.RecalculateTrustLevel(ctx, accusedID)

    report, err := s.GetReportByID(ctx, reportID)
    if err != nil {
        return ReportView{}, err
    }

    roots, rootErr := s.rootUserIDs(ctx)
    if rootErr == nil {
        event := realtimeEvent("report.created", map[string]any{
            "report": report,
            "actions": map[string]any{
                "reject": fmt.Sprintf("/v1/admin/reports/%s/reject", report.ID),
                "block":  fmt.Sprintf("/v1/admin/reports/%s/block", report.ID),
            },
        })
        s.hub.BroadcastToUsers(roots, event)
    }

    return report, nil
}

func (s *Service) GetReportByID(ctx context.Context, reportID uuid.UUID) (ReportView, error) {
    var out ReportView
    var reviewedAt sql.NullTime
    var reviewedBy sql.NullString

    err := s.pool.QueryRow(ctx,
        `SELECT id::text, message_id::text, conversation_id::text,
                reporter_user_id::text, accused_user_id::text,
                reason, COALESCE(note,''), status, created_at,
                reviewed_at, reviewed_by_user_id::text, COALESCE(review_note,'')
         FROM reports
         WHERE id = $1`,
        reportID,
    ).Scan(
        &out.ID,
        &out.MessageID,
        &out.ConversationID,
        &out.ReporterUserID,
        &out.AccusedUserID,
        &out.Reason,
        &out.Note,
        &out.Status,
        &out.CreatedAt,
        &reviewedAt,
        &reviewedBy,
        &out.ReviewNote,
    )
    if err != nil {
        if err == pgx.ErrNoRows {
            return ReportView{}, ErrNotFound
        }
        return ReportView{}, err
    }

    out.ReviewedAt = nullableTime(reviewedAt)
    if reviewedBy.Valid {
        v := reviewedBy.String
        out.ReviewedByID = &v
    }

    return out, nil
}

func (s *Service) ListReportsAdmin(ctx context.Context, status string, limit int) ([]ReportView, error) {
    if limit <= 0 || limit > 500 {
        limit = 200
    }

    status = stringsToReportStatus(status)
    query := `
SELECT id::text, message_id::text, conversation_id::text,
       reporter_user_id::text, accused_user_id::text,
       reason, COALESCE(note,''), status, created_at,
       reviewed_at, reviewed_by_user_id::text, COALESCE(review_note,'')
FROM reports
`
    args := []any{}
    if status != "" {
        query += `WHERE status = $1\n`
        args = append(args, status)
    }
    query += `ORDER BY created_at DESC LIMIT ` + fmt.Sprintf("%d", limit)

    rows, err := s.pool.Query(ctx, query, args...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]ReportView, 0, limit)
    for rows.Next() {
        var item ReportView
        var reviewedAt sql.NullTime
        var reviewedBy sql.NullString
        if scanErr := rows.Scan(
            &item.ID,
            &item.MessageID,
            &item.ConversationID,
            &item.ReporterUserID,
            &item.AccusedUserID,
            &item.Reason,
            &item.Note,
            &item.Status,
            &item.CreatedAt,
            &reviewedAt,
            &reviewedBy,
            &item.ReviewNote,
        ); scanErr != nil {
            return nil, scanErr
        }

        item.ReviewedAt = nullableTime(reviewedAt)
        if reviewedBy.Valid {
            v := reviewedBy.String
            item.ReviewedByID = &v
        }
        out = append(out, item)
    }

    return out, rows.Err()
}

func (s *Service) RejectReportAdmin(ctx context.Context, adminID uuid.UUID, reportID uuid.UUID, note string) (ReportView, error) {
    cmd, err := s.pool.Exec(ctx,
        `UPDATE reports
         SET status = 'rejected', reviewed_at = NOW(), reviewed_by_user_id = $2, review_note = $3
         WHERE id = $1`,
        reportID,
        adminID,
        note,
    )
    if err != nil {
        return ReportView{}, err
    }
    if cmd.RowsAffected() == 0 {
        return ReportView{}, ErrNotFound
    }

    return s.GetReportByID(ctx, reportID)
}

func (s *Service) BlockReportAdmin(ctx context.Context, adminID uuid.UUID, reportID uuid.UUID, note string) (ReportView, error) {
    tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return ReportView{}, err
    }
    defer func() {
        if err != nil {
            _ = tx.Rollback(ctx)
        }
    }()

    var accusedID uuid.UUID
    err = tx.QueryRow(ctx,
        `SELECT accused_user_id FROM reports WHERE id = $1 FOR UPDATE`,
        reportID,
    ).Scan(&accusedID)
    if err != nil {
        if err == pgx.ErrNoRows {
            return ReportView{}, ErrNotFound
        }
        return ReportView{}, err
    }

    _, err = tx.Exec(ctx,
        `UPDATE reports
         SET status = 'blocked', reviewed_at = NOW(), reviewed_by_user_id = $2, review_note = $3
         WHERE id = $1`,
        reportID,
        adminID,
        note,
    )
    if err != nil {
        return ReportView{}, err
    }

    _, err = tx.Exec(ctx,
        `UPDATE users SET is_banned = TRUE, updated_at = NOW() WHERE id = $1`,
        accusedID,
    )
    if err != nil {
        return ReportView{}, err
    }

    if err = tx.Commit(ctx); err != nil {
        return ReportView{}, err
    }

    _ = s.RecalculateTrustLevel(ctx, accusedID)
    return s.GetReportByID(ctx, reportID)
}

func (s *Service) rootUserIDs(ctx context.Context) ([]uuid.UUID, error) {
    rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE is_root = TRUE`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    out := make([]uuid.UUID, 0)
    for rows.Next() {
        var id uuid.UUID
        if scanErr := rows.Scan(&id); scanErr != nil {
            return nil, scanErr
        }
        out = append(out, id)
    }

    return out, rows.Err()
}

func stringsToReportReason(v string) string {
    switch strings.TrimSpace(strings.ToLower(v)) {
    case "spam", "insult", "virus", "scam", "other":
        return strings.TrimSpace(strings.ToLower(v))
    default:
        return ""
    }
}

func stringsToReportStatus(v string) string {
    switch strings.TrimSpace(strings.ToLower(v)) {
    case "pending", "rejected", "blocked":
        return strings.TrimSpace(strings.ToLower(v))
    default:
        return ""
    }
}

func stringsContainsFold(value, needle string) bool {
    return strings.Contains(strings.ToLower(value), strings.ToLower(needle))
}
