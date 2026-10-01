package base

import (
	"fmt"
	"regexp"
	"strings"

	filterpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/filter"
)

// =============================================================================
// Statement classification
// =============================================================================
//
// A restore replays a dump statement by statement, so when one is rejected the
// driver holds the statement and nothing else. Reporting a prefix of it — which
// is what the restore loops used to do — leaves the reader to work out both the
// kind of object and its name, and a CREATE TABLE with many columns does not
// reach its own name within a readable prefix.
//
// ClassifyStatement reads the object out of the statement instead, so the
// failure can be reported as "create failed on view sales.v_customer_stats"
// rather than as the first 120 characters of SQL.

// StatementSubject is the object a SQL statement acts on, and the action it
// takes on it. A statement that names no object — SET, USE, a comment — yields
// the zero value, which callers report positionally instead.
type StatementSubject struct {
	Kind string // ObjectKind* here, or filter's ObjectKind* for the kinds it defines
	Name string

	// Owner is the qualifier the statement carried, when it carried one — the
	// schema in "sales.orders". It is empty for the unqualified names MySQL
	// dumps write, where the enclosing database is known to the caller instead.
	Owner string

	Action string // one of the Action* constants
}

// Recognised reports whether the statement named an object.
func (s StatementSubject) Recognised() bool { return s.Kind != "" }

// statementKinds maps the keyword that follows CREATE, ALTER or DROP to the
// object kind it introduces. The vocabulary is the filter package's wherever
// that package defines the kind, so an object is named the same way whether it
// appears in an exclusion rule or in an error.
var statementKinds = map[string]string{
	"TABLE":      ObjectKindTable,
	"VIEW":       filterpkg.ObjectKindView,
	"FUNCTION":   filterpkg.ObjectKindFunction,
	"PROCEDURE":  filterpkg.ObjectKindProcedure,
	"TRIGGER":    filterpkg.ObjectKindTrigger,
	"EVENT":      filterpkg.ObjectKindEvent,
	"INDEX":      filterpkg.ObjectKindIndex,
	"SEQUENCE":   filterpkg.ObjectKindSequence,
	"TYPE":       filterpkg.ObjectKindType,
	"EXTENSION":  filterpkg.ObjectKindExtension,
	"RULE":       filterpkg.ObjectKindRule,
	"DATABASE":   ObjectKindDatabase,
	"SCHEMA":     ObjectKindDatabase,
	"COLLECTION": filterpkg.ObjectKindValidator,
}

// statementTokenLimit bounds how far into a statement the scan looks for the
// object keyword. Everything that introduces one — OR REPLACE, TEMPORARY,
// UNIQUE, ALGORITHM=, DEFINER=, SQL SECURITY — sits within a handful of tokens
// of the verb, and stopping early keeps a keyword inside a view's body or a
// column list from being mistaken for the subject.
const statementTokenLimit = 16

// versionCommentOpen matches the opening of a mysqldump version gate, which
// wraps statements the server may not understand: /*!50003 CREATE ... */.
var versionCommentOpen = regexp.MustCompile(`^/\*!\d*\s*`)

// ClassifyStatement identifies the object a single SQL statement acts on.
func ClassifyStatement(stmt string) StatementSubject {
	stmt = versionCommentOpen.ReplaceAllString(strings.TrimSpace(stmt), "")
	toks := sqlTokens(stmt, statementTokenLimit)
	if len(toks) == 0 {
		return StatementSubject{}
	}

	switch strings.ToUpper(toks[0]) {
	case "INSERT", "REPLACE":
		// INSERT [LOW_PRIORITY|DELAYED|HIGH_PRIORITY] [IGNORE] INTO name
		for i := 1; i < len(toks); i++ {
			if strings.ToUpper(toks[i]) == "INTO" && i+1 < len(toks) {
				owner, name := SplitQualifiedIdent(toks[i+1])
				return StatementSubject{
					Kind: ObjectKindTable, Owner: owner, Name: name, Action: ActionWriteRows,
				}
			}
		}
		// The verb alone still says rows were being written.
		return StatementSubject{Kind: ObjectKindTable, Action: ActionWriteRows}

	case "COPY":
		// PostgreSQL loads a table's rows with COPY <table> [(cols)] FROM STDIN.
		if len(toks) > 1 {
			owner, name := SplitQualifiedIdent(toks[1])
			return StatementSubject{
				Kind: ObjectKindTable, Owner: owner, Name: name, Action: ActionWriteRows,
			}
		}
		return StatementSubject{Kind: ObjectKindTable, Action: ActionWriteRows}

	case "CREATE":
		return subjectAfterKeyword(toks[1:], ActionWriteDDL)

	case "DROP":
		return subjectAfterKeyword(toks[1:], ActionDrop)

	case "ALTER":
		// A dump's ALTER TABLE statements are the foreign keys held back until
		// every table exists, so the constraint is the useful name; anything
		// else is reported against the table it alters.
		for i := 1; i+1 < len(toks); i++ {
			if strings.ToUpper(toks[i]) == "CONSTRAINT" && containsToken(toks, "FOREIGN") {
				owner, name := SplitQualifiedIdent(toks[i+1])
				return StatementSubject{
					Kind:   filterpkg.ObjectKindForeignKey,
					Owner:  owner,
					Name:   name,
					Action: ActionWriteDDL,
				}
			}
		}
		return subjectAfterKeyword(toks[1:], ActionAlter)
	}

	return StatementSubject{}
}

// subjectAfterKeyword finds the first object keyword among toks and takes the
// identifier that follows it as the object's name.
func subjectAfterKeyword(toks []string, action string) StatementSubject {
	for i, tok := range toks {
		kind, ok := statementKinds[strings.ToUpper(tok)]
		if !ok {
			continue
		}
		// PostgreSQL spells one kind in two words; the second is the one that
		// matches above, so the qualifier is checked behind it.
		if kind == filterpkg.ObjectKindView && i > 0 &&
			strings.EqualFold(toks[i-1], "MATERIALIZED") {
			kind = filterpkg.ObjectKindMaterializedView
		}
		var owner, name string
		if j := skipExistenceClause(toks, i+1); j < len(toks) {
			owner, name = SplitQualifiedIdent(toks[j])
		}
		return StatementSubject{Kind: kind, Owner: owner, Name: name, Action: action}
	}
	return StatementSubject{}
}

// skipExistenceClause steps over an IF NOT EXISTS / IF EXISTS guard so that the
// name is read from the token after it rather than from "IF".
func skipExistenceClause(toks []string, i int) int {
	if i >= len(toks) || !strings.EqualFold(toks[i], "IF") {
		return i
	}
	i++
	if i < len(toks) && strings.EqualFold(toks[i], "NOT") {
		i++
	}
	if i < len(toks) && strings.EqualFold(toks[i], "EXISTS") {
		i++
	}
	return i
}

func containsToken(toks []string, want string) bool {
	for _, tok := range toks {
		if strings.EqualFold(tok, want) {
			return true
		}
	}
	return false
}

// =============================================================================
// RestoreStatementError
// =============================================================================

// statementSnippet is how much of an unclassifiable statement goes into its
// error. Only the statements that name no object land here — SET, USE — and
// those fit well within it.
const statementSnippet = 120

// RestoreStatementError reports the failure of one statement during a restore,
// named by the object it acts on rather than by its text.
//
// owner is what encloses the object when the statement did not say so itself —
// the database for MySQL and MariaDB, whose dumps write unqualified names. A
// statement that carries its own qualifier, as PostgreSQL's do, keeps it.
//
// A statement that names no object has nothing to be named by, so it keeps the
// older reporting: its position in the dump and a prefix of its text. Either
// way the full statement is logged, so the message stays readable without the
// text being lost.
func RestoreStatementError(dbmsType, owner, stmt string, index, total int, err error) error {
	subject := ClassifyStatement(stmt)
	if subject.Owner != "" {
		owner = subject.Owner
	}

	logger.Error("restore statement failed",
		"dbms", dbmsType,
		"owner", owner,
		"statement", index,
		"statements", total,
		"objectKind", subject.Kind,
		"objectName", subject.Name,
		"sql", stmt,
		"err", err.Error(),
	)

	if !subject.Recognised() {
		if len(stmt) > statementSnippet {
			stmt = stmt[:statementSnippet] + "..."
		}
		return fmt.Errorf("statement %d/%d (%s) failed: %w", index, total, stmt, err)
	}

	return Obj{
		Kind: subject.Kind, Owner: owner, Name: subject.Name,
		Index: index, Total: total, Unit: "statement",
	}.Fail(subject.Action, err)
}

// =============================================================================
// Tokenising
// =============================================================================

// sqlTokens splits the head of a statement into whitespace-separated tokens,
// keeping a quoted identifier whole so that a name containing spaces stays one
// token. It stops at the first unquoted "(" or ";": the object's name always
// precedes the column list or the body, and stopping there keeps the scan out
// of them.
func sqlTokens(s string, limit int) []string {
	toks := make([]string, 0, limit)
	var cur strings.Builder
	var quote byte

	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}

	for i := 0; i < len(s) && len(toks) < limit; i++ {
		c := s[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '`', '\'', '"':
			quote = c
			cur.WriteByte(c)
		case ' ', '\t', '\r', '\n':
			flush()
		case '(', ';':
			flush()
			return toks
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

// SplitQualifiedIdent separates a possibly qualified, possibly quoted identifier into
// its qualifier and the object's own name: `sales`.`orders` and sales.orders
// both yield ("sales", "orders"), and a bare `orders` yields ("", "orders").
//
// The qualifier is kept rather than dropped because it is the only place a
// PostgreSQL statement names its schema, and the schema is what encloses the
// object there.
func SplitQualifiedIdent(tok string) (owner, name string) {
	tok = strings.TrimRight(tok, ",")

	// Find the last "." that is not inside quotes; everything after it is the
	// object's own name, everything before it the qualifier.
	var quote byte
	last := -1
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '`', '\'', '"':
			quote = c
		case '.':
			last = i
		}
	}
	if last >= 0 {
		owner = unquoteIdent(tok[:last])
	}
	return owner, unquoteIdent(tok[last+1:])
}

// unquoteIdent strips the quoting from a single identifier component.
func unquoteIdent(s string) string {
	if len(s) >= 2 {
		if q := s[0]; (q == '`' || q == '\'' || q == '"') && s[len(s)-1] == q {
			// A quoted identifier escapes the quote character by doubling it.
			return strings.ReplaceAll(s[1:len(s)-1], string(q)+string(q), string(q))
		}
	}
	return s
}

// =============================================================================
// Leading Comments
// =============================================================================

// StripLeadingComments removes the comment lines a dump writes above a
// statement and returns what is left, trimmed. A statement that is nothing but
// comments comes back empty.
//
// A restore loop cannot decide "this entry is only a comment" by looking at the
// first characters. A statement splitter cuts on the delimiter, so the comment
// block a dump puts above a statement arrives glued to it:
//
//	--
//	-- Dumping events for database 'app'
//	--
//	/*!50106 SET @save_time_zone= @@TIME_ZONE */
//
// Skipping that entry because it starts with "--" drops the SET with it, and the
// statement that reads @save_time_zone back fails on a NULL variable — a
// mysqldump file restored through the SQL path fails midway for that reason
// alone. Only "--" line comments are removed: a /*! … */ block is executable
// SQL to the server, not a comment.
func StripLeadingComments(stmt string) string {
	for {
		trimmed := strings.TrimLeft(stmt, " \t\r\n")
		if !strings.HasPrefix(trimmed, "--") {
			return strings.TrimSpace(trimmed)
		}
		idx := strings.IndexByte(trimmed, '\n')
		if idx < 0 {
			return "" // nothing but a comment
		}
		stmt = trimmed[idx+1:]
	}
}
