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
	"slices"
	"strconv"
	"strings"
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
		if api.Status == http.StatusServiceUnavailable || api.Status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		Error(w, api.Status, api.Message)
		return
	}
	w.Header().Set("Retry-After", "1")
	Error(w, 503, "storage unavailable")
}
func (c *Core) Send(ctx context.Context, id Identity, chat string, in MessageInput) (Message, error) {
	return c.sendMessage(ctx, id, chat, in, false)
}

// sendMessage keeps the default projection compatible, while receipt-only callers
// avoid reading and re-encrypting content after a durable commit.
func (c *Core) sendMessage(ctx context.Context, id Identity, chat string, in MessageInput, minimal bool) (Message, error) {
	job, saved, err := c.prepareMessage(ctx, id, chat, in)
	if err != nil {
		return Message{}, err
	}
	if job == nil {
		return c.sendResult(ctx, id, saved, minimal, true)
	}
	var result messageWriteResult
	if c.writer != nil {
		result.message, result.repeated, result.err = c.writer.enqueue(*job)
	} else {
		result = c.submitPrepared([]messageWriteJob{*job})[0]
	}
	if result.err != nil {
		return Message{}, result.err
	}
	if !result.repeated {
		c.Wake(chat)
	}
	return c.sendResult(ctx, id, result.message, minimal, result.repeated)
}

// submitPrepared keeps bounded groups together through admission and commit.
func (c *Core) submitPrepared(jobs []messageWriteJob) []messageWriteResult {
	if c.writer != nil {
		return c.writer.submitGroup(jobs)
	}
	// Open normally installs a writer; retain the embedding fallback with the
	// same savepoint and transaction implementation, without an admission queue.
	w := messageWriter{core: c}
	for i := range jobs {
		jobs[i].queued = time.Now()
		jobs[i].result = make(chan messageWriteResult, 1)
	}
	w.executeBatchContext(jobs, jobs[0].ctx)
	results := make([]messageWriteResult, len(jobs))
	for i := range jobs {
		results[i] = <-jobs[i].result
	}
	return results
}

func (c *Core) prepareMessage(ctx context.Context, id Identity, chat string, in MessageInput) (*messageWriteJob, Message, error) {
	if !validID(in.OperationID) {
		return nil, Message{}, &APIError{400, "operation_id required (1..128 safe characters)"}
	}
	// Queue jobs own their attachment metadata even when an embedding caller
	// reuses its input after admission. Preserve nil versus empty JSON arrays.
	in.Attachments = slices.Clone(in.Attachments)
	raw, _ := json.Marshal(in)
	digest := sha256.New()
	_, _ = digest.Write([]byte(chat))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(raw)
	hash := hex.EncodeToString(digest.Sum(nil))
	var previousHash string
	var previous []byte
	err := c.readQueryRow(ctx, `SELECT hash,result FROM operations WHERE device_id=? AND operation_id=?`, id.DeviceID, in.OperationID).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != hash {
			return nil, Message{}, &APIError{409, "operation_id reused with different content"}
		}
		var saved Message
		if err = json.Unmarshal(previous, &saved); err != nil {
			return nil, Message{}, err
		}
		return nil, saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, Message{}, err
	}
	var mode string
	var epoch int64
	var pending, canSend bool
	err = c.readQueryRow(ctx, `SELECT c.mode,c.epoch,c.pending,m.can_send FROM chats c JOIN members m ON m.chat_id=c.id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE c.id=? AND m.user_id=? AND d.id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0`, chat, id.UserID, id.DeviceID).Scan(&mode, &epoch, &pending, &canSend)
	if err != nil || !canSend {
		return nil, Message{}, &APIError{403, "send permission denied"}
	}
	if pending {
		return nil, Message{}, &APIError{409, "MLS epoch transition pending"}
	}
	if in.ReplyTo != "" && !c.Config.Features.Reply || in.ForwardFrom != "" && !c.Config.Features.Forward || len(in.Attachments) > 0 && !c.Config.Features.Files {
		return nil, Message{}, &APIError{400, "feature absent from build"}
	}
	if mode == "basic" {
		if in.Envelope == nil || in.MLS != "" {
			return nil, Message{}, &APIError{400, "basic HPKE envelope required"}
		}
		in.Payload, err = c.Engine.OpenEnvelope(*in.Envelope, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.OperationID))
		if err != nil {
			return nil, Message{}, &APIError{400, "invalid encrypted envelope"}
		}
	} else {
		if in.Envelope != nil || in.MLS == "" || in.Epoch != epoch {
			return nil, Message{}, &APIError{409, "invalid MLS message or epoch"}
		}
	}
	for _, hook := range c.Prepare {
		if err = hook(ctx, id, chat, &in); err != nil {
			return nil, Message{}, err
		}
	}
	if len(in.Payload) == 0 || len(in.Payload) > c.Config.Policy.MaxMessageBytes {
		return nil, Message{}, &APIError{400, "invalid message payload size"}
	}
	messageID := uuid.NewString()
	stored, err := c.Engine.Seal(in.Payload, []byte("message/"+messageID))
	if err != nil {
		return nil, Message{}, err
	}
	metadata, _ := json.Marshal(messageMetadata{Attachments: in.Attachments, ReplyTo: in.ReplyTo, ForwardFrom: in.ForwardFrom})
	// The queued closure retains ciphertext and metadata only, not plaintext or
	// the original transport envelope. Do not mutate caller-owned byte slices.
	in.Payload, in.Envelope, in.MLS = nil, nil, ""
	write := func(writeCtx context.Context, tx *sql.Tx) (Message, bool, error) {
		return c.persistMessageInTx(writeCtx, tx, id, chat, in, mode, epoch, hash, messageID, stored, metadata)
	}
	return &messageWriteJob{ctx: ctx, bytes: len(stored) + len(metadata), runTx: write}, Message{}, nil
}

// persistMessageInTx repeats ACL, epoch and dedup checks in the writer-owned
// transaction. The caller acknowledges only after that transaction commits.
func (c *Core) persistMessageInTx(ctx context.Context, tx *sql.Tx, id Identity, chat string, in MessageInput, mode string, epoch int64, hash, messageID string, stored, metadata []byte) (Message, bool, error) {
	var previousHash string
	var previous []byte
	var err error
	var active, allowed bool
	var currentEpoch int64
	var currentPending bool
	err = c.txQueryRow(ctx, tx, `SELECT m.active AND d.revoked=0 AND u.disabled=0,m.can_send,c.epoch,c.pending FROM members m JOIN chats c ON c.id=m.chat_id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.user_id=? AND d.id=?`, chat, id.UserID, id.DeviceID).Scan(&active, &allowed, &currentEpoch, &currentPending)
	if err != nil || !active || !allowed {
		return Message{}, false, &APIError{403, "send permission revoked"}
	}
	if currentPending || epoch != currentEpoch {
		return Message{}, false, &APIError{409, "epoch changed"}
	}
	// Check deduplication again under the serialized SQLite transaction.
	err = c.txQueryRow(ctx, tx, `SELECT hash,result FROM operations WHERE device_id=? AND operation_id=?`, id.DeviceID, in.OperationID).Scan(&previousHash, &previous)
	if err == nil {
		if previousHash != hash {
			return Message{}, false, &APIError{409, "operation conflict"}
		}
		var saved Message
		if err = json.Unmarshal(previous, &saved); err != nil {
			return Message{}, false, err
		}
		return saved, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	seq, err := c.Append(ctx, tx, chat, "message.created", messageID, nil)
	if err != nil {
		return Message{}, false, err
	}
	message := Message{ID: messageID, ChatID: chat, Sender: id.UserID, DeviceID: id.DeviceID, OperationID: in.OperationID, Seq: seq, Revision: 1, Mode: mode, Epoch: epoch, CreatedAt: time.Now().Unix(), Attachments: in.Attachments, ReplyTo: in.ReplyTo, ForwardFrom: in.ForwardFrom}
	_, err = c.txExec(ctx, tx, `INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, messageID, chat, id.UserID, id.DeviceID, in.OperationID, seq, stored, metadata, epoch, message.CreatedAt)
	if err != nil {
		return Message{}, false, err
	}
	result, _ := json.Marshal(message)
	_, err = c.txExec(ctx, tx, `INSERT INTO operations VALUES(?,?,?,?,?)`, id.DeviceID, in.OperationID, hash, result, time.Now().Unix())
	if err != nil {
		return Message{}, false, err
	}
	for _, hook := range c.InTransaction {
		if err = hook(ctx, tx, id, chat, message); err != nil {
			return Message{}, false, err
		}
	}
	return message, false, nil
}

func (c *Core) sendResult(ctx context.Context, id Identity, saved Message, minimal, repeated bool) (Message, error) {
	if !minimal {
		return c.ViewMessage(ctx, id, saved.ID)
	}
	if repeated {
		// A stored idempotency result must not bypass revoked membership/device access.
		var accessible bool
		err := c.readQueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages msg JOIN members m ON m.chat_id=msg.chat_id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE msg.id=? AND m.user_id=? AND m.active=1 AND msg.seq>=m.joined_seq AND d.id=? AND d.revoked=0 AND u.disabled=0)`, saved.ID, id.UserID, id.DeviceID).Scan(&accessible)
		if err != nil {
			return Message{}, err
		}
		if !accessible {
			return Message{}, &APIError{404, "message not accessible"}
		}
	}
	return saved, nil
}

func minimalPreference(r *http.Request) bool {
	for _, value := range r.Header.Values("Prefer") {
		for _, preference := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(preference, ";", 2)[0]), "return=minimal") {
				return true
			}
		}
	}
	return false
}

func acceptanceReceipt(m Message) map[string]any {
	return map[string]any{"message_id": m.ID, "chat_id": m.ChatID, "operation_id": m.OperationID, "seq": m.Seq}
}

type messageMetadata struct {
	Attachments []string `json:"attachments"`
	ForwardFrom string   `json:"forward_from"`
	ReplyTo     string   `json:"reply_to"`
}

func (c *Core) ViewMessage(ctx context.Context, id Identity, messageID string) (Message, error) {
	messages, err := c.projectMessages(ctx, id, []string{messageID})
	if err != nil {
		return Message{}, err
	}
	return messages[0], nil
}

// viewMessages provides an ID lookup for internal batch callers. History and
// event pages use ordered slices directly and need no output ID map.
func (c *Core) viewMessages(ctx context.Context, id Identity, ids []string) (map[string]Message, error) {
	messages, err := c.projectMessages(ctx, id, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Message, len(messages))
	for _, m := range messages {
		out[m.ID] = m
	}
	return out, nil
}

type storedMessage struct {
	message           Message
	payload, metadata []byte
}

// These columns have a stable order shared by direct, history, and event reads.
const messageProjectionColumns = `msg.id,msg.chat_id,msg.sender,msg.device_id,msg.operation_id,msg.seq,msg.revision,msg.deleted,msg.payload,msg.metadata,msg.epoch,msg.created_at,c.mode,d.public_key`
const nullableMessageProjectionColumns = `COALESCE(msg.id,''),COALESCE(msg.chat_id,''),COALESCE(msg.sender,''),COALESCE(msg.device_id,''),COALESCE(msg.operation_id,''),COALESCE(msg.seq,0),COALESCE(msg.revision,0),COALESCE(msg.deleted,0),msg.payload,msg.metadata,COALESCE(msg.epoch,0),COALESCE(msg.created_at,0),c.mode,COALESCE(d.public_key,'')`
const messageProjectionAccess = ` JOIN chats c ON c.id=msg.chat_id JOIN members member ON member.chat_id=msg.chat_id JOIN devices d ON d.user_id=member.user_id JOIN users u ON u.id=member.user_id WHERE member.user_id=? AND d.id=? AND member.active=1 AND d.revoked=0 AND u.disabled=0 AND msg.seq>=member.joined_seq`

func (s *storedMessage) destinations(encodedKey *string) []any {
	m := &s.message
	return []any{&m.ID, &m.ChatID, &m.Sender, &m.DeviceID, &m.OperationID, &m.Seq, &m.Revision, &m.Deleted, &s.payload, &s.metadata, &m.Epoch, &m.CreatedAt, &m.Mode, encodedKey}
}

// projectMessages fetches message ACL and the active recipient key together.
// Nothing is cached across requests. Rows close before optional projection hooks.
func (c *Core) projectMessages(ctx context.Context, id Identity, ids []string) ([]Message, error) {
	if len(ids) == 0 {
		return []Message{}, nil
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, id.UserID, id.DeviceID)
	placeholders := make([]string, 0, len(ids))
	if len(ids) == 1 {
		args = append(args, ids[0])
		placeholders = append(placeholders, "?")
	} else {
		unique := make(map[string]bool, len(ids))
		for _, messageID := range ids {
			if !unique[messageID] {
				unique[messageID] = true
				args = append(args, messageID)
				placeholders = append(placeholders, "?")
			}
		}
	}
	rows, err := c.readQuery(ctx, `SELECT `+messageProjectionColumns+` FROM messages msg`+messageProjectionAccess+` AND msg.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	stored := make([]storedMessage, 0, len(placeholders))
	var encodedKey string
	for rows.Next() {
		var item storedMessage
		if err = rows.Scan(item.destinations(&encodedKey)...); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(stored) != len(placeholders) {
		return nil, &APIError{404, "message not accessible"}
	}
	return c.projectStoredMessages(ctx, id, stored, encodedKey)
}

// Every caller closes its SQL rows before entering hooks or cryptography.
func (c *Core) projectStoredMessages(ctx context.Context, id Identity, stored []storedMessage, encodedKey string) ([]Message, error) {
	started := diagnosticStart()
	defer func() { c.observeProjection(diagnosticElapsed(started)) }()
	out := make([]Message, 0, len(stored))
	var recipient cryptoenc.Recipient
	var recipientParsed bool
	var err error
	for _, item := range stored {
		m := item.message
		var meta messageMetadata
		if err = json.Unmarshal(item.metadata, &meta); err != nil {
			return nil, err
		}
		m.Attachments, m.ReplyTo, m.ForwardFrom = meta.Attachments, meta.ReplyTo, meta.ForwardFrom
		for _, hook := range c.Project {
			if err = hook(ctx, id, &m); err != nil {
				return nil, err
			}
		}
		if m.Deleted {
			m.Attachments, m.ReplyTo, m.ForwardFrom = nil, "", ""
			out = append(out, m)
			continue
		}
		plain, err := c.Engine.Open(item.payload, []byte("message/"+m.ID))
		if err != nil {
			return nil, err
		}
		if m.Mode == "basic" {
			if !recipientParsed {
				publicKey, e := base64.StdEncoding.DecodeString(encodedKey)
				if e != nil {
					return nil, e
				}
				recipient, err = cryptoenc.ParseRecipient(publicKey)
				if err != nil {
					return nil, err
				}
				recipientParsed = true
			}
			envelope, err := recipient.SealEnvelope(plain, cryptoenc.Binding(m.ChatID, id.UserID, id.DeviceID, m.ID))
			if err != nil {
				return nil, err
			}
			m.Envelope = &envelope
		} else {
			m.MLS = base64.StdEncoding.EncodeToString(plain)
		}
		out = append(out, m)
	}
	return out, nil
}
func (c *Core) send(w http.ResponseWriter, r *http.Request, id Identity) {
	var in MessageInput
	if !Decode(w, r, &in, int64(c.Config.Policy.MaxMessageBytes*2+16384)) {
		return
	}
	minimal := minimalPreference(r)
	msg, err := c.sendMessage(r.Context(), id, r.PathValue("chat"), in, minimal)
	if err != nil {
		statusError(w, err)
		return
	}
	if minimal {
		w.Header().Set("Preference-Applied", "return=minimal")
		JSON(w, 201, map[string]any{"status": "accepted", "receipt": acceptanceReceipt(msg)})
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
	minimal := minimalPreference(r)
	if minimal {
		w.Header().Set("Preference-Applied", "return=minimal")
	}
	results := make([]messageWriteResult, len(in.Messages))
	var jobs []messageWriteJob
	var indices []int
	bytes := 0
	pendingIDs := make(map[string]bool)
	groupLimit := maxWriteBatch
	if c.writer != nil {
		groupLimit = min(groupLimit, cap(c.writer.jobs))
	}
	flush := func() {
		if len(jobs) == 0 {
			return
		}
		committed := c.submitPrepared(jobs)
		for j, result := range committed {
			i := indices[j]
			results[i] = result
			if result.err == nil && !result.repeated {
				c.Wake(r.PathValue("chat"))
			}
		}
		jobs, indices, bytes = nil, nil, 0
		clear(pendingIDs)
	}
	for i, item := range in.Messages {
		// An earlier occurrence must become durable before retry/conflict validation,
		// including malformed replacement envelopes with the same operation ID.
		if pendingIDs[item.OperationID] {
			flush()
		}
		job, saved, err := c.prepareMessage(r.Context(), id, r.PathValue("chat"), item)
		if err != nil {
			results[i].err = err
			continue
		}
		if job == nil {
			results[i] = messageWriteResult{message: saved, repeated: true}
			continue
		}
		if len(jobs) == groupLimit || len(jobs) > 0 && job.bytes > maxWriteBatchBytes-bytes {
			flush()
		}
		jobs, indices, bytes = append(jobs, *job), append(indices, i), bytes+job.bytes
		pendingIDs[item.OperationID] = true
	}
	flush()
	for i, item := range in.Messages {
		result := results[i]
		msg, err := result.message, result.err
		if err == nil {
			msg, err = c.sendResult(r.Context(), id, msg, minimal, result.repeated)
		}
		if err != nil {
			code := 503
			var api *APIError
			if errors.As(err, &api) {
				code = api.Status
			}
			out = append(out, map[string]any{"operation_id": item.OperationID, "status": code, "error": err.Error()})
		} else if minimal {
			out = append(out, map[string]any{"operation_id": item.OperationID, "status": 201, "receipt": acceptanceReceipt(msg)})
		} else {
			out = append(out, map[string]any{"operation_id": item.OperationID, "status": 201, "message": msg})
		}
	}
	JSON(w, 207, out)
}
func (c *Core) history(w http.ResponseWriter, r *http.Request, id Identity) {
	chat := r.PathValue("chat")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit := 100
	if v, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && v > 0 && v <= 200 {
		limit = v
	}
	rows, err := c.readQuery(r.Context(), `SELECT `+nullableMessageProjectionColumns+` FROM members member JOIN chats c ON c.id=member.chat_id JOIN devices d ON d.user_id=member.user_id JOIN users u ON u.id=member.user_id LEFT JOIN messages msg ON msg.chat_id=c.id AND msg.seq>? AND msg.seq>=member.joined_seq WHERE member.chat_id=? AND member.user_id=? AND d.id=? AND member.active=1 AND d.revoked=0 AND u.disabled=0 ORDER BY msg.seq LIMIT ?`, after, chat, id.UserID, id.DeviceID, limit)
	if err != nil {
		statusError(w, err)
		return
	}
	stored := []storedMessage{}
	var encodedKey string
	authorized := false
	for rows.Next() {
		var item storedMessage
		if err = rows.Scan(item.destinations(&encodedKey)...); err != nil {
			rows.Close()
			statusError(w, err)
			return
		}
		authorized = true
		if item.message.ID != "" {
			stored = append(stored, item)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		statusError(w, err)
		return
	}
	if !authorized {
		Error(w, 403, "membership required")
		return
	}
	out, err := c.projectStoredMessages(r.Context(), id, stored, encodedKey)
	if err != nil {
		statusError(w, err)
		return
	}
	JSON(w, 200, out)
}
func (c *Core) Events(ctx context.Context, id Identity, chat string, after int64, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		return nil, &APIError{400, "event limit must be 1..200"}
	}
	var current, joined int64
	var minimum sql.NullInt64
	err := c.readQueryRow(ctx, `SELECT c.seq,m.joined_seq,CASE WHEN c.seq>? THEN (SELECT MIN(seq) FROM events WHERE chat_id=c.id) END FROM chats c JOIN members m ON m.chat_id=c.id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE c.id=? AND m.user_id=? AND d.id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0`, after, chat, id.UserID, id.DeviceID).Scan(&current, &joined, &minimum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &APIError{403, "membership required"}
	}
	if err != nil {
		return nil, err
	}
	if after < joined-1 {
		after = joined - 1
	}
	if after < current && (!minimum.Valid || after+1 < minimum.Int64) {
		return nil, &APIError{410, "cursor expired; sync history and current state"}
	}
	if after > current {
		return nil, &APIError{400, "cursor ahead of chat"}
	}
	if after == current {
		return []Event{}, nil
	}
	// Fetch the bounded event page and current message state in one snapshot.
	// A one-event page needs no ranking or temporary sort. Bound that fast path
	// to the observed head; a concurrent append remains for the next
	// replay call, rather than expanding this query beyond its validated head.
	// Rank within the page avoids copying the same payload for repeated events.
	// LEFT JOIN retains non-message events and exposes inaccessible references
	// explicitly rather than silently dropping them. ACL and key are fresh here.
	query := `WITH page AS (SELECT seq,kind,message_id,data FROM events WHERE chat_id=? AND seq>? ORDER BY seq LIMIT ?), ranked AS (SELECT *,ROW_NUMBER() OVER (PARTITION BY message_id ORDER BY seq) AS message_rank FROM page)
SELECT page.seq,page.kind,page.message_id,page.data,(d.id IS NOT NULL AND u.id IS NOT NULL),
` + nullableMessageProjectionColumns + `
FROM ranked page JOIN chats c ON c.id=?
LEFT JOIN members member ON member.chat_id=c.id AND member.user_id=? AND member.active=1
LEFT JOIN devices d ON d.user_id=member.user_id AND d.id=? AND d.revoked=0
LEFT JOIN users u ON u.id=member.user_id AND u.disabled=0
LEFT JOIN messages msg ON page.message_rank=1 AND msg.id=page.message_id AND msg.chat_id=c.id AND msg.seq>=member.joined_seq AND d.id IS NOT NULL AND u.id IS NOT NULL
ORDER BY page.seq`
	var args []any
	if current-after == 1 || limit == 1 {
		query = `SELECT page.seq,page.kind,page.message_id,page.data,(d.id IS NOT NULL AND u.id IS NOT NULL),
` + nullableMessageProjectionColumns + `
FROM (SELECT seq,kind,message_id,data FROM events WHERE chat_id=? AND seq>? AND seq<=? ORDER BY seq LIMIT 1) page JOIN chats c ON c.id=?
LEFT JOIN members member ON member.chat_id=c.id AND member.user_id=? AND member.active=1
LEFT JOIN devices d ON d.user_id=member.user_id AND d.id=? AND d.revoked=0
LEFT JOIN users u ON u.id=member.user_id AND u.disabled=0
LEFT JOIN messages msg ON msg.id=page.message_id AND msg.chat_id=c.id AND msg.seq>=member.joined_seq AND d.id IS NOT NULL AND u.id IS NOT NULL`
		args = []any{chat, after, current, chat, id.UserID, id.DeviceID}
	} else {
		args = []any{chat, after, limit, chat, id.UserID, id.DeviceID}
	}
	rows, err := c.readQuery(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	type item struct {
		event        Event
		data         []byte
		messageIndex int
	}
	items := []item{}
	stored := []storedMessage{}
	indices := make(map[string]int)
	var encodedKey string
	for rows.Next() {
		it := item{event: Event{ChatID: chat}, messageIndex: -1}
		var message storedMessage
		var authorized bool
		destinations := append([]any{&it.event.Seq, &it.event.Type, &it.event.MessageID, &it.data, &authorized}, message.destinations(&encodedKey)...)
		if err = rows.Scan(destinations...); err != nil {
			rows.Close()
			return nil, err
		}
		if !authorized {
			rows.Close()
			return nil, &APIError{403, "membership required"}
		}
		if it.event.MessageID != "" {
			index, present := indices[it.event.MessageID]
			if !present {
				if message.message.ID == "" {
					rows.Close()
					return nil, &APIError{404, "message not accessible"}
				}
				index = len(stored)
				indices[it.event.MessageID] = index
				stored = append(stored, message)
			}
			it.messageIndex = index
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	messages, err := c.projectStoredMessages(ctx, id, stored, encodedKey)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(items))
	for _, it := range items {
		if it.event.MessageID != "" {
			it.event.Data = messages[it.messageIndex]
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
	_ = c.readQueryRow(r.Context(), `SELECT seq FROM chats WHERE id=?`, chat).Scan(&current)
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
