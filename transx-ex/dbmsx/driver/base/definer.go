package base

import (
	"bytes"
	"io"
	"regexp"
)

// =============================================================================
// DEFINER stripping — MySQL and MariaDB
// =============================================================================
//
// MySQL stamps the creating account onto every view, routine, trigger and event
// as a DEFINER clause, whether or not the CREATE statement asked for one. A dump
// therefore carries the *source* server's account:
//
//	CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER
//	VIEW `v_customer_stats` AS select ...
//
// Replaying that on another server needs the named account to exist there and
// the restoring account to be allowed to speak for it. Neither holds on a
// managed instance: an AWS RDS master user is not granted SUPER,
// SET_ANY_DEFINER or ALLOW_NONEXISTENT_DEFINER, so the restore stops at the
// first view with
//
//	Error 1227 (42000): Access denied; you need (at least one of) the SUPER or
//	ALLOW_NONEXISTENT_DEFINER privilege(s) for this operation
//
// Removing the clause makes the restoring account the definer, which is the
// only account guaranteed to exist on the destination. That is what a
// mysqldump-based migration does by hand with sed, and it is done here at dump
// time rather than at restore time so that the dump file itself is portable —
// and so that the transformation only ever sees DDL.

// definerClause matches a DEFINER=user@host clause and the whitespace after it.
// Each half is an identifier that MySQL may quote with backticks, single or
// double quotes, or leave bare.
var definerClause = regexp.MustCompile(
	"(?i)\\s*DEFINER\\s*=\\s*(`[^`]*`|'[^']*'|\"[^\"]*\"|[^\\s@]+)@(`[^`]*`|'[^']*'|\"[^\"]*\"|[^\\s`'\"]+)")

// StripDefiner removes every DEFINER=user@host clause from one DDL statement.
//
// Intended for the output of SHOW CREATE {VIEW,FUNCTION,PROCEDURE,TRIGGER,EVENT}.
// A statement without one — SHOW CREATE TABLE, or any non-MySQL engine — is
// returned unchanged, so callers do not have to know which kind they hold.
func StripDefiner(stmt string) string {
	return definerClause.ReplaceAllString(stmt, "")
}

// dataStatementPrefixes are the line starts that carry row data rather than DDL.
// A value inside one could contain the literal text "DEFINER=`a`@`b`", and
// rewriting it would corrupt the data instead of the schema.
var dataStatementPrefixes = [][]byte{
	[]byte("INSERT INTO"),
	[]byte("REPLACE INTO"),
}

// DefinerFilterWriter strips DEFINER clauses from a stream of SQL as it is
// written, for the dump path that shells out to mysqldump instead of building
// the statements itself. mysqldump has no option to omit the clause.
//
// It works line by line because mysqldump never splits a DEFINER clause across
// lines: it writes them inline with the CREATE, or inside a version-gated
// comment of their own (/*!50017 DEFINER=`root`@`localhost`*/). Emptying such a
// comment leaves /*!50017 */, which MySQL parses and ignores.
//
// Close must be called to flush a trailing line that has no newline.
type DefinerFilterWriter struct {
	dst io.Writer
	buf bytes.Buffer
}

// NewDefinerFilterWriter wraps dst so that everything written through it has its
// DEFINER clauses removed.
func NewDefinerFilterWriter(dst io.Writer) *DefinerFilterWriter {
	return &DefinerFilterWriter{dst: dst}
}

func (w *DefinerFilterWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			return len(p), nil
		}
		line := make([]byte, i+1)
		_, _ = w.buf.Read(line)
		if err := w.writeLine(line); err != nil {
			return 0, err
		}
	}
}

// Close flushes whatever is left when the stream ended without a final newline.
func (w *DefinerFilterWriter) Close() error {
	if w.buf.Len() == 0 {
		return nil
	}
	line := w.buf.Bytes()
	w.buf.Reset()
	return w.writeLine(line)
}

func (w *DefinerFilterWriter) writeLine(line []byte) error {
	out := line
	if !isDataStatement(line) && bytes.Contains(bytes.ToUpper(line), []byte("DEFINER")) {
		out = definerClause.ReplaceAll(line, nil)
	}
	_, err := w.dst.Write(out)
	return err
}

// isDataStatement reports whether the line begins a row-data statement.
func isDataStatement(line []byte) bool {
	trimmed := bytes.ToUpper(bytes.TrimLeft(line, " \t"))
	for _, prefix := range dataStatementPrefixes {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}
