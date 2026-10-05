package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type fileMeasurement struct {
	PlainBytes                 int64   `json:"plain_bytes"`
	WireBytes                  int64   `json:"wire_bytes"`
	Chunks                     int     `json:"chunks"`
	CreateSeconds              float64 `json:"create_seconds"`
	UploadSeconds              float64 `json:"upload_seconds"`
	ResumeSeconds              float64 `json:"resume_seconds"`
	DownloadSeconds            float64 `json:"download_seconds"`
	UploadMiBPerSecond         float64 `json:"upload_mib_per_second"`
	DownloadMiBPerSecond       float64 `json:"download_mib_per_second"`
	PlainSHA256                string  `json:"plain_sha256"`
	CorruptChunkRejected       bool    `json:"corrupt_chunk_rejected"`
	ExactChunkRetry            bool    `json:"exact_chunk_retry"`
	CompleteRetry              bool    `json:"complete_retry"`
	ResumeStatusVerified       bool    `json:"resume_status_verified"`
	UnpublishedRecipientDenied bool    `json:"unpublished_recipient_denied"`
	DownloadVerified           bool    `json:"download_verified"`
}
type fileScenarioResult struct {
	Files             []fileMeasurement `json:"files"`
	ConcurrentUploads int               `json:"concurrent_uploads"`
	ElapsedSeconds    float64           `json:"elapsed_seconds"`
	TotalPlainBytes   int64             `json:"total_plain_bytes"`
	WebsocketErrors   int64             `json:"websocket_errors"`
	Limitations       []string          `json:"limitations"`
}

// Chunks are reproducible synthetic content; only transport keys are random.
// Exactly one MiB of plaintext per chunk; wire adds nonce/tag overhead.
func filePlainChunk(file, index, size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte((file*71 + index*13 + i) % 251)
	}
	return out
}
func sealFileChunk(aead cipher.AEAD, plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, aad), nil
}
func openFileChunk(aead cipher.AEAD, raw, aad []byte) ([]byte, error) {
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, fmt.Errorf("truncated file chunk")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], aad)
}
func (q *qGramm) binaryRequest(ctx context.Context, method, path string, index int, body []byte, checksum string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(q.cfg.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	token, err := q.token(index)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Content-Type", "application/octet-stream")
	if checksum != "" {
		req.Header.Set("X-Chunk-SHA256", checksum)
	}
	response, err := q.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return raw, response.StatusCode, err
}
func runFileScenario(ctx context.Context, cfg Config) (any, error) {
	if cfg.Service != "qgramm" || cfg.Users != 2 || cfg.Chats != 1 || cfg.Fanout != 1 || cfg.FileBytes <= 0 || cfg.FileBytes > 64<<20 || cfg.FileCount < 1 || cfg.FileCount > 2 {
		return nil, fmt.Errorf("file scenario requires QGramm, two users, one direct chat, 1..2 files up to64MiB")
	}
	adapter, err := NewQGramm(ctx, cfg, func(Delivery) {})
	if err != nil {
		return nil, err
	}
	q := adapter.(*qGramm)
	defer q.Close()
	result := fileScenarioResult{Files: make([]fileMeasurement, cfg.FileCount), ConcurrentUploads: cfg.FileCount, Limitations: []string{"QGramm-only basic encrypted attachment acceptance; competitors do not implement this file API", "Synthetic reproducible payload; 1MiB plaintext chunks plus28B AESGCM overhead", "Resume drops idle HTTP pooled connections between successful requests; no midrequest/server crash", "Upload wall time includes checksum refusal, exact chunk retry, status resume and completion; generation excluded"}}
	started := time.Now()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for i := 0; i < cfg.FileCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			measurement, err := q.runFile(ctx, i, cfg.FileBytes)
			result.Files[i] = measurement
			if err != nil {
				once.Do(func() { first = err })
				return
			}
		}(i)
	}
	wg.Wait()
	result.ElapsedSeconds = time.Since(started).Seconds()
	result.TotalPlainBytes = cfg.FileBytes * int64(cfg.FileCount)
	result.WebsocketErrors = q.Errors()
	if first != nil {
		return result, first
	}
	if result.WebsocketErrors != 0 {
		return result, fmt.Errorf("file scenario WebSocket errors")
	}
	return result, nil
}
func (q *qGramm) runFile(ctx context.Context, file int, size int64) (fileMeasurement, error) {
	m := fileMeasurement{PlainBytes: size}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return m, err
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	operation := fmt.Sprintf("file-%d", file)
	chat, owner := q.chats[0], q.names[0]
	chunks := make([][]byte, 0, (size+(1<<20)-1)/(1<<20))
	checksums := []string{}
	plainHash, wireHash := sha256.New(), sha256.New()
	for remaining := size; remaining > 0; {
		n := int64(1 << 20)
		if remaining < n {
			n = remaining
		}
		index := len(chunks)
		plain := filePlainChunk(file, index, int(n))
		plainHash.Write(plain)
		wire, err := sealFileChunk(aead, plain, cryptoenc.Binding(chat, owner, owner, operation+"/"+strconv.Itoa(index)))
		if err != nil {
			return m, err
		}
		chunks = append(chunks, wire)
		hash := sha256.Sum256(wire)
		checksums = append(checksums, hex.EncodeToString(hash[:]))
		wireHash.Write(wire)
		m.WireBytes += int64(len(wire))
		remaining -= n
	}
	m.Chunks = len(chunks)
	m.PlainSHA256 = hex.EncodeToString(plainHash.Sum(nil))
	envelope, err := cryptoenc.SealEnvelope(q.serverKey, key, cryptoenc.Binding(chat, owner, owner, operation))
	if err != nil {
		return m, err
	}
	started := time.Now()
	raw, status, err := q.request(ctx, "POST", "/v1/chats/"+chat+"/uploads", 0, map[string]any{"operation_id": operation, "size": m.WireBytes, "chunks": len(chunks), "sha256": hex.EncodeToString(wireHash.Sum(nil)), "envelope": envelope})
	if err != nil {
		return m, err
	}
	if status != 201 {
		return m, fmt.Errorf("file create status %d", status)
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &created) != nil || created.ID == "" || created.State != "uploading" {
		return m, fmt.Errorf("invalid file creation response")
	}
	m.CreateSeconds = time.Since(started).Seconds()
	base := "/v1/uploads/" + created.ID
	uploadStart := time.Now()
	put := func(index int, checksum string, want int) error {
		_, status, err := q.binaryRequest(ctx, "PUT", fmt.Sprintf("%s/chunks/%d", base, index), 0, chunks[index], checksum)
		if err != nil {
			return err
		}
		if status != want {
			return fmt.Errorf("file chunk status %d expected %d", status, want)
		}
		return nil
	}
	if err = put(0, strings.Repeat("0", 64), 400); err != nil {
		return m, err
	}
	m.CorruptChunkRejected = true
	half := len(chunks) / 2
	if half < 1 {
		half = 1
	}
	for i := 0; i < half; i++ {
		if err = put(i, checksums[i], 201); err != nil {
			return m, err
		}
	}
	if err = put(0, checksums[0], 200); err != nil {
		return m, err
	}
	m.ExactChunkRetry = true
	resumeStart := time.Now()
	raw, status, err = q.request(ctx, "GET", base, 0, nil)
	if err != nil {
		return m, err
	}
	if status != 200 {
		return m, fmt.Errorf("file resume status %d", status)
	}
	var resume struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Chunks []struct {
			Index int    `json:"index"`
			Size  int    `json:"size"`
			SHA   string `json:"sha256"`
		} `json:"chunks"`
	}
	if json.Unmarshal(raw, &resume) != nil || resume.ID != created.ID || resume.State != "uploading" || len(resume.Chunks) != half {
		return m, fmt.Errorf("file resume chunks mismatch")
	}
	for i, c := range resume.Chunks {
		if c.Index != i || c.Size != len(chunks[i]) || c.SHA != checksums[i] {
			return m, fmt.Errorf("file accepted chunk metadata mismatch")
		}
	}
	m.ResumeStatusVerified = true
	q.http.CloseIdleConnections()
	for i := half; i < len(chunks); i++ {
		if err = put(i, checksums[i], 201); err != nil {
			return m, err
		}
	}
	m.ResumeSeconds = time.Since(resumeStart).Seconds()
	for retry := 0; retry < 2; retry++ {
		raw, status, err = q.request(ctx, "POST", base+"/complete", 0, nil)
		if err != nil {
			return m, err
		}
		var complete struct {
			ID    string `json:"id"`
			State string `json:"state"`
		}
		if status != 200 || json.Unmarshal(raw, &complete) != nil || complete.ID != created.ID || complete.State != "ready" {
			return m, fmt.Errorf("file completion status or identity invalid: %d", status)
		}
	}
	m.CompleteRetry = true
	m.UploadSeconds = time.Since(uploadStart).Seconds()
	m.UploadMiBPerSecond = float64(size) / (1 << 20) / m.UploadSeconds
	_, status, err = q.request(ctx, "GET", base+"/key", 1, nil)
	if err != nil {
		return m, err
	}
	if status != 404 {
		return m, fmt.Errorf("unpublished recipient file key accessible: %d", status)
	}
	m.UnpublishedRecipientDenied = true
	messageOperation := operation + "-message"
	caption := []byte("benchmark caption")
	messageEnvelope, err := cryptoenc.SealEnvelope(q.serverKey, caption, cryptoenc.Binding(chat, owner, owner, messageOperation))
	if err != nil {
		return m, err
	}
	raw, status, err = q.request(ctx, "POST", "/v1/chats/"+chat+"/messages", 0, map[string]any{"operation_id": messageOperation, "envelope": messageEnvelope, "attachments": []string{created.ID}})
	if err != nil {
		return m, err
	}
	if status != 201 {
		return m, fmt.Errorf("file publication status %d", status)
	}
	if err = validateQAck(raw, q.cfg.ResponseMode == "minimal", chat, messageOperation); err != nil {
		return m, err
	}
	downloadStart := time.Now()
	raw, status, err = q.request(ctx, "GET", base+"/key", 1, nil)
	if err != nil {
		return m, err
	}
	if status != 200 {
		return m, fmt.Errorf("recipient file key status %d", status)
	}
	var wrapped struct {
		ID       string             `json:"upload_id"`
		Envelope cryptoenc.Envelope `json:"envelope"`
		Binding  struct {
			Chat      string `json:"chat_id"`
			User      string `json:"user_id"`
			Device    string `json:"device_id"`
			Operation string `json:"operation_id"`
		} `json:"chunk_binding"`
	}
	if json.Unmarshal(raw, &wrapped) != nil || wrapped.ID != created.ID || wrapped.Binding.Chat != chat || wrapped.Binding.User != owner || wrapped.Binding.Device != owner || wrapped.Binding.Operation != operation {
		return m, fmt.Errorf("recipient file binding invalid")
	}
	recipientKey, err := q.engines[1].OpenEnvelope(wrapped.Envelope, cryptoenc.Binding(chat, q.names[1], q.names[1], created.ID))
	if err != nil || !bytes.Equal(recipientKey, key) {
		return m, fmt.Errorf("recipient key unwrap failed")
	}
	recipientBlock, _ := aes.NewCipher(recipientKey)
	recipientAEAD, _ := cipher.NewGCM(recipientBlock)
	downloadedHash := sha256.New()
	var downloadedBytes int64
	for i := range chunks {
		raw, status, err = q.binaryRequest(ctx, "GET", fmt.Sprintf("%s/chunks/%d", base, i), 1, nil, "")
		if err != nil {
			return m, err
		}
		if status != 200 {
			return m, fmt.Errorf("file download status %d", status)
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != checksums[i] {
			return m, fmt.Errorf("downloaded wire hash differs")
		}
		plain, err := openFileChunk(recipientAEAD, raw, cryptoenc.Binding(chat, owner, owner, operation+"/"+strconv.Itoa(i)))
		if err != nil {
			return m, err
		}
		expectedSize := len(chunks[i]) - recipientAEAD.NonceSize() - recipientAEAD.Overhead()
		if !bytes.Equal(plain, filePlainChunk(file, i, expectedSize)) {
			return m, fmt.Errorf("downloaded plaintext differs")
		}
		downloadedHash.Write(plain)
		downloadedBytes += int64(len(plain))
	}
	if downloadedBytes != size || hex.EncodeToString(downloadedHash.Sum(nil)) != m.PlainSHA256 {
		return m, fmt.Errorf("downloaded file hash differs")
	}
	m.DownloadVerified = true
	m.DownloadSeconds = time.Since(downloadStart).Seconds()
	m.DownloadMiBPerSecond = float64(size) / (1 << 20) / m.DownloadSeconds
	return m, nil
}
