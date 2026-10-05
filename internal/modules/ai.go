//go:build qg_openai || qg_anthropic

package modules

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
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

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type aiCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type aiTurn struct {
	Role       string   `json:"role"`
	Content    string   `json:"content"`
	Calls      []aiCall `json:"calls,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
}
type aiUsage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
	Known, InputKnown, OutputKnown                               bool
}
type aiAnswer struct {
	Text  string
	Calls []aiCall
	Usage aiUsage
}
type aiPolicyEffect struct {
	ID, Job, Action, Name, Destination, RequestHash                                                string
	Required                                                                                       bool
	Epoch, SessionVersion                                                                          int64
	ReserveMicrounits, InputPriceMicrounitsPerMillionTokens, OutputPriceMicrounitsPerMillionTokens int64
}

var ErrAIApprovalPending = errors.New("AI approval pending")
var aiPolicyInstall func(*core.Core) error
var aiPolicyConversation func(context.Context, *core.Core, string, aiProvider, []aiTurn, []config.Tool, aiRequester) (string, error)
var aiSavedContinuation func(context.Context, *sql.Tx, *core.Core, string) ([]byte, error)
var aiReadUsage func(string, []byte, aiUsage, bool) (aiUsage, error)
var aiPolicyActive func(context.Context) bool

func aiAssertPolicyEffect(ctx context.Context, enabled bool) error {
	if enabled && (aiPolicyActive == nil || !aiPolicyActive(ctx)) {
		return errors.New("AI policy gateway required")
	}
	return nil
}

type aiProgressFlushKey struct{}

type aiProvider func(context.Context, config.Config, []aiTurn, []config.Tool, aiRequester) (aiAnswer, error)
type aiToolRunner func(context.Context, config.Tool, json.RawMessage, aiRequester) (string, error)

var aiProviders = map[string]aiProvider{}
var aiTools = map[string]aiToolRunner{}
var aiInstalled sync.Map

// Optional streaming code is linked only by qg_ai_streaming. The callback
// commits encrypted progress before exposing its cursor to clients.
var aiStreamContext func(context.Context, func(string, json.RawMessage) error) context.Context
var aiProgressInstall func(*core.Core) error
var aiProgressPrepare func(context.Context, *core.Core, string) (context.Context, func() error)

type aiSettingsKey struct{}

type aiActiveKey struct {
	Core *core.Core
	Job  string
}

var aiActive sync.Map

func aiCancelActive(c *core.Core, job string) {
	if value, ok := aiActive.Load(aiActiveKey{c, job}); ok {
		value.(context.CancelFunc)()
	}
}

type aiTaskIdentity struct {
	Chat, User, Device, SourceUser, SourceDevice, SourceMessage, Mode, Status string
	Epoch                                                                     int64
	State                                                                     []byte
}

func aiSettings(ctx context.Context, c *core.Core) config.AI {
	if settings, ok := ctx.Value(aiSettingsKey{}).(config.AI); ok {
		return settings
	}
	return c.Config.AI
}

func aiEffectiveConfig(ctx context.Context, c *core.Core) config.Config {
	cfg := c.Config
	cfg.AI = aiSettings(ctx, c)
	return cfg
}

type aiIdentity struct {
	PublicKey, SigningKey                 string
	State, Welcome, GroupID, GroupContext []byte
	Epoch                                 int64
}

var aiCreateMLS func(*core.Core, string, string, string, []byte) (aiIdentity, error)
var aiInitRelay func(context.Context, *core.Core, *sql.Tx, string, string, string, aiIdentity) error
var aiOpenMLS func(*core.Core, string, []byte, []byte, []byte) ([]byte, []byte, error)
var aiSealMLS func(*core.Core, string, []byte, []byte, []byte) ([]byte, []byte, error)

func installAI(c *core.Core) (err error) {
	if _, loaded := aiInstalled.LoadOrStore(c, true); loaded {
		return nil
	}
	defer func() {
		if err != nil {
			aiInstalled.Delete(c)
		}
	}()
	_, e := c.DB.Exec(`CREATE TABLE IF NOT EXISTS ai_chats(chat_id TEXT PRIMARY KEY REFERENCES chats(id),user_id TEXT NOT NULL,device_id TEXT NOT NULL,provider TEXT NOT NULL,tools BLOB NOT NULL,context BLOB NOT NULL,state BLOB);
CREATE TABLE IF NOT EXISTS ai_jobs(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES ai_chats(chat_id),message_id TEXT NOT NULL UNIQUE,status TEXT NOT NULL,result_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS ai_jobs_queue ON ai_jobs(status,created_at);
CREATE TABLE IF NOT EXISTS ai_audit(id INTEGER PRIMARY KEY,job_id TEXT NOT NULL,action TEXT NOT NULL,tool_name TEXT NOT NULL DEFAULT '',outcome TEXT NOT NULL,created_at INTEGER NOT NULL);`)
	if e != nil {
		aiInstalled.Delete(c)
		return e
	}
	if e = installAINamed(c); e != nil {
		return e
	}
	if c.Config.Features.AIStreaming && aiProgressInstall != nil {
		if e = aiProgressInstall(c); e != nil {
			return e
		}
	}
	if c.Config.Features.AIPolicy {
		if aiPolicyInstall == nil {
			return errors.New("AI policy module unavailable")
		}
		if e = aiPolicyInstall(c); e != nil {
			return e
		}
	}
	if c.Config.Features.AIPolicy && !c.Config.Features.AIStreaming {
		c.AddRoute("POST /v1/chats/{chat}/ai/jobs/{job}/cancel", func(w http.ResponseWriter, r *http.Request, id core.Identity) { cancelAIJob(c, w, r, id) })
	}
	// In-flight external effects have unknown outcomes after a restart. Never
	// reissue them automatically, including provider calls or side-effect tools.
	if _, e = c.DB.Exec(`UPDATE ai_jobs SET status='uncertain',updated_at=? WHERE status='running'`, time.Now().Unix()); e != nil {
		return e
	}
	c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
		var chat string
		err := tx.QueryRowContext(ctx, `SELECT a.chat_id FROM ai_chats a JOIN messages m ON m.chat_id=a.chat_id WHERE m.id=?`, message).Scan(&chat)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Context can contain paraphrases of the deleted input. Clear the entire
		// conversation context and invalidate captured in-flight contexts atomically.
		blob, err := c.Engine.Seal([]byte("[]"), []byte("ai/context/"+chat))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_chats SET context=? WHERE chat_id=?`, blob, chat); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_jobs SET status='failed',updated_at=? WHERE chat_id=? AND status IN ('queued','running','awaiting_approval')`, time.Now().Unix(), chat)
		return err
	})
	c.InTransaction = append(c.InTransaction, func(ctx context.Context, tx *sql.Tx, id core.Identity, chat string, m core.Message) error {
		var aiUser string
		e := tx.QueryRowContext(ctx, `SELECT user_id FROM ai_chats WHERE chat_id=?`, chat).Scan(&aiUser)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if aiUser == id.UserID {
			return nil
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM ai_jobs WHERE chat_id=? AND status IN ('queued','running','awaiting_approval')`, chat).Scan(&count); e != nil {
			return e
		}
		if count >= 64 {
			return &core.APIError{Status: 429, Message: "AI queue full"}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO ai_jobs VALUES(?,?,?,'queued','',?,?)`, uuid.NewString(), chat, m.ID, time.Now().Unix(), time.Now().Unix())
		return e
	})
	c.AddManagementRoute("POST /management/v1/ai/participants", func(w http.ResponseWriter, r *http.Request) { aiCreateParticipant(c, w, r) })
	c.AddManagementRoute("PUT /management/v1/ai/chats/{chat}/tools", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tools []string `json:"tools"`
		}
		if !core.Decode(w, r, &in, 8192) {
			return
		}
		if _, e := aiAllowed(c, in.Tools); e != nil {
			core.Error(w, 400, "invalid tool allowlist")
			return
		}
		encoded, _ := json.Marshal(in.Tools)
		res, e := c.DB.ExecContext(r.Context(), `UPDATE ai_chats SET tools=? WHERE chat_id=?`, encoded, r.PathValue("chat"))
		if e != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			core.Error(w, 404, "AI chat unavailable")
			return
		}
		core.JSON(w, 200, map[string]any{"tools": in.Tools})
	})
	c.AddManagementRoute("GET /management/v1/ai/chats/{chat}/jobs", func(w http.ResponseWriter, r *http.Request) {
		rows, e := c.DB.QueryContext(r.Context(), `SELECT id,message_id,status,result_id,created_at,updated_at FROM ai_jobs WHERE chat_id=? ORDER BY created_at DESC LIMIT 100`, r.PathValue("chat"))
		if e != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		defer rows.Close()
		jobs := []map[string]any{}
		for rows.Next() {
			var id, msg, status, result string
			var created, updated int64
			if rows.Scan(&id, &msg, &status, &result, &created, &updated) != nil {
				core.Error(w, 503, "storage unavailable")
				return
			}
			jobs = append(jobs, map[string]any{"id": id, "message_id": msg, "status": status, "result_id": result, "created_at": created, "updated_at": updated})
		}
		if rows.Err() != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		core.JSON(w, 200, map[string]any{"jobs": jobs})
	})
	workers := c.Config.Capacity.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	var pool sync.WaitGroup
	for i := 0; i < workers; i++ {
		pool.Add(1)
		go func() {
			defer pool.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-c.Context.Done():
					return
				case <-ticker.C:
					aiNext(c)
					aiNextNamed(c)
				}
			}
		}()
	}
	go func() { pool.Wait(); aiInstalled.Delete(c) }()
	return nil
}

func aiAllowed(c *core.Core, names []string) ([]config.Tool, error) {
	out := []config.Tool{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return nil, errors.New("duplicate tool")
		}
		seen[name] = true
		found := false
		for _, t := range c.Config.AI.Tools {
			if t.Name == name && aiTools[t.Kind] != nil {
				out = append(out, t)
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("tool not configured or compiled")
		}
	}
	return out, nil
}

func aiCreateParticipant(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID     string   `json:"user_id"`
		DeviceID   string   `json:"device_id"`
		Provider   string   `json:"provider"`
		Mode       string   `json:"mode"`
		KeyPackage string   `json:"key_package"`
		Tools      []string `json:"tools"`
	}
	if !core.Decode(w, r, &in, 65536) {
		return
	}
	if in.Mode == "" {
		in.Mode = "basic"
	}
	if in.Mode != "basic" && in.Mode != "e2ee" {
		core.Error(w, 400, "invalid chat mode")
		return
	}
	settings, defaultProvider, err := c.Config.ResolveBot("")
	if err != nil {
		core.Error(w, 400, "default AI profile unavailable")
		return
	}
	if defaultProvider != "" {
		if in.Provider == "" {
			in.Provider = defaultProvider
		}
		if in.Provider != defaultProvider {
			core.Error(w, 400, "provider differs from default bot")
			return
		}
	}
	if aiProviders[in.Provider] == nil {
		core.Error(w, 400, "provider not compiled")
		return
	}
	env := settings.OpenAIKeyEnv
	if in.Provider == "anthropic" {
		env = settings.AnthropicKeyEnv
	}
	if settings.Auth != "none" && os.Getenv(env) == "" {
		core.Error(w, 503, "provider credential unavailable")
		return
	}
	if _, e := aiAllowed(c, in.Tools); e != nil {
		core.Error(w, 400, "invalid tool allowlist")
		return
	}
	for _, name := range in.Tools {
		allowed := false
		for _, tool := range settings.Tools {
			if tool.Name == name {
				allowed = true
				break
			}
		}
		if !allowed {
			core.Error(w, 400, "tool excluded by default bot")
			return
		}
	}
	var humanSigning string
	if c.DB.QueryRowContext(r.Context(), `SELECT d.signing_key FROM devices d JOIN users u ON u.id=d.user_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0`, in.DeviceID, in.UserID).Scan(&humanSigning) != nil {
		core.Error(w, 400, "active human device required")
		return
	}
	chat, user, device := uuid.NewString(), "ai-"+uuid.NewString(), uuid.NewString()
	var identity aiIdentity
	var e error
	if in.Mode == "e2ee" {
		if !c.Config.Features.E2EE || aiCreateMLS == nil {
			core.Error(w, 400, "E2EE AI unavailable in this build")
			return
		}
		kp, err := base64.StdEncoding.DecodeString(in.KeyPackage)
		if err != nil {
			core.Error(w, 400, "invalid human key package")
			return
		}
		identity, e = aiCreateMLS(c, chat, device, in.DeviceID, kp)
		if e != nil {
			core.Error(w, 400, "AI MLS admission failed")
			return
		}
		_ = humanSigning
	} else {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			core.Error(w, 500, "AI identity generation failed")
			return
		}
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			core.Error(w, 500, "AI identity generation failed")
			return
		}
		identity.PublicKey = base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
		identity.SigningKey = base64.StdEncoding.EncodeToString(pub)
	}
	contextBlob, e := c.Engine.Seal([]byte("[]"), []byte("ai/context/"+chat))
	if e != nil {
		core.Error(w, 500, "AI context creation failed")
		return
	}
	tools, _ := json.Marshal(in.Tools)
	tx, e := c.DB.BeginTx(r.Context(), nil)
	if e != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	// Recheck the authority-controlled device in the same creation transaction.
	var valid bool
	if tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM devices d JOIN users u ON u.id=d.user_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0 AND d.signing_key=?)`, in.DeviceID, in.UserID, humanSigning).Scan(&valid) != nil || !valid {
		core.Error(w, 409, "human device changed")
		return
	}
	for _, stmt := range []struct {
		query string
		args  []any
	}{{`INSERT INTO users(id) VALUES(?)`, []any{user}}, {`INSERT INTO devices(id,user_id,public_key,signing_key) VALUES(?,?,?,?)`, []any{device, user, identity.PublicKey, identity.SigningKey}}, {`INSERT INTO chats(id,kind,mode,epoch,created_at) VALUES(?,'direct',?,?,?)`, []any{chat, in.Mode, identity.Epoch, time.Now().Unix()}}, {`INSERT INTO members(chat_id,user_id,role,can_send,joined_seq) VALUES(?,?,'member',1,0),(?,?,'owner',1,0)`, []any{chat, user, chat, in.UserID}}, {`INSERT INTO ai_chats VALUES(?,?,?,?,?,?,?)`, []any{chat, user, device, in.Provider, tools, contextBlob, identity.State}}} {
		if _, e = tx.ExecContext(r.Context(), stmt.query, stmt.args...); e != nil {
			core.Error(w, 503, "AI participant persistence failed")
			return
		}
	}
	if in.Mode == "e2ee" {
		if aiInitRelay == nil || aiInitRelay(r.Context(), c, tx, chat, device, in.DeviceID, identity) != nil {
			core.Error(w, 503, "AI MLS relay initialization failed")
			return
		}
	}
	if _, e = c.Append(r.Context(), tx, chat, "ai.participant.created", "", map[string]any{"user_id": user, "device_id": device, "provider": in.Provider}); e != nil || tx.Commit() != nil {
		core.Error(w, 503, "AI participant persistence failed")
		return
	}
	c.Wake(chat)
	core.JSON(w, 201, map[string]any{"chat_id": chat, "user_id": user, "device_id": device, "provider": in.Provider, "mode": in.Mode, "epoch": identity.Epoch, "welcome": base64.StdEncoding.EncodeToString(identity.Welcome), "tools": in.Tools})
}

func aiNext(c *core.Core) {
	aiNextWith(c, aiConversation)
}

type aiConversationRunner func(context.Context, *core.Core, string, string, []aiTurn, []config.Tool) (string, error)

func aiNextWith(c *core.Core, conversation aiConversationRunner) {
	ctx, cancel := context.WithTimeout(c.Context, 3*time.Minute)
	defer cancel()
	settings, defaultProvider, err := c.Config.ResolveBot("")
	if err != nil {
		return
	}
	ctx = context.WithValue(ctx, aiSettingsKey{}, settings)
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var job, chat, message, user, device, provider, mode, sender, sourceDevice, operation string
	var state, contextBlob, allowedBlob, payload []byte
	var epoch int64
	e = tx.QueryRowContext(ctx, `SELECT j.id,j.chat_id,j.message_id,a.user_id,a.device_id,a.provider,a.tools,a.context,a.state,ch.mode,ch.epoch,m.payload,m.sender,m.device_id,m.operation_id FROM ai_jobs j JOIN ai_chats a ON a.chat_id=j.chat_id JOIN chats ch ON ch.id=j.chat_id JOIN messages m ON m.id=j.message_id WHERE j.status='queued' AND NOT EXISTS(SELECT 1 FROM ai_jobs active WHERE active.chat_id=j.chat_id AND active.status IN ('running','awaiting_approval')) ORDER BY m.seq,j.created_at LIMIT 1`).Scan(&job, &chat, &message, &user, &device, &provider, &allowedBlob, &contextBlob, &state, &mode, &epoch, &payload, &sender, &sourceDevice, &operation)
	if e != nil {
		return
	}
	if _, active := aiActive.Load(aiActiveKey{c, job}); active {
		return
	}
	var resume []byte
	if c.Config.Features.AIPolicy && aiSavedContinuation != nil {
		resume, e = aiSavedContinuation(ctx, tx, c, job)
		if e != nil {
			return
		}
	}
	if defaultProvider != "" && provider != defaultProvider {
		aiFailTx(ctx, tx, job, "failed")
		_ = tx.Commit()
		return
	}
	var authorized bool
	if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM devices d JOIN users u ON u.id=d.user_id JOIN members m ON m.user_id=d.user_id JOIN chats ch ON ch.id=m.chat_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0 AND m.chat_id=? AND m.active=1 AND m.can_send=1 AND ch.pending=0) AND EXISTS(SELECT 1 FROM devices d JOIN users u ON u.id=d.user_id JOIN members m ON m.user_id=d.user_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0 AND m.chat_id=? AND m.active=1 AND m.can_send=1)`, sourceDevice, sender, chat, device, user, chat).Scan(&authorized) != nil || !authorized {
		aiFailTx(ctx, tx, job, "failed")
		_ = tx.Commit()
		return
	}
	var turns []aiTurn
	if len(resume) > 0 {
		var saved struct {
			Turns []aiTurn `json:"turns"`
		}
		if e = json.Unmarshal(resume, &saved); e != nil {
			return
		}
		// The execution loop restores tool continuations separately. Persisted
		// conversation history keeps the same user/final-answer contract as a
		// fresh job, without incomplete assistant tool-call previews.
		for _, turn := range saved.Turns {
			if turn.Role != "tool" && len(turn.Calls) == 0 {
				turns = append(turns, turn)
			}
		}
	} else {
		plain, e := c.Engine.Open(payload, []byte("message/"+message))
		if e != nil {
			aiFailTx(ctx, tx, job, "failed")
			_ = tx.Commit()
			return
		}
		if mode == "e2ee" && len(resume) == 0 {
			if aiOpenMLS == nil {
				aiFailTx(ctx, tx, job, "failed")
				_ = tx.Commit()
				return
			}
			plain, state, e = aiOpenMLS(c, chat, state, plain, cryptoenc.Binding(chat, sender, sourceDevice, operation))
			if e != nil {
				aiFailTx(ctx, tx, job, "failed")
				_ = tx.Commit()
				return
			}
		}
		contextRaw, e := c.Engine.Open(contextBlob, []byte("ai/context/"+chat))
		if e != nil || json.Unmarshal(contextRaw, &turns) != nil {
			aiFailTx(ctx, tx, job, "failed")
			_ = tx.Commit()
			return
		}
		// Payloads are UTF-8 application text. Attachments and opaque structures are
		// not automatically fetched or interpreted as instructions to tools.
		if !utf8.Valid(plain) {
			aiFailTx(ctx, tx, job, "failed")
			_ = tx.Commit()
			return
		}
		turns = append(turns, aiTurn{Role: "user", Content: string(plain)})
		turns = aiBoundContext(turns, settings)
		raw, _ := json.Marshal(turns)
		if len(raw) > settings.MaxContextBytes {
			aiFailTx(ctx, tx, job, "failed")
			_ = tx.Commit()
			return
		}
	}
	// Commit received MLS state before external access, but retain only the
	// last successful conversation context. Failed/cancelled input must not
	// silently enter the next provider invocation.
	if _, e = tx.ExecContext(ctx, `UPDATE ai_chats SET state=? WHERE chat_id=?`, state, chat); e != nil {
		return
	}
	if _, e = tx.ExecContext(ctx, `UPDATE ai_jobs SET status='running',updated_at=? WHERE id=? AND status='queued'`, time.Now().Unix(), job); e != nil {
		return
	}
	if tx.Commit() != nil {
		return
	}
	var names []string
	if json.Unmarshal(allowedBlob, &names) != nil {
		aiFinishError(c, job, "failed")
		return
	}
	tools, e := aiAllowed(c, names)
	if e != nil {
		aiFinishError(c, job, "failed")
		return
	}
	answer, e := conversation(ctx, c, job, provider, turns, tools)
	if e != nil {
		if errors.Is(e, ErrAIApprovalPending) {
			return
		}
		aiFinishError(c, job, "uncertain")
		return
	}
	if answer == "" || len(answer) > c.Config.Policy.MaxMessageBytes {
		aiFinishError(c, job, "failed")
		return
	}
	aiComplete(ctx, c, job, chat, user, device, mode, epoch, state, turns, answer)
}

func aiBoundContext(turns []aiTurn, settings config.AI) []aiTurn {
	maxTurns, maxBytes := settings.MaxContextTurns, settings.MaxContextBytes
	if maxTurns <= 0 || maxTurns > 20 {
		maxTurns = 20
	}
	if maxBytes <= 0 || maxBytes > 262144 {
		maxBytes = 262144
	}
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
		for len(turns) > 1 && turns[0].Role != "user" {
			turns = turns[1:]
		}
	}
	for len(turns) > 1 {
		raw, _ := json.Marshal(turns)
		if len(raw) <= maxBytes {
			break
		}
		turns = turns[1:]
		for len(turns) > 1 && turns[0].Role != "user" {
			turns = turns[1:]
		}
	}
	return turns
}
func aiFailTx(ctx context.Context, tx *sql.Tx, job, status string) {
	_, _ = tx.ExecContext(ctx, `UPDATE ai_jobs SET status=?,updated_at=? WHERE id=? AND status IN('queued','running')`, status, time.Now().Unix(), job)
	_, _ = tx.ExecContext(ctx, `INSERT INTO ai_audit(job_id,action,outcome,created_at) VALUES(?,'job',?,?)`, job, status, time.Now().Unix())
}
func aiFinishError(c *core.Core, job, status string) {
	tx, e := c.DB.BeginTx(c.Context, nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	aiFailTx(c.Context, tx, job, status)
	_ = tx.Commit()
}
func aiAudit(c *core.Core, job, action, tool, outcome string) {
	_, _ = c.DB.ExecContext(c.Context, `INSERT INTO ai_audit(job_id,action,tool_name,outcome,created_at) VALUES(?,?,?,?,?)`, job, action, tool, outcome, time.Now().Unix())
}
func aiConversation(ctx context.Context, c *core.Core, job, provider string, turns []aiTurn, tools []config.Tool) (string, error) {
	fn := aiProviders[provider]
	return aiConversationWith(ctx, c, job, fn, turns, tools, aiRequest)
}
func aiTaskLookup(ctx context.Context, tx *sql.Tx, job string) (aiTaskIdentity, error) {
	var out aiTaskIdentity
	err := tx.QueryRowContext(ctx, `SELECT a.chat_id,a.user_id,a.device_id,m.sender,m.device_id,m.id,ch.mode,j.status,ch.epoch,a.state FROM ai_jobs j JOIN ai_chats a ON a.chat_id=j.chat_id JOIN messages m ON m.id=j.message_id JOIN chats ch ON ch.id=a.chat_id WHERE j.id=?`, job).Scan(&out.Chat, &out.User, &out.Device, &out.SourceUser, &out.SourceDevice, &out.SourceMessage, &out.Mode, &out.Status, &out.Epoch, &out.State)
	if errors.Is(err, sql.ErrNoRows) {
		return aiNamedTaskIdentity(ctx, tx, job)
	}
	return out, err
}

func aiTaskAuthorized(ctx context.Context, tx *sql.Tx, task aiTaskIdentity) bool {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m JOIN members a ON a.chat_id=m.chat_id AND a.user_id=? JOIN devices ad ON ad.user_id=a.user_id AND ad.id=? JOIN users au ON au.id=a.user_id JOIN members s ON s.chat_id=m.chat_id AND s.user_id=m.sender JOIN devices sd ON sd.id=m.device_id AND sd.user_id=s.user_id JOIN users su ON su.id=s.user_id JOIN chats ch ON ch.id=m.chat_id WHERE m.id=? AND m.deleted=0 AND m.seq>=a.joined_seq AND a.active=1 AND a.can_send=1 AND ad.revoked=0 AND au.disabled=0 AND m.seq>=s.joined_seq AND s.active=1 AND s.can_send=1 AND sd.revoked=0 AND su.disabled=0 AND ch.pending=0 AND ch.epoch=?)`, task.User, task.Device, task.SourceMessage, task.Epoch).Scan(&valid)
	return err == nil && valid
}

func aiEffectsAllowed(ctx context.Context, c *core.Core, job string) bool {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, job)
	return err == nil && task.Status == "running" && aiTaskAuthorized(ctx, tx, task)
}

func aiConversationWith(ctx context.Context, c *core.Core, job string, fn aiProvider, turns []aiTurn, tools []config.Tool, request aiRequester) (answer string, err error) {
	ctx, cancel := context.WithCancel(ctx)
	key := aiActiveKey{c, job}
	if _, loaded := aiActive.LoadOrStore(key, context.CancelFunc(cancel)); loaded {
		cancel()
		return "", errors.New("AI job already active")
	}
	defer func() { aiActive.Delete(key); cancel() }()
	settings := aiSettings(ctx, c)
	cfg := aiEffectiveConfig(ctx, c)
	ctx = context.WithValue(ctx, aiLimitKey{}, aiLimits(settings))
	if settings.Streaming && aiProgressPrepare != nil {
		var flush func() error
		ctx, flush = aiProgressPrepare(ctx, c, job)
		ctx = context.WithValue(ctx, aiProgressFlushKey{}, flush)
		// Flush is also called on errors; partial output remains explicitly partial.
		defer func() {
			if flushErr := flush(); flushErr != nil && err == nil {
				answer, err = "", flushErr
			}
		}()
	}
	if c.Config.Features.AIPolicy {
		if aiPolicyConversation == nil {
			return "", errors.New("AI policy module unavailable")
		}
		return aiPolicyConversation(ctx, c, job, fn, turns, tools, request)
	}
	request = aiBoundRequester(request)
	turns = aiBoundContext(turns, settings)
	initial, _ := json.Marshal(turns)
	if len(initial) > settings.MaxContextBytes {
		return "", errors.New("AI context exhausted")
	}
	if fn == nil {
		return "", errors.New("provider unavailable")
	}
	for step := 0; step < settings.MaxSteps; step++ {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		if !aiEffectsAllowed(ctx, c, job) {
			return "", errors.New("AI job access revoked")
		}
		a, e := fn(ctx, cfg, turns, tools, request)
		if e != nil {
			aiAudit(c, job, "provider", "", "uncertain")
			return "", e
		}
		aiAudit(c, job, "provider", "", "succeeded")
		if len(a.Calls) == 0 {
			return a.Text, nil
		}
		if len(a.Calls) > 8 {
			return "", errors.New("too many tool calls")
		}
		turns = append(turns, aiTurn{Role: "assistant", Content: a.Text, Calls: a.Calls})

		for _, call := range a.Calls {
			tool, e := aiAuthorizedTool(ctx, c, job, call, tools)
			if e != nil {
				return "", e
			}
			result, e := aiExecuteTool(ctx, c, job, tool, call, request)
			if e != nil {
				return "", e
			}
			turns = append(turns, aiTurn{Role: "tool", Content: result, ToolCallID: call.ID})
		}

		raw, _ := json.Marshal(turns)
		if len(raw) > settings.MaxContextBytes || len(turns) > settings.MaxContextTurns {
			return "", errors.New("AI context exhausted")
		}
	}
	return "", errors.New("AI step limit reached")
}

func aiAuthorizedTool(ctx context.Context, c *core.Core, job string, call aiCall, tools []config.Tool) (config.Tool, error) {
	var tool config.Tool
	found := false
	for _, candidate := range tools {
		if candidate.Name == call.Name {
			tool = candidate
			found = true
			break
		}
	}
	if !found || aiTools[tool.Kind] == nil {
		return tool, errors.New("tool permission denied")
	}
	if len(call.Arguments) > 65536 {
		return tool, errors.New("tool arguments too large")
	}
	if !aiEffectsAllowed(ctx, c, job) {
		return tool, errors.New("AI job access revoked")
	}
	var current []byte
	if c.DB.QueryRowContext(ctx, `SELECT a.tools FROM ai_chats a JOIN ai_jobs j ON j.chat_id=a.chat_id WHERE j.id=? UNION ALL SELECT s.tools FROM ai_sessions s JOIN ai_tasks t ON t.chat_id=s.chat_id AND t.agent_id=s.agent_id WHERE t.id=?`, job, job).Scan(&current) != nil {
		return tool, errors.New("tool permission unavailable")
	}
	var names []string
	permitted := false
	if json.Unmarshal(current, &names) == nil {
		for _, name := range names {
			if name == call.Name {
				permitted = true
			}
		}
	}
	if !permitted {
		return tool, errors.New("tool permission revoked")
	}
	if err := aiCheckArguments(tool.Schema, call.Arguments); err != nil {
		return tool, err
	}
	return tool, nil
}
func aiExecuteTool(ctx context.Context, c *core.Core, job string, tool config.Tool, call aiCall, request aiRequester) (string, error) {
	if err := aiAssertPolicyEffect(ctx, c.Config.Features.AIPolicy); err != nil {
		return "", err
	}
	toolCtx, cancel := aiToolContext(ctx, tool)
	defer cancel()
	result, err := aiTools[tool.Kind](toolCtx, tool, call.Arguments, request)
	if err == nil && len(result) > min(65536, aiContextLimits(toolCtx).response) {
		err = errors.New("tool response too large")
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "uncertain"
	}
	aiAudit(c, job, "tool", tool.Name, outcome)
	return result, err
}

func aiComplete(ctx context.Context, c *core.Core, job, chat, user, device, mode string, epoch int64, state []byte, turns []aiTurn, answer string) {
	settings := aiSettings(ctx, c)
	operation := "ai-" + job
	message := uuid.NewString()
	payload := []byte(answer)
	var e error
	if mode == "e2ee" {
		if aiSealMLS == nil {
			aiFinishError(c, job, "failed")
			return
		}
		// Streaming may have advanced the sender ratchet through durable chunks.
		// Reload the single-owner snapshot before sealing the final message;
		// reusing the pre-stream snapshot would reuse MLS generations.
		if e = c.DB.QueryRowContext(ctx, `SELECT state FROM ai_chats WHERE chat_id=?`, chat).Scan(&state); e != nil {
			return
		}
		payload, state, e = aiSealMLS(c, chat, state, payload, cryptoenc.Binding(chat, user, device, operation))
		if e != nil {
			aiFinishError(c, job, "failed")
			return
		}
	}
	stored, e := c.Engine.Seal(payload, []byte("message/"+message))
	if e != nil {
		return
	}
	turns = aiBoundContext(append(turns, aiTurn{Role: "assistant", Content: answer}), settings)
	raw, _ := json.Marshal(turns)
	if len(raw) > settings.MaxContextBytes {
		aiFinishError(c, job, "failed")
		return
	}
	contextBlob, e := c.Engine.Seal(raw, []byte("ai/context/"+chat))
	if e != nil {
		return
	}
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	task, lookupErr := aiTaskLookup(ctx, tx, job)
	if lookupErr != nil || task.Status != "running" {
		return
	}
	if task.Chat != chat || task.User != user || task.Device != device || task.Epoch != epoch || !aiTaskAuthorized(ctx, tx, task) {
		aiFailTx(ctx, tx, job, "failed")
		_ = tx.Commit()
		return
	}
	seq, e := c.Append(ctx, tx, chat, "message.created", message, nil)
	if e != nil {
		return
	}
	now := time.Now().Unix()
	m := core.Message{ID: message, ChatID: chat, Sender: user, DeviceID: device, OperationID: operation, Seq: seq, Revision: 1, Mode: mode, Epoch: epoch, CreatedAt: now}
	result, _ := json.Marshal(m)
	hash := sha256.Sum256(payload)
	for _, stmt := range []struct {
		query string
		args  []any
	}{{`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES(?,?,?,?,?,?,?,'{}',?,?)`, []any{message, chat, user, device, operation, seq, stored, epoch, now}}, {`INSERT INTO operations VALUES(?,?,?,?,?)`, []any{device, operation, hex.EncodeToString(hash[:]), result, now}}, {`UPDATE ai_chats SET context=?,state=? WHERE chat_id=?`, []any{contextBlob, state, chat}}, {`UPDATE ai_jobs SET status='succeeded',result_id=?,updated_at=? WHERE id=?`, []any{message, now, job}}} {
		if _, e = tx.ExecContext(ctx, stmt.query, stmt.args...); e != nil {
			return
		}
	}
	if tx.Commit() == nil {
		c.Wake(chat)
	}
}

func aiProgressMember(ctx context.Context, tx *sql.Tx, task aiTaskIdentity, id core.Identity) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT d.public_key FROM members member JOIN devices d ON d.user_id=member.user_id JOIN users u ON u.id=member.user_id JOIN messages source ON source.chat_id=member.chat_id WHERE member.chat_id=? AND member.user_id=? AND d.id=? AND member.active=1 AND d.revoked=0 AND u.disabled=0 AND source.id=? AND source.seq>=member.joined_seq AND source.deleted=0`, task.Chat, id.UserID, id.DeviceID, task.SourceMessage).Scan(&key)
	return key, err
}

func cancelAIJob(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
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
	if task.Status == "queued" || task.Status == "running" || task.Status == "awaiting_approval" {
		res, e := tx.ExecContext(r.Context(), `UPDATE ai_jobs SET status='cancelled',updated_at=? WHERE id=? AND status IN('queued','running','awaiting_approval')`, time.Now().Unix(), job)
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
