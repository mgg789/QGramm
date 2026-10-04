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
	chats := make([]string, 0, len(c.subscribers))
	for chat := range c.subscribers {
		chats = append(chats, chat)
	}
	// Prune even when no chats remain; memory follows live subscriptions.
	for chat := range c.replayHeads {
		if len(c.subscribers[chat]) == 0 {
			delete(c.replayHeads, chat)
		}
	}
	c.mu.Unlock()
	if len(chats) == 0 {
		return
	}
	if c.replayHeads == nil {
		c.replayHeads = make(map[string]int64, len(chats))
	}
	for start := 0; start < len(chats); start += 500 {
		batch := chats[start:min(start+500, len(chats))]
		ctx, cancel := context.WithTimeout(c.Context, 5*time.Second)
		heads, err := c.durableReplayHeads(ctx, batch)
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

func (c *Core) durableReplayHeads(ctx context.Context, chats []string) (map[string]int64, error) {
	args := make([]any, len(chats))
	marks := make([]string, len(chats))
	for i, chat := range chats {
		args[i], marks[i] = chat, "?"
	}
	rows, err := c.readQuery(ctx, `SELECT id,seq FROM chats WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := make(map[string]int64, len(chats))
	for rows.Next() {
		var chat string
		var head int64
		if err := rows.Scan(&chat, &head); err != nil {
			return nil, err
		}
		heads[chat] = head
	}
	return heads, rows.Err()
}
