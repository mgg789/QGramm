//go:build qg_ai_endpoint && qg_e2ee && qg_ai_storage

package microsafer

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mgg789/QGramm/internal/aivault"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	_ "modernc.org/sqlite"
)

type StorageHandler struct {
	Vault  *aivault.Store
	DB     *sql.DB
	Engine *cryptoenc.Engine
	Config StorageConfig
	User   string
	Peers  map[string]PeerConfig
	// MaxGrantRecords is a durable replay-safety horizon. Records are never
	// auto-deleted: once this cap is reached an operator must reset the
	// offline identity/group before accepting new grants.
	MaxGrantRecords int
	lock            *ownerLock
}

const storageSchema = 1

type StorageRequest struct {
	Action      string          `json:"action"`
	Resource    string          `json:"resource"`
	Destination string          `json:"destination"`
	Grant       string          `json:"grant"`
	Body        json.RawMessage `json:"body"`
	Chat        string          `json:"-"`
	SourcePeer  string          `json:"-"`
	RequestID   string          `json:"-"`
}

func OpenStorage(c Config) (*StorageHandler, error) {
	c.setDefaults()
	if !c.Storage.Enabled {
		return nil, errors.New("microsafer: storage is disabled")
	}
	s := c.Storage
	if s.DBPath == "" {
		s.DBPath = c.Endpoint.DBPath + ".vault"
	}
	if s.MasterKeyEnv == "" {
		s.MasterKeyEnv = c.Endpoint.MasterKeyEnv
	}
	if s.HPKEKeyEnv == "" {
		s.HPKEKeyEnv = c.Endpoint.HPKEKeyEnv
	}
	master, e := decodeSecretEnv(s.MasterKeyEnv)
	if e != nil {
		return nil, e
	}
	hpke, e := decodeSecretEnv(s.HPKEKeyEnv)
	if e != nil {
		return nil, e
	}
	engine, e := cryptoenc.New(master, hpke)
	if e != nil {
		return nil, e
	}
	if dir := filepath.Dir(s.DBPath); dir != "." {
		if e = osMkdir(dir); e != nil {
			return nil, e
		}
	}
	lock, e := acquireOwnerLock(s.DBPath)
	if e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", s.DBPath)
	if e != nil {
		_ = lock.Close()
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000; CREATE TABLE IF NOT EXISTS microsafer_storage_meta(key TEXT PRIMARY KEY, value INTEGER NOT NULL); CREATE TABLE IF NOT EXISTS microsafer_storage_grants(nonce TEXT PRIMARY KEY, token_hash BLOB NOT NULL, consumed_at INTEGER NOT NULL)`); e != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, e
	}
	var schema int
	e = db.QueryRow(`SELECT value FROM microsafer_storage_meta WHERE key='schema_version'`).Scan(&schema)
	if errors.Is(e, sql.ErrNoRows) {
		_, e = db.Exec(`INSERT INTO microsafer_storage_meta(key,value) VALUES('schema_version',?)`, storageSchema)
	} else if e == nil && schema != storageSchema {
		e = errors.New("microsafer: unsupported storage schema version")
	}
	if e != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, e
	}
	vault, e := aivault.New(db, engine, aivault.Limits{MaxResources: s.MaxResources, MaxDocuments: s.MaxDocuments, MaxEdges: s.MaxEdges, MaxItemBytes: s.MaxItemBytes, MaxTextBytes: s.MaxTextBytes, MaxSearchResults: s.MaxResults, MaxBFSDepth: s.MaxGraphDepth})
	if e != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, e
	}
	peers := make(map[string]PeerConfig, len(c.Peers)+len(c.Chats))
	for name, peer := range c.Peers {
		peers[name] = peer
	}
	for chat := range c.Chats {
		if peer, ok := c.peerForChat(chat); ok {
			peers[chat] = peer
		}
	}
	return &StorageHandler{Vault: vault, DB: db, Engine: engine, Config: s, User: c.Endpoint.User, Peers: peers, MaxGrantRecords: c.Endpoint.MaxPending * 4, lock: lock}, nil
}
func osMkdir(path string) error { return os.MkdirAll(path, 0700) }
func (h *StorageHandler) Close() error {
	if h == nil || h.DB == nil {
		return nil
	}
	err := h.DB.Close()
	if e := h.lock.Close(); err == nil {
		err = e
	}
	return err
}
func AttachStorageHandler(r *Runtime, h *StorageHandler) error {
	if r == nil || h == nil {
		return errors.New("microsafer: storage handler required")
	}
	return r.RegisterRPCHandler("storage", false, h.HandleRPC)
}
func (h *StorageHandler) HandleRPC(ctx context.Context, req RPCRequest) (json.RawMessage, error) {
	var in StorageRequest
	if err := json.Unmarshal(req.Body, &in); err != nil {
		return nil, storageRequestRejected("malformed request")
	}
	in.Chat, in.SourcePeer, in.RequestID = req.Chat, req.SourcePeer, req.RequestID
	return h.handle(ctx, in)
}
func (h *StorageHandler) Handle(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in StorageRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return h.handle(ctx, in)
}
func (h *StorageHandler) handle(ctx context.Context, in StorageRequest) (json.RawMessage, error) {
	if in.Action == "" || in.Resource == "" || in.Grant == "" || in.Destination == "" {
		return nil, storageRequestRejected("action/resource/grant/destination required")
	}
	if err := h.authorize(ctx, in); err != nil {
		return nil, err
	}
	switch aivault.Action(in.Action) {
	case aivault.ActionRead:
		return h.read(ctx, in)
	case aivault.ActionSearch:
		return h.search(ctx, in)
	case aivault.ActionGraph:
		return h.graph(ctx, in)
	default:
		return nil, storageRequestRejected("unsupported storage action")
	}
}

func storageRequestRejected(reason string) error {
	return fmt.Errorf("microsafer: storage request rejected: %s", reason)
}
func (h *StorageHandler) authorize(ctx context.Context, in StorageRequest) error {
	pub, e := decodePinnedEnv(h.Config.GrantPublicKeyEnv)
	if e != nil {
		return e
	}
	claims, e := aivault.Verify(ed25519.PublicKey(pub), h.Config.Issuer, h.Config.Audience, time.Duration(h.Config.GrantTTLSeconds)*time.Second, in.Grant)
	if e != nil {
		return storageRequestRejected("invalid grant")
	}
	hash, e := RequestHash(in.Body)
	if e != nil {
		return storageRequestRejected("invalid body")
	}
	sourceUser := in.SourcePeer
	if peer, ok := h.Peers[in.Chat]; ok {
		if in.SourcePeer == peer.Device || in.SourcePeer == peer.User+"/"+peer.Device {
			sourceUser = peer.User
		}
	}
	if claims.RequestHash != base64.RawURLEncoding.EncodeToString(hash) || claims.Resource != in.Resource || claims.Destination != in.Destination || claims.Action != aivault.Action(in.Action) || claims.AIUser != h.User || (in.SourcePeer != "" && claims.SourceUser != sourceUser) || (in.Chat != "" && claims.ChatID != in.Chat) || (in.RequestID != "" && claims.RequestID != in.RequestID) {
		return storageRequestRejected("grant request mismatch")
	}
	allow, ok := h.Config.Resources[in.Resource]
	if !ok {
		return storageRequestRejected("storage resource is not allowlisted")
	}
	res, e := h.Vault.ResourceByID(ctx, in.Resource)
	if e != nil {
		return e
	}
	if string(res.Scope) != allow.Scope || res.Owner != allow.Owner {
		return storageRequestRejected("storage resource scope/owner mismatch")
	}
	if res.Scope == aivault.ScopeUser && res.Owner != sourceUser {
		return storageRequestRejected("user storage resource belongs to another peer")
	}
	if res.Scope == aivault.ScopeBot && res.Owner != h.User {
		return storageRequestRejected("bot storage resource owner mismatch")
	}
	limit := h.MaxGrantRecords
	if limit <= 0 {
		limit = 4096 * 4
	}
	var records int
	tx, e := h.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_storage_grants`).Scan(&records); e != nil {
		return e
	}
	if records >= limit {
		return errors.New("microsafer: storage grant retention cap reached; operator reset required")
	}
	sum := sha256.Sum256([]byte(in.Grant))
	_, e = tx.ExecContext(ctx, `INSERT INTO microsafer_storage_grants(nonce,token_hash,consumed_at) VALUES(?,?,?)`, claims.Nonce, sum[:], time.Now().Unix())
	if e != nil {
		return errors.New("microsafer: storage grant already consumed")
	}
	return tx.Commit()
}
func (h *StorageHandler) read(ctx context.Context, in StorageRequest) (json.RawMessage, error) {
	var x struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal(in.Body, &x); err != nil || x.DocumentID == "" {
		return nil, errors.New("microsafer: document_id required")
	}
	out, e := h.Vault.GetDocument(ctx, in.Resource, x.DocumentID)
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
func (h *StorageHandler) search(ctx context.Context, in StorageRequest) (json.RawMessage, error) {
	var x struct {
		Query  string    `json:"query"`
		Vector []float64 `json:"vector"`
		Limit  int       `json:"limit"`
	}
	if err := json.Unmarshal(in.Body, &x); err != nil {
		return nil, err
	}
	out, e := h.Vault.Search(ctx, in.Resource, aivault.SearchQuery{Text: x.Query, Vector: x.Vector, Limit: x.Limit})
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}
func (h *StorageHandler) graph(ctx context.Context, in StorageRequest) (json.RawMessage, error) {
	var x struct {
		Start string `json:"start"`
		Depth int    `json:"depth"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(in.Body, &x); err != nil {
		return nil, err
	}
	out, e := h.Vault.GraphBFS(ctx, in.Resource, aivault.GraphQuery{Start: x.Start, Depth: x.Depth, Limit: x.Limit})
	if e != nil {
		return nil, e
	}
	return json.Marshal(out)
}

type IngestDocument struct {
	ID       string          `json:"id"`
	Text     string          `json:"text"`
	Vector   []float64       `json:"vector"`
	Filename string          `json:"filename"`
	File     []byte          `json:"file"`
	Metadata json.RawMessage `json:"metadata"`
}
type IngestEdge struct {
	ID     string    `json:"id"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Label  string    `json:"label"`
	Vector []float64 `json:"vector"`
}
type IngestResource struct {
	ID             string           `json:"id"`
	Scope          string           `json:"scope"`
	Owner          string           `json:"owner"`
	RoutingVisible bool             `json:"routing_visible"`
	Metadata       json.RawMessage  `json:"metadata"`
	Documents      []IngestDocument `json:"documents"`
	Edges          []IngestEdge     `json:"edges"`
}

func (h *StorageHandler) Ingest(ctx context.Context, raw []byte) error {
	var in struct {
		Resources []IngestResource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	for _, r := range in.Resources {
		if _, err := h.Vault.CreateResource(ctx, aivault.ResourceSpec{ID: r.ID, Scope: aivault.Scope(r.Scope), Owner: r.Owner, RoutingVisible: r.RoutingVisible, Metadata: r.Metadata}); err != nil {
			return err
		}
		for _, d := range r.Documents {
			if _, err := h.Vault.PutDocument(ctx, r.ID, aivault.DocumentInput{ID: d.ID, Text: d.Text, Vector: d.Vector, Filename: d.Filename, File: d.File, Metadata: d.Metadata}); err != nil {
				return err
			}
		}
		for _, e := range r.Edges {
			if _, err := h.Vault.PutEdge(ctx, r.ID, aivault.EdgeInput{ID: e.ID, From: e.From, To: e.To, Label: e.Label, Vector: e.Vector}); err != nil {
				return err
			}
		}
	}
	return nil
}
