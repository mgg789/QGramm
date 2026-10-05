package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

const scenarioStream = "BENCH"

type natsAdapter struct {
	cfg         Config
	receive     Receiver
	mu          sync.Mutex
	clients     []*nats.Conn
	errors      atomic.Int64
	intentional []atomic.Bool
}

func NewNATS(ctx context.Context, cfg Config, receive Receiver) (Adapter, error) {
	a := &natsAdapter{cfg: cfg, receive: receive, clients: make([]*nats.Conn, cfg.Users), intentional: make([]atomic.Bool, cfg.Users)}
	nc, err := a.connect(0)
	if err != nil {
		return nil, err
	}
	a.clients[0] = nc
	js, err := nc.JetStream()
	if err != nil {
		a.Close()
		return nil, err
	}
	_, err = js.AddStream(&nats.StreamConfig{Name: scenarioStream, Subjects: []string{"bench.*"}, Storage: nats.FileStorage, Replicas: 1, MaxMsgs: 1000000, MaxConsumers: 20000})
	if err != nil {
		a.Close()
		return nil, err
	}
	err = competitorSetup(ctx, cfg.Users, func(user int) error {
		if user != 0 {
			conn, e := a.connect(user)
			if e != nil {
				return e
			}
			a.mu.Lock()
			a.clients[user] = conn
			a.mu.Unlock()
		}
		if isReceiver(cfg, user) {
			return a.subscribe(user, true)
		}
		return nil
	})
	if err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

func (a *natsAdapter) connect(user int) (*nats.Conn, error) {
	return nats.Connect(a.cfg.URL, nats.Token(a.cfg.Secret), nats.NoReconnect(), nats.Timeout(15*time.Second), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, _ error) {
		if !a.intentional[user].Load() {
			a.errors.Add(1)
		}
	}), nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
		if err != nil && !a.intentional[user].Load() {
			a.errors.Add(1)
		}
	}))
}
func (a *natsAdapter) subscribe(user int, create bool) error {
	a.mu.Lock()
	nc := a.clients[user]
	a.mu.Unlock()
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	durable := "receiver_" + strconv.Itoa(user)
	subject := fmt.Sprintf("bench.%d", chatFor(a.cfg, user))
	if create {
		_, err = js.AddConsumer(scenarioStream, &nats.ConsumerConfig{Durable: durable, DeliverSubject: nats.NewInbox(), FilterSubject: subject, AckPolicy: nats.AckExplicitPolicy, AckWait: 30 * time.Second, MaxAckPending: 1000000, DeliverPolicy: nats.DeliverAllPolicy})
		if err != nil {
			return err
		}
	}
	_, err = js.Subscribe(subject, func(msg *nats.Msg) {
		meta, e := msg.Metadata()
		if e != nil {
			a.errors.Add(1)
			return
		}
		id, e := competitorPayloadID(msg.Data)
		if e != nil {
			a.errors.Add(1)
			return
		}
		a.receive(Delivery{ID: id, Chat: chatFor(a.cfg, user), Receiver: user, Seq: meta.Sequence.Stream, Payload: append([]byte(nil), msg.Data...)})
		if e = msg.Ack(); e != nil && !a.intentional[user].Load() {
			a.errors.Add(1)
		}
	}, nats.Bind(scenarioStream, durable), nats.ManualAck())
	if err != nil {
		return err
	}
	return nc.FlushTimeout(15 * time.Second)
}
func (a *natsAdapter) Send(ctx context.Context, p Publication) error {
	a.mu.Lock()
	nc := a.clients[p.Sender]
	a.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("sender disconnected")
	}
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	msg := nats.NewMsg(fmt.Sprintf("bench.%d", p.Chat))
	msg.Data = p.Payload
	msg.Header.Set(nats.MsgIdHdr, p.ID)
	_, err = js.PublishMsg(msg, nats.Context(ctx))
	return err
}
func (a *natsAdapter) History(ctx context.Context, accepted []Publication) (HistoryResult, error) {
	result := HistoryResult{Supported: true}
	a.mu.Lock()
	nc := a.clients[0]
	a.mu.Unlock()
	if nc == nil {
		return result, fmt.Errorf("history connection unavailable")
	}
	js, err := nc.JetStream()
	if err != nil {
		return result, err
	}
	info, err := js.StreamInfo(scenarioStream, nats.Context(ctx))
	if err != nil {
		return result, err
	}
	expected := make(map[string]Publication, len(accepted))
	for _, p := range accepted {
		expected[p.ID] = p
	}
	seen := make(map[string]bool, len(accepted))
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && seq != 0; seq++ {
		msg, e := js.GetMsg(scenarioStream, seq, nats.Context(ctx))
		if e != nil {
			return result, e
		}
		result.Checked++
		id, e := competitorPayloadID(msg.Data)
		p, ok := expected[id]
		if e != nil || !ok {
			result.Unexpected++
			continue
		}
		if seen[id] {
			result.Unexpected++
		}
		seen[id] = true
		if string(msg.Data) != string(p.Payload) || msg.Subject != fmt.Sprintf("bench.%d", p.Chat) {
			result.Corrupt++
		}
	}
	for id := range expected {
		if !seen[id] {
			result.Missing++
		}
	}
	if uint64(result.Checked) != info.State.Msgs {
		return result, fmt.Errorf("stream history changed during validation")
	}
	return result, nil
}
func (a *natsAdapter) Disconnect(_ context.Context, users []int) error {
	for _, u := range users {
		a.intentional[u].Store(true)
		a.mu.Lock()
		nc := a.clients[u]
		a.clients[u] = nil
		a.mu.Unlock()
		if nc != nil {
			_ = nc.FlushTimeout(5 * time.Second)
			nc.Close()
		}
	}
	return nil
}
func (a *natsAdapter) Reconnect(ctx context.Context, users []int) error {
	return competitorSetup(ctx, len(users), func(i int) error {
		u := users[i]
		nc, e := a.connect(u)
		if e != nil {
			return e
		}
		a.mu.Lock()
		a.clients[u] = nc
		a.mu.Unlock()
		if isReceiver(a.cfg, u) {
			e = a.subscribe(u, false)
		}
		if e == nil {
			a.intentional[u].Store(false)
		}
		return e
	})
}
func (a *natsAdapter) Close() error {
	for u := range a.clients {
		a.intentional[u].Store(true)
		a.mu.Lock()
		nc := a.clients[u]
		a.clients[u] = nil
		a.mu.Unlock()
		if nc != nil {
			nc.Close()
		}
	}
	return nil
}
func (a *natsAdapter) Errors() int64 { return a.errors.Load() }

func competitorSetup(ctx context.Context, count int, fn func(int) error) error {
	work := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range work {
				if ctx.Err() != nil {
					continue
				}
				if e := fn(n); e != nil {
					once.Do(func() { first = e; cancel() })
				}
			}
		}()
	}
loop:
	for i := 0; i < count; i++ {
		select {
		case work <- i:
		case <-ctx.Done():
			break loop
		}
	}
	close(work)
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
func competitorPayloadID(payload []byte) (string, error) {
	if len(payload) < 17 {
		return "", fmt.Errorf("payload shorter than ID")
	}
	id := string(payload[:17])
	for _, b := range []byte(id) {
		if b < '0' || b > '9' {
			return "", fmt.Errorf("invalid payload ID")
		}
	}
	return id, nil
}
