//go:build qg_redis

package modules

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
)

func TestRedisRealPubSubFailureAndShutdown(t *testing.T) {
	binary := testRedisBinary(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan string, 4)
	cfg := config.Defaults().Redis
	cfg.Binary = binary
	b, err := newRedisBroker(ctx, cfg, func(chat string) { received <- chat })
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	for name, want := range map[string]string{"port": "0", "save": "", "appendonly": "no", "maxmemory": "67108864"} {
		settings, err := b.client.ConfigGet(ctx, name).Result()
		if err != nil || settings[name] != want {
			t.Fatalf("unexpected Redis runtime setting %s: %v %v", name, settings, err)
		}
	}
	dir := b.dir
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private directory: %v %v", info, err)
	}
	info, err = os.Stat(dir + "/redis.sock")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private socket: %v %v", info, err)
	}
	assertWake := func(chat string) {
		t.Helper()
		b.wake(chat)
		select {
		case actual := <-received:
			if actual != chat {
				t.Fatalf("wake=%q want=%q", actual, chat)
			}
		case <-time.After(time.Second):
			t.Fatal("notification lost")
		}
	}
	assertWake("healthy-pubsub")
	caps := map[string]any{}
	b.capabilities(caps)
	if caps["redis"].(map[string]any)["status"] != "ready" {
		t.Fatal("healthy broker not reported")
	}
	if err := b.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("child not reaped")
	}
	assertWake("failed-local-fallback")
	b.capabilities(caps)
	if caps["redis"].(map[string]any)["status"] != "degraded_local_fallback" {
		t.Fatal("dead broker not reported")
	}
	b.close()
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
	b.close() // Idempotent; never waits on an already reaped child.
}

func TestRedisMissingBinaryFailsStartup(t *testing.T) {
	cfg := config.Defaults().Redis
	cfg.Binary = "/nonexistent/qgramm-redis-server"
	if _, err := newRedisBroker(context.Background(), cfg, func(string) {}); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestRedisFullQueueFallsBackWithoutBlocking(t *testing.T) {
	called := false
	b := &redisBroker{queue: make(chan string, 1), local: func(string) { called = true }}
	b.healthy.Store(true)
	b.queue <- "full"
	b.wake("fallback")
	if !called {
		t.Fatal("full queue lost wakeup")
	}
}
