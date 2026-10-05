package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func parseSteps(s string) ([]Step, error) {
	var out []Step
	for i, part := range strings.Split(s, ",") {
		pieces := strings.Split(part, ":")
		if len(pieces) != 2 {
			return nil, fmt.Errorf("step must be rate:duration")
		}
		rate, err := strconv.Atoi(pieces[0])
		if err != nil || rate < 1 {
			return nil, fmt.Errorf("invalid rate")
		}
		d, err := time.ParseDuration(pieces[1])
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid duration")
		}
		if float64(rate)*d.Seconds() < 1 {
			return nil, fmt.Errorf("step must offer at least one message")
		}
		out = append(out, Step{fmt.Sprintf("step-%d", i), rate, d})
	}
	return out, nil
}
func validate(c Config) error {
	if c.Chats < 1 || c.Fanout < 1 || c.Users < c.Chats*(c.Fanout+1) {
		return fmt.Errorf("users must cover disjoint sender + fanout recipients for each chat")
	}
	if c.PayloadBytes < 17 || c.PayloadBytes > 16<<20 {
		return fmt.Errorf("payload bytes must be 17..16777216")
	}
	if c.Workers < 1 || c.Workers > 256 {
		return fmt.Errorf("workers must be 1..256")
	}
	if c.ReconnectCount < 0 || c.ReconnectCount > c.Chats*c.Fanout {
		return fmt.Errorf("invalid reconnect count")
	}
	if c.ReconnectCount > 0 && len(c.Steps) < 2 {
		return fmt.Errorf("reconnect requires a second phase")
	}
	if c.Idle < 0 || c.Drain < 0 || c.Drain > 30*time.Second || c.Offline < 0 {
		return fmt.Errorf("invalid idle/drain/offline duration")
	}
	if c.ResponseMode != "full" && c.ResponseMode != "minimal" {
		return fmt.Errorf("response-mode must be full or minimal")
	}
	if c.FileBytes < 0 || c.FileCount < 1 || (c.FileBytes > 0 && (c.Service != "qgramm" || c.Chats != 1 || c.Fanout != 1)) {
		return fmt.Errorf("file scenario requires qgramm, chats=1, fanout=1, file-count>=1 and nonnegative file-bytes")
	}
	return nil
}
func execute() int {
	var c Config
	var steps, out string
	flag.StringVar(&c.Service, "service", "qgramm", "qgramm|nats|centrifugo")
	flag.StringVar(&c.URL, "url", "http://127.0.0.1:8080", "service endpoint")
	flag.StringVar(&c.EnvFile, "env-file", "", "private fixture env file")
	flag.StringVar(&c.ResponseMode, "response-mode", "full", "full|minimal")
	flag.IntVar(&c.Users, "users", 1000, "connected clients")
	flag.IntVar(&c.Chats, "chats", 100, "disjoint active conversations")
	flag.IntVar(&c.Fanout, "fanout", 1, "receivers per conversation")
	flag.IntVar(&c.PayloadBytes, "payload-bytes", 256, "identical plaintext payload bytes")
	flag.IntVar(&c.Workers, "workers", 256, "maximum concurrent publish requests")
	flag.StringVar(&steps, "steps", "500:20s,1500:20s,3000:20s,100:20s", "open-loop rate:duration phases")
	flag.DurationVar(&c.Idle, "idle", 10*time.Second, "connected idle observation")
	flag.DurationVar(&c.Drain, "drain", 30*time.Second, "maximum post-load drain")
	flag.IntVar(&c.ReconnectCount, "reconnect", 0, "first N real recipients to disconnect in second phase")
	flag.DurationVar(&c.Offline, "offline", 0, "offline duration, zero means entire second phase")
	flag.StringVar(&c.PhaseFile, "phase-file", "", "phase marker path")
	flag.StringVar(&out, "out", "", "JSON output, stdout if omitted")
	flag.Int64Var(&c.FileBytes, "file-bytes", 0, "resumable QGramm attachment size; zero selects messaging scenario")
	flag.IntVar(&c.FileCount, "file-count", 2, "number of files in attachment scenario")
	flag.Parse()
	c.Secret = os.Getenv("BENCH_SECRET")
	var err error
	c.Steps, err = parseSteps(steps)
	if err == nil {
		err = validate(c)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.FileBytes > 0 {
		r, runErr := runFileScenario(ctx, c)
		if err := writeResult(out, r); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "file scenario failed; see JSON evidence")
			return 1
		}
		return 0
	}
	e := newEngine(c)
	e.phase("setup")
	var a Adapter
	switch c.Service {
	case "qgramm":
		a, err = NewQGramm(ctx, c, e.receive)
	case "nats":
		a, err = NewNATS(ctx, c, e.receive)
	case "centrifugo":
		a, err = NewCentrifugo(ctx, c, e.receive)
	default:
		err = fmt.Errorf("unknown service")
	}
	if err != nil {
		r := e.result
		r.Error = "setup: " + err.Error()
		writeResult(out, r)
		return 1
	}
	r := e.run(ctx, a)
	if err = a.Close(); err != nil && r.Error == "" {
		r.Error = "close: " + err.Error()
	}
	if err = writeResult(out, r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if failed(r) {
		return 1
	}
	return 0
}
func writeResult(path string, r any) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0644)
}
func main() { os.Exit(execute()) }
