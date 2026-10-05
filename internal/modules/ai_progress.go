//go:build qg_ai_streaming && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func init() {
	aiProgressInstall = installAIProgress
	aiProgressPrepare = prepareAIProgress
}

func installAIProgress(c *core.Core) error {
	tx, err := c.DB.BeginTx(c.Context, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c.Context, `CREATE TABLE IF NOT EXISTS ai_progress_versions(version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(c.Context, `SELECT COALESCE(MAX(version),0) FROM ai_progress_versions`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("AI progress schema newer than binary")
	}
	if _, err = tx.ExecContext(c.Context, `CREATE TABLE IF NOT EXISTS ai_progress_versions(version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS ai_progress(job_id TEXT NOT NULL,chat_id TEXT NOT NULL REFERENCES chats(id),chunk INTEGER NOT NULL,kind TEXT NOT NULL,payload BLOB NOT NULL,created_at INTEGER NOT NULL,epoch INTEGER NOT NULL,PRIMARY KEY(job_id,chunk));
CREATE TABLE IF NOT EXISTS ai_progress_heads(job_id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES chats(id),last_chunk INTEGER NOT NULL,stored_bytes INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS ai_progress_expiry ON ai_progress(created_at);
INSERT OR IGNORE INTO ai_progress_versions VALUES(1);`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.AddRoute("GET /v1/chats/{chat}/ai/jobs/{job}/progress", func(w http.ResponseWriter, r *http.Request, id core.Identity) { readAIProgress(c, w, r, id) })
	c.AddRoute("POST /v1/chats/{chat}/ai/jobs/{job}/cancel", func(w http.ResponseWriter, r *http.Request, id core.Identity) { cancelAIProgress(c, w, r, id) })
	c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM ai_progress WHERE chat_id=(SELECT chat_id FROM messages WHERE id=?)`, message)
		return err
	})
	c.Cleanup = append(c.Cleanup, func(ctx context.Context) error {
		_, err := c.DB.ExecContext(ctx, `DELETE FROM ai_progress WHERE rowid IN(SELECT rowid FROM ai_progress INDEXED BY ai_progress_expiry WHERE created_at<? LIMIT 256)`, time.Now().Add(-time.Duration(c.Config.Policy.EventRetentionHours)*time.Hour).Unix())
		return err
	})
	return nil
}

// Text deltas are coalesced without a goroutine per stream. The first delta is
// durable immediately; subsequent deltas flush at 4KiB, a 25ms arrival interval,
// a different delta kind, or provider termination.
func prepareAIProgress(ctx context.Context, c *core.Core, job string) (context.Context, func() error) {
	var mu sync.Mutex
	var text strings.Builder
	var last time.Time
	var failed error
	flush := func() error {
		if failed != nil {
			return failed
		}
		if text.Len() == 0 {
			return nil
		}
		payload, _ := json.Marshal(map[string]string{"text": text.String()})
		failed = writeAIProgress(ctx, c, job, "text.delta", payload)
		text.Reset()
		last = time.Now()
		return failed
	}
	emit := func(kind string, payload json.RawMessage) error {
		mu.Lock()
		defer mu.Unlock()
		if failed != nil {
			return failed
		}
		if kind == "text.delta" {
			var delta struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(payload, &delta) != nil {
				return errors.New("invalid text delta")
			}
			if text.Len()+len(delta.Text) > c.Config.Policy.MaxMessageBytes {
				return errors.New("AI progress too large")
			}
			text.WriteString(delta.Text)
			if last.IsZero() || text.Len() >= 4096 || time.Since(last) >= 25*time.Millisecond {
				return flush()
			}
			return nil
		}
		if err := flush(); err != nil {
			return err
		}
		failed = writeAIProgress(ctx, c, job, kind, payload)
		return failed
	}
	if aiStreamContext != nil {
		ctx = aiStreamContext(ctx, emit)
	}
	return ctx, func() error { mu.Lock(); defer mu.Unlock(); return flush() }
}

func writeAIProgress(ctx context.Context, c *core.Core, job, kind string, payload json.RawMessage) error {
	if !json.Valid(payload) || len(payload) > 65536 || len(kind) > 64 {
		return errors.New("invalid AI progress")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil || task.Status != "running" || !aiTaskAuthorized(ctx, tx, task) {
		return errors.New("AI task unavailable or revoked")
	}
	var index, bytes int
	err = tx.QueryRowContext(ctx, `SELECT last_chunk+1,stored_bytes FROM ai_progress_heads WHERE job_id=?`, job).Scan(&index, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		index = 1
		bytes = 0
		err = nil
	}
	if err != nil {
		return err
	}
	if index > 4096 || bytes+len(payload)+128 > 2<<20 {
		return errors.New("AI progress budget exhausted")
	}
	wire := []byte(payload)
	if task.Mode == "e2ee" {
		if aiSealMLS == nil {
			return errors.New("MLS streaming unavailable")
		}
		operation := fmt.Sprintf("ai-progress-%s-%d", job, index)
		wire, task.State, err = aiSealMLS(c, task.Chat, task.State, wire, cryptoenc.Binding(task.Chat, task.User, task.Device, operation))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_chats SET state=? WHERE chat_id=?`, task.State, task.Chat); err != nil {
			return err
		}
	}
	stored, err := c.Engine.Seal(wire, []byte(fmt.Sprintf("ai/progress/%s/%d", job, index)))
	if err != nil {
		return err
	}
	if bytes+len(stored) > 2<<20 {
		return errors.New("AI progress budget exhausted")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_progress VALUES(?,?,?,?,?,?,?)`, job, task.Chat, index, kind, stored, time.Now().Unix(), task.Epoch); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_progress_heads VALUES(?,?,?,?) ON CONFLICT(job_id) DO UPDATE SET last_chunk=excluded.last_chunk,stored_bytes=excluded.stored_bytes`, job, task.Chat, index, bytes+len(stored)); err != nil {
		return err
	}
	if _, err = c.Append(ctx, tx, task.Chat, "ai.progress.available", "", map[string]any{"job_id": job, "chunk": index, "kind": kind}); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		c.Wake(task.Chat)
	}
	return err
}

func aiProgressMember(ctx context.Context, tx *sql.Tx, task aiTaskIdentity, id core.Identity) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT d.public_key FROM members member JOIN devices d ON d.user_id=member.user_id JOIN users u ON u.id=member.user_id JOIN messages source ON source.chat_id=member.chat_id WHERE member.chat_id=? AND member.user_id=? AND d.id=? AND member.active=1 AND d.revoked=0 AND u.disabled=0 AND source.id=? AND source.seq>=member.joined_seq AND source.deleted=0`, task.Chat, id.UserID, id.DeviceID, task.SourceMessage).Scan(&key)
	return key, err
}

func readAIProgress(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	after, err := strconv.Atoi(r.URL.Query().Get("after"))
	if r.URL.Query().Get("after") == "" {
		after = 0
		err = nil
	}
	if err != nil || after < 0 {
		core.Error(w, 400, "invalid chunk cursor")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(r.Context(), tx, r.PathValue("job"))
	if err != nil || task.Chat != r.PathValue("chat") {
		core.Error(w, 404, "AI job unavailable")
		return
	}
	key, err := aiProgressMember(r.Context(), tx, task, id)
	if err != nil {
		core.Error(w, 403, "membership required")
		return
	}
	var low sql.NullInt64
	var high int
	if err = tx.QueryRowContext(r.Context(), `SELECT MIN(chunk),COALESCE((SELECT last_chunk FROM ai_progress_heads WHERE job_id=?),0) FROM ai_progress WHERE job_id=?`, r.PathValue("job"), r.PathValue("job")).Scan(&low, &high); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if (low.Valid && int64(after) < low.Int64-1) || (!low.Valid && after < high) {
		core.Error(w, 410, "progress cursor expired")
		return
	}
	if after > high {
		core.Error(w, 400, "cursor ahead of progress")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT chunk,kind,payload,epoch FROM ai_progress WHERE job_id=? AND chunk>? ORDER BY chunk LIMIT 200`, r.PathValue("job"), after)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var index int
		var epoch int64
		var kind string
		var stored []byte
		if err = rows.Scan(&index, &kind, &stored, &epoch); err != nil {
			break
		}
		plain, e := c.Engine.Open(stored, []byte(fmt.Sprintf("ai/progress/%s/%d", r.PathValue("job"), index)))
		if e != nil {
			err = e
			break
		}
		operation := fmt.Sprintf("ai-progress-%s-%d", r.PathValue("job"), index)
		item := map[string]any{"chunk": index, "kind": kind, "operation_id": operation, "sender": task.User, "device_id": task.Device, "mode": task.Mode, "epoch": epoch}
		if task.Mode == "e2ee" {
			item["mls"] = base64.StdEncoding.EncodeToString(plain)
		} else {
			public, e := base64.StdEncoding.DecodeString(key)
			if e != nil {
				err = e
				break
			}
			env, e := cryptoenc.SealEnvelope(public, plain, cryptoenc.Binding(task.Chat, task.User, task.Device, operation))
			if e != nil {
				err = e
				break
			}
			item["envelope"] = env
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		core.Error(w, 503, "progress unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"job_id": r.PathValue("job"), "status": task.Status, "chunks": items})
}

func cancelAIProgress(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	job := r.PathValue("job")
	task, err := aiTaskLookup(r.Context(), tx, job)
	if err != nil || task.Chat != r.PathValue("chat") {
		core.Error(w, 404, "AI job unavailable")
		return
	}
	if _, err = aiProgressMember(r.Context(), tx, task, id); err != nil {
		core.Error(w, 403, "membership required")
		return
	}
	var role string
	if err = tx.QueryRowContext(r.Context(), `SELECT role FROM members WHERE chat_id=? AND user_id=? AND active=1`, task.Chat, id.UserID).Scan(&role); err != nil || (task.SourceUser != id.UserID && role != "owner" && role != "admin") {
		core.Error(w, 403, "job owner or administrator required")
		return
	}
	if task.Status == "queued" || task.Status == "running" {
		res, e := tx.ExecContext(r.Context(), `UPDATE ai_jobs SET status='cancelled',updated_at=? WHERE id=? AND status IN('queued','running')`, time.Now().Unix(), job)
		if e != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			if err = aiNamedSetStatus(r.Context(), tx, job, "cancelled"); err != nil {
				core.Error(w, 503, "storage unavailable")
				return
			}
		}
		if _, err = c.Append(r.Context(), tx, task.Chat, "ai.job.cancelled", "", map[string]string{"job_id": job}); err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		task.Status = "cancelled"
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	aiCancelActive(c, job)
	c.Wake(task.Chat)
	core.JSON(w, 200, map[string]string{"job_id": job, "status": task.Status})
}
