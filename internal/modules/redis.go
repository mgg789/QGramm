//go:build qg_redis

package modules

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/redis/go-redis/v9"
)

const redisWakeChannel = "qgramm:wake"

func init() {
	core.Register("redis", func(c *core.Core) error {
		broker, err := newRedisBroker(c.Context, c.Config.Redis, c.WakeLocal)
		if err != nil {
			return err
		}
		c.OnWake = append(c.OnWake, broker.wake)
		c.AddShutdown(broker.close)
		c.ExtendCapabilities = append(c.ExtendCapabilities, broker.capabilities)
		return nil
	})
}

// Redis carries only chat IDs as wake hints. SQLite remains the authoritative
// event journal and the independent one-second replay poll remains active.
type redisBroker struct {
	client    *redis.Client
	pubsub    *redis.PubSub
	cmd       *exec.Cmd
	dir       string
	queue     chan string
	local     func(string)
	healthy   atomic.Bool
	done      chan struct{}
	workers   sync.WaitGroup
	closeOnce sync.Once
}

func newRedisBroker(ctx context.Context, cfg config.Redis, local func(string)) (*redisBroker, error) {
	dir, err := os.MkdirTemp("", "qgramm-redis-")
	if err != nil {
		return nil, err
	}
	b := &redisBroker{dir: dir, queue: make(chan string, cfg.QueueDepth), local: local, done: make(chan struct{})}
	socket := filepath.Join(dir, "redis.sock")
	b.cmd = exec.Command(cfg.Binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "600", "--protected-mode", "yes", "--daemonize", "no", "--save", "", "--appendonly", "no", "--maxmemory", strconv.Itoa(cfg.MaxMemoryMB)+"mb", "--maxmemory-policy", "noeviction", "--dir", dir, "--loglevel", "warning")
	b.cmd.Stdout, b.cmd.Stderr = io.Discard, io.Discard
	if err = b.cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start embedded Redis: %w", err)
	}
	go func() { _ = b.cmd.Wait(); b.healthy.Store(false); close(b.done) }()
	b.client = redis.NewClient(&redis.Options{Network: "unix", Addr: socket, DialTimeout: 10 * time.Millisecond, ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, PoolSize: 2, MaxRetries: -1, ContextTimeoutEnabled: true, DisableIdentity: true})
	startup, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		if b.client.Ping(startup).Err() == nil {
			break
		}
		select {
		case <-startup.Done():
			b.close()
			return nil, fmt.Errorf("embedded Redis startup timed out")
		case <-b.done:
			b.close()
			return nil, fmt.Errorf("embedded Redis exited during startup")
		case <-time.After(20 * time.Millisecond):
		}
	}
	b.pubsub = b.client.Subscribe(startup, redisWakeChannel)
	if _, err = b.pubsub.Receive(startup); err != nil {
		b.close()
		return nil, fmt.Errorf("subscribe embedded Redis: %w", err)
	}
	b.healthy.Store(true)
	b.workers.Add(2)
	go func() {
		defer b.workers.Done()
		defer b.healthy.Store(false)
		for {
			msg, err := b.pubsub.ReceiveMessage(ctx)
			if err != nil {
				return
			}
			if msg.Channel == redisWakeChannel && len(msg.Payload) > 0 && len(msg.Payload) <= 128 {
				b.local(msg.Payload)
			}
		}
	}()
	go func() {
		defer b.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.done:
				return
			case chat := <-b.queue:
				request, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
				n, err := b.client.Publish(request, redisWakeChannel, chat).Result()
				cancel()
				if err != nil || n == 0 {
					b.local(chat)
				}
			}
		}
	}()
	return b, nil
}

func (b *redisBroker) wake(chat string) {
	if !b.healthy.Load() {
		b.local(chat)
		return
	}
	select {
	case b.queue <- chat:
	default:
		b.local(chat)
	}
}

func (b *redisBroker) close() {
	b.closeOnce.Do(func() {
		b.healthy.Store(false)
		if b.pubsub != nil {
			_ = b.pubsub.Close()
		}
		if b.client != nil {
			_ = b.client.Close()
		}
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Signal(os.Interrupt)
			select {
			case <-b.done:
			case <-time.After(time.Second):
				_ = b.cmd.Process.Kill()
				<-b.done
			}
		}
		b.workers.Wait()
		_ = os.RemoveAll(b.dir)
	})
}

func (b *redisBroker) capabilities(caps map[string]any) {
	status := "degraded_local_fallback"
	if b.healthy.Load() {
		status = "ready"
	}
	caps["redis"] = map[string]any{"status": status, "durable": false}
}
