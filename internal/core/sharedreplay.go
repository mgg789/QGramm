package core

import (
	"context"
	"strings"
	"time"
)

func (c *Core) replayLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Context.Done():
			return
		case <-ticker.C:
			c.pollReplayHeads()
		}
	}
}

// A single poll reads one head per subscribed chat, irrespective of the number
// of subscribers. Connection cursors and ACL checks remain in Events/flush.
func (c *Core) pollReplayHeads() {
	c.replayMu.Lock()
	defer c.replayMu.Unlock()
	c.mu.Lock()
	if c.replayPlan.generation != c.subscriptionGeneration {
		c.rebuildReplayPlan()
	}
	c.mu.Unlock()
	if len(c.replayPlan.batches) == 0 {
		return
	}
	for _, query := range c.replayPlan.batches {
		batch := query.chats
		ctx, cancel := context.WithTimeout(c.Context, 5*time.Second)
		heads, err := c.durableReplayHeads(ctx, query)
		cancel()
		if err != nil {
			// Preserve prior observations on read failure; retry next tick.
			continue
		}
		c.mu.Lock()
		for _, chat := range batch {
			if len(c.subscribers[chat]) == 0 {
				delete(c.replayHeads, chat)
				continue
			}
			head, found := heads[chat]
			previous, known := c.replayHeads[chat]
			if !found || !known || head != previous {
				// Missing chats also wake: Events rejects their access.
				for conn := range c.subscribers[chat] {
					conn.notify(chat)
				}
			}
			if found {
				c.replayHeads[chat] = head
			}
		}
		c.mu.Unlock()
	}
}

// The poll owns these buffers under replayMu. Subscription changes only bump
// a generation under mu; no connection retains or mutates this snapshot.
type replayPollPlan struct {
	generation uint64
	batches    []replayHeadQuery
	heads      map[string]int64
}

type replayHeadQuery struct {
	chats []string
	args  []any
	sql   string
}

// Called with both replayMu and mu held. Rebuilding on chat-set changes drops
// high-water buffers/maps, so retained memory follows the live subscription set.
func (c *Core) rebuildReplayPlan() {
	plan := replayPollPlan{generation: c.subscriptionGeneration}
	if len(c.subscribers) == 0 {
		c.replayPlan = plan
		c.replayHeads = nil
		return
	}
	chats := make([]string, 0, len(c.subscribers))
	observations := make(map[string]int64, len(c.subscribers))
	for chat := range c.subscribers {
		chats = append(chats, chat)
		if head, known := c.replayHeads[chat]; known {
			observations[chat] = head
		}
	}
	plan.heads = make(map[string]int64, min(500, len(chats)))
	plan.batches = make([]replayHeadQuery, 0, (len(chats)+499)/500)
	for start := 0; start < len(chats); start += 500 {
		batch := chats[start:min(start+500, len(chats))]
		args := make([]any, len(batch))
		for i, chat := range batch {
			args[i] = chat
		}
		query := `SELECT id,seq FROM chats WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + `)`
		plan.batches = append(plan.batches, replayHeadQuery{chats: batch, args: args, sql: query})
	}
	c.replayHeads = observations
	c.replayPlan = plan
}

func (c *Core) durableReplayHeads(ctx context.Context, query replayHeadQuery) (map[string]int64, error) {
	heads := c.replayPlan.heads
	clear(heads)
	rows, err := c.readQuery(ctx, query.sql, query.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chat string
	var head int64
	for rows.Next() {
		if err := rows.Scan(&chat, &head); err != nil {
			return nil, err
		}
		heads[chat] = head
	}
	return heads, rows.Err()
}
