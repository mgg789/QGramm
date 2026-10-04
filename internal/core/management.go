package core

import (
	"crypto/ecdh"
	"encoding/base64"
	"github.com/google/uuid"
	"net/http"
	"time"
)

func (c *Core) routes() {
	c.Mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"status": "ok"}) })
	c.Mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := c.DB.PingContext(r.Context()); err != nil {
			Error(w, 503, "storage unavailable")
			return
		}
		JSON(w, 200, map[string]string{"status": "ready"})
	})
	c.AddRoute("GET /v1/capabilities", c.capabilities)
	c.AddRoute("POST /v1/ws-tickets", c.ticket)
	c.Mux.HandleFunc("GET /v1/ws", c.websocket)
	c.AddRoute("GET /v1/chats", c.listChats)
	c.AddRoute("GET /v1/chats/{chat}/messages", c.history)
	c.AddRoute("POST /v1/chats/{chat}/messages", c.send)
	c.AddRoute("POST /v1/chats/{chat}/messages/batch", c.batch)
	c.AddRoute("GET /v1/chats/{chat}/events", c.eventsHTTP)
	c.AddRoute("POST /v1/chats/{chat}/receipts", c.receipt)
	c.AddManagementRoute("PUT /management/v1/users/{user}", c.putUser)
	c.AddManagementRoute("PUT /management/v1/users/{user}/devices/{device}", c.putDevice)
	c.AddManagementRoute("DELETE /management/v1/users/{user}/devices/{device}", c.revokeDevice)
	c.AddManagementRoute("POST /management/v1/chats/direct", func(w http.ResponseWriter, r *http.Request) { c.CreateChat(w, r, "direct") })
	c.AddManagementRoute("PUT /management/v1/chats/{chat}/members/{user}", c.putMember)
}
func (c *Core) capabilities(w http.ResponseWriter, r *http.Request, id Identity) {
	caps := map[string]any{"protocol_version": 1, "features": c.Config.Features.Enabled(), "server_key": c.Engine.PublicKey(), "server_key_id": c.Engine.KeyID(), "hpke_suite": "X25519-HKDF-SHA256-AES128GCM", "delivery": "at-least-once", "event_retention_hours": c.Config.Policy.EventRetentionHours, "dedup_retention_hours": c.Config.Policy.DedupRetentionHours, "history": c.Config.Policy.History, "delete_mode": c.Config.Policy.DeleteMode, "reaction_types": c.Config.Policy.ReactionTypes, "max_batch": c.Config.Policy.MaxBatch, "ai_trust_boundary": "container recipient; provider receives plaintext"}
	for _, extend := range c.ExtendCapabilities {
		extend(caps)
	}
	JSON(w, 200, caps)
}
func (c *Core) putUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Disabled bool `json:"disabled"`
	}
	if !Decode(w, r, &in, 1024) {
		return
	}
	user := r.PathValue("user")
	if !validID(user) {
		Error(w, 400, "invalid user id")
		return
	}
	_, err := c.DB.ExecContext(r.Context(), `INSERT INTO users(id,disabled) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET disabled=excluded.disabled`, user, in.Disabled)
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	if in.Disabled {
		c.DisconnectUser(user)
	}
	JSON(w, 200, map[string]any{"id": user, "disabled": in.Disabled})
}
func validID(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, ch := range v {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}
func (c *Core) putDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicKey  string `json:"public_key"`
		SigningKey string `json:"signing_key"`
	}
	if !Decode(w, r, &in, 4096) {
		return
	}
	key, err := base64.StdEncoding.DecodeString(in.PublicKey)
	if err != nil {
		Error(w, 400, "invalid X25519 public key")
		return
	}
	if _, err = ecdh.X25519().NewPublicKey(key); err != nil {
		Error(w, 400, "invalid X25519 public key")
		return
	}
	if in.SigningKey != "" {
		sign, e := base64.StdEncoding.DecodeString(in.SigningKey)
		if e != nil || len(sign) != 32 {
			Error(w, 400, "invalid signing key")
			return
		}
	}
	user, device := r.PathValue("user"), r.PathValue("device")
	if !validID(user) || !validID(device) {
		Error(w, 400, "invalid identity")
		return
	}
	result, err := c.DB.ExecContext(r.Context(), `INSERT INTO devices(id,user_id,public_key,signing_key) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, device, user, in.PublicKey, in.SigningKey)
	if err != nil {
		Error(w, 409, "unknown user or invalid device")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var existing, owner, sign string
		_ = c.reader().QueryRowContext(r.Context(), `SELECT public_key,user_id,signing_key FROM devices WHERE id=?`, device).Scan(&existing, &owner, &sign)
		if existing != in.PublicKey || owner != user || sign != in.SigningKey {
			Error(w, 409, "device keys immutable; register new device")
			return
		}
	}
	JSON(w, 200, map[string]string{"id": device, "user_id": user})
}
func (c *Core) revokeDevice(w http.ResponseWriter, r *http.Request) {
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `UPDATE devices SET revoked=1 WHERE id=? AND user_id=?`, r.PathValue("device"), r.PathValue("user"))
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		Error(w, 404, "device not found")
		return
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE chats SET pending=1 WHERE mode='e2ee' AND id IN(SELECT chat_id FROM members WHERE user_id=? AND active=1)`, r.PathValue("user"))
	if err != nil || tx.Commit() != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	c.DisconnectDevice(r.PathValue("device"))
	JSON(w, 200, map[string]bool{"revoked": true})
}
func (c *Core) CreateChat(w http.ResponseWriter, r *http.Request, kind string) {
	var in struct {
		ID      string   `json:"id"`
		Mode    string   `json:"mode"`
		Members []string `json:"members"`
	}
	if !Decode(w, r, &in, 16384) {
		return
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if !validID(in.ID) || len(in.Members) < 2 || (kind == "direct" && len(in.Members) != 2) {
		Error(w, 400, "invalid chat or member count")
		return
	}
	if in.Mode == "" {
		in.Mode = "basic"
	}
	if in.Mode != "basic" && !(in.Mode == "e2ee" && c.Config.Features.E2EE) {
		Error(w, 400, "unsupported encryption mode")
		return
	}
	seen := map[string]bool{}
	for _, user := range in.Members {
		if seen[user] || !validID(user) {
			Error(w, 400, "duplicate or invalid member")
			return
		}
		seen[user] = true
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	pending := in.Mode == "e2ee"
	_, err = tx.ExecContext(r.Context(), `INSERT INTO chats(id,kind,mode,pending,created_at) VALUES(?,?,?,?,?)`, in.ID, kind, in.Mode, pending, time.Now().Unix())
	if err != nil {
		Error(w, 409, "chat already exists")
		return
	}
	for i, user := range in.Members {
		role := "member"
		if i == 0 {
			role = "owner"
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO members(chat_id,user_id,role,can_send,joined_seq) VALUES(?,?,?,1,1)`, in.ID, user, role); err != nil {
			Error(w, 400, "unknown member")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	JSON(w, 201, map[string]any{"id": in.ID, "kind": kind, "mode": in.Mode, "pending_rekey": pending})
}
func (c *Core) putMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role         string `json:"role"`
		CanSend      bool   `json:"can_send"`
		Active       bool   `json:"active"`
		AllowHistory bool   `json:"allow_history"`
	}
	if !Decode(w, r, &in, 1024) {
		return
	}
	if in.Role != "owner" && in.Role != "admin" && in.Role != "member" {
		Error(w, 400, "invalid role")
		return
	}
	if in.AllowHistory && c.Config.Policy.History != "all" {
		Error(w, 400, "past history disabled")
		return
	}
	chat, user := r.PathValue("chat"), r.PathValue("user")
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var seq int64
	var mode, kind string
	if err = tx.QueryRowContext(r.Context(), `SELECT seq,mode,kind FROM chats WHERE id=?`, chat).Scan(&seq, &mode, &kind); err != nil {
		Error(w, 404, "chat not found")
		return
	}
	var previousActive bool
	var previousJoined int64
	previousErr := tx.QueryRowContext(r.Context(), `SELECT active,joined_seq FROM members WHERE chat_id=? AND user_id=?`, chat, user).Scan(&previousActive, &previousJoined)
	if kind == "direct" && previousErr != nil {
		Error(w, 400, "direct membership fixed")
		return
	}
	joined := seq + 1
	if in.AllowHistory {
		joined = 1
	} else if previousErr == nil && previousActive && in.Active {
		joined = previousJoined
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES(?,?,?,?,?,?) ON CONFLICT(chat_id,user_id) DO UPDATE SET role=excluded.role,can_send=excluded.can_send,joined_seq=excluded.joined_seq,active=excluded.active`, chat, user, in.Role, in.CanSend, joined, in.Active)
	if err != nil {
		Error(w, 400, "unknown user")
		return
	}
	if mode == "e2ee" && (previousErr != nil || previousActive != in.Active) {
		_, err = tx.ExecContext(r.Context(), `UPDATE chats SET pending=1 WHERE id=?`, chat)
		if err != nil {
			Error(w, 503, "storage unavailable")
			return
		}
	}
	_, err = c.Append(r.Context(), tx, chat, "membership.changed", "", map[string]any{"user_id": user, "active": in.Active, "can_send": in.CanSend})
	if err != nil || tx.Commit() != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	c.Wake(chat)
	if !in.Active {
		c.DisconnectUser(user)
	}
	JSON(w, 200, map[string]bool{"updated": true})
}
func (c *Core) listChats(w http.ResponseWriter, r *http.Request, id Identity) {
	rows, err := c.reader().QueryContext(r.Context(), `SELECT c.id,c.kind,c.mode,c.seq,c.epoch,c.pending,m.role,m.can_send FROM chats c JOIN members m ON m.chat_id=c.id WHERE m.user_id=? AND m.active=1 ORDER BY c.created_at,c.id`, id.UserID)
	if err != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var chat, kind, mode, role string
		var seq, epoch int64
		var pending, send bool
		if rows.Scan(&chat, &kind, &mode, &seq, &epoch, &pending, &role, &send) != nil {
			Error(w, 503, "storage unavailable")
			return
		}
		out = append(out, map[string]any{"id": chat, "kind": kind, "mode": mode, "seq": seq, "epoch": epoch, "pending_rekey": pending, "role": role, "can_send": send})
	}
	if rows.Err() != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	JSON(w, 200, out)
}
