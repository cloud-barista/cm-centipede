package base

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
)

// rowwriters.go runs the row statements of a MySQL-family SQL restore over
// several connections at once.
//
// A restore over a WAN spends most of its time waiting: one INSERT goes out,
// the server writes it, the reply comes back, and only then does the next one
// leave. With the foreign key checks off, the rows of a dump do not depend on
// one another, so several INSERTs can be in flight at the same time.
//
// Everything else in the dump stays on the restore's own connection, in order.
// Before such a statement runs, every queued INSERT is finished and committed
// (Barrier): the DDL that follows may concern a table those rows went into, and
// an open transaction on another connection would hold its metadata lock.

// RestoreWriters is how many connections a MySQL-family restore writes rows over.
const RestoreWriters = 4

// rowWriterSetup is what every writer connection runs before its first row:
// the same session the restore connection itself sets up.
var rowWriterSetup = []string{
	"SET FOREIGN_KEY_CHECKS=0",
	"SET autocommit=0, unique_checks=0",
}

// RowWriters is a set of connections writing a restore's INSERTs in parallel.
type RowWriters struct {
	ctx     context.Context
	cancel  context.CancelFunc
	writers []*rowWriter
	jobs    chan rowJob
	done    func(n int, rows int64)
	wrap    func(stmt string, index int, err error) error

	pending sync.WaitGroup // queued statements not yet run
	running sync.WaitGroup // the writer goroutines
	closed  bool

	mu  sync.Mutex
	err error
}

// A writer commits far more often than the restore connection does, every
// rowWriterCommitStatements statements or rowWriterCommitBytes of SQL. Its
// open transaction holds locks the other writers may be waiting on: MariaDB,
// loading an empty table with the checks off, locks the whole table until the
// commit, and a transaction of the usual 16 MB kept the other writers - and a
// Barrier waiting on them - past innodb_lock_wait_timeout over a slow link.
// A megabyte is one statement of the SQL dump, so batching still pays for a
// dump of single-row INSERTs and costs nothing for one of multi-row ones.
const (
	rowWriterCommitStatements = 1000
	rowWriterCommitBytes      = 1 << 20
)

type rowWriter struct {
	// mu serialises the connection between the writer's goroutine and the
	// restore's own, which commits and replays session statements on it.
	mu      sync.Mutex
	conn    *sql.Conn
	commits *CommitBatcher
}

type rowJob struct {
	stmt  string
	index int
}

// NewRowWriters opens n connections from db, each set up as the restore's own,
// and starts writing on them. done is told of every statement written, its
// bytes and the rows it wrote; wrap turns a failed statement into the error the
// restore returns. Nothing is opened, and an error returned, when a connection
// cannot be; the restore can then go on with its own connection alone.
func NewRowWriters(ctx context.Context, db *sql.DB, n int,
	done func(n int, rows int64), wrap func(stmt string, index int, err error) error,
) (*RowWriters, error) {
	ctx, cancel := context.WithCancel(ctx)
	w := &RowWriters{ctx: ctx, cancel: cancel, jobs: make(chan rowJob, 2*n), done: done, wrap: wrap}
	for i := 0; i < n; i++ {
		conn, err := db.Conn(ctx)
		if err == nil {
			err = execAll(ctx, conn, rowWriterSetup)
			if err != nil {
				conn.Close()
			}
		}
		if err != nil {
			for _, rw := range w.writers {
				rw.conn.Close()
			}
			cancel()
			return nil, fmt.Errorf("open row writer: %w", err)
		}
		rw := &rowWriter{conn: conn}
		rw.commits = newCommitBatcherEvery(func() error {
			_, err := conn.ExecContext(ctx, "COMMIT")
			return err
		}, rowWriterCommitStatements, rowWriterCommitBytes)
		w.writers = append(w.writers, rw)
	}
	for _, rw := range w.writers {
		w.running.Add(1)
		go w.run(rw)
	}
	return w, nil
}

func execAll(ctx context.Context, conn *sql.Conn, stmts []string) error {
	for _, s := range stmts {
		if _, err := conn.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}

func (w *RowWriters) run(rw *rowWriter) {
	defer w.running.Done()
	for {
		var j rowJob
		var ok bool
		select {
		case j, ok = <-w.jobs:
		default:
			// Nothing queued: commit before waiting. A writer idling with its
			// transaction open keeps its locks, and the writers blocked on
			// them - and a Barrier waiting for those - would wait for good.
			w.commitIdle(rw)
			j, ok = <-w.jobs
		}
		if !ok {
			return
		}
		w.write(rw, j)
		w.pending.Done()
	}
}

func (w *RowWriters) commitIdle(rw *rowWriter) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if w.Err() != nil {
		return // rolled back by Close
	}
	if err := rw.commits.Flush(); err != nil {
		w.fail(fmt.Errorf("commit restored rows: %w", err))
	}
}

func (w *RowWriters) write(rw *rowWriter, j rowJob) {
	if w.Err() != nil {
		return // drain, so Insert never blocks on a queue nobody empties
	}
	rw.mu.Lock()
	defer rw.mu.Unlock()
	res, err := rw.conn.ExecContext(w.ctx, j.stmt)
	if err != nil {
		// Cancelled because another writer failed first: that failure is the
		// one to report, and wrap would log this one as a failure of its own.
		if w.ctx.Err() != nil {
			return
		}
		w.fail(w.wrap(j.stmt, j.index, err))
		return
	}
	if err := rw.commits.Done(len(j.stmt)); err != nil {
		w.fail(fmt.Errorf("commit restored rows: %w", err))
		return
	}
	rows, _ := res.RowsAffected()
	w.done(len(j.stmt), rows)
}

func (w *RowWriters) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
	w.cancel()
}

// Err returns the first error a writer met, if any.
func (w *RowWriters) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Insert queues stmt, the index-th statement of the dump, for the next free
// writer. It returns the first error any writer has met, so the restore stops
// reading the dump once one has.
func (w *RowWriters) Insert(stmt string, index int) error {
	if err := w.Err(); err != nil {
		return err
	}
	w.pending.Add(1)
	select {
	case w.jobs <- rowJob{stmt: stmt, index: index}:
		return nil
	case <-w.ctx.Done():
		w.pending.Done()
		if err := w.Err(); err != nil {
			return err
		}
		return w.ctx.Err()
	}
}

// Barrier waits for every queued statement to be written and commits what
// each writer has open, so the restore connection may run a statement of its
// own with no row in flight and no transaction holding a lock.
func (w *RowWriters) Barrier() error {
	w.pending.Wait()
	if err := w.Err(); err != nil {
		return err
	}
	// The writers have nothing left to write, but one may still be committing
	// on its way to idle: rw.mu waits for it.
	for _, rw := range w.writers {
		rw.mu.Lock()
		err := rw.commits.Flush()
		rw.mu.Unlock()
		if err != nil {
			w.fail(fmt.Errorf("commit restored rows: %w", err))
			return w.Err()
		}
	}
	return nil
}

// Session runs a session statement of the dump - SET, USE - on every writer,
// after a Barrier. The restore connection runs it as well: the INSERTs that
// follow are written in the session the dump set up, and mysqldump sets one up
// that decides what the rows mean (TIME_ZONE for a TIMESTAMP, SQL_MODE's
// NO_AUTO_VALUE_ON_ZERO for an id of 0).
func (w *RowWriters) Session(stmt string) error {
	if err := w.Barrier(); err != nil {
		return err
	}
	for _, rw := range w.writers {
		rw.mu.Lock()
		_, err := rw.conn.ExecContext(w.ctx, stmt)
		rw.mu.Unlock()
		if err != nil {
			w.fail(fmt.Errorf("replay %q on a row writer: %w", truncateStatement(stmt, statementSnippet), err))
			return w.Err()
		}
	}
	return nil
}

// Close stops the writers and closes their connections. With ok, what they
// have open is committed - Barrier's work, which a successful restore has
// already done - and otherwise rolled back. It is safe to call more than once.
func (w *RowWriters) Close(ok bool) error {
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if ok {
		err = w.Barrier()
	}
	close(w.jobs)
	w.running.Wait()
	for _, rw := range w.writers {
		if !ok || err != nil {
			// Not w.ctx: it is cancelled once a writer fails, and the rollback
			// still has to reach the server.
			rw.conn.ExecContext(context.WithoutCancel(w.ctx), "ROLLBACK") //nolint:errcheck
		}
		rw.conn.Close()
	}
	w.cancel()
	return err
}

// mysqlNeverVersion is the version a dump writes on a comment no server is to
// run: mariadb-dump opens with "/*M!999999\- enable the sandbox mode */", for
// the client alone.
const mysqlNeverVersion = "999999"

// MySQLStatementHead returns the start of a MySQL-family statement in upper
// case, as the server reads it: a leading version comment is opened -
// "/*!40101 SET NAMES utf8 */" reads as "SET NAMES UTF8 */" - and any other
// leading comment is skipped. A comment with no semicolon of its own reaches
// the splitter glued to the statement below it, so mariadb-dump's sandbox line
// arrives in front of its first SET. Only the head is upper-cased: a statement
// may be a megabyte of INSERT.
func MySQLStatementHead(stmt string) string {
	s := strings.TrimSpace(stmt)
	for {
		// A line comment below the sandbox line (mariadb-dump's banner) is
		// part of the same glued statement.
		if strings.HasPrefix(s, "--") || strings.HasPrefix(s, "#") {
			nl := strings.IndexByte(s, '\n')
			if nl < 0 {
				return ""
			}
			s = strings.TrimSpace(s[nl+1:])
			continue
		}
		if !strings.HasPrefix(s, "/*") {
			break
		}
		end := strings.Index(s, "*/")
		if end < 0 {
			break
		}
		body := s[2:end]
		marker := strings.TrimPrefix(body, "M")
		if strings.HasPrefix(marker, "!") {
			rest := marker[1:]
			ver := rest[:len(rest)-len(strings.TrimLeft(rest, "0123456789"))]
			if ver != mysqlNeverVersion {
				s = strings.TrimSpace(rest[len(ver):])
				break
			}
		}
		s = strings.TrimSpace(s[end+2:])
	}
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.ToUpper(s)
}

// IsMySQLSessionStmt reports whether a statement sets up the session rather
// than writing anything: SET (variables, user variables, NAMES) or USE. A
// parallel restore replays it on every row writer.
func IsMySQLSessionStmt(stmt string) bool {
	head := MySQLStatementHead(stmt)
	return strings.HasPrefix(head, "SET ") || strings.HasPrefix(head, "USE ")
}

// IsMySQLTableLockStmt reports whether a statement is LOCK TABLES or UNLOCK
// TABLES, which mysqldump wraps around each table's rows. A parallel restore
// leaves them out: the lock would be the restore connection's, and the writers
// putting the rows in would wait on it for good.
func IsMySQLTableLockStmt(stmt string) bool {
	head := MySQLStatementHead(stmt)
	return strings.HasPrefix(head, "LOCK TABLE") || strings.HasPrefix(head, "UNLOCK TABLE")
}
