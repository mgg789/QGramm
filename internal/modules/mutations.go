//go:build qg_delete || qg_edit || qg_reactions

package modules

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/mgg789/QGramm/internal/core"
	"net/http"
	"time"
)

type mutationInput struct {
	OperationID      string            `json:"operation_id"`
	ExpectedRevision int               `json:"expected_revision"`
	Message          core.MessageInput `json:"message"`
	Type             int               `json:"type"`
	Remove           bool              `json:"remove"`
}

func mutate(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity, action string, in mutationInput, fn func(*sql.Tx, string, int, bool) error) {
	if in.OperationID == "" || len(in.OperationID) > 128 {
		core.Error(w, 400, "operation_id required")
		return
	}
	chat, message := r.PathValue("chat"), r.PathValue("message")
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(append([]byte(action+"/"+chat+"/"+message+"\x00"), raw...))
	hash := hex.EncodeToString(digest[:])
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(r.Context(), `SELECT hash FROM operations WHERE device_id=? AND operation_id=?`, id.DeviceID, in.OperationID).Scan(&previous)
	if err == nil {
		if previous != hash {
			core.Error(w, 409, "operation conflict")
			return
		}
		core.JSON(w, 200, map[string]bool{"updated": true})
		return
	}
	if err != sql.ErrNoRows {
		core.Error(w, 503, "storage unavailable")
		return
	}
	var sender, role string
	var revision int
	var deleted bool
	err = tx.QueryRowContext(r.Context(), `SELECT msg.sender,msg.revision,msg.deleted,member.role FROM messages msg JOIN members member ON member.chat_id=msg.chat_id JOIN devices d ON d.user_id=member.user_id JOIN users u ON u.id=member.user_id WHERE msg.id=? AND msg.chat_id=? AND member.user_id=? AND member.active=1 AND d.id=? AND d.revoked=0 AND u.disabled=0 AND msg.seq>=member.joined_seq`, message, chat, id.UserID, id.DeviceID).Scan(&sender, &revision, &deleted, &role)
	if err != nil {
		core.Error(w, 404, "message inaccessible")
		return
	}
	if action != "reaction" && sender != id.UserID && !(action == "delete" && (role == "owner" || role == "admin") && c.Config.Policy.DeleteMode == "global") {
		core.Error(w, 403, "operation denied")
		return
	}
	if action == "edit" && (deleted || revision != in.ExpectedRevision) {
		core.Error(w, 409, "message deleted or revision conflict")
		return
	}
	if err = fn(tx, sender, revision, deleted); err != nil {
		core.Error(w, 409, "mutation rejected")
		return
	}
	result, _ := json.Marshal(map[string]string{"id": message})
	_, err = tx.ExecContext(r.Context(), `INSERT INTO operations VALUES(?,?,?,?,?)`, id.DeviceID, in.OperationID, hash, result, time.Now().Unix())
	if err != nil || tx.Commit() != nil {
		core.Error(w, 503, "storage unavailable")
		return
	}
	c.Wake(chat)
	core.JSON(w, 200, map[string]bool{"updated": true})
}
