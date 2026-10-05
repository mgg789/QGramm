package core

import "context"

// projectBatchResults shares the ACL query and recipient parsing for the normal
// full-response path. Bounds match writer groups; projection never grows with
// the configured HTTP batch limit. Hooks can change access between items, so
// retain sequential reads for them. A failed shared projection also falls back
// to individual reads, preserving partial errors and exact retry semantics.
func (c *Core) projectBatchResults(ctx context.Context, id Identity, results []messageWriteResult) {
	indices := make([]int, 0, maxWriteBatch)
	ids := make([]string, 0, maxWriteBatch)
	flush := func() {
		if len(indices) == 0 {
			return
		}
		unique := make(map[string]bool, len(ids))
		shared := len(c.Project) == 0
		for _, messageID := range ids {
			if unique[messageID] {
				// A duplicate response still gets its own randomized HPKE envelope.
				shared = false
			}
			unique[messageID] = true
		}
		var projected map[string]Message
		var err error
		if shared {
			projected, err = c.viewMessages(ctx, id, ids)
		}
		for _, i := range indices {
			if shared && err == nil {
				results[i].message = projected[results[i].message.ID]
			} else {
				results[i].message, results[i].err = c.sendResult(ctx, id, results[i].message, false, results[i].repeated)
			}
		}
		indices, ids = indices[:0], ids[:0]
	}
	for i := range results {
		if results[i].err != nil {
			continue
		}
		indices = append(indices, i)
		ids = append(ids, results[i].message.ID)
		if len(indices) == maxWriteBatch {
			flush()
		}
	}
	flush()
}
