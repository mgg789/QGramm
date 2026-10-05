//go:build qg_bench_profile

package core

import (
	"database/sql"
	"sync/atomic"
	"time"
)

// Query lifetime includes preparation/pool waits, execution and result
// consumption until Scan/Close. It is not isolated SQLite CPU time.
type diagnosticRow struct {
	*sql.Row
	core    *Core
	started time.Time
	done    atomic.Bool
}

func (r *diagnosticRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if r.done.CompareAndSwap(false, true) {
		r.core.observeSQL(diagnosticElapsed(r.started))
	}
	return err
}

type diagnosticRows struct {
	*sql.Rows
	core    *Core
	started time.Time
	done    atomic.Bool
}

func (r *diagnosticRows) Close() error {
	err := r.Rows.Close()
	if r.done.CompareAndSwap(false, true) {
		r.core.observeSQL(diagnosticElapsed(r.started))
	}
	return err
}

func (c *Core) wrapReadRow(row *sql.Row, started time.Time) *diagnosticRow {
	return &diagnosticRow{Row: row, core: c, started: started}
}

func (c *Core) wrapReadRows(rows *sql.Rows, err error, started time.Time) (*diagnosticRows, error) {
	if err != nil {
		c.observeSQL(diagnosticElapsed(started))
		return nil, err
	}
	return &diagnosticRows{Rows: rows, core: c, started: started}, nil
}
