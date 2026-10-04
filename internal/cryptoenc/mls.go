//go:build qg_e2ee

package cryptoenc

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"

	"encoding/json"
	"errors"
	"sync"

	"github.com/thomas-vilte/mls-go/ciphersuite"
	"github.com/thomas-vilte/mls-go/credentials"
	"github.com/thomas-vilte/mls-go/framing"
	"github.com/thomas-vilte/mls-go/group"
	"github.com/thomas-vilte/mls-go/keypackages"
	"github.com/thomas-vilte/mls-go/treesync"
)

// MLSParticipant is a single device in one MLS group, using RFC 9420 suite 1.
// Applications MUST serialize access, persist Snapshot after every successful
// mutation before releasing ciphertext or acknowledging input, and prevent
// rollback or concurrent restores of snapshots. Restoring old send counters
// can reuse a nonce. An identity is a credential claim; membership admission
// must separately authenticate the identity/signing key against account policy.
type MLSParticipant struct {
	mu   sync.Mutex
	kp   *keypackages.KeyPackage
	keys *keypackages.KeyPackagePrivateKeys
	g    *group.Group
	seen map[string]bool
}

func NewMLS(identity []byte) (*MLSParticipant, error) {
	if len(identity) == 0 {
		return nil, errors.New("cryptoenc: MLS identity is empty")
	}
	cred, _, err := credentials.GenerateCredentialWithKeyForCS(identity, ciphersuite.MLS128DHKEMX25519)
	if err != nil {
		return nil, err
	}
	kp, keys, err := keypackages.Generate(cred, ciphersuite.MLS128DHKEMX25519)
	if err != nil {
		return nil, err
	}
	return &MLSParticipant{kp: kp, keys: keys, seen: map[string]bool{}}, nil
}
func (p *MLSParticipant) KeyPackage() []byte { p.mu.Lock(); defer p.mu.Unlock(); return p.kp.Marshal() }
func (p *MLSParticipant) SigningPublicKey() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return bytes.Clone(p.keys.Ed25519SignatureKey.Public().(ed25519.PublicKey))
}
func (p *MLSParticipant) GroupID() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return nil
	}
	return bytes.Clone(p.g.GroupID().AsSlice())
}
func (p *MLSParticipant) Epoch() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return 0
	}
	return p.g.Epoch().AsUint64()
}
func (p *MLSParticipant) GroupContext() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return nil
	}
	return p.g.GroupContext().Marshal()
}

// KeyPackageIdentity verifies suite1 KeyPackage structure/signature and returns
// its raw basic-credential identity and Ed25519 signing key. Compare BOTH with
// the externally registered device before allowing group admission.
func KeyPackageIdentity(wire []byte) (identity, signingPub []byte, err error) {
	kp, err := keypackages.UnmarshalKeyPackage(wire)
	if err != nil {
		return nil, nil, err
	}
	if kp.CipherSuite != ciphersuite.MLS128DHKEMX25519 {
		return nil, nil, errors.New("cryptoenc: MLS suite1 required")
	}
	if !bytes.Equal(kp.Marshal(), wire) {
		return nil, nil, errors.New("cryptoenc: noncanonical KeyPackage wire")
	}
	if err = kp.Validate(); err != nil {
		return nil, nil, err
	}
	if err = kp.Verify(kp.CipherSuite); err != nil {
		return nil, nil, err
	}
	leaf, err := treesync.UnmarshalLeafNodeData(kp.LeafNode.Marshal())
	if err != nil {
		return nil, nil, err
	}
	if err = treesync.ValidateLeafNodeWithContext(leaf, kp.CipherSuite, nil, 0); err != nil {
		return nil, nil, err
	}
	if kp.LeafNode.Credential.CredentialType != credentials.BasicCredential {
		return nil, nil, errors.New("cryptoenc: basic device credential required")
	}
	signingPub = kp.LeafNode.SignatureKeyBytes
	if len(signingPub) != ed25519.PublicKeySize || len(kp.InitKey) != 32 || len(kp.LeafNode.EncryptionKey) != 32 {
		return nil, nil, errors.New("cryptoenc: invalid suite1 key lengths")
	}
	return bytes.Clone(kp.LeafNode.Credential.Identity), bytes.Clone(signingPub), nil
}
func (p *MLSParticipant) CreateGroup() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g != nil {
		return nil, errors.New("cryptoenc: already joined")
	}
	id, err := group.NewGroupIDRandom()
	if err != nil {
		return nil, err
	}
	p.g, err = group.NewGroup(id, ciphersuite.MLS128DHKEMX25519, p.kp, p.keys)
	if err != nil {
		return nil, err
	}
	return id.AsSlice(), nil
}
func (p *MLSParticipant) Join(wire []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g != nil {
		return nil, errors.New("cryptoenc: already joined")
	}
	msg, err := framing.UnmarshalMLSMessage(wire)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(msg.Marshal(), wire) {
		return nil, errors.New("cryptoenc: noncanonical MLS wire")
	}
	if len(msg.Welcome) == 0 {
		return nil, errors.New("cryptoenc: expected MLS Welcome")
	}
	welcome, err := group.UnmarshalWelcome(msg.Welcome)
	if err != nil {
		return nil, err
	}
	p.g, err = group.JoinFromWelcome(welcome, p.kp, p.keys, nil)
	if err != nil {
		return nil, err
	}
	return p.g.GroupID().AsSlice(), nil
}
func (p *MLSParticipant) Invite(kpBytes []byte) (commit, welcome []byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return nil, nil, errors.New("cryptoenc: not joined")
	}
	kp, err := keypackages.UnmarshalKeyPackage(kpBytes)
	if err != nil {
		return nil, nil, err
	}
	if _, err = p.g.AddMember(kp); err != nil {
		return nil, nil, err
	}
	signer := p.keys.GetSignaturePrivateKey()
	staged, err := p.g.Commit(signer, signer.PublicKey(), nil)
	if err != nil {
		return nil, nil, err
	}
	joiner := staged.JoinerSecret()
	if err = p.g.MergeCommit(staged); err != nil {
		return nil, nil, err
	}
	wm, err := p.g.CreateWelcomeWithOpts([]*keypackages.KeyPackage{kp}, signer, group.WithJoinerSecret(joiner), group.WithPSKIDs(staged.PskIDs()), group.WithPSKSecret(staged.RawPskSecret()), group.WithStagedCommit(staged))
	if err != nil {
		return nil, nil, err
	}
	cm := framing.NewMLSMessagePublic(&framing.PublicMessage{Content: staged.AuthenticatedContent().Content, Auth: staged.AuthenticatedContent().Auth, MembershipTag: staged.MembershipTag()})
	wrapper := framing.MLSMessage{Welcome: wm.Marshal()}
	p.seen = map[string]bool{}
	return cm.Marshal(), wrapper.Marshal(), nil
}
func (p *MLSParticipant) Encrypt(plain, aad []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return nil, errors.New("cryptoenc: not joined")
	}
	pm, err := p.g.SendMessage(plain, p.keys.GetSignaturePrivateKey(), group.WithAAD(aad))
	if err != nil {
		return nil, err
	}
	return framing.NewMLSMessagePrivate(pm).Marshal(), nil
}
func (p *MLSParticipant) Decrypt(wire, expectedAAD []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return nil, errors.New("cryptoenc: not joined")
	}
	msg, err := framing.UnmarshalMLSMessage(wire)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(msg.Marshal(), wire) {
		return nil, errors.New("cryptoenc: noncanonical MLS wire")
	}
	pm, ok := msg.AsPrivate()
	if !ok {
		return nil, errors.New("cryptoenc: expected MLS PrivateMessage")
	}
	if pm.Epoch != p.g.Epoch().AsUint64() {
		return nil, errors.New("cryptoenc: current MLS epoch required")
	}
	sum := sha256.Sum256(wire)
	hash := hex.EncodeToString(sum[:])
	if p.seen[hash] {
		return nil, errors.New("cryptoenc: replayed MLS application")
	}
	if len(p.seen) >= 65536 {
		return nil, errors.New("cryptoenc: MLS epoch replay ledger full; rekey required")
	}
	// Check public authenticated_data before consuming ratchet state.
	if !bytes.Equal(pm.AuthenticatedData, expectedAAD) {
		return nil, errors.New("cryptoenc: MLS binding mismatch")
	}
	pt, aad, _, err := p.g.ReceiveApplicationMessage(pm)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(aad, expectedAAD) {
		return nil, errors.New("cryptoenc: MLS binding mismatch")
	}
	p.seen[hash] = true
	return pt, nil
}
func (p *MLSParticipant) ProcessCommit(wire []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.g == nil {
		return errors.New("cryptoenc: not joined")
	}
	msg, err := framing.UnmarshalMLSMessage(wire)
	if err != nil {
		return err
	}
	if !bytes.Equal(msg.Marshal(), wire) {
		return errors.New("cryptoenc: noncanonical MLS wire")
	}
	pm, ok := msg.AsPublic()
	if !ok {
		return errors.New("cryptoenc: expected public MLS commit")
	}
	if err = pm.VerifyMembershipTagWithContext(p.g.CipherSuite(), p.g.EpochSecrets().MembershipKey, p.g.GroupContext().Marshal()); err != nil {
		return err
	}
	ac := &framing.AuthenticatedContent{WireFormat: framing.WireFormatPublicMessage, Content: pm.Content, Auth: pm.Auth, GroupContext: p.g.GroupContext().Marshal()}
	if err = p.g.ProcessReceivedCommit(ac, treesync.LeafIndex(pm.Content.Sender.LeafIndex), p.g.MyLeafEncryptionKey()); err != nil {
		return err
	}
	p.seen = map[string]bool{}
	return nil
}

type mlsSnapshot struct {
	Version                              int `json:"version"`
	KP, Signing, Init, Encryption, Group []byte
	Seen                                 []string
}

// Snapshot contains all secrets and ratchet state encrypted at rest. The caller
// must atomically replace durable state and commit the associated outbound job.
func (p *MLSParticipant) Snapshot(engine *Engine, aad []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	signing := bytes.Clone(p.keys.Ed25519SignatureKey)
	var err error
	s := mlsSnapshot{Version: 2, KP: p.kp.Marshal(), Signing: signing, Init: p.keys.InitKey.Bytes(), Encryption: p.keys.EncryptionKey.Bytes()}
	for hash := range p.seen {
		s.Seen = append(s.Seen, hash)
	}
	if p.g != nil {
		s.Group, err = p.g.MarshalState()
		if err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return engine.Seal(data, aad)
}
func RestoreMLS(engine *Engine, blob, aad []byte) (*MLSParticipant, error) {
	data, err := engine.Open(blob, aad)
	if err != nil {
		return nil, err
	}
	var s mlsSnapshot
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Version != 2 {
		return nil, errors.New("cryptoenc: unsupported MLS snapshot")
	}
	if len(s.Signing) != ed25519.PrivateKeySize {
		return nil, errors.New("cryptoenc: invalid MLS signing key")
	}
	signing := ed25519.PrivateKey(bytes.Clone(s.Signing))
	init, err := ecdh.X25519().NewPrivateKey(s.Init)
	if err != nil {
		return nil, err
	}
	enc, err := ecdh.X25519().NewPrivateKey(s.Encryption)
	if err != nil {
		return nil, err
	}
	kp, err := keypackages.UnmarshalKeyPackage(s.KP)
	if err != nil {
		return nil, err
	}
	if len(s.Seen) > 65536 {
		return nil, errors.New("cryptoenc: invalid replay ledger")
	}
	seen := map[string]bool{}
	for _, hash := range s.Seen {
		b, e := hex.DecodeString(hash)
		if e != nil || len(b) != 32 || seen[hash] {
			return nil, errors.New("cryptoenc: invalid replay ledger")
		}
		seen[hash] = true
	}
	p := &MLSParticipant{kp: kp, keys: &keypackages.KeyPackagePrivateKeys{InitKey: init, EncryptionKey: enc, Ed25519SignatureKey: signing}, seen: seen}
	if len(s.Group) > 0 {
		p.g, err = group.UnmarshalGroupState(s.Group)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}
