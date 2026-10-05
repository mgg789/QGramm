package core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func cleanupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = applyMigrations(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRetentionMigrationOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err = applyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err = applyMigrations(context.Background(), db); err != nil {
		t.Fatal("migration is not idempotent:", err)
	}

	var version int
	if err = db.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	for _, index := range []string{"tickets_expires", "operations_created_at", "events_created_at"} {
		var found int
		if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Fatalf("index %s was not migrated", index)
		}
	}
}

func insertRetentionRows(t *testing.T, db *sql.DB, spec retentionCleanupSpec, count int, timestamp int64) {
	t.Helper()
	for i := 0; i < count; i++ {
		var err error
		suffix := strconv.FormatInt(timestamp, 10) + "-" + strconv.Itoa(i)
		switch spec.table {
		case "tickets":
			_, err = db.Exec(`INSERT INTO tickets(hash,device_id,user_id,expires) VALUES(?,?,?,?)`, "ticket-"+suffix, "device", "user", timestamp)
		case "operations":
			_, err = db.Exec(`INSERT INTO operations(device_id,operation_id,hash,result,created_at) VALUES(?,?,?,?,?)`, "device-"+suffix, "operation", "hash", []byte("result"), timestamp)
		case "events":
			_, err = db.Exec(`INSERT INTO events(chat_id,seq,kind,created_at) VALUES(?,?,?,?)`, "chat-"+suffix, i, "test", timestamp)
		default:
			t.Fatalf("unknown retention table %q", spec.table)
		}
		if err != nil {
			t.Fatalf("insert %s row %d: %v", spec.table, i, err)
		}
	}
}

func TestRetentionPlansUseIndexes(t *testing.T) {
	db := cleanupTestDB(t)
	for _, spec := range []retentionCleanupSpec{
		{table: "tickets", column: "expires", index: "tickets_expires"},
		{table: "operations", column: "created_at", index: "operations_created_at"},
		{table: "events", column: "created_at", index: "events_created_at"},
	} {
		insertRetentionRows(t, db, spec, 2000, 2000)
		insertRetentionRows(t, db, spec, 10, 100)
		rows, err := db.Query(`EXPLAIN QUERY PLAN `+retentionDeleteSQL(spec), 1000, retentionCleanupBatchSize)
		if err != nil {
			t.Fatal(spec.table, err)
		}
		var plan string
		for rows.Next() {
			var id, parent, detailCode int
			var detail string
			if err = rows.Scan(&id, &parent, &detailCode, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan, spec.index) {
			t.Fatalf("%s plan does not use %s:\n%s", spec.table, spec.index, plan)
		}
	}
}

func countRetentionRows(t *testing.T, db *sql.DB, spec retentionCleanupSpec, cutoff int64) int {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM " + spec.table + " WHERE " + spec.column + "<?"
	if err := db.QueryRow(query, cutoff).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countRetentionAt(t *testing.T, db *sql.DB, spec retentionCleanupSpec, timestamp int64) int {
	t.Helper()
	var count int
	query := "SELECT COUNT(*) FROM " + spec.table + " WHERE " + spec.column + "=?"
	if err := db.QueryRow(query, timestamp).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRetentionCleanupIsBoundedAndKeepsBoundary(t *testing.T) {
	db := cleanupTestDB(t)
	cutoff := int64(1000)
	for _, spec := range []retentionCleanupSpec{
		{table: "tickets", column: "expires", index: "tickets_expires"},
		{table: "operations", column: "created_at", index: "operations_created_at"},
		{table: "events", column: "created_at", index: "events_created_at"},
	} {
		insertRetentionRows(t, db, spec, retentionCleanupBatchSize+10, cutoff-1)
		insertRetentionRows(t, db, spec, 1, cutoff)
		insertRetentionRows(t, db, spec, 1, cutoff+1)
		deleted, err := deleteRetentionBatch(context.Background(), db, spec, cutoff, retentionCleanupBatchSize)
		if err != nil {
			t.Fatal(spec.table, err)
		}
		if deleted != retentionCleanupBatchSize {
			t.Fatalf("%s deleted %d rows, want %d", spec.table, deleted, retentionCleanupBatchSize)
		}
		if got := countRetentionRows(t, db, spec, cutoff); got != 10 {
			t.Fatalf("%s has %d expired rows after first batch, want 10", spec.table, got)
		}
		if got := countRetentionAt(t, db, spec, cutoff); got != 1 {
			t.Fatalf("%s removed exact-boundary row", spec.table)
		}
		if got := countRetentionAt(t, db, spec, cutoff+1); got != 1 {
			t.Fatalf("%s removed live row", spec.table)
		}
		deleted, err = deleteRetentionBatch(context.Background(), db, spec, cutoff, retentionCleanupBatchSize)
		if err != nil {
			t.Fatal(spec.table, err)
		}
		if deleted != 10 || countRetentionRows(t, db, spec, cutoff) != 0 {
			t.Fatalf("%s did not make bounded cleanup progress: deleted=%d remaining=%d", spec.table, deleted, countRetentionRows(t, db, spec, cutoff))
		}
	}
}

func TestRetentionCleanupDrainsBacklogInBoundedBatches(t *testing.T) {
	db := cleanupTestDB(t)
	spec := retentionCleanupSpec{table: "tickets", column: "expires", index: "tickets_expires"}
	cutoff := int64(1000)
	insertRetentionRows(t, db, spec, retentionCleanupBatchSize*2+1, cutoff-1)
	if err := cleanupRetention(context.Background(), db, []retentionCleanup{{spec: spec, cutoff: cutoff}}); err != nil {
		t.Fatal(err)
	}
	if got := countRetentionRows(t, db, spec, cutoff); got != 0 {
		t.Fatalf("cleanup left %d expired rows", got)
	}
}

func TestRetentionCleanupHonorsCancellation(t *testing.T) {
	db := cleanupTestDB(t)
	spec := retentionCleanupSpec{table: "tickets", column: "expires", index: "tickets_expires"}
	insertRetentionRows(t, db, spec, 10, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deleted, err := deleteRetentionBatch(ctx, db, spec, 2, retentionCleanupBatchSize)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup error = %v, want context.Canceled", err)
	}
	if deleted != 0 || countRetentionRows(t, db, spec, 2) != 10 {
		t.Fatalf("cancelled cleanup changed rows: deleted=%d remaining=%d", deleted, countRetentionRows(t, db, spec, 2))
	}
}
