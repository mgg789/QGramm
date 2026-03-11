package services

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type UploadView struct {
	ID             string    `json:"id"`
	OwnerUserID    string    `json:"owner_user_id"`
	Kind           string    `json:"kind"`
	FileName       string    `json:"file_name"`
	MimeType       string    `json:"mime_type,omitempty"`
	TotalSize      int64     `json:"total_size"`
	ChunkSize      int       `json:"chunk_size"`
	ExpectedChunks int       `json:"expected_chunks"`
	ReceivedChunks int       `json:"received_chunks"`
	ReceivedBytes  int64     `json:"received_bytes"`
	Status         string    `json:"status"`
	AttachmentID   *string   `json:"finalized_attachment_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Service) CreateUpload(ctx context.Context, userID uuid.UUID, in CreateUploadInput) (UploadView, error) {
	kind := strings.TrimSpace(in.Kind)
	if !isValidAttachmentKind(kind) {
		return UploadView{}, fmt.Errorf("%w: invalid upload kind", ErrBadRequest)
	}
	if strings.TrimSpace(in.FileName) == "" {
		return UploadView{}, fmt.Errorf("%w: file_name is required", ErrBadRequest)
	}
	if in.TotalSize <= 0 {
		return UploadView{}, fmt.Errorf("%w: total_size must be positive", ErrBadRequest)
	}

	maxSize := s.maxSizeForKind(kind)
	if in.TotalSize > maxSize {
		return UploadView{}, fmt.Errorf("%w: file is larger than limit for kind %s", ErrBadRequest, kind)
	}

	chunkSize := in.ChunkSize
	if chunkSize <= 0 {
		chunkSize = int(s.cfg.Limits.MaxUploadChunkSizeBytes)
	}
	if int64(chunkSize) > s.cfg.Limits.MaxUploadChunkSizeBytes {
		return UploadView{}, fmt.Errorf("%w: chunk size exceeds server limit", ErrBadRequest)
	}

	if err := s.checkStorageQuota(ctx, userID, in.TotalSize); err != nil {
		return UploadView{}, err
	}

	if err := s.failStaleUploads(ctx, userID); err != nil {
		return UploadView{}, err
	}

	activeUploads, err := s.activeUploadsCount(ctx, userID)
	if err != nil {
		return UploadView{}, err
	}
	if activeUploads >= s.cfg.Limits.MaxParallelUploadsPerUser {
		return UploadView{}, fmt.Errorf("%w: too many parallel uploads", ErrForbidden)
	}

	expectedChunks := int(math.Ceil(float64(in.TotalSize) / float64(chunkSize)))
	if expectedChunks <= 0 {
		expectedChunks = 1
	}

	uploadID := uuid.New()
	tempDir := s.fileStore.UploadTempDir(uploadID)

	_, err = s.pool.Exec(ctx,
		`INSERT INTO uploads (
            id, owner_user_id, kind, file_name, mime_type,
            total_size, chunk_size, expected_chunks,
            temp_dir, status
         ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'created')`,
		uploadID,
		userID,
		kind,
		in.FileName,
		in.MimeType,
		in.TotalSize,
		chunkSize,
		expectedChunks,
		tempDir,
	)
	if err != nil {
		return UploadView{}, err
	}

	return s.GetUpload(ctx, userID, uploadID)
}

func (s *Service) PutUploadChunk(ctx context.Context, userID uuid.UUID, in UploadChunkInput) (UploadView, error) {
	uploadID, err := uuid.Parse(in.UploadID)
	if err != nil {
		return UploadView{}, fmt.Errorf("%w: invalid upload_id", ErrBadRequest)
	}
	if in.ChunkIndex < 0 {
		return UploadView{}, fmt.Errorf("%w: chunk index must be >= 0", ErrBadRequest)
	}
	if int64(len(in.Payload)) > s.cfg.Limits.MaxUploadChunkSizeBytes {
		return UploadView{}, fmt.Errorf("%w: chunk too large", ErrBadRequest)
	}

	var expectedChunks int
	var status string
	err = s.pool.QueryRow(ctx,
		`SELECT expected_chunks, status
         FROM uploads
         WHERE id = $1 AND owner_user_id = $2`,
		uploadID,
		userID,
	).Scan(&expectedChunks, &status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return UploadView{}, ErrNotFound
		}
		return UploadView{}, err
	}

	if status == "assembled" {
		return UploadView{}, fmt.Errorf("%w: upload already finalized", ErrBadRequest)
	}
	if in.ChunkIndex >= expectedChunks {
		return UploadView{}, fmt.Errorf("%w: chunk index out of range", ErrBadRequest)
	}

	size, checksum, path, err := s.fileStore.WriteChunk(uploadID, in.ChunkIndex, in.Payload)
	if err != nil {
		return UploadView{}, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadView{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	var oldSize int64
	oldExistsErr := tx.QueryRow(ctx,
		`SELECT size_bytes
         FROM upload_chunks
         WHERE upload_id = $1 AND chunk_index = $2`,
		uploadID,
		in.ChunkIndex,
	).Scan(&oldSize)
	hasOld := oldExistsErr == nil
	if oldExistsErr != nil && oldExistsErr != pgx.ErrNoRows {
		return UploadView{}, oldExistsErr
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO upload_chunks (upload_id, chunk_index, size_bytes, checksum_sha256, stored_path)
         VALUES ($1,$2,$3,$4,$5)
         ON CONFLICT (upload_id, chunk_index)
         DO UPDATE SET size_bytes = EXCLUDED.size_bytes,
                       checksum_sha256 = EXCLUDED.checksum_sha256,
                       stored_path = EXCLUDED.stored_path,
                       created_at = NOW()`,
		uploadID,
		in.ChunkIndex,
		size,
		checksum,
		path,
	)
	if err != nil {
		return UploadView{}, err
	}

	deltaSize := size
	chunkIncrement := 1
	if hasOld {
		deltaSize = size - oldSize
		chunkIncrement = 0
	}

	_, err = tx.Exec(ctx,
		`UPDATE uploads
         SET received_chunks = received_chunks + $2,
             received_bytes = received_bytes + $3,
             status = 'uploading',
             updated_at = NOW()
         WHERE id = $1`,
		uploadID,
		chunkIncrement,
		deltaSize,
	)
	if err != nil {
		return UploadView{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return UploadView{}, err
	}

	return s.GetUpload(ctx, userID, uploadID)
}

func (s *Service) CompleteUpload(ctx context.Context, userID uuid.UUID, in CompleteUploadInput) (UploadView, error) {
	uploadID, err := uuid.Parse(in.UploadID)
	if err != nil {
		return UploadView{}, fmt.Errorf("%w: invalid upload_id", ErrBadRequest)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UploadView{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	var upload UploadView
	var attachmentID sql.NullString
	err = tx.QueryRow(ctx,
		`SELECT id::text, owner_user_id::text, kind, file_name, COALESCE(mime_type,''),
                total_size, chunk_size, expected_chunks, received_chunks, received_bytes,
                status, finalized_attachment_id::text, created_at, updated_at
         FROM uploads
         WHERE id = $1 AND owner_user_id = $2
         FOR UPDATE`,
		uploadID,
		userID,
	).Scan(
		&upload.ID,
		&upload.OwnerUserID,
		&upload.Kind,
		&upload.FileName,
		&upload.MimeType,
		&upload.TotalSize,
		&upload.ChunkSize,
		&upload.ExpectedChunks,
		&upload.ReceivedChunks,
		&upload.ReceivedBytes,
		&upload.Status,
		&attachmentID,
		&upload.CreatedAt,
		&upload.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return UploadView{}, ErrNotFound
		}
		return UploadView{}, err
	}
	if attachmentID.Valid {
		v := attachmentID.String
		upload.AttachmentID = &v
	}

	if upload.Status == "assembled" {
		return upload, nil
	}

	if upload.ReceivedChunks < upload.ExpectedChunks {
		return UploadView{}, fmt.Errorf("%w: upload is incomplete", ErrBadRequest)
	}

	assembled, err := s.fileStore.AssembleUpload(uploadID, upload.FileName, upload.ExpectedChunks, upload.Kind)
	if err != nil {
		_, _ = tx.Exec(ctx, `UPDATE uploads SET status = 'failed', updated_at = NOW() WHERE id = $1`, uploadID)
		return UploadView{}, err
	}
	if assembled.SizeBytes != upload.TotalSize {
		_, _ = tx.Exec(ctx, `UPDATE uploads SET status = 'failed', updated_at = NOW() WHERE id = $1`, uploadID)
		return UploadView{}, fmt.Errorf("%w: assembled size mismatch", ErrBadRequest)
	}

	newAttachmentID := uuid.New()
	_, err = tx.Exec(ctx,
		`INSERT INTO attachments (
            id, owner_user_id, kind, storage_path, preview_path,
            file_name, mime_type, size_bytes, checksum_sha256
         ) VALUES ($1,$2,$3,$4,NULL,$5,NULLIF($6,''),$7,$8)`,
		newAttachmentID,
		userID,
		upload.Kind,
		filepath.ToSlash(assembled.RelativePath),
		upload.FileName,
		upload.MimeType,
		assembled.SizeBytes,
		assembled.SHA256,
	)
	if err != nil {
		return UploadView{}, err
	}

	_, err = tx.Exec(ctx,
		`UPDATE uploads
         SET status = 'assembled',
             finalized_attachment_id = $2,
             received_bytes = $3,
             updated_at = NOW()
         WHERE id = $1`,
		uploadID,
		newAttachmentID,
		assembled.SizeBytes,
	)
	if err != nil {
		return UploadView{}, err
	}

	if upload.Kind == "avatar" {
		_, err = tx.Exec(ctx,
			`UPDATE users
             SET avatar_path = $2,
                 updated_at = NOW()
             WHERE id = $1`,
			userID,
			filepath.ToSlash(assembled.RelativePath),
		)
		if err != nil {
			return UploadView{}, err
		}
	}

	if err = tx.Commit(ctx); err != nil {
		return UploadView{}, err
	}

	_ = s.fileStore.RemoveUploadTemp(uploadID)

	return s.GetUpload(ctx, userID, uploadID)
}

func (s *Service) CancelUpload(ctx context.Context, userID, uploadID uuid.UUID) (UploadView, error) {
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status
         FROM uploads
         WHERE id = $1 AND owner_user_id = $2`,
		uploadID,
		userID,
	).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return UploadView{}, ErrNotFound
		}
		return UploadView{}, err
	}

	if status == "assembled" {
		return UploadView{}, fmt.Errorf("%w: upload already finalized", ErrBadRequest)
	}
	if status != "failed" {
		_, err = s.pool.Exec(ctx,
			`UPDATE uploads
             SET status = 'failed', updated_at = NOW()
             WHERE id = $1`,
			uploadID,
		)
		if err != nil {
			return UploadView{}, err
		}
	}

	_ = s.fileStore.RemoveUploadTemp(uploadID)
	return s.GetUpload(ctx, userID, uploadID)
}

func (s *Service) GetUpload(ctx context.Context, userID, uploadID uuid.UUID) (UploadView, error) {
	var out UploadView
	var attachmentID sql.NullString
	err := s.pool.QueryRow(ctx,
		`SELECT id::text, owner_user_id::text, kind, file_name, COALESCE(mime_type,''),
                total_size, chunk_size, expected_chunks, received_chunks, received_bytes,
                status, finalized_attachment_id::text, created_at, updated_at
         FROM uploads
         WHERE id = $1 AND owner_user_id = $2`,
		uploadID,
		userID,
	).Scan(
		&out.ID,
		&out.OwnerUserID,
		&out.Kind,
		&out.FileName,
		&out.MimeType,
		&out.TotalSize,
		&out.ChunkSize,
		&out.ExpectedChunks,
		&out.ReceivedChunks,
		&out.ReceivedBytes,
		&out.Status,
		&attachmentID,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return UploadView{}, ErrNotFound
		}
		return UploadView{}, err
	}
	if attachmentID.Valid {
		v := attachmentID.String
		out.AttachmentID = &v
	}
	return out, nil
}

func (s *Service) checkStorageQuota(ctx context.Context, userID uuid.UUID, incomingBytes int64) error {
	var userUsed int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0)
         FROM attachments
         WHERE owner_user_id = $1`,
		userID,
	).Scan(&userUsed)
	if err != nil {
		return err
	}

	if userUsed+incomingBytes > s.cfg.Limits.MaxStoragePerUserBytes {
		return fmt.Errorf("%w: user storage quota exceeded", ErrForbidden)
	}

	var totalUsed int64
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM attachments`,
	).Scan(&totalUsed)
	if err != nil {
		return err
	}

	if totalUsed+incomingBytes > s.cfg.Limits.MaxProjectStorageBytes {
		return fmt.Errorf("%w: project storage quota exceeded", ErrForbidden)
	}

	return nil
}

func (s *Service) activeUploadsCount(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)
         FROM uploads
         WHERE owner_user_id = $1
           AND status IN ('created', 'uploading')`,
		userID,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Service) failStaleUploads(ctx context.Context, userID uuid.UUID) error {
	staleMinutes := s.cfg.Storage.CleanupIntervalMinutes * 4
	if staleMinutes < 30 {
		staleMinutes = 30
	}

	rows, err := s.pool.Query(ctx,
		`UPDATE uploads
         SET status = 'failed',
             updated_at = NOW()
         WHERE owner_user_id = $1
           AND status IN ('created', 'uploading')
           AND updated_at < NOW() - ($2::text || ' minutes')::interval
         RETURNING id`,
		userID,
		strconv.Itoa(staleMinutes),
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var uploadID uuid.UUID
		if scanErr := rows.Scan(&uploadID); scanErr != nil {
			return scanErr
		}
		_ = s.fileStore.RemoveUploadTemp(uploadID)
	}

	return rows.Err()
}

func (s *Service) maxSizeForKind(kind string) int64 {
	switch kind {
	case "voice_note":
		return s.cfg.Limits.MaxVoiceSizeBytes
	case "circular_video":
		return s.cfg.Limits.MaxVideoNoteSizeBytes
	case "avatar":
		return s.cfg.Limits.MaxAvatarSizeBytes
	default:
		return s.cfg.Limits.MaxFileSizeBytes
	}
}

func isValidAttachmentKind(kind string) bool {
	switch kind {
	case "file", "media", "voice_note", "circular_video", "avatar":
		return true
	default:
		return false
	}
}
