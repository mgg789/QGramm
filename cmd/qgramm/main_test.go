package main

import (
	"database/sql"
	"github.com/mgg789/QGramm/internal/core"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupExclusiveAndPrivate(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE sample(value TEXT);INSERT INTO sample VALUES('example')`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if err = createBackup(&core.Core{DB: db}, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", info, err)
	}
	if err = createBackup(&core.Core{DB: db}, path); err == nil {
		t.Fatal("backup overwritten")
	}
	backup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var value string
	if err = backup.QueryRow(`SELECT value FROM sample`).Scan(&value); err != nil || value != "example" {
		t.Fatal("inconsistent backup", err)
	}
}

func TestOfflineBackupNeedsNoSecretsAndNeverCreatesSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source with spaces.db")
	destination := filepath.Join(dir, "backup.db")
	if err := createOfflineBackup(source, destination); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("missing source created")
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE sample(n INTEGER); INSERT INTO sample VALUES(42)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// This path bypasses Core.Open entirely: no secrets, installer or AI worker.
	if err = createOfflineBackup(source, destination); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var n int
	if err = backup.QueryRow(`SELECT n FROM sample`).Scan(&n); err != nil || n != 42 {
		t.Fatal("backup contents", n, err)
	}
}
