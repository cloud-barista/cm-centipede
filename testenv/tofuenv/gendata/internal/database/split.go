package database

import "strings"

// splitMySQL splits a MySQL/MariaDB dump into individual statements. It is aware
// of the client-only DELIMITER directive (so routine/trigger/event bodies are
// emitted as single statements), line/block comments, and quoted strings.
func splitMySQL(script string) []string {
	var out []string
	var stmt strings.Builder
	delim := ";"
	n := len(script)
	atLineStart := true

	for i := 0; i < n; {
		// DELIMITER directive (only meaningful at line start).
		if atLineStart {
			j := i
			for j < n && (script[j] == ' ' || script[j] == '\t') {
				j++
			}
			if hasPrefixFold(script[j:], "DELIMITER ") {
				k := j + len("DELIMITER ")
				for k < n && (script[k] == ' ' || script[k] == '\t') {
					k++
				}
				start := k
				for k < n && script[k] != '\n' && script[k] != '\r' {
					k++
				}
				delim = strings.TrimSpace(script[start:k])
				if delim == "" {
					delim = ";"
				}
				i = k
				atLineStart = true
				continue
			}
		}

		c := script[i]

		// line comment: "-- " or "--\n" or "#"
		if c == '-' && i+1 < n && script[i+1] == '-' &&
			(i+2 >= n || script[i+2] == ' ' || script[i+2] == '\t' || script[i+2] == '\n' || script[i+2] == '\r') {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		if c == '#' {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		// block comment
		if c == '/' && i+1 < n && script[i+1] == '*' {
			i += 2
			for i+1 < n && !(script[i] == '*' && script[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		// quoted string / identifier
		if c == '\'' || c == '"' || c == '`' {
			stmt.WriteByte(c)
			i++
			for i < n {
				ch := script[i]
				stmt.WriteByte(ch)
				if ch == '\\' && i+1 < n {
					i++
					stmt.WriteByte(script[i])
					i++
					continue
				}
				if ch == c {
					i++
					break
				}
				i++
			}
			atLineStart = false
			continue
		}
		// delimiter
		if strings.HasPrefix(script[i:], delim) {
			if s := strings.TrimSpace(stmt.String()); s != "" {
				out = append(out, s)
			}
			stmt.Reset()
			i += len(delim)
			atLineStart = false
			continue
		}

		stmt.WriteByte(c)
		switch c {
		case '\n':
			atLineStart = true
		case ' ', '\t', '\r':
			// keep atLineStart as-is
		default:
			atLineStart = false
		}
		i++
	}
	if s := strings.TrimSpace(stmt.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// splitPostgres splits a PostgreSQL script into statements, honoring dollar-quoted
// blocks ($$ / $tag$), single/double-quoted strings, comments, and dropping psql
// backslash meta-commands (\set, \c, ...).
func splitPostgres(script string) []string {
	var out []string
	var stmt strings.Builder
	n := len(script)
	atLineStart := true

	for i := 0; i < n; {
		c := script[i]

		// psql meta-command line
		if atLineStart && c == '\\' {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		// line comment
		if c == '-' && i+1 < n && script[i+1] == '-' {
			for i < n && script[i] != '\n' {
				i++
			}
			continue
		}
		// block comment
		if c == '/' && i+1 < n && script[i+1] == '*' {
			i += 2
			for i+1 < n && !(script[i] == '*' && script[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		// dollar-quoted string: $tag$ ... $tag$
		if c == '$' {
			end := i + 1
			for end < n && isIdentChar(script[end]) {
				end++
			}
			if end < n && script[end] == '$' {
				tag := script[i : end+1]
				stmt.WriteString(tag)
				i = end + 1
				if idx := strings.Index(script[i:], tag); idx >= 0 {
					stmt.WriteString(script[i : i+idx+len(tag)])
					i += idx + len(tag)
				} else {
					stmt.WriteString(script[i:])
					i = n
				}
				atLineStart = false
				continue
			}
		}
		// single-quoted string (with '' escape)
		if c == '\'' {
			stmt.WriteByte(c)
			i++
			for i < n {
				ch := script[i]
				stmt.WriteByte(ch)
				if ch == '\'' {
					if i+1 < n && script[i+1] == '\'' {
						stmt.WriteByte('\'')
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			atLineStart = false
			continue
		}
		// double-quoted identifier
		if c == '"' {
			stmt.WriteByte(c)
			i++
			for i < n {
				ch := script[i]
				stmt.WriteByte(ch)
				i++
				if ch == '"' {
					break
				}
			}
			atLineStart = false
			continue
		}
		// statement terminator
		if c == ';' {
			if s := strings.TrimSpace(stmt.String()); s != "" {
				out = append(out, s)
			}
			stmt.Reset()
			i++
			atLineStart = false
			continue
		}

		stmt.WriteByte(c)
		switch c {
		case '\n':
			atLineStart = true
		case ' ', '\t', '\r':
		default:
			atLineStart = false
		}
		i++
	}
	if s := strings.TrimSpace(stmt.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func isIdentChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}
