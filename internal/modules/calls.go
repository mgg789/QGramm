//go:build qg_calls

package modules

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func init() { core.Register("calls", installCalls) }

func turnCredential(secret, username string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Calls only relays bounded signaling. Media travels directly or through TURN.
func installCalls(c *core.Core) error {
	_, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS calls(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL,caller TEXT NOT NULL,callee TEXT NOT NULL,state TEXT NOT NULL,mode TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL); CREATE INDEX IF NOT EXISTS calls_chat ON calls(chat_id,state); CREATE TABLE IF NOT EXISTS call_operations(device TEXT NOT NULL,operation TEXT NOT NULL,call_id TEXT NOT NULL,hash TEXT NOT NULL,seq INTEGER NOT NULL,state TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(device,operation))`)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	c.Cleanup = append(c.Cleanup, func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now().Unix()
		rows, e := c.DB.QueryContext(ctx, `SELECT id,chat_id FROM calls WHERE (state='ringing' AND created_at<?) OR (state='active' AND created_at<?)`, now-120, now-43200)
		if e != nil {
			return e
		}
		type expired struct{ id, chat string }
		items := []expired{}
		for rows.Next() {
			var it expired
			if e = rows.Scan(&it.id, &it.chat); e != nil {
				rows.Close()
				return e
			}
			items = append(items, it)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, it := range items {
			tx, e := c.DB.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `UPDATE calls SET state='ended',updated_at=? WHERE id=?`, now, it.id)
			if e == nil {
				_, e = c.Append(ctx, tx, it.chat, "call.end", "", map[string]any{"id": it.id, "reason": "timeout"})
			}
			if e != nil {
				tx.Rollback()
				return e
			}
			if e = tx.Commit(); e != nil {
				return e
			}
			c.Wake(it.chat)
		}
		_, e = c.DB.ExecContext(ctx, `DELETE FROM call_operations WHERE created_at<?`, now-int64(c.Config.Policy.DedupRetentionHours)*3600)
		if e != nil {
			return e
		}
		_, e = c.DB.ExecContext(ctx, `DELETE FROM calls WHERE state='ended' AND updated_at<?`, now-86400)
		return e
	})
	c.AddRoute("GET /v1/calls/turn", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
		secret := os.Getenv(c.Config.Calls.TURNSecretEnv)
		if secret == "" {
			core.Error(w, 503, "TURN unavailable")
			return
		}
		expires := time.Now().Unix() + int64(c.Config.Calls.CredentialTTLSeconds)
		username := strconv.FormatInt(expires, 10) + ":" + id.UserID
		core.JSON(w, 200, map[string]any{"urls": c.Config.Calls.TURNURLs, "username": username, "credential": turnCredential(secret, username), "expires_at": expires})
	})
	c.AddRoute("POST /v1/chats/{chat}/calls", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
		var in struct {
			Mode string `json:"mode"`
		}
		if !core.Decode(w, r, &in, 1024) {
			return
		}
		if in.Mode != "audio" && in.Mode != "video" {
			core.Error(w, 400, "invalid call mode")
			return
		}
		chat := r.PathValue("chat")
		_, send, _, e := c.Member(r.Context(), id.UserID, chat)
		if e != nil || !send {
			core.Error(w, 403, "chat access denied")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var kind, callee string
		var count int
		if c.DB.QueryRowContext(r.Context(), `SELECT kind FROM chats WHERE id=?`, chat).Scan(&kind) != nil || kind != "direct" {
			core.Error(w, 400, "calls require a direct chat")
			return
		}
		if c.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM members WHERE chat_id=? AND active=1`, chat).Scan(&count) != nil || count != 2 {
			core.Error(w, 409, "calls require two active participants")
			return
		}
		if c.DB.QueryRowContext(r.Context(), `SELECT user_id FROM members WHERE chat_id=? AND active=1 AND user_id<>?`, chat, id.UserID).Scan(&callee) != nil {
			core.Error(w, 409, "peer unavailable")
			return
		}
		if c.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM calls WHERE chat_id=? AND state IN ('ringing','active')`, chat).Scan(&count) != nil || count != 0 {
			core.Error(w, 409, "call already in progress")
			return
		}
		call := uuid.NewString()
		now := time.Now().Unix()
		tx, e := c.DB.BeginTx(r.Context(), nil)
		if e != nil {
			core.Error(w, 500, "create call failed")
			return
		}
		defer tx.Rollback()
		if !callPermission(r.Context(), tx, id, chat) {
			core.Error(w, 403, "call access changed")
			return
		}
		if tx.QueryRowContext(r.Context(), `SELECT count(*) FROM members WHERE chat_id=? AND active=1`, chat).Scan(&count) != nil || count != 2 {
			core.Error(w, 409, "call participants changed")
			return
		}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO calls VALUES(?,?,?,?,?,?,?,?)`, call, chat, id.UserID, callee, "ringing", in.Mode, now, now); e != nil {
			core.Error(w, 500, "create call failed")
			return
		}
		seq, e := c.Append(r.Context(), tx, chat, "call.ringing", "", map[string]any{"id": call, "caller": id.UserID, "mode": in.Mode})
		if e != nil {
			core.Error(w, 500, "signal persistence failed")
			return
		}
		if tx.Commit() != nil {
			core.Error(w, 500, "create call failed")
			return
		}
		c.Wake(chat)
		core.JSON(w, 201, map[string]any{"id": call, "state": "ringing", "seq": seq})
	})
	c.AddRoute("POST /v1/calls/{call}/signals", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
		var in struct {
			Type      string              `json:"type"`
			Operation string              `json:"operation_id"`
			ToDevice  string              `json:"to_device,omitempty"`
			Envelope  *cryptoenc.Envelope `json:"envelope,omitempty"`
			MLS       string              `json:"mls,omitempty"`
			Epoch     int64               `json:"epoch,omitempty"`
		}
		if !core.Decode(w, r, &in, 100000) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var chat, caller, callee, state, mode string
		if c.DB.QueryRowContext(r.Context(), `SELECT chat_id,caller,callee,state FROM calls WHERE id=?`, r.PathValue("call")).Scan(&chat, &caller, &callee, &state) != nil {
			core.Error(w, 404, "call unavailable")
			return
		}
		if id.UserID != caller && id.UserID != callee {
			core.Error(w, 403, "call access denied")
			return
		}
		_, send, _, e := c.Member(r.Context(), id.UserID, chat)
		if e != nil || !send {
			core.Error(w, 403, "chat access denied")
			return
		}
		if c.DB.QueryRowContext(r.Context(), `SELECT mode FROM chats WHERE id=?`, chat).Scan(&mode) != nil {
			core.Error(w, 404, "chat unavailable")
			return
		}
		if len(in.Operation) < 1 || len(in.Operation) > 128 {
			core.Error(w, 400, "operation_id required")
			return
		}
		wire, _ := json.Marshal(in)
		sum := sha256.Sum256(wire)
		hash := hex.EncodeToString(sum[:])
		var previousHash, previousState, previousCall string
		var previousSeq int64
		err := c.DB.QueryRowContext(r.Context(), `SELECT hash,state,seq,call_id FROM call_operations WHERE device=? AND operation=?`, id.DeviceID, in.Operation).Scan(&previousHash, &previousState, &previousSeq, &previousCall)
		if err == nil {
			if previousHash != hash || previousCall != r.PathValue("call") {
				core.Error(w, 409, "operation differs")
				return
			}
			core.JSON(w, 200, map[string]any{"seq": previousSeq, "state": previousState})
			return
		} else if !errors.Is(err, sql.ErrNoRows) {
			core.Error(w, 500, "operation lookup failed")
			return
		}
		var payload struct {
			SDP       string `json:"sdp,omitempty"`
			Candidate string `json:"candidate,omitempty"`
			SDPMid    string `json:"sdp_mid,omitempty"`
			SDPLine   *int   `json:"sdp_mline_index,omitempty"`
		}
		next := state
		switch in.Type {
		case "accept":
			if state != "ringing" || id.UserID != callee {
				core.Error(w, 409, "invalid call transition")
				return
			}
			next = "active"
		case "reject":
			if state != "ringing" || id.UserID != callee {
				core.Error(w, 409, "invalid call transition")
				return
			}
			next = "ended"
		case "end":
			if state != "ringing" && state != "active" {
				core.Error(w, 409, "call already ended")
				return
			}
			next = "ended"
		case "offer":
			if id.UserID != caller || (state != "ringing" && state != "active") {
				core.Error(w, 409, "invalid offer")
				return
			}
		case "answer":
			if id.UserID != callee || state != "active" {
				core.Error(w, 409, "invalid answer")
				return
			}
		case "ice":
			if state != "ringing" && state != "active" {
				core.Error(w, 409, "call ended")
				return
			}
		default:
			core.Error(w, 400, "invalid signal type")
			return
		}
		media := in.Type == "offer" || in.Type == "answer" || in.Type == "ice"
		if media && mode == "e2ee" {
			raw, err := base64.StdEncoding.DecodeString(in.MLS)
			if err != nil || len(raw) == 0 || len(raw) > 45000 || in.Envelope != nil || in.ToDevice != "" {
				core.Error(w, 400, "opaque MLS signaling required")
				return
			}
		} else if media {
			if in.Envelope == nil || in.MLS != "" || len(in.ToDevice) < 1 || len(in.ToDevice) > 128 {
				core.Error(w, 400, "encrypted signaling and recipient device required")
				return
			}
			raw, err := c.Engine.OpenEnvelope(*in.Envelope, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.Operation))
			if err != nil || len(raw) > 60000 {
				core.Error(w, 400, "invalid signal envelope")
				return
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err = dec.Decode(&payload); err != nil {
				core.Error(w, 400, "invalid signal payload")
				return
			}
			var trailing any
			if !errors.Is(dec.Decode(&trailing), io.EOF) {
				core.Error(w, 400, "invalid signal payload")
				return
			}
			if len(payload.SDPMid) > 128 || (payload.SDPLine != nil && (*payload.SDPLine < 0 || *payload.SDPLine > 128)) {
				core.Error(w, 400, "invalid signal")
				return
			}
			if in.Type == "ice" {
				if payload.Candidate == "" || len(payload.Candidate) > 4096 || payload.SDP != "" {
					core.Error(w, 400, "invalid ICE candidate")
					return
				}
			} else if payload.SDP == "" || len(payload.SDP) > 58000 || payload.Candidate != "" {
				core.Error(w, 400, "invalid session description")
				return
			}
		} else if in.Envelope != nil || in.MLS != "" || in.ToDevice != "" {
			core.Error(w, 400, "unexpected signal payload")
			return
		}
		tx, e := c.DB.BeginTx(r.Context(), nil)
		if e != nil {
			core.Error(w, 500, "signal persistence failed")
			return
		}
		defer tx.Rollback()
		if !callPermission(r.Context(), tx, id, chat) {
			core.Error(w, 403, "call access changed")
			return
		}
		if media && mode == "e2ee" {
			var epoch int64
			var pending bool
			if tx.QueryRowContext(r.Context(), `SELECT epoch,pending FROM chats WHERE id=?`, chat).Scan(&epoch, &pending) != nil || pending || epoch != in.Epoch {
				core.Error(w, 409, "MLS epoch changed or rekey pending")
				return
			}
		}
		data := map[string]any{"id": r.PathValue("call"), "sender": id.UserID, "device_id": id.DeviceID, "operation_id": in.Operation, "type": in.Type}
		if media && mode == "basic" {
			peer := callee
			if id.UserID == callee {
				peer = caller
			}
			var public string
			if tx.QueryRowContext(r.Context(), `SELECT d.public_key FROM devices d JOIN users u ON u.id=d.user_id JOIN members m ON m.user_id=d.user_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0 AND m.chat_id=? AND m.active=1`, in.ToDevice, peer, chat).Scan(&public) != nil {
				core.Error(w, 403, "recipient device unavailable")
				return
			}
			pk, err := base64.StdEncoding.DecodeString(public)
			if err != nil {
				core.Error(w, 500, "recipient key invalid")
				return
			}
			raw, _ := json.Marshal(payload)
			env, err := cryptoenc.SealEnvelope(pk, raw, cryptoenc.Binding(chat, peer, in.ToDevice, in.Operation))
			if err != nil {
				core.Error(w, 500, "signal encryption failed")
				return
			}
			data["envelope"] = env
			data["to_device"] = in.ToDevice
			data["to_user"] = peer
		} else if media {
			data["mls"] = in.MLS
			data["epoch"] = in.Epoch
		}
		seq, e := c.Append(r.Context(), tx, chat, "call."+in.Type, "", data)
		if e != nil {
			core.Error(w, 500, "signal persistence failed")
			return
		}
		if next != state {
			if _, e = tx.ExecContext(r.Context(), `UPDATE calls SET state=?,updated_at=? WHERE id=?`, next, time.Now().Unix(), r.PathValue("call")); e != nil {
				core.Error(w, 500, "call transition failed")
				return
			}
		}
		if _, e = tx.ExecContext(r.Context(), `INSERT INTO call_operations VALUES(?,?,?,?,?,?,?)`, id.DeviceID, in.Operation, r.PathValue("call"), hash, seq, next, time.Now().Unix()); e != nil {
			core.Error(w, 500, "operation persistence failed")
			return
		}
		if tx.Commit() != nil {
			core.Error(w, 500, "signal persistence failed")
			return
		}
		c.Wake(chat)
		core.JSON(w, 200, map[string]any{"seq": seq, "state": next})
	})
	if len(c.Config.Calls.TURNURLs) == 0 {
		return fmt.Errorf("calls require TURN configuration")
	}
	return nil
}

func callPermission(ctx context.Context, tx *sql.Tx, id core.Identity, chat string) bool {
	var n int
	return tx.QueryRowContext(ctx, `SELECT count(*) FROM members m JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.user_id=? AND m.active=1 AND m.can_send=1 AND d.id=? AND d.revoked=0 AND u.disabled=0`, chat, id.UserID, id.DeviceID).Scan(&n) == nil && n == 1
}
