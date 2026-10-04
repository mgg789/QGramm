//go:build qg_delete

package modules

import (
	"context"
	"database/sql"
	"github.com/mgg789/QGramm/internal/core"
	"net/http"
)

func init() {
	core.Register("delete", func(c *core.Core) error {
		if _, err := c.DB.Exec(`CREATE TABLE IF NOT EXISTS hidden_messages(message_id TEXT NOT NULL,user_id TEXT NOT NULL,PRIMARY KEY(message_id,user_id))`); err != nil {
			return err
		}
		c.Project = append(c.Project, func(ctx context.Context, id core.Identity, m *core.Message) error {
			var hidden bool
			err := c.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hidden_messages WHERE message_id=? AND user_id=?)`, m.ID, id.UserID).Scan(&hidden)
			if hidden {
				m.Deleted = true
			}
			return err
		})
		c.AddRoute("DELETE /v1/chats/{chat}/messages/{message}", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
			var in mutationInput
			if !core.Decode(w, r, &in, 1024) {
				return
			}
			mutate(c, w, r, id, "delete", in, func(tx *sql.Tx, sender string, revision int, deleted bool) error {
				message, chat := r.PathValue("message"), r.PathValue("chat")
				if c.Config.Policy.DeleteMode == "author_only" {
					_, err := tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO hidden_messages VALUES(?,?)`, message, id.UserID)
					if err != nil {
						return err
					}
					_, err = c.Append(r.Context(), tx, chat, "message.hidden", message, nil)
					return err
				}
				if deleted {
					return nil
				}
				_, err := tx.ExecContext(r.Context(), `UPDATE messages SET deleted=1,payload=X'',metadata='{}',revision=revision+1 WHERE id=?`, message)
				if err != nil {
					return err
				}
				for _, hook := range c.OnDelete {
					if err = hook(r.Context(), tx, message); err != nil {
						return err
					}
				}
				_, err = c.Append(r.Context(), tx, chat, "message.deleted", message, nil)
				return err
			})
		})
		return nil
	})
}
