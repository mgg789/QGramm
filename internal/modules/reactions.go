//go:build qg_reactions

package modules

import (
	"context"
	"database/sql"
	"github.com/mgg789/QGramm/internal/core"
	"net/http"
)

func init() {
	core.Register("reactions", func(c *core.Core) error {
		if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS reactions(message_id TEXT NOT NULL,user_id TEXT NOT NULL,type INTEGER NOT NULL,PRIMARY KEY(message_id,user_id,type))`); err != nil {
			return err
		}
		c.OnDelete = append(c.OnDelete, func(ctx context.Context, tx *sql.Tx, message string) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM reactions WHERE message_id=?`, message)
			return err
		})
		c.Project = append(c.Project, func(ctx context.Context, id core.Identity, m *core.Message) error {
			if m.Deleted {
				return nil
			}
			rows, err := c.DB.QueryContext(ctx, `SELECT type,user_id FROM reactions WHERE message_id=? ORDER BY type,user_id`, m.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var reaction core.Reaction
				if err = rows.Scan(&reaction.Type, &reaction.UserID); err != nil {
					return err
				}
				m.Reactions = append(m.Reactions, reaction)
			}
			return rows.Err()
		})
		c.AddRoute("GET /v1/chats/{chat}/messages/{message}/reactions", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
			if m, err := c.ViewMessage(r.Context(), id, r.PathValue("message")); err != nil || m.ChatID != r.PathValue("chat") || m.Deleted {
				core.Error(w, 404, "message inaccessible")
				return
			}
			rows, err := c.DB.QueryContext(r.Context(), `SELECT type,user_id FROM reactions WHERE message_id=? ORDER BY type,user_id`, r.PathValue("message"))
			if err != nil {
				core.Error(w, 503, "storage unavailable")
				return
			}
			defer rows.Close()
			out := []map[string]any{}
			for rows.Next() {
				var kind int
				var user string
				if err = rows.Scan(&kind, &user); err != nil {
					core.Error(w, 503, "storage unavailable")
					return
				}
				out = append(out, map[string]any{"type": kind, "user_id": user})
			}
			core.JSON(w, 200, out)
		})
		c.AddRoute("POST /v1/chats/{chat}/messages/{message}/reactions", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
			var in mutationInput
			if !core.Decode(w, r, &in, 1024) {
				return
			}
			if in.Type < 0 || in.Type >= len(c.Config.Policy.ReactionTypes) {
				core.Error(w, 400, "unknown reaction type")
				return
			}
			mutate(c, w, r, id, "reaction", in, func(tx *sql.Tx, sender string, revision int, deleted bool) error {
				if deleted {
					return &core.APIError{Status: 409, Message: "message deleted"}
				}
				var err error
				if in.Remove {
					_, err = tx.ExecContext(r.Context(), `DELETE FROM reactions WHERE message_id=? AND user_id=? AND type=?`, r.PathValue("message"), id.UserID, in.Type)
				} else {
					_, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO reactions VALUES(?,?,?)`, r.PathValue("message"), id.UserID, in.Type)
				}
				if err != nil {
					return err
				}
				_, err = c.Append(r.Context(), tx, r.PathValue("chat"), "reaction.updated", r.PathValue("message"), nil)
				return err
			})
		})
		return nil
	})
}
