//go:build qg_ai_storage && qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mgg789/QGramm/internal/aivault"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

type aiVaultState struct {
	Store *aivault.Store
	Key   ed25519.PublicKey
}

var aiVaults sync.Map

func init() {
	core.Register("ai_storage", installAI)
	aiStorageInstall = installAIStorage
	aiTools["storage"] = aiVaultTool
	aiStorageRelease = aiVaultRelease
	aiStorageContextFilter = aiVaultContextFilter
	aiStorageValidateFinal = aiVaultValidateFinal
	aiStoragePreflight = aiVaultPreflight
}

func installAIStorage(c *core.Core) error {
	s := c.Config.AIStorage
	key, err := base64.StdEncoding.DecodeString(os.Getenv(s.GrantPublicKeyEnv))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("AI storage grant public key must be base64 Ed25519 public key")
	}
	store, err := aivault.New(c.DB, c.Engine, aivault.Limits{MaxResources: s.MaxResources, MaxDocuments: s.MaxDocuments, MaxEdges: s.MaxDocuments, MaxItemBytes: s.MaxItemBytes, MaxTextBytes: min(s.MaxItemBytes, 1<<20), MaxMetadataBytes: min(s.MaxItemBytes, 65536), MaxVectorDimensions: s.MaxVectorDimensions, MaxSearchResults: s.MaxResults, MaxBFSDepth: s.MaxGraphDepth})
	if err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(c.Context, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c.Context, `CREATE TABLE IF NOT EXISTS ai_storage_schema(version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(c.Context, `SELECT COALESCE(MAX(version),0) FROM ai_storage_schema`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("unsupported AI storage grant schema")
	}
	if _, err = tx.ExecContext(c.Context, `CREATE TABLE IF NOT EXISTS ai_storage_grants(nonce TEXT PRIMARY KEY,request_id TEXT NOT NULL UNIQUE,token_hash TEXT NOT NULL,token BLOB NOT NULL,expires_at INTEGER NOT NULL,used_at INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0);CREATE TABLE IF NOT EXISTS ai_storage_revocations(nonce TEXT PRIMARY KEY);CREATE INDEX IF NOT EXISTS ai_storage_grants_expiry ON ai_storage_grants(expires_at); INSERT OR IGNORE INTO ai_storage_schema VALUES(1)`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	aiVaults.Store(c, aiVaultState{Store: store, Key: key})
	if done := c.Context.Done(); done != nil {
		go func() { <-done; aiVaults.Delete(c) }()
	}
	c.AddManagementRoute("PUT /management/v1/ai/storage/resources/{resource}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope    aivault.Scope   `json:"scope"`
			Owner    string          `json:"owner"`
			Metadata json.RawMessage `json:"metadata"`
		}
		if !core.Decode(w, r, &in, int64(s.MaxItemBytes)) {
			return
		}
		out, err := store.CreateResource(r.Context(), aivault.ResourceSpec{ID: r.PathValue("resource"), Scope: in.Scope, Owner: in.Owner, Metadata: in.Metadata})
		vaultReply(w, out, err)
	})
	c.AddManagementRoute("DELETE /management/v1/ai/storage/resources/{resource}", func(w http.ResponseWriter, r *http.Request) {
		vaultReply(w, map[string]bool{"deleted": true}, store.DeleteResource(r.Context(), r.PathValue("resource")))
	})
	c.AddManagementRoute("PUT /management/v1/ai/storage/resources/{resource}/documents/{document}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text     string          `json:"text"`
			Vector   []float64       `json:"vector"`
			Filename string          `json:"filename"`
			File     []byte          `json:"file"`
			Metadata json.RawMessage `json:"metadata"`
		}
		if !core.Decode(w, r, &in, int64(s.MaxItemBytes)*2+65536) {
			return
		}
		out, err := store.PutDocument(r.Context(), r.PathValue("resource"), aivault.DocumentInput{ID: r.PathValue("document"), Text: in.Text, Vector: in.Vector, Filename: in.Filename, File: in.File, Metadata: in.Metadata})
		if err != nil {
			vaultReply(w, nil, err)
			return
		}
		core.JSON(w, 200, map[string]string{"id": out.ID, "resource_id": out.ResourceID})
	})
	c.AddManagementRoute("GET /management/v1/ai/storage/resources/{resource}/documents/{document}", func(w http.ResponseWriter, r *http.Request) {
		out, err := store.GetDocument(r.Context(), r.PathValue("resource"), r.PathValue("document"))
		vaultReply(w, out, err)
	})
	c.AddManagementRoute("DELETE /management/v1/ai/storage/resources/{resource}/documents/{document}", func(w http.ResponseWriter, r *http.Request) {
		vaultReply(w, map[string]bool{"deleted": true}, store.DeleteDocument(r.Context(), r.PathValue("resource"), r.PathValue("document")))
	})
	c.AddManagementRoute("PUT /management/v1/ai/storage/resources/{resource}/edges/{edge}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			From   string    `json:"from"`
			To     string    `json:"to"`
			Label  string    `json:"label"`
			Vector []float64 `json:"vector"`
		}
		if !core.Decode(w, r, &in, int64(s.MaxItemBytes)) {
			return
		}
		out, err := store.PutEdge(r.Context(), r.PathValue("resource"), aivault.EdgeInput{ID: r.PathValue("edge"), From: in.From, To: in.To, Label: in.Label, Vector: in.Vector})
		vaultReply(w, out, err)
	})
	c.AddManagementRoute("DELETE /management/v1/ai/storage/resources/{resource}/edges/{edge}", func(w http.ResponseWriter, r *http.Request) {
		vaultReply(w, map[string]bool{"deleted": true}, store.DeleteEdge(r.Context(), r.PathValue("resource"), r.PathValue("edge")))
	})
	c.AddManagementRoute("GET /management/v1/ai/storage/requests/{request}", func(w http.ResponseWriter, r *http.Request) { aiVaultRequest(c, w, r) })
	c.AddManagementRoute("POST /management/v1/ai/storage/grants", func(w http.ResponseWriter, r *http.Request) { aiVaultGrant(c, w, r) })
	c.AddManagementRoute("DELETE /management/v1/ai/storage/grants/{nonce}", func(w http.ResponseWriter, r *http.Request) {
		nonce := r.PathValue("nonce")
		if nonce == "" || len(nonce) > 1024 {
			core.Error(w, 400, "invalid nonce")
			return
		}
		tx, err := c.DB.BeginTx(r.Context(), nil)
		if err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO ai_storage_revocations(nonce) VALUES(?)`, nonce); err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE ai_storage_grants SET revoked=1 WHERE nonce=?`, nonce)
		}
		if err == nil {
			err = tx.Commit()
		}
		vaultReply(w, map[string]bool{"revoked": true}, err)
	})
	c.AddManagementRoute("POST /management/v1/ai/storage/mcp", func(w http.ResponseWriter, r *http.Request) { aiVaultMCP(c, store, w, r) })
	return nil
}

func vaultReply(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := 400
		if errors.Is(err, aivault.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, aivault.ErrCorrupt) {
			status = 503
		}
		core.Error(w, status, "storage operation rejected")
		return
	}
	core.JSON(w, 200, value)
}

func vaultState(c *core.Core) (aiVaultState, error) {
	v, ok := aiVaults.Load(c)
	if !ok {
		return aiVaultState{}, errors.New("AI storage unavailable")
	}
	return v.(aiVaultState), nil
}
func vaultVerify(c *core.Core, token string) (aivault.Claims, error) {
	state, err := vaultState(c)
	if err != nil {
		return aivault.Claims{}, err
	}
	s := c.Config.AIStorage
	return aivault.Verify(state.Key, s.Issuer, s.Audience, time.Duration(s.GrantTTLSeconds)*time.Second, token)
}

func vaultToolSettings(ctx context.Context, c *core.Core, job, name string) (config.Tool, config.AI, error) {
	settings := c.Config.AI
	var bot string
	if c.DB.QueryRowContext(ctx, `SELECT a.bot_name FROM ai_agents a JOIN ai_tasks t ON t.agent_id=a.id WHERE t.id=?`, job).Scan(&bot) == nil {
		var err error
		settings, _, err = c.Config.ResolveBot(bot)
		if err != nil {
			return config.Tool{}, settings, err
		}
	}
	for _, tool := range settings.Tools {
		if tool.Name == name && tool.Kind == "storage" {
			return tool, settings, nil
		}
	}
	return config.Tool{}, settings, errors.New("storage tool unavailable")
}

type vaultBinding struct {
	Task                           aiTaskIdentity
	Tool                           config.Tool
	Destination, Hash, Status, Job string
}

func vaultRequestBinding(ctx context.Context, c *core.Core, id string) (vaultBinding, error) {
	var job, name, hash, status string
	var expires int64
	if err := c.DB.QueryRowContext(ctx, `SELECT job_id,name,request_hash,status,expires_at FROM ai_policy_requests WHERE id=? AND action='tool'`, id).Scan(&job, &name, &hash, &status, &expires); err != nil {
		return vaultBinding{}, err
	}
	if expires <= time.Now().Unix() {
		return vaultBinding{}, errors.New("storage policy request expired")
	}
	tool, settings, err := vaultToolSettings(ctx, c, job, name)
	if err != nil {
		return vaultBinding{}, err
	}
	ctx = context.WithValue(ctx, aiSettingsKey{}, settings)
	_, destination := policyProvider(ctx, c, job, settings)
	if destination == "" {
		return vaultBinding{}, errors.New("provider unavailable")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return vaultBinding{}, err
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil {
		return vaultBinding{}, err
	}
	check := task
	check.Status = "running"
	if (task.Status != "awaiting_approval" && task.Status != "queued" && task.Status != "running") || !aiTaskAuthorized(ctx, tx, check) {
		return vaultBinding{}, errors.New("source authorization revoked")
	}
	if _, err = vaultResourceAuthorized(ctx, tx, task, tool.Resource); err != nil {
		return vaultBinding{}, err
	}
	return vaultBinding{Task: task, Tool: tool, Destination: destination, Hash: hash, Status: status, Job: job}, nil
}

func vaultResourceAuthorized(ctx context.Context, tx *sql.Tx, task aiTaskIdentity, resource string) (string, error) {
	var scope, owner string
	if err := tx.QueryRowContext(ctx, `SELECT scope,owner FROM aivault_resources WHERE id=?`, resource).Scan(&scope, &owner); err != nil {
		return "", err
	}
	if scope == "user" && owner != task.SourceUser {
		return "", errors.New("private resource belongs to another source user")
	}
	if scope == "bot" && owner != task.User {
		return "", errors.New("private resource belongs to another AI participant")
	}
	if scope != "shared" {
		var kind string
		var members int
		if err := tx.QueryRowContext(ctx, `SELECT kind,(SELECT count(*) FROM members WHERE chat_id=? AND active=1) FROM chats WHERE id=?`, task.Chat, task.Chat).Scan(&kind, &members); err != nil || kind != "direct" || members != 2 {
			return "", errors.New("private retrieval requires a two-member direct chat")
		}
	}
	return scope + "/" + owner, nil
}

func aiVaultRequest(c *core.Core, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("request")
	binding, err := vaultRequestBinding(r.Context(), c, id)
	if err != nil {
		core.Error(w, 403, "storage request unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"request_id": id, "request_hash": binding.Hash, "chat_id": binding.Task.Chat, "source_user": binding.Task.SourceUser, "ai_user": binding.Task.User, "resource": binding.Tool.Resource, "action": binding.Tool.StorageAction, "destination": binding.Destination, "status": binding.Status})
}

func aiVaultGrant(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !core.Decode(w, r, &in, 16384) {
		return
	}
	claims, err := vaultVerify(c, in.Token)
	if err != nil {
		core.Error(w, 400, "invalid storage grant")
		return
	}
	bound, err := vaultRequestBinding(r.Context(), c, claims.RequestID)
	if err != nil || (bound.Status != "awaiting" && bound.Status != "approved") || claims.RequestHash != bound.Hash || claims.ChatID != bound.Task.Chat || claims.SourceUser != bound.Task.SourceUser || claims.AIUser != bound.Task.User || claims.Resource != bound.Tool.Resource || string(claims.Action) != bound.Tool.StorageAction || claims.Destination != bound.Destination {
		core.Error(w, 403, "storage grant scope mismatch")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var revoked bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM ai_storage_revocations WHERE nonce=?)`, claims.Nonce).Scan(&revoked); err != nil || revoked {
		core.Error(w, 403, "storage grant revoked")
		return
	}
	hash := sha256.Sum256([]byte(in.Token))
	tokenHash := hex.EncodeToString(hash[:])
	var previous string
	if tx.QueryRowContext(r.Context(), `SELECT token_hash FROM ai_storage_grants WHERE nonce=?`, claims.Nonce).Scan(&previous) == nil {
		if previous != tokenHash {
			core.Error(w, 409, "storage nonce conflict")
			return
		}
		core.JSON(w, 200, map[string]string{"nonce": claims.Nonce, "status": "granted"})
		return
	}
	sealed, err := c.Engine.Seal([]byte(in.Token), []byte("ai-storage/grant/"+claims.Nonce))
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO ai_storage_grants(nonce,request_id,token_hash,token,expires_at) VALUES(?,?,?,?,?)`, claims.Nonce, claims.RequestID, tokenHash, sealed, claims.ExpiresAt)
	}
	if err == nil {
		// Generic approval may have arrived first. A waiting task becomes
		// eligible only once both grants are durably present.
		_, err = tx.ExecContext(r.Context(), `UPDATE ai_tasks SET status='queued' WHERE id=? AND status='awaiting_approval' AND EXISTS(SELECT 1 FROM ai_policy_requests WHERE id=? AND status='approved')`, bound.Job, claims.RequestID)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE ai_jobs SET status='queued' WHERE id=? AND status='awaiting_approval' AND EXISTS(SELECT 1 FROM ai_policy_requests WHERE id=? AND status='approved')`, bound.Job, claims.RequestID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		core.Error(w, 409, "storage grant unavailable")
		return
	}
	core.JSON(w, 200, map[string]string{"nonce": claims.Nonce, "status": "granted"})
	c.Wake(bound.Task.Chat)
}

// A missing second grant pauses before marking the storage effect dispatched.
// It must not turn an approval-order race into an uncertain external effect.
func aiVaultPreflight(ctx context.Context, c *core.Core, job string, tool config.Tool, request string) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM ai_policy_requests WHERE id=? AND job_id=?`, request, job).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "approved") {
		return nil
	}
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_storage_grants WHERE request_id=?)`, request).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	} // Dispatch path validates the complete signed binding.
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil {
		return err
	}
	if err = aiPolicyMarkAwaiting(ctx, tx, c, task.Chat, job, request, time.Now().Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.Wake(task.Chat)
	return ErrAIApprovalPending
}

func vaultClaimsTx(ctx context.Context, tx *sql.Tx, c *core.Core, request string) (aivault.Claims, int64, error) {
	var nonce string
	var sealed []byte
	var used int64
	var revoked bool
	err := tx.QueryRowContext(ctx, `SELECT nonce,token,used_at,revoked FROM ai_storage_grants WHERE request_id=?`, request).Scan(&nonce, &sealed, &used, &revoked)
	if err != nil || revoked {
		return aivault.Claims{}, 0, errors.New("storage grant absent or revoked")
	}
	var tombstone bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_storage_revocations WHERE nonce=?)`, nonce).Scan(&tombstone); err != nil || tombstone {
		return aivault.Claims{}, 0, errors.New("storage grant revoked")
	}
	plain, err := c.Engine.Open(sealed, []byte("ai-storage/grant/"+nonce))
	if err != nil {
		return aivault.Claims{}, 0, err
	}
	claims, err := vaultVerify(c, string(plain))
	return claims, used, err
}

func aiVaultTool(ctx context.Context, tool config.Tool, args json.RawMessage, _ aiRequester) (string, error) {
	runtime, ok := ctx.Value(aiVaultRuntimeKey{}).(aiVaultRuntime)
	if !ok || runtime.Core == nil || runtime.Boundary == nil {
		return "", errors.New("storage runtime authorization unavailable")
	}
	c := runtime.Core
	request, _ := ctx.Value(aiPolicyTicketKey{}).(string)
	if request == "" {
		return "", errors.New("storage effect intent absent")
	}
	state, err := vaultState(c)
	if err != nil {
		return "", err
	}
	settings := aiSettings(ctx, c)
	_, destination := policyProvider(ctx, c, runtime.Job, settings)
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, runtime.Job)
	if err != nil || task.Status != "running" || !aiTaskAuthorized(ctx, tx, task) {
		return "", errors.New("storage source revoked")
	}
	claims, used, err := vaultClaimsTx(ctx, tx, c, request)
	if err != nil || used != 0 {
		return "", errors.New("storage grant unavailable or consumed")
	}
	var hash, status, name, job string
	if err = tx.QueryRowContext(ctx, `SELECT request_hash,status,name,job_id FROM ai_policy_requests WHERE id=?`, request).Scan(&hash, &status, &name, &job); err != nil || status != "dispatched" || name != tool.Name || job != runtime.Job {
		return "", errors.New("storage effect mismatch")
	}
	if claims.RequestID != request || claims.RequestHash != hash || claims.ChatID != task.Chat || claims.SourceUser != task.SourceUser || claims.AIUser != task.User || claims.Authorize(aivault.RetrievalRequest{Action: aivault.Action(tool.StorageAction), ResourceID: tool.Resource, Destination: destination}) != nil {
		return "", errors.New("storage grant binding mismatch")
	}
	label, err := vaultResourceAuthorized(ctx, tx, task, tool.Resource)
	if err != nil {
		return "", err
	}
	boundary := *runtime.Boundary
	if boundary != nil && (boundary.Label != label || boundary.Destination != destination) {
		return "", errors.New("mixed storage confidentiality labels prohibited")
	}
	result, err := tx.ExecContext(ctx, `UPDATE ai_storage_grants SET used_at=? WHERE nonce=? AND used_at=0 AND revoked=0`, time.Now().Unix(), claims.Nonce)
	if err != nil {
		return "", err
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		return "", errors.New("storage grant already consumed")
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if boundary == nil {
		boundary = &aiStorageBoundary{Label: label, Destination: destination}
		*runtime.Boundary = boundary
	}
	boundary.Requests = append(boundary.Requests, request)
	return vaultQuery(ctx, state.Store, tool.Resource, tool.StorageAction, args, c.Config.AIStorage.MaxResults)
}

func aiVaultRelease(ctx context.Context, c *core.Core, job string, boundary *aiStorageBoundary, action, destination string) error {
	if boundary == nil {
		return nil
	}
	if action != "provider" && action != "storage" {
		return errors.New("retrieved context may not be released to another tool")
	}
	if action == "provider" && destination != boundary.Destination {
		return errors.New("retrieved context destination changed")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil || !aiTaskAuthorized(ctx, tx, task) {
		return errors.New("retrieval access revoked")
	}
	for _, request := range boundary.Requests {
		claims, used, err := vaultClaimsTx(ctx, tx, c, request)
		if err != nil || used == 0 || claims.Destination != boundary.Destination || claims.ChatID != task.Chat || claims.SourceUser != task.SourceUser || claims.AIUser != task.User {
			return errors.New("retrieval grant revoked or expired")
		}
		label, err := vaultResourceAuthorized(ctx, tx, task, claims.Resource)
		if err != nil || label != boundary.Label {
			return errors.New("retrieval scope changed")
		}
	}
	return nil
}

func aiVaultContextFilter(ctx context.Context, c *core.Core, job string, turns []aiTurn) ([]aiTurn, error) {
	var used bool
	err := c.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_storage_grants g JOIN ai_policy_requests r ON r.id=g.request_id WHERE r.job_id=? AND g.used_at>0)`, job).Scan(&used)
	if used {
		return []aiTurn{}, err
	}
	return turns, err
}
func aiVaultValidateFinal(ctx context.Context, tx *sql.Tx, c *core.Core, job string) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.request_id FROM ai_storage_grants g JOIN ai_policy_requests r ON r.id=g.request_id WHERE r.job_id=? AND g.used_at>0`, job)
	if err != nil {
		return err
	}
	requests := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		requests = append(requests, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil {
		return err
	}
	for _, request := range requests {
		claims, _, err := vaultClaimsTx(ctx, tx, c, request)
		if err != nil {
			return err
		}
		if claims.ChatID != task.Chat || claims.SourceUser != task.SourceUser || claims.AIUser != task.User {
			return errors.New("final retrieval binding mismatch")
		}
		if _, err = vaultResourceAuthorized(ctx, tx, task, claims.Resource); err != nil {
			return err
		}
	}
	return nil
}

func vaultQuery(ctx context.Context, store *aivault.Store, resource, action string, args json.RawMessage, maxResults int) (string, error) {
	var q struct {
		DocumentID string    `json:"document_id"`
		Query      string    `json:"query"`
		Vector     []float64 `json:"vector"`
		Start      string    `json:"start"`
		Depth      int       `json:"depth"`
		Limit      int       `json:"limit"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		return "", err
	}
	if q.Limit < 0 || q.Limit > maxResults {
		return "", errors.New("retrieval result limit exceeded")
	}
	if q.Limit == 0 {
		q.Limit = min(8, maxResults)
	}
	var result any
	var err error
	switch action {
	case "read":
		result, err = store.GetDocument(ctx, resource, q.DocumentID)
	case "search":
		var items []aivault.DocumentResult
		items, err = store.Search(ctx, resource, aivault.SearchQuery{Text: q.Query, Vector: q.Vector, Limit: q.Limit})
		for i := range items {
			items[i].Document.Text = vaultSnippet(items[i].Document.Text, 4096)
			items[i].Document.File = nil
			items[i].Document.Vector = nil
			items[i].Document.Metadata = nil
		}
		result = items
	case "graph":
		result, err = store.GraphBFS(ctx, resource, aivault.GraphQuery{Start: q.Start, Depth: q.Depth, Limit: q.Limit})
	default:
		return "", errors.New("unsupported storage action")
	}
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(result)
	if len(raw) > 65536 {
		return "", errors.New("retrieval response too large")
	}
	return string(raw), err
}
func vaultSnippet(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Administrative MCP access has the same authority as the management API.
// Models use the scoped storage runner above, never this management bearer.
func aiVaultMCP(c *core.Core, store *aivault.Store, w http.ResponseWriter, r *http.Request) {
	var in struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if !core.Decode(w, r, &in, int64(c.Config.AIStorage.MaxItemBytes)*2+65536) {
		return
	}
	if in.JSONRPC != "2.0" {
		core.Error(w, 400, "invalid RPC version")
		return
	}
	respond := func(result any) { core.JSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result}) }
	switch in.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    json.RawMessage `json:"capabilities"`
			ClientInfo      json.RawMessage `json:"clientInfo"`
		}
		if json.Unmarshal(in.Params, &params) != nil || (params.ProtocolVersion != "2025-03-26" && params.ProtocolVersion != "2025-06-18") {
			core.Error(w, 400, "unsupported MCP version")
			return
		}
		respond(map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "QGramm encrypted storage", "version": "1"}})
	case "notifications/initialized":
		w.WriteHeader(202)
	case "tools/list":
		schema := map[string]any{"type": "object", "properties": map[string]any{"resource": map[string]string{"type": "string"}, "document_id": map[string]string{"type": "string"}, "query": map[string]string{"type": "string"}, "vector": map[string]any{"type": "array", "items": map[string]string{"type": "number"}}, "start": map[string]string{"type": "string"}, "depth": map[string]string{"type": "integer"}, "limit": map[string]string{"type": "integer"}}, "required": []string{"resource"}, "additionalProperties": false}
		items := []map[string]any{}
		for _, name := range []string{"read", "search", "graph"} {
			items = append(items, map[string]any{"name": name, "description": "Administrative encrypted-resource retrieval", "inputSchema": schema})
		}
		respond(map[string]any{"tools": items})
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(in.Params, &params) != nil {
			core.Error(w, 400, "invalid tool call")
			return
		}
		var resource string
		if json.Unmarshal(params.Arguments["resource"], &resource) != nil {
			core.Error(w, 400, "resource required")
			return
		}
		delete(params.Arguments, "resource")
		args, _ := json.Marshal(params.Arguments)
		result, err := vaultQuery(r.Context(), store, resource, params.Name, args, c.Config.AIStorage.MaxResults)
		if err != nil {
			respond(map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": "storage operation rejected"}}})
			return
		}
		respond(map[string]any{"content": []map[string]string{{"type": "text", "text": result}}, "isError": false})
	default:
		core.JSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": in.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}
