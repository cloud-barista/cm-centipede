#!/usr/bin/env bash
#
# lib/honeybee.sh — the cm-honeybee client (source collection)
#
# The matrix asks honeybee for two things only.
#   1) hold one connection per source bucket, pointing at the MinIO container
#   2) inspect that bucket through it
#
# ── Collected once per bucket, not once per cell ────────────────────────────
# The managed-DB matrix re-collects for every cell, because its source container
# is replaced between cells. Here one MinIO serves the whole run, so a bucket is
# inspected once and the result is reused by every CSP column. Six imports for a
# 6x2 matrix instead of twelve.
#
# ── One connection per bucket, because honeybee stores one ──────────────────
# ImportObjectStorageReq.Bucket is required and SavedObjectStorageInfo is keyed
# by connection id, holding exactly one bucket's result. Two buckets on one
# connection would mean the second import overwriting the first, so each bucket
# gets its own ConnectionInfo — <group>-<bucket>-src.
#
# ── The SourceGroup is type "minio", not "db" ───────────────────────────────
# doImportObjectStorage refuses anything else in as many words: a csp group is
# rejected outright and any other type with "object storage inspection is
# supported only for minio source group". provider_name is onprem, which is what
# makes os_endpoint the endpoint (resolveS3Endpoint) and means no region is
# required.
#
# ── Access is always direct ─────────────────────────────────────────────────
# os_access_type is "direct", and there is no other choice worth offering: the
# ssh-tunnel branch of doImportObjectStorage sends the inspect to an agent
# installed on the source host, which this folder does not deploy.
#
# ⚠ The S3 keys travel in the connection_info body, which is why
#   os_access_key_id / os_secret_access_key are in common.sh's masking filter.

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

hb_ok() { [ "${1:-000}" -ge 200 ] && [ "${1:-000}" -lt 300 ]; }

# ---------------------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------------------
# Called before a single bucket is created. Learning that honeybee is not running
# should never cost a created resource first.
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

	body="$(jq -cn --arg n "$name" --arg d "cm-centipede object storage migration matrix" \
		'{name:$n, description:$d, type:"minio", provider_name:"onprem"}')"
	hb_curl POST "/source_group" "$body" "$tmp" >/dev/null
	id="$(jq -r '.id // empty' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	if [ -z "$id" ]; then
		fail "could not create the honeybee SourceGroup: $name"
		fail "  It must be type \"minio\" with provider_name \"onprem\"; a group of another"
		fail "  type already registered under this name would be refused. Rename it with"
		fail "  HB_SOURCE_GROUP, or delete the old one."
		return 1
	fi
	printf '%s' "$id"
}

# _hb_conn_body BUCKET NAME -> the ConnectionInfo body for the MinIO source.
#
#   The endpoint carries its scheme on purpose. honeybee treats a scheme typed
#   into os_endpoint as an explicit statement about TLS and lets it beat
#   os_use_ssl, so "http://" is what actually turns TLS off for a local MinIO.
#
#   os_scan_bucket is what the inspect reads, and honeybee requires it here: the
#   bucket belongs to the connection, not to the import request. That is why
#   this folder already registers one connection per bucket.
_hb_conn_body() {
	local bucket="$1" name="$2"
	jq -n --arg n "$name" --arg d "matrix source bucket $bucket (direct)" \
		--arg ep "$(src_endpoint)" \
		--arg ak "${MINIO_ROOT_USER:-minioadmin}" \
		--arg sk "$MINIO_ROOT_PASSWORD" \
		--arg bucket "$bucket" \
		'{name:$n, description:$d,
		  os_access_type:"direct",
		  os_endpoint:$ep,
		  os_access_key_id:$ak,
		  os_secret_access_key:$sk,
		  os_use_ssl:false,
		  os_scan_bucket:$bucket}'
}

# hb_connection SG_ID BUCKET -> ConnectionInfo id (POST when absent, PUT to update)
hb_connection() {
	local sg="$1" bucket="$2" name id tmp body
	name="${HB_SOURCE_GROUP:-cpbos-matrix}-${bucket}-src"
	body="$(_hb_conn_body "$bucket" "$name")"
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

# hb_import SG_ID CONN_ID BUCKET — inspect this connection's bucket.
#
#   No metric is requested. The plan reads Path and Folders[].Key and nothing
#   else, and each metric field costs a full object scan on the source; the
#   object count this folder prints comes from the container's own mc instead.
#
#   BUCKET is for the failure message only. The target is the connection's
#   os_scan_bucket, set at registration, so the body carries just the metric
#   selection — none here, hence the empty object.
hb_import() {
	local sg="$1" conn="$2" bucket="$3" tmp code body
	body='{}'
	tmp="$(mktemp)"
	code="$(hb_curl POST "/source_group/$sg/connection_info/$conn/import/objectstorage" "$body" "$tmp")"
	if ! hb_ok "$code"; then
		fail "source collection failed (HTTP $code) — honeybee could not inspect '$bucket'."
		fail "  Check that HOST_IP=${HOST_IP:-127.0.0.1} is an address honeybee can reach the source"
		fail "  container on. (If honeybee runs in a container, 127.0.0.1 points at honeybee itself.)"
		fail "  Endpoint handed to honeybee: $(src_endpoint)"
		mask_json < "$tmp" | sed 's/^/      /' >&2
		rm -f "$tmp"
		return 1
	fi
	rm -f "$tmp"
}

# hb_source_model SG_ID CONN_ID -> centipede's source model JSON on stdout.
#
#   /objectstorage/refined already returns the whole SourceDataMigrationModel
#   wrapper — connection ref included — which is exactly what POST /plans/target
#   takes as its "source". Nothing is assembled here; the response goes through
#   unchanged.
hb_source_model() {
	local sg="$1" conn="$2" tmp code out
	tmp="$(mktemp)"
	code="$(hb_curl GET "/source_group/$sg/connection_info/$conn/objectstorage/refined" "" "$tmp")"
	if ! hb_ok "$code"; then
		fail "could not read the collection result (HTTP $code)"
		rm -f "$tmp"
		return 1
	fi
	out="$(jq -c '.' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	if [ -z "$out" ] || [ "$(printf '%s' "$out" | jq -r '(.sourceDataMigrationModel.objectStorages // []) | length')" = "0" ]; then
		fail "honeybee returned no object storage in the collection result."
		return 1
	fi
	printf '%s' "$out"
}

# hb_scan_root SRC_MODEL — the path the plan will treat as this bucket's source.
#   honeybee builds it as "<bucket>/<prefix>" and transx-ex normalises it to
#   "<bucket>/" for an empty prefix. Printed so a cell's line names what it
#   actually migrated rather than what was asked for.
hb_scan_root() {
	printf '%s' "$1" | jq -r '.sourceDataMigrationModel.objectStorages[0].path // ""' 2>/dev/null
}

# hb_folder_count SRC_MODEL — how many prefixes the inspect found under the root.
hb_folder_count() {
	printf '%s' "$1" | jq -r '(.sourceDataMigrationModel.objectStorages[0].folders // []) | length' 2>/dev/null
}
