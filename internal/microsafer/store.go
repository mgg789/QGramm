//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
	_ "modernc.org/sqlite"
)

const storeSchema = 2

// Store is an encrypted durable state store.  SQLite metadata (table names,
// row IDs and timestamps) is intentionally small; payloads and MLS snapshots
// are encrypted with the endpoint's local master key.
type Store struct {
	db         *sql.DB
	engine     *cryptoenc.Engine
	lock       *ownerLock
	path       string
	maxPending int
	mu         sync.Mutex
}

func OpenStore(c Config) (*Store, error) {
	master, err := decodeSecretEnv(c.Endpoint.MasterKeyEnv)
	if err != nil {
		return nil, err
	}
	hpke, err := decodeSecretEnv(c.Endpoint.HPKEKeyEnv)
	if err != nil {
		return nil, err
	}
	return OpenStoreWithKeys(c, master, hpke)
}

func OpenStoreWithKeys(c Config, master, hpke []byte) (*Store, error) {
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if len(master) != 32 || len(hpke) != 32 {
		return nil, errors.New("microsafer: master and HPKE keys must be 32 bytes")
	}
	var err error
	dir := filepath.Dir(c.Endpoint.DBPath)
	if dir != "." {
		if err = ensureDir(dir); err != nil {
			return nil, err
		}
	}
	lock, err := acquireOwnerLock(c.Endpoint.DBPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", c.Endpoint.DBPath)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, lock: lock, path: c.Endpoint.DBPath, maxPending: c.Endpoint.MaxPending}
	s.engine, err = cryptoenc.New(master, hpke)
	if err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err = s.init(); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	return s, nil
}

func ensureDir(path string) error { return os.MkdirAll(path, 0700) }

func (s *Store) init() error {
	_, err := s.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS microsafer_meta (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_state (name TEXT PRIMARY KEY, value BLOB NOT NULL, revision INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_intents (client_id TEXT PRIMARY KEY, request_hash BLOB NOT NULL, operation_id TEXT NOT NULL UNIQUE, status TEXT NOT NULL, value BLOB NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_cursors (chat TEXT PRIMARY KEY, value BLOB NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_inbox (operation_id TEXT PRIMARY KEY, chat TEXT NOT NULL, epoch INTEGER NOT NULL, sender_user TEXT NOT NULL, sender_device TEXT NOT NULL, payload BLOB NOT NULL, status TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_grants (nonce TEXT PRIMARY KEY, grant_hash BLOB NOT NULL, consumed_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS microsafer_outbox (operation_id TEXT PRIMARY KEY, seq INTEGER NOT NULL, epoch INTEGER NOT NULL DEFAULT 0, wire BLOB NOT NULL, sent INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);`)
	if err != nil {
		return err
	}
	var version int
	err = s.db.QueryRow(`SELECT value FROM microsafer_meta WHERE key='schema_version'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// Version 1 databases predate the durable outbox epoch. Detect the
		// column before migrating so startup never mutates an unknown schema.
		rows, qerr := s.db.Query(`PRAGMA table_info(microsafer_outbox)`)
		if qerr != nil {
			return qerr
		}
		hasEpoch := false
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var def any
			if qerr = rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); qerr != nil {
				_ = rows.Close()
				return qerr
			}
			if name == "epoch" {
				hasEpoch = true
			}
		}
		if qerr = rows.Close(); qerr != nil {
			return qerr
		}
		if !hasEpoch {
			if _, qerr = s.db.Exec(`ALTER TABLE microsafer_outbox ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`); qerr != nil {
				return qerr
			}
		}
		_, err = s.db.Exec(`INSERT INTO microsafer_meta(key,value) VALUES('schema_version',?)`, storeSchema)
		return err
	}
	if err != nil {
		return err
	}
	if version > storeSchema {
		return fmt.Errorf("microsafer: store schema %d is newer than supported %d", version, storeSchema)
	}
	if version == 1 {
		if err = s.addOutboxEpochIfMissing(); err != nil {
			return err
		}
		_, err = s.db.Exec(`UPDATE microsafer_meta SET value=? WHERE key='schema_version'`, storeSchema)
		return err
	}
	if version < storeSchema {
		return fmt.Errorf("microsafer: unsupported store schema migration %d to %d", version, storeSchema)
	}
	return err
}

func (s *Store) addOutboxEpochIfMissing() error {
	rows, err := s.db.Query(`PRAGMA table_info(microsafer_outbox)`)
	if err != nil {
		return err
	}
	hasEpoch := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "epoch" {
			hasEpoch = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if !hasEpoch {
		_, err = s.db.Exec(`ALTER TABLE microsafer_outbox ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`)
	}
	return err
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if e := s.lock.Close(); err == nil {
		err = e
	}
	return err
}
func (s *Store) DB() *sql.DB               { return s.db }
func (s *Store) Engine() *cryptoenc.Engine { return s.engine }
func (s *Store) Path() string              { return s.path }

func (s *Store) seal(kind string, value []byte) ([]byte, error) {
	return s.engine.Seal(value, []byte("microsafer/"+kind+"/v1"))
}
func (s *Store) open(kind string, value []byte) ([]byte, error) {
	return s.engine.Open(value, []byte("microsafer/"+kind+"/v1"))
}

func (s *Store) PutState(ctx context.Context, name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	enc, err := s.seal("state/"+name, b)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = s.db.ExecContext(ctx, `INSERT INTO microsafer_state(name,value,revision,updated_at) VALUES(?,?,1,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value,revision=microsafer_state.revision+1,updated_at=excluded.updated_at`, name, enc, now)
	return err
}
func (s *Store) PutStateTx(ctx context.Context, tx *sql.Tx, name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	enc, err := s.seal("state/"+name, b)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO microsafer_state(name,value,revision,updated_at) VALUES(?,?,1,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value,revision=microsafer_state.revision+1,updated_at=excluded.updated_at`, name, enc, now)
	return err
}
func (s *Store) GetState(ctx context.Context, name string, out any) error {
	var b []byte
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM microsafer_state WHERE name=?`, name).Scan(&b); err != nil {
		return err
	}
	p, err := s.open("state/"+name, b)
	if err != nil {
		return err
	}
	return json.Unmarshal(p, out)
}
func (s *Store) DeleteState(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM microsafer_state WHERE name=?`, name)
	return err
}

type intentRow struct {
	ClientID, OperationID, Status string
	Hash, Value                   []byte
}

func (s *Store) getIntent(ctx context.Context, clientID string) (intentRow, error) {
	var x intentRow
	err := s.db.QueryRowContext(ctx, `SELECT client_id,request_hash,operation_id,status,value FROM microsafer_intents WHERE client_id=?`, clientID).Scan(&x.ClientID, &x.Hash, &x.OperationID, &x.Status, &x.Value)
	if err != nil {
		return x, err
	}
	x.Value, err = s.open("intent/"+clientID, x.Value)
	return x, err
}
func (s *Store) PutIntent(ctx context.Context, clientID, hash, operationID, status string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	enc, err := s.seal("intent/"+clientID, b)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = s.db.ExecContext(ctx, `INSERT INTO microsafer_intents(client_id,request_hash,operation_id,status,value,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, clientID, []byte(hash), operationID, status, enc, now, now)
	return err
}
func (s *Store) PutIntentTx(ctx context.Context, tx *sql.Tx, clientID string, hash []byte, operationID, status string, value any) error {
	if s.maxPending > 0 {
		var retained int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_intents`).Scan(&retained); err != nil {
			return err
		}
		if retained >= s.maxPending*4 {
			return errors.New("microsafer: retained intent capacity exhausted; operator reset required")
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_intents WHERE status IN ('dispatched','uncertain')`).Scan(&pending); err != nil {
			return err
		}
		if pending >= s.maxPending {
			return errors.New("microsafer: durable pending intent capacity exhausted")
		}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	enc, err := s.seal("intent/"+clientID, b)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO microsafer_intents(client_id,request_hash,operation_id,status,value,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, clientID, hash, operationID, status, enc, now, now)
	return err
}
func (s *Store) UpdateIntent(ctx context.Context, clientID, status string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	enc, err := s.seal("intent/"+clientID, b)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE microsafer_intents SET status=?,value=?,updated_at=? WHERE client_id=?`, status, enc, time.Now().UnixNano(), clientID)
	return err
}

type InboxItem struct {
	OperationID, Chat, SenderUser, SenderDevice, Status string
	Epoch                                               uint64
	Request                                             RPCRequest
}

func (s *Store) PutInboxTx(ctx context.Context, tx *sql.Tx, item InboxItem) error {
	if s.maxPending > 0 {
		var retained int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_inbox`).Scan(&retained); err != nil {
			return err
		}
		if retained >= s.maxPending*4 {
			return errors.New("microsafer: retained inbox capacity exhausted; operator reset required")
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_inbox WHERE status='pending'`).Scan(&pending); err != nil {
			return err
		}
		if pending >= s.maxPending {
			return errors.New("microsafer: durable pending inbox capacity exhausted")
		}
	}
	b, err := json.Marshal(item.Request)
	if err != nil {
		return err
	}
	enc, err := s.seal("inbox/"+item.OperationID, b)
	if err != nil {
		return err
	}
	now := time.Now().UnixNano()
	_, err = tx.ExecContext(ctx, `INSERT INTO microsafer_inbox(operation_id,chat,epoch,sender_user,sender_device,payload,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(operation_id) DO NOTHING`, item.OperationID, item.Chat, item.Epoch, item.SenderUser, item.SenderDevice, enc, item.Status, now, now)
	return err
}
func (s *Store) GetInbox(ctx context.Context, op string) (InboxItem, error) {
	var x InboxItem
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation_id,chat,epoch,sender_user,sender_device,payload,status FROM microsafer_inbox WHERE operation_id=?`, op).Scan(&x.OperationID, &x.Chat, &x.Epoch, &x.SenderUser, &x.SenderDevice, &b, &x.Status)
	if err != nil {
		return x, err
	}
	plain, err := s.open("inbox/"+op, b)
	if err != nil {
		return x, err
	}
	err = json.Unmarshal(plain, &x.Request)
	return x, err
}
func (s *Store) UpdateInbox(ctx context.Context, op, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE microsafer_inbox SET status=?,updated_at=? WHERE operation_id=?`, status, time.Now().UnixNano(), op)
	return err
}
func (s *Store) PendingInbox(ctx context.Context, chat string) ([]InboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id,chat,epoch,sender_user,sender_device,payload,status FROM microsafer_inbox WHERE chat=? AND status=? ORDER BY created_at`, chat, "pending")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InboxItem
	for rows.Next() {
		var x InboxItem
		var b []byte
		if err = rows.Scan(&x.OperationID, &x.Chat, &x.Epoch, &x.SenderUser, &x.SenderDevice, &b, &x.Status); err != nil {
			return nil, err
		}
		plain, e := s.open("inbox/"+x.OperationID, b)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(plain, &x.Request); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) ConsumeGrantTx(ctx context.Context, tx *sql.Tx, nonce string, hash []byte) error {
	if nonce == "" || len(hash) == 0 {
		return errors.New("microsafer: invalid grant nonce")
	}
	if s.maxPending > 0 {
		var retained int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_grants`).Scan(&retained); err != nil {
			return err
		}
		if retained >= s.maxPending*4 {
			return errors.New("microsafer: retained grant capacity exhausted; operator reset required")
		}
	}
	now := time.Now().UnixNano()
	res, err := tx.ExecContext(ctx, `INSERT INTO microsafer_grants(nonce,grant_hash,consumed_at) VALUES(?,?,?)`, nonce, hash, now)
	if err != nil {
		return fmt.Errorf("microsafer: grant already consumed: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("microsafer: grant was not consumed")
	}
	return nil
}

func (s *Store) SaveOutboxTx(ctx context.Context, tx *sql.Tx, operationID string, seq uint64, wire []byte) error {
	return s.SaveOutboxEpochTx(ctx, tx, operationID, seq, 0, wire)
}
func (s *Store) SaveOutboxEpochTx(ctx context.Context, tx *sql.Tx, operationID string, seq, epoch uint64, wire []byte) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_outbox WHERE operation_id=?`, operationID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	if s.maxPending > 0 {
		var retained int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_outbox`).Scan(&retained); err != nil {
			return err
		}
		if retained >= s.maxPending*8 {
			return errors.New("microsafer: retained outbox capacity exhausted; operator reset required")
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM microsafer_outbox WHERE sent=0`).Scan(&pending); err != nil {
			return err
		}
		if pending >= s.maxPending {
			return errors.New("microsafer: durable pending outbox capacity exhausted")
		}
	}
	enc, err := s.seal("outbox/"+operationID, wire)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO microsafer_outbox(operation_id,seq,epoch,wire,sent,created_at) VALUES(?,?,?, ?,0,?) ON CONFLICT(operation_id) DO NOTHING`, operationID, seq, epoch, enc, time.Now().UnixNano())
	return err
}
func (s *Store) MarkOutboxSent(ctx context.Context, operationID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE microsafer_outbox SET sent=1 WHERE operation_id=?`, operationID)
	return err
}
func (s *Store) PendingOutbox(ctx context.Context) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id,seq,epoch,wire FROM microsafer_outbox WHERE sent=0 ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var x OutboxItem
		var b []byte
		if err = rows.Scan(&x.OperationID, &x.Seq, &x.Epoch, &b); err != nil {
			return nil, err
		}
		x.Wire, err = s.open("outbox/"+x.OperationID, b)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

type OutboxItem struct {
	OperationID string
	Seq         uint64
	Epoch       uint64
	Wire        []byte
}

func (s *Store) GetCursor(ctx context.Context, chat string) (string, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM microsafer_cursors WHERE chat=?`, chat).Scan(&b)
	if err != nil {
		return "", err
	}
	p, err := s.open("cursor/"+chat, b)
	return string(p), err
}
func (s *Store) SetCursorTx(ctx context.Context, tx *sql.Tx, chat, cursor string) error {
	enc, err := s.seal("cursor/"+chat, []byte(cursor))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO microsafer_cursors(chat,value,updated_at) VALUES(?,?,?) ON CONFLICT(chat) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, chat, enc, time.Now().UnixNano())
	return err
}
