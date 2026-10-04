//go:build !qg_bench_profile

package core

import (
	"database/sql"
	"time"
)

// Aliases keep production queries unchanged, without wrappers or timers.
type diagnosticRow = sql.Row
type diagnosticRows = sql.Rows

func (c *Core) wrapReadRow(row *sql.Row, _ time.Time) *diagnosticRow { return row }
func (c *Core) wrapReadRows(rows *sql.Rows, err error, _ time.Time) (*diagnosticRows, error) {
	return rows, err
}
