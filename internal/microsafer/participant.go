//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mgg789/QGramm/internal/cryptoenc"
	"github.com/thomas-vilte/mls-go/group"
)

type Participant struct {
	mu     sync.Mutex
	MLS    *cryptoenc.MLSParticipant
	Store  *Store
	Chat   string
	User   string
	Device string
}

// New creates a separately registered device identity.  The MLS basic
// credential contains the immutable device ID; account/user binding is
// checked by the relay and the configured peer roster.
func New(user, device string) (*Participant, error) {
	if user == "" || device == "" {
		return nil, errors.New("microsafer: user and device are required")
	}
	mls, err := cryptoenc.NewMLS([]byte(device))
	if err != nil {
		return nil, err
	}
	return &Participant{MLS: mls, User: user, Device: device}, nil
}

func (p *Participant) attach(s *Store, chat string) { p.Store = s; p.Chat = chat }
func (p *Participant) snapshotAAD() []byte {
	return []byte("microsafer/mls/" + p.User + "/" + p.Device + "/" + p.Chat + "/v1")
}
func (p *Participant) Snapshot() ([]byte, error) {
	if p == nil || p.MLS == nil || p.Store == nil {
		return nil, errors.New("microsafer: participant is not attached")
	}
	return p.MLS.Snapshot(p.Store.Engine(), p.snapshotAAD())
}
func (p *Participant) save(ctx context.Context) error {
	b, err := p.Snapshot()
	if err != nil {
		return err
	}
	return p.Store.PutState(ctx, "mls/"+p.Chat, b)
}

func loadParticipant(ctx context.Context, s *Store, c Config, chat string) (*Participant, error) {
	var blob []byte
	err := s.GetState(ctx, "mls/"+chat, &blob)
	if err != nil {
		return nil, err
	}
	p, err := New(c.Endpoint.User, c.Endpoint.Device)
	if err != nil {
		return nil, err
	}
	p.attach(s, chat)
	p.MLS, err = cryptoenc.RestoreMLS(s.Engine(), blob, p.snapshotAAD())
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Bootstrap creates and durably stores a pending key package.  The returned
// values are public provisioning material; private MLS state stays encrypted
// in the local database.
type BootstrapOutput struct {
	User             string `json:"user"`
	Device           string `json:"device"`
	KeyPackage       string `json:"key_package"`
	SigningPublicKey string `json:"signing_public_key"`
	GroupID          string `json:"group_id,omitempty"`
}

func Bootstrap(ctx context.Context, c Config, chat string) (BootstrapOutput, error) {
	s, err := OpenStore(c)
	if err != nil {
		return BootstrapOutput{}, err
	}
	defer s.Close()
	if chat == "" {
		chat = "default"
	}
	var existing []byte
	if err = s.GetState(ctx, "mls/"+chat, &existing); err == nil {
		return BootstrapOutput{}, errors.New("microsafer: state already exists; refusing identity reset")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return BootstrapOutput{}, err
	}
	p, err := New(c.Endpoint.User, c.Endpoint.Device)
	if err != nil {
		return BootstrapOutput{}, err
	}
	p.attach(s, chat)
	if err = p.save(ctx); err != nil {
		return BootstrapOutput{}, err
	}
	return p.public(), nil
}
func (p *Participant) public() BootstrapOutput {
	out := BootstrapOutput{User: p.User, Device: p.Device, KeyPackage: base64.StdEncoding.EncodeToString(p.MLS.KeyPackage()), SigningPublicKey: base64.StdEncoding.EncodeToString(p.MLS.SigningPublicKey())}
	if id := p.MLS.GroupID(); len(id) > 0 {
		out.GroupID = base64.StdEncoding.EncodeToString(id)
	}
	return out
}

// Join consumes one externally supplied Welcome.  Group ID/context/epoch are
// compared to the offline pinned peer configuration before the new state is
// committed.  Membership changes and automatic rekeys are intentionally not
// supported by this first release: a changed epoch must be rejoined offline.
func JoinWelcome(ctx context.Context, c Config, chat string, welcome []byte) (BootstrapOutput, error) {
	if len(welcome) == 0 {
		return BootstrapOutput{}, errors.New("microsafer: empty MLS Welcome")
	}
	peer, ok := c.peerForChat(chat)
	if !ok {
		return BootstrapOutput{}, fmt.Errorf("microsafer: no pinned peer for chat %q", chat)
	}
	s, err := OpenStore(c)
	if err != nil {
		return BootstrapOutput{}, err
	}
	defer s.Close()
	p, err := loadParticipant(ctx, s, c, chat)
	if errors.Is(err, sql.ErrNoRows) {
		p, err = New(c.Endpoint.User, c.Endpoint.Device)
		if err == nil {
			p.attach(s, chat)
		}
	}
	if err != nil {
		return BootstrapOutput{}, err
	}
	if _, err = p.MLS.Join(welcome); err != nil {
		return BootstrapOutput{}, err
	}
	if err = verifyPins(p.MLS, peer); err != nil {
		return BootstrapOutput{}, err
	}
	if err = verifyRoster(p, peer); err != nil {
		return BootstrapOutput{}, err
	}
	if err = p.save(ctx); err != nil {
		return BootstrapOutput{}, err
	}
	return p.public(), nil
}
func verifyPins(m *cryptoenc.MLSParticipant, pin PeerConfig) error {
	gid, err := decodePinned(pin.GroupID)
	if err != nil || !bytes.Equal(gid, m.GroupID()) {
		return errors.New("microsafer: MLS group ID does not match offline pin")
	}
	if pin.GroupContext != "" {
		want, e := decodePinned(pin.GroupContext)
		if e != nil || !bytes.Equal(want, m.GroupContext()) {
			return errors.New("microsafer: MLS group context does not match offline pin")
		}
	}
	if pin.Epoch != 0 && pin.Epoch != m.Epoch() {
		return errors.New("microsafer: MLS epoch does not match offline pin")
	}
	return nil
}

func verifyRoster(p *Participant, pin PeerConfig) error {
	want, err := decodePinned(pin.SigningKey)
	if err != nil || len(want) != 32 {
		return errors.New("microsafer: invalid peer signing key pin")
	}
	blob, err := p.Snapshot()
	if err != nil {
		return err
	}
	plain, err := p.Store.Engine().Open(blob, p.snapshotAAD())
	if err != nil {
		return err
	}
	var raw struct {
		Group []byte `json:"Group"`
	}
	if err = json.Unmarshal(plain, &raw); err != nil || len(raw.Group) == 0 {
		return errors.New("microsafer: MLS roster is unavailable")
	}
	g, err := group.UnmarshalGroupState(raw.Group)
	if err != nil {
		return err
	}
	if g.MemberCount() != 2 {
		return errors.New("microsafer: only pinned two-member MLS chats are supported")
	}
	if _, ok := g.FindMemberBySigKey(want); !ok {
		return errors.New("microsafer: peer signing key is absent from MLS roster")
	}
	return nil
}
func decodeGroupPin(s string) ([]byte, error) {
	if b, e := base64.StdEncoding.Strict().DecodeString(s); e == nil {
		return b, nil
	}
	return hex.DecodeString(s)
}

func (p *Participant) Encrypt(ctx context.Context, chat, sender, operation string, plain []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if chat != p.Chat || sender != p.User {
		return nil, errors.New("microsafer: message identity mismatch")
	}
	wire, err := p.MLS.Encrypt(plain, cryptoenc.Binding(chat, sender, p.Device, operation))
	if err != nil {
		return nil, err
	}
	if err = p.save(ctx); err != nil {
		return nil, fmt.Errorf("microsafer: snapshot before release: %w", err)
	}
	return wire, nil
}
func (p *Participant) Decrypt(ctx context.Context, chat, sender, device, operation string, wire []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if chat != p.Chat {
		return nil, errors.New("microsafer: chat mismatch")
	}
	pt, err := p.MLS.Decrypt(wire, cryptoenc.Binding(chat, sender, device, operation))
	if err != nil {
		return nil, err
	}
	if err = p.save(ctx); err != nil {
		return nil, fmt.Errorf("microsafer: snapshot after decrypt: %w", err)
	}
	return pt, nil
}

// decryptForInbox advances MLS and returns its encrypted snapshot without
// committing it separately. The caller must commit snapshot and inbox intent
// in one SQLite transaction before processing the external effect.
func (p *Participant) decryptForInbox(chat, sender, device, operation string, wire []byte) ([]byte, []byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if chat != p.Chat {
		return nil, nil, errors.New("microsafer: chat mismatch")
	}
	pt, err := p.MLS.Decrypt(wire, cryptoenc.Binding(chat, sender, device, operation))
	if err != nil {
		return nil, nil, err
	}
	snap, err := p.MLS.Snapshot(p.Store.Engine(), p.snapshotAAD())
	if err != nil {
		return nil, nil, err
	}
	return pt, snap, nil
}
