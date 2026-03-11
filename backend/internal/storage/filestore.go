package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"qgramm/backend/internal/config"
)

var invalidFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type FileStore struct {
	cfg config.StorageConfig
}

type AssembledFile struct {
	RelativePath string
	AbsolutePath string
	SizeBytes    int64
	SHA256       string
}

func NewFileStore(cfg config.StorageConfig) *FileStore {
	return &FileStore{cfg: cfg}
}

func (f *FileStore) EnsureDirs() error {
	dirs := []string{f.cfg.DataDir, f.cfg.TmpDir, f.cfg.AttachmentsDir, f.cfg.AvatarsDir}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}

func (f *FileStore) UploadTempDir(uploadID uuid.UUID) string {
	return filepath.Join(f.cfg.TmpDir, "uploads", uploadID.String())
}

func (f *FileStore) ChunkPath(uploadID uuid.UUID, chunkIndex int) string {
	return filepath.Join(f.UploadTempDir(uploadID), fmt.Sprintf("chunk-%08d.part", chunkIndex))
}

func (f *FileStore) WriteChunk(uploadID uuid.UUID, chunkIndex int, data []byte) (int64, string, string, error) {
	tempDir := f.UploadTempDir(uploadID)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return 0, "", "", fmt.Errorf("create upload temp dir: %w", err)
	}

	path := f.ChunkPath(uploadID, chunkIndex)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, "", "", fmt.Errorf("write chunk: %w", err)
	}

	hash := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(hash[:]), path, nil
}

func (f *FileStore) AssembleUpload(uploadID uuid.UUID, fileName string, expectedChunks int, kind string) (AssembledFile, error) {
	safeName := sanitizeFileName(fileName)
	if safeName == "" {
		safeName = uploadID.String()
	}

	rel := f.buildRelativePath(kind, uploadID, safeName)
	dst := filepath.Join(f.cfg.DataDir, rel)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return AssembledFile{}, fmt.Errorf("create destination directory: %w", err)
	}

	out, err := os.Create(dst)
	if err != nil {
		return AssembledFile{}, fmt.Errorf("create destination file: %w", err)
	}
	defer out.Close()

	hasher := sha256.New()
	var total int64

	for i := 0; i < expectedChunks; i++ {
		chunkPath := f.ChunkPath(uploadID, i)
		in, openErr := os.Open(chunkPath)
		if openErr != nil {
			return AssembledFile{}, fmt.Errorf("open chunk %d: %w", i, openErr)
		}

		written, copyErr := io.Copy(io.MultiWriter(out, hasher), in)
		closeErr := in.Close()
		if closeErr != nil {
			return AssembledFile{}, fmt.Errorf("close chunk %d: %w", i, closeErr)
		}
		if copyErr != nil {
			return AssembledFile{}, fmt.Errorf("copy chunk %d: %w", i, copyErr)
		}
		total += written
	}

	return AssembledFile{
		RelativePath: rel,
		AbsolutePath: dst,
		SizeBytes:    total,
		SHA256:       hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func (f *FileStore) RemoveUploadTemp(uploadID uuid.UUID) error {
	return os.RemoveAll(f.UploadTempDir(uploadID))
}

func (f *FileStore) ResolveStoredPath(storedPath string) (string, error) {
	cleanStored := filepath.Clean(strings.TrimSpace(storedPath))
	if cleanStored == "" || cleanStored == "." {
		return "", errors.New("empty stored path")
	}
	if filepath.IsAbs(cleanStored) {
		return "", errors.New("absolute stored path is forbidden")
	}
	if strings.HasPrefix(cleanStored, "..") {
		return "", errors.New("invalid stored path")
	}

	dataRootAbs, err := filepath.Abs(f.cfg.DataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w", err)
	}

	resolved := filepath.Clean(filepath.Join(dataRootAbs, cleanStored))
	sep := string(os.PathSeparator)
	if resolved != dataRootAbs && !strings.HasPrefix(resolved, dataRootAbs+sep) {
		return "", errors.New("stored path escapes data dir")
	}
	return resolved, nil
}

func (f *FileStore) buildRelativePath(kind string, uploadID uuid.UUID, fileName string) string {
	date := time.Now().UTC()
	base := "attachments"
	if kind == "avatar" {
		base = "avatars"
	}

	return filepath.Join(base,
		fmt.Sprintf("%04d", date.Year()),
		fmt.Sprintf("%02d", int(date.Month())),
		fmt.Sprintf("%02d", date.Day()),
		fmt.Sprintf("%s_%s", uploadID.String(), fileName),
	)
}

func sanitizeFileName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, string(os.PathSeparator), "_")
	cleaned := invalidFilenameChars.ReplaceAllString(trimmed, "_")
	cleaned = strings.Trim(cleaned, "._-")
	if len(cleaned) > 120 {
		cleaned = cleaned[:120]
	}
	return cleaned
}
