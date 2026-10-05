//go:build qg_ai_storage

// Package aivault stores bounded, encrypted resources used by AI retrieval.
// The package deliberately knows nothing about core, configuration, jobs, or
// embedding providers. Callers must authorize a request before invoking it.
package aivault

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type Scope string

const (
	ScopeShared Scope = "shared"
	ScopeBot    Scope = "bot"
	ScopeUser   Scope = "user"
)

type Limits struct {
	MaxResources        int
	MaxDocuments        int
	MaxEdges            int
	MaxItemBytes        int
	MaxTextBytes        int
	MaxMetadataBytes    int
	MaxFilenameBytes    int
	MaxVectorDimensions int
	MaxSearchResults    int
	MaxBFSDepth         int
}

const (
	defaultMaxResources        = 1024
	defaultMaxDocuments        = 10000
	defaultMaxEdges            = 20000
	defaultMaxItemBytes        = 8 << 20
	defaultMaxTextBytes        = 1 << 20
	defaultMaxMetadataBytes    = 256 << 10
	defaultMaxFilenameBytes    = 4096
	defaultMaxVectorDimensions = 4096
	defaultMaxSearchResults    = 100
	defaultMaxBFSDepth         = 16
	maxResourcesCeiling        = 100000
	maxDocumentsCeiling        = 1000000
	maxEdgesCeiling            = 2000000
	maxItemBytesCeiling        = 64 << 20
	maxTextBytesCeiling        = 8 << 20
	maxMetadataBytesCeiling    = 4 << 20
	maxFilenameBytesCeiling    = 64 << 10
	maxVectorDimensionsCeiling = 65536
	maxSearchResultsCeiling    = 10000
	maxBFSDepthCeiling         = 64
)

func (l Limits) withDefaults() Limits {
	if l.MaxResources <= 0 {
		l.MaxResources = defaultMaxResources
	}
	if l.MaxDocuments <= 0 {
		l.MaxDocuments = defaultMaxDocuments
	}
	if l.MaxEdges <= 0 {
		l.MaxEdges = defaultMaxEdges
	}
	if l.MaxItemBytes <= 0 {
		l.MaxItemBytes = defaultMaxItemBytes
	}
	if l.MaxTextBytes <= 0 {
		l.MaxTextBytes = defaultMaxTextBytes
	}
	if l.MaxMetadataBytes <= 0 {
		l.MaxMetadataBytes = defaultMaxMetadataBytes
	}
	if l.MaxFilenameBytes <= 0 {
		l.MaxFilenameBytes = defaultMaxFilenameBytes
	}
	if l.MaxVectorDimensions <= 0 {
		l.MaxVectorDimensions = defaultMaxVectorDimensions
	}
	if l.MaxSearchResults <= 0 {
		l.MaxSearchResults = defaultMaxSearchResults
	}
	if l.MaxBFSDepth <= 0 {
		l.MaxBFSDepth = defaultMaxBFSDepth
	}
	return l
}

func validateLimits(l Limits) error {
	values := []struct {
		name           string
		value, ceiling int
	}{
		{"max_resources", l.MaxResources, maxResourcesCeiling}, {"max_documents", l.MaxDocuments, maxDocumentsCeiling},
		{"max_edges", l.MaxEdges, maxEdgesCeiling}, {"max_item_bytes", l.MaxItemBytes, maxItemBytesCeiling},
		{"max_text_bytes", l.MaxTextBytes, maxTextBytesCeiling}, {"max_metadata_bytes", l.MaxMetadataBytes, maxMetadataBytesCeiling},
		{"max_filename_bytes", l.MaxFilenameBytes, maxFilenameBytesCeiling}, {"max_vector_dimensions", l.MaxVectorDimensions, maxVectorDimensionsCeiling},
		{"max_search_results", l.MaxSearchResults, maxSearchResultsCeiling}, {"max_bfs_depth", l.MaxBFSDepth, maxBFSDepthCeiling},
	}
	for _, v := range values {
		if v.value < 0 {
			return fmt.Errorf("aivault: %s must not be negative", v.name)
		}
		if v.value > v.ceiling {
			return fmt.Errorf("aivault: %s exceeds package ceiling", v.name)
		}
	}
	return nil
}

type Store struct {
	db     *sql.DB
	engine *cryptoenc.Engine
	limits Limits
}

// New creates the versioned schema before returning the store.
func New(db *sql.DB, engine *cryptoenc.Engine, limits Limits) (*Store, error) {
	if db == nil || engine == nil {
		return nil, errors.New("aivault: database and engine are required")
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	s := &Store{db: db, engine: engine, limits: limits.withDefaults()}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func NewStore(db *sql.DB, engine *cryptoenc.Engine, limits Limits) (*Store, error) {
	return New(db, engine, limits)
}
func Open(db *sql.DB, engine *cryptoenc.Engine, limits Limits) (*Store, error) {
	return New(db, engine, limits)
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS aivault_schema(version INTEGER PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS aivault_resources(id TEXT PRIMARY KEY, scope TEXT NOT NULL, owner TEXT NOT NULL, routing_visible INTEGER NOT NULL, payload BLOB NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS aivault_documents(id TEXT PRIMARY KEY, resource_id TEXT NOT NULL REFERENCES aivault_resources(id) ON DELETE CASCADE, payload BLOB NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS aivault_documents_resource ON aivault_documents(resource_id)`,
		`CREATE TABLE IF NOT EXISTS aivault_edges(id TEXT PRIMARY KEY, resource_id TEXT NOT NULL REFERENCES aivault_resources(id) ON DELETE CASCADE, payload BLOB NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS aivault_edges_resource ON aivault_edges(resource_id)`,
	}
	for _, q := range statements {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM aivault_schema`).Scan(&version)
	if err != nil {
		return err
	}
	if version > 1 {
		return fmt.Errorf("aivault: unsupported schema version %d", version)
	}
	if version < 1 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO aivault_schema(version) VALUES(1)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type ResourceSpec struct {
	ID             string          `json:"id"`
	Scope          Scope           `json:"scope"`
	Owner          string          `json:"owner"`
	RoutingVisible bool            `json:"routing_visible"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type Resource struct {
	ID             string          `json:"id"`
	Scope          Scope           `json:"scope"`
	Owner          string          `json:"owner"`
	RoutingVisible bool            `json:"routing_visible"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type resourcePayload struct {
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func (s *Store) CreateResource(ctx context.Context, spec ResourceSpec) (Resource, error) {
	if spec.ID == "" {
		spec.ID = newID()
	}
	if err := validID(spec.ID); err != nil {
		return Resource{}, err
	}
	if spec.Scope != ScopeShared && spec.Scope != ScopeBot && spec.Scope != ScopeUser {
		return Resource{}, errors.New("aivault: invalid resource scope")
	}
	if spec.Owner == "" || len(spec.Owner) > 256 {
		return Resource{}, errors.New("aivault: invalid resource owner")
	}
	meta, err := boundedJSON(spec.Metadata, s.limits.MaxMetadataBytes)
	if err != nil {
		return Resource{}, err
	}
	plain, err := json.Marshal(resourcePayload{Metadata: meta})
	if err != nil {
		return Resource{}, err
	}
	sealed, err := s.engine.Seal(plain, aadResource(spec.ID, spec.Scope, spec.Owner, spec.RoutingVisible))
	if err != nil {
		return Resource{}, err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM aivault_resources`).Scan(&n); err != nil {
		return Resource{}, err
	}
	if n >= s.limits.MaxResources {
		return Resource{}, ErrQuota
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aivault_resources(id,scope,owner,routing_visible,payload,created_at) VALUES(?,?,?,?,?,?)`, spec.ID, spec.Scope, spec.Owner, boolInt(spec.RoutingVisible), sealed, now.Unix())
	if err != nil {
		return Resource{}, err
	}
	if err = tx.Commit(); err != nil {
		return Resource{}, err
	}
	return Resource{ID: spec.ID, Scope: spec.Scope, Owner: spec.Owner, RoutingVisible: spec.RoutingVisible, Metadata: append(json.RawMessage(nil), meta...), CreatedAt: now}, nil
}

func (s *Store) GetResource(ctx context.Context, id string) (Resource, error) {
	header, err := s.ResourceByID(ctx, id)
	if err != nil {
		return Resource{}, err
	}
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload FROM aivault_resources WHERE id=?`, id).Scan(&sealed)
	if err != nil {
		return Resource{}, err
	}
	plain, err := s.engine.Open(sealed, aadResource(id, header.Scope, header.Owner, header.RoutingVisible))
	if err != nil {
		return Resource{}, ErrCorrupt
	}
	var p resourcePayload
	if json.Unmarshal(plain, &p) != nil {
		return Resource{}, ErrCorrupt
	}
	header.Metadata = p.Metadata
	return header, nil
}

// ResourceByID authenticates routing and ownership against sealed metadata,
// without returning metadata or decrypting document/file/vector contents.
func (s *Store) ResourceByID(ctx context.Context, id string) (Resource, error) {
	var scope, owner string
	var visible, created int64
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT scope,owner,routing_visible,created_at,payload FROM aivault_resources WHERE id=?`, id).Scan(&scope, &owner, &visible, &created, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, err
	}
	if visible != 0 && visible != 1 {
		return Resource{}, ErrCorrupt
	}
	if _, err = s.engine.Open(sealed, aadResource(id, Scope(scope), owner, visible != 0)); err != nil {
		return Resource{}, ErrCorrupt
	}
	return Resource{ID: id, Scope: Scope(scope), Owner: owner, RoutingVisible: visible != 0, CreatedAt: time.Unix(created, 0).UTC()}, nil
}

func (s *Store) DeleteResource(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM aivault_documents WHERE resource_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM aivault_edges WHERE resource_id=?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM aivault_resources WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

type DocumentInput struct {
	ID       string          `json:"id"`
	Text     string          `json:"text,omitempty"`
	Vector   []float64       `json:"vector,omitempty"`
	Filename string          `json:"filename,omitempty"`
	File     []byte          `json:"file,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type Document struct {
	ID         string          `json:"id"`
	ResourceID string          `json:"resource_id"`
	Text       string          `json:"text,omitempty"`
	Vector     []float64       `json:"vector,omitempty"`
	Filename   string          `json:"filename,omitempty"`
	File       []byte          `json:"file,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type documentPayload struct {
	Text     string          `json:"text,omitempty"`
	Vector   []float64       `json:"vector,omitempty"`
	Filename string          `json:"filename,omitempty"`
	File     []byte          `json:"file,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func (s *Store) PutDocument(ctx context.Context, resourceID string, in DocumentInput) (Document, error) {
	if err := validID(resourceID); err != nil {
		return Document{}, err
	}
	if in.ID == "" {
		in.ID = newID()
	}
	if err := validID(in.ID); err != nil {
		return Document{}, err
	}
	if len([]byte(in.Text)) > s.limits.MaxTextBytes || len([]byte(in.Filename)) > s.limits.MaxFilenameBytes {
		return Document{}, ErrQuota
	}
	if err := validateVector(in.Vector, s.limits.MaxVectorDimensions); err != nil {
		return Document{}, err
	}
	meta, err := boundedJSON(in.Metadata, s.limits.MaxMetadataBytes)
	if err != nil {
		return Document{}, err
	}
	p := documentPayload{Text: in.Text, Vector: append([]float64(nil), in.Vector...), Filename: in.Filename, File: append([]byte(nil), in.File...), Metadata: meta}
	plain, err := json.Marshal(p)
	if err != nil {
		return Document{}, err
	}
	if len(plain) > s.limits.MaxItemBytes {
		return Document{}, ErrQuota
	}
	sealed, err := s.engine.Seal(plain, aadDocument(resourceID, in.ID))
	if err != nil {
		return Document{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback()
	if !resourceExists(ctx, tx, resourceID) {
		return Document{}, ErrNotFound
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM aivault_documents WHERE resource_id=?`, resourceID).Scan(&n); err != nil {
		return Document{}, err
	}
	if n >= s.limits.MaxDocuments {
		return Document{}, ErrQuota
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO aivault_documents(id,resource_id,payload,created_at) VALUES(?,?,?,?)`, in.ID, resourceID, sealed, time.Now().Unix()); err != nil {
		return Document{}, err
	}
	if err = tx.Commit(); err != nil {
		return Document{}, err
	}
	return Document{ID: in.ID, ResourceID: resourceID, Text: in.Text, Vector: p.Vector, Filename: in.Filename, File: p.File, Metadata: meta}, nil
}

type DocumentResult struct {
	Document Document `json:"document"`
	Resource Resource `json:"resource"`
	Score    float64  `json:"score"`
}

func (s *Store) GetDocument(ctx context.Context, resourceID, documentID string) (DocumentResult, error) {
	res, err := s.ResourceByID(ctx, resourceID)
	if err != nil {
		return DocumentResult{}, err
	}
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload FROM aivault_documents WHERE id=? AND resource_id=?`, documentID, resourceID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return DocumentResult{}, ErrNotFound
	}
	if err != nil {
		return DocumentResult{}, err
	}
	plain, err := s.engine.Open(sealed, aadDocument(resourceID, documentID))
	if err != nil {
		return DocumentResult{}, ErrCorrupt
	}
	var p documentPayload
	if json.Unmarshal(plain, &p) != nil {
		return DocumentResult{}, ErrCorrupt
	}
	if err := s.validateDocumentPayload(p); err != nil {
		return DocumentResult{}, ErrCorrupt
	}
	return DocumentResult{Document: Document{ID: documentID, ResourceID: resourceID, Text: p.Text, Vector: p.Vector, Filename: p.Filename, File: p.File, Metadata: p.Metadata}, Resource: res}, nil
}

func (s *Store) DeleteDocument(ctx context.Context, resourceID, documentID string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM aivault_documents WHERE id=? AND resource_id=?`, documentID, resourceID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type SearchQuery struct {
	Text   string
	Vector []float64
	Limit  int
}

func (s *Store) Search(ctx context.Context, resourceID string, q SearchQuery) ([]DocumentResult, error) {
	if err := validID(resourceID); err != nil {
		return nil, err
	}
	if len([]byte(q.Text)) > s.limits.MaxTextBytes {
		return nil, ErrQuota
	}
	res, err := s.ResourceByID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if err = validateVector(q.Vector, s.limits.MaxVectorDimensions); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > s.limits.MaxSearchResults {
		limit = s.limits.MaxSearchResults
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,payload FROM aivault_documents WHERE resource_id=? ORDER BY id`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DocumentResult, 0, limit)
	for rows.Next() {
		var id string
		var sealed []byte
		if err = rows.Scan(&id, &sealed); err != nil {
			return nil, err
		}
		plain, e := s.engine.Open(sealed, aadDocument(resourceID, id))
		if e != nil {
			return nil, ErrCorrupt
		}
		var p searchPayload
		if json.Unmarshal(plain, &p) != nil {
			return nil, ErrCorrupt
		}
		if err := s.validateSearchPayload(p); err != nil {
			return nil, ErrCorrupt
		}
		score := scoreDocument(p.Text, p.Vector, q)
		if q.Text != "" && score == 0 {
			continue
		}
		candidate := DocumentResult{Document: Document{ID: id, ResourceID: resourceID, Text: p.Text, Filename: p.Filename, Metadata: p.Metadata}, Resource: res, Score: score}
		if len(out) < limit {
			out = append(out, candidate)
			continue
		}
		worst := 0
		for i := 1; i < len(out); i++ {
			if better(out[worst], out[i]) {
				worst = i
			}
		}
		if better(candidate, out[worst]) {
			out[worst] = candidate
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sortResults(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type searchPayload struct {
	Text     string          `json:"text,omitempty"`
	Vector   []float64       `json:"vector,omitempty"`
	Filename string          `json:"filename,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func (s *Store) validateDocumentPayload(p documentPayload) error {
	if len([]byte(p.Text)) > s.limits.MaxTextBytes || len([]byte(p.Filename)) > s.limits.MaxFilenameBytes || len(p.Metadata) > s.limits.MaxMetadataBytes {
		return ErrQuota
	}
	if err := validateVector(p.Vector, s.limits.MaxVectorDimensions); err != nil {
		return err
	}
	encoded, err := json.Marshal(p)
	if err != nil || len(encoded) > s.limits.MaxItemBytes {
		return ErrQuota
	}
	return nil
}

func (s *Store) validateSearchPayload(p searchPayload) error {
	if len([]byte(p.Text)) > s.limits.MaxTextBytes || len([]byte(p.Filename)) > s.limits.MaxFilenameBytes || len(p.Metadata) > s.limits.MaxMetadataBytes {
		return ErrQuota
	}
	return validateVector(p.Vector, s.limits.MaxVectorDimensions)
}

func better(a, b DocumentResult) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Document.ID < b.Document.ID
}

type EdgeInput struct {
	ID     string    `json:"id"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Label  string    `json:"label"`
	Vector []float64 `json:"vector,omitempty"`
}
type Edge struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resource_id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Label      string    `json:"label"`
	Vector     []float64 `json:"vector,omitempty"`
}
type edgePayload struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	Label  string    `json:"label,omitempty"`
	Vector []float64 `json:"vector,omitempty"`
}

func (s *Store) PutEdge(ctx context.Context, resourceID string, in EdgeInput) (Edge, error) {
	if err := validID(resourceID); err != nil {
		return Edge{}, err
	}
	if in.ID == "" {
		in.ID = newID()
	}
	if err := validID(in.ID); err != nil {
		return Edge{}, err
	}
	if in.From == "" || in.To == "" || len(in.From) > 256 || len(in.To) > 256 {
		return Edge{}, errors.New("aivault: invalid edge endpoints")
	}
	if err := validateVector(in.Vector, s.limits.MaxVectorDimensions); err != nil {
		return Edge{}, err
	}
	plain, _ := json.Marshal(edgePayload{From: in.From, To: in.To, Label: in.Label, Vector: in.Vector})
	if len(plain) > s.limits.MaxItemBytes {
		return Edge{}, ErrQuota
	}
	sealed, err := s.engine.Seal(plain, aadEdge(resourceID, in.ID))
	if err != nil {
		return Edge{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Edge{}, err
	}
	defer tx.Rollback()
	if !resourceExists(ctx, tx, resourceID) {
		return Edge{}, ErrNotFound
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM aivault_edges WHERE resource_id=?`, resourceID).Scan(&n); err != nil {
		return Edge{}, err
	}
	if n >= s.limits.MaxEdges {
		return Edge{}, ErrQuota
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO aivault_edges(id,resource_id,payload,created_at) VALUES(?,?,?,?)`, in.ID, resourceID, sealed, time.Now().Unix()); err != nil {
		return Edge{}, err
	}
	if err = tx.Commit(); err != nil {
		return Edge{}, err
	}
	return Edge{ID: in.ID, ResourceID: resourceID, From: in.From, To: in.To, Label: in.Label, Vector: append([]float64(nil), in.Vector...)}, nil
}

func (s *Store) DeleteEdge(ctx context.Context, resourceID, edgeID string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM aivault_edges WHERE id=? AND resource_id=?`, edgeID, resourceID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type GraphQuery struct {
	Start string
	Depth int
	Limit int
}
type GraphResult struct {
	Resource Resource `json:"resource"`
	Nodes    []string `json:"nodes"`
	Edges    []Edge   `json:"edges"`
}

func (s *Store) GraphBFS(ctx context.Context, resourceID string, q GraphQuery) (GraphResult, error) {
	if err := validID(resourceID); err != nil {
		return GraphResult{}, err
	}
	res, err := s.ResourceByID(ctx, resourceID)
	if err != nil {
		return GraphResult{}, err
	}
	depth := q.Depth
	if depth < 0 || depth > s.limits.MaxBFSDepth {
		return GraphResult{}, ErrQuota
	}
	limit := q.Limit
	if limit <= 0 || limit > s.limits.MaxSearchResults {
		limit = s.limits.MaxSearchResults
	}
	if err := validID(q.Start); err != nil {
		return GraphResult{}, errors.New("aivault: graph start is required")
	}
	seen := map[string]bool{q.Start: true}
	frontier := []string{q.Start}
	outEdges := []Edge{}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		next := []string{}
		frontierSet := make(map[string]struct{}, len(frontier))
		for _, node := range frontier {
			frontierSet[node] = struct{}{}
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id,payload FROM aivault_edges WHERE resource_id=? ORDER BY id`, resourceID)
		if err != nil {
			return GraphResult{}, err
		}
		for rows.Next() {
			var id string
			var sealed []byte
			if err = rows.Scan(&id, &sealed); err != nil {
				rows.Close()
				return GraphResult{}, err
			}
			plain, e := s.engine.Open(sealed, aadEdge(resourceID, id))
			if e != nil {
				rows.Close()
				return GraphResult{}, ErrCorrupt
			}
			var p edgePayload
			if json.Unmarshal(plain, &p) != nil {
				rows.Close()
				return GraphResult{}, ErrCorrupt
			}
			if _, ok := frontierSet[p.From]; !ok || seen[p.To] {
				continue
			}
			seen[p.To] = true
			// Graph traversal returns topology only. Edge vectors remain encrypted
			// in storage and are not materialized into a bounded graph response.
			outEdges = append(outEdges, Edge{ID: id, ResourceID: resourceID, From: p.From, To: p.To, Label: p.Label})
			next = append(next, p.To)
			if len(outEdges) >= limit {
				break
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return GraphResult{}, err
		}
		rows.Close()
		frontier = next
		if len(outEdges) >= limit {
			break
		}
	}
	nodes := make([]string, 0, len(seen))
	for n := range seen {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return GraphResult{Resource: res, Nodes: nodes, Edges: outEdges}, nil
}

var ErrNotFound = errors.New("aivault: not found")
var ErrQuota = errors.New("aivault: quota exceeded")
var ErrCorrupt = errors.New("aivault: encrypted payload is corrupt")

func resourceExists(ctx context.Context, tx *sql.Tx, id string) bool {
	var n int
	return tx.QueryRowContext(ctx, `SELECT 1 FROM aivault_resources WHERE id=?`, id).Scan(&n) == nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func validID(v string) error {
	if v == "" || len(v) > 256 || strings.IndexByte(v, 0) >= 0 {
		return errors.New("aivault: invalid id")
	}
	return nil
}
func boundedJSON(raw json.RawMessage, max int) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > max {
		return nil, ErrQuota
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("aivault: metadata must be JSON")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(out) > max {
		return nil, ErrQuota
	}
	return out, nil
}
func validateVector(v []float64, max int) error {
	if len(v) > max {
		return ErrQuota
	}
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("aivault: vector contains non-finite value")
		}
	}
	return nil
}
func aadResource(id string, scope Scope, owner string, visible bool) []byte {
	b, _ := json.Marshal([]any{"aivault/v1/resource", id, scope, owner, visible})
	return b
}
func aadDocument(r, id string) []byte {
	b, _ := json.Marshal([]string{"aivault/v1/document", r, id})
	return b
}
func aadEdge(r, id string) []byte { b, _ := json.Marshal([]string{"aivault/v1/edge", r, id}); return b }
func scoreDocument(text string, vector []float64, q SearchQuery) float64 {
	score := 0.0
	if q.Text != "" {
		needle := strings.ToLower(strings.TrimSpace(q.Text))
		if strings.Contains(strings.ToLower(text), needle) {
			score += 1
		}
		for _, term := range strings.Fields(needle) {
			if strings.Contains(strings.ToLower(text), term) {
				score += 0.1
			}
		}
	}
	if len(q.Vector) > 0 && len(vector) == len(q.Vector) {
		var dot, an, bn float64
		for i := range q.Vector {
			dot += q.Vector[i] * vector[i]
			an += q.Vector[i] * q.Vector[i]
			bn += vector[i] * vector[i]
		}
		if an > 0 && bn > 0 {
			score += (dot/(math.Sqrt(an)*math.Sqrt(bn)) + 1) / 2
		}
	}
	return score
}
func sortResults(items []DocumentResult) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].Score > items[j-1].Score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
