package postgresql

import "testing"

func TestPgIsSessionSetting(t *testing.T) {
	for stmt, want := range map[string]bool{
		"SET client_encoding = 'UTF8';":                           true,
		"  set standard_conforming_strings = on;":                 true,
		"SELECT pg_catalog.set_config('search_path', '', false);": true,
		"SET LOCAL statement_timeout = 0;":                        false,
		"CREATE TABLE t (id int);":                                false,
		"SELECT pg_catalog.setval('s', 42, true);":                false,
	} {
		if got := pgIsSessionSetting(stmt); got != want {
			t.Errorf("pgIsSessionSetting(%q) = %v, want %v", stmt, got, want)
		}
	}
}

func TestPgIsSetval(t *testing.T) {
	if !pgIsSetval("SELECT pg_catalog.setval('public.t_id_seq', 3, true);") {
		t.Error("setval not recognised")
	}
	if pgIsSetval("SELECT count(*) FROM t;") {
		t.Error("a query taken for setval")
	}
}
