//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signedTestGrant(t *testing.T, priv ed25519.PrivateKey, now time.Time, extra jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"request_id":       "request",
		"chat":             "chat",
		"source_peer":      "peer",
		"target_endpoint":  "endpoint",
		"action":           "invoke",
		"name":             "model",
		"body_hash":        "body-hash",
		"destination_hash": "destination-hash",
		"epoch":            float64(4),
		"group_id":         "group-id",
		"group_context":    "group-context",
		"nonce":            "nonce",
		"iat":              float64(now.Unix()),
		"exp":              float64(now.Add(5 * time.Minute).Unix()),
		"iss":              "issuer",
		"aud":              "audience",
	}
	for k, v := range extra {
		claims[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	raw, err := token.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRPCRequestHashBindsImmutableProjection(t *testing.T) {
	base := RPCRequest{Version: 1, RequestID: "r1", ClientID: "c", Chat: "chat", SourcePeer: "peer", Action: "invoke", Name: "model", Body: []byte(`{"x":1}`)}
	h, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]RPCRequest{
		"request_id": func() RPCRequest { x := base; x.RequestID = "r2"; return x }(),
		"chat":       func() RPCRequest { x := base; x.Chat = "other"; return x }(),
		"source":     func() RPCRequest { x := base; x.SourcePeer = "other"; return x }(),
		"action":     func() RPCRequest { x := base; x.Action = "read"; return x }(),
		"name":       func() RPCRequest { x := base; x.Name = "other"; return x }(),
	} {
		got, err := changed.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) == string(h) {
			t.Fatalf("%s was not bound into request hash", name)
		}
	}
}

func TestVerifyGrantStrictClaimsAndEpochBinding(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	expect := GrantExpectation{RequestID: "request", Chat: "chat", SourcePeer: "peer", TargetEndpoint: "endpoint", Action: "invoke", Name: "model", BodyHash: "body-hash", DestinationHash: "destination-hash", Epoch: 4, GroupID: "group-id", GroupContext: "group-context", Issuer: "issuer", Audience: "audience"}
	valid := signedTestGrant(t, priv, now, nil)
	if _, _, err = VerifyGrant(valid, pub, expect, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = VerifyGrant(signedTestGrant(t, priv, now, jwt.MapClaims{"unexpected": "field"}), pub, expect, now); err == nil {
		t.Fatal("accepted unknown grant claim")
	}
	if _, _, err = VerifyGrant(signedTestGrant(t, priv, now, jwt.MapClaims{"epoch": float64(3)}), pub, expect, now); err == nil {
		t.Fatal("accepted grant for another MLS epoch")
	}
	long := jwt.MapClaims{"exp": float64(now.Add(901 * time.Second).Unix())}
	if _, _, err = VerifyGrant(signedTestGrant(t, priv, now, long), pub, expect, now); err == nil {
		t.Fatal("accepted grant lifetime over 900 seconds")
	}
	if _, _, err = VerifyGrant(valid, pub, GrantExpectation{RequestID: expect.RequestID, Chat: expect.Chat, SourcePeer: expect.SourcePeer, TargetEndpoint: expect.TargetEndpoint, Action: expect.Action, Name: expect.Name, BodyHash: expect.BodyHash, DestinationHash: expect.DestinationHash, Epoch: 5, GroupID: expect.GroupID, GroupContext: expect.GroupContext, Issuer: expect.Issuer, Audience: expect.Audience}, now); err == nil {
		t.Fatal("accepted grant after epoch change")
	}
}
