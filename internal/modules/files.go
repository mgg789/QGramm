//go:build qg_files

package modules

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func init() { core.Register("files", installFiles) }

type fileUpload struct {
	ID, Chat, Owner, Device, Operation, Mode, Checksum, State string
	Size, Reserved, Expires, Seq                              int64
	Chunks                                                    int
	Key                                                       []byte
}
type fileStore struct {
	c  *core.Core
	mu sync.Mutex
}

const uploadSelect = `SELECT id,chat_id,owner,device,operation,mode,checksum,state,size,reserved,expires,published_seq,chunks,key FROM uploads WHERE id=?`

func (s *fileStore) get(ctx context.Context, id string) (fileUpload, error) {
	var u fileUpload
	e := s.c.DB.QueryRowContext(ctx, uploadSelect, id).Scan(&u.ID, &u.Chat, &u.Owner, &u.Device, &u.Operation, &u.Mode, &u.Checksum, &u.State, &u.Size, &u.Reserved, &u.Expires, &u.Seq, &u.Chunks, &u.Key)
	return u, e
}
func (s *fileStore) path(id string, index int) string {
	return filepath.Join(s.c.Config.Storage.Files, id, strconv.Itoa(index)+".chunk")
}
func fileAAD(id string, index int) []byte { return []byte(fmt.Sprintf("file/%s/%d", id, index)) }
func fileDigest(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && v == hex.EncodeToString(b)
}
func installFiles(c *core.Core) error {
	if err := os.MkdirAll(c.Config.Storage.Files, 0700); err != nil {
		return err
	}
	_, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS uploads(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL,owner TEXT NOT NULL,device TEXT NOT NULL,operation TEXT NOT NULL,mode TEXT NOT NULL,checksum TEXT NOT NULL,state TEXT NOT NULL,size INTEGER NOT NULL,reserved INTEGER NOT NULL,expires INTEGER NOT NULL,published_seq INTEGER NOT NULL DEFAULT 0,chunks INTEGER NOT NULL,key BLOB NOT NULL,UNIQUE(device,operation)); CREATE TABLE IF NOT EXISTS upload_chunks(upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,idx INTEGER NOT NULL,size INTEGER NOT NULL,checksum TEXT NOT NULL,PRIMARY KEY(upload_id,idx)); CREATE TABLE IF NOT EXISTS upload_messages(upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,message_id TEXT NOT NULL,PRIMARY KEY(upload_id,message_id));`)
	if err != nil {
		return err
	}
	s := &fileStore{c: c}
	if err = s.cleanup(c.Context); err != nil {
		return err
	}
	c.Cleanup = append(c.Cleanup, s.cleanup)
	c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
		_, err := tx.ExecContext(ctx, `UPDATE uploads SET state='deleted',reserved=0,key=X'' WHERE id IN(SELECT upload_id FROM upload_messages WHERE message_id=?) AND NOT EXISTS(SELECT 1 FROM upload_messages link JOIN messages msg ON msg.id=link.message_id WHERE link.upload_id=uploads.id AND msg.deleted=0)`, message)
		return err
	})
	c.AddRoute("POST /v1/chats/{chat}/uploads", s.create)
	c.AddRoute("GET /v1/uploads/{upload}", s.status)
	c.AddRoute("PUT /v1/uploads/{upload}/chunks/{index}", s.chunk)
	c.AddRoute("POST /v1/uploads/{upload}/complete", s.complete)
	c.AddRoute("GET /v1/uploads/{upload}/chunks/{index}", s.download)
	c.AddRoute("GET /v1/uploads/{upload}/key", s.key)
	c.Prepare = append(c.Prepare, func(ctx context.Context, id core.Identity, chat string, in *core.MessageInput) error {
		seen := map[string]bool{}
		for _, a := range in.Attachments {
			if seen[a] {
				return errors.New("duplicate attachment")
			}
			seen[a] = true
			u, e := s.get(ctx, a)
			if e != nil || u.Owner != id.UserID || u.Chat != chat || u.State != "ready" || (u.Seq == 0 && u.Expires < time.Now().Unix()) {
				return errors.New("attachment unavailable")
			}
		}
		return nil
	})
	c.InTransaction = append(c.InTransaction, func(ctx context.Context, tx *sql.Tx, id core.Identity, chat string, m core.Message) error {
		for _, a := range m.Attachments {
			res, e := tx.ExecContext(ctx, `UPDATE uploads SET published_seq=CASE WHEN published_seq=0 THEN ? ELSE published_seq END WHERE id=? AND chat_id=? AND owner=? AND state='ready' AND (published_seq>0 OR expires>=?)`, m.Seq, a, chat, id.UserID, time.Now().Unix())
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil || n != 1 {
				return errors.New("attachment changed before publication")
			}
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO upload_messages VALUES(?,?)`, a, m.ID); e != nil {
				return e
			}
		}
		return nil
	})
	return nil
}
func (s *fileStore) create(w http.ResponseWriter, r *http.Request, id core.Identity) {
	var in struct {
		Operation string              `json:"operation_id"`
		Size      int64               `json:"size"`
		Chunks    int                 `json:"chunks"`
		Checksum  string              `json:"sha256"`
		Envelope  *cryptoenc.Envelope `json:"envelope,omitempty"`
	}
	if !core.Decode(w, r, &in, 8192) {
		return
	}
	p := s.c.Config.Policy
	if len(in.Operation) < 1 || len(in.Operation) > 128 || in.Size <= 0 || in.Size > p.MaxFileBytes || in.Chunks <= 0 || in.Chunks > 65536 || int64(in.Chunks) > in.Size || int64(in.Chunks) > ((in.Size-1)/p.MaxChunkBytes+1)+1024 || !fileDigest(in.Checksum) {
		core.Error(w, 400, "invalid upload metadata")
		return
	}
	chat := r.PathValue("chat")
	_, send, _, e := s.c.Member(r.Context(), id.UserID, chat)
	if e != nil || !send {
		core.Error(w, 403, "chat access denied")
		return
	}
	var mode string
	if s.c.DB.QueryRowContext(r.Context(), `SELECT mode FROM chats WHERE id=?`, chat).Scan(&mode) != nil {
		core.Error(w, 404, "chat unavailable")
		return
	}
	var session []byte
	if mode == "basic" {
		if in.Envelope == nil {
			core.Error(w, 400, "encrypted file key required")
			return
		}
		session, e = s.c.Engine.OpenEnvelope(*in.Envelope, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.Operation))
		if e != nil || len(session) != 32 {
			core.Error(w, 400, "invalid encrypted file key")
			return
		}
	} else if in.Envelope != nil {
		core.Error(w, 400, "E2EE file keys belong in MLS messages")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing string
	e = s.c.DB.QueryRowContext(r.Context(), `SELECT id FROM uploads WHERE device=? AND operation=?`, id.DeviceID, in.Operation).Scan(&existing)
	if e == nil {
		u, e := s.get(r.Context(), existing)
		if e != nil || u.Chat != chat || u.Size != in.Size || u.Chunks != in.Chunks || u.Checksum != in.Checksum || u.Mode != mode {
			core.Error(w, 409, "operation metadata differs")
			return
		}
		if mode == "basic" {
			old, e := s.c.Engine.Open(u.Key, []byte("file-key/"+u.ID))
			if e != nil || !equalBytes(old, session) {
				core.Error(w, 409, "operation file key differs")
				return
			}
		}
		core.JSON(w, 200, map[string]any{"id": u.ID, "state": u.State, "expires_at": u.Expires})
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		core.Error(w, 500, "upload lookup failed")
		return
	}
	upload := uuid.NewString()
	reserved := in.Size + int64(in.Chunks)*64
	if reserved < in.Size {
		core.Error(w, 400, "upload reservation overflow")
		return
	}
	var stored []byte
	if mode == "basic" {
		stored, e = s.c.Engine.Seal(session, []byte("file-key/"+upload))
		if e != nil {
			core.Error(w, 500, "file key storage failed")
			return
		}
	} else {
		stored = []byte{}
	}
	expires := time.Now().Add(time.Duration(p.UploadTTLHours) * time.Hour).Unix()
	res, e := s.c.DB.ExecContext(r.Context(), `INSERT INTO uploads(id,chat_id,owner,device,operation,mode,checksum,state,size,reserved,expires,chunks,key) SELECT ?,?,?,?,?,?,?,'uploading',?,?,?,?,? WHERE COALESCE((SELECT SUM(reserved) FROM uploads),0)+?<=?`, upload, chat, id.UserID, id.DeviceID, in.Operation, mode, in.Checksum, in.Size, reserved, expires, in.Chunks, stored, reserved, p.MaxStorageBytes)
	if e != nil {
		core.Error(w, 500, "upload reservation failed")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		core.Error(w, 507, "storage quota exceeded")
		return
	}
	if e = os.Mkdir(filepath.Join(s.c.Config.Storage.Files, upload), 0700); e != nil {
		s.c.DB.ExecContext(r.Context(), `DELETE FROM uploads WHERE id=?`, upload)
		core.Error(w, 500, "file storage unavailable")
		return
	}
	core.JSON(w, 201, map[string]any{"id": upload, "state": "uploading", "expires_at": expires})
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
func (s *fileStore) owner(r *http.Request, id core.Identity) (fileUpload, error) {
	u, e := s.get(r.Context(), r.PathValue("upload"))
	if e != nil || u.Owner != id.UserID || u.Device != id.DeviceID {
		return u, errors.New("upload unavailable")
	}
	_, send, _, e := s.c.Member(r.Context(), id.UserID, u.Chat)
	if e != nil || !send {
		return u, errors.New("chat access denied")
	}
	return u, nil
}
func (s *fileStore) status(w http.ResponseWriter, r *http.Request, id core.Identity) {
	u, e := s.owner(r, id)
	if e != nil || u.Seq > 0 {
		u, e = s.access(r, id)
		if e != nil {
			core.Error(w, 404, "upload unavailable")
			return
		}
	}
	rows, e := s.c.DB.QueryContext(r.Context(), `SELECT idx,size,checksum FROM upload_chunks WHERE upload_id=? ORDER BY idx`, u.ID)
	if e != nil {
		core.Error(w, 500, "upload status failed")
		return
	}
	defer rows.Close()
	chunks := []map[string]any{}
	for rows.Next() {
		var i int
		var n int64
		var h string
		if rows.Scan(&i, &n, &h) != nil {
			core.Error(w, 500, "upload status failed")
			return
		}
		chunks = append(chunks, map[string]any{"index": i, "size": n, "sha256": h})
	}
	if rows.Err() != nil {
		core.Error(w, 500, "upload status failed")
		return
	}
	core.JSON(w, 200, map[string]any{"id": u.ID, "state": u.State, "mode": u.Mode, "size": u.Size, "chunks": chunks, "total_chunks": u.Chunks, "sha256": u.Checksum, "expires_at": u.Expires, "chunk_binding": map[string]string{"chat_id": u.Chat, "user_id": u.Owner, "device_id": u.Device, "operation_id": u.Operation}})
}
func (s *fileStore) chunk(w http.ResponseWriter, r *http.Request, id core.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, e := s.owner(r, id)
	index, e2 := strconv.Atoi(r.PathValue("index"))
	checksum := r.Header.Get("X-Chunk-SHA256")
	if e != nil {
		core.Error(w, 404, "upload unavailable")
		return
	}
	if e2 != nil || index < 0 || index >= u.Chunks || !fileDigest(checksum) {
		core.Error(w, 400, "invalid chunk metadata")
		return
	}
	if u.State != "uploading" || u.Expires < time.Now().Unix() {
		core.Error(w, 409, "upload is closed")
		return
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, s.c.Config.Policy.MaxChunkBytes))
	if e != nil || len(data) == 0 {
		core.Error(w, 413, "invalid chunk size")
		return
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != checksum {
		core.Error(w, 400, "chunk checksum mismatch")
		return
	}
	var old string
	e = s.c.DB.QueryRowContext(r.Context(), `SELECT checksum FROM upload_chunks WHERE upload_id=? AND idx=?`, u.ID, index).Scan(&old)
	if e == nil {
		if old != checksum {
			core.Error(w, 409, "chunk already differs")
			return
		}
		if _, err := os.Stat(s.path(u.ID, index)); err == nil {
			core.JSON(w, 200, map[string]any{"index": index})
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			core.Error(w, 500, "chunk storage unavailable")
			return
		}
		if _, err := s.c.DB.ExecContext(r.Context(), `DELETE FROM upload_chunks WHERE upload_id=? AND idx=?`, u.ID, index); err != nil {
			core.Error(w, 500, "chunk recovery failed")
			return
		}
		e = sql.ErrNoRows
	}
	if !errors.Is(e, sql.ErrNoRows) {
		core.Error(w, 500, "chunk lookup failed")
		return
	}
	var total int64
	if s.c.DB.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(size),0) FROM upload_chunks WHERE upload_id=?`, u.ID).Scan(&total) != nil || total+int64(len(data)) > u.Size {
		core.Error(w, 400, "upload size exceeded")
		return
	}
	if u.Mode == "basic" {
		key, e := s.c.Engine.Open(u.Key, []byte("file-key/"+u.ID))
		if e != nil {
			core.Error(w, 500, "file key unavailable")
			return
		}
		block, e := aes.NewCipher(key)
		if e != nil {
			core.Error(w, 500, "file key invalid")
			return
		}
		aead, e := cipher.NewGCM(block)
		if e != nil || len(data) < aead.NonceSize()+aead.Overhead() {
			core.Error(w, 400, "invalid encrypted chunk")
			return
		}
		if _, e = aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], cryptoenc.Binding(u.Chat, u.Owner, u.Device, u.Operation+"/"+strconv.Itoa(index))); e != nil {
			core.Error(w, 400, "chunk authentication failed")
			return
		}
	}
	encrypted, e := s.c.Engine.Seal(data, fileAAD(u.ID, index))
	if e != nil {
		core.Error(w, 500, "chunk encryption failed")
		return
	}
	if e = os.MkdirAll(filepath.Join(s.c.Config.Storage.Files, u.ID), 0700); e != nil {
		core.Error(w, 500, "chunk storage failed")
		return
	}
	tmp, e := os.CreateTemp(filepath.Join(s.c.Config.Storage.Files, u.ID), ".chunk-")
	if e != nil {
		core.Error(w, 500, "chunk storage failed")
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, e = tmp.Write(encrypted)
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(name, s.path(u.ID, index))
	}
	if e == nil {
		dir, err := os.Open(filepath.Join(s.c.Config.Storage.Files, u.ID))
		if err != nil {
			e = err
		} else {
			e = dir.Sync()
			closeErr := dir.Close()
			if e == nil {
				e = closeErr
			}
		}
	}
	if e != nil {
		core.Error(w, 500, "chunk storage failed")
		return
	}
	if _, e = s.c.DB.ExecContext(r.Context(), `INSERT INTO upload_chunks VALUES(?,?,?,?)`, u.ID, index, len(data), checksum); e != nil {
		os.Remove(s.path(u.ID, index))
		core.Error(w, 500, "chunk persistence failed")
		return
	}
	core.JSON(w, 201, map[string]any{"index": index})
}
func (s *fileStore) complete(w http.ResponseWriter, r *http.Request, id core.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, e := s.owner(r, id)
	if e != nil {
		core.Error(w, 404, "upload unavailable")
		return
	}
	if u.State == "ready" {
		core.JSON(w, 200, map[string]any{"id": u.ID, "state": u.State})
		return
	}
	if u.Expires < time.Now().Unix() {
		core.Error(w, 409, "upload expired")
		return
	}
	var count int
	var size int64
	if s.c.DB.QueryRowContext(r.Context(), `SELECT COUNT(*),COALESCE(SUM(size),0) FROM upload_chunks WHERE upload_id=?`, u.ID).Scan(&count, &size) != nil || count != u.Chunks || size != u.Size {
		core.Error(w, 409, "upload incomplete")
		return
	}
	h := sha256.New()
	for i := 0; i < u.Chunks; i++ {
		raw, e := os.ReadFile(s.path(u.ID, i))
		if e != nil {
			core.Error(w, 409, "chunk unavailable")
			return
		}
		b, e := s.c.Engine.Open(raw, fileAAD(u.ID, i))
		if e != nil {
			core.Error(w, 500, "chunk authentication failed")
			return
		}
		h.Write(b)
	}
	if hex.EncodeToString(h.Sum(nil)) != u.Checksum {
		core.Error(w, 400, "file checksum mismatch")
		return
	}
	if _, e = s.c.DB.ExecContext(r.Context(), `UPDATE uploads SET state='ready' WHERE id=?`, u.ID); e != nil {
		core.Error(w, 500, "completion failed")
		return
	}
	core.JSON(w, 200, map[string]any{"id": u.ID, "state": "ready"})
}
func (s *fileStore) access(r *http.Request, id core.Identity) (fileUpload, error) {
	u, e := s.get(r.Context(), r.PathValue("upload"))
	if e != nil || u.State != "ready" {
		return u, errors.New("file unavailable")
	}
	_, _, joined, e := s.c.Member(r.Context(), id.UserID, u.Chat)
	if e != nil {
		return u, e
	}
	if u.Seq == 0 {
		if u.Owner != id.UserID || u.Expires < time.Now().Unix() {
			return u, errors.New("file unpublished")
		}
	} else {
		rows, err := s.c.DB.QueryContext(r.Context(), `SELECT msg.id,msg.chat_id FROM upload_messages link JOIN messages msg ON msg.id=link.message_id WHERE link.upload_id=? AND msg.deleted=0 AND msg.seq>=?`, u.ID, joined)
		if err != nil {
			return u, err
		}
		messages := []core.Message{}
		for rows.Next() {
			var m core.Message
			if err = rows.Scan(&m.ID, &m.ChatID); err != nil {
				rows.Close()
				return u, err
			}
			messages = append(messages, m)
		}
		rows.Close()
		visible := false
		for _, m := range messages {
			for _, hook := range s.c.Project {
				if err = hook(r.Context(), id, &m); err != nil {
					return u, err
				}
			}
			if !m.Deleted {
				visible = true
			}
		}
		if !visible {
			return u, errors.New("file reference inaccessible")
		}
	}
	return u, nil
}
func (s *fileStore) download(w http.ResponseWriter, r *http.Request, id core.Identity) {
	u, e := s.access(r, id)
	i, e2 := strconv.Atoi(r.PathValue("index"))
	if e != nil {
		core.Error(w, 404, "file unavailable")
		return
	}
	if e2 != nil || i < 0 || i >= u.Chunks {
		core.Error(w, 404, "chunk unavailable")
		return
	}
	raw, e := os.ReadFile(s.path(u.ID, i))
	if e != nil {
		core.Error(w, 404, "chunk unavailable")
		return
	}
	data, e := s.c.Engine.Open(raw, fileAAD(u.ID, i))
	if e != nil {
		core.Error(w, 500, "chunk authentication failed")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
func (s *fileStore) key(w http.ResponseWriter, r *http.Request, id core.Identity) {
	u, e := s.access(r, id)
	if e != nil {
		core.Error(w, 404, "file unavailable")
		return
	}
	if u.Mode != "basic" {
		core.Error(w, 409, "file key is in MLS message")
		return
	}
	var public string
	if s.c.DB.QueryRowContext(r.Context(), `SELECT public_key FROM devices WHERE id=? AND user_id=? AND revoked=0`, id.DeviceID, id.UserID).Scan(&public) != nil {
		core.Error(w, 403, "device unavailable")
		return
	}
	pk, e := base64.StdEncoding.DecodeString(public)
	if e != nil {
		core.Error(w, 500, "device key invalid")
		return
	}
	key, e := s.c.Engine.Open(u.Key, []byte("file-key/"+u.ID))
	if e != nil {
		core.Error(w, 500, "file key unavailable")
		return
	}
	env, e := cryptoenc.SealEnvelope(pk, key, cryptoenc.Binding(u.Chat, id.UserID, id.DeviceID, u.ID))
	if e != nil {
		core.Error(w, 500, "file key encryption failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	core.JSON(w, 200, map[string]any{"envelope": env, "upload_id": u.ID, "chunk_binding": map[string]string{"chat_id": u.Chat, "user_id": u.Owner, "device_id": u.Device, "operation_id": u.Operation}})
}
func (s *fileStore) cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, e := s.c.DB.QueryContext(ctx, `SELECT id FROM uploads WHERE (published_seq=0 AND expires<?) OR state='deleted'`, time.Now().Unix())
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		res, err := s.c.DB.ExecContext(ctx, `DELETE FROM uploads WHERE id=? AND (published_seq=0 OR state='deleted')`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		if e = os.RemoveAll(filepath.Join(s.c.Config.Storage.Files, id)); e != nil {
			return e
		}
	}
	entries, e := os.ReadDir(s.c.Config.Storage.Files)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, e := uuid.Parse(entry.Name()); e != nil {
			continue
		}
		var count int
		if e = s.c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM uploads WHERE id=?`, entry.Name()).Scan(&count); e != nil {
			return e
		}
		if count == 0 {
			if e = os.RemoveAll(filepath.Join(s.c.Config.Storage.Files, entry.Name())); e != nil {
				return e
			}
			continue
		}
		files, e := os.ReadDir(filepath.Join(s.c.Config.Storage.Files, entry.Name()))
		if e != nil {
			return e
		}
		for _, f := range files {
			if len(f.Name()) > 7 && f.Name()[:7] == ".chunk-" {
				if e = os.Remove(filepath.Join(s.c.Config.Storage.Files, entry.Name(), f.Name())); e != nil {
					return e
				}
				continue
			}
			if filepath.Ext(f.Name()) == ".chunk" {
				idx, err := strconv.Atoi(f.Name()[:len(f.Name())-6])
				if err != nil {
					continue
				}
				var n int
				if err = s.c.DB.QueryRowContext(ctx, `SELECT count(*) FROM upload_chunks WHERE upload_id=? AND idx=?`, entry.Name(), idx).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					if err = os.Remove(filepath.Join(s.c.Config.Storage.Files, entry.Name(), f.Name())); err != nil {
						return err
					}
				}
			}
		}
		// A crash after reservation but before directory creation remains resumable.
	}
	return nil
}
