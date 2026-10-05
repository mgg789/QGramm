// qgramm-bench is development acceptance tooling, not a shipped client SDK.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type client struct {
	base, management string
	private          ed25519.PrivateKey
	engine           *cryptoenc.Engine
	http             *http.Client
	responseMode     string
}

func (c *client) token(user string) string {
	now := time.Now()
	token, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "qgramm", "aud": "qgramm", "sub": user, "device_id": user, "iat": now.Add(-5 * time.Second).Unix(), "exp": now.Add(14 * time.Minute).Unix()}).SignedString(c.private)
	return token
}
func (c *client) request(method, path, user string, body any) ([]byte, int, error) {
	raw, _ := json.Marshal(body)
	r, err := http.NewRequest(method, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Proto", "https")
	if c.responseMode == "minimal" && method == "POST" && (strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/messages/batch")) {
		r.Header.Set("Prefer", "return=minimal")
	}
	token := c.management
	if user != "" {
		token = c.token(user)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	response, err := c.http.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return data, response.StatusCode, err
}
func initEnv(path string) error {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	random := func(n int) string { b := make([]byte, n); rand.Read(b); return base64.StdEncoding.EncodeToString(b) }
	data := "QGRAMM_TOKEN_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(public) + "\nQGRAMM_BENCH_SIGNING_KEY=" + base64.StdEncoding.EncodeToString(private) + "\nQGRAMM_MASTER_KEY=" + random(32) + "\nQGRAMM_HPKE_KEY=" + random(32) + "\nQGRAMM_MANAGEMENT_SECRET=" + random(32) + "\n"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(data)
	return err
}
func run() error {
	envFile := flag.String("env", "", "private synthetic test environment file")
	init := flag.Bool("init", false, "create test environment only")
	base := flag.String("url", "http://127.0.0.1:8080", "isolated test server")
	users := flag.Int("users", 10000, "distinct concurrent users/sockets")
	groupSize := flag.Int("group-size", 2, "active chat members; above two requires groups build")
	duration := flag.Duration("duration", 20*time.Second, "steady traffic duration")
	rate := flag.Int("rate", 100, "steady offered messages/sec")
	burst := flag.Duration("burst", 5*time.Second, "burst duration")
	burstRate := flag.Int("burst-rate", 1000, "burst offered messages/sec")
	out := flag.String("out", "", "JSON evidence path")
	batchSize := flag.Int("batch-size", 1, "messages per HTTP request; rates count messages, not requests")
	responseMode := flag.String("response-mode", "full", "message acknowledgement: full or minimal (Prefer: return=minimal)")
	phaseFile := flag.String("phase-file", "", "private workload phase marker for resource sampling")
	idle := flag.Duration("idle", 0, "idle connected interval before traffic")
	idleSubscriptions := flag.Bool("idle-subscriptions", false, "subscribe every inactive user to a paired empty direct chat")
	flag.Parse()
	if *responseMode != "full" && *responseMode != "minimal" {
		return fmt.Errorf("invalid response mode")
	}
	mark := func(name string) {
		if *phaseFile != "" {
			_ = os.WriteFile(*phaseFile, []byte(name), 0600)
		}
	}
	mark("setup")
	if *envFile == "" {
		return fmt.Errorf("-env required")
	}
	if *init {
		return initEnv(*envFile)
	}
	if *users < 2 || *users > 100000 || *rate < 1 || *burstRate < 1 || *groupSize < 2 || *groupSize > *users || *groupSize > 1000 {
		return fmt.Errorf("invalid workload")
	}
	if *batchSize < 1 || *batchSize > 16 || *rate%*batchSize != 0 || *burstRate%*batchSize != 0 {
		return fmt.Errorf("batch-size must be 1..16 and divide both message rates")
	}
	if *idleSubscriptions && (*groupSize != 2 || *users%2 != 0) {
		return fmt.Errorf("idle subscriptions require an even number of users and group-size=2")
	}
	raw, err := os.ReadFile(*envFile)
	if err != nil {
		return err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			env[k] = v
		}
	}
	private, err := base64.StdEncoding.DecodeString(env["QGRAMM_BENCH_SIGNING_KEY"])
	if err != nil || len(private) != 64 {
		return fmt.Errorf("test signing key invalid")
	}
	key := make([]byte, 32)
	rand.Read(key)
	engine, err := cryptoenc.New(key, key)
	if err != nil {
		return err
	}
	c := &client{base: *base, management: env["QGRAMM_MANAGEMENT_SECRET"], private: ed25519.PrivateKey(private), engine: engine, http: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128}}, responseMode: *responseMode}
	seed := make([]byte, 8)
	rand.Read(seed)
	prefix := "load-" + base64.RawURLEncoding.EncodeToString(seed) + "-"
	names := make([]string, *users)
	for i := range names {
		names[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	setupStart := time.Now()
	work := make(chan string)
	var wg sync.WaitGroup
	var setupError atomic.Bool
	var setupDiagnostic sync.Once
	provision := func(method, path string, body any) bool {
		for retry := 0; retry < 20; retry++ {
			_, status, e := c.request(method, path, "", body)
			if e == nil && (status == 200 || status == 201) {
				return true
			}
			if e == nil && status != 503 && status != 429 {
				setupDiagnostic.Do(func() { fmt.Fprintf(os.Stderr, "provisioning %s status=%d transport_error=%v\n", path, status, e) })
				return false
			}
			time.Sleep(25 * time.Millisecond)
		}
		return false
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range work {
				if !provision("PUT", "/management/v1/users/"+name, map[string]bool{"disabled": false}) || !provision("PUT", "/management/v1/users/"+name+"/devices/"+name, map[string]string{"public_key": engine.PublicKey()}) {
					setupError.Store(true)
				}
			}
		}()
	}
	for _, name := range names {
		work <- name
	}
	close(work)
	wg.Wait()
	if setupError.Load() {
		return fmt.Errorf("provisioning failed")
	}
	chat := prefix + "chat"
	chatRoute := "/management/v1/chats/direct"
	if *groupSize > 2 {
		chatRoute = "/management/v1/chats/groups"
	}
	if !provision("POST", chatRoute, map[string]any{"id": chat, "members": names[:*groupSize]}) {
		return fmt.Errorf("chat setup failed")
	}
	idleChats := make([]string, *users)
	if *idleSubscriptions {
		pairs := make(chan int)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range pairs {
					id := fmt.Sprintf("%sidle-%d", prefix, index/2)
					if !provision("POST", "/management/v1/chats/direct", map[string]any{"id": id, "members": names[index : index+2]}) {
						setupError.Store(true)
					}
					idleChats[index], idleChats[index+1] = id, id
				}
			}()
		}
		for i := 2; i < *users; i += 2 {
			pairs <- i
		}
		close(pairs)
		wg.Wait()
		if setupError.Load() {
			return fmt.Errorf("idle chat setup failed")
		}
	}
	caps, status, err := c.request("GET", "/v1/capabilities", names[0], nil)
	if err != nil || status != 200 {
		return fmt.Errorf("capabilities unavailable")
	}
	var capability struct {
		Key string `json:"server_key"`
	}
	json.Unmarshal(caps, &capability)
	pub, err := base64.StdEncoding.DecodeString(capability.Key)
	if err != nil {
		return err
	}
	connections := make([]*websocket.Conn, *users)
	var connected atomic.Int64
	var subscribed atomic.Int64
	var socketFailures atomic.Int64
	var unexpectedDisconnects atomic.Int64
	var websocketErrors atomic.Int64
	var closing atomic.Bool
	var delivered atomic.Int64
	var started sync.Map
	var metricMu sync.Mutex
	statusCounts := map[int]int{}
	backpressureReasons := map[string]int{}
	var sendDiagnostic sync.Once
	latencies := []float64{}
	deliveryLatencies := []float64{}
	steadyDeliveryLatencies := []float64{}
	type sampleStart struct {
		at     time.Time
		steady bool
	}
	var receiveWG sync.WaitGroup
	connectJobs := make(chan int)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range connectJobs {
				var conn *websocket.Conn
				for retry := 0; retry < 20; retry++ {
					data, status, e := c.request("POST", "/v1/ws-tickets", names[index], nil)
					if e == nil && status == 201 {
						var ticket struct {
							Ticket string `json:"ticket"`
						}
						json.Unmarshal(data, &ticket)
						url := "ws" + strings.TrimPrefix(c.base, "http") + "/v1/ws?ticket=" + ticket.Ticket
						headers := http.Header{"X-Forwarded-Proto": []string{"https"}}
						conn, _, e = websocket.DefaultDialer.Dial(url, headers)
						if e == nil {
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
				if conn == nil {
					socketFailures.Add(1)
					continue
				}
				connections[index] = conn
				connected.Add(1)
				if index > 0 && index < *groupSize || *idleSubscriptions {
					subscription := chat
					if index >= *groupSize {
						subscription = idleChats[index]
					}
					if e := conn.WriteJSON(map[string]any{"type": "subscribe", "chat_id": subscription, "after": 0}); e != nil {
						socketFailures.Add(1)
					} else {
						subscribed.Add(1)
					}
				}
				receiveWG.Add(1)
				go func(conn *websocket.Conn, measure bool) {
					defer receiveWG.Done()
					for {
						_, data, e := conn.ReadMessage()
						if e != nil {
							if !closing.Load() {
								unexpectedDisconnects.Add(1)
							}
							return
						}
						var event struct {
							Type string `json:"type"`
							Data struct {
								OperationID string `json:"operation_id"`
							}
						}
						if json.Unmarshal(data, &event) != nil {
							websocketErrors.Add(1)
							continue
						}
						if event.Type == "error" {
							websocketErrors.Add(1)
						}
						if measure && event.Type == "message.created" {
							delivered.Add(1)
							if stamp, ok := started.Load(event.Data.OperationID); ok {
								metricMu.Lock()
								sample := stamp.(sampleStart)
								latency := float64(time.Since(sample.at).Microseconds()) / 1000
								deliveryLatencies = append(deliveryLatencies, latency)
								if sample.steady {
									steadyDeliveryLatencies = append(steadyDeliveryLatencies, latency)
								}
								metricMu.Unlock()
							}
						}
					}
				}(conn, index > 0 && index < *groupSize)
			}
		}()
	}
	for i := range names {
		connectJobs <- i
	}
	close(connectJobs)
	wg.Wait()
	defer func() {
		closing.Store(true)
		for _, conn := range connections {
			if conn != nil {
				conn.Close()
			}
		}
		receiveWG.Wait()
	}()
	if connected.Load() != int64(*users) {
		return fmt.Errorf("connected %d/%d, failures %d", connected.Load(), *users, socketFailures.Load())
	}
	fmt.Printf("Established %d distinct-user sockets in %s\n", connected.Load(), time.Since(setupStart).Round(time.Millisecond))
	mark("idle")
	time.Sleep(*idle)
	var accepted, rejected, failed, offered, generatorSkipped atomic.Int64
	sem := make(chan struct{}, 64)
	send := func(op string, steady bool) {
		defer wg.Done()
		defer func() { <-sem }()
		stamp := time.Now()
		started.Store(op, sampleStart{stamp, steady})
		envelope, e := cryptoenc.SealEnvelope(pub, []byte("benchmark payload"), cryptoenc.Binding(chat, names[0], names[0], op))
		if e != nil {
			failed.Add(1)
			return
		}
		body, status, e := c.request("POST", "/v1/chats/"+chat+"/messages", names[0], map[string]any{"operation_id": op, "envelope": envelope})
		metricMu.Lock()
		statusCounts[status]++
		metricMu.Unlock()
		if e != nil {
			failed.Add(1)
		} else if status == 201 {
			if c.responseMode == "minimal" {
				var receipt struct {
					Status  string `json:"status"`
					Receipt struct {
						ID        string `json:"message_id"`
						Chat      string `json:"chat_id"`
						Operation string `json:"operation_id"`
						Seq       int64  `json:"seq"`
					} `json:"receipt"`
				}
				if json.Unmarshal(body, &receipt) != nil || receipt.Status != "accepted" || receipt.Receipt.ID == "" || receipt.Receipt.Chat != chat || receipt.Receipt.Operation != op || receipt.Receipt.Seq <= 0 {
					failed.Add(1)
					return
				}
			}
			accepted.Add(1)
			metricMu.Lock()
			latencies = append(latencies, float64(time.Since(stamp).Microseconds())/1000)
			metricMu.Unlock()
		} else if status == 503 || status == 429 {
			var response struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &response)
			reason := "other"
			// Only fixed public errors enter evidence; never arbitrary responses.
			switch response.Error {
			case "request capacity reached", "storage unavailable":
				reason = response.Error
			}
			metricMu.Lock()
			backpressureReasons[reason]++
			metricMu.Unlock()
			rejected.Add(1)
			started.Delete(op)
		} else {
			sendDiagnostic.Do(func() { fmt.Fprintf(os.Stderr, "unexpected send status=%d\n", status) })
			failed.Add(1)
			started.Delete(op)
		}
	}
	sendBatch := func(ops []string, steady bool) {
		defer wg.Done()
		defer func() { <-sem }()
		stamp := time.Now()
		inputs := make([]map[string]any, len(ops))
		for i, op := range ops {
			started.Store(op, sampleStart{stamp, steady})
			envelope, e := cryptoenc.SealEnvelope(pub, []byte("benchmark payload"), cryptoenc.Binding(chat, names[0], names[0], op))
			if e != nil {
				failed.Add(int64(len(ops)))
				return
			}
			inputs[i] = map[string]any{"operation_id": op, "envelope": envelope}
		}
		body, status, e := c.request("POST", "/v1/chats/"+chat+"/messages/batch", names[0], map[string]any{"messages": inputs})
		metricMu.Lock()
		statusCounts[status]++
		metricMu.Unlock()
		if e != nil {
			failed.Add(int64(len(ops)))
			return
		}
		if status == 429 || status == 503 {
			rejected.Add(int64(len(ops)))
			for _, op := range ops {
				started.Delete(op)
			}
			return
		}
		if status != 207 {
			failed.Add(int64(len(ops)))
			return
		}
		statuses, e := validateBatchReply(body, ops, chat, c.responseMode == "minimal")
		if e != nil {
			failed.Add(int64(len(ops)))
			return
		}
		elapsed := float64(time.Since(stamp).Microseconds()) / 1000
		for i, code := range statuses {
			if code == 201 {
				accepted.Add(1)
				metricMu.Lock()
				latencies = append(latencies, elapsed)
				metricMu.Unlock()
			} else {
				started.Delete(ops[i])
				if code == 429 || code == 503 {
					rejected.Add(1)
				} else {
					failed.Add(1)
				}
			}
		}
	}
	phase := func(d time.Duration, frequency int, steady bool) {
		timer := time.NewTimer(d)
		ticker := time.NewTicker(time.Second * time.Duration(*batchSize) / time.Duration(frequency))
		defer timer.Stop()
		defer ticker.Stop()
		for {
			select {
			case <-timer.C:
				wg.Wait()
				return
			case <-ticker.C:
				n := offered.Add(int64(*batchSize))
				select {
				case sem <- struct{}{}:
					wg.Add(1)
					if *batchSize == 1 {
						go send(fmt.Sprintf("%sop-%d", prefix, n), steady)
					} else {
						ops := make([]string, *batchSize)
						for i := range ops {
							ops[i] = fmt.Sprintf("%sop-%d", prefix, n-int64(*batchSize)+int64(i)+1)
						}
						go sendBatch(ops, steady)
					}
				default:
					generatorSkipped.Add(int64(*batchSize))
				}
			}
		}
	}
	loadStart := time.Now()
	mark("steady")
	phase(*duration, *rate, true)
	steadyAccepted := accepted.Load()
	steadyRejected := rejected.Load()
	metricMu.Lock()
	steadyLatency := append([]float64(nil), latencies...)
	metricMu.Unlock()
	mark("burst")
	phase(*burst, *burstRate, false)
	mark("drain")
	deadline := time.Now().Add(15 * time.Second)
	expectedDeliveries := accepted.Load() * int64(*groupSize-1)
	for delivered.Load() < expectedDeliveries && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	historyCount := 0
	mark("history")
	after := int64(0)
	for {
		data, status, e := c.request("GET", fmt.Sprintf("/v1/chats/%s/messages?after=%d&limit=200", chat, after), names[1], nil)
		if e != nil || status != 200 {
			return fmt.Errorf("history verification failed")
		}
		var messages []struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(data, &messages) != nil {
			return fmt.Errorf("history JSON invalid")
		}
		if len(messages) == 0 {
			break
		}
		historyCount += len(messages)
		after = messages[len(messages)-1].Seq
	}
	percentile := func(values []float64, quantile float64) float64 {
		if len(values) == 0 {
			return 0
		}
		sort.Float64s(values)
		return values[int(math.Ceil(float64(len(values))*quantile))-1]
	}
	metricMu.Lock()
	result := map[string]any{"users_connected": connected.Load(), "offered": offered.Load(), "accepted": accepted.Load(), "steady_accepted": steadyAccepted, "steady_backpressure": steadyRejected, "backpressure": rejected.Load(), "failed": failed.Load(), "status_counts": statusCounts, "generator_skipped": generatorSkipped.Load(), "delivered": delivered.Load(), "history_messages": historyCount, "accept_p95_ms": percentile(latencies, .95), "delivery_p95_ms": percentile(deliveryLatencies, .95), "steady_accept_p95_ms": percentile(steadyLatency, .95), "steady_delivery_p95_ms": percentile(steadyDeliveryLatencies, .95), "steady_seconds": duration.Seconds(), "steady_rate": *rate, "burst_seconds": burst.Seconds(), "burst_rate": *burstRate, "elapsed_seconds": time.Since(loadStart).Seconds(), "limitations": []string{"synthetic payload and shared recipient HPKE fixture key", fmt.Sprintf("%d users; one active %d-member conversation", *users, *groupSize), "HTTP behind isolated trusted proxy header; TLS CPU not measured", "generator runs outside server container; resource stats recorded separately"}}
	result["group_size"] = *groupSize
	result["response_mode"] = *responseMode
	result["batch_size"] = *batchSize
	result["batch_latency_origin"] = "Before HPKE preparation of the whole ready batch; assembly wait excluded; HTTP acknowledgement shared by elements"
	result["backpressure_reasons"] = backpressureReasons
	result["idle_seconds"] = idle.Seconds()
	result["idle_subscriptions"] = *idleSubscriptions
	result["subscription_commands_sent"] = subscribed.Load()
	if *idleSubscriptions {
		result["empty_subscribed_chats"] = *users/2 - 1
	}
	result["unexpected_disconnects"] = unexpectedDisconnects.Load()
	result["websocket_errors"] = websocketErrors.Load()
	result["accept_p99_ms"] = percentile(latencies, .99)
	result["delivery_p99_ms"] = percentile(deliveryLatencies, .99)
	result["steady_accept_p99_ms"] = percentile(steadyLatency, .99)
	result["steady_delivery_p99_ms"] = percentile(steadyDeliveryLatencies, .99)
	result["accept_samples"] = len(latencies)
	result["delivery_samples"] = len(deliveryLatencies)
	result["steady_delivery_samples"] = len(steadyDeliveryLatencies)
	result["percentile_method"] = "nearest rank: sorted samples[ceil(n*q)-1]; successful requests only; steady deliveries classified by send phase"

	data, _ := json.MarshalIndent(result, "", "  ")
	metricMu.Unlock()
	if *out != "" {
		if err = os.WriteFile(*out, append(data, '\n'), 0644); err != nil {
			return err
		}
	}
	fmt.Println(string(data))
	if historyCount != int(accepted.Load()) || delivered.Load() != expectedDeliveries || failed.Load() > 0 || unexpectedDisconnects.Load() > 0 || websocketErrors.Load() > 0 || socketFailures.Load() > 0 {
		return fmt.Errorf("load acceptance integrity failed")
	}
	return nil
}

// validateBatchReply checks individual outcomes before acceptance counters advance.
func validateBatchReply(body []byte, ops []string, chat string, minimal bool) ([]int, error) {
	var rows []struct {
		Operation string `json:"operation_id"`
		Status    int    `json:"status"`
		Message   struct {
			ID        string `json:"id"`
			Chat      string `json:"chat_id"`
			Operation string `json:"operation_id"`
			Seq       int64  `json:"seq"`
		} `json:"message"`
		Receipt struct {
			ID        string `json:"message_id"`
			Chat      string `json:"chat_id"`
			Operation string `json:"operation_id"`
			Seq       int64  `json:"seq"`
		} `json:"receipt"`
	}
	if json.Unmarshal(body, &rows) != nil || len(rows) != len(ops) {
		return nil, fmt.Errorf("invalid batch result count")
	}
	statuses := make([]int, len(rows))
	for i, row := range rows {
		if row.Operation != ops[i] || row.Status < 100 || row.Status > 599 {
			return nil, fmt.Errorf("invalid batch element")
		}
		if row.Status == 201 {
			id, ch, op, seq := row.Message.ID, row.Message.Chat, row.Message.Operation, row.Message.Seq
			if minimal {
				id, ch, op, seq = row.Receipt.ID, row.Receipt.Chat, row.Receipt.Operation, row.Receipt.Seq
			}
			if id == "" || ch != chat || op != ops[i] || seq <= 0 {
				return nil, fmt.Errorf("invalid accepted batch element")
			}
		}
		statuses[i] = row.Status
	}
	return statuses, nil
}
