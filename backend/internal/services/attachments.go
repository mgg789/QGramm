package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type AttachmentView struct {
	ID              string    `json:"id"`
	OwnerUserID     string    `json:"owner_user_id"`
	Kind            string    `json:"kind"`
	StoragePath     string    `json:"-"`
	PreviewPath     string    `json:"preview_path,omitempty"`
	FileName        string    `json:"file_name"`
	MimeType        string    `json:"mime_type,omitempty"`
	SizeBytes       int64     `json:"size_bytes"`
	DurationSeconds *int      `json:"duration_seconds,omitempty"`
	ChecksumSHA256  string    `json:"checksum_sha256,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Service) GetAttachmentByIDForUser(ctx context.Context, requesterID, attachmentID uuid.UUID) (AttachmentView, error) {
	var out AttachmentView
	var previewPath, mimeType, checksum sql.NullString
	var duration sql.NullInt32

	err := s.pool.QueryRow(ctx,
		`SELECT a.id::text,
                a.owner_user_id::text,
                a.kind,
                a.storage_path,
                a.preview_path,
                a.file_name,
                a.mime_type,
                a.size_bytes,
                a.duration_seconds,
                a.checksum_sha256,
                a.created_at
         FROM attachments a
         WHERE a.id = $1
           AND (
               a.owner_user_id = $2
               OR EXISTS (
                    SELECT 1
                    FROM users u
                    WHERE u.id = $2
                      AND u.is_root = TRUE
               )
               OR EXISTS (
                    SELECT 1
                    FROM messages m
                    JOIN conversation_members cm ON cm.conversation_id = m.conversation_id
                    WHERE m.attachment_id = a.id
                      AND cm.user_id = $2
               )
           )
         LIMIT 1`,
		attachmentID,
		requesterID,
	).Scan(
		&out.ID,
		&out.OwnerUserID,
		&out.Kind,
		&out.StoragePath,
		&previewPath,
		&out.FileName,
		&mimeType,
		&out.SizeBytes,
		&duration,
		&checksum,
		&out.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return AttachmentView{}, ErrNotFound
		}
		return AttachmentView{}, err
	}

	if previewPath.Valid {
		out.PreviewPath = previewPath.String
	}
	if mimeType.Valid {
		out.MimeType = mimeType.String
	}
	if checksum.Valid {
		out.ChecksumSHA256 = checksum.String
	}
	if duration.Valid {
		v := int(duration.Int32)
		out.DurationSeconds = &v
	}

	return out, nil
}

func (s *Service) ResolveAttachmentPathForUser(ctx context.Context, requesterID, attachmentID uuid.UUID) (AttachmentView, string, error) {
	attachment, err := s.GetAttachmentByIDForUser(ctx, requesterID, attachmentID)
	if err != nil {
		return AttachmentView{}, "", err
	}

	absPath, err := s.fileStore.ResolveStoredPath(attachment.StoragePath)
	if err != nil {
		return AttachmentView{}, "", fmt.Errorf("%w: invalid attachment path", ErrNotFound)
	}

	return attachment, absPath, nil
}
