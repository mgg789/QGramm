package interop_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
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
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	_ "github.com/mgg789/QGramm/internal/modules"
	"github.com/thomas-vilte/mls-go/framing"
	pb "github.com/thomas-vilte/mls-go/interop/server/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// All identities/keys are fresh public test fixtures. Never format protobuf
// responses, participant snapshots, JWTs, credentials or plaintext into logs.
func remote(t *testing.T) pb.MLSClientClient {
	t.Helper()
	addr := os.Getenv("QG_INTEROP_OPENMLS")
	if addr == "" {
		t.Fatal("OpenMLS endpoint required; use tools/mls-interop/run.sh")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client := pb.NewMLSClientClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, err := client.Name(ctx, &pb.NameRequest{})
	if err != nil {
		t.Fatal("OpenMLS unavailable:", err)
	}
	if name.Name != "OpenMLS" {
		t.Fatal("independent OpenMLS implementation required")
	}
	return client
}
func rpcContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The MLS WG gRPC API returns MLSMessage(wire_format=key_package), whereas
// QGramm's admission contract accepts the canonical inner KeyPackage object.
func keyPackageObject(t *testing.T, wire []byte) []byte {
	t.Helper()
	message, err := framing.UnmarshalMLSMessage(wire)
	if err != nil || len(message.KeyPackage) == 0 || !bytes.Equal(message.Marshal(), wire) {
		t.Fatal("invalid independent KeyPackage MLSMessage framing")
	}
	return bytes.Clone(message.KeyPackage)
}
func testEngine(t *testing.T) *cryptoenc.Engine {
	t.Helper()
	master := make([]byte, 32)
	priv := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	engine, err := cryptoenc.New(master, priv)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func saveRestore(t *testing.T, p *cryptoenc.MLSParticipant, e *cryptoenc.Engine, path string) *cryptoenc.MLSParticipant {
	t.Helper()
	blob, err := p.Snapshot(e, []byte("fixture/participant"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path+".next", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(blob); err != nil {
		t.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cryptoenc.RestoreMLS(e, disk, []byte("fixture/participant"))
	if err != nil {
		t.Fatal(err)
	}
	return restored
}
func TestProductionAdapterOpenMLSRestartReplayAndEpoch(t *testing.T) {
	client := remote(t)
	ctx := rpcContext(t)
	engine := testEngine(t)
	statePath := filepath.Join(t.TempDir(), "participant.enc")
	actor, err := cryptoenc.NewMLS([]byte("fixture-ai-device"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = actor.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	kp, err := client.CreateKeyPackage(ctx, &pb.CreateKeyPackageRequest{CipherSuite: 1, Identity: []byte("fixture-human-device")})
	if err != nil {
		t.Fatal(err)
	}
	kp.KeyPackage = keyPackageObject(t, kp.KeyPackage)
	identity, _, err := cryptoenc.KeyPackageIdentity(kp.KeyPackage)
	if err != nil {
		t.Fatal("OpenMLS KeyPackage admission failed:", err)
	}
	if !bytes.Equal(identity, []byte("fixture-human-device")) {
		t.Fatal("OpenMLS KeyPackage identity mismatch")
	}
	_, welcome, err := actor.Invite(kp.KeyPackage)
	if err != nil {
		t.Fatal(err)
	}
	human, err := client.JoinGroup(ctx, &pb.JoinGroupRequest{TransactionId: kp.TransactionId, Welcome: welcome, Identity: identity, EncryptHandshake: false})
	if err != nil {
		t.Fatal(err)
	}
	actor = saveRestore(t, actor, engine, statePath)
	binding := cryptoenc.Binding("fixture-chat", "human", "fixture-human-device", "op1")
	incoming, err := client.Protect(ctx, &pb.ProtectRequest{StateId: human.StateId, AuthenticatedData: binding, Plaintext: []byte("fixture question")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = actor.Decrypt(incoming.Ciphertext, []byte("wrong binding")); err == nil {
		t.Fatal("wrong binding accepted")
	}
	plain, err := actor.Decrypt(incoming.Ciphertext, binding)
	if err != nil || string(plain) != "fixture question" {
		t.Fatal("production adapter failed independent human decrypt:", err)
	}
	actor = saveRestore(t, actor, engine, statePath)
	if _, err = actor.Decrypt(incoming.Ciphertext, binding); err == nil {
		t.Fatal("replay accepted after persisted restart")
	}
	if _, err = actor.Decrypt(append(bytes.Clone(incoming.Ciphertext), 0), binding); err == nil {
		t.Fatal("appended-byte replay accepted")
	}
	for i := 0; i < 2; i++ {
		replyBinding := cryptoenc.Binding("fixture-chat", "ai", "fixture-ai-device", "reply"+strconv.Itoa(i))
		ciphertext, err := actor.Encrypt([]byte("fixture answer"), replyBinding)
		if err != nil {
			t.Fatal(err)
		}
		actor = saveRestore(t, actor, engine, statePath)
		reply, err := client.Unprotect(ctx, &pb.UnprotectRequest{StateId: human.StateId, Ciphertext: ciphertext})
		if err != nil || string(reply.Plaintext) != "fixture answer" || !bytes.Equal(reply.AuthenticatedData, replyBinding) {
			t.Fatal("OpenMLS failed persisted sender counter interoperability:", err)
		}
	}
	tabletKP, err := client.CreateKeyPackage(ctx, &pb.CreateKeyPackageRequest{CipherSuite: 1, Identity: []byte("fixture-tablet")})
	if err != nil {
		t.Fatal(err)
	}
	tabletKP.KeyPackage = keyPackageObject(t, tabletKP.KeyPackage)
	commit, welcome, err := actor.Invite(tabletKP.KeyPackage)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := client.HandleCommit(ctx, &pb.HandleCommitRequest{StateId: human.StateId, Commit: commit})
	if err != nil {
		t.Fatal(err)
	}
	human.StateId = advanced.StateId
	tablet, err := client.JoinGroup(ctx, &pb.JoinGroupRequest{TransactionId: tabletKP.TransactionId, Welcome: welcome, Identity: []byte("fixture-tablet"), EncryptHandshake: false})
	if err != nil {
		t.Fatal(err)
	}
	actor = saveRestore(t, actor, engine, statePath)
	next, err := client.Protect(ctx, &pb.ProtectRequest{StateId: tablet.StateId, AuthenticatedData: binding, Plaintext: []byte("fixture new epoch")})
	if err != nil {
		t.Fatal(err)
	}
	plain, err = actor.Decrypt(next.Ciphertext, binding)
	if err != nil || string(plain) != "fixture new epoch" {
		t.Fatal("new independent epoch failed:", err)
	}
	if _, err = actor.Decrypt(incoming.Ciphertext, binding); err == nil {
		t.Fatal("stale previous epoch accepted")
	}
	t.Log("PASS: production adapter/OpenMLS suite1; durable participant restore, bound bidirectional application, replay rejection, public Add epoch transition")
}

func mockProvider(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	fixtureIP := net.ParseIP(os.Getenv("QG_INTEROP_FIXTURE_IP"))
	if fixtureIP == nil {
		t.Fatal("isolated Docker fixture IP required")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "QGramm test fixture only"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{fixtureIP}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err = os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", trust)
	listener, err := net.Listen("tcp", net.JoinHostPort(fixtureIP.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	server := &http.Server{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		defer r.Body.Close()
		var request map[string]json.RawMessage
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request) != nil || len(request["messages"]) == 0 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "fixture reply " + strconv.Itoa(int(n))}}}})
	})}
	go server.ServeTLS(listener, "", "")
	t.Cleanup(func() { server.Close() })
	return "https://" + listener.Addr().String() + "/v1", calls
}
func httpJSON(t *testing.T, base, method, path, token string, body any, want int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("HTTP %s %s status=%d want=%d", method, path, response.StatusCode, want)
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}
func TestProductionAIHTTPWithIndependentOpenMLSAndDBRestart(t *testing.T) {
	client := remote(t)
	// Explicit opt-in only: the normal isolated fixture never spends credits.
	live := os.Getenv("QGRAMM_LIVE_MLS") == "1"
	providerURL, model, keyEnv := "", "fixture-model", "QG_FIXTURE_PROVIDER_KEY"
	var providerCalls *atomic.Int32
	if live {
		providerURL, model, keyEnv = "https://api.deepseek.com/v1", "deepseek-flash", "QGRAMM_LIVE_KEY"
		if os.Getenv(keyEnv) == "" {
			t.Fatal("live provider credential unavailable")
		}
	} else {
		providerURL, providerCalls = mockProvider(t)
		t.Setenv(keyEnv, "fixture-only-provider-token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tokenPub, tokenPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Server.AllowInsecureLoopback = true
	cfg.Features.E2EE = true
	cfg.Features.OpenAI = true
	cfg.Capacity.Workers = 1
	cfg.Capacity.MaxConnections = 32
	cfg.Capacity.ExpectedConcurrentUsers = 32
	cfg.Capacity.QueueDepth = 32
	cfg.Storage.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Storage.Files = filepath.Join(t.TempDir(), "files")
	cfg.AI.Model = model
	cfg.AI.OpenAIURL = providerURL
	cfg.AI.OpenAIKeyEnv = keyEnv
	cfg.Security.TokenPublicKeyEnv = "QG_FIXTURE_TOKEN_PUB"
	cfg.Security.MasterKeyEnv = "QG_FIXTURE_MASTER"
	cfg.Security.HPKEKeyEnv = "QG_FIXTURE_HPKE"
	cfg.Security.ManagementSecretEnv = "QG_FIXTURE_MANAGEMENT"
	management := strings.Repeat("fixture-only-", 4)
	t.Setenv(cfg.Security.ManagementSecretEnv, management)
	t.Setenv(cfg.Security.TokenPublicKeyEnv, base64.StdEncoding.EncodeToString(tokenPub))
	for _, name := range []string{cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, base64.StdEncoding.EncodeToString(key))
	}
	compiled := []string{"e2ee", "openai"}
	runtime, err := core.Open(cfg, compiled)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(runtime.Mux)
	t.Cleanup(func() { server.Close(); runtime.Close() })
	kp, err := client.CreateKeyPackage(ctx, &pb.CreateKeyPackageRequest{CipherSuite: 1, Identity: []byte("human-phone")})
	if err != nil {
		t.Fatal(err)
	}
	kp.KeyPackage = keyPackageObject(t, kp.KeyPackage)
	_, signing, err := cryptoenc.KeyPackageIdentity(kp.KeyPackage)
	if err != nil {
		t.Fatal(err)
	}
	hpke, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	httpJSON(t, server.URL, "PUT", "/management/v1/users/human", management, map[string]any{"disabled": false}, 200, nil)
	httpJSON(t, server.URL, "PUT", "/management/v1/users/human/devices/human-phone", management, map[string]any{"public_key": base64.StdEncoding.EncodeToString(hpke.PublicKey().Bytes()), "signing_key": base64.StdEncoding.EncodeToString(signing)}, 200, nil)
	var admitted struct {
		ChatID   string `json:"chat_id"`
		UserID   string `json:"user_id"`
		DeviceID string `json:"device_id"`
		Welcome  string `json:"welcome"`
		Epoch    int64  `json:"epoch"`
	}
	httpJSON(t, server.URL, "POST", "/management/v1/ai/participants", management, map[string]any{"user_id": "human", "device_id": "human-phone", "provider": "openai", "mode": "e2ee", "key_package": base64.StdEncoding.EncodeToString(kp.KeyPackage), "tools": []string{}}, 201, &admitted)
	welcome, err := base64.StdEncoding.Strict().DecodeString(admitted.Welcome)
	if err != nil {
		t.Fatal(err)
	}
	human, err := client.JoinGroup(ctx, &pb.JoinGroupRequest{TransactionId: kp.TransactionId, Welcome: welcome, Identity: []byte("human-phone"), EncryptHandshake: false})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": cfg.Security.Issuer, "aud": cfg.Security.Audience, "sub": "human", "device_id": "human-phone", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}).SignedString(tokenPrivate)
	if err != nil {
		t.Fatal(err)
	}
	var firstWire, firstBinding []byte
	for round := 1; round <= 2; round++ {
		operation := "human-op-" + strconv.Itoa(round)
		binding := cryptoenc.Binding(admitted.ChatID, "human", "human-phone", operation)
		question := "fixture question " + strconv.Itoa(round)
		if live {
			question = "Reply only with the short literal QGRAMM_MLS_ACCEPTED_" + strconv.Itoa(round) + ". This is a synthetic integration test."
		}
		encrypted, err := client.Protect(ctx, &pb.ProtectRequest{StateId: human.StateId, AuthenticatedData: binding, Plaintext: []byte(question)})
		if err != nil {
			t.Fatal(err)
		}
		if round == 1 {
			firstWire = bytes.Clone(encrypted.Ciphertext)
			firstBinding = bytes.Clone(binding)
		}
		var accepted struct {
			Message core.Message `json:"message"`
		}
		httpJSON(t, server.URL, "POST", "/v1/chats/"+admitted.ChatID+"/messages", token, map[string]any{"operation_id": operation, "epoch": admitted.Epoch, "mls": base64.StdEncoding.EncodeToString(encrypted.Ciphertext)}, 201, &accepted)
		resultID := ""
		deadline := time.Now().Add(50 * time.Second)
		for time.Now().Before(deadline) {
			var jobs struct {
				Jobs []struct {
					MessageID string `json:"message_id"`
					Status    string `json:"status"`
					ResultID  string `json:"result_id"`
				} `json:"jobs"`
			}
			httpJSON(t, server.URL, "GET", "/management/v1/ai/chats/"+admitted.ChatID+"/jobs", management, nil, 200, &jobs)
			for _, job := range jobs.Jobs {
				if job.MessageID == accepted.Message.ID {
					if job.Status == "failed" || job.Status == "uncertain" {
						t.Fatal("AI job did not complete successful provider lifecycle; details omitted")
					}
					if job.Status == "succeeded" {
						resultID = job.ResultID
					}
				}
			}
			if resultID != "" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if resultID == "" {
			t.Fatal("AI completion timeout")
		}
		var messages []core.Message
		httpJSON(t, server.URL, "GET", "/v1/chats/"+admitted.ChatID+"/messages", token, nil, 200, &messages)
		var answer core.Message
		for _, message := range messages {
			if message.ID == resultID {
				answer = message
			}
		}
		if answer.ID == "" || answer.Sender != admitted.UserID || answer.DeviceID != admitted.DeviceID {
			t.Fatal("AI response identity/durable history mismatch")
		}
		wire, err := base64.StdEncoding.Strict().DecodeString(answer.MLS)
		if err != nil {
			t.Fatal(err)
		}
		opened, err := client.Unprotect(ctx, &pb.UnprotectRequest{StateId: human.StateId, Ciphertext: wire})
		if err != nil {
			t.Fatal("independent human could not decrypt actual AI response:", err)
		}
		expected := cryptoenc.Binding(admitted.ChatID, admitted.UserID, admitted.DeviceID, answer.OperationID)
		validPlaintext := string(opened.Plaintext) == "fixture reply "+strconv.Itoa(round)
		if live {
			validPlaintext = len(bytes.TrimSpace(opened.Plaintext)) > 0
		}
		if !bytes.Equal(opened.AuthenticatedData, expected) || !validPlaintext {
			t.Fatal("AI response bound plaintext mismatch")
		}
		if round == 1 {
			server.Close()
			if err = runtime.Close(); err != nil {
				t.Fatal(err)
			}
			runtime, err = core.Open(cfg, compiled)
			if err != nil {
				t.Fatal(err)
			}
			server = httptest.NewServer(runtime.Mux)
		}
	}
	if providerCalls != nil && providerCalls.Load() != 2 {
		t.Fatal("unexpected provider request count")
	}
	var succeededJobs, succeededCalls int
	if err = runtime.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE chat_id=? AND status='succeeded'`, admitted.ChatID).Scan(&succeededJobs); err != nil {
		t.Fatal("durable job count unavailable")
	}
	if err = runtime.DB.QueryRow(`SELECT count(*) FROM ai_audit a JOIN ai_jobs j ON j.id=a.job_id WHERE j.chat_id=? AND a.action='provider' AND a.outcome='succeeded'`, admitted.ChatID).Scan(&succeededCalls); err != nil || succeededJobs != 2 || succeededCalls != 2 {
		t.Fatal("unexpected durable provider lifecycle count")
	}
	var stored []byte
	if err = runtime.DB.QueryRow(`SELECT state FROM ai_chats WHERE chat_id=?`, admitted.ChatID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	restored, err := cryptoenc.RestoreMLS(runtime.Engine, stored, []byte("ai/state/"+admitted.ChatID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Decrypt(firstWire, firstBinding); err == nil {
		t.Fatal("actual durable AI snapshot lost replay ledger")
	}
	if live {
		t.Log("PASS: live DeepSeek Chat Completions via Go HTTP + independent OpenMLS human + actual Core admission/send/jobs/history + SQLite Core restart + durable replay rejection; two synthetic successful provider calls; authenticated nonempty response plaintext omitted")
	} else {
		t.Log("PASS: actual AI management admission/HTTP send/jobs/history + independent OpenMLS human + SQLite Core restart; provider explicitly local HTTPS mock")
	}
}
