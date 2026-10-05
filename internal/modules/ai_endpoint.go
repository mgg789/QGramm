//go:build qg_ai_endpoint && qg_e2ee

package modules

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/mgg789/QGramm/internal/core"
)

func init() { core.Register("ai_endpoint", installAIEndpoints) }

// This is an identity registry, never an owner of endpoint MLS private state.
func installAIEndpoints(c *core.Core) error {
	tx, err := c.DB.BeginTx(c.Context, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c.Context, `CREATE TABLE IF NOT EXISTS ai_endpoint_schema(version INTEGER PRIMARY KEY); CREATE TABLE IF NOT EXISTS ai_endpoints(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,device_id TEXT NOT NULL UNIQUE,kind TEXT NOT NULL,created_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(c.Context, `SELECT COALESCE(MAX(version),0) FROM ai_endpoint_schema`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("unsupported AI endpoint schema")
	}
	if version == 0 {
		if _, err = tx.ExecContext(c.Context, `INSERT INTO ai_endpoint_schema VALUES(1)`); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	c.AddManagementRoute("PUT /management/v1/ai/endpoints/{endpoint}", func(w http.ResponseWriter, r *http.Request) { putAIEndpoint(c, w, r) })
	c.AddRoute("GET /v1/chats/{chat}/ai/endpoints", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
		chat := r.PathValue("chat")
		if _, _, _, err := c.Member(r.Context(), id.UserID, chat); err != nil {
			core.Error(w, 403, "chat access denied")
			return
		}
		rows, err := c.DB.QueryContext(r.Context(), `SELECT e.id,e.user_id,e.device_id,e.kind FROM ai_endpoints e JOIN members m ON m.user_id=e.user_id JOIN devices d ON d.id=e.device_id JOIN users u ON u.id=e.user_id WHERE m.chat_id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0 ORDER BY e.id LIMIT 128`, chat)
		if err != nil {
			core.Error(w, 503, "endpoint registry unavailable")
			return
		}
		defer rows.Close()
		items := []map[string]string{}
		for rows.Next() {
			var name, user, device, kind string
			if rows.Scan(&name, &user, &device, &kind) != nil {
				core.Error(w, 503, "endpoint registry unavailable")
				return
			}
			items = append(items, map[string]string{"id": name, "user_id": user, "device_id": device, "kind": kind, "participant_owner": "endpoint", "transport": "mls", "plaintext_boundary": "endpoint_host"})
		}
		if rows.Err() != nil {
			core.Error(w, 503, "endpoint registry unavailable")
			return
		}
		core.JSON(w, 200, map[string]any{"items": items})
	})
	return nil
}

func putAIEndpoint(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in struct {
		User   string `json:"user_id"`
		Device string `json:"device_id"`
		Kind   string `json:"kind"`
	}
	if !core.Decode(w, r, &in, 4096) {
		return
	}
	name := r.PathValue("endpoint")
	if name == "" || len(name) > 128 || in.User == "" || in.Device == "" || (in.Kind != "llm" && in.Kind != "tools" && in.Kind != "storage") {
		core.Error(w, 400, "invalid endpoint")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "registry unavailable")
		return
	}
	defer tx.Rollback()
	var signing string
	if err = tx.QueryRowContext(r.Context(), `SELECT d.signing_key FROM devices d JOIN users u ON u.id=d.user_id WHERE d.id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0`, in.Device, in.User).Scan(&signing); err != nil {
		core.Error(w, 403, "registered active endpoint device required")
		return
	}
	key, err := base64.StdEncoding.DecodeString(signing)
	if err != nil || len(key) != 32 {
		core.Error(w, 400, "endpoint MLS signing key required")
		return
	}
	// Core-managed AI identities cannot acquire a second state owner.
	for _, table := range []string{"ai_chats", "ai_agents"} {
		var exists bool
		if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, table).Scan(&exists); err != nil {
			core.Error(w, 503, "registry unavailable")
			return
		}
		if exists {
			var conflict bool
			if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE user_id=? OR device_id=?)`, in.User, in.Device).Scan(&conflict); err != nil || conflict {
				core.Error(w, 409, "core already owns this AI identity")
				return
			}
		}
	}
	var existingUser, existingDevice, existingKind string
	err = tx.QueryRowContext(r.Context(), `SELECT user_id,device_id,kind FROM ai_endpoints WHERE id=?`, name).Scan(&existingUser, &existingDevice, &existingKind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		core.Error(w, 503, "registry unavailable")
		return
	}
	if err == nil && (existingUser != in.User || existingDevice != in.Device || existingKind != in.Kind) {
		core.Error(w, 409, "endpoint identity is immutable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO ai_endpoints(id,user_id,device_id,kind,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, name, in.User, in.Device, in.Kind, time.Now().Unix()); err != nil {
		core.Error(w, 409, "endpoint device already registered")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 503, "registry unavailable")
		return
	}
	core.JSON(w, 200, map[string]string{"id": name, "participant_owner": "endpoint", "transport": "mls", "plaintext_boundary": "endpoint_host"})
}
