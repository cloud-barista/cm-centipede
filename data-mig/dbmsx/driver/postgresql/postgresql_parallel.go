package postgresql

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	base "github.com/cloud-barista/cm-centipede/data-mig/dbmsx/driver/base"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// =============================================================================
// Parallel COPY — the rows of several tables loaded at once
// =============================================================================
//
// One connection loads one table at a time, and over a WAN most of a COPY is
// waiting on the round trip. The tables of a dump's data section do not depend
// on one another — their foreign keys and triggers are held back until the rows
// are in (see restoreViaSQL) — so their COPY blocks can go over connections of
// their own.
//
// The dump is still read once, front to back. A COPY block is spooled to a file
// beside the dump and handed to a free connection; the reader goes on to the
// next while it loads. At most as many blocks as connections are loading and
// one more is being spooled, so the staging disk holds that many tables' rows
// beyond the dump at most.
//
// Everything other than COPY stays on the restore's own connection, in order.
// Before such a statement runs every COPY in flight is finished (wait): it may
// read the rows. pg_dump's setval calls between the blocks are the exception —
// a sequence's value does not depend on any table's rows — so they do not hold
// the loading back.

// pgCopier loads COPY blocks over connections of its own.
type pgCopier struct {
	ctx      context.Context
	loc      base.DBMSLocation
	dir      string // where blocks are spooled: beside the dump
	progress *base.RestoreProgress
	notify   func(string)

	n     int
	conns []*pgx.Conn
	free  chan *pgx.Conn
	g     *errgroup.Group
	gctx  context.Context
}

// newPgCopier opens n connections to loc, each set up as the restore's own:
// session is what the restore connection has run so far that a COPY reads under
// (SET and set_config), replica says whether session_replication_role took.
func (d *PostgreSQLDriver) newPgCopier(ctx context.Context, loc base.DBMSLocation, n int, session []string, replica bool,
	dir string, progress *base.RestoreProgress, notify func(string)) (*pgCopier, error) {
	c := &pgCopier{ctx: ctx, loc: loc, dir: dir, progress: progress, notify: notify, n: n, free: make(chan *pgx.Conn, n)}
	for range n {
		conn, err := d.openConn(ctx, loc)
		if err == nil {
			err = d.setRestoreSearchPath(ctx, conn, loc)
		}
		if err == nil && replica {
			_, err = conn.Exec(ctx, "SET session_replication_role = replica")
		}
		for _, stmt := range session {
			if err != nil {
				break
			}
			_, err = conn.Exec(ctx, stmt)
		}
		if err != nil {
			if conn != nil {
				conn.Close(ctx)
			}
			c.close()
			return nil, fmt.Errorf("open copy connection: %w", err)
		}
		c.conns = append(c.conns, conn)
		c.free <- conn
	}
	c.reset()
	return c, nil
}

// reset starts a new group of loads. Its limit is what keeps the spooling one
// block ahead: Go waits for a load to finish before the next block is taken.
func (c *pgCopier) reset() {
	c.g, c.gctx = errgroup.WithContext(c.ctx)
	c.g.SetLimit(c.n)
}

// copy spools the block header introduces — rows, the index-th statement of
// the dump — and loads it over the next free connection. It returns once the
// block is spooled, or with the error a load before it failed with.
func (c *pgCopier) copy(header string, index int, rows io.Reader) error {
	if err := c.gctx.Err(); err != nil {
		return err // a block has failed; wait says which
	}
	f, err := os.CreateTemp(c.dir, "copy-*.tsv")
	if err != nil {
		return fmt.Errorf("spool copy block: %w", err)
	}
	if _, err := io.Copy(f, rows); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("spool copy block: %w", err)
	}
	table := pgExtractCopyTable(header)
	c.g.Go(func() error {
		defer os.Remove(f.Name())
		defer f.Close()
		if err := c.gctx.Err(); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		conn := <-c.free
		defer func() { c.free <- conn }()
		// Bytes are counted as they load; the table switch was counted by the
		// reader, in the dump's order, so the load does not name the table.
		tag, err := conn.PgConn().CopyFrom(c.gctx, &pgCopyProgress{r: f, p: c.progress}, header)
		if err != nil {
			return base.RestoreStatementError(
				base.DBMSTypePostgreSQL, c.loc.Database, header, index, c.progress.Statements(), err)
		}
		c.progress.Statement(len(header), tag.RowsAffected(), "")
		if c.notify != nil && table != "" {
			c.notify(table)
		}
		return nil
	})
	return nil
}

// wait finishes every block in flight and returns the first failure. The
// copier takes blocks again after it.
func (c *pgCopier) wait() error {
	err := c.g.Wait()
	c.reset()
	return err
}

// execAll runs a session setting met after the blocks began on every copy
// connection, so the blocks after it are read under it too. Called after wait.
func (c *pgCopier) execAll(stmt string) error {
	for _, conn := range c.conns {
		if _, err := conn.Exec(c.ctx, stmt); err != nil {
			return fmt.Errorf("copy connection: %s: %w", stmt, err)
		}
	}
	return nil
}

func (c *pgCopier) close() {
	for _, conn := range c.conns {
		conn.Close(context.WithoutCancel(c.ctx))
	}
	c.conns = nil
}

// pgIsSessionSetting reports whether stmt sets a session parameter a COPY may be
// read under — client_encoding, standard_conforming_strings, search_path — as
// SET or pg_dump's set_config(..., false). SET LOCAL ends with its transaction.
func pgIsSessionSetting(stmt string) bool {
	s := strings.ToUpper(strings.TrimSpace(stmt))
	return (strings.HasPrefix(s, "SET ") && !strings.HasPrefix(s, "SET LOCAL ")) ||
		strings.HasPrefix(s, "SELECT PG_CATALOG.SET_CONFIG(")
}

// pgIsSetval reports whether stmt is pg_dump's sequence position, which depends
// on no table's rows and need not wait for them.
func pgIsSetval(stmt string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "SELECT PG_CATALOG.SETVAL(")
}

// pgFreeConnections returns how many more connections the server takes for an
// ordinary role — max_connections less the reserved ones and those open — or
// -1 when conn cannot read them.
func pgFreeConnections(ctx context.Context, conn *pgx.Conn) int {
	var free int
	err := conn.QueryRow(ctx, `SELECT current_setting('max_connections')::int
		- current_setting('superuser_reserved_connections')::int
		- (SELECT count(*) FROM pg_stat_activity)::int`).Scan(&free)
	if err != nil {
		logger.Info("postgresql restore could not read free connections", "err", err.Error())
		return -1
	}
	return max(0, free)
}

// spoolDir is where restoreViaSQL spools COPY blocks: beside the dump, on the
// staging disk the operator sized for it.
func spoolDir(inputPath string) string { return filepath.Dir(inputPath) }
