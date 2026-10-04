package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"net/http"
	"strconv"
	"time"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }
func statusError(w http.ResponseWriter, err error) {
	var api *APIError
	if errors.As(err, &api) {
		Error(w, api.Status, api.Message)
		return
	}
	w.Header().Set("Retry-After", "1")
	Error(w, 503, "storage unavailable")
}
func (c *Core) Send(ctx context.Context, id Identity, chat string, in MessageInput) (Message, error) {
	if !validID(in.OperationID) {
		return Message{}, &APIError{400, "operation_id required (1..128 safe characters)"}
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(append([]byte(chat+"\x00"), raw...))
	hash := hex.EncodeToString(digest[:])
	var previousHash string
	var previous []byte
	err := c.DB.QueryRowContext(ctx, `SELECT hash,result FROM operations WHERE device_id=? AND operation_id=?`, id.DeviceID, in.OperationID).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != hash {
			return Message{}, &APIError{409, "operation_id reused with different content"}
		}
		var saved Message
		if err = json.Unmarshal(previous, &saved); err != nil {
			return Message{}, err
		}
		return c.ViewMessage(ctx, id, saved.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, err
	}
	_, canSend, _, err := c.Member(ctx, id.UserID, chat)
	if err != nil || !canSend {
		return Message{}, &APIError{403, "send permission denied"}
	}
	var mode string
	var epoch int64
	var pending bool
	if err = c.DB.QueryRowContext(ctx, `SELECT mode,epoch,pending FROM chats WHERE id=?`, chat).Scan(&mode, &epoch, &pending); err != nil {
		return Message{}, err
	}
	if pending {
		return Message{}, &APIError{409, "MLS epoch transition pending"}
	}
	if in.ReplyTo != "" && !c.Config.Features.Reply || in.ForwardFrom != "" && !c.Config.Features.Forward || len(in.Attachments) > 0 && !c.Config.Features.Files {
		return Message{}, &APIError{400, "feature absent from build"}
	}
	if mode == "basic" {
		if in.Envelope == nil || in.MLS != "" {
			return Message{}, &APIError{400, "basic HPKE envelope required"}
		}
		in.Payload, err = c.Engine.OpenEnvelope(*in.Envelope, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.OperationID))
		if err != nil {
			return Message{}, &APIError{400, "invalid encrypted envelope"}
		}
	} else {
		if in.Envelope != nil || in.MLS == "" || in.Epoch != epoch {
			return Message{}, &APIError{409, "invalid MLS message or epoch"}
		}
	}
	for _, hook := range c.Prepare {
		if err = hook(ctx, id, chat, &in); err != nil {
			return Message{}, err
		}
	}
	if len(in.Payload) == 0 || len(in.Payload) > c.Config.Policy.MaxMessageBytes {
		return Message{}, &APIError{400, "invalid message payload size"}
	}
	messageID := uuid.NewString()
	stored, err := c.Engine.Seal(in.Payload, []byte("message/"+messageID))
	if err != nil {
		return Message{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"attachments": in.Attachments, "reply_to": in.ReplyTo, "forward_from": in.ForwardFrom})
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	var active, allowed bool
	var currentEpoch int64
	var currentPending bool
	err = tx.QueryRowContext(ctx, `SELECT m.active AND d.revoked=0 AND u.disabled=0,m.can_send,c.epoch,c.pending FROM members m JOIN chats c ON c.id=m.chat_id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.user_id=? AND d.id=?`, chat, id.UserID, id.DeviceID).Scan(&active, &allowed, &currentEpoch, &currentPending)
	if err != nil || !active || !allowed {
		return Message{}, &APIError{403, "send permission revoked"}
	}
	if currentPending || epoch != currentEpoch {
		return Message{}, &APIError{409, "epoch changed"}
	}
	// Check deduplication again under the serialized SQLite transaction.
	err = tx.QueryRowContext(ctx, `SELECT hash,result FROM operations WHERE device_id=? AND operation_id=?`, id.DeviceID, in.OperationID).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != hash {
			return Message{}, &APIError{409, "operation conflict"}
		}
		_ = tx.Rollback()
		var saved Message
		_ = json.Unmarshal(previous, &saved)
		return c.ViewMessage(ctx, id, saved.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, err
	}
	seq, err := c.Append(ctx, tx, chat, "message.created", messageID, nil)
	if err != nil {
		return Message{}, err
	}
	message := Message{ID: messageID, ChatID: chat, Sender: id.UserID, DeviceID: id.DeviceID, OperationID: in.OperationID, Seq: seq, Revision: 1, Mode: mode, Epoch: epoch, CreatedAt: time.Now().Unix(), Attachments: in.Attachments, ReplyTo: in.ReplyTo, ForwardFrom: in.ForwardFrom}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, messageID, chat, id.UserID, id.DeviceID, in.OperationID, seq, stored, metadata, epoch, message.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	result, _ := json.Marshal(message)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations VALUES(?,?,?,?,?)`, id.DeviceID, in.OperationID, hash, result, time.Now().Unix())
	if err != nil {
		return Message{}, err
	}
	for _, hook := range c.InTransaction {
		if err = hook(ctx, tx, id, chat, message); err != nil {
			return Message{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	c.Wake(chat)
	return c.ViewMessage(ctx, id, messageID)
}
func (c *Core) ViewMessage(ctx context.Context, id Identity, messageID string) (Message, error) {
	var m Message
	var payload, metadata []byte
	err := c.DB.QueryRowContext(ctx, `SELECT msg.id,msg.chat_id,msg.sender,msg.device_id,msg.operation_id,msg.seq,msg.revision,msg.deleted,msg.payload,msg.metadata,msg.epoch,msg.created_at,c.mode FROM messages msg JOIN chats c ON c.id=msg.chat_id JOIN members member ON member.chat_id=msg.chat_id WHERE msg.id=? AND member.user_id=? AND member.active=1 AND msg.seq>=member.joined_seq`, messageID, id.UserID).Scan(&m.ID, &m.ChatID, &m.Sender, &m.DeviceID, &m.OperationID, &m.Seq, &m.Revision, &m.Deleted, &payload, &metadata, &m.Epoch, &m.CreatedAt, &m.Mode)
	if err != nil {
		return m, &APIError{404, "message not accessible"}
	}
	var meta struct {
		Attachments []string `json:"attachments"`
		ReplyTo     string   `json:"reply_to"`
		ForwardFrom string   `json:"forward_from"`
	}
	_ = json.Unmarshal(metadata, &meta)
	m.Attachments = meta.Attachments
	m.ReplyTo = meta.ReplyTo
	m.ForwardFrom = meta.ForwardFrom
	for _, hook := range c.Project {
		if err = hook(ctx, id, &m); err != nil {
			return m, err
		}
	}
	if m.Deleted {
		m.Attachments = nil
		m.ReplyTo = ""
		m.ForwardFrom = ""
		return m, nil
	}
	plain, err := c.Engine.Open(payload, []byte("message/"+m.ID))
	if err != nil {
		return m, err
	}
	if m.Mode == "basic" {
		var encoded string
		if err = c.DB.QueryRowContext(ctx, `SELECT public_key FROM devices WHERE id=? AND user_id=? AND revoked=0`, id.DeviceID, id.UserID).Scan(&encoded); err != nil {
			return m, err
		}
		pub, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return m, err
		}
		envelope, err := cryptoenc.SealEnvelope(pub, plain, cryptoenc.Binding(m.ChatID, id.UserID, id.DeviceID, m.ID))
		if err != nil {
			return m, err
		}
		m.Envelope = &envelope
	} else {
		m.MLS = base64.StdEncoding.EncodeToString(plain)
	}
	return m, nil
}
func (c *Core) send(w http.ResponseWriter, r *http.Request, id Identity) {
	var in MessageInput
	if !Decode(w, r, &in, int64(c.Config.Policy.MaxMessageBytes*2+16384)) {
		return
	}
	msg, err := c.Send(r.Context(), id, r.PathValue("chat"), in)
	if err != nil {
		statusError(w, err)
		return
	}
	JSON(w, 201, map[string]any{"status": "accepted", "message": msg})
}
func (c *Core) batch(w http.ResponseWriter, r *http.Request, id Identity) {
	var in struct {
		Messages []MessageInput `json:"messages"`
	}
	if !Decode(w, r, &in, int64(c.Config.Policy.MaxBatch*(c.Config.Policy.MaxMessageBytes*2+16384))) {
		return
	}
	if len(in.Messages) == 0 || len(in.Messages) > c.Config.Policy.MaxBatch {
		Error(w, 400, "invalid batch size")
		return
	}
	out := []map[string]any{}
	for _, item := range in.Messages {
		msg, err := c.Send(r.Context(), id, r.PathValue("chat"), item)
		if err != nil {
			code := 503
			var api *APIError
			if errors.As(err, &api) {
				code = api.Status
			}
			out = append(out, map[string]any{"operation_id": item.OperationID, "status": code, "error": err.Error()})
		} else {
			out = append(out, map[string]any{"operation_id": item.OperationID, "status": 201, "message": msg})
		}
	}
	JSON(w, 207, out)
}
func (c *Core) history(w http.ResponseWriter, r *http.Request, id Identity) {
	chat := r.PathValue("chat")
	_, _, joined, err := c.Member(r.Context(), id.UserID, chat)
	if err != nil {
		Error(w, 403, "membership required")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after < joined-1 {
		after = joined - 1
	}
	limit := 100
	if v, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && v > 0 && v <= 200 {
		limit = v
	}
	rows, err := c.DB.QueryContext(r.Context(), `SELECT id FROM messages WHERE chat_id=? AND seq>? ORDER BY seq LIMIT ?`, chat, after, limit)
	if err != nil {
		statusError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var mid string
		if err = rows.Scan(&mid); err != nil {
			rows.Close()
			statusError(w, err)
			return
		}
		ids = append(ids, mid)
	}
	rows.Close()
	out := []Message{}
	for _, mid := range ids {
		m, e := c.ViewMessage(r.Context(), id, mid)
		if e != nil {
			statusError(w, e)
			return
		}
		out = append(out, m)
	}
	JSON(w, 200, out)
}
func (c *Core) Events(ctx context.Context, id Identity, chat string, after int64, limit int) ([]Event, error) {
	_, _, joined, err := c.Member(ctx, id.UserID, chat)
	if err != nil {
		return nil, &APIError{403, "membership required"}
	}
	if after < joined-1 {
		after = joined - 1
	}
	var current int64
	var minimum sql.NullInt64
	if err = c.DB.QueryRowContext(ctx, `SELECT seq FROM chats WHERE id=?`, chat).Scan(&current); err != nil {
		return nil, err
	}
	if err = c.DB.QueryRowContext(ctx, `SELECT MIN(seq) FROM events WHERE chat_id=?`, chat).Scan(&minimum); err != nil {
		return nil, err
	}
	if after < current && (!minimum.Valid || after+1 < minimum.Int64) {
		return nil, &APIError{410, "cursor expired; sync history and current state"}
	}
	if after > current {
		return nil, &APIError{400, "cursor ahead of chat"}
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT seq,kind,message_id,data FROM events WHERE chat_id=? AND seq>? ORDER BY seq LIMIT ?`, chat, after, limit)
	if err != nil {
		return nil, err
	}
	type item struct {
		event Event
		data  []byte
	}
	items := []item{}
	for rows.Next() {
		it := item{event: Event{ChatID: chat}}
		if err = rows.Scan(&it.event.Seq, &it.event.Type, &it.event.MessageID, &it.data); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	out := []Event{}
	for _, it := range items {
		if it.event.MessageID != "" {
			m, e := c.ViewMessage(ctx, id, it.event.MessageID)
			if e != nil {
				return nil, e
			}
			it.event.Data = m
		} else if len(it.data) > 0 {
			data, e := c.Engine.Open(it.data, []byte(fmt.Sprintf("event/%s/%d", chat, it.event.Seq)))
			if e != nil {
				return nil, e
			}
			if e = json.Unmarshal(data, &it.event.Data); e != nil {
				return nil, e
			}
		}
		out = append(out, it.event)
	}
	return out, nil
}
func (c *Core) eventsHTTP(w http.ResponseWriter, r *http.Request, id Identity) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if err != nil || after < 0 {
		Error(w, 400, "after cursor required")
		return
	}
	events, err := c.Events(r.Context(), id, r.PathValue("chat"), after, 200)
	if err != nil {
		statusError(w, err)
		return
	}
	JSON(w, 200, events)
}
func (c *Core) receipt(w http.ResponseWriter, r *http.Request, id Identity) {
	var in struct {
		Delivered int64 `json:"delivered"`
		Read      int64 `json:"read"`
	}
	if !Decode(w, r, &in, 1024) {
		return
	}
	chat := r.PathValue("chat")
	if _, _, _, err := c.Member(r.Context(), id.UserID, chat); err != nil {
		Error(w, 403, "membership required")
		return
	}
	var current int64
	_ = c.DB.QueryRowContext(r.Context(), `SELECT seq FROM chats WHERE id=?`, chat).Scan(&current)
	if in.Read < 0 || in.Delivered < in.Read || in.Delivered > current {
		Error(w, 400, "invalid receipt range")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		statusError(w, err)
		return
	}
	defer tx.Rollback()
	var oldD, oldR int64
	_ = tx.QueryRowContext(r.Context(), `SELECT delivered,read FROM cursors WHERE device_id=? AND chat_id=?`, id.DeviceID, chat).Scan(&oldD, &oldR)
	if in.Delivered <= oldD && in.Read <= oldR {
		JSON(w, 200, map[string]bool{"acknowledged": true})
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO cursors VALUES(?,?,?,?) ON CONFLICT(device_id,chat_id) DO UPDATE SET delivered=MAX(delivered,excluded.delivered),read=MAX(read,excluded.read)`, id.DeviceID, chat, in.Delivered, in.Read)
	if err != nil {
		statusError(w, err)
		return
	}
	_, err = c.Append(r.Context(), tx, chat, "receipt.updated", "", map[string]any{"user_id": id.UserID, "device_id": id.DeviceID, "delivered": max(oldD, in.Delivered), "read": max(oldR, in.Read)})
	if err != nil || tx.Commit() != nil {
		Error(w, 503, "storage unavailable")
		return
	}
	c.Wake(chat)
	JSON(w, 200, map[string]bool{"acknowledged": true})
}
