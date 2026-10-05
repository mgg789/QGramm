//go:build qg_ai_endpoint && qg_e2ee && qg_groups && qg_delete && qg_edit && qg_reactions && qg_reply && qg_forward && qg_openai

package modules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"github.com/mgg789/QGramm/internal/microsafer"
)

func TestAIEndpointCoreMicroSaferTransport(t *testing.T) {
	h := newModuleHarness(t, "global")
	chat := "relay-chat"
	relayErrors := make(chan string, 16)
	coreHandler := h.c.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		coreHandler.ServeHTTP(recorder, r)
		if recorder.Code >= 400 {
			select {
			case relayErrors <- fmt.Sprintf("%s %s -> %d: %s", r.Method, r.URL.Path, recorder.Code, strings.TrimSpace(recorder.Body.String())):
			default:
			}
		}
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()

	tokenEnv := "QG_TEST_MICRO_RELAY_TOKEN"
	masterEnv := "QG_TEST_MICRO_MASTER"
	hpkeEnv := "QG_TEST_MICRO_HPKE"
	grantEnv := "QG_TEST_MICRO_GRANT"
	master := bytes.Repeat([]byte{0x21}, 32)
	hpke := bytes.Repeat([]byte{0x42}, 32)
	t.Setenv(masterEnv, base64.StdEncoding.EncodeToString(master))
	t.Setenv(hpkeEnv, base64.StdEncoding.EncodeToString(hpke))
	public, err := base64.StdEncoding.DecodeString(os.Getenv(h.cfg.Security.TokenPublicKeyEnv))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(grantEnv, base64.StdEncoding.EncodeToString(public))

	alice, err := cryptoenc.NewMLS([]byte("phone"))
	if err != nil {
		t.Fatal(err)
	}
	bobConfigPath := filepath.Join(t.TempDir(), "bob.toml")
	initialPath := writeMicroTOML(t, bobConfigPath, server.URL, chat, tokenEnv, masterEnv, hpkeEnv, grantEnv, "", "", "", 0, nil, nil)
	initial, err := microsafer.LoadConfig(initialPath)
	if err != nil {
		t.Fatal(err)
	}
	bobBoot, err := microsafer.Bootstrap(context.Background(), initial, chat)
	if err != nil {
		t.Fatal(err)
	}
	bobKeyPackage, err := base64.StdEncoding.Strict().DecodeString(bobBoot.KeyPackage)
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := alice.CreateGroup()
	if err != nil {
		t.Fatal(err)
	}
	_, welcome, err := alice.Invite(bobKeyPackage)
	if err != nil {
		t.Fatal(err)
	}
	groupContext := alice.GroupContext()
	aliceSigning := base64.StdEncoding.EncodeToString(alice.SigningPublicKey())
	if _, err = h.c.DB.Exec(`UPDATE devices SET signing_key=? WHERE id='phone'`, aliceSigning); err != nil {
		t.Fatal(err)
	}
	if _, err = h.c.DB.Exec(`UPDATE devices SET signing_key=? WHERE id='bob-phone'`, bobBoot.SigningPublicKey); err != nil {
		t.Fatal(err)
	}
	finalPath := writeMicroTOML(t, bobConfigPath, server.URL, chat, tokenEnv, masterEnv, hpkeEnv, grantEnv, "alice", "phone", aliceSigning, alice.Epoch(), groupID, groupContext)
	bobConfig, err := microsafer.LoadConfig(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = microsafer.JoinWelcome(context.Background(), bobConfig, chat, welcome); err != nil {
		t.Fatal(err)
	}

	h.request(t, "POST", "/management/v1/chats/direct", map[string]any{"id": chat, "members": []string{"alice", "bob"}, "mode": "e2ee"}, 201, true)
	var pending bool
	if err = h.c.DB.QueryRow(`SELECT pending FROM chats WHERE id=?`, chat).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("new E2EE chat did not start with a pending MLS epoch")
	}
	tx, err := h.c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = InitMLSRelayTx(context.Background(), h.c, tx, chat, groupID, groupContext, alice.Epoch(), []MLSRelayMember{{DeviceID: "phone", LeafIndex: 0}, {DeviceID: "bob-phone", LeafIndex: 1}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = h.c.DB.QueryRow(`SELECT pending FROM chats WHERE id=?`, chat).Scan(&pending); err != nil || pending {
		t.Fatalf("relay initialization did not clear pending epoch: %v", err)
	}
	var aiJobs int
	if err = h.c.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE chat_id=?`, chat).Scan(&aiJobs); err != nil {
		t.Fatal(err)
	}
	if aiJobs != 0 {
		t.Fatalf("transport fixture unexpectedly created AI jobs: %d", aiJobs)
	}

	t.Setenv(tokenEnv, relayJWT(t, h, "bob", "bob-phone"))
	runtime, err := microsafer.OpenRuntime(context.Background(), bobConfig, chat)
	if err != nil {
		t.Fatal(err)
	}
	seedTx, err := runtime.Participant().Store.DB().BeginTx(context.Background(), nil)
	if err != nil {
		runtime.Participant().Store.Close()
		t.Fatal(err)
	}
	if err = runtime.Participant().Store.SetCursorTx(context.Background(), seedTx, chat, "0"); err != nil {
		_ = seedTx.Rollback()
		runtime.Participant().Store.Close()
		t.Fatal(err)
	}
	if err = seedTx.Commit(); err != nil {
		runtime.Participant().Store.Close()
		t.Fatal(err)
	}
	var effects atomic.Int32
	if err = runtime.RegisterHandler("echo", false, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		effects.Add(1)
		return body, nil
	}); err != nil {
		runtime.Participant().Store.Close()
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runtime.Run(runCtx) }()

	request := microsafer.RPCRequest{Version: 1, RequestID: "request-1", ClientID: "client-1", Chat: chat, SourcePeer: "phone", Action: "invoke", Name: "echo", Body: json.RawMessage(`{"value":1}`)}
	requestBody, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "human-op-1"
	wire, err := alice.Encrypt(requestBody, cryptoenc.Binding(chat, "alice", "phone", operationID))
	if err != nil {
		t.Fatal(err)
	}
	sent, err := h.c.Send(context.Background(), core.Identity{UserID: "alice", DeviceID: "phone"}, chat, core.MessageInput{OperationID: operationID, MLS: base64.StdEncoding.EncodeToString(wire), Epoch: int64(alice.Epoch())})
	if err != nil {
		t.Fatal(err)
	}
	events := waitForBobResponses(t, h.c, chat, sent.Seq, runDone, relayErrors)
	if effects.Load() != 1 {
		t.Fatalf("micro handler effect count = %d, want 1", effects.Load())
	}
	responses := decryptBobResponses(t, alice, chat, events)
	if len(responses) != 2 || responses[0].Kind != "notice" || responses[1].Kind != "result" || string(responses[1].Body) != `{"value":1}` {
		t.Fatalf("unexpected decrypted Bob responses: %#v", responses)
	}

	retry, err := h.c.Send(context.Background(), core.Identity{UserID: "alice", DeviceID: "phone"}, chat, core.MessageInput{OperationID: operationID, MLS: base64.StdEncoding.EncodeToString(wire), Epoch: int64(alice.Epoch())})
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != sent.ID || retry.Seq != sent.Seq || effects.Load() != 1 {
		t.Fatalf("same operation was not idempotent: first=%+v retry=%+v effects=%d", sent, retry, effects.Load())
	}

	cancelRun()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("micro runtime did not stop")
	}
	if err = runtime.Participant().Store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := microsafer.OpenRuntime(context.Background(), bobConfig, chat)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.RegisterHandler("echo", false, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		effects.Add(1)
		return body, nil
	}); err != nil {
		restarted.Participant().Store.Close()
		t.Fatal(err)
	}
	if replay, replayErr := restarted.ProcessRPC(context.Background(), request); replayErr != nil || len(replay) != 1 || replay[0].Kind != "result" {
		restarted.Participant().Store.Close()
		t.Fatalf("durable restart replay failed: responses=%#v err=%v", replay, replayErr)
	}
	if effects.Load() != 1 {
		restarted.Participant().Store.Close()
		t.Fatalf("restart replay re-executed effect: %d", effects.Load())
	}
	restartCtx, cancelRestart := context.WithTimeout(context.Background(), 120*time.Millisecond)
	if err = restarted.Run(restartCtx); err == nil && !restarted.Frozen() {
		t.Fatal("restart runtime unexpectedly completed without cancellation")
	}
	cancelRestart()
	if err = restarted.Participant().Store.Close(); err != nil {
		t.Fatal(err)
	}

	// The same Core.Handler rejects malformed relay messages, an epoch barrier,
	// and a revoked relay device. These requests cross the real HTTP boundary.
	if status := postCoreMessage(t, server.URL, relayJWT(t, h, "bob", "bob-phone"), chat, `{"operation_id":"malformed","mls":"%%%","epoch":1}`); status/100 == 2 {
		t.Fatalf("malformed relay message accepted with status %d", status)
	}
	if _, err = h.c.DB.Exec(`UPDATE chats SET pending=1 WHERE id=?`, chat); err != nil {
		t.Fatal(err)
	}
	pendingBody := `{"operation_id":"pending-op","mls":"` + base64.StdEncoding.EncodeToString(wire) + `","epoch":1}`
	if status := postCoreMessage(t, server.URL, relayJWT(t, h, "bob", "bob-phone"), chat, pendingBody); status != http.StatusConflict {
		t.Fatalf("pending epoch accepted with status %d", status)
	}
	if _, err = h.c.DB.Exec(`UPDATE chats SET pending=0 WHERE id=?`, chat); err != nil {
		t.Fatal(err)
	}
	if _, err = h.c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='bob-phone'`); err != nil {
		t.Fatal(err)
	}
	if status := getCoreEvents(t, server.URL, relayJWT(t, h, "bob", "bob-phone"), chat); status/100 == 2 {
		t.Fatalf("revoked relay device could poll with status %d", status)
	}
}

func writeMicroTOML(t *testing.T, path, serverURL, chat, tokenEnv, masterEnv, hpkeEnv, grantEnv, peerUser, peerDevice, signing string, epoch uint64, groupID []byte, groupContext []byte) string {
	t.Helper()
	text := fmt.Sprintf(`[endpoint]
	name = "bob-endpoint"
	user = "bob"
	device = "bob-phone"
	server_url = %s
	token_env = %s
	master_key_env = %s
	hpke_key_env = %s
	db_path = %s
	grant_public_key_env = %s
	issuer = "qgramm-test"
	audience = "qgramm-micro"
	poll_interval_ms = 10

	[handlers.echo]
	kind = "tools"
	require_approval = false
	`, strconv.Quote(serverURL), strconv.Quote(tokenEnv), strconv.Quote(masterEnv), strconv.Quote(hpkeEnv), strconv.Quote(filepath.Join(filepath.Dir(path), "state.db")), strconv.Quote(grantEnv))
	if peerUser != "" {
		text += fmt.Sprintf(`
	[peers.%s]
	user = %s
	device = %s
	signing_key = %s
	group_id = %s
	group_context = %s
	epoch = %d

	[chats.%s]
	peer = %s
	`, strconv.Quote(chat), strconv.Quote(peerUser), strconv.Quote(peerDevice), strconv.Quote(signing), strconv.Quote(base64.StdEncoding.EncodeToString(groupID)), strconv.Quote(base64.StdEncoding.EncodeToString(groupContext)), epoch, strconv.Quote(chat), strconv.Quote(chat))
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func relayJWT(t *testing.T, h *moduleHarness, user, device string) string {
	t.Helper()
	now := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": user, "device_id": device, "iss": h.cfg.Security.Issuer, "aud": h.cfg.Security.Audience, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}).SignedString(h.sign)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func waitForBobResponses(t *testing.T, c *core.Core, chat string, after int64, runDone <-chan error, relayErrors <-chan string) []core.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := c.Events(context.Background(), core.Identity{UserID: "alice", DeviceID: "phone"}, chat, after, 200)
		if err == nil {
			var bob []core.Event
			for _, event := range events {
				if message, ok := event.Data.(core.Message); ok && message.Sender == "bob" && strings.HasPrefix(message.OperationID, "ai-endpoint-") {
					bob = append(bob, event)
				}
			}
			if len(bob) >= 2 {
				return bob
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-runDone:
		select {
		case relayErr := <-relayErrors:
			t.Fatalf("micro runtime stopped before Bob responses: %v (%s)", err, relayErr)
		default:
			t.Fatalf("micro runtime stopped before Bob responses: %v", err)
		}
	default:
		t.Fatalf("timed out waiting for Bob responses")
	}
	return nil
}

func decryptBobResponses(t *testing.T, alice *cryptoenc.MLSParticipant, chat string, events []core.Event) []microsafer.RPCResponse {
	t.Helper()
	out := make([]microsafer.RPCResponse, 0, len(events))
	for _, event := range events {
		message := event.Data.(core.Message)
		wire, err := base64.StdEncoding.Strict().DecodeString(message.MLS)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := alice.Decrypt(wire, cryptoenc.Binding(chat, "bob", "bob-phone", message.OperationID))
		if err != nil {
			t.Fatalf("decrypt Bob operation %s: %v", message.OperationID, err)
		}
		var response microsafer.RPCResponse
		if err = json.Unmarshal(plain, &response); err != nil {
			t.Fatal(err)
		}
		out = append(out, response)
	}
	return out
}

func postCoreMessage(t *testing.T, baseURL, token, chat, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chats/"+chat+"/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func getCoreEvents(t *testing.T, baseURL, token, chat string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/chats/"+chat+"/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
