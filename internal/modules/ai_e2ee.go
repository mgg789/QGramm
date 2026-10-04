//go:build qg_e2ee && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"

	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func init() {
	MLSBeginTransitionHooks = append(MLSBeginTransitionHooks, func(ctx context.Context, c *core.Core, tx *sql.Tx, chat string) error {
		if !c.Config.Features.OpenAI && !c.Config.Features.Anthropic {
			return nil
		}
		var active int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM ai_jobs WHERE chat_id=? AND status IN ('queued','running')`, chat).Scan(&active); e != nil {
			return e
		}
		if active > 0 {
			return errors.New("AI jobs must drain before MLS transition")
		}
		return nil
	})
	MLSCommitHooks = append(MLSCommitHooks, func(ctx context.Context, c *core.Core, tx *sql.Tx, chat string, wire []byte) error {
		if !c.Config.Features.OpenAI && !c.Config.Features.Anthropic {
			return nil
		}
		var state []byte
		e := tx.QueryRowContext(ctx, `SELECT state FROM ai_chats WHERE chat_id=?`, chat).Scan(&state)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		var active int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM ai_jobs WHERE chat_id=? AND status='running'`, chat).Scan(&active); e != nil {
			return e
		}
		if active > 0 {
			return errors.New("AI external operation active")
		}
		participant, e := cryptoenc.RestoreMLS(c.Engine, state, []byte("ai/state/"+chat))
		if e != nil {
			return e
		}
		if e = participant.ProcessCommit(wire); e != nil {
			return e
		}
		state, e = participant.Snapshot(c.Engine, []byte("ai/state/"+chat))
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE ai_chats SET state=? WHERE chat_id=?`, state, chat)
		return e
	})
	aiCreateMLS = func(c *core.Core, chat, device, human string, kp []byte) (aiIdentity, error) {
		identity, signing, e := cryptoenc.KeyPackageIdentity(kp)
		if e != nil || !bytes.Equal(identity, []byte(human)) {
			return aiIdentity{}, errors.New("human MLS identity mismatch")
		}
		var registered string
		if c.DB.QueryRowContext(c.Context, `SELECT signing_key FROM devices WHERE id=? AND revoked=0`, human).Scan(&registered) != nil {
			return aiIdentity{}, errors.New("human device unavailable")
		}
		registeredKey, e := base64.StdEncoding.Strict().DecodeString(registered)
		if e != nil || !bytes.Equal(registeredKey, signing) {
			return aiIdentity{}, errors.New("human MLS signing key mismatch")
		}
		participant, e := cryptoenc.NewMLS([]byte(device))
		if e != nil {
			return aiIdentity{}, e
		}
		if _, e = participant.CreateGroup(); e != nil {
			return aiIdentity{}, e
		}
		_, welcome, e := participant.Invite(kp)
		if e != nil {
			return aiIdentity{}, e
		}
		state, e := participant.Snapshot(c.Engine, []byte("ai/state/"+chat))
		if e != nil {
			return aiIdentity{}, e
		}
		key, e := ecdh.X25519().GenerateKey(rand.Reader)
		if e != nil {
			return aiIdentity{}, e
		}
		return aiIdentity{PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), SigningKey: base64.StdEncoding.EncodeToString(participant.SigningPublicKey()), State: state, Welcome: welcome, GroupID: participant.GroupID(), GroupContext: participant.GroupContext(), Epoch: int64(participant.Epoch())}, nil
	}
	aiInitRelay = func(ctx context.Context, c *core.Core, tx *sql.Tx, chat, device, human string, id aiIdentity) error {
		// The same invitation establishes the verified two-leaf roster.
		if e := InitMLSRelayTx(ctx, c, tx, chat, id.GroupID, id.GroupContext, uint64(id.Epoch), []MLSRelayMember{{DeviceID: device, LeafIndex: 0}, {DeviceID: human, LeafIndex: 1}}); e != nil {
			return e
		}
		return StoreMLSWelcomeTx(ctx, c, tx, chat, human, uint64(id.Epoch), id.Welcome)
	}
	aiOpenMLS = func(c *core.Core, chat string, state, wire, aad []byte) ([]byte, []byte, error) {
		p, e := cryptoenc.RestoreMLS(c.Engine, state, []byte("ai/state/"+chat))
		if e != nil {
			return nil, nil, e
		}
		plain, e := p.Decrypt(wire, aad)
		if e != nil {
			return nil, nil, e
		}
		snapshot, e := p.Snapshot(c.Engine, []byte("ai/state/"+chat))
		return plain, snapshot, e
	}
	aiSealMLS = func(c *core.Core, chat string, state, plain, aad []byte) ([]byte, []byte, error) {
		p, e := cryptoenc.RestoreMLS(c.Engine, state, []byte("ai/state/"+chat))
		if e != nil {
			return nil, nil, e
		}
		wire, e := p.Encrypt(plain, aad)
		if e != nil {
			return nil, nil, e
		}
		snapshot, e := p.Snapshot(c.Engine, []byte("ai/state/"+chat))
		return wire, snapshot, e
	}
}
