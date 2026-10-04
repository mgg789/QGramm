//go:build qg_e2ee

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
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"github.com/thomas-vilte/mls-go/ciphersuite"
	"github.com/thomas-vilte/mls-go/framing"
	"github.com/thomas-vilte/mls-go/group"
	"github.com/thomas-vilte/mls-go/keypackages"
)

func init() { core.Register("e2ee", installE2EE) }

const mlsSchema = `
CREATE TABLE IF NOT EXISTS mls_groups(chat_id TEXT PRIMARY KEY REFERENCES chats(id),group_id BLOB NOT NULL UNIQUE,context BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS mls_roster(chat_id TEXT NOT NULL REFERENCES chats(id),device_id TEXT NOT NULL REFERENCES devices(id),leaf_index INTEGER NOT NULL,PRIMARY KEY(chat_id,device_id),UNIQUE(chat_id,leaf_index));
CREATE TABLE IF NOT EXISTS mls_keypackages(id TEXT PRIMARY KEY,device_id TEXT NOT NULL REFERENCES devices(id),payload BLOB NOT NULL,claimed_chat TEXT,used INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mls_controls(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES chats(id),target_device TEXT NOT NULL REFERENCES devices(id),epoch INTEGER NOT NULL,kind TEXT NOT NULL,payload BLOB NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mls_transitions(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES chats(id),sender_device TEXT NOT NULL REFERENCES devices(id),epoch INTEGER NOT NULL,hash TEXT NOT NULL,payload BLOB NOT NULL,confirmed INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS mls_one_transition ON mls_transitions(chat_id) WHERE confirmed=0;
`

type MLSRelayMember struct {
	DeviceID  string `json:"device_id"`
	LeafIndex uint32 `json:"leaf_index"`
}

// Hooks update explicitly admitted AI participant state in the confirmation
// transaction. Human group secrets never enter the relay.
var MLSCommitHooks []func(context.Context, *core.Core, *sql.Tx, string, []byte) error
var MLSBeginTransitionHooks []func(context.Context, *core.Core, *sql.Tx, string) error

type rosterProof struct {
	Version       int              `json:"version"`
	ChatID        string           `json:"chat_id"`
	GroupID       string           `json:"group_id"`
	PreviousEpoch int64            `json:"previous_epoch"`
	Epoch         uint64           `json:"epoch"`
	CommitHash    string           `json:"commit_hash"`
	ContextHash   string           `json:"context_hash"`
	Roster        []MLSRelayMember `json:"roster"`
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// MLSRosterProof is the exact external-authority signature payload. Sort by
// device_id, then use Go JSON escaping and the declared field order.
func MLSRosterProof(chat string, groupID []byte, previous int64, epoch uint64, commitHash string, newContext []byte, roster []MLSRelayMember) []byte {
	sorted := append([]MLSRelayMember(nil), roster...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].DeviceID < sorted[j].DeviceID })
	out, _ := json.Marshal(rosterProof{1, chat, base64.StdEncoding.EncodeToString(groupID), previous, epoch, commitHash, digest(newContext), sorted})
	return out
}
func parseMLSContext(data, groupID []byte, epoch uint64) error {
	gc, err := group.UnmarshalGroupContext(data)
	if err != nil {
		return err
	}
	validTranscript := len(gc.ConfirmedTranscriptHash) == 32 || (epoch == 0 && len(gc.ConfirmedTranscriptHash) == 0)
	if gc.Version != keypackages.MLS10 || gc.CipherSuite != ciphersuite.MLS128DHKEMX25519 || !bytes.Equal(gc.GroupID.AsSlice(), groupID) || gc.Epoch.AsUint64() != epoch || len(gc.TreeHash) != 32 || !validTranscript || !bytes.Equal(gc.Marshal(), data) {
		return errors.New("invalid suite1 GroupContext")
	}
	return nil
}
func validateMLSRoster(ctx context.Context, tx *sql.Tx, chat string, roster []MLSRelayMember) error {
	if len(roster) == 0 || len(roster) > 1024 {
		return errors.New("invalid roster size")
	}
	devices := map[string]bool{}
	leaves := map[uint32]bool{}
	users := map[string]bool{}
	for _, member := range roster {
		if devices[member.DeviceID] || leaves[member.LeafIndex] || member.LeafIndex > 65535 {
			return errors.New("duplicate or invalid roster entry")
		}
		devices[member.DeviceID] = true
		leaves[member.LeafIndex] = true
		var user, sign string
		err := tx.QueryRowContext(ctx, `SELECT d.user_id,d.signing_key FROM devices d JOIN users u ON u.id=d.user_id JOIN members m ON m.user_id=d.user_id WHERE d.id=? AND m.chat_id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0`, member.DeviceID, chat).Scan(&user, &sign)
		if err != nil {
			return errors.New("roster contains unauthorized device")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(sign)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return errors.New("roster device has no signing key")
		}
		users[user] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.user_id FROM members m JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.active=1 AND u.disabled=0`, chat)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var user string
		if err = rows.Scan(&user); err != nil {
			return err
		}
		if !users[user] {
			return errors.New("roster must cover every active account")
		}
	}
	return rows.Err()
}
func saveMLSRoster(ctx context.Context, tx *sql.Tx, chat string, roster []MLSRelayMember) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM mls_roster WHERE chat_id=?`, chat); err != nil {
		return err
	}
	for _, m := range roster {
		if _, err := tx.ExecContext(ctx, `INSERT INTO mls_roster(chat_id,device_id,leaf_index) VALUES(?,?,?)`, chat, m.DeviceID, m.LeafIndex); err != nil {
			return err
		}
	}
	return nil
}

// InitMLSRelayTx is a privileged management initializer for a new relay. The
// caller must have verified an actual participant's MLS state and account ACL.
// No human private keys or epoch secrets are accepted by this relay.
func InitMLSRelayTx(ctx context.Context, c *core.Core, tx *sql.Tx, chat string, groupID, groupContext []byte, epoch uint64, roster []MLSRelayMember) error {
	if epoch > math.MaxInt64 || len(groupID) == 0 || len(groupID) > 256 {
		return errors.New("invalid MLS metadata")
	}
	if err := parseMLSContext(groupContext, groupID, epoch); err != nil {
		return err
	}
	if err := validateMLSRoster(ctx, tx, chat, roster); err != nil {
		return err
	}
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode FROM chats WHERE id=?`, chat).Scan(&mode); err != nil || mode != "e2ee" {
		return errors.New("E2EE chat required")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mls_groups(chat_id,group_id,context) VALUES(?,?,?)`, chat, groupID, groupContext); err != nil {
		return err
	}
	if err := saveMLSRoster(ctx, tx, chat, roster); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE chats SET epoch=?,pending=0 WHERE id=?`, epoch, chat)
	return err
}
func StoreMLSWelcomeTx(ctx context.Context, c *core.Core, tx *sql.Tx, chat, target string, epoch uint64, wire []byte) error {
	msg, err := framing.UnmarshalMLSMessage(wire)
	if err != nil || len(msg.Welcome) == 0 {
		return errors.New("invalid MLS Welcome")
	}
	welcome, err := group.UnmarshalWelcome(msg.Welcome)
	if err != nil || welcome.CipherSuite != ciphersuite.MLS128DHKEMX25519 {
		return errors.New("suite1 Welcome required")
	}
	id := uuid.NewString()
	stored, err := c.Engine.Seal(wire, []byte("mls/control/"+id+"/"+target))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mls_controls(id,chat_id,target_device,epoch,kind,payload,created_at) VALUES(?,?,?,?,?,?,?)`, id, chat, target, epoch, "welcome", stored, time.Now().Unix())
	return err
}
func installE2EE(c *core.Core) error {
	if _, err := c.DB.Exec(mlsSchema); err != nil {
		return err
	}
	c.Prepare = append(c.Prepare, func(ctx context.Context, id core.Identity, chat string, in *core.MessageInput) error {
		var mode string
		if err := c.DB.QueryRowContext(ctx, `SELECT mode FROM chats WHERE id=?`, chat).Scan(&mode); err != nil {
			return err
		}
		if mode != "e2ee" {
			return nil
		}
		wire, err := base64.StdEncoding.Strict().DecodeString(in.MLS)
		if err != nil {
			return &core.APIError{Status: 400, Message: "invalid MLS encoding"}
		}
		msg, err := framing.UnmarshalMLSMessage(wire)
		if err != nil {
			return &core.APIError{Status: 400, Message: "invalid MLS wire"}
		}
		pm, ok := msg.AsPrivate()
		if !ok || pm.ContentType != framing.ContentTypeApplication || len(pm.EncryptedSenderData) < 16 || len(pm.Ciphertext) < 16 || !bytes.Equal(msg.Marshal(), wire) {
			return &core.APIError{Status: 400, Message: "MLS encrypted application message required"}
		}
		var groupID []byte
		var epoch int64
		var pending bool
		err = c.DB.QueryRowContext(ctx, `SELECT g.group_id,c.epoch,c.pending FROM mls_groups g JOIN chats c ON c.id=g.chat_id JOIN mls_roster roster ON roster.chat_id=g.chat_id WHERE g.chat_id=? AND roster.device_id=?`, chat, id.DeviceID).Scan(&groupID, &epoch, &pending)
		if err != nil || pending || pm.Epoch != uint64(epoch) || in.Epoch != epoch || !bytes.Equal(pm.GroupID, groupID) {
			return &core.APIError{Status: 409, Message: "MLS group, device, epoch or transition mismatch"}
		}
		if !bytes.Equal(pm.AuthenticatedData, cryptoenc.Binding(chat, id.UserID, id.DeviceID, in.OperationID)) {
			return &core.APIError{Status: 400, Message: "MLS authenticated binding mismatch"}
		}
		in.Payload = wire
		return nil
	})
	c.InTransaction = append(c.InTransaction, func(ctx context.Context, tx *sql.Tx, id core.Identity, chat string, msg core.Message) error {
		if msg.Mode != "e2ee" {
			return nil
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mls_roster WHERE chat_id=? AND device_id=?`, chat, id.DeviceID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return &core.APIError{Status: 409, Message: "device not in current MLS roster"}
		}
		return nil
	})
	c.AddRoute("POST /v1/mls/keypackages", func(w http.ResponseWriter, r *http.Request, id core.Identity) { uploadMLSKeyPackage(c, w, r, id) })
	c.AddRoute("POST /v1/chats/{chat}/mls/keypackages/{device}/claim", func(w http.ResponseWriter, r *http.Request, id core.Identity) { claimMLSKeyPackage(c, w, r, id) })
	c.AddRoute("POST /v1/chats/{chat}/mls/commits", func(w http.ResponseWriter, r *http.Request, id core.Identity) { submitMLSCommit(c, w, r, id) })
	c.AddRoute("GET /v1/chats/{chat}/mls", func(w http.ResponseWriter, r *http.Request, id core.Identity) { getMLSState(c, w, r, id) })
	c.AddRoute("GET /v1/chats/{chat}/mls/inbox", func(w http.ResponseWriter, r *http.Request, id core.Identity) { getMLSInbox(c, w, r, id) })
	c.AddManagementRoute("POST /management/v1/chats/{chat}/mls/confirm", func(w http.ResponseWriter, r *http.Request) { confirmMLSEpoch(c, w, r) })
	return nil
}
func uploadMLSKeyPackage(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	var in struct {
		KeyPackage string `json:"key_package"`
	}
	if !core.Decode(w, r, &in, 128<<10) {
		return
	}
	wire, err := base64.StdEncoding.Strict().DecodeString(in.KeyPackage)
	if err != nil {
		core.Error(w, 400, "invalid KeyPackage encoding")
		return
	}
	identity, pub, err := cryptoenc.KeyPackageIdentity(wire)
	var registered string
	if err != nil || !bytes.Equal(identity, []byte(id.DeviceID)) || c.DB.QueryRowContext(r.Context(), `SELECT signing_key FROM devices WHERE id=? AND user_id=? AND revoked=0`, id.DeviceID, id.UserID).Scan(&registered) != nil {
		core.Error(w, 400, "invalid device KeyPackage")
		return
	}
	key, err := base64.StdEncoding.Strict().DecodeString(registered)
	if err != nil || !bytes.Equal(pub, key) {
		core.Error(w, 403, "KeyPackage signing key differs from registered device")
		return
	}
	ref := digest(wire)
	stored, err := c.Engine.Seal(wire, []byte("mls/keypackage/"+ref+"/"+id.DeviceID))
	if err != nil {
		core.Error(w, 500, "KeyPackage encryption failed")
		return
	}
	result, err := c.DB.ExecContext(r.Context(), `INSERT INTO mls_keypackages(id,device_id,payload,created_at) SELECT ?,?,?,? WHERE (SELECT count(*) FROM mls_keypackages WHERE device_id=? AND claimed_chat IS NULL)<100 ON CONFLICT(id) DO NOTHING`, ref, id.DeviceID, stored, time.Now().Unix(), id.DeviceID)
	if err != nil {
		core.Error(w, 500, "KeyPackage persistence failed")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		core.Error(w, 409, "duplicate KeyPackage or device quota reached")
		return
	}
	core.JSON(w, 201, map[string]string{"id": ref})
}
func claimMLSKeyPackage(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	chat, target := r.PathValue("chat"), r.PathValue("device")
	_, send, _, err := c.Member(r.Context(), id.UserID, chat)
	if err != nil || !send {
		core.Error(w, 403, "chat access denied")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 500, "transaction unavailable")
		return
	}
	defer tx.Rollback()
	var allowed int
	err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM devices d JOIN users u ON u.id=d.user_id JOIN members m ON m.user_id=d.user_id JOIN chats c ON c.id=m.chat_id WHERE d.id=? AND m.chat_id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0 AND c.mode='e2ee'`, target, chat).Scan(&allowed)
	if err != nil || allowed != 1 {
		core.Error(w, 403, "target device not authorized for E2EE chat")
		return
	}
	var ref string
	var stored []byte
	err = tx.QueryRowContext(r.Context(), `UPDATE mls_keypackages SET claimed_chat=? WHERE id=(SELECT id FROM mls_keypackages WHERE device_id=? AND claimed_chat IS NULL ORDER BY created_at,id LIMIT 1) AND claimed_chat IS NULL RETURNING id,payload`, chat, target).Scan(&ref, &stored)
	if err != nil {
		core.Error(w, 409, "no unclaimed KeyPackage")
		return
	}
	wire, err := c.Engine.Open(stored, []byte("mls/keypackage/"+ref+"/"+target))
	if err != nil {
		core.Error(w, 500, "stored KeyPackage invalid")
		return
	}
	if _, _, err = cryptoenc.KeyPackageIdentity(wire); err != nil {
		// Retire expired/invalid entries rather than selecting the same oldest
		// package forever. Keep its ID reserved to prevent one-time key reuse.
		if err = tx.Commit(); err != nil {
			core.Error(w, 409, "concurrent KeyPackage retirement")
			return
		}
		core.Error(w, 409, "KeyPackage expired or invalid")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 409, "concurrent claim")
		return
	}
	core.JSON(w, 200, map[string]string{"id": ref, "device_id": target, "key_package": base64.StdEncoding.EncodeToString(wire)})
}
func submitMLSCommit(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	var in struct {
		Commit   string `json:"commit"`
		Welcomes []struct {
			DeviceID     string `json:"device_id"`
			KeyPackageID string `json:"key_package_id"`
			Welcome      string `json:"welcome"`
		} `json:"welcomes"`
	}
	if !core.Decode(w, r, &in, 2<<20) {
		return
	}
	chat := r.PathValue("chat")
	_, send, _, err := c.Member(r.Context(), id.UserID, chat)
	if err != nil || !send {
		core.Error(w, 403, "chat access denied")
		return
	}
	wire, err := base64.StdEncoding.Strict().DecodeString(in.Commit)
	if err != nil {
		core.Error(w, 400, "invalid commit encoding")
		return
	}
	parsed, err := framing.UnmarshalMLSMessage(wire)
	if err != nil {
		core.Error(w, 400, "invalid MLS commit")
		return
	}
	pm, ok := parsed.AsPublic()
	if !ok || pm.Content.ContentType() != framing.ContentTypeCommit || pm.Content.Sender.Type != framing.SenderTypeMember {
		core.Error(w, 400, "public member commit required")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 500, "transaction unavailable")
		return
	}
	defer tx.Rollback()
	var gid, gc []byte
	var epoch int64
	var leaf uint32
	var sign string
	err = tx.QueryRowContext(r.Context(), `SELECT g.group_id,g.context,c.epoch,roster.leaf_index,d.signing_key FROM mls_groups g JOIN chats c ON c.id=g.chat_id JOIN mls_roster roster ON roster.chat_id=g.chat_id JOIN devices d ON d.id=roster.device_id JOIN members m ON m.chat_id=g.chat_id AND m.user_id=d.user_id JOIN users u ON u.id=d.user_id WHERE g.chat_id=? AND roster.device_id=? AND d.user_id=? AND d.revoked=0 AND u.disabled=0 AND m.active=1 AND m.can_send=1`, chat, id.DeviceID, id.UserID).Scan(&gid, &gc, &epoch, &leaf, &sign)
	if err != nil || pm.Content.Epoch != uint64(epoch) || !bytes.Equal(pm.Content.GroupID, gid) || pm.Content.Sender.LeafIndex != leaf {
		core.Error(w, 409, "commit group, sender or epoch mismatch")
		return
	}
	key, err := base64.StdEncoding.Strict().DecodeString(sign)
	if err != nil || len(key) != 32 {
		core.Error(w, 403, "registered signing key required")
		return
	}
	ac := &framing.AuthenticatedContent{WireFormat: framing.WireFormatPublicMessage, Content: pm.Content, Auth: pm.Auth, GroupContext: gc}
	pub := ciphersuite.NewMLSSignaturePublicKey(key, ciphersuite.ED25519)
	if err = ciphersuite.VerifyWithLabel(pub, "FramedContentTBS", ac.MarshalTBS(), pm.Auth.Signature); err != nil {
		core.Error(w, 403, "invalid MLS commit signature")
		return
	}
	for _, hook := range MLSBeginTransitionHooks {
		if err = hook(r.Context(), c, tx, chat); err != nil {
			core.Error(w, 409, "MLS transition conflicts with admitted AI work")
			return
		}
	}
	transition := uuid.NewString()
	stored, err := c.Engine.Seal(wire, []byte("mls/transition/"+transition))
	if err != nil {
		core.Error(w, 500, "commit encryption failed")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO mls_transitions(id,chat_id,sender_device,epoch,hash,payload,created_at) VALUES(?,?,?,?,?,?,?)`, transition, chat, id.DeviceID, epoch, digest(wire), stored, time.Now().Unix())
	if err != nil {
		core.Error(w, 409, "epoch transition already pending")
		return
	}
	if len(in.Welcomes) > 1024 {
		core.Error(w, 400, "too many Welcome recipients")
		return
	}
	targets := map[string]bool{}
	for _, item := range in.Welcomes {
		if targets[item.DeviceID] {
			core.Error(w, 400, "duplicate Welcome recipient")
			return
		}
		targets[item.DeviceID] = true
		var claimedChat, device string
		var packageStored []byte
		err = tx.QueryRowContext(r.Context(), `UPDATE mls_keypackages SET used=1 WHERE id=? AND used=0 RETURNING claimed_chat,device_id,payload`, item.KeyPackageID).Scan(&claimedChat, &device, &packageStored)
		if err != nil || claimedChat != chat || device != item.DeviceID {
			core.Error(w, 403, "Welcome needs a consumed device KeyPackage for this chat")
			return
		}
		var allowed int
		err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM devices d JOIN members m ON m.user_id=d.user_id JOIN users u ON u.id=d.user_id WHERE d.id=? AND m.chat_id=? AND m.active=1 AND d.revoked=0 AND u.disabled=0`, device, chat).Scan(&allowed)
		if err != nil || allowed != 1 {
			core.Error(w, 403, "Welcome recipient unauthorized")
			return
		}
		welcome, err := base64.StdEncoding.Strict().DecodeString(item.Welcome)
		if err == nil {
			packageWire, openErr := c.Engine.Open(packageStored, []byte("mls/keypackage/"+item.KeyPackageID+"/"+device))
			if openErr != nil {
				core.Error(w, 500, "stored KeyPackage authentication failed")
				return
			}
			welcomeMessage, parseErr := framing.UnmarshalMLSMessage(welcome)
			if parseErr != nil || len(welcomeMessage.Welcome) == 0 {
				core.Error(w, 400, "invalid Welcome wire")
				return
			}
			welcomeObject, parseErr := group.UnmarshalWelcome(welcomeMessage.Welcome)
			if parseErr != nil {
				core.Error(w, 400, "invalid Welcome structure")
				return
			}
			ref := ciphersuite.MakeKeyPackageRef(packageWire, ciphersuite.MLS128DHKEMX25519.HashFunction()).AsSlice()
			matched := false
			for _, entry := range welcomeObject.Secrets {
				if bytes.Equal(entry.NewMember, ref) {
					matched = true
				}
			}
			if !matched {
				core.Error(w, 400, "Welcome does not target claimed KeyPackage reference")
				return
			}
		}
		if err != nil || StoreMLSWelcomeTx(r.Context(), c, tx, chat, device, uint64(epoch+1), welcome) != nil {
			core.Error(w, 400, "invalid Welcome")
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE chats SET pending=1 WHERE id=?`, chat); err != nil {
		core.Error(w, 500, "barrier persistence failed")
		return
	}
	if _, err = c.Append(r.Context(), tx, chat, "mls.commit.pending", "", map[string]any{"transition_id": transition, "epoch": epoch, "commit_hash": digest(wire)}); err != nil {
		core.Error(w, 500, "event persistence failed")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 409, "concurrent MLS transition")
		return
	}
	core.JSON(w, 202, map[string]any{"transition_id": transition, "commit_hash": digest(wire), "pending_rekey": true})
}
func getMLSState(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	chat := r.PathValue("chat")
	if _, _, _, err := c.Member(r.Context(), id.UserID, chat); err != nil {
		core.Error(w, 403, "chat access denied")
		return
	}
	var gid, gc []byte
	var epoch int64
	var pending bool
	if err := c.DB.QueryRowContext(r.Context(), `SELECT g.group_id,g.context,c.epoch,c.pending FROM mls_groups g JOIN chats c ON c.id=g.chat_id WHERE g.chat_id=?`, chat).Scan(&gid, &gc, &epoch, &pending); err != nil {
		core.Error(w, 409, "MLS group not initialized")
		return
	}
	rows, err := c.DB.QueryContext(r.Context(), `SELECT device_id,leaf_index FROM mls_roster WHERE chat_id=? ORDER BY device_id`, chat)
	if err != nil {
		core.Error(w, 500, "roster unavailable")
		return
	}
	defer rows.Close()
	roster := []MLSRelayMember{}
	for rows.Next() {
		var member MLSRelayMember
		if err = rows.Scan(&member.DeviceID, &member.LeafIndex); err != nil {
			core.Error(w, 500, "invalid roster")
			return
		}
		roster = append(roster, member)
	}
	if rows.Err() != nil {
		core.Error(w, 500, "roster unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"group_id": base64.StdEncoding.EncodeToString(gid), "group_context": base64.StdEncoding.EncodeToString(gc), "epoch": epoch, "pending_rekey": pending, "roster": roster, "suite": 1})
}
func getMLSInbox(c *core.Core, w http.ResponseWriter, r *http.Request, id core.Identity) {
	chat := r.PathValue("chat")
	if _, _, _, err := c.Member(r.Context(), id.UserID, chat); err != nil {
		core.Error(w, 403, "chat access denied")
		return
	}
	var welcomeAfter, commitAfter int64
	if cursor := r.URL.Query().Get("after_welcome"); cursor != "" {
		if err := c.DB.QueryRowContext(r.Context(), `SELECT rowid FROM mls_controls WHERE id=? AND chat_id=? AND target_device=?`, cursor, chat, id.DeviceID).Scan(&welcomeAfter); err != nil {
			core.Error(w, 400, "invalid Welcome cursor")
			return
		}
	}
	if cursor := r.URL.Query().Get("after_commit"); cursor != "" {
		if err := c.DB.QueryRowContext(r.Context(), `SELECT rowid FROM mls_transitions WHERE id=? AND chat_id=?`, cursor, chat).Scan(&commitAfter); err != nil {
			core.Error(w, 400, "invalid commit cursor")
			return
		}
	}
	rows, err := c.DB.QueryContext(r.Context(), `SELECT id,epoch,kind,payload FROM mls_controls WHERE chat_id=? AND target_device=? AND rowid>? ORDER BY rowid LIMIT 100`, chat, id.DeviceID, welcomeAfter)
	if err != nil {
		core.Error(w, 500, "MLS inbox unavailable")
		return
	}
	controls := []map[string]any{}
	nextWelcome, nextCommit := r.URL.Query().Get("after_welcome"), r.URL.Query().Get("after_commit")
	for rows.Next() {
		var ref, kind string
		var epoch int64
		var stored []byte
		if err = rows.Scan(&ref, &epoch, &kind, &stored); err != nil {
			rows.Close()
			core.Error(w, 500, "MLS inbox invalid")
			return
		}
		wire, err := c.Engine.Open(stored, []byte("mls/control/"+ref+"/"+id.DeviceID))
		if err != nil {
			rows.Close()
			core.Error(w, 500, "MLS inbox authentication failed")
			return
		}
		controls = append(controls, map[string]any{"id": ref, "epoch": epoch, "kind": kind, "wire": base64.StdEncoding.EncodeToString(wire)})
		nextWelcome = ref
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		core.Error(w, 500, "MLS inbox unavailable")
		return
	}
	rows, err = c.DB.QueryContext(r.Context(), `SELECT t.id,t.epoch,t.hash,t.payload,t.confirmed FROM mls_transitions t JOIN mls_roster roster ON roster.chat_id=t.chat_id WHERE t.chat_id=? AND roster.device_id=? AND t.rowid>? ORDER BY t.rowid LIMIT 100`, chat, id.DeviceID, commitAfter)
	if err != nil {
		core.Error(w, 500, "MLS transitions unavailable")
		return
	}
	defer rows.Close()
	commits := []map[string]any{}
	for rows.Next() {
		var ref, hash string
		var epoch int64
		var stored []byte
		var confirmed bool
		if err = rows.Scan(&ref, &epoch, &hash, &stored, &confirmed); err != nil {
			core.Error(w, 500, "MLS transitions invalid")
			return
		}
		wire, err := c.Engine.Open(stored, []byte("mls/transition/"+ref))
		if err != nil {
			core.Error(w, 500, "MLS commit authentication failed")
			return
		}
		commits = append(commits, map[string]any{"id": ref, "epoch": epoch, "commit_hash": hash, "wire": base64.StdEncoding.EncodeToString(wire), "confirmed": confirmed})
		nextCommit = ref
	}
	if rows.Err() != nil {
		core.Error(w, 500, "MLS transitions unavailable")
		return
	}
	core.JSON(w, 200, map[string]any{"welcomes": controls, "commits": commits, "next_welcome": nextWelcome, "next_commit": nextCommit})
}
func confirmMLSEpoch(c *core.Core, w http.ResponseWriter, r *http.Request) {
	var in struct {
		TransitionID  string           `json:"transition_id"`
		GroupID       string           `json:"group_id"`
		GroupContext  string           `json:"group_context"`
		PreviousEpoch int64            `json:"previous_epoch"`
		Epoch         uint64           `json:"epoch"`
		SignerDevice  string           `json:"signer_device"`
		Signature     string           `json:"signature"`
		Roster        []MLSRelayMember `json:"roster"`
	}
	if !core.Decode(w, r, &in, 256<<10) {
		return
	}
	chat := r.PathValue("chat")
	gid, err := base64.StdEncoding.Strict().DecodeString(in.GroupID)
	if err != nil {
		core.Error(w, 400, "invalid group encoding")
		return
	}
	gc, err := base64.StdEncoding.Strict().DecodeString(in.GroupContext)
	if err != nil || parseMLSContext(gc, gid, in.Epoch) != nil || in.Epoch > math.MaxInt64 {
		core.Error(w, 400, "invalid suite1 context")
		return
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(in.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		core.Error(w, 400, "invalid authority signature encoding")
		return
	}
	tx, err := c.DB.BeginTx(r.Context(), nil)
	if err != nil {
		core.Error(w, 500, "transaction unavailable")
		return
	}
	defer tx.Rollback()
	if err = validateMLSRoster(r.Context(), tx, chat, in.Roster); err != nil {
		core.Error(w, 409, "new roster differs from active account/device ACL")
		return
	}
	var sign string
	err = tx.QueryRowContext(r.Context(), `SELECT signing_key FROM devices WHERE id=? AND revoked=0`, in.SignerDevice).Scan(&sign)
	if err != nil {
		core.Error(w, 403, "registered authority signer required")
		return
	}
	pub, err := base64.StdEncoding.Strict().DecodeString(sign)
	if err != nil || len(pub) != 32 {
		core.Error(w, 403, "authority signing key unavailable")
		return
	}
	inRoster := false
	for _, member := range in.Roster {
		if member.DeviceID == in.SignerDevice {
			inRoster = true
		}
	}
	if !inRoster {
		core.Error(w, 403, "signer must remain in approved roster")
		return
	}
	var commitHash string
	var wire []byte
	if in.PreviousEpoch == -1 {
		if in.TransitionID != "" {
			core.Error(w, 400, "initialization cannot reference transition")
			return
		}
	} else {
		if in.PreviousEpoch < 0 || in.PreviousEpoch == math.MaxInt64 || in.Epoch != uint64(in.PreviousEpoch+1) {
			core.Error(w, 409, "epoch must advance by one")
			return
		}
		var oldGID []byte
		var oldEpoch int64
		var pending bool
		var rosterMember int
		err = tx.QueryRowContext(r.Context(), `SELECT g.group_id,c.epoch,c.pending FROM mls_groups g JOIN chats c ON c.id=g.chat_id WHERE g.chat_id=?`, chat).Scan(&oldGID, &oldEpoch, &pending)
		if err != nil || !pending || oldEpoch != in.PreviousEpoch || !bytes.Equal(oldGID, gid) {
			core.Error(w, 409, "stale MLS confirmation")
			return
		}
		err = tx.QueryRowContext(r.Context(), `SELECT count(*) FROM mls_roster WHERE chat_id=? AND device_id=?`, chat, in.SignerDevice).Scan(&rosterMember)
		if err != nil || rosterMember != 1 {
			core.Error(w, 403, "existing MLS participant signer required")
			return
		}
		var stored []byte
		err = tx.QueryRowContext(r.Context(), `SELECT hash,payload FROM mls_transitions WHERE id=? AND chat_id=? AND epoch=? AND sender_device=? AND confirmed=0`, in.TransitionID, chat, in.PreviousEpoch, in.SignerDevice).Scan(&commitHash, &stored)
		if err != nil {
			core.Error(w, 409, "pending signed transition required")
			return
		}
		wire, err = c.Engine.Open(stored, []byte("mls/transition/"+in.TransitionID))
		if err != nil {
			core.Error(w, 500, "commit authentication failed")
			return
		}
	}
	proof := MLSRosterProof(chat, gid, in.PreviousEpoch, in.Epoch, commitHash, gc, in.Roster)
	if !ed25519.Verify(ed25519.PublicKey(pub), proof, signature) {
		core.Error(w, 403, "invalid roster authority proof")
		return
	}
	if in.PreviousEpoch == -1 {
		err = InitMLSRelayTx(r.Context(), c, tx, chat, gid, gc, in.Epoch, in.Roster)
	} else {
		for _, hook := range MLSCommitHooks {
			if err = hook(r.Context(), c, tx, chat, wire); err != nil {
				core.Error(w, 409, "admitted AI participant cannot apply commit")
				return
			}
		}
		if err = saveMLSRoster(r.Context(), tx, chat, in.Roster); err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE mls_groups SET context=? WHERE chat_id=?`, gc, chat)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE mls_transitions SET confirmed=1 WHERE id=? AND confirmed=0`, in.TransitionID)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE chats SET epoch=?,pending=0 WHERE id=? AND epoch=? AND pending=1`, in.Epoch, chat, in.PreviousEpoch)
		}
	}
	if err != nil {
		core.Error(w, 409, "MLS initialization or confirmation conflict")
		return
	}
	if _, err = c.Append(r.Context(), tx, chat, "mls.epoch.confirmed", "", map[string]any{"epoch": in.Epoch, "transition_id": in.TransitionID, "roster": in.Roster}); err != nil {
		core.Error(w, 500, "MLS event persistence failed")
		return
	}
	if err = tx.Commit(); err != nil {
		core.Error(w, 409, "concurrent MLS confirmation")
		return
	}
	core.JSON(w, 200, map[string]any{"epoch": in.Epoch, "pending_rekey": false})
}
