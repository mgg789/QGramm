// Isolated real-service load generator. No QGramm dependencies.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
)

type phase struct {
	Name          string             `json:"name"`
	Offered       int                `json:"offered"`
	Accepted      int                `json:"accepted"`
	Delivered     int                `json:"delivered"`
	Duplicate     int                `json:"duplicate"`
	PublishErrors int                `json:"publish_errors"`
	AckMS         map[string]float64 `json:"ack_ms"`
	DeliveryMS    map[string]float64 `json:"delivery_ms"`
	Elapsed       float64            `json:"elapsed_seconds"`
	OfferedRate   int                `json:"offered_rate"`
	ActualRate    float64            `json:"actual_offered_per_second"`
}
type entry struct {
	started  time.Time
	phase    int
	received bool
}

var mu sync.Mutex
var entries = map[string]*entry{}
var phases []phase
var ackSamples, deliverySamples [][]float64
var connectionErrors atomic.Int64

func observe(id string) {
	mu.Lock()
	defer mu.Unlock()
	e := entries[id]
	if e == nil {
		connectionErrors.Add(1)
		return
	}
	if e.received {
		phases[e.phase].Duplicate++
		return
	}
	e.received = true
	phases[e.phase].Delivered++
	deliverySamples[e.phase] = append(deliverySamples[e.phase], float64(time.Since(e.started))/1e6)
}
func quantiles(a []float64) map[string]float64 {
	sort.Float64s(a)
	m := map[string]float64{}
	if len(a) == 0 {
		return m
	}
	for k, p := range map[string]float64{"p50": .5, "p95": .95, "p99": .99, "max": 1} {
		m[k] = a[int(math.Ceil(p*float64(len(a))))-1]
	}
	return m
}
func validateResults(results []phase, receiveErrors int64) error {
	if receiveErrors != 0 {
		return fmt.Errorf("connection/receive errors: %d", receiveErrors)
	}
	for _, p := range results {
		if p.PublishErrors != 0 || p.Offered != p.Accepted || p.Accepted != p.Delivered || p.Duplicate != 0 {
			return fmt.Errorf("%s integrity failed: offered=%d accepted=%d delivered=%d duplicates=%d publish_errors=%d", p.Name, p.Offered, p.Accepted, p.Delivered, p.Duplicate, p.PublishErrors)
		}
	}
	return nil
}
func jwt(secret, user string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	b, _ := json.Marshal(map[string]any{"sub": user, "exp": time.Now().Add(time.Hour).Unix()})
	s := h + "." + base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(s))
	return s + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func wsReply(c *websocket.Conn, id int) error {
	for {
		var r map[string]json.RawMessage
		if err := c.ReadJSON(&r); err != nil {
			return err
		}
		if string(r["id"]) == fmt.Sprint(id) {
			if e := r["error"]; e != nil {
				return fmt.Errorf("protocol error: %s", e)
			}
			return nil
		}
		if len(r) == 0 {
			c.WriteJSON(map[string]any{})
		}
	}
}
func main() {
	service := flag.String("service", "nats", "")
	url := flag.String("url", "", "address")
	users := flag.Int("users", 10000, "")
	duration := flag.Duration("duration", 30*time.Second, "")
	burst := flag.Duration("burst", 5*time.Second, "")
	rate := flag.Int("rate", 100, "")
	burstRate := flag.Int("burst-rate", 1000, "")
	out := flag.String("out", "result.json", "")
	idle := flag.Duration("idle", 10*time.Second, "connected idle interval before traffic")
	phaseFile := flag.String("phase-file", "", "private workload phase marker")
	flag.Parse()
	mark := func(name string) {
		if *phaseFile != "" {
			_ = os.WriteFile(*phaseFile, []byte(name), 0600)
		}
	}
	mark("setup")
	if *duration <= 0 || *burst <= 0 || *rate <= 0 || *burstRate <= 0 {
		panic("positive durations and rates required")
	}
	if *users < 2 {
		panic("at least two clients")
	}
	setup := time.Now()
	var publish func(string) error
	var closeAll func()
	if *service == "nats" {
		clients := make([]*nats.Conn, 0, *users)
		for i := 0; i < *users; i++ {
			c, e := nats.Connect(*url, nats.Token(os.Getenv("BENCH_SECRET")), nats.NoReconnect(), nats.DisconnectErrHandler(func(_ *nats.Conn, _ error) { connectionErrors.Add(1) }), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, _ error) { connectionErrors.Add(1) }))
			if e != nil {
				panic(e)
			}
			clients = append(clients, c)
		}
		closeAll = func() {
			for _, c := range clients {
				c.Close()
			}
		}
		js, e := clients[0].JetStream(nats.MaxWait(30 * time.Second))
		if e != nil {
			panic(e)
		}
		_, e = js.AddStream(&nats.StreamConfig{Name: "BENCH", Subjects: []string{"bench.messages"}, Storage: nats.FileStorage, Replicas: 1, MaxMsgs: 100000})
		if e != nil {
			panic(e)
		}
		receiver, e := clients[1].JetStream()
		if e != nil {
			panic(e)
		}
		_, e = receiver.Subscribe("bench.messages", func(m *nats.Msg) {
			observe(string(m.Data))
			if m.Ack() != nil {
				connectionErrors.Add(1)
			}
		}, nats.Durable("receiver"), nats.ManualAck(), nats.AckExplicit(), nats.DeliverAll(), nats.MaxAckPending(100000))
		if e != nil {
			panic(e)
		}
		if e = clients[1].Flush(); e != nil {
			panic(e)
		}
		publish = func(id string) error { _, e := js.Publish("bench.messages", []byte(id)); return e }
	} else if *service == "centrifugo" {
		clients := make([]*websocket.Conn, 0, *users)
		for i := 0; i < *users; i++ {
			c, _, e := websocket.DefaultDialer.Dial(*url, nil)
			if e != nil {
				panic(e)
			}
			c.SetReadDeadline(time.Now().Add(30 * time.Second))
			if e = c.WriteJSON(map[string]any{"id": 1, "connect": map[string]any{"token": jwt(os.Getenv("BENCH_SECRET"), fmt.Sprint(i))}}); e != nil {
				panic(e)
			}
			if e = wsReply(c, 1); e != nil {
				panic(e)
			}
			c.SetReadDeadline(time.Time{})
			clients = append(clients, c)
		}
		closeAll = func() {
			for _, c := range clients {
				c.Close()
			}
		}
		receiver := clients[1]
		receiver.WriteJSON(map[string]any{"id": 2, "subscribe": map[string]any{"channel": "bench"}})
		if e := wsReply(receiver, 2); e != nil {
			panic(e)
		}
		for i, c := range clients {
			if i == 0 {
				continue
			}
			go func(c *websocket.Conn, receive bool) {
				for {
					var r map[string]json.RawMessage
					if c.ReadJSON(&r) != nil {
						connectionErrors.Add(1)
						return
					}
					if len(r) == 0 {
						c.WriteJSON(map[string]any{})
						continue
					}
					if receive {
						var v struct {
							Pub struct {
								Data string `json:"data"`
							} `json:"pub"`
						}
						if b := r["push"]; b != nil {
							if json.Unmarshal(b, &v) == nil && v.Pub.Data != "" {
								observe(v.Pub.Data)
							}
						}
					}
				}
			}(c, i == 1)
		}
		var sendMu sync.Mutex
		nextID := 2
		publish = func(id string) error {
			sendMu.Lock()
			defer sendMu.Unlock()
			nextID++
			c := clients[0]
			c.SetWriteDeadline(time.Now().Add(30 * time.Second))
			c.SetReadDeadline(time.Now().Add(30 * time.Second))
			if e := c.WriteJSON(map[string]any{"id": nextID, "publish": map[string]any{"channel": "bench", "data": id}}); e != nil {
				return e
			}
			return wsReply(c, nextID)
		}
	} else {
		panic("unknown service")
	}
	defer closeAll()
	setupSeconds := time.Since(setup).Seconds()
	fmt.Println("setup complete", *service, *users)
	mark("idle")
	time.Sleep(*idle)
	phases = make([]phase, 2)
	ackSamples = make([][]float64, 2)
	deliverySamples = make([][]float64, 2)
	var seq atomic.Int64
	for p, spec := range []struct {
		name string
		dur  time.Duration
		rate int
	}{{"steady", *duration, *rate}, {"burst", *burst, *burstRate}} {
		phases[p].Name = spec.name
		mark(spec.name)
		phases[p].OfferedRate = spec.rate
		start := time.Now()
		count := int(spec.dur.Seconds() * float64(spec.rate))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 64)
		for i := 0; i < count; i++ {
			target := start.Add(time.Duration(float64(i) / float64(spec.rate) * 1e9))
			if delay := time.Until(target); delay > 0 {
				time.Sleep(delay)
			}
			sem <- struct{}{}
			id := fmt.Sprintf("%017d", seq.Add(1))
			t := time.Now()
			mu.Lock()
			entries[id] = &entry{started: t, phase: p}
			phases[p].Offered++
			mu.Unlock()
			wg.Add(1)
			go func(id string, t time.Time, p int) {
				defer wg.Done()
				defer func() { <-sem }()
				e := publish(id)
				mu.Lock()
				defer mu.Unlock()
				if e != nil {
					phases[p].PublishErrors++
				} else {
					phases[p].Accepted++
					ackSamples[p] = append(ackSamples[p], float64(time.Since(t))/1e6)
				}
			}(id, t, p)
		}
		wg.Wait()
		phases[p].Elapsed = time.Since(start).Seconds()
		phases[p].ActualRate = float64(phases[p].Offered) / phases[p].Elapsed
		mark("drain")
		time.Sleep(3 * time.Second)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		pending := 0
		for _, e := range entries {
			if !e.received {
				pending++
			}
		}
		mu.Unlock()
		if pending == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range phases {
		phases[i].AckMS = quantiles(ackSamples[i])
		phases[i].DeliveryMS = quantiles(deliverySamples[i])
	}
	b, e := json.MarshalIndent(map[string]any{"service": *service, "connections": *users, "setup_seconds": setupSeconds, "payload_bytes": 17, "percentile_method": "nearest rank ceil(p*n)-1", "phases": phases, "connection_or_receive_errors": connectionErrors.Load(), "generator_max_inflight": 64}, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(*out, append(b, '\n'), 0600); e != nil {
		panic(e)
	}
	// Preserve failing evidence, then fail the runner so loss cannot pass acceptance.
	if e := validateResults(phases, connectionErrors.Load()); e != nil {
		panic(e)
	}
}
