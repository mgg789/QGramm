package core

import (
	"context"
	"database/sql"
	"sync"
)

// Entries remain alive until shutdown. Unknown shapes bypass the bounded cache.
type statementCache struct {
	mu      sync.Mutex
	entries map[string]*sql.Stmt
}

func (s *statementCache) get(ctx context.Context, db *sql.DB, query string, prepare bool) *sql.Stmt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stmt := s.entries[query]; stmt != nil {
		return stmt
	}
	if !prepare || len(s.entries) >= 128 {
		return nil
	}
	stmt, err := db.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}
	if s.entries == nil {
		s.entries = make(map[string]*sql.Stmt)
	}
	s.entries[query] = stmt
	return stmt
}
func (s *statementCache) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stmt := range s.entries {
		stmt.Close()
	}
}
func (c *Core) readQueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	c.observeRead()
	if stmt := c.readStatements.get(ctx, c.reader(), query, true); stmt != nil {
		return stmt.QueryRowContext(ctx, args...)
	}
	return c.reader().QueryRowContext(ctx, query, args...)
}
func (c *Core) readQuery(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.observeRead()
	if stmt := c.readStatements.get(ctx, c.reader(), query, true); stmt != nil {
		return stmt.QueryContext(ctx, args...)
	}
	return c.reader().QueryContext(ctx, query, args...)
}
func (c *Core) writeQueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	if stmt := c.writeStatements.get(ctx, c.DB, query, true); stmt != nil {
		return stmt.QueryRowContext(ctx, args...)
	}
	return c.DB.QueryRowContext(ctx, query, args...)
}
func (c *Core) writeExec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if stmt := c.writeStatements.get(ctx, c.DB, query, true); stmt != nil {
		return stmt.ExecContext(ctx, args...)
	}
	return c.DB.ExecContext(ctx, query, args...)
}
func (c *Core) txQueryRow(ctx context.Context, tx *sql.Tx, query string, args ...any) *sql.Row {
	if stmt := c.writeStatements.get(ctx, c.DB, query, false); stmt != nil {
		return tx.StmtContext(ctx, stmt).QueryRowContext(ctx, args...)
	}
	return tx.QueryRowContext(ctx, query, args...)
}
func (c *Core) txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if stmt := c.writeStatements.get(ctx, c.DB, query, false); stmt != nil {
		return tx.StmtContext(ctx, stmt).ExecContext(ctx, args...)
	}
	return tx.ExecContext(ctx, query, args...)
}
func (c *Core) prepareHotWrites() error {
	for _, query := range []string{
		`SELECT m.active AND d.revoked=0 AND u.disabled=0,m.can_send,c.epoch,c.pending FROM members m JOIN chats c ON c.id=m.chat_id JOIN devices d ON d.user_id=m.user_id JOIN users u ON u.id=m.user_id WHERE m.chat_id=? AND m.user_id=? AND d.id=?`,
		`SELECT hash,result FROM operations WHERE device_id=? AND operation_id=?`,
		`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		`INSERT INTO operations VALUES(?,?,?,?,?)`,
		`UPDATE chats SET seq=seq+1 WHERE id=? RETURNING seq`,
		`INSERT INTO events(chat_id,seq,kind,message_id,data,created_at) VALUES(?,?,?,?,?,?)`,
	} {
		if c.writeStatements.get(c.Context, c.DB, query, true) == nil {
			stmt, err := c.DB.PrepareContext(c.Context, query)
			if stmt != nil {
				stmt.Close()
			}
			return err
		}
	}
	return nil
}
