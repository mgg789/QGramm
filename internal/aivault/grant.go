//go:build qg_ai_storage

package aivault

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type Action string

const (
	ActionRead   Action = "read"
	ActionSearch Action = "search"
	ActionGraph  Action = "graph"
)

type Claims struct {
	Nonce       string `json:"nonce"`
	RequestID   string `json:"request_id"`
	RequestHash string `json:"request_hash"`
	ChatID      string `json:"chat_id"`
	SourceUser  string `json:"source_user"`
	AIUser      string `json:"ai_user"`
	Resource    string `json:"resource"`
	Action      Action `json:"action"`
	Destination string `json:"destination"`
	Issuer      string `json:"iss"`
	Audience    string `json:"aud"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

// Grant is a stateless verifier. Core remains responsible for job validation,
// single-use consumption, revocation, and checking request/hash ownership.
type Grant struct{}

// Verify checks the exact Ed25519 JWT contract. ttl is the maximum allowed
// lifetime; issuer and audience are compared byte-for-byte.
func (Grant) Verify(publicKey ed25519.PublicKey, issuer, audience string, ttl time.Duration, token string) (Claims, error) {
	return Verify(publicKey, issuer, audience, ttl, token)
}

func Verify(publicKey ed25519.PublicKey, issuer, audience string, ttl time.Duration, token string) (Claims, error) {
	if len(publicKey) != ed25519.PublicKeySize || ttl <= 0 {
		return Claims{}, errors.New("aivault: invalid grant verifier")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, errors.New("aivault: invalid grant")
	}
	header, err := decodeObject(parts[0])
	if err != nil {
		return Claims{}, err
	}
	if len(header) != 2 || string(header["alg"]) != `"EdDSA"` || string(header["typ"]) != `"JWT"` {
		return Claims{}, errors.New("aivault: invalid grant header")
	}
	raw, err := decodeObject(parts[1])
	if err != nil {
		return Claims{}, err
	}
	allowed := map[string]bool{"nonce": true, "request_id": true, "request_hash": true, "chat_id": true, "source_user": true, "ai_user": true, "resource": true, "action": true, "destination": true, "iss": true, "aud": true, "iat": true, "exp": true}
	for name := range raw {
		if !allowed[name] {
			return Claims{}, errors.New("aivault: unknown grant claim")
		}
	}
	var claims Claims
	if err := decodeStrict(parts[1], &claims); err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(publicKey, []byte(parts[0]+"."+parts[1]), sig) {
		return Claims{}, errors.New("aivault: invalid grant signature")
	}
	now := time.Now().Unix()
	maxSeconds := ttl / time.Second
	delta := claims.ExpiresAt - claims.IssuedAt
	if maxSeconds <= 0 || claims.Issuer != issuer || claims.Audience != audience || claims.IssuedAt <= 0 || delta <= 0 || claims.IssuedAt > now || claims.ExpiresAt <= now || delta > int64(maxSeconds) {
		return Claims{}, errors.New("aivault: invalid grant claims")
	}
	for _, value := range []string{claims.Nonce, claims.RequestID, claims.RequestHash, claims.ChatID, claims.SourceUser, claims.AIUser, claims.Resource, claims.Destination, claims.Issuer, claims.Audience} {
		if !grantField(value) {
			return Claims{}, errors.New("aivault: invalid grant claims")
		}
	}
	if claims.Action != ActionRead && claims.Action != ActionSearch && claims.Action != ActionGraph {
		return Claims{}, errors.New("aivault: invalid grant action")
	}
	return claims, nil
}

type GrantSigner struct {
	PrivateKey ed25519.PrivateKey
	Issuer     string
	Audience   string
	MaxTTL     time.Duration
}

func NewGrantSigner(privateKey ed25519.PrivateKey, issuer, audience string, maxTTL time.Duration) (GrantSigner, error) {
	if len(privateKey) != ed25519.PrivateKeySize || issuer == "" || audience == "" || maxTTL <= 0 {
		return GrantSigner{}, errors.New("aivault: invalid grant signer")
	}
	return GrantSigner{PrivateKey: append(ed25519.PrivateKey(nil), privateKey...), Issuer: issuer, Audience: audience, MaxTTL: maxTTL}, nil
}

func (s GrantSigner) Sign(claims Claims) (string, error) {
	if len(s.PrivateKey) != ed25519.PrivateKeySize {
		return "", errors.New("aivault: invalid grant signer")
	}
	claims.Issuer = s.Issuer
	claims.Audience = s.Audience
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = claims.IssuedAt + int64(s.MaxTTL/time.Second)
	}
	if err := validateClaimsForSigning(claims, s.MaxTTL); err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	input := header + "." + payload
	sig := ed25519.Sign(s.PrivateKey, []byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c Claims) Authorize(req RetrievalRequest) error {
	if c.Action != req.Action || c.Resource != req.ResourceID || c.Destination != req.Destination {
		return errors.New("aivault: grant request mismatch")
	}
	return nil
}

type RetrievalRequest struct {
	Action      Action
	ResourceID  string
	Destination string
}

func validateClaimsForSigning(c Claims, ttl time.Duration) error {
	for _, v := range []string{c.Nonce, c.RequestID, c.RequestHash, c.ChatID, c.SourceUser, c.AIUser, c.Resource, c.Destination, c.Issuer, c.Audience} {
		if !grantField(v) {
			return errors.New("aivault: invalid grant claims")
		}
	}
	if c.Action != ActionRead && c.Action != ActionSearch && c.Action != ActionGraph {
		return errors.New("aivault: invalid grant action")
	}
	if c.IssuedAt <= 0 || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt <= 0 || time.Duration(c.ExpiresAt-c.IssuedAt)*time.Second > ttl {
		return errors.New("aivault: invalid grant lifetime")
	}
	return nil
}
func grantField(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	return !strings.ContainsAny(v, "\x00\r\n")
}

func decodeObject(segment string) (map[string]json.RawMessage, error) {
	b, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, errors.New("aivault: invalid grant encoding")
	}
	if err = rejectDuplicateKeys(b); err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err = json.Unmarshal(b, &out); err != nil || out == nil {
		return nil, errors.New("aivault: invalid grant JSON")
	}
	return out, nil
}
func decodeStrict(segment string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return errors.New("aivault: invalid grant encoding")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return errors.New("aivault: trailing grant JSON")
	}
	return nil
}
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("aivault: grant object required")
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return errors.New("aivault: invalid grant key")
		}
		if seen[name] {
			return errors.New("aivault: duplicate grant claim")
		}
		seen[name] = true
		if err = skipJSON(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}
func skipJSON(dec *json.Decoder) error { var x any; return dec.Decode(&x) }
