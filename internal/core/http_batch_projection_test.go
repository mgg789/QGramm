package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type batchProjectionReply struct {
	OperationID string  `json:"operation_id"`
	Status      int     `json:"status"`
	Message     Message `json:"message"`
}

func decodeBatchProjection(t *testing.T, w *httptest.ResponseRecorder) []batchProjectionReply {
	t.Helper()
	var out []batchProjectionReply
	if w.Code != 207 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("invalid full batch response: %d %s", w.Code, w.Body)
	}
	return out
}

func TestHTTPBatchProjectionOrderedAcrossChunksAndRetry(t *testing.T) {
	f := newFixture(t)
	inputs := make([]MessageInput, maxWriteBatch+3)
	for i := range inputs {
		inputs[i] = f.input(t, fmt.Sprintf("projection-%02d", i), fmt.Sprintf("payload-%02d", i))
	}
	// Invalid preparation must not shift neighboring full responses.
	inputs[4] = MessageInput{OperationID: "projection-invalid", MLS: "wrong-mode"}
	first := decodeBatchProjection(t, batchRequest(t, f, context.Background(), inputs, false))
	second := decodeBatchProjection(t, batchRequest(t, f, context.Background(), inputs, false))
	if len(first) != len(inputs) || len(second) != len(inputs) {
		t.Fatal("result count changed")
	}
	for i, reply := range first {
		if reply.OperationID != inputs[i].OperationID {
			t.Fatalf("response %d reordered", i)
		}
		if i == 4 {
			if reply.Status != 400 || second[i].Status != 400 {
				t.Fatal("invalid item affected neighbors")
			}
			continue
		}
		if reply.Status != 201 || second[i].Status != 201 || reply.Message.ID != second[i].Message.ID || reply.Message.Envelope == nil || second[i].Message.Envelope == nil {
			t.Fatalf("response %d invalid or duplicated durable message", i)
		}
		for _, m := range []Message{reply.Message, second[i].Message} {
			plain, err := f.alice.OpenEnvelope(*m.Envelope, cryptoenc.Binding("chat", f.id.UserID, f.id.DeviceID, m.ID))
			if err != nil || string(plain) != fmt.Sprintf("payload-%02d", i) {
				t.Fatalf("response %d envelope invalid: %v", i, err)
			}
		}
	}
}

func TestHTTPBatchProjectionDuplicatesGetIndependentEnvelopes(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "projection-duplicate", "duplicate payload")
	out := decodeBatchProjection(t, batchRequest(t, f, context.Background(), []MessageInput{in, in}, false))
	if len(out) != 2 || out[0].Status != 201 || out[1].Status != 201 || out[0].Message.ID != out[1].Message.ID {
		t.Fatal("duplicate result changed")
	}
	a, _ := json.Marshal(out[0].Message.Envelope)
	b, _ := json.Marshal(out[1].Message.Envelope)
	if string(a) == string(b) {
		t.Fatal("duplicate response reused randomized envelope")
	}
}

func TestBatchProjectionMissingAndRevokedRemainIndividual(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "projection-accessible", "accessible"))
	if err != nil {
		t.Fatal(err)
	}
	results := []messageWriteResult{{message: Message{ID: "missing"}}, {message: m}}
	f.c.projectBatchResults(ctx, f.id, results)
	var api *APIError
	if !errors.As(results[0].err, &api) || api.Status != 404 || results[1].err != nil || results[1].message.Envelope == nil {
		t.Fatal("missing item invalidated accessible neighbor")
	}
	if _, err = f.c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id=?`, f.id.DeviceID); err != nil {
		t.Fatal(err)
	}
	results = []messageWriteResult{{message: m}}
	f.c.projectBatchResults(ctx, f.id, results)
	if !errors.As(results[0].err, &api) || api.Status != 404 || results[0].message.Envelope != nil {
		t.Fatal("projection bypassed fresh device revocation")
	}
}

func TestHTTPBatchProjectionHooksKeepSequentialAccessAndPartialErrors(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			f := newFixture(t)
			calls := 0
			f.c.Project = append(f.c.Project, func(ctx context.Context, id Identity, m *Message) error {
				calls++
				if m.OperationID != "projection-hook-a" {
					return nil
				}
				if revoke {
					_, err := f.c.DB.ExecContext(ctx, `UPDATE members SET active=0 WHERE chat_id='chat' AND user_id=?`, id.UserID)
					return err
				}
				return &APIError{409, "projection hook denied"}
			})
			out := decodeBatchProjection(t, batchRequest(t, f, context.Background(), []MessageInput{f.input(t, "projection-hook-a", "a"), f.input(t, "projection-hook-b", "b")}, false))
			if revoke {
				if out[0].Status != 201 || out[1].Status != 404 || calls != 1 {
					t.Fatalf("hook revocation bypassed: %+v calls=%d", out, calls)
				}
			} else if out[0].Status != 409 || out[1].Status != 201 || calls != 2 {
				t.Fatalf("hook failure changed neighbors: %+v calls=%d", out, calls)
			}
		})
	}
}
