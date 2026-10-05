//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

// This exercises the actual QGramm core JSON wire shape (Event -> Message
// data, and MessageInput mls/epoch on POST) over HTTP, with MLS encryption on
// both sides. It catches transport-only tests that accidentally use the old
// wire field names.
func TestCoreTransportMLSMessageWire(t *testing.T) {
	alice, _ := cryptoenc.NewMLS([]byte("alice-device"))
	bob, _ := cryptoenc.NewMLS([]byte("bob-device"))
	chat := "chat"
	if _, err := alice.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	if _, welcome, err := alice.Invite(bob.KeyPackage()); err != nil {
		t.Fatal(err)
	} else if _, err = bob.Join(welcome); err != nil {
		t.Fatal(err)
	}
	op := "core-operation"
	plain := []byte(`{"version":1,"request_id":"r","client_id":"c","chat":"chat","source_peer":"alice-device","action":"invoke","name":"echo","body":{}}`)
	wire, err := alice.Encrypt(plain, cryptoenc.Binding(chat, "alice", "alice-device", op))
	if err != nil {
		t.Fatal(err)
	}
	responseOp := responseOperationID(RPCRequest{RequestID: "r", ClientID: "c", Chat: chat}, 2, "bob-device")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/chats/chat/events" {
			_, _ = w.Write([]byte(`[{"seq":1,"type":"message.deleted","data":{"deleted":true}},{"seq":2,"type":"message.created","data":{"operation_id":"core-operation","sender":"alice","device_id":"alice-device","mls":"` + base64.StdEncoding.EncodeToString(wire) + `","epoch":0}}]`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/chats/chat/messages" {
			var in struct {
				OperationID string `json:"operation_id"`
				MLS         string `json:"mls"`
				Epoch       uint64 `json:"epoch"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.OperationID != responseOp || in.Epoch != bob.Epoch() {
				http.Error(w, "bad core message", 400)
				return
			}
			got, e := base64.StdEncoding.Strict().DecodeString(in.MLS)
			if e != nil {
				http.Error(w, "bad b64", 400)
				return
			}
			if _, e = bob.Decrypt(got, cryptoenc.Binding(chat, "bob", "bob-device", in.OperationID)); e != nil {
				http.Error(w, "bad MLS AAD", 400)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	tx := &HTTPTransport{BaseURL: server.URL, Client: server.Client(), MaxBody: 1 << 20}
	poll, err := tx.Poll(context.Background(), chat, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(poll.Messages) != 1 || poll.Cursor != "2" {
		t.Fatalf("poll: %#v", poll)
	}
	if poll.Messages[0].OperationID != op || poll.Messages[0].SenderDevice != "alice-device" {
		t.Fatalf("message: %#v", poll.Messages[0])
	}
	if err = tx.Send(context.Background(), chat, OutboxItem{OperationID: "invalid-op", Seq: 2, Epoch: bob.Epoch(), Wire: wire}); err == nil {
		t.Fatal("test server should reject wrong response ciphertext")
	}
	// Send the actual Bob ciphertext with operation-bound MLS AAD.
	responseWire, _ := bob.Encrypt([]byte(`{"request_id":"r","kind":"result","seq":2,"final":true}`), cryptoenc.Binding(chat, "bob", "bob-device", responseOp))
	if err = tx.Send(context.Background(), chat, OutboxItem{OperationID: responseOp, Seq: 2, Epoch: bob.Epoch(), Wire: responseWire}); err != nil {
		t.Fatal(err)
	}
}
