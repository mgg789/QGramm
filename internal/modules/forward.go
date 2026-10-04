//go:build qg_forward

package modules

import (
	"context"
	"database/sql"
	"github.com/mgg789/QGramm/internal/core"
)

func init() {
	core.Register("forward", func(c *core.Core) error {
		c.InTransaction = append(c.InTransaction, func(ctx context.Context, tx *sql.Tx, id core.Identity, chat string, m core.Message) error {
			if m.ForwardFrom == "" {
				return nil
			}
			var found bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages msg JOIN members member ON member.chat_id=msg.chat_id WHERE msg.id=? AND msg.deleted=0 AND member.user_id=? AND member.active=1 AND msg.seq>=member.joined_seq)`, m.ForwardFrom, id.UserID).Scan(&found)
			if err != nil {
				return err
			}
			if !found {
				return &core.APIError{Status: 403, Message: "forward source inaccessible"}
			}
			return nil
		})
		return nil
	})
}
