//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

// The policy module is deliberately kept at the storage boundary.  Provider
// workers call aiPolicyBegin before an external effect and aiPolicySettle
// after it; neither function ever puts a prompt, continuation, URL query, or
// provider token in an unsealed SQLite column.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

// ErrAIApprovalPending is intentionally comparable: the worker turns it into
// a durable awaiting_approval job without exposing the continuation.

const aiPolicySchemaVersion = 1

type aiPolicyKeyState struct {
	key ed25519.PublicKey
}

var aiPolicyKeys sync.Map // *core.Core -> aiPolicyKeyState

type aiPolicyStored struct {
	Effect         aiPolicyEffect `json:"effect"`
	Continuation   []byte         `json:"continuation"`
	SessionEpoch   int64          `json:"session_epoch"`
	SessionVersion int64          `json:"session_version"`
}

type aiPolicyAmounts struct {
	Reserved         int64  `json:"reserved"`
	Cost             int64  `json:"cost"`
	Used             int64  `json:"used"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Known            bool   `json:"known"`
	Currency         string `json:"currency"`
}

func aiPolicyAccountAAD(scope, scopeID, day string) []byte {
	return []byte("ai-policy/account/" + scope + "/" + scopeID + "/" + day)
}

type aiPolicyGrantClaims struct {
	V               int    `json:"v"`
	JTI             string `json:"jti"`
	RequestID       string `json:"request_id"`
	JobID           string `json:"job_id"`
	ChatID          string `json:"chat_id"`
	SourceUser      string `json:"source_user"`
	SourceDevice    string `json:"source_device"`
	AIUser          string `json:"ai_user"`
	AIDevice        string `json:"ai_device"`
	Epoch           int64  `json:"epoch"`
	Action          string `json:"action"`
	Name            string `json:"name"`
	RequestHash     string `json:"request_hash"`
	DestinationHash string `json:"destination_hash"`
	Issuer          string `json:"iss"`
	Audience        string `json:"aud"`
	IssuedAt        int64  `json:"iat"`
	ExpiresAt       int64  `json:"exp"`
	jwt.RegisteredClaims
}

// RegisteredClaims is embedded only for jwt.Parser's time checks. The fields
// above are the canonical representation used for exact claim comparison.
// A duplicate JSON key is rejected by strictJSON below before parsing.

func installAIPolicy(c *core.Core) error {
	if !c.Config.Features.AIPolicy {
		return errors.New("ai policy installer enabled while feature is disabled")
	}
	ref := strings.TrimSpace(c.Config.AIPolicy.GrantPublicKeyEnv)
	if ref == "" {
		return errors.New("ai policy grant_public_key_env is required")
	}
	b, err := base64.StdEncoding.DecodeString(os.Getenv(ref))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return errors.New("AI grant public key must be base64 Ed25519 public key")
	}
	tx, err := c.DB.BeginTx(c.Context, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ai_policy_schema(version INTEGER PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS ai_policy_requests(id TEXT PRIMARY KEY,job_id TEXT NOT NULL,chat_id TEXT NOT NULL,source_user TEXT NOT NULL,source_device TEXT NOT NULL,ai_user TEXT NOT NULL,ai_device TEXT NOT NULL,epoch INTEGER NOT NULL,request_hash TEXT NOT NULL,action TEXT NOT NULL,name TEXT NOT NULL,destination_hash TEXT NOT NULL,payload BLOB NOT NULL,status TEXT NOT NULL,expires_at INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,outcome TEXT NOT NULL DEFAULT '',dispatched_at INTEGER NOT NULL DEFAULT 0,UNIQUE(job_id,id))`,
		`CREATE INDEX IF NOT EXISTS ai_policy_requests_job ON ai_policy_requests(job_id,status)`,
		`CREATE INDEX IF NOT EXISTS ai_policy_requests_expiry ON ai_policy_requests(status,expires_at)`,
		`CREATE TABLE IF NOT EXISTS ai_policy_grants(id TEXT PRIMARY KEY,request_id TEXT NOT NULL,nonce TEXT NOT NULL,token_hash TEXT NOT NULL,token BLOB NOT NULL,issued_at INTEGER NOT NULL,expires_at INTEGER NOT NULL,used_at INTEGER NOT NULL DEFAULT 0,revoked_at INTEGER NOT NULL DEFAULT 0,UNIQUE(request_id,token_hash),UNIQUE(nonce))`,
		`CREATE TABLE IF NOT EXISTS ai_policy_revocations(id TEXT PRIMARY KEY,nonce TEXT NOT NULL,revoked_at INTEGER NOT NULL,UNIQUE(nonce))`,
		`CREATE TABLE IF NOT EXISTS ai_policy_ledger(id INTEGER PRIMARY KEY,request_id TEXT NOT NULL,job_id TEXT NOT NULL,scope TEXT NOT NULL,scope_id TEXT NOT NULL,day TEXT NOT NULL,reserved INTEGER NOT NULL DEFAULT 0,cost INTEGER NOT NULL DEFAULT 0,amounts BLOB NOT NULL,payload BLOB NOT NULL,status TEXT NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,UNIQUE(request_id,scope))`,
		`CREATE INDEX IF NOT EXISTS ai_policy_ledger_account ON ai_policy_ledger(scope,scope_id,day,status)`,
		`CREATE TABLE IF NOT EXISTS ai_policy_accounts(scope TEXT NOT NULL,scope_id TEXT NOT NULL,day TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(scope,scope_id,day))`,
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(c.Context, statement); err != nil {
			return err
		}
	}
	var version int
	if err = tx.QueryRowContext(c.Context, `SELECT COALESCE(MAX(version),0) FROM ai_policy_schema`).Scan(&version); err != nil {
		return err
	}
	if version > aiPolicySchemaVersion {
		return fmt.Errorf("unsupported AI policy schema version %d", version)
	}
	if version < aiPolicySchemaVersion {
		if _, err = tx.ExecContext(c.Context, `INSERT INTO ai_policy_schema(version) VALUES(?)`, aiPolicySchemaVersion); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	aiPolicyKeys.Store(c, aiPolicyKeyState{key: append(ed25519.PublicKey(nil), b...)})
	// An effect that was durably marked dispatched may have crossed a process
	// boundary before the provider response arrived. Keep its reservation and
	// make the outcome explicitly uncertain; the worker must never resend it.
	startupNow := aiPolicyNow()
	if _, err = c.DB.ExecContext(c.Context, `UPDATE ai_policy_requests SET status='uncertain',updated_at=? WHERE status='dispatched'`, startupNow); err != nil {
		return err
	}
	if _, err = c.DB.ExecContext(c.Context, `UPDATE ai_jobs SET status='uncertain',updated_at=? WHERE id IN (SELECT job_id FROM ai_policy_requests WHERE status='uncertain') AND status='running'`, startupNow); err != nil {
		return err
	}
	if _, err = c.DB.ExecContext(c.Context, `UPDATE ai_tasks SET status='uncertain',updated_at=? WHERE id IN (SELECT job_id FROM ai_policy_requests WHERE status='uncertain') AND status='running'`, startupNow); err != nil {
		return err
	}
	if done := c.Context.Done(); done != nil {
		go func() { <-done; aiPolicyKeys.Delete(c) }()
	}
	c.Cleanup = append(c.Cleanup, func(ctx context.Context) error { return aiPolicyCleanupExpired(ctx, c) })
	c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
		var chat string
		if err := tx.QueryRowContext(ctx, `SELECT chat_id FROM messages WHERE id=?`, message).Scan(&chat); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id,payload,status FROM ai_policy_requests WHERE chat_id=?`, chat)
		if err != nil {
			return err
		}
		defer rows.Close()
		type scrub struct {
			id      string
			payload []byte
			status  string
		}
		items := []scrub{}
		for rows.Next() {
			var item scrub
			if err = rows.Scan(&item.id, &item.payload, &item.status); err != nil {
				return err
			}
			items = append(items, item)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		for _, item := range items {
			stored, openErr := aiPolicyOpenStored(c, item.id, item.payload)
			if openErr != nil {
				return openErr
			}
			stored.Continuation = nil
			sealed, sealErr := c.Engine.Seal(mustJSON(stored), aiPolicyAAD("request", item.id))
			if sealErr != nil {
				return sealErr
			}
			status := item.status
			if status == "awaiting" || status == "approved" {
				status = "cancelled"
			}
			if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_requests SET payload=?,status=?,updated_at=? WHERE id=?`, sealed, status, aiPolicyNow(), item.id); err != nil {
				return err
			}
		}
		return nil
	})
	c.AddManagementRoute("POST /management/v1/ai/approvals", func(w http.ResponseWriter, r *http.Request) { aiPolicyApprove(c, w, r) })
	c.AddManagementRoute("DELETE /management/v1/ai/grants/{grant}", func(w http.ResponseWriter, r *http.Request) { aiPolicyRevoke(c, w, r) })
	c.AddManagementRoute("GET /management/v1/ai/requests/{request}", func(w http.ResponseWriter, r *http.Request) { aiPolicyRequest(c, w, r) })
	c.AddManagementRoute("GET /management/v1/ai/usage", func(w http.ResponseWriter, r *http.Request) { aiPolicyUsage(c, w, r) })
	return nil
}

func aiPolicyAAD(kind, id string) []byte { return []byte("ai-policy/" + kind + "/" + id) }

func aiPolicyNow() int64 { return time.Now().Unix() }

// aiPolicyCleanupExpired is intentionally bounded: an expired approval must
// release the chat queue promptly, while a large historical backlog cannot
// monopolize Core's single writer connection.
func aiPolicyCleanupExpired(ctx context.Context, c *core.Core) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := aiPolicyNow()
	rows, err := tx.QueryContext(ctx, `SELECT id,job_id,chat_id,payload,status FROM ai_policy_requests WHERE status IN('awaiting','approved') AND expires_at<=? ORDER BY expires_at,rowid LIMIT 256`, now)
	if err != nil {
		return err
	}
	type expiredRequest struct {
		id, job, chat, status string
		payload               []byte
	}
	items := make([]expiredRequest, 0, 256)
	for rows.Next() {
		var item expiredRequest
		if err = rows.Scan(&item.id, &item.job, &item.chat, &item.payload, &item.status); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	chats := map[string]bool{}
	for _, item := range items {
		stored, openErr := aiPolicyOpenStored(c, item.id, item.payload)
		if openErr != nil {
			return openErr
		}
		stored.Continuation = nil
		sealed, sealErr := c.Engine.Seal(mustJSON(stored), aiPolicyAAD("request", item.id))
		if sealErr != nil {
			return sealErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_requests SET payload=?,status='expired',updated_at=?,outcome='approval_expired' WHERE id=? AND status IN('awaiting','approved')`, sealed, now, item.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_jobs SET status='failed',updated_at=? WHERE id=? AND status IN('queued','awaiting_approval')`, now, item.job); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status='failed',updated_at=? WHERE id=? AND status IN('queued','awaiting_approval')`, now, item.job); err != nil {
			return err
		}
		chats[item.chat] = true
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for chat := range chats {
		c.Wake(chat)
	}
	return nil
}

func aiPolicyHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func aiPolicyDestinationHash(destination string) string {
	h := sha256.Sum256([]byte(destination))
	return hex.EncodeToString(h[:])
}

func aiPolicyEffectHash(effect aiPolicyEffect) string {
	if effect.RequestHash != "" {
		return effect.RequestHash
	}
	copy := effect
	copy.RequestHash = ""
	return aiPolicyHash(copy)
}

func aiPolicyCap(cfg config.Config, scope string) int64 {
	switch scope {
	case "global":
		return cfg.AIPolicy.GlobalDailyBudgetMicrounits
	case "bot":
		return cfg.AIPolicy.PerBotDailyBudgetMicrounits
	case "source_user":
		return cfg.AIPolicy.PerUserDailyBudgetMicrounits
	default:
		return 0
	}
}

func aiPolicyRequired(c *core.Core, effect aiPolicyEffect) bool {
	return effect.Required || (effect.Action == "provider" && c.Config.AIPolicy.RequireProviderApproval)
}

func aiPolicySessionVersion(ctx context.Context, tx *sql.Tx, job string) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT s.version FROM ai_sessions s JOIN ai_tasks t ON t.chat_id=s.chat_id AND t.agent_id=s.agent_id WHERE t.id=?`, job).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // legacy direct AI sessions have no named session version
	}
	return version, err
}

func aiPolicyBegin(ctx context.Context, c *core.Core, effect aiPolicyEffect, continuationRaw []byte) (string, error) {
	if c == nil || c.DB == nil || !c.Config.Features.AIPolicy {
		return "", errors.New("AI policy unavailable")
	}
	if effect.ID == "" {
		effect.ID = uuid.NewString()
	}
	if effect.ReserveMicrounits < 0 {
		return "", errors.New("negative AI policy reserve")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	task, err := aiTaskLookup(ctx, tx, effect.Job)
	if err != nil || task.Status != "running" || !aiTaskAuthorized(ctx, tx, task) {
		if err == nil {
			err = errors.New("AI job is not authorized")
		}
		return "", err
	}
	currentSessionVersion, err := aiPolicySessionVersion(ctx, tx, effect.Job)
	if err != nil {
		return "", err
	}
	if effect.Epoch != task.Epoch {
		return "", errors.New("AI policy epoch mismatch")
	}
	if effect.SessionVersion != currentSessionVersion {
		return "", errors.New("AI policy session version mismatch")
	}
	now := aiPolicyNow()
	hash := aiPolicyEffectHash(effect)
	destinationHash := aiPolicyDestinationHash(effect.Destination)
	var status, previousHash string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT status,request_hash,payload FROM ai_policy_requests WHERE id=? AND job_id=?`, effect.ID, effect.Job).Scan(&status, &previousHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		stored := aiPolicyStored{Effect: effect, Continuation: append([]byte(nil), continuationRaw...), SessionEpoch: task.Epoch, SessionVersion: currentSessionVersion}
		payload, err = c.Engine.Seal(mustJSON(stored), aiPolicyAAD("request", effect.ID))
		if err != nil {
			return "", err
		}
		expires := now + int64(maxInt(c.Config.AIPolicy.ApprovalTTLSeconds, 1))
		_, err = tx.ExecContext(ctx, `INSERT INTO ai_policy_requests(id,job_id,chat_id,source_user,source_device,ai_user,ai_device,epoch,request_hash,action,name,destination_hash,payload,status,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'awaiting',?,?,?)`, effect.ID, effect.Job, task.Chat, task.SourceUser, task.SourceDevice, task.User, task.Device, task.Epoch, hash, effect.Action, effect.Name, destinationHash, payload, expires, now, now)
		if err != nil {
			return "", err
		}
		if aiPolicyRequired(c, effect) {
			if err = aiPolicyMarkAwaiting(ctx, tx, c, task.Chat, effect.Job, effect.ID, now); err != nil {
				return "", err
			}
			if err = tx.Commit(); err != nil {
				return "", err
			}
			c.Wake(task.Chat)
			return effect.ID, ErrAIApprovalPending
		}
		status = "approved"
		if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_requests SET status='approved',updated_at=? WHERE id=?`, now, effect.ID); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if subtle.ConstantTimeCompare([]byte(previousHash), []byte(hash)) != 1 {
		return "", errors.New("AI policy request hash mismatch")
	}
	if status == "awaiting" {
		return "", ErrAIApprovalPending
	}
	if status == "dispatched" || status == "uncertain" || status == "completed" {
		return "", errors.New("AI policy effect already dispatched; replay prohibited")
	}
	if status != "approved" {
		return "", errors.New("AI policy request is not dispatchable")
	}
	if err = aiPolicyDispatchTx(ctx, tx, c, effect.ID, task, effect, payload, now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	c.Wake(task.Chat)
	return effect.ID, nil
}

func aiPolicyMarkAwaiting(ctx context.Context, tx *sql.Tx, c *core.Core, chat, job, request string, now int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE ai_jobs SET status='awaiting_approval',updated_at=? WHERE id=? AND status='running'`, now, job); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ai_tasks SET status='awaiting_approval',updated_at=? WHERE id=? AND status='running'`, now, job); err != nil {
		return err
	}
	return aiPolicyEvent(ctx, tx, c, chat, "ai.approval.required", map[string]any{"job": job, "request_id": request, "action": "approval_required"})
}

func aiPolicyEvent(ctx context.Context, tx *sql.Tx, c *core.Core, chat, typ string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return func() error {
		_, err := c.Append(ctx, tx, chat, typ, "", json.RawMessage(b))
		return err
	}()
}

func aiPolicyDispatchTx(ctx context.Context, tx *sql.Tx, c *core.Core, id string, task aiTaskIdentity, effect aiPolicyEffect, payload []byte, now int64) error {
	if task.Status != "running" || !aiTaskAuthorized(ctx, tx, task) {
		return errors.New("AI job authorization changed")
	}
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM ai_policy_requests WHERE id=?`, id).Scan(&expires); err != nil {
		return err
	}
	if expires <= now {
		return errors.New("AI policy request expired")
	}
	if aiPolicyRequired(c, effect) {
		var grantID, tokenBlob, nonce string
		var used, revoked int64
		err := tx.QueryRowContext(ctx, `SELECT id,token,nonce,used_at,revoked_at FROM ai_policy_grants WHERE request_id=? ORDER BY issued_at DESC LIMIT 1`, id).Scan(&grantID, &tokenBlob, &nonce, &used, &revoked)
		if err != nil {
			return ErrAIApprovalPending
		}
		if used != 0 || revoked != 0 {
			return errors.New("AI grant already consumed or revoked")
		}
		var revokedByNonce bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_policy_revocations WHERE nonce=?)`, nonce).Scan(&revokedByNonce); err != nil {
			return err
		}
		if revokedByNonce {
			return errors.New("AI grant revoked")
		}
		plain, err := c.Engine.Open([]byte(tokenBlob), aiPolicyAAD("grant", grantID))
		if err != nil {
			return errors.New("AI grant unavailable")
		}
		if err = aiPolicyVerifyToken(c, string(plain), id, task, effect, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_grants SET used_at=? WHERE id=? AND used_at=0 AND revoked_at=0`, now, grantID); err != nil {
			return err
		}
	}
	if err := aiPolicyReserveTx(ctx, tx, c, id, task, effect, payload, now); err != nil {
		return err
	}
	origin := aiPolicyOrigin(effect.Destination)
	if err := aiPolicyEvent(ctx, tx, c, task.Chat, "ai.egress.notice", map[string]any{"destination_origin": origin, "action": effect.Action, "name": effect.Name, "job": effect.Job, "request_id": id, "confidentiality": "external_plaintext"}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE ai_policy_requests SET status='dispatched',dispatched_at=?,updated_at=? WHERE id=? AND status='approved'`, now, now, id)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_jobs SET status='running',updated_at=? WHERE id=? AND status='awaiting_approval'`, now, effect.Job); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status='running',updated_at=? WHERE id=? AND status='awaiting_approval'`, now, effect.Job); err != nil {
		return err
	}
	return nil
}

func aiPolicyReserveTx(ctx context.Context, tx *sql.Tx, c *core.Core, request string, task aiTaskIdentity, effect aiPolicyEffect, sealed []byte, now int64) error {
	day := time.Unix(now, 0).UTC().Format("2006-01-02")
	reserve := effect.ReserveMicrounits
	if reserve == 0 && effect.Action != "tool" {
		reserve = c.Config.AIPolicy.ProviderReserveMicrounits
	}
	if reserve < 0 {
		return errors.New("negative AI reserve")
	}
	for _, scope := range []struct{ name, id string }{{"global", "global"}, {"bot", task.User}, {"source_user", task.SourceUser}} {
		cap := aiPolicyCap(c.Config, scope.name)
		var account aiPolicyAmounts
		var sealedAccount []byte
		accountErr := tx.QueryRowContext(ctx, `SELECT payload FROM ai_policy_accounts WHERE scope=? AND scope_id=? AND day=?`, scope.name, scope.id, day).Scan(&sealedAccount)
		if accountErr == nil {
			plain, openErr := c.Engine.Open(sealedAccount, aiPolicyAccountAAD(scope.name, scope.id, day))
			if openErr != nil || json.Unmarshal(plain, &account) != nil || account.Used < 0 || account.Reserved < 0 {
				return errors.New("AI account unavailable")
			}
		} else if !errors.Is(accountErr, sql.ErrNoRows) {
			return accountErr
		}
		if cap > 0 {
			if account.Used > math.MaxInt64-account.Reserved || account.Used+account.Reserved > math.MaxInt64-reserve || account.Used+account.Reserved+reserve > cap {
				return errors.New("AI daily budget exceeded")
			}
		}
		sealedEntry, err := c.Engine.Seal(mustJSON(map[string]any{"effect": effect, "scope": scope.name, "scope_id": scope.id, "day": day}), []byte("ai-policy/ledger/"+request+"/"+scope.name))
		if err != nil {
			return err
		}
		// Amounts are sealed separately because budget counters and usage are
		// account metadata, even though scope/day are indexed for admission.
		amounts, sealErr := c.Engine.Seal(mustJSON(aiPolicyAmounts{Reserved: reserve, Currency: c.Config.AIPolicy.Currency}), []byte("ai-policy/amounts/"+request+"/"+scope.name))
		if sealErr != nil {
			return sealErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ai_policy_ledger(request_id,job_id,scope,scope_id,day,reserved,cost,amounts,payload,status,created_at,updated_at) VALUES(?,?,?,?,?,0,0,?,?, 'reserved',?,?)`, request, effect.Job, scope.name, scope.id, day, amounts, sealedEntry, now, now); err != nil {
			return err
		}
		if account.Reserved > math.MaxInt64-reserve {
			return errors.New("AI budget counter overflow")
		}
		account.Reserved += reserve
		sealedAccount, err = c.Engine.Seal(mustJSON(account), aiPolicyAccountAAD(scope.name, scope.id, day))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO ai_policy_accounts(scope,scope_id,day,payload) VALUES(?,?,?,?) ON CONFLICT(scope,scope_id,day) DO UPDATE SET payload=excluded.payload`, scope.name, scope.id, day, sealedAccount); err != nil {
			return err
		}
	}
	return nil
}

func aiPolicyOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "invalid"
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func aiPolicySettle(ctx context.Context, c *core.Core, id string, usage aiUsage, outcome string) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, job string
	var payload []byte
	if err = tx.QueryRowContext(ctx, `SELECT status,job_id,payload FROM ai_policy_requests WHERE id=?`, id).Scan(&status, &job, &payload); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if status != "dispatched" && status != "uncertain" {
		return errors.New("AI policy request is not settled")
	}
	stored, err := aiPolicyOpenStored(c, id, payload)
	if err != nil {
		return err
	}
	known := usage.Known && usage.InputTokens >= 0 && usage.OutputTokens >= 0
	if stored.Effect.Action == "tool" {
		known = true // tool success has a frozen fixed cost; no token usage is needed
	}
	actual := stored.Effect.ReserveMicrounits
	if actual == 0 && stored.Effect.Action != "tool" {
		actual = c.Config.AIPolicy.ProviderReserveMicrounits
	}
	if known {
		actual, err = aiPolicyCost(stored.Effect, usage)
		if err != nil {
			return err
		}
	}
	newStatus := "uncertain"
	if known && (strings.EqualFold(outcome, "success") || strings.EqualFold(outcome, "succeeded")) {
		newStatus = "completed"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,request_id,scope,scope_id,day,amounts FROM ai_policy_ledger WHERE request_id=?`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ledgerID int64
		var ledgerRequest, scope, scopeID, day string
		var sealedAmounts []byte
		if err = rows.Scan(&ledgerID, &ledgerRequest, &scope, &scopeID, &day, &sealedAmounts); err != nil {
			rows.Close()
			return err
		}
		plainAmounts, openErr := c.Engine.Open(sealedAmounts, []byte("ai-policy/amounts/"+ledgerRequest+"/"+scope))
		if openErr != nil {
			rows.Close()
			return errors.New("AI ledger amounts unavailable")
		}
		var oldAmounts aiPolicyAmounts
		if json.Unmarshal(plainAmounts, &oldAmounts) != nil {
			rows.Close()
			return errors.New("invalid AI ledger amounts")
		}
		cost := int64(0)
		reservedValue := oldAmounts.Reserved
		if newStatus == "completed" {
			cost = actual
			reservedValue = 0
		}
		sealedUpdated, sealErr := c.Engine.Seal(mustJSON(aiPolicyAmounts{Reserved: reservedValue, Cost: cost, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, Known: known, Currency: c.Config.AIPolicy.Currency}), []byte("ai-policy/amounts/"+ledgerRequest+"/"+scope))
		if sealErr != nil {
			rows.Close()
			return sealErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_ledger SET reserved=0,cost=0,amounts=?,status=?,updated_at=? WHERE id=?`, sealedUpdated, newStatus, aiPolicyNow(), ledgerID); err != nil {
			rows.Close()
			return err
		}
		if newStatus == "completed" {
			var sealedAccount []byte
			if err = tx.QueryRowContext(ctx, `SELECT payload FROM ai_policy_accounts WHERE scope=? AND scope_id=? AND day=?`, scope, scopeID, day).Scan(&sealedAccount); err != nil {
				rows.Close()
				return err
			}
			plainAccount, openErr := c.Engine.Open(sealedAccount, aiPolicyAccountAAD(scope, scopeID, day))
			if openErr != nil {
				rows.Close()
				return errors.New("AI account unavailable")
			}
			var account aiPolicyAmounts
			if json.Unmarshal(plainAccount, &account) != nil || account.Reserved < oldAmounts.Reserved || account.Used > math.MaxInt64-cost {
				rows.Close()
				return errors.New("invalid AI account amounts")
			}
			account.Reserved -= oldAmounts.Reserved
			account.Used += cost
			sealedAccount, sealErr = c.Engine.Seal(mustJSON(account), aiPolicyAccountAAD(scope, scopeID, day))
			if sealErr != nil {
				rows.Close()
				return sealErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE ai_policy_accounts SET payload=? WHERE scope=? AND scope_id=? AND day=?`, sealedAccount, scope, scopeID, day); err != nil {
				rows.Close()
				return err
			}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	_, err = tx.ExecContext(ctx, `UPDATE ai_policy_requests SET status=?,outcome=?,updated_at=? WHERE id=?`, newStatus, outcome, aiPolicyNow(), id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func aiPolicyCost(effect aiPolicyEffect, usage aiUsage) (int64, error) {
	if effect.Action == "tool" {
		return effect.ReserveMicrounits, nil
	}
	in, err := aiPolicyCeilRate(usage.InputTokens, effect.InputPriceMicrounitsPerMillionTokens)
	if err != nil {
		return 0, err
	}
	out, err := aiPolicyCeilRate(usage.OutputTokens, effect.OutputPriceMicrounitsPerMillionTokens)
	if err != nil || in > math.MaxInt64-out {
		return 0, errors.New("AI usage cost overflow")
	}
	return in + out, err
}

func aiPolicyCeilRate(tokens, rate int64) (int64, error) {
	if tokens < 0 || rate < 0 || tokens > math.MaxInt64/2 || rate > math.MaxInt64/2 {
		return 0, errors.New("AI usage cost overflow")
	}
	if tokens == 0 || rate == 0 {
		return 0, nil
	}
	if tokens > (math.MaxInt64-999999)/rate {
		return 0, errors.New("AI usage cost overflow")
	}
	return (tokens*rate + 999999) / 1000000, nil
}

func aiPolicyOpenStored(c *core.Core, id string, blob []byte) (aiPolicyStored, error) {
	plain, err := c.Engine.Open(blob, aiPolicyAAD("request", id))
	if err != nil {
		return aiPolicyStored{}, err
	}
	var stored aiPolicyStored
	err = json.Unmarshal(plain, &stored)
	return stored, err
}

func aiPolicySaved(ctx context.Context, tx *sql.Tx, c *core.Core, job string) ([]byte, error) {
	if c == nil || c.Engine == nil {
		return nil, errors.New("AI policy storage key unavailable")
	}
	var id string
	var blob []byte
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT id,payload,status FROM ai_policy_requests WHERE job_id=? AND status='approved' ORDER BY rowid DESC LIMIT 1`, job).Scan(&id, &blob, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			var count int
			if countErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM ai_policy_requests WHERE job_id=?`, job).Scan(&count); countErr != nil {
				return nil, countErr
			}
			if count == 0 {
				return nil, nil
			}
			return nil, errors.New("AI continuation request is not approved")
		}
		return nil, err
	}
	if status != "approved" {
		return nil, errors.New("AI continuation is not approved")
	}
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil || (task.Status != "queued" && task.Status != "running") || !aiTaskAuthorized(ctx, tx, task) {
		return nil, errors.New("AI continuation job is not authorized")
	}
	version, err := aiPolicySessionVersion(ctx, tx, job)
	if err != nil {
		return nil, err
	}
	stored, err := aiPolicyOpenStored(c, id, blob)
	if err != nil || stored.SessionEpoch != task.Epoch || stored.SessionVersion != version || stored.Effect.Job != job || stored.Effect.Epoch != task.Epoch || stored.Effect.SessionVersion != version {
		return nil, errors.New("AI continuation binding mismatch")
	}
	return append([]byte(nil), stored.Continuation...), nil
}

func aiPolicyResume(ctx context.Context, c *core.Core, job string) ([]byte, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id string
	var blob []byte
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT id,payload,status FROM ai_policy_requests WHERE job_id=? AND status='approved' ORDER BY rowid DESC LIMIT 1`, job).Scan(&id, &blob, &status); err != nil {
		return nil, err
	}
	if status != "approved" {
		return nil, errors.New("AI continuation is not approved")
	}
	task, err := aiTaskLookup(ctx, tx, job)
	if err != nil || task.Status != "running" || !aiTaskAuthorized(ctx, tx, task) {
		return nil, errors.New("AI job is not authorized")
	}
	version, versionErr := aiPolicySessionVersion(ctx, tx, job)
	if versionErr != nil {
		return nil, versionErr
	}
	stored, err := aiPolicyOpenStored(c, id, blob)
	if err != nil || stored.SessionEpoch != task.Epoch || stored.SessionVersion != version || stored.Effect.Job != job || stored.Effect.Epoch != task.Epoch || stored.Effect.SessionVersion != version {
		return nil, errors.New("AI continuation binding mismatch")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return append([]byte(nil), stored.Continuation...), nil
}

func aiPolicyApprove(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !core.Decode(w, r, &in, 32768) || strings.TrimSpace(in.Token) == "" {
		return
	}
	claims, err := aiPolicyParseToken(c, in.Token)
	if err != nil {
		core.Error(w, 400, "invalid AI approval")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var job, chat, sourceUser, sourceDevice, aiUser, aiDevice, hash, action, name, destinationHash, status string
	var sealedPayload []byte
	var epoch, expires int64
	if err = tx.QueryRowContext(r.Context(), `SELECT job_id,chat_id,source_user,source_device,ai_user,ai_device,epoch,request_hash,action,name,destination_hash,status,payload,expires_at FROM ai_policy_requests WHERE id=?`, claims.RequestID).Scan(&job, &chat, &sourceUser, &sourceDevice, &aiUser, &aiDevice, &epoch, &hash, &action, &name, &destinationHash, &status, &sealedPayload, &expires); err != nil {
		core.Error(w, 404, "AI request not found")
		return
	}
	tokenHash := aiPolicyHash(in.Token)
	if status != "awaiting" || expires <= aiPolicyNow() {
		var existing string
		if err = tx.QueryRowContext(r.Context(), `SELECT id FROM ai_policy_grants WHERE request_id=? AND token_hash=?`, claims.RequestID, tokenHash).Scan(&existing); err == nil {
			if err = tx.Commit(); err != nil {
				core.Error(w, 503, "storage unavailable")
				return
			}
			core.JSON(w, 200, map[string]any{"grant": existing, "status": "approved", "idempotent": true})
			return
		}
		core.Error(w, 409, "AI request is no longer awaiting approval")
		return
	}
	task, err := aiTaskLookup(r.Context(), tx, job)
	if err != nil || task.Status != "awaiting_approval" {
		core.Error(w, 409, "AI job is not awaiting approval")
		return
	}
	check := task
	check.Status = "running"
	if check.Chat != chat || check.User != aiUser || check.Device != aiDevice || check.SourceUser != sourceUser || check.SourceDevice != sourceDevice || check.Epoch != epoch || !aiTaskAuthorized(r.Context(), tx, check) {
		core.Error(w, 403, "AI approval source is no longer authorized")
		return
	}
	stored, openErr := aiPolicyOpenStored(c, claims.RequestID, sealedPayload)
	if openErr != nil || stored.Effect.Job != job || stored.Effect.Action != action || stored.Effect.Name != name || stored.Effect.RequestHash != hash || aiPolicyDestinationHash(stored.Effect.Destination) != destinationHash {
		core.Error(w, 400, "AI request payload unavailable")
		return
	}
	if err = aiPolicyVerifyToken(c, in.Token, claims.RequestID, check, stored.Effect, aiPolicyNow()); err != nil || claims.DestinationHash != destinationHash {
		core.Error(w, 400, "AI approval claims do not match request")
		return
	}
	var existing string
	if err = tx.QueryRowContext(r.Context(), `SELECT id FROM ai_policy_grants WHERE request_id=? AND token_hash=?`, claims.RequestID, tokenHash).Scan(&existing); err == nil {
		if err = tx.Commit(); err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		core.JSON(w, 200, map[string]any{"grant": existing, "status": "approved", "idempotent": true})
		return
	}
	var nonceRevoked bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM ai_policy_revocations WHERE nonce=?)`, claims.JTI).Scan(&nonceRevoked); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if nonceRevoked {
		core.Error(w, 409, "AI grant revoked")
		return
	}
	grantID := claims.JTI
	if grantID == "" {
		grantID = uuid.NewString()
	}
	sealed, err := c.Engine.Seal([]byte(in.Token), aiPolicyAAD("grant", grantID))
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	now := aiPolicyNow()
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO ai_policy_grants(id,request_id,nonce,token_hash,token,issued_at,expires_at) VALUES(?,?,?,?,?,?,?)`, grantID, claims.RequestID, claims.JTI, tokenHash, sealed, now, claims.ExpiresAt); err != nil {
		core.Error(w, 409, "AI approval already exists")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE ai_policy_requests SET status='approved',updated_at=? WHERE id=? AND status='awaiting'`, now, claims.RequestID); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE ai_jobs SET status='queued',updated_at=? WHERE id=? AND status='awaiting_approval'`, now, job); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE ai_tasks SET status='queued',updated_at=? WHERE id=? AND status='awaiting_approval'`, now, job); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	c.Wake(chat)
	core.JSON(w, 200, map[string]any{"grant": grantID, "status": "approved"})
}

func aiPolicyParseToken(c *core.Core, token string) (aiPolicyGrantClaims, error) {
	state, ok := aiPolicyKeys.Load(c)
	if !ok {
		return aiPolicyGrantClaims{}, errors.New("AI grant key unavailable")
	}
	key := state.(aiPolicyKeyState).key
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return aiPolicyGrantClaims{}, errors.New("invalid JWT")
	}
	var header map[string]json.RawMessage
	if err := strictJSON(parts[0], &header); err != nil || len(header) != 2 || string(header["alg"]) != `"EdDSA"` || string(header["typ"]) != `"JWT"` {
		return aiPolicyGrantClaims{}, errors.New("invalid JWT header")
	}
	var raw map[string]json.RawMessage
	if err := strictJSON(parts[1], &raw); err != nil {
		return aiPolicyGrantClaims{}, err
	}
	allowed := map[string]bool{"v": true, "jti": true, "request_id": true, "job_id": true, "chat_id": true, "source_user": true, "source_device": true, "ai_user": true, "ai_device": true, "epoch": true, "action": true, "name": true, "request_hash": true, "destination_hash": true, "iss": true, "aud": true, "iat": true, "exp": true}
	for k := range raw {
		if !allowed[k] {
			return aiPolicyGrantClaims{}, errors.New("unknown JWT claim")
		}
	}
	var claims aiPolicyGrantClaims
	if err := strictJSON(parts[1], &claims); err != nil {
		return aiPolicyGrantClaims{}, err
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(c.Config.AIPolicy.Issuer), jwt.WithAudience(c.Config.AIPolicy.Audience), jwt.WithLeeway(0))
	parsed, err := parser.Parse(token, func(*jwt.Token) (any, error) { return key, nil })
	if err != nil || parsed == nil || !parsed.Valid {
		return aiPolicyGrantClaims{}, errors.New("invalid JWT signature")
	}
	now := aiPolicyNow()
	maxTTL := int64(maxInt(c.Config.AIPolicy.ApprovalTTLSeconds, 1))
	if claims.V != 1 || !aiPolicyField(claims.JTI) || !aiPolicyField(claims.RequestID) || !aiPolicyField(claims.JobID) || !aiPolicyField(claims.ChatID) || !aiPolicyField(claims.SourceUser) || !aiPolicyField(claims.SourceDevice) || !aiPolicyField(claims.AIUser) || !aiPolicyField(claims.AIDevice) || !aiPolicyField(claims.Action) || !aiPolicyField(claims.Name) || !aiPolicyHashField(claims.RequestHash) || !aiPolicyHashField(claims.DestinationHash) || claims.Issuer != c.Config.AIPolicy.Issuer || claims.Audience != c.Config.AIPolicy.Audience || claims.IssuedAt <= 0 || claims.IssuedAt > now || claims.ExpiresAt <= now || claims.ExpiresAt <= claims.IssuedAt || claims.ExpiresAt-claims.IssuedAt > maxTTL {
		return aiPolicyGrantClaims{}, errors.New("invalid JWT claims")
	}
	return claims, nil
}

func aiPolicyField(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, ch := range value {
		if ch < 0x21 || ch > 0x7e || ch == '"' || ch == '\\' {
			return false
		}
	}
	return true
}

func aiPolicyHashField(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

func aiPolicyVerifyToken(c *core.Core, token, id string, task aiTaskIdentity, effect aiPolicyEffect, now int64) error {
	claims, err := aiPolicyParseToken(c, token)
	if err != nil {
		return err
	}
	if claims.RequestID != id || claims.JobID != effect.Job || claims.ChatID != task.Chat || claims.SourceUser != task.SourceUser || claims.SourceDevice != task.SourceDevice || claims.AIUser != task.User || claims.AIDevice != task.Device || claims.Epoch != task.Epoch || claims.Action != effect.Action || claims.Name != effect.Name || claims.RequestHash != aiPolicyEffectHash(effect) || claims.DestinationHash != aiPolicyDestinationHash(effect.Destination) || claims.ExpiresAt <= now {
		return errors.New("AI approval claims mismatch")
	}
	return nil
}

func aiPolicyRevoke(c *core.Core, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("grant")
	if id == "" {
		core.Error(w, 400, "invalid grant")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	now := aiPolicyNow()
	var nonce string
	if err = tx.QueryRowContext(r.Context(), `SELECT nonce FROM ai_policy_grants WHERE id=?`, id).Scan(&nonce); errors.Is(err, sql.ErrNoRows) {
		nonce = id
	} else if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO ai_policy_revocations(id,nonce,revoked_at) VALUES(?,?,?)`, id, nonce, now); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE ai_policy_grants SET revoked_at=? WHERE id=?`, now, id); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"grant": id, "revoked": true})
}

func aiPolicyRequest(c *core.Core, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("request")
	var job, chat, sourceUser, sourceDevice, aiUser, aiDevice, action, name, hash, destinationHash, status string
	var epoch int64
	var payload []byte
	var expires, created int64
	err := c.DB.QueryRowContext(r.Context(), `SELECT job_id,chat_id,source_user,source_device,ai_user,ai_device,epoch,action,name,request_hash,destination_hash,status,payload,expires_at,created_at FROM ai_policy_requests WHERE id=?`, id).Scan(&job, &chat, &sourceUser, &sourceDevice, &aiUser, &aiDevice, &epoch, &action, &name, &hash, &destinationHash, &status, &payload, &expires, &created)
	if errors.Is(err, sql.ErrNoRows) {
		core.Error(w, 404, "AI request not found")
		return
	}
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	stored, err := aiPolicyOpenStored(c, id, payload)
	if err != nil {
		core.Error(w, 503, "sealed AI request unavailable")
		return
	}
	var sessionVersion int64
	if stored.Effect.SessionVersion != 0 {
		sessionVersion = stored.Effect.SessionVersion
	}
	core.JSON(w, 200, map[string]any{"request_id": id, "job_id": job, "chat_id": chat, "source_user": sourceUser, "source_device": sourceDevice, "ai_user": aiUser, "ai_device": aiDevice, "epoch": epoch, "session_version": sessionVersion, "action": action, "name": name, "request_hash": hash, "destination_hash": destinationHash, "destination_origin": aiPolicyOrigin(stored.Effect.Destination), "required": stored.Effect.Required, "reserve_microunits": stored.Effect.ReserveMicrounits, "status": status, "expires_at": expires, "created_at": created})
}

func aiPolicyUsage(c *core.Core, w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 1 || n > 100 {
			core.Error(w, 400, "invalid limit")
			return
		}
		limit = n
	}
	args := []any{}
	where := []string{"1=1"}
	user, bot := r.URL.Query().Get("user"), r.URL.Query().Get("bot")
	if user != "" && bot != "" {
		core.Error(w, 400, "user and bot filters are mutually exclusive")
		return
	}
	if user != "" {
		where = append(where, "scope='source_user' AND scope_id=?")
		args = append(args, user)
	}
	if bot != "" {
		where = append(where, "scope=? AND scope_id=?")
		args = append(args, "bot", bot)
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		where = append(where, "id<?")
		args = append(args, cursor)
	}
	args = append(args, limit+1)
	rows, err := c.DB.QueryContext(r.Context(), `SELECT id,request_id,job_id,scope,scope_id,day,amounts,status,created_at FROM ai_policy_ledger WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var request, job, scope, scopeID, day, status string
		var created int64
		var sealedAmounts []byte
		if err = rows.Scan(&id, &request, &job, &scope, &scopeID, &day, &sealedAmounts, &status, &created); err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		plainAmounts, openErr := c.Engine.Open(sealedAmounts, []byte("ai-policy/amounts/"+request+"/"+scope))
		if openErr != nil {
			core.Error(w, 503, "sealed AI usage unavailable")
			return
		}
		var amounts aiPolicyAmounts
		if json.Unmarshal(plainAmounts, &amounts) != nil {
			core.Error(w, 503, "invalid AI usage")
			return
		}
		if len(items) < limit {
			items = append(items, map[string]any{"id": id, "request_id": request, "job_id": job, "scope": scope, "scope_id": scopeID, "day": day, "reserved_microunits": amounts.Reserved, "cost_microunits": amounts.Cost, "input_tokens": amounts.InputTokens, "output_tokens": amounts.OutputTokens, "cache_read_tokens": amounts.CacheReadTokens, "cache_write_tokens": amounts.CacheWriteTokens, "usage_known": amounts.Known, "currency": amounts.Currency, "invocations": 1, "status": status, "created_at": created})
		}
	}
	if err = rows.Err(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	result := map[string]any{"items": items}
	if len(items) == limit {
		result["next_cursor"] = fmt.Sprint(items[len(items)-1]["id"])
	}
	core.JSON(w, 200, result)
}

func strictJSON(encoded string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	if err = rejectDuplicateJSONKeys(b); err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("invalid JSON object key")
		}
		if seen[name] {
			return errors.New("duplicate JSON claim")
		}
		seen[name] = true
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return err
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func maxInt(v, fallback int) int {
	if v > fallback {
		return v
	}
	return fallback
}
