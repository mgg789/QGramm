//go:build qg_edit

package modules

import (
	"database/sql"

	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"net/http"
)

func init() {
	core.Register("edit", func(c *core.Core) error {
		c.AddRoute("PATCH /v1/chats/{chat}/messages/{message}", func(w http.ResponseWriter, r *http.Request, id core.Identity) {
			var in mutationInput
			if !core.Decode(w, r, &in, int64(c.Config.Policy.MaxMessageBytes*2+16384)) {
				return
			}
			chat := r.PathValue("chat")
			var mode string
			var epoch int64
			var pending bool
			if err := c.DB.QueryRowContext(r.Context(), `SELECT mode,epoch,pending FROM chats WHERE id=?`, chat).Scan(&mode, &epoch, &pending); err != nil {
				core.Error(w, 404, "chat not found")
				return
			}
			if pending {
				core.Error(w, 409, "epoch transition pending")
				return
			}
			in.Message.OperationID = in.OperationID
			var err error
			if mode == "basic" {
				if in.Message.Envelope == nil {
					core.Error(w, 400, "encrypted envelope required")
					return
				}
				in.Message.Payload, err = c.Engine.OpenEnvelope(*in.Message.Envelope, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.OperationID))
				if err != nil {
					core.Error(w, 400, "invalid envelope")
					return
				}
			}
			for _, hook := range c.Prepare {
				if err = hook(r.Context(), id, chat, &in.Message); err != nil {
					core.Error(w, 400, "invalid replacement")
					return
				}
			}
			if len(in.Message.Payload) == 0 || len(in.Message.Payload) > c.Config.Policy.MaxMessageBytes {
				core.Error(w, 400, "invalid payload size")
				return
			}
			if in.Message.ReplyTo != "" || in.Message.ForwardFrom != "" || len(in.Message.Attachments) > 0 {
				core.Error(w, 400, "edit changes text payload only")
				return
			}
			mutate(c, w, r, id, "edit", in, func(tx *sql.Tx, sender string, revision int, deleted bool) error {
				var currentEpoch int64
				var currentPending, canSend bool
				if err := tx.QueryRowContext(r.Context(), `SELECT c.epoch,c.pending,m.can_send FROM chats c JOIN members m ON m.chat_id=c.id WHERE c.id=? AND m.user_id=? AND m.active=1`, chat, id.UserID).Scan(&currentEpoch, &currentPending, &canSend); err != nil {
					return err
				}
				if !canSend || currentPending || epoch != currentEpoch {
					return &core.APIError{Status: 409, Message: "permission or epoch changed"}
				}
				payload, err := c.Engine.Seal(in.Message.Payload, []byte("message/"+r.PathValue("message")))
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(r.Context(), `UPDATE messages SET payload=?,revision=revision+1,epoch=?,device_id=?,operation_id=? WHERE id=?`, payload, currentEpoch, id.DeviceID, in.OperationID, r.PathValue("message"))
				if err != nil {
					return err
				}
				_, err = c.Append(r.Context(), tx, chat, "message.edited", r.PathValue("message"), nil)
				return err
			})
		})
		return nil
	})
}
