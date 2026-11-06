package base

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
)

// concurrency.go settles how many connections a restore writes over, from the
// migration's Concurrency (core.ConcurrencyFrom) and what the target can take.

// MaxRestoreConnections is the most connections a restore writes over at once,
// whatever the migration asks for. Past about twice the target's cores the
// writers only queue for its CPU and its log, and a restore is not the only
// thing a target serves.
const MaxRestoreConnections = 16

// RestoreConnections returns how many connections a restore should write over,
// and why that is less than the migration asked for when it is: the
// migration's Concurrency, at most MaxRestoreConnections, and at most half the
// connections the server has free (free < 0: unknown, not limited).
func RestoreConnections(ctx context.Context, free int) (int, string) {
	want := core.ConcurrencyFrom(ctx)
	n, note := want, ""
	if n > MaxRestoreConnections {
		n, note = MaxRestoreConnections, fmt.Sprintf("capped from %d: at most %d restore connections", want, MaxRestoreConnections)
	}
	if free >= 0 {
		if half := max(1, free/2); n > half {
			n, note = half, fmt.Sprintf("capped from %d: %d connections free on the target", want, free)
		}
	}
	return n, note
}

// MySQLFreeConnections returns how many more connections a MySQL-family server
// takes — max_connections less those open — or -1 when conn cannot read them.
func MySQLFreeConnections(ctx context.Context, conn *sql.Conn) int {
	var maxConns int
	if err := conn.QueryRowContext(ctx, "SELECT @@max_connections").Scan(&maxConns); err != nil {
		logger.Info("restore could not read max_connections", "err", err.Error())
		return -1
	}
	var name, value string
	if err := conn.QueryRowContext(ctx, "SHOW GLOBAL STATUS LIKE 'Threads_connected'").Scan(&name, &value); err != nil {
		logger.Info("restore could not read Threads_connected", "err", err.Error())
		return -1
	}
	open, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return max(0, maxConns-open)
}

// ReportConcurrency reports how many connections a restore settled on, and why
// that is less than the migration asked for when it is. Once, before the rows.
func ReportConcurrency(ctx context.Context, n int, note string) {
	core.ReportConcurrency(ctx, n, note)
}

// ReportSingleStream reports a restore that runs one stream whatever the
// migration asked for — a CLI fed over SSH executes its input in order, on one
// session — saying which one.
func ReportSingleStream(ctx context.Context, why string) {
	core.ReportConcurrency(ctx, 1, why)
}
