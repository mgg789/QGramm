package core

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	_ "modernc.org/sqlite"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_versions(version INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, disabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS devices(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),public_key TEXT NOT NULL,signing_key TEXT NOT NULL DEFAULT '',revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS chats(id TEXT PRIMARY KEY,kind TEXT NOT NULL,mode TEXT NOT NULL,seq INTEGER NOT NULL DEFAULT 0,epoch INTEGER NOT NULL DEFAULT 0,pending INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS members(chat_id TEXT NOT NULL REFERENCES chats(id),user_id TEXT NOT NULL REFERENCES users(id),role TEXT NOT NULL,can_send INTEGER NOT NULL,joined_seq INTEGER NOT NULL,active INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(chat_id,user_id));
CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,chat_id TEXT NOT NULL REFERENCES chats(id),sender TEXT NOT NULL,device_id TEXT NOT NULL,operation_id TEXT NOT NULL,seq INTEGER NOT NULL,revision INTEGER NOT NULL DEFAULT 1,deleted INTEGER NOT NULL DEFAULT 0,payload BLOB NOT NULL,metadata BLOB NOT NULL,epoch INTEGER NOT NULL,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS messages_chat_seq ON messages(chat_id,seq);
CREATE TABLE IF NOT EXISTS events(chat_id TEXT NOT NULL REFERENCES chats(id),seq INTEGER NOT NULL,kind TEXT NOT NULL,message_id TEXT NOT NULL DEFAULT '',data BLOB,created_at INTEGER NOT NULL,PRIMARY KEY(chat_id,seq));
CREATE TABLE IF NOT EXISTS operations(device_id TEXT NOT NULL,operation_id TEXT NOT NULL,hash TEXT NOT NULL,result BLOB NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(device_id,operation_id));
CREATE TABLE IF NOT EXISTS cursors(device_id TEXT NOT NULL,chat_id TEXT NOT NULL,delivered INTEGER NOT NULL DEFAULT 0,read INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(device_id,chat_id));
CREATE TABLE IF NOT EXISTS tickets(hash TEXT PRIMARY KEY,device_id TEXT NOT NULL,user_id TEXT NOT NULL,expires INTEGER NOT NULL);
INSERT OR IGNORE INTO schema_versions VALUES(1);`

func Open(cfg config.Config, compiled []string) (*Core, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	actual := map[string]bool{}
	for _, name := range Registered() {
		actual[name] = true
	}
	expected := map[string]bool{}
	for _, name := range compiled {
		if name != "" {
			expected[name] = true
		}
	}
	for _, name := range cfg.Features.Enabled() {
		if !actual[name] || !expected[name] {
			return nil, fmt.Errorf("module %s absent from build", name)
		}
	}
	if len(cfg.Features.Enabled()) != len(expected) || len(actual) != len(expected) {
		return nil, errors.New("TOML features differ from compiled manifest")
	}
	key, err := secretKey(cfg.Security.TokenPublicKeyEnv, ed25519.PublicKeySize)
	if err != nil {
		return nil, err
	}
	master, err := secretKey(cfg.Security.MasterKeyEnv, 32)
	if err != nil {
		return nil, err
	}
	hpke, err := secretKey(cfg.Security.HPKEKeyEnv, 32)
	if err != nil {
		return nil, err
	}
	management := os.Getenv(cfg.Security.ManagementSecretEnv)
	if len(management) < 32 {
		return nil, errors.New("management secret must contain at least 32 characters")
	}
	loadRetired := func(refs []string) ([][]byte, error) {
		keys := make([][]byte, 0, len(refs))
		for _, ref := range refs {
			key, e := secretKey(ref, 32)
			if e != nil {
				return nil, e
			}
			keys = append(keys, key)
		}
		return keys, nil
	}
	previousMasters, err := loadRetired(cfg.Security.PreviousMasterKeyEnvs)
	if err != nil {
		return nil, err
	}
	previousHPKE, err := loadRetired(cfg.Security.PreviousHPKEKeyEnvs)
	if err != nil {
		return nil, err
	}
	engine, err := cryptoenc.NewWithPrevious(master, hpke, previousMasters, previousHPKE)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(cfg.Storage.Path), 0700); err != nil {
		return nil, err
	}
	dsn, err := sqliteDSN(cfg.Storage.Path, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA secure_delete=ON"} {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(cfg.Storage.Path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Core{httpSlots: make(chan struct{}, cfg.Capacity.Workers*4), httpPending: make(chan struct{}, cfg.Capacity.Workers*8), DB: db, Config: cfg, Engine: engine, Mux: http.NewServeMux(), Context: ctx, cancel: cancel, verifyKey: key, managementSecret: management, connections: map[string]map[*connection]struct{}{}}
	if cfg.Storage.Path != ":memory:" {
		readDSN, e := sqliteDSN(cfg.Storage.Path, true)
		if e == nil {
			c.readDB, e = sql.Open("sqlite", readDSN)
		}
		if e == nil {
			readers := min(8, max(1, cfg.Capacity.Workers))
			c.readDB.SetMaxOpenConns(readers)
			c.readDB.SetMaxIdleConns(readers)
			e = c.readDB.PingContext(ctx)
		}
		if e != nil {
			cancel()
			if c.readDB != nil {
				c.readDB.Close()
			}
			db.Close()
			return nil, e
		}
	}
	c.routes()
	for _, name := range cfg.Features.Enabled() {
		if err = registry[name](c); err != nil {
			c.Close()
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
	}
	if err := c.prepareHotWrites(); err != nil {
		c.Close()
		return nil, err
	}
	c.writer = newMessageWriter(c)
	if err := c.startCheckpointer(); err != nil {
		c.Close()
		return nil, err
	}
	go c.replayLoop()
	go c.cleanupLoop()
	go c.connectionAuthLoop()
	return c, nil
}

// Every physical connection receives its own safety PRAGMAs. A separate
// read-only pool uses WAL snapshots while the single writer serializes commits.
func sqliteDSN(path string, readOnly bool) (string, error) {
	if path == ":memory:" {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := url.Values{}
	for _, pragma := range []string{"foreign_keys(1)", "busy_timeout(5000)", "synchronous(FULL)", "secure_delete(1)"} {
		q.Add("_pragma", pragma)
	}
	if readOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Core) reader() *sql.DB {
	if c.readDB != nil {
		return c.readDB
	}
	return c.DB
}

// ReadStats exposes pool wait counters to in-process diagnostic tooling.
func (c *Core) ReadStats() sql.DBStats { return c.reader().Stats() }
func secretKey(name string, n int) ([]byte, error) {
	v, err := base64.StdEncoding.DecodeString(os.Getenv(name))
	if err != nil || len(v) != n {
		return nil, fmt.Errorf("%s must reference a base64 %d-byte key", name, n)
	}
	return v, nil
}
func (c *Core) Close() error {
	c.cancel()
	if c.writer != nil {
		c.writer.close()
	}
	if c.checkpoint != nil {
		<-c.checkpoint.done
		c.checkpoint.db.Close()
	}
	c.readStatements.close()
	c.writeStatements.close()
	c.mu.Lock()
	for _, set := range c.connections {
		for conn := range set {
			conn.close()
		}
	}
	c.mu.Unlock()
	var readErr error
	if c.readDB != nil {
		readErr = c.readDB.Close()
	}
	return errors.Join(readErr, c.DB.Close())
}
func (c *Core) Member(ctx context.Context, user, chat string) (string, bool, int64, error) {
	var role string
	var send bool
	var joined int64
	err := c.readQueryRow(ctx, `SELECT role,can_send,joined_seq FROM members WHERE chat_id=? AND user_id=? AND active=1`, chat, user).Scan(&role, &send, &joined)
	return role, send, joined, err
}
func (c *Core) Publish(ctx context.Context, chat, kind, message string, data any) (int64, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, err := c.Append(ctx, tx, chat, kind, message, data)
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}
func (c *Core) Append(ctx context.Context, tx *sql.Tx, chat, kind, message string, data any) (int64, error) {
	var seq int64
	if err := c.txQueryRow(ctx, tx, `UPDATE chats SET seq=seq+1 WHERE id=? RETURNING seq`, chat).Scan(&seq); err != nil {
		return 0, err
	}
	var stored []byte
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return 0, err
		}
		stored, err = c.Engine.Seal(raw, []byte(fmt.Sprintf("event/%s/%d", chat, seq)))
		if err != nil {
			return 0, err
		}
	}
	_, err := c.txExec(ctx, tx, `INSERT INTO events(chat_id,seq,kind,message_id,data,created_at) VALUES(?,?,?,?,?,?)`, chat, seq, kind, message, stored, time.Now().Unix())
	return seq, err
}
func (c *Core) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-c.Context.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.Context, 20*time.Second)
			_, _ = c.DB.ExecContext(ctx, `DELETE FROM tickets WHERE expires<?`, time.Now().Unix())
			_, _ = c.DB.ExecContext(ctx, `DELETE FROM operations WHERE created_at<?`, time.Now().Add(-time.Duration(c.Config.Policy.DedupRetentionHours)*time.Hour).Unix())
			_, _ = c.DB.ExecContext(ctx, `DELETE FROM events WHERE created_at<?`, time.Now().Add(-time.Duration(c.Config.Policy.EventRetentionHours)*time.Hour).Unix())
			for _, fn := range c.Cleanup {
				_ = fn(ctx)
			}
			cancel()
		}
	}
}
