//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type Runtime struct {
	cfg               Config
	store             *Store
	participant       *Participant
	chat              string
	transport         *HTTPTransport
	handlers          map[string]Handler
	sem               chan struct{}
	mu                sync.Mutex
	frozen            bool
	requireRelayState bool
}

func OpenRuntime(ctx context.Context, c Config, chat string) (*Runtime, error) {
	if chat == "" {
		if len(c.Chats) == 1 {
			for name := range c.Chats {
				chat = name
			}
		} else {
			chat = "default"
		}
	}
	s, err := OpenStore(c)
	if err != nil {
		return nil, err
	}
	p, err := loadParticipant(ctx, s, c, chat)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("microsafer: load MLS state: %w", err)
	}
	r := NewRuntime(c, s, p, chat)
	r.requireRelayState = true
	return r, nil
}
func NewRuntime(c Config, s *Store, p *Participant, chat string) *Runtime {
	c.setDefaults()
	r := &Runtime{cfg: c, store: s, participant: p, chat: chat, handlers: map[string]Handler{}, sem: make(chan struct{}, c.Endpoint.MaxConcurrent)}
	r.transport = &HTTPTransport{BaseURL: c.Endpoint.ServerURL, TokenEnv: c.Endpoint.TokenEnv, Client: &http.Client{Timeout: time.Duration(c.Endpoint.RequestTimeoutMS) * time.Millisecond, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, MaxBody: c.Endpoint.MaxBodyBytes}
	for name, h := range c.Handlers {
		require := h.RequireApproval
		if !h.ApprovalSet {
			require = true
		}
		r.handlers[name] = Handler{Name: name, RequireApproval: require}
	}
	return r
}
func (r *Runtime) Config() Config            { return r.cfg }
func (r *Runtime) Participant() *Participant { return r.participant }
func (r *Runtime) Frozen() bool              { r.mu.Lock(); defer r.mu.Unlock(); return r.frozen }
func (r *Runtime) freeze()                   { r.mu.Lock(); r.frozen = true; r.mu.Unlock() }

type RelayMessage struct {
	OperationID  string `json:"operation_id"`
	SenderUser   string `json:"sender_user"`
	SenderDevice string `json:"sender_device"`
	Epoch        uint64 `json:"epoch"`
	Wire         string `json:"wire"`
}
type PollResult struct {
	Messages []RelayMessage `json:"messages"`
	Cursor   string         `json:"cursor"`
}

// UnmarshalJSON accepts both the standalone relay page and QGramm core's
// `[]Event` response. Core events carry the actual Message under data with
// fields operation_id/mls/epoch/sender/device_id/seq.
func (p *PollResult) UnmarshalJSON(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("microsafer: empty poll response")
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var events []struct {
			Seq  int64           `json:"seq"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &events); err != nil {
			return err
		}
		p.Messages = make([]RelayMessage, 0, len(events))
		for _, event := range events {
			// Core's event sequence is the cursor, including tombstones,
			// notices, and messages that this endpoint must ignore.
			p.Cursor = strconv.FormatInt(event.Seq, 10)
			if len(bytes.TrimSpace(event.Data)) == 0 || bytes.Equal(bytes.TrimSpace(event.Data), []byte("null")) {
				continue
			}
			var msg RelayMessage
			if err := json.Unmarshal(event.Data, &msg); err != nil {
				continue
			}
			if msg.Wire == "" {
				var coreMessage struct {
					MLS    string `json:"mls"`
					Sender string `json:"sender"`
					Device string `json:"device_id"`
				}
				if err := json.Unmarshal(event.Data, &coreMessage); err != nil {
					continue
				}
				msg.Wire, msg.SenderUser, msg.SenderDevice = coreMessage.MLS, coreMessage.Sender, coreMessage.Device
			}
			if msg.OperationID == "" || msg.Wire == "" {
				continue
			}
			if msg.Epoch == 0 {
				var envelope struct {
					Epoch uint64 `json:"epoch"`
				}
				_ = json.Unmarshal(event.Data, &envelope)
				msg.Epoch = envelope.Epoch
			}
			p.Messages = append(p.Messages, msg)
		}
		return nil
	}
	type pollAlias PollResult
	var out pollAlias
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*p = PollResult(out)
	return nil
}

// Run uses bounded HTTP polling.  It flushes durable ciphertext before
// reading more work and stops on an epoch/membership mismatch until an
// operator performs an offline authoritative rejoin.
func (r *Runtime) Run(ctx context.Context) error {
	if r.transport == nil {
		return errors.New("microsafer: transport is nil")
	}
	interval := time.Duration(r.cfg.Endpoint.PollIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	for {
		if err := r.drainInbox(ctx); err != nil {
			r.freeze()
			return err
		}
		if err := r.verifyRelayState(ctx); err != nil {
			r.freeze()
			return err
		}
		if err := r.flushOutbox(ctx); err != nil {
			return err
		}
		if r.Frozen() {
			return errors.New("microsafer: relay epoch or membership changed; offline rejoin required")
		}
		cursor, cursorErr := r.store.GetCursor(ctx, r.chat)
		if cursorErr != nil && !errors.Is(cursorErr, sql.ErrNoRows) {
			r.freeze()
			return cursorErr
		}
		poll, e := r.transport.Poll(ctx, r.chat, cursor)
		if e != nil {
			return e
		}
		for _, m := range poll.Messages {
			if e = r.receive(ctx, m); e != nil {
				r.freeze()
				return e
			}
		}
		if poll.Cursor != cursor {
			tx, e := r.store.db.BeginTx(ctx, nil)
			if e != nil {
				r.freeze()
				return e
			}
			if e = r.store.SetCursorTx(ctx, tx, r.chat, poll.Cursor); e == nil {
				e = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if e != nil {
				r.freeze()
				return e
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
func (r *Runtime) receive(ctx context.Context, m RelayMessage) error {
	if m.OperationID == "" {
		return errors.New("microsafer: relay operation_id is required")
	}
	// The relay echoes this endpoint's own ciphertext.  It is already in the
	// durable outbox and must advance the cursor without consuming MLS receive
	// state or attempting to process its RPC as an inbound request.
	if m.SenderUser == r.participant.User && m.SenderDevice == r.participant.Device {
		return nil
	}
	if m.Epoch != r.participant.MLS.Epoch() {
		return errors.New("microsafer: relay epoch mismatch")
	}
	item, e := r.store.GetInbox(ctx, m.OperationID)
	if e == nil {
		if item.Epoch != m.Epoch || item.SenderUser != m.SenderUser || item.SenderDevice != m.SenderDevice {
			return errors.New("microsafer: inbox relay identity mismatch")
		}
		return r.processInbox(ctx, item)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	wire, e := base64.StdEncoding.Strict().DecodeString(m.Wire)
	if e != nil {
		return e
	}
	pt, snap, e := r.participant.decryptForInbox(r.chat, m.SenderUser, m.SenderDevice, m.OperationID, wire)
	if e != nil {
		return e
	}
	var req RPCRequest
	decodeErr := json.Unmarshal(pt, &req)
	if decodeErr != nil || req.RequestID == "" || req.Chat != r.chat || (req.SourcePeer != m.SenderDevice && req.SourcePeer != m.SenderUser+"/"+m.SenderDevice) {
		// Preserve the ratchet snapshot and route the malformed application
		// envelope through the ordinary encrypted rejection path. The original
		// plaintext is never echoed into the error response.
		req = RPCRequest{Version: 1, RequestID: m.OperationID, ClientID: "malformed-" + m.OperationID, Chat: r.chat, SourcePeer: m.SenderDevice, Action: "invalid", Name: "invalid", Body: json.RawMessage(`{"error":"malformed RPC"}`)}
	}
	tx, e := r.store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = r.store.PutStateTx(ctx, tx, "mls/"+r.chat, snap); e != nil {
		r.freeze()
		return e
	}
	if e = r.store.PutInboxTx(ctx, tx, InboxItem{OperationID: m.OperationID, Chat: r.chat, Epoch: m.Epoch, SenderUser: m.SenderUser, SenderDevice: m.SenderDevice, Status: "pending", Request: req}); e != nil {
		r.freeze()
		return e
	}
	if e = tx.Commit(); e != nil {
		r.freeze()
		return e
	}
	return r.processInbox(ctx, InboxItem{OperationID: m.OperationID, Chat: r.chat, Epoch: m.Epoch, SenderUser: m.SenderUser, SenderDevice: m.SenderDevice, Status: "pending", Request: req})
}
func (r *Runtime) processInbox(ctx context.Context, item InboxItem) error {
	if item.Status != "pending" {
		return nil
	}
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	responses, err := r.ProcessRPC(ctx, item.Request)
	<-r.sem
	if err != nil {
		if isApplicationRPCError(err) {
			if e2 := r.store.UpdateInbox(ctx, item.OperationID, "rejected"); e2 != nil {
				return e2
			}
			message := err.Error()
			if len(message) > 512 {
				message = message[:512]
			}
			resp := RPCResponse{RequestID: item.Request.RequestID, Kind: "error", Seq: 1, Final: true}
			resp.Body, _ = json.Marshal(map[string]string{"error": message})
			if e2 := r.persistResponse(ctx, item.Request, resp); e2 != nil {
				return e2
			}
			return nil
		}
		_ = r.store.UpdateInbox(ctx, item.OperationID, "rejected")
		return err
	}
	status := "done"
	for _, resp := range responses {
		if resp.Kind == "uncertain" {
			status = "uncertain"
			break
		}
	}
	return r.store.UpdateInbox(ctx, item.OperationID, status)
}

func isApplicationRPCError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, prefix := range []string{
		"microsafer: unsupported RPC", "microsafer: RPC fields", "microsafer: RPC body",
		"microsafer: unknown chat", "microsafer: handler", "microsafer: grant",
		"microsafer: external ", "microsafer: model ", "microsafer: configured ",
		"microsafer: MCP ", "microsafer: tool ", "microsafer: storage ",
		"microsafer: client_id is already bound",
	} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
func (r *Runtime) drainInbox(ctx context.Context) error {
	items, err := r.store.PendingInbox(ctx, r.chat)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = r.processInbox(ctx, item); err != nil {
			return err
		}
	}
	return nil
}
func (r *Runtime) flushOutbox(ctx context.Context) error {
	items, e := r.store.PendingOutbox(ctx)
	if e != nil {
		r.freeze()
		return e
	}
	for _, item := range items {
		if r.requireRelayState {
			if e = r.verifyRelayState(ctx); e != nil {
				r.freeze()
				return e
			}
		}
		if e = r.transport.Send(ctx, r.chat, item); e != nil {
			return e
		}
		if e = r.store.MarkOutboxSent(ctx, item.OperationID); e != nil {
			r.freeze()
			return e
		}
	}
	return nil
}

func (r *Runtime) verifyRelayState(ctx context.Context) error {
	state, err := r.transport.CheckMLS(ctx, r.chat)
	if err != nil {
		return err
	}
	if state.PendingRekey {
		return errors.New("microsafer: relay has pending MLS rekey")
	}
	wantID := base64.StdEncoding.EncodeToString(r.participant.MLS.GroupID())
	wantContext := base64.StdEncoding.EncodeToString(r.participant.MLS.GroupContext())
	if state.GroupID != wantID || state.GroupContext != wantContext || state.Epoch != r.participant.MLS.Epoch() {
		return errors.New("microsafer: relay MLS state differs from durable participant")
	}
	peer, ok := r.cfg.peerForChat(r.chat)
	if !ok {
		return errors.New("microsafer: no offline peer pin for chat")
	}
	seenSelf, seenPeer := false, false
	for _, member := range state.Roster {
		if member.DeviceID == r.participant.Device {
			seenSelf = true
		}
		if member.DeviceID == peer.Device {
			seenPeer = true
		}
	}
	if !seenSelf || !seenPeer || len(state.Roster) != 2 {
		return errors.New("microsafer: relay MLS roster mismatch")
	}
	return nil
}

func (r *Runtime) encryptResponse(ctx context.Context, req RPCRequest, resp RPCResponse) ([]byte, error) {
	if r.requireRelayState {
		if err := r.verifyRelayState(ctx); err != nil {
			r.freeze()
			return nil, err
		}
	}
	wireBody, e := json.Marshal(resp)
	if e != nil {
		return nil, e
	}
	if len(wireBody) > r.cfg.Endpoint.MaxFrameBytes {
		return nil, errors.New("microsafer: encrypted response frame exceeds limit")
	}
	r.participant.mu.Lock()
	defer r.participant.mu.Unlock()
	operationID := responseOperationID(req, resp.Seq, r.participant.Device)
	wire, e := r.participant.MLS.Encrypt(wireBody, cryptoenc.Binding(req.Chat, r.participant.User, r.participant.Device, operationID))
	if e != nil {
		return nil, e
	}
	snap, e := r.participant.MLS.Snapshot(r.store.Engine(), r.participant.snapshotAAD())
	if e != nil {
		r.freeze()
		return nil, e
	}
	tx, e := r.store.db.BeginTx(ctx, nil)
	if e != nil {
		r.freeze()
		return nil, e
	}
	if e = r.store.PutStateTx(ctx, tx, "mls/"+r.chat, snap); e == nil {
		e = r.store.SaveOutboxEpochTx(ctx, tx, operationID, resp.Seq, r.participant.MLS.Epoch(), wire)
	}
	if e == nil {
		e = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if e != nil {
		r.freeze()
	}
	return wire, e
}

// responseOperationID is a Core-safe stable operation ID. Core rejects slash
// and other arbitrary request-id characters; the same digest is used for the
// MLS AAD, durable outbox row, and HTTP MessageInput.
func responseOperationID(req RPCRequest, seq uint64, endpointDevice string) string {
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", req.RequestID, req.ClientID, req.Chat, endpointDevice, seq)
	digest := sha256.Sum256([]byte(seed))
	return "ai-endpoint-" + hex.EncodeToString(digest[:])
}

type HTTPTransport struct {
	BaseURL, TokenEnv string
	Client            *http.Client
	MaxBody           int
}

type RelayMLSState struct {
	GroupID      string `json:"group_id"`
	GroupContext string `json:"group_context"`
	Epoch        uint64 `json:"epoch"`
	PendingRekey bool   `json:"pending_rekey"`
	Roster       []struct {
		DeviceID string `json:"device_id"`
	} `json:"roster"`
}

func (t *HTTPTransport) CheckMLS(ctx context.Context, chat string) (RelayMLSState, error) {
	u := strings.TrimRight(t.BaseURL, "/") + "/v1/chats/" + url.PathEscape(chat) + "/mls"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return RelayMLSState{}, err
	}
	t.auth(req)
	resp, err := t.client().Do(req)
	if err != nil {
		return RelayMLSState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return RelayMLSState{}, fmt.Errorf("microsafer: relay MLS state status %s", resp.Status)
	}
	var out RelayMLSState
	if err = json.NewDecoder(io.LimitReader(resp.Body, int64(maxInt(t.MaxBody, 1<<20)))).Decode(&out); err != nil {
		return RelayMLSState{}, err
	}
	return out, nil
}

func (t *HTTPTransport) auth(req *http.Request) {
	if v := strings.TrimSpace(os.Getenv(t.TokenEnv)); v != "" {
		req.Header.Set("Authorization", "Bearer "+v)
	}
}
func (t *HTTPTransport) Poll(ctx context.Context, chat, cursor string) (PollResult, error) {
	base := strings.TrimRight(t.BaseURL, "/") + "/v1/chats/" + url.PathEscape(chat) + "/events"
	u, err := url.Parse(base)
	if err != nil {
		return PollResult{}, err
	}
	q := u.Query()
	if cursor != "" {
		q.Set("after", cursor)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return PollResult{}, err
	}
	t.auth(req)
	resp, err := t.client().Do(req)
	if err != nil {
		return PollResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return PollResult{}, fmt.Errorf("microsafer: relay poll status %s", resp.Status)
	}
	if t.MaxBody <= 0 {
		t.MaxBody = 1 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.MaxBody)+1))
	if err != nil {
		return PollResult{}, err
	}
	if len(raw) > t.MaxBody {
		return PollResult{}, errors.New("microsafer: relay poll response exceeds limit")
	}
	var out PollResult
	if err = json.Unmarshal(raw, &out); err != nil {
		return PollResult{}, err
	}
	return out, nil
}
func (t *HTTPTransport) Send(ctx context.Context, chat string, item OutboxItem) error {
	payload := map[string]any{"operation_id": item.OperationID, "mls": base64.StdEncoding.EncodeToString(item.Wire), "epoch": item.Epoch}
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	u := strings.TrimRight(t.BaseURL, "/") + "/v1/chats/" + url.PathEscape(chat) + "/messages"
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	t.auth(req)
	resp, e := t.client().Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("microsafer: relay send status %s", resp.Status)
	}
	return nil
}
func (t *HTTPTransport) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
