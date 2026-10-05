package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func testQEngine(t *testing.T) *cryptoenc.Engine {
	t.Helper()
	master, key := make([]byte, 32), make([]byte, 32)
	rand.Read(master)
	rand.Read(key)
	engine, err := cryptoenc.New(master, key)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func TestQToken(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	q := &qGramm{private: private, names: []string{"device-7"}}
	raw, err := q.token(0)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return public, nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("qgramm"), jwt.WithAudience("qgramm"), jwt.WithExpirationRequired())
	if err != nil {
		t.Fatal(err)
	}
	claims := token.Claims.(jwt.MapClaims)
	if claims["sub"] != "device-7" || claims["device_id"] != "device-7" {
		t.Fatal("identity claims wrong")
	}
	expiry, _ := claims.GetExpirationTime()
	if time.Until(expiry.Time) > 14*time.Minute || time.Until(expiry.Time) < 13*time.Minute {
		t.Fatal("unexpected token lifetime")
	}
}

func TestQHistoryPageBound(t *testing.T) {
	if qHistoryPageLimit(256) != 200 || qHistoryPageLimit(64<<10) >= 200 || qHistoryPageLimit(16<<20) != 1 {
		t.Fatal("large encrypted payload history page not bounded")
	}
}
func TestQAcknowledgementValidation(t *testing.T) {
	for _, test := range []struct {
		name, raw      string
		minimal, valid bool
	}{
		{"full", `{"status":"accepted","message":{"id":"m","chat_id":"c","operation_id":"op","seq":1}}`, false, true},
		{"unwrapped-full-invalid", `{"id":"m","chat_id":"c","operation_id":"op","seq":1}`, false, false},
		{"minimal", `{"status":"accepted","receipt":{"message_id":"m","chat_id":"c","operation_id":"op","seq":1}}`, true, true},
		{"wrong-chat", `{"status":"accepted","message":{"id":"m","chat_id":"wrong","operation_id":"op","seq":1}}`, false, false},
		{"wrong-operation", `{"status":"accepted","message":{"id":"m","chat_id":"c","operation_id":"other","seq":1}}`, false, false},
		{"negative-seq", `{"status":"accepted","message":{"id":"m","chat_id":"c","operation_id":"op","seq":-1}}`, false, false},
		{"zero-seq", `{"status":"accepted","message":{"id":"m","chat_id":"c","operation_id":"op","seq":0}}`, false, false},
		{"missing-id", `{"status":"accepted","message":{"chat_id":"c","operation_id":"op","seq":1}}`, false, false},
		{"not-accepted", `{"status":"queued","receipt":{"message_id":"m","chat_id":"c","operation_id":"op","seq":1}}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateQAck([]byte(test.raw), test.minimal, "c", "op")
			if (err == nil) != test.valid {
				t.Fatalf("err=%v valid=%v", err, test.valid)
			}
		})
	}
}
func TestQRecipientEnvelopeBinding(t *testing.T) {
	first, second := testQEngine(t), testQEngine(t)
	q := &qGramm{cfg: Config{Chats: 1, Fanout: 2, Users: 3}, names: []string{"sender", "one", "two"}, chats: []string{"chat"}, engines: []*cryptoenc.Engine{testQEngine(t), first, second}}
	key, _ := base64.StdEncoding.DecodeString(first.PublicKey())
	envelope, err := cryptoenc.SealEnvelope(key, []byte("payload"), cryptoenc.Binding("chat", "one", "one", "m"))
	if err != nil {
		t.Fatal(err)
	}
	message := qMessage{ID: "m", Chat: "chat", Operation: "op", Seq: 1, Envelope: &envelope}
	plain, err := q.decrypt(1, message)
	if err != nil || string(plain) != "payload" {
		t.Fatalf("valid recipient: %v", err)
	}
	if _, err = q.decrypt(2, message); err == nil {
		t.Fatal("different device decrypted envelope")
	}
	message.ID = "other"
	if _, err = q.decrypt(1, message); err == nil {
		t.Fatal("committed message ID binding not enforced")
	}
}
func TestQHistoryChecksEveryChatAndPayload(t *testing.T) {
	cfg := Config{Chats: 2, Fanout: 1, Users: 4}
	engines := []*cryptoenc.Engine{testQEngine(t), testQEngine(t), testQEngine(t), testQEngine(t)}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	q := &qGramm{cfg: cfg, private: private, names: []string{"s0", "r0", "s1", "r1"}, chats: []string{"c0", "c1"}, engines: engines}
	rows := make([]qMessage, 2)
	for i := range rows {
		idx := i*2 + 1
		pub, _ := base64.StdEncoding.DecodeString(engines[idx].PublicKey())
		envelope, _ := cryptoenc.SealEnvelope(pub, []byte("payload"), cryptoenc.Binding(q.chats[i], q.names[idx], q.names[idx], "m"))
		rows[i] = qMessage{ID: "m", Chat: q.chats[i], Operation: "op" + q.chats[i], Seq: 1, Envelope: &envelope}
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = true
		mu.Unlock()
		if r.URL.Query().Get("after") != "0" {
			w.Write([]byte("[]"))
			return
		}
		index := 0
		if r.URL.Path == "/v1/chats/c1/messages" {
			index = 1
		}
		json.NewEncoder(w).Encode([]qMessage{rows[index]})
	}))
	defer server.Close()
	q.cfg.URL = server.URL
	q.http = server.Client()
	accepted := []Publication{{ID: "opc0", Chat: 0, Payload: []byte("payload")}, {ID: "opc1", Chat: 1, Payload: []byte("wrong")}, {ID: "missing", Chat: 1, Payload: []byte("payload")}}
	result, err := q.History(context.Background(), accepted)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checked != 2 || result.Corrupt != 1 || result.Missing != 1 || result.Unexpected != 0 || len(seen) != 2 {
		t.Fatalf("bad verification: %+v paths=%v", result, seen)
	}
	result, err = q.History(context.Background(), accepted[:1])
	if err != nil || result.Unexpected != 1 {
		t.Fatalf("unacknowledged stored message ignored: %+v %v", result, err)
	}
}
func TestQReadValidatesDeliveryAndPreservesCursor(t *testing.T) {
	engine := testQEngine(t)
	pub, _ := base64.StdEncoding.DecodeString(engine.PublicKey())
	envelope, _ := cryptoenc.SealEnvelope(pub, []byte("hello"), cryptoenc.Binding("chat", "r", "r", "m"))
	delivered := make(chan Delivery, 1)
	q := &qGramm{cfg: Config{Users: 2, Chats: 1, Fanout: 1}, names: []string{"s", "r"}, chats: []string{"chat"}, engines: []*cryptoenc.Engine{nil, engine}, sockets: make([]qSocket, 2), receive: func(d Delivery) { delivered <- d }}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.WriteJSON(map[string]any{"type": "message.created", "chat_id": "chat", "seq": 3, "data": qMessage{ID: "m", Chat: "chat", Operation: "op", Seq: 3, Envelope: &envelope}})
		conn.ReadMessage()
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
	if err != nil {
		t.Fatal(err)
	}
	q.sockets[1].conn = conn
	q.sockets[1].done = make(chan struct{})
	go q.read(1, conn, q.sockets[1].done)
	select {
	case d := <-delivered:
		if d.ID != "op" || string(d.Payload) != "hello" || d.Seq != 3 {
			t.Fatal(d)
		}
	case <-time.After(time.Second):
		t.Fatal("no validated delivery")
	}
	if err = q.disconnect(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if q.sockets[1].cursor != 3 || q.Errors() != 0 {
		t.Fatalf("cursor=%d errors=%d", q.sockets[1].cursor, q.Errors())
	}
}
