#!/usr/bin/env bash
#
# lib/honeybee.sh — the cm-honeybee client (source collection)
#
# The matrix asks honeybee for two things only.
#   1) hold one connection pointing at the source container
#   2) collect the source database list through it
#
# That result goes into centipede's source model unchanged. honeybee and
# centipede share the same dmdl source-model type, so nothing is converted: the
# databases array from GET .../db becomes sourceDataMigrationModel.databases[].databases.
#
# ── One connection per engine, re-collected per cell ────────────────────────
# A source container's address and port do not change with its version (the port
# is fixed per engine), so one connection per engine is created once and reused,
# updated with PUT. Collection is re-run for every cell, because the database
#
# behind that endpoint has been replaced.
#
# ⚠ With MODE=ssh the private key itself travels in the request body: honeybee
#   stores the PEM (RSA-wrapped), not a path to it. That is why private_key is in
#   the API log's masking filter.

if [ -n "${MATRIX_HONEYBEE_SH:-}" ]; then return 0; fi
MATRIX_HONEYBEE_SH=1

HB_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$HB_LIB_DIR/common.sh"
# shellcheck source=./source.sh
. "$HB_LIB_DIR/source.sh"

HB_BASE="${HB_BASE:-http://localhost:8081/honeybee}"

# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

# hb_curl METHOD PATH JSON_BODY OUT_FILE -> HTTP code on stdout, body in OUT_FILE
hb_curl() {
	local method="$1" path="$2" data="${3:-}" out="$4" code cmd
	if [ -n "$data" ]; then
		code="$(curl -s -o "$out" -w '%{http_code}' -X "$method" "$HB_BASE$path" \
			-H 'Content-Type: application/json' -d "$data")"
		cmd="$(printf "curl -s -X %s '%s' \\\\\n  -H 'Content-Type: application/json' \\\\\n  -d '%s'" \
			"$method" "$(sq_escape "$HB_BASE$path")" \
			"$(sq_escape "$(printf '%s' "$data" | mask_json)")")"
	else
		code="$(curl -s -o "$out" -w '%{http_code}' -X "$method" "$HB_BASE$path")"
		cmd="curl -s -X $method '$(sq_escape "$HB_BASE$path")'"
	fi
	api_log "$method" "$path" "$cmd" "$code" "$out"
	printf '%s' "$code"
}

# hb_ok CODE — is it a 2xx?
hb_ok() { [ "${1:-000}" -ge 200 ] && [ "${1:-000}" -lt 300 ]; }

# ---------------------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------------------
# Called before a single managed instance is created. Learning that honeybee is
# not running should never cost a 30-minute instance first.
hb_preflight() {
	local tmp code
	tmp="$(mktemp)"
	code="$(hb_curl GET "/source_group" "" "$tmp")"
	rm -f "$tmp"
	if ! hb_ok "$code"; then
		fail "cannot reach cm-honeybee: $HB_BASE (HTTP $code)"
		fail "  Check that it is running and that HB_BASE in .env is right."
		return 1
	fi
	ok "cm-honeybee answers: $HB_BASE"
}

# ---------------------------------------------------------------------------
# SourceGroup / ConnectionInfo
# ---------------------------------------------------------------------------

# hb_source_group NAME -> id (created when absent)
hb_source_group() {
	local name="$1" id tmp body
	tmp="$(mktemp)"
	hb_curl GET "/source_group" "" "$tmp" >/dev/null
	id="$(jq -r --arg n "$name" '.source_group[]? | select(.name==$n) | .id' "$tmp" 2>/dev/null | head -1)"
	if [ -n "$id" ] && [ "$id" != "null" ]; then rm -f "$tmp"; printf '%s' "$id"; return 0; fi

	body="$(jq -cn --arg n "$name" --arg d "cm-centipede managed-DB version matrix" \
		--arg p "${SRC_PROVIDER:-onprem}" \
		'{name:$n, description:$d, type:"db", provider_name:$p}')"
	hb_curl POST "/source_group" "$body" "$tmp" >/dev/null
	id="$(jq -r '.id // empty' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	[ -n "$id" ] || { fail "could not create SourceGroup: $name"; return 1; }
	printf '%s' "$id"
}

# _hb_conn_body ENGINE NAME -> the ConnectionInfo body.
#
#   direct connects to the published port; ssh tunnels to the server inside the
#   container. Either way db_name is stated: this container holds one seeded
#   database, and naming it scopes the inspection to that one rather than every
#   user database on the server. It is not the authentication database: that is
#   db_auth_source, which this body leaves unset, so the URI transx-ex builds
#   falls back to admin - where the account also exists.
_hb_conn_body() {
	local engine="$1" name="$2" db="${SRC_DB:-matrix_db}"

	if [ "${MODE:-direct}" = "ssh" ]; then
		jq -n --arg n "$name" --arg t "$(lower "$engine")" --arg db "$db" \
			--arg host "$HOST_IP" --arg sport "$(src_ssh_port "$engine")" \
			--arg iport "$(src_internal_port "$engine")" \
			--arg u "${SRC_DB_USER:-centipede}" --arg w "${SRC_DB_PASS:-centipede_pass}" \
			--arg key "$(cat "$(src_ssh_key_path)")" \
			'{name:$n, description:"matrix source (ssh-tunnel)",
			  db_type:$t, db_access_type:"ssh-tunnel", db_name:$db,
			  db_host:"127.0.0.1", db_port:$iport,
			  db_username:$u, db_password:$w,
			  ip_address:$host, ssh_port:$sport, user:"root", private_key:$key}'
	else
		jq -n --arg n "$name" --arg t "$(lower "$engine")" --arg db "$db" \
			--arg host "$HOST_IP" --arg port "$(src_db_port "$engine")" \
			--arg u "${SRC_DB_USER:-centipede}" --arg w "${SRC_DB_PASS:-centipede_pass}" \
			'{name:$n, description:"matrix source (direct)",
			  db_type:$t, db_access_type:"direct", db_name:$db,
			  db_host:$host, db_port:$port,
			  db_username:$u, db_password:$w}'
	fi
}

# hb_connection SG_ID ENGINE -> ConnectionInfo id (POST when absent, PUT to update)
hb_connection() {
	local sg="$1" engine="$2" name id tmp body
	name="${HB_SOURCE_GROUP:-cptfm-matrix}-$(lower "$engine")-src"
	body="$(_hb_conn_body "$engine" "$name")"
	tmp="$(mktemp)"

	hb_curl GET "/source_group/$sg/connection_info" "" "$tmp" >/dev/null
	id="$(jq -r --arg n "$name" '.connection_info[]? | select(.name==$n) | .id' "$tmp" 2>/dev/null | head -1)"
	if [ -n "$id" ] && [ "$id" != "null" ]; then
		hb_curl PUT "/source_group/$sg/connection_info/$id" "$body" "$tmp" >/dev/null
	else
		hb_curl POST "/source_group/$sg/connection_info" "$body" "$tmp" >/dev/null
		id="$(jq -r '.id // empty' "$tmp" 2>/dev/null)"
	fi
	rm -f "$tmp"
	[ -n "$id" ] || { fail "could not obtain ConnectionInfo: $name"; return 1; }
	printf '%s' "$id"
}

# ---------------------------------------------------------------------------
# Collection
# ---------------------------------------------------------------------------

# hb_import SG_ID CONN_ID — collect the database list from the source. Re-run per cell.
hb_import() {
	local sg="$1" conn="$2" tmp code
	tmp="$(mktemp)"
	code="$(hb_curl POST "/source_group/$sg/connection_info/$conn/import/db" '{}' "$tmp")"
	if ! hb_ok "$code"; then
		fail "source collection failed (HTTP $code) — honeybee could not reach the source."
		fail "  Check that HOST_IP=$HOST_IP is an address honeybee can reach the source container on."
		fail "  (If honeybee runs in a container, 127.0.0.1 points at honeybee itself.)"
		mask_json < "$tmp" | sed 's/^/      /' >&2
		rm -f "$tmp"
		return 1
	fi
	rm -f "$tmp"
}

# hb_databases SG_ID CONN_ID -> the collection result JSON on stdout
hb_databases() {
	local sg="$1" conn="$2" tmp code
	tmp="$(mktemp)"
	code="$(hb_curl GET "/source_group/$sg/connection_info/$conn/db" "" "$tmp")"
	if ! hb_ok "$code"; then
		fail "could not read the collection result (HTTP $code)"
		rm -f "$tmp"
		return 1
	fi
	cat "$tmp"
	rm -f "$tmp"
}

# hb_source_model CONN_ID DB_JSON -> centipede's source model JSON
#
#   honeybee and centipede share the dmdl type, so the databases array goes in as is.
hb_source_model() {
	local conn="$1" dbjson="$2"
	jq -n --arg id "$conn" --argjson r "$dbjson" \
		'{sourceDataMigrationModel:{databases:[{
		    connection:{source:"honeybee", honeybee:{connectionId:$id}},
		    databases:($r.databases // [])}]}}'
}
