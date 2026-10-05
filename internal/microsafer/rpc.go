//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type RPCRequest struct {
	Version    int             `json:"version"`
	RequestID  string          `json:"request_id"`
	ClientID   string          `json:"client_id"`
	Chat       string          `json:"chat"`
	SourcePeer string          `json:"source_peer"`
	Action     string          `json:"action"`
	Name       string          `json:"name"`
	Body       json.RawMessage `json:"body"`
	Grant      string          `json:"grant,omitempty"`
}

type RPCResponse struct {
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"` // notice, delta, result, error, uncertain
	Seq       uint64          `json:"seq"`
	Final     bool            `json:"final"`
	Body      json.RawMessage `json:"body,omitempty"`
}

type GrantClaims struct {
	RequestID       string `json:"request_id"`
	Chat            string `json:"chat"`
	SourcePeer      string `json:"source_peer"`
	TargetEndpoint  string `json:"target_endpoint"`
	Action          string `json:"action"`
	Name            string `json:"name"`
	BodyHash        string `json:"body_hash"`
	DestinationHash string `json:"destination_hash"`
	Epoch           uint64 `json:"epoch"`
	GroupID         string `json:"group_id"`
	GroupContext    string `json:"group_context"`
	Nonce           string `json:"nonce"`
	Issuer          string `json:"iss"`
	Audience        string `json:"aud"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
}

type GrantExpectation struct {
	RequestID, Chat, SourcePeer, TargetEndpoint, Action, Name string
	BodyHash, DestinationHash, GroupID, GroupContext          string
	Issuer, Audience                                          string
	Epoch                                                     uint64
}

func CanonicalJSON(raw []byte) ([]byte, error) {
	if err := rejectDuplicateJSON(raw); err != nil {
		return nil, err
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return nil, errors.New("microsafer: trailing JSON")
	}
	return json.Marshal(v)
}
func rejectDuplicateJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := walkJSONToken(d); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("microsafer: trailing JSON")
	}
	return nil
}
func walkJSONToken(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("microsafer: duplicate or invalid JSON key")
				}
				seen[name] = true
				if err = walkJSONToken(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		if v == '[' {
			for d.More() {
				if err = walkJSONToken(d); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return errors.New("microsafer: invalid JSON delimiter")
	}
	return nil
}
func RequestHash(raw []byte) ([]byte, error) {
	c, e := CanonicalJSON(raw)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(c)
	return h[:], nil
}
func (r RPCRequest) CanonicalBody() ([]byte, error) {
	if len(r.Body) == 0 {
		return []byte("null"), nil
	}
	return CanonicalJSON(r.Body)
}
func (r RPCRequest) Hash() ([]byte, error) {
	body, err := r.CanonicalBody()
	if err != nil {
		return nil, err
	}
	// The intent key is bound to the complete immutable request projection.
	// Hashing only body allowed a client_id to be replayed with another chat,
	// action or request id and receive the old cached response.
	projection := struct {
		Version    int             `json:"version"`
		RequestID  string          `json:"request_id"`
		ClientID   string          `json:"client_id"`
		Chat       string          `json:"chat"`
		SourcePeer string          `json:"source_peer"`
		Action     string          `json:"action"`
		Name       string          `json:"name"`
		Body       json.RawMessage `json:"body"`
	}{r.Version, r.RequestID, r.ClientID, r.Chat, r.SourcePeer, r.Action, r.Name, body}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil, err
	}
	return RequestHash(encoded)
}
func (r RPCRequest) Validate(maxBody int) error {
	if r.Version != 1 {
		return errors.New("microsafer: unsupported RPC version")
	}
	if r.RequestID == "" || r.ClientID == "" || r.Chat == "" || r.SourcePeer == "" || r.Action == "" || r.Name == "" {
		return errors.New("microsafer: RPC fields request_id/client_id/chat/source_peer/action/name are required")
	}
	if len(r.RequestID) > 128 || len(r.ClientID) > 128 || !safeCoreID(r.RequestID) || !safeCoreID(r.ClientID) {
		return errors.New("microsafer: request_id/client_id must be <=128 safe characters")
	}
	if len(r.Body) > maxBody {
		return errors.New("microsafer: RPC body exceeds limit")
	}
	_, err := r.CanonicalBody()
	return err
}

func safeCoreID(value string) bool {
	for _, c := range value {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func GrantDestinationHash(destination string, epoch uint64, groupID, groupContext string) string {
	b, _ := json.Marshal(struct {
		Destination  string `json:"destination"`
		Epoch        uint64 `json:"epoch"`
		GroupID      string `json:"group_id"`
		GroupContext string `json:"group_context"`
	}{destination, epoch, groupID, groupContext})
	h, _ := RequestHash(b)
	return base64.RawURLEncoding.EncodeToString(h)
}
func VerifyGrant(raw string, pub ed25519.PublicKey, expected GrantExpectation, now time.Time) (GrantClaims, []byte, error) {
	if len(pub) != ed25519.PublicKeySize || strings.TrimSpace(raw) == "" || len(raw) > 16<<10 {
		return GrantClaims{}, nil, errors.New("microsafer: invalid grant key/token")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return GrantClaims{}, nil, errors.New("microsafer: malformed compact grant")
	}
	header, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(header) > 4096 || rejectDuplicateJSON(header) != nil {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant header")
	}
	var hfields map[string]json.RawMessage
	if err = json.Unmarshal(header, &hfields); err != nil {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant header")
	}
	for k := range hfields {
		if k != "alg" && k != "typ" {
			return GrantClaims{}, nil, fmt.Errorf("microsafer: unknown grant header %q", k)
		}
	}
	var alg string
	if err = json.Unmarshal(hfields["alg"], &alg); err != nil || alg != "EdDSA" {
		return GrantClaims{}, nil, errors.New("microsafer: grant must use EdDSA")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(payload) > 12<<10 || rejectDuplicateJSON(payload) != nil {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant claims")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(payload, &fields); err != nil {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant claims")
	}
	allowed := map[string]bool{"request_id": true, "chat": true, "source_peer": true, "target_endpoint": true, "action": true, "name": true, "body_hash": true, "destination_hash": true, "epoch": true, "group_id": true, "group_context": true, "nonce": true, "iat": true, "exp": true, "iss": true, "aud": true}
	for k := range fields {
		if !allowed[k] {
			return GrantClaims{}, nil, fmt.Errorf("microsafer: unknown grant claim %q", k)
		}
	}
	for _, k := range []string{"request_id", "chat", "source_peer", "target_endpoint", "action", "name", "body_hash", "destination_hash", "epoch", "group_id", "group_context", "nonce", "iat", "exp", "iss", "aud"} {
		if _, ok := fields[k]; !ok {
			return GrantClaims{}, nil, fmt.Errorf("microsafer: required grant claim %q is missing", k)
		}
	}
	var mc jwt.MapClaims
	options := []jwt.ParserOption{jwt.WithValidMethods([]string{"EdDSA"})}
	if expected.Issuer == "" || expected.Audience == "" {
		return GrantClaims{}, nil, errors.New("microsafer: issuer and audience are required")
	}
	options = append(options, jwt.WithIssuer(expected.Issuer), jwt.WithAudience(expected.Audience))
	tok, err := jwt.ParseWithClaims(raw, &mc, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, errors.New("microsafer: grant must use EdDSA")
		}
		return pub, nil
	}, options...)
	if err != nil || !tok.Valid {
		if err == nil {
			err = errors.New("invalid token")
		}
		return GrantClaims{}, nil, fmt.Errorf("microsafer: grant verification failed: %w", err)
	}
	var c GrantClaims
	if err = json.Unmarshal(payload, &c); err != nil {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant claims")
	}
	for _, triple := range [][3]string{{"request_id", expected.RequestID, c.RequestID}, {"chat", expected.Chat, c.Chat}, {"source_peer", expected.SourcePeer, c.SourcePeer}, {"target_endpoint", expected.TargetEndpoint, c.TargetEndpoint}, {"action", expected.Action, c.Action}, {"name", expected.Name, c.Name}, {"body_hash", expected.BodyHash, c.BodyHash}, {"destination_hash", expected.DestinationHash, c.DestinationHash}, {"group_id", expected.GroupID, c.GroupID}, {"group_context", expected.GroupContext, c.GroupContext}, {"iss", expected.Issuer, c.Issuer}, {"aud", expected.Audience, c.Audience}} {
		if triple[1] != "" && triple[2] != triple[1] {
			return GrantClaims{}, nil, fmt.Errorf("microsafer: grant %s mismatch", triple[0])
		}
	}
	if c.Nonce == "" || len(c.Nonce) > 256 || c.BodyHash == "" || c.DestinationHash == "" || c.IssuedAt <= 0 || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > 900 {
		return GrantClaims{}, nil, errors.New("microsafer: malformed grant lifetime/nonce")
	}
	if c.Epoch != expected.Epoch {
		return GrantClaims{}, nil, errors.New("microsafer: grant epoch mismatch")
	}
	n := now.Unix()
	if n < c.IssuedAt-30 || n >= c.ExpiresAt {
		return GrantClaims{}, nil, errors.New("microsafer: grant is outside its lifetime")
	}
	h := sha256.Sum256([]byte(raw))
	return c, h[:], nil
}

type Handler struct {
	Name            string
	RequireApproval bool
	Fn              func(context.Context, json.RawMessage) (json.RawMessage, error)
	FnRPC           func(context.Context, RPCRequest) (json.RawMessage, error)
	StreamRPC       func(context.Context, RPCRequest, ModelResponseFunc) error
}
type HandlerFunc func(context.Context, json.RawMessage) (json.RawMessage, error)

func (r *Runtime) RegisterHandler(name string, requireApproval bool, fn HandlerFunc) error {
	if name == "" || fn == nil {
		return errors.New("microsafer: handler name and function required")
	}
	r.handlers[name] = Handler{Name: name, RequireApproval: requireApproval, Fn: fn}
	return nil
}
func (r *Runtime) RegisterRPCHandler(name string, requireApproval bool, fn func(context.Context, RPCRequest) (json.RawMessage, error)) error {
	if name == "" || fn == nil {
		return errors.New("microsafer: handler name and function required")
	}
	r.handlers[name] = Handler{Name: name, RequireApproval: requireApproval, FnRPC: fn}
	return nil
}

func (r *Runtime) ProcessRPC(ctx context.Context, req RPCRequest) ([]RPCResponse, error) {
	if err := req.Validate(r.cfg.Endpoint.MaxBodyBytes); err != nil {
		return nil, err
	}
	if r.requireRelayState {
		if err := r.verifyRelayState(ctx); err != nil {
			r.freeze()
			return nil, err
		}
	}
	if req.Chat != r.chat {
		return nil, errors.New("microsafer: unknown chat")
	}
	h, err := req.Hash()
	if err != nil {
		return nil, err
	}
	existing, e := r.store.getIntent(ctx, req.ClientID)
	if e == nil {
		if !bytes.Equal(existing.Hash, h) {
			return nil, errors.New("microsafer: client_id is already bound to another request")
		}
		if existing.Status == "uncertain" || existing.Status == "dispatched" {
			if responses, decodeErr := r.decodeResponses(req.RequestID, existing.Value); decodeErr == nil && len(responses) > 0 {
				return responses, nil
			}
			return r.uncertainResponse(ctx, req, req.ClientID, existing.OperationID, 1, "external effect uncertain after restart")
		}
		return r.decodeResponses(req.RequestID, existing.Value)
	} else if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	handler, ok := r.handlers[req.Name]
	if !ok {
		return nil, fmt.Errorf("microsafer: handler %q is not configured", req.Name)
	}
	if handler.Fn == nil && handler.FnRPC == nil && handler.StreamRPC == nil {
		return nil, fmt.Errorf("microsafer: handler %q has no implementation", req.Name)
	}
	if err = r.validateHandlerDestination(req.Name); err != nil {
		return nil, err
	}
	bodyHashBytes, err := RequestHash(req.Body)
	if err != nil {
		return nil, err
	}
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyHashBytes)
	epoch := r.participant.MLS.Epoch()
	groupID := base64.StdEncoding.EncodeToString(r.participant.MLS.GroupID())
	groupContext := base64.StdEncoding.EncodeToString(r.participant.MLS.GroupContext())
	destinationURL := r.handlerDestination(req.Name)
	destination := GrantDestinationHash(destinationURL, epoch, groupID, groupContext)
	var grant GrantClaims
	var grantHash []byte
	if handler.RequireApproval || r.cfg.Handlers[req.Name].RequireApproval {
		pub, pe := r.grantPublicKey()
		if pe != nil {
			return nil, pe
		}
		grant, grantHash, pe = VerifyGrant(req.Grant, pub, GrantExpectation{RequestID: req.RequestID, Chat: req.Chat, SourcePeer: req.SourcePeer, TargetEndpoint: r.cfg.Endpoint.Name, Action: req.Action, Name: req.Name, BodyHash: bodyHash, DestinationHash: destination, Epoch: epoch, GroupID: groupID, GroupContext: groupContext, Issuer: r.cfg.Endpoint.Issuer, Audience: r.cfg.Endpoint.Audience}, time.Now())
		if pe != nil {
			return nil, pe
		}
	}
	op := req.RequestID
	if _, e = uuid.Parse(op); e != nil {
		op = uuid.NewSHA1(uuid.NameSpaceOID, []byte(req.RequestID)).String()
	}
	// Mark dispatched and consume the one-use grant before invoking the external
	// handler.  A restart sees dispatched/uncertain and never retries it.
	tx, e := r.store.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if len(grantHash) > 0 {
		if e = r.store.ConsumeGrantTx(ctx, tx, grant.Nonce, grantHash); e != nil {
			return nil, e
		}
	}
	intent := map[string]any{"request": req, "responses": []RPCResponse{}}
	if e = r.store.PutIntentTx(ctx, tx, req.ClientID, h, op, "dispatched", intent); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	noticeBody := r.handlerNoticeBody(req, op)
	if e = r.persistResponse(ctx, req, RPCResponse{RequestID: req.RequestID, Kind: "notice", Seq: 1, Final: false, Body: noticeBody}); e != nil {
		return r.uncertainResponse(ctx, req, req.ClientID, op, 2, "notice durability failed")
	}
	if handler.StreamRPC != nil {
		var last *RPCResponse
		e = handler.StreamRPC(ctx, req, func(resp RPCResponse) error {
			resp.RequestID = req.RequestID
			if err := r.persistResponse(ctx, req, resp); err != nil {
				return err
			}
			copy := resp
			last = &copy
			return nil
		})
		if e != nil {
			seq := uint64(1)
			if last != nil {
				seq = last.Seq + 1
			}
			return r.uncertainResponse(ctx, req, req.ClientID, op, seq, e.Error())
		}
		if last == nil || !last.Final {
			return r.uncertainResponse(ctx, req, req.ClientID, op, 1, "stream ended without a final response")
		}
		data, _ := json.Marshal([]RPCResponse{*last})
		if e = r.store.UpdateIntent(ctx, req.ClientID, "done", map[string]any{"responses": json.RawMessage(data)}); e != nil {
			return nil, e
		}
		return []RPCResponse{*last}, nil
	}
	var out json.RawMessage
	if handler.FnRPC != nil {
		out, e = handler.FnRPC(ctx, req)
	} else {
		out, e = handler.Fn(ctx, req.Body)
	}
	if e != nil {
		return r.uncertainResponse(ctx, req, req.ClientID, op, 2, e.Error())
	}
	resp := RPCResponse{RequestID: req.RequestID, Kind: "result", Seq: 2, Final: true, Body: out}
	if e = r.persistResponse(ctx, req, resp); e != nil {
		return nil, e
	}
	data, _ := json.Marshal([]RPCResponse{resp})
	if e = r.store.UpdateIntent(ctx, req.ClientID, "done", map[string]any{"responses": json.RawMessage(data)}); e != nil {
		return nil, e
	}
	return []RPCResponse{resp}, nil
}

func (r *Runtime) persistResponse(ctx context.Context, req RPCRequest, resp RPCResponse) error {
	if _, err := r.encryptResponse(ctx, req, resp); err != nil {
		return err
	}
	if r.requireRelayState {
		return r.flushOutbox(ctx)
	}
	return nil
}

func (r *Runtime) uncertainResponse(ctx context.Context, req RPCRequest, clientID, op string, seq uint64, message string) ([]RPCResponse, error) {
	resp := RPCResponse{RequestID: req.RequestID, Kind: "uncertain", Seq: seq, Final: true}
	resp.Body, _ = json.Marshal(map[string]string{"error": message})
	r.markUncertain(ctx, clientID, op)
	if err := r.persistResponse(ctx, req, resp); err != nil {
		r.freeze()
		return nil, err
	}
	data, _ := json.Marshal([]RPCResponse{resp})
	if err := r.store.UpdateIntent(ctx, clientID, "uncertain", map[string]any{"operation_id": op, "responses": json.RawMessage(data)}); err != nil {
		r.freeze()
		return nil, err
	}
	return []RPCResponse{resp}, nil
}

func (r *Runtime) validateHandlerDestination(name string) error {
	h, ok := r.cfg.Handlers[name]
	if !ok {
		return nil
	}
	raw := h.URL
	if h.Model != "" {
		if m, exists := r.cfg.Models[h.Model]; exists {
			raw = m.URL
			if !isLoopbackURL(raw) && !(r.cfg.Endpoint.AllowExternalPlaintext && m.AllowPlaintext) {
				return errors.New("microsafer: external model requires explicit plaintext opt-in")
			}
		}
	}
	if raw != "" && !isLoopbackURL(raw) && !(r.cfg.Endpoint.AllowExternalPlaintext && h.AllowPlaintext) {
		return errors.New("microsafer: external handler requires explicit plaintext opt-in")
	}
	return nil
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && isLoopbackHost(u.Hostname())
}

func (r *Runtime) handlerNoticeBody(req RPCRequest, op string) json.RawMessage {
	body := map[string]string{"action": req.Action, "name": req.Name, "operation_id": op}
	if h, ok := r.cfg.Handlers[req.Name]; ok {
		raw := h.URL
		if h.Model != "" {
			if m, exists := r.cfg.Models[h.Model]; exists {
				raw = m.URL
			}
		}
		if raw != "" && !isLoopbackURL(raw) {
			body["kind"] = "external_plaintext"
			body["url"] = safeNoticeURL(raw)
		}
	}
	out, _ := json.Marshal(body)
	return out
}

func (r *Runtime) handlerDestination(name string) string {
	if h, ok := r.cfg.Handlers[name]; ok {
		if h.URL != "" {
			return h.URL
		}
		if h.Model != "" {
			if m, exists := r.cfg.Models[h.Model]; exists {
				return m.URL
			}
		}
		if h.Kind == "storage" {
			return "vault://" + r.cfg.Endpoint.Name + "/storage"
		}
	}
	return "handler://" + r.cfg.Endpoint.Name + "/" + name
}
func (r *Runtime) decodeResponses(id string, value []byte) ([]RPCResponse, error) {
	var x struct {
		Responses []RPCResponse `json:"responses"`
	}
	if len(value) == 0 {
		return nil, nil
	}
	if e := json.Unmarshal(value, &x); e != nil {
		return nil, e
	}
	return x.Responses, nil
}
func (r *Runtime) markUncertain(ctx context.Context, clientID, op string) {
	_ = r.store.UpdateIntent(ctx, clientID, "uncertain", map[string]any{"operation_id": op})
}
func (r *Runtime) grantPublicKey() (ed25519.PublicKey, error) {
	b, e := decodePinnedEnv(r.cfg.Endpoint.GrantPublicKeyEnv)
	if e != nil {
		return nil, e
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, errors.New("microsafer: grant public key must be Ed25519")
	}
	return ed25519.PublicKey(b), nil
}
func decodePinnedEnv(name string) ([]byte, error) {
	v := strings.TrimSpace(getenv(name))
	if b, e := base64.StdEncoding.Strict().DecodeString(v); e == nil {
		return b, nil
	}
	return nil, errors.New("microsafer: grant public key environment must be base64")
}

var getenv = func(s string) string { return os.Getenv(s) }
