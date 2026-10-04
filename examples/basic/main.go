// Run from the repository root: go run ./examples/basic
// This local smoke uses real HTTP, WebSocket, SQLite and HPKE. It is not a SDK.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Backend only: authenticate the person and bind this device before minting.
func mint(private ed25519.PrivateKey, user, device string) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": "qgramm", "aud": "qgramm", "sub": user, "device_id": device,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}).SignedString(private)
}

type api struct {
	base      string
	http      *http.Client
	serverPin string // Independently supplied by the local harness, never by capabilities.
}

// body is already serialized: retain these exact bytes for operation retries.
func (a api) request(method, path, token string, body []byte, status int, out any) error {
	r, err := http.NewRequest(method, a.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	response, err := a.http.Do(r)
	if err != nil {
		return fmt.Errorf("%s %s: request failed", method, path)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		return fmt.Errorf("%s %s: status %d, expected %d", method, path, response.StatusCode, status)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
	}
	return nil
}

func randomKey() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// Smoke harness only. Real deployments run cmd/qgramm separately over HTTPS.
func localServer(public ed25519.PublicKey) (api, string, func(), error) {
	dir, err := os.MkdirTemp("", "qgramm-basic-")
	if err != nil {
		return api{}, "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	fail := func(err error) (api, string, func(), error) { cleanup(); return api{}, "", nil, err }
	cfg := config.Defaults()
	cfg.Server.AllowInsecureLoopback = true // Explicitly local test HTTP only.
	cfg.Storage.Path, cfg.Storage.Files = filepath.Join(dir, "qgramm.db"), filepath.Join(dir, "files")
	cfg.Capacity = config.DeriveCapacity(cfg.Capacity, config.DetectResources())
	// Dedicated process-local names avoid overwriting deployment environment keys.
	cfg.Security.TokenPublicKeyEnv, cfg.Security.ManagementSecretEnv = "QGRAMM_EXAMPLE_VERIFY", "QGRAMM_EXAMPLE_MANAGEMENT"
	cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv = "QGRAMM_EXAMPLE_MASTER", "QGRAMM_EXAMPLE_HPKE"
	values := map[string]string{cfg.Security.TokenPublicKeyEnv: base64.StdEncoding.EncodeToString(public)}
	for _, name := range []string{cfg.Security.ManagementSecretEnv, cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		key, e := randomKey()
		if e != nil {
			return fail(e)
		}
		values[name] = base64.StdEncoding.EncodeToString(key)
	}
	for name, value := range values {
		old, exists := os.LookupEnv(name)
		previousCleanup := cleanup
		cleanup = func() {
			if exists {
				_ = os.Setenv(name, old)
			} else {
				_ = os.Unsetenv(name)
			}
			previousCleanup()
		}
		if err := os.Setenv(name, value); err != nil {
			return fail(err)
		}
	}
	c, err := core.Open(cfg, nil)
	if err != nil {
		return fail(err)
	}
	server := httptest.NewServer(c.Handler())
	previousCleanup := cleanup
	cleanup = func() { server.Close(); _ = c.Close(); previousCleanup() }
	return api{server.URL, &http.Client{Timeout: 5 * time.Second}, c.Engine.PublicKey()}, values[cfg.Security.ManagementSecretEnv], cleanup, nil
}

func run() error {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	a, management, cleanup, err := localServer(public)
	if err != nil {
		return err
	}
	defer cleanup()
	engines := map[string]*cryptoenc.Engine{}
	tokens := map[string]string{}
	// Backend provisioning; private device keys belong to separate clients.
	for _, user := range []string{"alice", "bob"} {
		master, err := randomKey()
		if err != nil {
			return err
		}
		deviceKey, err := randomKey()
		if err != nil {
			return err
		}
		engines[user], err = cryptoenc.New(master, deviceKey)
		if err != nil {
			return err
		}
		if err = a.request("PUT", "/management/v1/users/"+user, management, []byte(`{"disabled":false}`), 200, nil); err != nil {
			return err
		}
		body, err := json.Marshal(map[string]string{"public_key": engines[user].PublicKey()})
		if err != nil {
			return err
		}
		if err = a.request("PUT", "/management/v1/users/"+user+"/devices/"+user+"-phone", management, body, 200, nil); err != nil {
			return err
		}
		tokens[user], err = mint(private, user, user+"-phone")
		if err != nil {
			return err
		}
	}
	if err = a.request("POST", "/management/v1/chats/direct", management, []byte(`{"id":"hello","mode":"basic","members":["alice","bob"]}`), 201, nil); err != nil {
		return err
	}
	// Bob client: exchange the short-lived JWT for a single-use socket ticket.
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err = a.request("POST", "/v1/ws-tickets", tokens["bob"], nil, 201, &ticket); err != nil {
		return err
	}
	conn, response, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).Dial(
		strings.Replace(a.base, "http://", "ws://", 1)+"/v1/ws?ticket="+url.QueryEscape(ticket.Ticket), nil)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return fmt.Errorf("WebSocket connection failed") // Do not log the ticket URL.
	}
	defer conn.Close()
	if err = conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err = conn.WriteJSON(map[string]any{"type": "subscribe", "chat_id": "hello", "after": 0}); err != nil {
		return err
	}
	// Alice client: pin the key obtained through an authenticated backend channel.
	var caps struct {
		Key string `json:"server_key"`
	}
	if err = a.request("GET", "/v1/capabilities", tokens["alice"], nil, 200, &caps); err != nil {
		return err
	}
	// The harness owns the server; production must supply this pin independently.
	if caps.Key != a.serverPin {
		return fmt.Errorf("server key pin mismatch")
	}
	key, err := base64.StdEncoding.DecodeString(caps.Key)
	if err != nil {
		return err
	}
	envelope, err := cryptoenc.SealEnvelope(key, []byte("Hello, Bob!"), cryptoenc.Binding("hello", "alice", "alice-phone", "hello-1"))
	if err != nil {
		return err
	}
	body, err := json.Marshal(core.MessageInput{OperationID: "hello-1", Envelope: &envelope})
	if err != nil {
		return err
	}
	var sent struct {
		Message core.Message `json:"message"`
	}
	if err = a.request("POST", "/v1/chats/hello/messages", tokens["alice"], body, 201, &sent); err != nil {
		return err
	}
	var retried struct {
		Message core.Message `json:"message"`
	}
	if err = a.request("POST", "/v1/chats/hello/messages", tokens["alice"], body, 201, &retried); err != nil {
		return err
	}
	if retried.Message.ID != sent.Message.ID {
		return fmt.Errorf("retry created a duplicate message")
	}
	// Bob client: events carry the current projection rewrapped for Bob's key.
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	var event struct {
		Type      string       `json:"type"`
		Seq       int64        `json:"seq"`
		MessageID string       `json:"message_id"`
		Data      core.Message `json:"data"`
	}
	if err = conn.ReadJSON(&event); err != nil {
		return err
	}
	if event.Type != "message.created" || event.MessageID != sent.Message.ID || event.Data.Envelope == nil {
		return fmt.Errorf("unexpected message event")
	}
	plain, err := engines["bob"].OpenEnvelope(*event.Data.Envelope, cryptoenc.Binding("hello", "bob", "bob-phone", sent.Message.ID))
	if err != nil {
		return err
	}
	if string(plain) != "Hello, Bob!" {
		return fmt.Errorf("plaintext mismatch")
	}
	// Persist the processed cursor before acknowledging in a real client.
	body, err = json.Marshal(map[string]int64{"delivered": event.Seq, "read": 0})
	if err != nil {
		return err
	}
	if err = a.request("POST", "/v1/chats/hello/receipts", tokens["bob"], body, 200, nil); err != nil {
		return err
	}
	var receipt struct {
		Type string `json:"type"`
	}
	if err = conn.ReadJSON(&receipt); err != nil {
		return err
	}
	if receipt.Type != "receipt.updated" {
		return fmt.Errorf("unexpected receipt event")
	}
	fmt.Println("OK: provisioned two devices; JWT → WS ticket → exact retry → event → HPKE decrypt → delivered receipt")
	return nil
}
