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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

// aiNamedTask is the durable unit processed by the named-agent worker. The
// task and session are intentionally separate from the legacy direct-chat
// tables: a message can target several independent agents in one chat.
type aiNamedTask struct {
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	ResultID  string `json:"result_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type aiNamedAgent struct {
	ID, BotName, UserID, DeviceID, Provider string
	Profile, Tools                          []byte
	Version                                 int64
}

type aiNamedSession struct {
	ChatID, AgentID string
	Context, Tools  []byte
	Version         int64
}

// installAINamed installs the phase-1 named-agent storage and routes. It is
// deliberately called by the provider installer after the legacy AI tables
// exist; this keeps provider-free builds and the legacy E2EE path unchanged.
func installAINamed(c *core.Core) error {
	schemaCtx := c.Context
	if schemaCtx == nil || schemaCtx.Err() != nil {
		schemaCtx = context.Background()
	}
	tx, err := c.DB.BeginTx(schemaCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{`
CREATE TABLE IF NOT EXISTS ai_named_schema_versions(version INTEGER PRIMARY KEY);
`, `
CREATE TABLE IF NOT EXISTS ai_agents(
 id TEXT PRIMARY KEY,
 bot_name TEXT NOT NULL UNIQUE,
 user_id TEXT NOT NULL UNIQUE REFERENCES users(id),
 device_id TEXT NOT NULL UNIQUE REFERENCES devices(id),
 provider TEXT NOT NULL,
 profile BLOB NOT NULL,
 tools BLOB NOT NULL DEFAULT '[]',
 version INTEGER NOT NULL DEFAULT 1,
 private_key BLOB NOT NULL,
 signing_key BLOB NOT NULL,
 disabled INTEGER NOT NULL DEFAULT 0,
 revoked INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ai_sessions(
 chat_id TEXT NOT NULL REFERENCES chats(id),
 agent_id TEXT NOT NULL REFERENCES ai_agents(id),
 context BLOB NOT NULL,
 tools BLOB NOT NULL DEFAULT '[]',
 version INTEGER NOT NULL DEFAULT 1,
 state BLOB NOT NULL DEFAULT X'',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(chat_id,agent_id)
);
CREATE TABLE IF NOT EXISTS ai_tasks(
 id TEXT PRIMARY KEY,
 chat_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES messages(id),
 status TEXT NOT NULL,
 result_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(message_id,agent_id),
 FOREIGN KEY(chat_id,agent_id) REFERENCES ai_sessions(chat_id,agent_id)
);
CREATE INDEX IF NOT EXISTS ai_tasks_queue ON ai_tasks(status,created_at);
CREATE INDEX IF NOT EXISTS ai_tasks_session ON ai_tasks(chat_id,agent_id,status,created_at);
`}
	for _, statement := range statements {
		if _, err = tx.ExecContext(schemaCtx, statement); err != nil {
			return err
		}
	}
	var version int
	if err = tx.QueryRowContext(schemaCtx, `SELECT COALESCE(MAX(version),0) FROM ai_named_schema_versions`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("unsupported named AI schema version")
	}
	if version < 1 {
		if _, err = tx.ExecContext(schemaCtx, `INSERT INTO ai_named_schema_versions(version) VALUES(1)`); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// An external provider call has an unknown outcome after process restart;
	// named tasks follow the same fail-closed rule as legacy jobs.
	if _, err = c.DB.Exec(`UPDATE ai_tasks SET status='uncertain',updated_at=? WHERE status='running'`, time.Now().Unix()); err != nil {
		return err
	}

	c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
		var chat string
		if err := tx.QueryRowContext(ctx, `SELECT chat_id FROM messages WHERE id=?`, message).Scan(&chat); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		// Deleting one source message invalidates every derived named context in
		// the chat. Do this in the message deletion transaction so no worker can
		// publish from stale context after the delete commits.
		rows, err := tx.QueryContext(ctx, `SELECT agent_id,version FROM ai_sessions WHERE chat_id=?`, chat)
		if err != nil {
			return err
		}
		type sessionVersion struct {
			agent   string
			version int64
		}
		var sessions []sessionVersion
		for rows.Next() {
			var session sessionVersion
			if err = rows.Scan(&session.agent, &session.version); err != nil {
				_ = rows.Close()
				return err
			}
			sessions = append(sessions, session)
		}
		if err = rows.Close(); err != nil {
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		for _, session := range sessions {
			newVersion := session.version + 1
			empty, sealErr := c.Engine.Seal([]byte("[]"), []byte("ai/context/"+chat+"/"+session.agent+"/"+formatAIInteger(newVersion)))
			if sealErr != nil {
				return sealErr
			}
			if _, err = tx.ExecContext(ctx, `UPDATE ai_sessions SET context=?,version=?,updated_at=? WHERE chat_id=? AND agent_id=?`, empty, newVersion, time.Now().Unix(), chat, session.agent); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status='cancelled',updated_at=? WHERE chat_id=? AND status IN ('queued','running')`, time.Now().Unix(), chat)
		return err
	})

	c.AddManagementRoute("POST /management/v1/ai/agents", func(w http.ResponseWriter, r *http.Request) { aiCreateNamedAgent(c, w, r) })
	c.AddManagementRoute("POST /management/v1/ai/agents/{agent}/chats/{chat}", func(w http.ResponseWriter, r *http.Request) { aiAttachNamedAgent(c, w, r) })
	c.AddRoute("POST /v1/chats/{chat}/ai/tasks", func(w http.ResponseWriter, r *http.Request, id core.Identity) { aiQueueNamedTasks(c, w, r, id) })
	c.AddRoute("GET /v1/chats/{chat}/ai/tasks", func(w http.ResponseWriter, r *http.Request, id core.Identity) { aiListNamedTasks(c, w, r, id) })
	c.AddRoute("GET /v1/chats/{chat}/ai/tasks/{task}", func(w http.ResponseWriter, r *http.Request, id core.Identity) { aiGetNamedTask(c, w, r, id) })
	c.AddRoute("DELETE /v1/chats/{chat}/ai/tasks/{task}", func(w http.ResponseWriter, r *http.Request, id core.Identity) { aiCancelNamedTask(c, w, r, id) })
	return nil
}

type aiNamedAgentInput struct {
	BotName string   `json:"bot_name"`
	UserID  string   `json:"user_id"`
	Tools   []string `json:"tools"`
}

func aiCreateNamedAgent(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in aiNamedAgentInput
	if !core.Decode(w, r, &in, 131072) {
		return
	}
	if !validAINamedID(in.BotName) {
		core.Error(w, 400, "valid bot_name required")
		return
	}
	settings, provider, err := c.Config.ResolveBot(in.BotName)
	if err != nil || aiProviders[provider] == nil {
		core.Error(w, 400, "AI bot is not configured")
		return
	}
	if in.Tools == nil {
		for _, tool := range settings.Tools {
			in.Tools = append(in.Tools, tool.Name)
		}
	}
	if _, err := aiNamedAllowedForSettings(c, settings, in.Tools); err != nil {
		core.Error(w, 400, "invalid tool allowlist")
		return
	}
	if in.UserID != "" && !validAINamedID(in.UserID) {
		core.Error(w, 400, "invalid user_id")
		return
	}
	profile, err := json.Marshal(settings)
	if len(profile) > 65536 || err != nil {
		core.Error(w, 500, "AI profile serialization failed")
		return
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		core.Error(w, 500, "AI identity generation failed")
		return
	}
	public, signing, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		core.Error(w, 500, "AI identity generation failed")
		return
	}
	agentID := "ai-" + uuid.NewString()
	if in.UserID != "" {
		agentID = in.UserID
	}
	deviceID := "ai-device-" + uuid.NewString()
	sealedPrivate, err := c.Engine.Seal(private.Bytes(), []byte("ai/agent/"+agentID+"/private"))
	if err != nil {
		core.Error(w, 500, "AI identity storage failed")
		return
	}
	sealedSigning, err := c.Engine.Seal(signing, []byte("ai/agent/"+agentID+"/signing"))
	if err != nil {
		core.Error(w, 500, "AI identity storage failed")
		return
	}
	sealedProfile, err := c.Engine.Seal(profile, []byte("ai/agent/"+agentID+"/profile/1"))
	if err != nil {
		core.Error(w, 500, "AI profile storage failed")
		return
	}
	tools, _ := json.Marshal(in.Tools)
	now := time.Now().Unix()
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id) VALUES(?)`, []any{agentID}},
		{`INSERT INTO devices(id,user_id,public_key,signing_key) VALUES(?,?,?,?)`, []any{deviceID, agentID, base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()), base64.StdEncoding.EncodeToString(public)}},
		{`INSERT INTO ai_agents(id,bot_name,user_id,device_id,provider,profile,tools,version,private_key,signing_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, []any{agentID, in.BotName, agentID, deviceID, provider, sealedProfile, tools, 1, sealedPrivate, sealedSigning, now, now}},
	} {
		if _, err = tx.ExecContext(r.Context(), stmt.query, stmt.args...); err != nil {
			core.Error(w, 409, "AI agent already exists or identity unavailable")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	core.JSON(w, 201, map[string]any{"agent_id": agentID, "bot_name": in.BotName, "user_id": agentID, "device_id": deviceID, "provider": provider, "version": 1, "tools": in.Tools})
}

type aiNamedAttachInput struct {
	Tools []string `json:"tools"`
}

func aiAttachNamedAgent(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in aiNamedAttachInput
	if !core.Decode(w, r, &in, 8192) {
		return
	}
	chat, agent := r.PathValue("chat"), r.PathValue("agent")
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var mode, kind, user, device string
	var agentTools []byte
	var seq int64
	if err = tx.QueryRowContext(r.Context(), `SELECT c.mode,c.kind,c.seq,a.user_id,a.device_id,a.tools FROM chats c JOIN ai_agents a ON a.id=? WHERE c.id=? AND a.disabled=0 AND a.revoked=0`, agent, chat).Scan(&mode, &kind, &seq, &user, &device, &agentTools); err != nil {
		core.Error(w, 404, "AI agent or chat not found")
		return
	}
	if mode != "basic" {
		core.Error(w, 400, "named AI requires a BASIC chat")
		return
	}
	if kind == "direct" {
		var count int
		if err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM members WHERE chat_id=? AND active=1`, chat).Scan(&count); err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		var already bool
		_ = tx.QueryRowContext(r.Context(), `SELECT active=1 FROM members WHERE chat_id=? AND user_id=?`, chat, user).Scan(&already)
		if count >= 2 && !already {
			core.Error(w, 400, "direct chat membership is full")
			return
		}
	}
	var valid bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u JOIN devices d ON d.user_id=u.id WHERE u.id=? AND d.id=? AND u.disabled=0 AND d.revoked=0)`, user, device).Scan(&valid); err != nil || !valid {
		core.Error(w, 409, "AI identity unavailable")
		return
	}
	if in.Tools == nil {
		if json.Unmarshal(agentTools, &in.Tools) != nil {
			core.Error(w, 503, "AI tool profile unavailable")
			return
		}
	}
	if _, err := aiNamedAllowedForNames(c, in.Tools, decodeAINames(agentTools)); err != nil {
		core.Error(w, 400, "invalid tool allowlist")
		return
	}
	tools, _ := json.Marshal(in.Tools)
	contextBlob, err := c.Engine.Seal([]byte("[]"), []byte("ai/context/"+chat+"/"+agent+"/1"))
	if err != nil {
		core.Error(w, 500, "AI context creation failed")
		return
	}
	now := time.Now().Unix()
	var joined int64 = seq + 1
	var previousActive bool
	var previousJoined int64
	if scanErr := tx.QueryRowContext(r.Context(), `SELECT active,joined_seq FROM members WHERE chat_id=? AND user_id=?`, chat, user).Scan(&previousActive, &previousJoined); scanErr == nil && previousActive {
		joined = previousJoined
	} else if c.Config.Policy.History == "all" {
		joined = 1
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES(?,?, 'member',1,?,1) ON CONFLICT(chat_id,user_id) DO UPDATE SET active=1,can_send=1,joined_seq=excluded.joined_seq`, chat, user, joined); err != nil {
		core.Error(w, 409, "AI membership unavailable")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO ai_sessions(chat_id,agent_id,context,tools,version,state,created_at,updated_at) VALUES(?,?,?, ?,1,X'',?,?) ON CONFLICT(chat_id,agent_id) DO UPDATE SET tools=excluded.tools,updated_at=excluded.updated_at`, chat, agent, contextBlob, tools, now, now)
	if err != nil {
		core.Error(w, 409, "AI agent already attached or session unavailable")
		return
	}
	var sessionVersion int64
	if err = tx.QueryRowContext(r.Context(), `SELECT version FROM ai_sessions WHERE chat_id=? AND agent_id=?`, chat, agent).Scan(&sessionVersion); err != nil {
		core.Error(w, 503, "AI session unavailable")
		return
	}
	if _, err = c.Append(r.Context(), tx, chat, "ai.agent.attached", "", map[string]any{"agent_id": agent}); err != nil || tx.Commit() != nil {
		core.Error(w, 503, "AI attachment persistence failed")
		return
	}
	c.Wake(chat)
	core.JSON(w, 201, map[string]any{"chat_id": chat, "agent_id": agent, "user_id": user, "device_id": device, "version": sessionVersion, "tools": in.Tools})
}

type aiNamedTaskInput struct {
	MessageID string   `json:"message_id"`
	Agents    []string `json:"agents"`
}

func aiQueueNamedTasks(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	var in aiNamedTaskInput
	if !core.Decode(w, r, &in, 16384) {
		return
	}
	chat := r.PathValue("chat")
	if !validAINamedID(in.MessageID) || len(in.Agents) == 0 || len(in.Agents) > 16 {
		core.Error(w, 400, "message_id and 1..16 agents required")
		return
	}
	seen := map[string]bool{}
	for _, agent := range in.Agents {
		if !validAINamedID(agent) || seen[agent] {
			core.Error(w, 400, "duplicate or invalid agent")
			return
		}
		seen[agent] = true
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var mode, sender, sourceDevice string
	var deleted bool
	if err = tx.QueryRowContext(r.Context(), `SELECT c.mode,m.sender,m.device_id,m.deleted FROM messages m JOIN chats c ON c.id=m.chat_id WHERE m.id=? AND m.chat_id=?`, in.MessageID, chat).Scan(&mode, &sender, &sourceDevice, &deleted); err != nil {
		core.Error(w, 404, "message not found")
		return
	}
	if mode != "basic" || deleted {
		core.Error(w, 400, "named AI tasks require an undeleted BASIC message")
		return
	}
	var authorized bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM members m JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.user_id=? AND d.id=? AND m.active=1 AND m.can_send=1 AND d.revoked=0 AND u.disabled=0 AND m.joined_seq<= (SELECT seq FROM messages WHERE id=?))`, chat, id.UserID, id.DeviceID, in.MessageID).Scan(&authorized); err != nil || !authorized || sender != id.UserID || sourceDevice != id.DeviceID {
		core.Error(w, 403, "message ownership required")
		return
	}
	for _, agent := range in.Agents {
		var valid bool
		if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM ai_sessions s JOIN ai_agents a ON a.id=s.agent_id JOIN members m ON m.chat_id=s.chat_id AND m.user_id=a.user_id JOIN users u ON u.id=a.user_id JOIN devices d ON d.id=a.device_id WHERE s.chat_id=? AND s.agent_id=? AND a.disabled=0 AND a.revoked=0 AND m.active=1 AND m.can_send=1 AND u.disabled=0 AND d.revoked=0)`, chat, agent).Scan(&valid); err != nil || !valid {
			core.Error(w, 403, "AI agent is not an active chat member")
			return
		}
	}
	now := time.Now().Unix()
	ids := make([]string, 0, len(in.Agents))
	insertedAgents := make([]string, 0, len(in.Agents))
	for _, agent := range in.Agents {
		var existing int
		if err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM ai_tasks WHERE chat_id=? AND agent_id=? AND status IN ('queued','running')`, chat, agent).Scan(&existing); err != nil {
			core.Error(w, 503, "AI task storage unavailable")
			return
		}
		taskID := uuid.NewString()
		result, insertErr := tx.ExecContext(r.Context(), `INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES(?,?,?,?, 'queued','',?,?) ON CONFLICT(message_id,agent_id) DO NOTHING`, taskID, chat, agent, in.MessageID, now, now)
		err = insertErr
		if err != nil {
			core.Error(w, 503, "AI task persistence failed")
			return
		}
		if n, _ := result.RowsAffected(); n == 1 {
			insertedAgents = append(insertedAgents, agent)
			if existing >= 64 {
				core.Error(w, 429, "AI session queue full")
				return
			}
			var globalExisting int
			if err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM ai_tasks WHERE status IN ('queued','running')`).Scan(&globalExisting); err != nil {
				core.Error(w, 503, "AI task storage unavailable")
				return
			}
			globalLimit := c.Config.Capacity.Workers * 64
			if globalLimit < 64 {
				globalLimit = 64
			}
			if globalExisting >= globalLimit {
				core.Error(w, 429, "AI task queue full")
				return
			}
		}
		var actual string
		if err = tx.QueryRowContext(r.Context(), `SELECT id FROM ai_tasks WHERE message_id=? AND agent_id=?`, in.MessageID, agent).Scan(&actual); err != nil {
			core.Error(w, 503, "AI task persistence failed")
			return
		}
		ids = append(ids, actual)
	}
	if len(insertedAgents) > 0 {
		if _, err = c.Append(r.Context(), tx, chat, "ai.tasks.queued", "", map[string]any{"source_message_id": in.MessageID, "agents": insertedAgents}); err != nil {
			core.Error(w, 503, "AI task persistence failed")
			return
		}
	}
	if tx.Commit() != nil {
		core.Error(w, 503, "AI task persistence failed")
		return
	}
	c.Wake(chat)
	core.JSON(w, 202, map[string]any{"chat_id": chat, "message_id": in.MessageID, "task_ids": ids})
}

func aiGetNamedTask(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	var task aiNamedTask
	err := c.DB.QueryRowContext(r.Context(), `SELECT t.id,t.chat_id,t.message_id,t.agent_id,t.status,t.result_id,t.created_at,t.updated_at FROM ai_tasks t JOIN messages src ON src.id=t.message_id AND src.chat_id=t.chat_id JOIN members m ON m.chat_id=t.chat_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN devices d ON d.id=? AND d.user_id=u.id WHERE t.id=? AND t.chat_id=? AND m.active=1 AND m.can_send=1 AND d.revoked=0 AND u.disabled=0 AND src.seq>=m.joined_seq`, id.UserID, id.DeviceID, r.PathValue("task"), r.PathValue("chat")).Scan(&task.ID, &task.ChatID, &task.MessageID, &task.AgentID, &task.Status, &task.ResultID, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		core.Error(w, 404, "AI task not found")
		return
	}
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	core.JSON(w, 200, task)
}

func aiListNamedTasks(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	rows, err := c.DB.QueryContext(r.Context(), `SELECT t.id,t.chat_id,t.message_id,t.agent_id,t.status,t.result_id,t.created_at,t.updated_at FROM ai_tasks t JOIN messages src ON src.id=t.message_id AND src.chat_id=t.chat_id JOIN members m ON m.chat_id=t.chat_id AND m.user_id=? JOIN users u ON u.id=m.user_id JOIN devices d ON d.id=? AND d.user_id=u.id WHERE t.chat_id=? AND m.active=1 AND m.can_send=1 AND d.revoked=0 AND u.disabled=0 AND src.seq>=m.joined_seq ORDER BY t.created_at DESC LIMIT 100`, id.UserID, id.DeviceID, r.PathValue("chat"))
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer rows.Close()
	tasks := []aiNamedTask{}
	for rows.Next() {
		var task aiNamedTask
		if err = rows.Scan(&task.ID, &task.ChatID, &task.MessageID, &task.AgentID, &task.Status, &task.ResultID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			core.Error(w, 503, "storage unavailable")
			return
		}
		tasks = append(tasks, task)
	}
	if err = rows.Err(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"tasks": tasks})
}

func aiCancelNamedTask(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var allowed bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1
FROM ai_tasks t
JOIN messages src ON src.id=t.message_id AND src.chat_id=t.chat_id
JOIN members m ON m.chat_id=t.chat_id AND m.user_id=?
JOIN users u ON u.id=m.user_id
JOIN devices d ON d.id=? AND d.user_id=u.id
WHERE t.id=? AND t.chat_id=? AND m.active=1 AND m.can_send=1 AND d.revoked=0 AND u.disabled=0
AND src.seq>=m.joined_seq AND (m.role IN ('owner','admin') OR src.sender=?))`, id.UserID, id.DeviceID, r.PathValue("task"), r.PathValue("chat"), id.UserID).Scan(&allowed); err != nil || !allowed {
		core.Error(w, 404, "AI task not found")
		return
	}
	var current string
	if err = tx.QueryRowContext(r.Context(), `SELECT status FROM ai_tasks WHERE id=?`, r.PathValue("task")).Scan(&current); err != nil {
		core.Error(w, 404, "AI task not found")
		return
	}
	if current != "queued" && current != "running" {
		core.JSON(w, 200, map[string]any{"task_id": r.PathValue("task"), "status": current})
		return
	}
	if err = aiNamedSetStatus(r.Context(), tx, r.PathValue("task"), "cancelled"); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	aiCancelActive(c, r.PathValue("task"))
	core.JSON(w, 200, map[string]any{"task_id": r.PathValue("task"), "status": "cancelled"})
}

// claimAINamedTask atomically reserves the oldest queued task for one
// session. SQLite's single writer serializes concurrent workers; the status
// predicate makes the helper safe for callers that use several goroutines.
func claimAINamedTask(ctx context.Context, c *core.Core) (aiNamedTask, bool, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return aiNamedTask{}, false, err
	}
	defer tx.Rollback()
	var task aiNamedTask
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.chat_id,t.message_id,t.agent_id,t.status,t.result_id,t.created_at,t.updated_at
FROM ai_tasks t
JOIN ai_sessions s ON s.chat_id=t.chat_id AND s.agent_id=t.agent_id
JOIN ai_agents a ON a.id=t.agent_id
JOIN chats c ON c.id=t.chat_id
JOIN messages src ON src.id=t.message_id AND src.chat_id=t.chat_id
JOIN members sm ON sm.chat_id=t.chat_id AND sm.user_id=src.sender
JOIN users su ON su.id=sm.user_id
JOIN devices sd ON sd.id=src.device_id AND sd.user_id=su.id
JOIN members am ON am.chat_id=t.chat_id AND am.user_id=a.user_id
JOIN users au ON au.id=a.user_id
JOIN devices ad ON ad.id=a.device_id AND ad.user_id=au.id
WHERE t.status='queued' AND c.mode='basic' AND c.pending=0 AND src.deleted=0
AND src.seq>=sm.joined_seq AND sm.active=1 AND sm.can_send=1 AND su.disabled=0 AND sd.revoked=0
AND a.disabled=0 AND a.revoked=0 AND am.active=1 AND am.can_send=1 AND au.disabled=0 AND ad.revoked=0
AND NOT EXISTS(SELECT 1 FROM ai_tasks active WHERE active.chat_id=t.chat_id AND active.agent_id=t.agent_id AND active.status='running')
ORDER BY t.created_at,t.id LIMIT 1`).Scan(&task.ID, &task.ChatID, &task.MessageID, &task.AgentID, &task.Status, &task.ResultID, &task.CreatedAt, &task.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return aiNamedTask{}, false, nil
	}
	if err != nil {
		return aiNamedTask{}, false, err
	}
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE ai_tasks SET status='running',updated_at=? WHERE id=? AND status='queued'`, now, task.ID)
	if err != nil {
		return aiNamedTask{}, false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return aiNamedTask{}, false, nil
	}
	if err = tx.Commit(); err != nil {
		return aiNamedTask{}, false, err
	}
	task.Status, task.UpdatedAt = "running", now
	return task, true, nil
}

func loadAINamedAgent(ctx context.Context, c *core.Core, agentID string) (aiNamedAgent, error) {
	var a aiNamedAgent
	err := c.DB.QueryRowContext(ctx, `SELECT id,bot_name,user_id,device_id,provider,profile,tools,version FROM ai_agents WHERE id=? AND disabled=0 AND revoked=0`, agentID).Scan(&a.ID, &a.BotName, &a.UserID, &a.DeviceID, &a.Provider, &a.Profile, &a.Tools, &a.Version)
	return a, err
}

func loadAINamedSession(ctx context.Context, c *core.Core, chat, agent string) (aiNamedSession, error) {
	var s aiNamedSession
	err := c.DB.QueryRowContext(ctx, `SELECT chat_id,agent_id,context,tools,version FROM ai_sessions WHERE chat_id=? AND agent_id=?`, chat, agent).Scan(&s.ChatID, &s.AgentID, &s.Context, &s.Tools, &s.Version)
	return s, err
}

func openAINamedProfile(c *core.Core, a aiNamedAgent) ([]byte, error) {
	return c.Engine.Open(a.Profile, []byte("ai/agent/"+a.ID+"/profile/"+formatAIInteger(a.Version)))
}

func aiNamedTools(c *core.Core, names []string) ([]config.Tool, error) {
	return aiAllowed(c, names)
}

// aiNamedToolsCurrent applies all three capability boundaries at execution:
// the session grant, the agent creation grant, and the currently resolved TOML
// bot catalog. A removed tool therefore fails closed before any provider call.
func aiNamedToolsCurrent(c *core.Core, settings config.AI, sessionNames, agentNames []string) ([]config.Tool, error) {
	session := map[string]bool{}
	for _, name := range sessionNames {
		session[name] = true
	}
	agent := map[string]bool{}
	for _, name := range agentNames {
		agent[name] = true
	}
	tools := make([]config.Tool, 0, len(sessionNames))
	seen := map[string]bool{}
	for _, tool := range settings.Tools {
		if !session[tool.Name] || !agent[tool.Name] || seen[tool.Name] || aiTools[tool.Kind] == nil {
			continue
		}
		seen[tool.Name] = true
		tools = append(tools, tool)
	}
	if len(seen) != len(session) {
		return nil, errors.New("tool permission revoked or unavailable")
	}
	return tools, nil
}

func decodeAINames(raw []byte) []string {
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		return nil
	}
	return names
}

func aiNamedAllowedForSettings(c *core.Core, settings config.AI, names []string) ([]config.Tool, error) {
	configured := make([]string, 0, len(settings.Tools))
	for _, tool := range settings.Tools {
		configured = append(configured, tool.Name)
	}
	return aiNamedAllowedForNames(c, names, configured)
}

func aiNamedAllowedForNames(c *core.Core, names, configured []string) ([]config.Tool, error) {
	allowed := map[string]bool{}
	for _, name := range configured {
		allowed[name] = true
	}
	for _, name := range names {
		if !allowed[name] {
			return nil, errors.New("tool not allowed for AI agent")
		}
	}
	return aiAllowed(c, names)
}

func validAINamedID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, ch := range value {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}

func formatAIInteger(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buf := [20]byte{}
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// aiNamedTaskIdentity snapshots the source and AI identities under the same
// transaction that claims/updates a task. The worker must recheck active ACL
// state before provider effects; this helper only supplies its immutable
// routing context.
func aiNamedTaskIdentity(ctx context.Context, tx *sql.Tx, job string) (aiTaskIdentity, error) {
	var out aiTaskIdentity
	err := tx.QueryRowContext(ctx, `SELECT t.chat_id,a.user_id,a.device_id,m.sender,m.device_id,m.id,ch.mode,ch.epoch,t.status,s.state FROM ai_tasks t JOIN ai_agents a ON a.id=t.agent_id JOIN ai_sessions s ON s.chat_id=t.chat_id AND s.agent_id=t.agent_id JOIN messages m ON m.id=t.message_id JOIN chats ch ON ch.id=t.chat_id WHERE t.id=?`, job).Scan(&out.Chat, &out.User, &out.Device, &out.SourceUser, &out.SourceDevice, &out.SourceMessage, &out.Mode, &out.Epoch, &out.Status, &out.State)
	return out, err
}

// aiNamedSetStatus is deliberately conditional. A cancellation performed by
// deletion or management revocation wins over a late provider completion.
func aiNamedSetStatus(ctx context.Context, tx *sql.Tx, job, status string) error {
	status = strings.TrimSpace(status)
	if status != "queued" && status != "running" && status != "succeeded" && status != "failed" && status != "uncertain" && status != "cancelled" {
		return errors.New("invalid AI task status")
	}
	_, err := tx.ExecContext(ctx, `UPDATE ai_tasks SET status=?,updated_at=? WHERE id=? AND status IN ('queued','running')`, status, time.Now().Unix(), job)
	return err
}

// The named worker is implemented in this module. The hook is retained only
// as an explicit override for tests or an embedding runtime; the normal ticker
// falls back to aiNamedProcess and therefore does real provider work.
var aiNamedNextHook func(*core.Core)

func aiNextNamed(c *core.Core) {
	if aiNamedNextHook != nil {
		aiNamedNextHook(c)
		return
	}
	aiNamedProcess(c)
}

func aiNamedProcess(c *core.Core) {
	ctx, cancel := context.WithTimeout(c.Context, 3*time.Minute)
	defer cancel()
	task, claimed, err := claimAINamedTask(ctx, c)
	if err != nil || !claimed {
		return
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		aiNamedFinishStatus(c, task.ID, "uncertain")
		return
	}
	identity, err := aiNamedTaskIdentity(ctx, tx, task.ID)
	if err != nil {
		_ = tx.Rollback()
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	var authorized bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM chats ch
JOIN members am ON am.chat_id=ch.id AND am.user_id=?
JOIN users au ON au.id=am.user_id
JOIN devices ad ON ad.id=? AND ad.user_id=au.id
JOIN members sm ON sm.chat_id=ch.id AND sm.user_id=?
JOIN users su ON su.id=sm.user_id
JOIN devices sd ON sd.id=? AND sd.user_id=su.id
JOIN messages src ON src.id=? AND src.chat_id=ch.id
WHERE ch.id=? AND ch.mode='basic' AND ch.pending=0
AND am.active=1 AND am.can_send=1 AND au.disabled=0 AND ad.revoked=0
AND sm.active=1 AND sm.can_send=1 AND su.disabled=0 AND sd.revoked=0
AND src.deleted=0 AND src.seq>=sm.joined_seq)`, identity.User, identity.Device, identity.SourceUser, identity.SourceDevice, identity.SourceMessage, identity.Chat).Scan(&authorized)
	if err != nil || !authorized || identity.Mode != "basic" || identity.Status != "running" {
		_ = tx.Rollback()
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	var payload, contextBlob []byte
	var sessionVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT m.payload,s.context,s.version FROM messages m JOIN ai_sessions s ON s.chat_id=? AND s.agent_id=? WHERE m.id=?`, identity.Chat, task.AgentID, identity.SourceMessage).Scan(&payload, &contextBlob, &sessionVersion); err != nil {
		_ = tx.Rollback()
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	if err = tx.Commit(); err != nil {
		aiNamedFinishStatus(c, task.ID, "uncertain")
		return
	}
	plain, err := c.Engine.Open(payload, []byte("message/"+identity.SourceMessage))
	if err != nil || !utf8.Valid(plain) {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	contextRaw, err := c.Engine.Open(contextBlob, []byte("ai/context/"+identity.Chat+"/"+task.AgentID+"/"+formatAIInteger(sessionVersion)))
	if err != nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	var turns []aiTurn
	if json.Unmarshal(contextRaw, &turns) != nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	turns = append(turns, aiTurn{Role: "user", Content: string(plain)})
	agent, err := loadAINamedAgent(ctx, c, task.AgentID)
	if err != nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	// The encrypted creation profile is retained as an audit snapshot. Runtime
	// execution always resolves the current TOML bot so endpoint/key/model
	// removal or tool revocation takes effect after restart without migration.
	settings, providerName, err := c.Config.ResolveBot(agent.BotName)
	if err != nil || aiProviders[providerName] == nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	// Bound the candidate context only in memory. A failed, cancelled, or
	// uncertain provider call must not persist the source turn: only the final
	// successful transaction in aiCompleteNamed may advance the session context.
	// The encrypted creation profile is not model content; ResolveBot above
	// supplies the current runtime settings instead.
	turns = aiBoundContext(turns, settings)
	var names []string
	if err = c.DB.QueryRowContext(ctx, `SELECT tools FROM ai_sessions WHERE chat_id=? AND agent_id=?`, identity.Chat, task.AgentID).Scan(&contextBlob); err != nil || json.Unmarshal(contextBlob, &names) != nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	tools, err := aiNamedToolsCurrent(c, settings, names, decodeAINames(agent.Tools))
	if err != nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	var stillRunning bool
	if err = c.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_tasks t JOIN ai_sessions s ON s.chat_id=t.chat_id AND s.agent_id=t.agent_id JOIN ai_agents a ON a.id=t.agent_id JOIN members m ON m.chat_id=t.chat_id AND m.user_id=a.user_id JOIN users u ON u.id=a.user_id JOIN devices d ON d.id=a.device_id JOIN chats ch ON ch.id=t.chat_id JOIN messages src ON src.id=t.message_id JOIN members sm ON sm.chat_id=t.chat_id AND sm.user_id=src.sender JOIN users su ON su.id=sm.user_id JOIN devices sd ON sd.id=src.device_id AND sd.user_id=su.id WHERE t.id=? AND t.status='running' AND s.version=? AND ch.mode='basic' AND ch.pending=0 AND src.deleted=0 AND src.seq>=sm.joined_seq AND m.active=1 AND m.can_send=1 AND u.disabled=0 AND d.revoked=0 AND sm.active=1 AND sm.can_send=1 AND su.disabled=0 AND sd.revoked=0)`, task.ID, sessionVersion).Scan(&stillRunning); err != nil || !stillRunning {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	provider := aiProviders[providerName]
	if provider == nil {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	providerCtx := context.WithValue(ctx, aiSettingsKey{}, settings)
	answer, err := aiConversationWith(providerCtx, c, task.ID, provider, turns, tools, aiRequest)
	if err != nil {
		aiNamedFinishStatus(c, task.ID, "uncertain")
		return
	}
	if answer == "" || len(answer) > c.Config.Policy.MaxMessageBytes {
		aiNamedFinishStatus(c, task.ID, "failed")
		return
	}
	if err = aiCompleteNamed(providerCtx, c, task, identity, sessionVersion, turns, answer); err != nil {
		aiNamedFinishStatus(c, task.ID, "uncertain")
	}
}

func aiNamedFinishStatus(c *core.Core, job, status string) {
	tx, err := c.DB.BeginTx(c.Context, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if err = aiNamedSetStatus(c.Context, tx, job, status); err == nil {
		_ = tx.Commit()
	}
}

func aiCompleteNamed(ctx context.Context, c *core.Core, task aiNamedTask, identity aiTaskIdentity, version int64, turns []aiTurn, answer string) error {
	messageID := uuid.NewString()
	operation := "ai-" + task.ID
	payload := []byte(answer)
	stored, err := c.Engine.Seal(payload, []byte("message/"+messageID))
	if err != nil {
		return err
	}
	turns = aiBoundContext(append(turns, aiTurn{Role: "assistant", Content: answer}), aiSettings(ctx, c))
	raw, _ := json.Marshal(turns)
	settings := aiSettings(ctx, c)
	maxContextBytes := settings.MaxContextBytes
	if maxContextBytes <= 0 || maxContextBytes > 262144 {
		maxContextBytes = 262144
	}
	if len(raw) > maxContextBytes {
		return errors.New("AI context exhausted")
	}
	contextBlob, err := c.Engine.Seal(raw, []byte("ai/context/"+identity.Chat+"/"+task.AgentID+"/"+formatAIInteger(version)))
	if err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM ai_tasks WHERE id=?`, task.ID).Scan(&status); err != nil || status != "running" {
		return errors.New("AI task no longer running")
	}
	var valid bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_sessions s JOIN ai_agents a ON a.id=s.agent_id JOIN members m ON m.chat_id=s.chat_id AND m.user_id=a.user_id JOIN users u ON u.id=a.user_id JOIN devices d ON d.id=a.device_id JOIN chats ch ON ch.id=s.chat_id JOIN messages src ON src.id=? AND src.chat_id=s.chat_id JOIN members sm ON sm.chat_id=s.chat_id AND sm.user_id=src.sender JOIN users su ON su.id=sm.user_id JOIN devices sd ON sd.id=src.device_id AND sd.user_id=su.id WHERE s.chat_id=? AND s.agent_id=? AND s.version=? AND ch.mode='basic' AND ch.pending=0 AND src.deleted=0 AND src.seq>=sm.joined_seq AND src.sender=? AND src.device_id=? AND m.active=1 AND m.can_send=1 AND u.disabled=0 AND d.revoked=0 AND sm.active=1 AND sm.can_send=1 AND su.disabled=0 AND sd.revoked=0)`, identity.SourceMessage, identity.Chat, task.AgentID, version, identity.SourceUser, identity.SourceDevice).Scan(&valid); err != nil || !valid {
		return errors.New("AI task authorization revoked")
	}
	seq, err := c.Append(ctx, tx, identity.Chat, "message.created", messageID, nil)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	message := core.Message{ID: messageID, ChatID: identity.Chat, Sender: identity.User, DeviceID: identity.Device, OperationID: operation, Seq: seq, Revision: 1, Mode: "basic", Epoch: identity.Epoch, CreatedAt: now}
	result, _ := json.Marshal(message)
	hash := sha256.Sum256(payload)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES(?,?,?,?,?,?,?,'{}',?,?)`, []any{messageID, identity.Chat, identity.User, identity.Device, operation, seq, stored, identity.Epoch, now}},
		{`INSERT INTO operations VALUES(?,?,?,?,?)`, []any{identity.Device, operation, hex.EncodeToString(hash[:]), result, now}},
		{`UPDATE ai_sessions SET context=?,updated_at=? WHERE chat_id=? AND agent_id=? AND version=?`, []any{contextBlob, now, identity.Chat, task.AgentID, version}},
	} {
		if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_tasks SET status='succeeded',result_id=?,updated_at=? WHERE id=? AND status='running'`, messageID, now, task.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.Wake(identity.Chat)
	return nil
}
