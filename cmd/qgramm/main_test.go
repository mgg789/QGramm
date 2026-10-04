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
