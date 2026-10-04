package core

import (
	"sync"
	"testing"
)

func TestSharedReplayStablePlanAndChatSetGeneration(t *testing.T) {
	f := newFixture(t)
	first := &connection{wake: make(chan struct{}, 1)}
	second := &connection{wake: make(chan struct{}, 1)}
	f.c.subscribe(first, "chat", true)
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	generation := f.c.replayPlan.generation
	args := &f.c.replayPlan.batches[0].args[0]
	f.c.replayMu.Unlock()
	// More devices in the same chat neither change the SQL shape nor rebuild
	// the per-chat buffers. Repeated subscriptions/unsubscriptions are harmless.
	f.c.subscribe(second, "chat", true)
	f.c.subscribe(second, "chat", true)
	f.c.subscribe(first, "chat", false)
	f.c.subscribe(first, "chat", false)
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	if f.c.replayPlan.generation != generation || &f.c.replayPlan.batches[0].args[0] != args {
		t.Error("stable chat set rebuilt poll arguments")
	}
	f.c.replayMu.Unlock()
	// No persistent snapshot, SQL arguments, observations or scratch map remains
	// after the final subscriber leaves; a subsequent first subscriber rebuilds.
	f.c.subscribe(second, "chat", false)
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	if f.c.replayPlan.batches != nil || f.c.replayPlan.heads != nil || f.c.replayHeads != nil {
		t.Error("empty subscriptions retained poll buffers")
	}
	f.c.replayMu.Unlock()
	f.c.subscribe(first, "chat", true)
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	if f.c.replayPlan.generation == generation || len(f.c.replayHeads) != 1 {
		t.Error("resubscription failed to refresh poll snapshot")
	}
	f.c.replayMu.Unlock()
	f.c.subscribe(first, "chat", false)
}

func TestSharedReplayConcurrentChatSetChurn(t *testing.T) {
	f := newFixture(t)
	conn := &connection{wake: make(chan struct{}, 1)}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			f.c.subscribe(conn, "chat", true)
			f.c.subscribe(conn, "missing", true)
			f.c.subscribe(conn, "chat", false)
			f.c.subscribe(conn, "missing", false)
		}
	}()
	for i := 0; i < 100; i++ {
		f.c.pollReplayHeads()
	}
	wg.Wait()
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	defer f.c.replayMu.Unlock()
	if len(f.c.replayHeads) != 0 || len(f.c.replayPlan.batches) != 0 || f.c.replayPlan.heads != nil {
		t.Fatal("subscription churn retained departed chats or buffers")
	}
}
