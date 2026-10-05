package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCompetitorPayloadIntegrity(t *testing.T) {
	for _, value := range []string{"", "0000000000000000", "0000000000000000x"} {
		if _, e := competitorPayloadID([]byte(value)); e == nil {
			t.Fatalf("accepted malformed %q", value)
		}
	}
	body := []byte("00000000000000001abc")
	id, e := competitorPayloadID(body)
	if e != nil || id != "00000000000000001" {
		t.Fatalf("id %q error %v", id, e)
	}
	encoded, _ := json.Marshal(string(body))
	payload, got, e := centrifugoPayload(centrifugoPub{Data: encoded})
	if e != nil || got != id || string(payload) != string(body) {
		t.Fatalf("payload %q id %q error %v", payload, got, e)
	}
	if _, _, e = centrifugoPayload(centrifugoPub{Data: json.RawMessage(`{"x":1}`)}); e == nil {
		t.Fatal("accepted object payload")
	}
}

func centrifugoTestServer(t *testing.T, recovered bool, history []centrifugoPub, offset uint64) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		for {
			var cmd map[string]json.RawMessage
			if e = ws.ReadJSON(&cmd); e != nil {
				return
			}
			var id uint64
			_ = json.Unmarshal(cmd["id"], &id)
			reply := map[string]any{"id": id}
			switch {
			case cmd["connect"] != nil:
				reply["connect"] = map[string]any{"client": "test"}
			case cmd["subscribe"] != nil:
				var args struct {
					Recover bool `json:"recover"`
				}
				_ = json.Unmarshal(cmd["subscribe"], &args)
				reply["subscribe"] = map[string]any{"recoverable": true, "recovered": recovered && args.Recover, "offset": offset, "epoch": "epoch", "publications": []any{}}
			case cmd["history"] != nil:
				var args struct {
					Limit int                 `json:"limit"`
					Since *centrifugoPosition `json:"since"`
				}
				_ = json.Unmarshal(cmd["history"], &args)
				var page []centrifugoPub
				for _, pub := range history {
					if args.Limit > len(page) && (args.Since == nil || pub.Offset > args.Since.Offset) {
						page = append(page, pub)
					}
				}
				reply["history"] = map[string]any{"offset": offset, "epoch": "epoch", "publications": page}
			default:
				reply["publish"] = map[string]any{}
			}
			if e = ws.WriteJSON(reply); e != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCentrifugoHistoryBoundedPagination(t *testing.T) {
	const count = 300
	accepted := make([]Publication, count)
	pubs := make([]centrifugoPub, count)
	for i := range accepted {
		id := fmt.Sprintf("%017d", i+1)
		body := []byte(id + strings.Repeat("x", 4096-len(id)))
		encoded, _ := json.Marshal(string(body))
		accepted[i] = Publication{ID: id, Chat: 0, Payload: body}
		pubs[i] = centrifugoPub{Data: encoded, Offset: uint64(i + 1)}
	}
	var pageRequests atomic.Int64
	var badRequest atomic.Bool
	up := websocket.Upgrader{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var cmd map[string]json.RawMessage
			if err = ws.ReadJSON(&cmd); err != nil {
				return
			}
			var id uint64
			_ = json.Unmarshal(cmd["id"], &id)
			reply := map[string]any{"id": id}
			switch {
			case cmd["connect"] != nil:
				reply["connect"] = map[string]any{"client": "test"}
			case cmd["subscribe"] != nil:
				reply["subscribe"] = map[string]any{"epoch": "epoch", "offset": count, "recoverable": true}
			case cmd["history"] != nil:
				var args struct {
					Limit   int                 `json:"limit"`
					Reverse bool                `json:"reverse"`
					Since   *centrifugoPosition `json:"since"`
				}
				_ = json.Unmarshal(cmd["history"], &args)
				var page []centrifugoPub
				if args.Limit > 0 {
					pageRequests.Add(1)
					// Model a bounded server output queue: the old all-history
					// request would close the socket here despite valid live traffic.
					if args.Limit > 128 || args.Limit*(4096+256) > 512*1024 || args.Since == nil || args.Since.Epoch != "epoch" || args.Reverse || args.Since.Offset >= count {
						badRequest.Store(true)
						return
					}
					start := int(args.Since.Offset)
					page = pubs[start:min(count, start+args.Limit)]
				}
				reply["history"] = map[string]any{"epoch": "epoch", "offset": count, "publications": page}
			}
			if err = ws.WriteJSON(reply); err != nil {
				return
			}
		}
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := NewCentrifugo(ctx, Config{URL: "ws" + strings.TrimPrefix(s.URL, "http"), Users: 2, Chats: 1, Fanout: 1, PayloadBytes: 4096, Secret: "test-only"}, func(Delivery) {})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	history, err := a.History(ctx, accepted)
	if err != nil || history.Checked != count || history.Missing != 0 || history.Unexpected != 0 || history.Corrupt != 0 || a.Errors() != 0 || badRequest.Load() || pageRequests.Load() < 3 {
		t.Fatalf("history %+v error %v pages %d bad %v errors %d", history, err, pageRequests.Load(), badRequest.Load(), a.Errors())
	}
}
func TestCentrifugoIntentionalDisconnectAndRecoveryFailure(t *testing.T) {
	s := centrifugoTestServer(t, false, nil, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	adapter, e := NewCentrifugo(ctx, Config{URL: "ws" + strings.TrimPrefix(s.URL, "http"), Users: 2, Chats: 1, Fanout: 1, Secret: "test-only"}, func(Delivery) {})
	if e != nil {
		t.Fatal(e)
	}
	defer adapter.Close()
	if e = adapter.Disconnect(ctx, []int{1}); e != nil {
		t.Fatal(e)
	}
	if adapter.Errors() != 0 {
		t.Fatal("intentional disconnect counted error")
	}
	if e = adapter.Reconnect(ctx, []int{1}); e == nil {
		t.Fatal("unrecovered subscription accepted")
	}
}
func TestCentrifugoHistoryChecksContentAndTruncation(t *testing.T) {
	body := []byte("00000000000000001")
	encoded, _ := json.Marshal(string(body))
	for _, test := range []struct {
		name      string
		data      json.RawMessage
		offset    uint64
		wantError bool
		corrupt   int
	}{{"exact", encoded, 1, false, 0}, {"corrupt", json.RawMessage(`"00000000000000001X"`), 1, false, 1}, {"truncated", encoded, 2, true, 0}} {
		t.Run(test.name, func(t *testing.T) {
			s := centrifugoTestServer(t, true, []centrifugoPub{{Data: test.data, Offset: 1}}, test.offset)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			adapter, e := NewCentrifugo(ctx, Config{URL: "ws" + strings.TrimPrefix(s.URL, "http"), Users: 2, Chats: 1, Fanout: 1, Secret: "test-only"}, func(Delivery) {})
			if e != nil {
				t.Fatal(e)
			}
			defer adapter.Close()
			result, e := adapter.History(ctx, []Publication{{ID: string(body), Chat: 0, Payload: body}})
			if (e != nil) != test.wantError || result.Corrupt != test.corrupt {
				t.Fatalf("result %+v error %v", result, e)
			}
			if !test.wantError && (result.Checked != 1 || result.Missing != 0 || result.Unexpected != 0) {
				t.Fatalf("result %+v", result)
			}
		})
	}
}
