// Package cryptoenc provides RFC 9180 base-mode HPKE envelopes and authenticated
// encryption at rest. HPKE base mode does not authenticate the sender: callers
// must authenticate requests and authorize the supplied binding independently.
package cryptoenc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
)

const hpkeInfo = "qgramm/basic/v1"

var suite = hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_AES128GCM)

type Envelope struct {
	KeyID      string `json:"key_id"`
	Enc        string `json:"enc"`
	Ciphertext string `json:"ciphertext"`
}

type Engine struct {
	aead         cipher.AEAD
	private      kem.PrivateKey
	public       []byte
	id           string
	previousAEAD []cipher.AEAD
	previousHPKE map[string]kem.PrivateKey
}

// New requires independently generated 32-byte AES and X25519 private keys.
// Keys must be stable across restarts and supplied by the application's secret store.
func New(master, hpkePrivate []byte) (*Engine, error) {
	return NewWithPrevious(master, hpkePrivate, nil, nil)
}

// NewWithPrevious accepts at most four retired keys of each kind. Encryption
// always uses the primary keys; retired keys only decrypt existing data.
// Removing a retired key immediately removes its decryption capability.
func NewWithPrevious(master, hpkePrivate []byte, previousMasters, previousHPKE [][]byte) (*Engine, error) {
	if len(previousMasters) > 4 || len(previousHPKE) > 4 {
		return nil, errors.New("cryptoenc: at most four retired keys of each kind allowed")
	}
	if len(master) != 32 || len(hpkePrivate) != 32 {
		return nil, errors.New("cryptoenc: keys must each contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	private, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPrivateKey(hpkePrivate)
	if err != nil {
		return nil, fmt.Errorf("cryptoenc: invalid HPKE key: %w", err)
	}
	public, err := private.Public().MarshalBinary()
	if err != nil {
		return nil, err
	}
	e := &Engine{aead: aead, private: private, public: public, id: keyID(public), previousHPKE: make(map[string]kem.PrivateKey)}
	for _, key := range previousMasters {
		if len(key) != 32 {
			return nil, errors.New("cryptoenc: retired master key must contain exactly 32 bytes")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, err
		}
		e.previousAEAD = append(e.previousAEAD, aead)
	}
	for _, key := range previousHPKE {
		if len(key) != 32 {
			return nil, errors.New("cryptoenc: retired HPKE key must contain exactly 32 bytes")
		}
		private, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("cryptoenc: invalid retired HPKE key: %w", err)
		}
		public, err := private.Public().MarshalBinary()
		if err != nil {
			return nil, err
		}
		e.previousHPKE[keyID(public)] = private
	}
	return e, nil
}
func keyID(public []byte) string    { sum := sha256.Sum256(public); return hex.EncodeToString(sum[:]) }
func (e *Engine) PublicKey() string { return base64.StdEncoding.EncodeToString(e.public) }
func (e *Engine) KeyID() string     { return e.id }

// Seal uses Go's random-nonce AES-256-GCM. Frame: version byte (1), nonce,
// ciphertext, tag. The version is also authenticated to prevent downgrade.
// A single key must encrypt fewer than 2^32 records over its entire lifetime.
func (e *Engine) Seal(plain, aad []byte) ([]byte, error) {
	frame := []byte{1}
	return e.aead.Seal(frame, nil, plain, storageAAD(aad)), nil
}
func (e *Engine) Open(frame, aad []byte) ([]byte, error) {
	if len(frame) < 1+e.aead.Overhead() || frame[0] != 1 {
		return nil, errors.New("cryptoenc: invalid encrypted frame")
	}
	bound := storageAAD(aad)
	plain, err := e.aead.Open(nil, nil, frame[1:], bound)
	if err == nil {
		return plain, nil
	}
	for _, retired := range e.previousAEAD {
		plain, retiredErr := retired.Open(nil, nil, frame[1:], bound)
		if retiredErr == nil {
			return plain, nil
		}
	}
	return nil, err
}
func storageAAD(aad []byte) []byte {
	out := make([]byte, 1+len(aad))
	out[0] = 1
	copy(out[1:], aad)
	return out
}

func SealEnvelope(publicKey, plaintext, binding []byte) (Envelope, error) {
	recipient, err := ParseRecipient(publicKey)
	if err != nil {
		return Envelope{}, err
	}
	return recipient.SealEnvelope(plaintext, binding)
}

// Recipient contains only the parsed public key and its identifier. Reuse it
// within one authorized projection batch; SealEnvelope creates a fresh HPKE
// sender and encapsulation for every message.
type Recipient struct {
	public kem.PublicKey
	id     string
}

func ParseRecipient(publicKey []byte) (Recipient, error) {
	public, err := hpke.KEM_X25519_HKDF_SHA256.Scheme().UnmarshalBinaryPublicKey(publicKey)
	if err != nil {
		return Recipient{}, err
	}
	return Recipient{public: public, id: keyID(publicKey)}, nil
}

func (r *Recipient) SealEnvelope(plaintext, binding []byte) (Envelope, error) {
	sender, err := suite.NewSender(r.public, []byte(hpkeInfo))
	if err != nil {
		return Envelope{}, err
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return Envelope{}, err
	}
	ct, err := sealer.Seal(plaintext, binding)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{KeyID: r.id, Enc: base64.StdEncoding.EncodeToString(enc), Ciphertext: base64.StdEncoding.EncodeToString(ct)}, nil
}
func (e *Engine) OpenEnvelope(env Envelope, binding []byte) ([]byte, error) {
	private := e.private
	if env.KeyID != e.id {
		private = e.previousHPKE[env.KeyID]
	}
	if private == nil {
		return nil, errors.New("cryptoenc: unknown envelope key")
	}
	enc, err := base64.StdEncoding.Strict().DecodeString(env.Enc)
	if err != nil {
		return nil, errors.New("cryptoenc: invalid encapsulated key encoding")
	}
	ct, err := base64.StdEncoding.Strict().DecodeString(env.Ciphertext)
	if err != nil {
		return nil, errors.New("cryptoenc: invalid ciphertext encoding")
	}
	receiver, err := suite.NewReceiver(private, []byte(hpkeInfo))
	if err != nil {
		return nil, err
	}
	opener, err := receiver.Setup(enc)
	if err != nil {
		return nil, err
	}
	return opener.Open(ct, binding)
}

// Binding is canonical UTF-8 JSON, in exactly the field order below. Clients
// must use the same JSON escaping rules (including HTML escaping).
func Binding(chat, user, device, operation string) []byte {
	out, _ := json.Marshal(struct {
		Version   int    `json:"version"`
		Chat      string `json:"chat"`
		User      string `json:"user"`
		Device    string `json:"device"`
		Operation string `json:"operation"`
	}{1, chat, user, device, operation})
	return out
}
