package filter

// object_name.go — which object an object_exclude rule's name takes.
//
// A rule names its object as text, and an engine has more than one way to spell
// the same object. The dump of each engine decides, object by object, whether a
// rule takes it; ObjectNameMatches asks the same question of a name as Inspect
// lists it, so a caller holding only the inventory (a migration plan checking
// that every rule names something) reaches the dump's answer.
//
// PostgreSQL routines carry one more spelling. Inspect lists them with their
// identity arguments — sales.user_count(integer, text) — because overloads share
// a name, and that is the form a caller copies out of an inventory. A rule may
// name a routine
//
//	name                   every overload of name, in any schema
//	schema.name            every overload of name in schema
//	schema.name(args)      the one routine with that signature (name(args) too)
//
// where either identifier may be double-quoted, as quote_ident renders it, and
// args are compared without the IN mode PostgreSQL 14 began to print and
// without regard to spacing.

import (
	"strings"
)

// ObjectExcludeNames returns the names of the object_exclude rules of kind that
// carry no table qualifier, in rule order. A driver whose objects can be named
// in more than one spelling reads them here and decides the match itself.
func (o *DBMSFilterOption) ObjectExcludeNames(kind string) []string {
	if o == nil {
		return nil
	}
	var names []string
	for _, r := range o.Rules {
		if oe, ok := r.Matcher.(objectExcludeRule); ok && oe.kind() == kind && oe.table() == "" {
			names = append(names, oe.name())
		}
	}
	return names
}

// PgRoutineRuleMatches reports whether an object_exclude rule naming rule takes
// the PostgreSQL routine schema.name with identity arguments args (as
// pg_get_function_identity_arguments renders them, without parentheses).
func PgRoutineRuleMatches(rule, schema, name, args string) bool {
	if rule == name || rule == schema+"."+name {
		return true
	}
	parts, ruleArgs, hasArgs, ok := splitPgName(rule)
	if !ok {
		return false
	}
	switch {
	case len(parts) == 1 && parts[0] == name:
	case len(parts) == 2 && parts[0] == schema && parts[1] == name:
	default:
		return false
	}
	return !hasArgs || normalizePgArgs(ruleArgs) == normalizePgArgs(args)
}

// ObjectNameMatches reports whether an object_exclude rule of kind naming rule
// takes the object Inspect lists as inventoryName on dbmsType. It answers as the
// engine's dump does: on MySQL, MariaDB and MongoDB the names must be equal; on
// PostgreSQL the rule may also leave out the schema, and a routine rule may
// carry a signature (see PgRoutineRuleMatches). Index rules are not decided
// here: an index is listed with its table, not as a name of its own.
func ObjectNameMatches(dbmsType, kind, rule, inventoryName string) bool {
	if rule == inventoryName {
		return true
	}
	if dbmsType != "postgresql" {
		return false
	}
	parts, args, hasArgs, ok := splitPgName(inventoryName)
	if !ok {
		return false
	}
	switch {
	case len(parts) == 1:
		return rule == parts[0]
	case kind == ObjectKindFunction || kind == ObjectKindProcedure:
		return hasArgs && PgRoutineRuleMatches(rule, parts[0], parts[1], args)
	default:
		return !hasArgs && (rule == parts[1] || rule == parts[0]+"."+parts[1])
	}
}

// splitPgName splits a PostgreSQL object name — name or schema.name, either part
// optionally double-quoted, optionally followed by (args) — into its unquoted
// identifiers and the text between the parentheses. ok is false for text that
// is not such a name.
func splitPgName(s string) (parts []string, args string, hasArgs, ok bool) {
	s = strings.TrimSpace(s)
	for {
		var id string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for {
				j := strings.IndexByte(s[i:], '"')
				if j < 0 {
					return nil, "", false, false
				}
				b.WriteString(s[i : i+j])
				i += j + 1
				if !strings.HasPrefix(s[i:], `"`) {
					break
				}
				b.WriteByte('"')
				i++
			}
			id, s = b.String(), s[i:]
		} else {
			end := strings.IndexAny(s, ".(")
			if end < 0 {
				end = len(s)
			}
			id, s = strings.TrimSpace(s[:end]), s[end:]
		}
		if id == "" {
			return nil, "", false, false
		}
		parts = append(parts, id)
		s = strings.TrimLeft(s, " ")
		if !strings.HasPrefix(s, ".") {
			break
		}
		if len(parts) == 2 {
			return nil, "", false, false
		}
		s = strings.TrimLeft(s[1:], " ")
	}
	switch {
	case s == "":
		return parts, "", false, true
	case strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")"):
		return parts, s[1 : len(s)-1], true, true
	}
	return nil, "", false, false
}

// normalizePgArgs puts an identity argument list in one spelling: single
// spaces, ", " between arguments, and no leading IN mode (the default mode,
// which PostgreSQL 14 prints and 13 does not).
func normalizePgArgs(args string) string {
	list := strings.Split(args, ",")
	for i, a := range list {
		fields := strings.Fields(a)
		if len(fields) > 1 && strings.EqualFold(fields[0], "IN") {
			fields = fields[1:]
		}
		list[i] = strings.Join(fields, " ")
	}
	if len(list) == 1 && list[0] == "" {
		return ""
	}
	return strings.Join(list, ", ")
}
