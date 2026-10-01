#!/usr/bin/env bash
#
# lib/beetle.sh — the cm-beetle client (written for this folder)
#
# The provisioner, whole and entire: everything that creates, reads and deletes a
# managed bucket lives here. os-matrix.sh calls the functions below and knows
# nothing about cm-beetle itself, which is what keeps the matrix body, its
# verdicts and its table free of provisioner detail.
#
# ── Which server answers which call ─────────────────────────────────────────
# Every call about a resource goes through cm-beetle. cb-tumblebug is addressed
# directly in exactly two places, and each is a place beetle has no usable API:
#
#   namespace create/delete   cm-beetle has no namespace API — the routes exist
#                             in pkg/api/rest/server.go but are commented out,
#                             while every migration handler starts by reading the
#                             namespace and fails without it.
#   connection lookup         the connection catalogue is tumblebug's own.
#
#   namespace     POST|DELETE {TUMBLEBUG}/ns[/{ns}]
#   connection    GET  {TUMBLEBUG}/connConfig/{name}
#   support       GET  /recommendation/middleware/objectStorage/support
#   recommend     POST /recommendation/middleware/objectStorage
#   bucket        POST|GET|DELETE /migration/middleware/ns/{ns}/objectStorage[/{osId}]
#   async track   GET  /request/{reqId}
#
# ── No network, and no secret ───────────────────────────────────────────────
# Unlike the managed-RDBMS path there is no vNet, no subnet and no security
# group: a bucket sits outside the VPC. There is no master password either — the
# create call takes no credential of any kind, and the bucket is reached through
# cb-tumblebug's ObjectStorage API rather than by a client of ours holding keys.
# So this file has no db_password() and no equivalent.
#
# ── Where "what exists" is remembered ───────────────────────────────────────
# There is no local state file. cb-tumblebug's namespace is the state, and the
# list API is read back through beetle rather than trusted from disk.
#
# Ownership is carried by the names instead: every bucket is
# <prefix>-<csp>-os-<bucket> inside a namespace this folder alone uses, so
# cleanup_all can tell its own resources from anyone else's without a record.
#
# One hole is left by that, and inflight_* fills it: a create whose request
# reached the CSP but whose record never reached tumblebug is in no list.
#
# ── The CSP bucket name is not ours to choose ───────────────────────────────
# cb-tumblebug generates the real bucket name in the CSP from a uid of its own
# (src/core/resource/objectStorage.go), so osId identifies the bucket only on the
# tumblebug side. That is why .env has no bucket-name key and why nothing here
# worries about S3's global namespace.

if [ -n "${MATRIX_BEETLE_SH:-}" ]; then return 0; fi
MATRIX_BEETLE_SH=1

# shellcheck source=./common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

BEETLE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BEETLE_ROOT="$(cd "$BEETLE_LIB_DIR/../.." && pwd)"

BEETLE_URL="${BEETLE_URL:-http://localhost:8056/beetle}"
BEETLE_USERNAME="${BEETLE_USERNAME:-default}"
BEETLE_PASSWORD="${BEETLE_PASSWORD:-default}"
TUMBLEBUG_URL="${TUMBLEBUG_URL:-http://localhost:1323/tumblebug}"
TUMBLEBUG_USERNAME="${TUMBLEBUG_USERNAME:-default}"
TUMBLEBUG_PASSWORD="${TUMBLEBUG_PASSWORD:-default}"

# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------
BT_STATUS=""
BT_BODY=""
BT_RETRY_AFTER=""
BT_LAST_METHOD=""
BT_LAST_URL=""
BT_HEADERS=()
bt_header() { BT_HEADERS+=("$1"); }

# cm-beetle throttles its cb-tumblebug calls and answers a 503 with Retry-After
# when there is no room. That is "in a moment", not a failure, so it is retried.
BT_RATE_LIMIT_RETRIES="${MATRIX_RATE_LIMIT_RETRIES:-3}"

# _http_request BASE USER PASS METHOD PATH [BODY]
_http_request() {
	local base="$1" user="$2" pass="$3" method="$4" path="$5" body="${6:-}"
	local url="${base%/}${path}" out hdr code h cmd

	# Kept so a failure can name the request that produced it. The same sentence
	# comes back from several calls, and the response alone does not say which.
	BT_LAST_METHOD="$method"
	BT_LAST_URL="$url"

	out="$(mktemp "${MATRIX_TMP:-/tmp}/resp.XXXXXX")"
	hdr="${out}.hdr"

	local -a args=(
		--silent --show-error
		--output "$out" --dump-header "$hdr" --write-out '%{http_code}'
		--request "$method"
		--header 'Accept: application/json'
		--connect-timeout 15
	)
	[ -n "$user" ] && args+=(--user "${user}:${pass}")
	# Header values contain spaces (e.g. "Prefer: respond-async"), so they must be
	# quoted on expansion.
	cmd="curl -s -X $method '$(sq_escape "$url")' -u '$user:...'"
	if [ "${#BT_HEADERS[@]}" -gt 0 ]; then
		for h in "${BT_HEADERS[@]}"; do
			args+=(--header "$h")
			cmd="$cmd \\
  -H '$(sq_escape "$h")'"
		done
	fi
	if [ -n "$body" ]; then
		args+=(--header 'Content-Type: application/json' --data-binary "$body")
		cmd="$cmd \\
  -H 'Content-Type: application/json' \\
  -d '$(sq_escape "$(printf '%s' "$body" | mask_json)")'"
	fi

	if code="$(curl "${args[@]}" "$url" 2>"${out}.err")"; then
		BT_BODY="$(cat "$out")"
	else
		code="000"
		BT_BODY="$(cat "${out}.err")"
	fi
	BT_STATUS="$code"
	BT_RETRY_AFTER="$(grep -i '^retry-after:' "$hdr" 2>/dev/null | tail -1 \
		| sed 's/^[^:]*: *//' | tr -d '\r' || true)"
	api_log "$method" "$path" "$cmd" "$code" "$out"
	rm -f "$out" "$hdr" "${out}.err"

	case "$BT_STATUS" in 2*) return 0 ;; *) return 1 ;; esac
}

# bt_request METHOD PATH [BODY] — a cm-beetle call. 503 + Retry-After is retried.
#   Headers added with bt_header apply to this call only.
bt_request() {
	local method="$1" path="$2" body="${3:-}" attempt=0 wait
	local -a headers=()
	[ "${#BT_HEADERS[@]}" -gt 0 ] && headers=("${BT_HEADERS[@]}")

	while :; do
		BT_HEADERS=()
		[ "${#headers[@]}" -gt 0 ] && BT_HEADERS=("${headers[@]}")
		if _http_request "$BEETLE_URL" "${BEETLE_USERNAME:-}" "${BEETLE_PASSWORD:-}" \
		                 "$method" "$path" "$body"; then
			BT_HEADERS=()
			return 0
		fi
		if [ "$BT_STATUS" != "503" ] || [ -z "$BT_RETRY_AFTER" ] \
		   || [ "$attempt" -ge "$BT_RATE_LIMIT_RETRIES" ]; then
			BT_HEADERS=()
			return 1
		fi
		attempt=$((attempt + 1))
		wait="$BT_RETRY_AFTER"
		case "$wait" in ''|*[!0-9]*) wait=5 ;; esac
		warn "beetle is rate limiting — retrying in ${wait}s (${attempt}/${BT_RATE_LIMIT_RETRIES})"
		sleep "$wait"
	done
}

bt_get()    { bt_request GET    "$1"; }
bt_post()   { bt_request POST   "$1" "${2:-}"; }
bt_delete() { bt_request DELETE "$1" "${2:-}"; }

# tb_request — a direct cb-tumblebug call. Only the two cases in the header.
tb_request() {
	local method="$1" path="$2" body="${3:-}"
	_http_request "$TUMBLEBUG_URL" "${TUMBLEBUG_USERNAME:-}" "${TUMBLEBUG_PASSWORD:-}" \
	              "$method" "$path" "$body"
}
tb_get()    { tb_request GET    "$1"; }
tb_post()   { tb_request POST   "$1" "${2:-}"; }
tb_delete() { tb_request DELETE "$1" "${2:-}"; }

# ---------------------------------------------------------------------------
# Reading the last response — beetle wraps in {success,data}, the cb-tumblebug
# proxy endpoints hand the payload back bare. Both shapes are accepted.
# ---------------------------------------------------------------------------
bt_payload() {
	printf '%s' "$BT_BODY" | jq -c '
		if type == "object" and has("success") and has("data") then .data else . end
	' 2>/dev/null || printf ''
}
# `// empty` is avoided: jq's alternative operator swallows false as well as
# null, which would turn a false flag into an empty string.
bt_data() {
	printf '%s' "$BT_BODY" | jq -r "
		(if type == \"object\" and has(\"success\") and has(\"data\") then .data else . end)
		| ${1}
		| if . == null then empty else . end
	" 2>/dev/null || printf ''
}
bt_jq() { bt_payload | jq "$@" 2>/dev/null || printf ''; }

# bt_raw_message — what beetle said, in full, uncut.
#
#   beetle passes a CSP's answer through untouched, so which field holds it is
#   not fixed. The known ones are tried top down, and when none is present the
#   whole response is shown — hiding a shape we do not recognise would remove the
#   only way to see the cause.
bt_raw_message() {
	local m=""
	m="$(printf '%s' "$BT_BODY" | jq -r '
		if type == "object" then (.error // .message // .Message // .text // empty) else empty end' 2>/dev/null || true)"
	if [ -z "$m" ]; then
		m="$(printf '%s' "$BT_BODY" | jq -r \
			'first(.. | objects | (.returnMessage // .message // .Message // .error // empty))' 2>/dev/null || true)"
	fi
	if [ -z "$m" ]; then
		m="$(printf '%s' "$BT_BODY" | jq -e . 2>/dev/null)" || m="$BT_BODY"
	fi
	[ -n "$m" ] || m="(the response body was empty)"
	printf '%s' "$m" | sed 's/\\n/\n/g; s/\\t/\t/g'
}

# _bt_report_lines PRINTER CONTEXT — one failure in three parts: what we were
#   doing, what the request was, and what the server said.
_bt_report_lines() {
	local printer="$1" context="$2" line
	"$printer" "$context"
	"$printer" "  request : ${BT_LAST_METHOD:-?} ${BT_LAST_URL:-?}  →  HTTP ${BT_STATUS:-?}"
	"$printer" "  beetle says:"
	while IFS= read -r line; do
		"$printer" "    $line"
	done <<< "$(bt_raw_message)"
}

bt_report()      { _bt_report_lines fail "$1"; }
bt_report_warn() { _bt_report_lines warn "$1"; }

# bt_absent — did the last response mean "no such resource"?
bt_absent() {
	[ "$BT_STATUS" = "404" ] && return 0
	printf '%s' "$BT_BODY" | grep -qi 'does not exist\|not found\|no such' && return 0
	return 1
}

# bt_not_empty — did the last response mean "the bucket still has objects in it"?
#   cb-spider answers a plain DELETE on a non-empty bucket with 409 BucketNotEmpty
#   and says so in the message (S3Rest.go DeleteS3Bucket), and it counts object
#   versions and delete markers separately from ordinary objects.
bt_not_empty() {
	printf '%s' "$BT_BODY" | grep -qi 'BucketNotEmpty\|is not empty\|object versions'
}

# bt_deletion_unconfirmed — did the delete go out but the CSP still hold it?
#   cb-tumblebug checks the CSP after a DELETE and, if the bucket is still there,
#   returns a 409 without removing its record (objectStorage.go, issue #2685).
#   That is deliberate — removing the record first would strand the bucket. So
#   this is "not yet", not a failure: wait and call again.
bt_deletion_unconfirmed() {
	printf '%s' "$BT_BODY" | grep -qi 'still exists on the CSP\|deletion unconfirmed'
}

# ---------------------------------------------------------------------------
# Asynchronous calls — a bucket is created in seconds, but the create endpoint
#   still offers Prefer: respond-async, and using it keeps one code path for
#   "ask, then wait" whatever the CSP does on the day.
#
#   The request id is made here rather than read from the response: it has to be
#   known even if the connection drops before the 202 arrives, which is also what
#   makes the inflight marker below useful.
# ---------------------------------------------------------------------------
BT_REQUEST_ID=""
bt_async() {
	local method="$1" path="$2" body="${3:-}"
	BT_REQUEST_ID="cpbos-$(date +%s)-$RANDOM"
	bt_header 'Prefer: respond-async'
	bt_header "X-Request-Id: ${BT_REQUEST_ID}"
	bt_request "$method" "$path" "$body" || return 1
	# Anything but 202 means the server finished it synchronously — nothing to wait for.
	[ "$BT_STATUS" = "202" ] && printf '%s' "$BT_REQUEST_ID"
	return 0
}

# bt_wait REQ_ID LABEL TIMEOUT — wait for an async call. An empty id is already done.
bt_wait() {
	local req_id="$1" label="$2" timeout="$3" waited=0 status detail line
	local interval="${BUCKET_POLL_INTERVAL:-5}"
	if [ -z "$req_id" ]; then
		ok "$label done"
		return 0
	fi
	while [ "$waited" -lt "$timeout" ]; do
		QUIET_API_LOG=1
		bt_get "/request/$(urlq "$req_id")" && status="$(bt_data '.status')" || status="pending"
		QUIET_API_LOG=0
		case "$status" in
		Success)
			printf '\r%-110s\n' "" >&2
			ok "$label done ($(secs_fmt "$waited"))"
			return 0 ;;
		Error)
			# errorResponse carries the CSP's own sentence. Printed line by line and
			# uncut — cutting loses the end, which is where the cause is.
			detail="$(bt_data '.errorResponse')"
			printf '\r%-110s\n' "" >&2
			fail "$label failed"
			fail "  request : GET ${BT_LAST_URL:-?}  →  status Error"
			fail "  beetle says:"
			while IFS= read -r line; do
				fail "    $line"
			done <<< "$(printf '%s' "${detail:-(errorResponse was empty)}" | sed 's/\\n/\n/g; s/\\t/\t/g')"
			return 1 ;;
		esac
		printf '\r    %-100s' "$label — ${status:-Handling} ($(secs_fmt "$waited"))" >&2
		sleep "$interval"
		waited=$((waited + interval))
	done
	printf '\r%-110s\n' "" >&2
	fail "$label timed out after ${timeout}s. The resource may still be being created."
	return 1
}

# ---------------------------------------------------------------------------
# Naming
# ---------------------------------------------------------------------------
# Every resource is <prefix>-<csp>-<what>. The CSP is in the name because one
# namespace is shared by several CSPs and cb-tumblebug's resource ids are unique
# per namespace, not per connection — a plain cpbos-os-raw-data would mean the
# AWS bucket and the NCP bucket at once, and a per-CSP cleanup would delete the
# wrong one.
#
# This is also what cleanup_all recognises its own resources by. There is no
# state file, so the name is the record.
resource_name() { printf '%s-%s-%s' "$MATRIX_NAME_PREFIX" "$(lower "$1")" "$2"; }

# os_name CSP BUCKET — the osId for one cell's target bucket.
#   One osId is one bucket: centipede rewrites a beetleObjectStorage destination's
#   path to the osId (pkg/core/plan/target.go resolveObjectStorageDstPath), so a
#   cell that shared an osId with another cell would write into the same bucket.
os_name() {
	local b
	b="$(lower "$2" | tr -c 'a-z0-9' '-' | sed 's/-\{2,\}/-/g; s/-$//')"
	resource_name "$1" "os-${b}"
}

# os_name_prefix CSP — what every bucket this folder creates starts with.
#   cleanup_all deletes exactly the buckets whose name begins with this.
os_name_prefix() { resource_name "$1" "os-"; }

# connection_name CSP — has to equal what cm-beetle's GenerateConnectionName
#   builds: <csp>-<region>. A connection registered under any other name is
#   invisible to beetle.
#
#   Lowercased as a whole, because beetle does that:
#     connectionName := strings.ToLower(fmt.Sprintf("%s-%s", csp, region))
#     (pkg/core/migration/object-storage.go GenerateConnectionName)
#   A region id can be upper case — cloudinfo.yaml has KR for ncp — so lowering
#   only the CSP would give ncp-KR, which no connection has, and the call ends in
#   400 "Cannot find the model.ConnConfig".
connection_name() { lower "$(printf '%s-%s' "$1" "$(csp_env "$1" REGION)")"; }

_os_path() { printf '/migration/middleware/ns/%s/objectStorage' "$(urlq "$MATRIX_NS")"; }

# ---------------------------------------------------------------------------
# Pre-flight and namespace
# ---------------------------------------------------------------------------
beetle_preflight() {
	if ! bt_get "/readyz"; then
		bt_report "cannot reach cm-beetle: $BEETLE_URL"
		die "This folder starts neither beetle nor tumblebug — they must already be up."
	fi
	if ! tb_get "/readyz"; then
		bt_report "cannot reach cb-tumblebug: $TUMBLEBUG_URL"
		die "beetle hands every resource call to tumblebug, so nothing would work."
	fi
	ok "cm-beetle and cb-tumblebug answer"
}

# ---------------------------------------------------------------------------
# Which CSPs can hold a managed bucket
# ---------------------------------------------------------------------------
# Asked at run time rather than kept in a table here. The managed-RDBMS matrix
# has to keep BEETLE_ENGINES_<CSP> by hand because tumblebug's capability
# endpoint pins dbEngine to mysql and cannot be asked the question; object
# storage has an endpoint that answers it, so there is nothing to maintain.
OS_SUPPORT_CACHE=""

# os_supported_csps — the CSPs beetle reports, space separated, on stdout.
os_supported_csps() {
	if [ -z "$OS_SUPPORT_CACHE" ]; then
		bt_get "/recommendation/middleware/objectStorage/support" || return 1
		OS_SUPPORT_CACHE="$(bt_jq -r '(.supports // {}) | keys | join(" ")')"
	fi
	printf '%s' "$OS_SUPPORT_CACHE"
}

# assert_os_support "CSP ..." — before anything is created.
#   Every CSP is checked at once: learning that the second one is unsupported
#   after the first has already run wastes the first.
assert_os_support() {
	local wanted="$1" available csp missing=""
	if ! available="$(os_supported_csps)"; then
		bt_report "could not read the object storage support matrix"
		fail "  Without it a bucket could be asked for on a CSP that has none, so this stops here."
		return 1
	fi
	if [ -z "$available" ]; then
		fail "cm-beetle reports no CSP as supporting object storage."
		fail "  Check that cb-tumblebug and cb-spider are up and the catalogue has loaded."
		return 1
	fi
	for csp in $wanted; do
		in_list "$csp" "$available" && continue
		missing="${missing:+$missing }$csp"
	done
	if [ -n "$missing" ]; then
		fail "cm-beetle cannot create object storage on: $missing"
		fail "  Supported today: $available"
		fail "  Fix OS_CSPS in ${ENV_FILE:-.env}. Nothing was created."
		return 1
	fi
	info "object storage support confirmed: $wanted  (beetle offers: $available)"
	return 0
}

# assert_connection CSP — is the connection actually registered, before anything
#   is created? Caught here, we can say both what is missing and what is there.
assert_connection() {
	local csp="$1" conn names
	conn="$(connection_name "$csp")"
	if tb_get "/connConfig/$(urlq "$conn")"; then
		info "connection confirmed: $conn"
		return 0
	fi
	bt_report "cb-tumblebug has no connection called '$conn'"
	fail "  Check $(upper "$csp")_REGION matches the registered region (currently: $(csp_env "$csp" REGION))."
	fail "  Credentials are registered by the deployments stack, not by this folder:"
	fail "    cd ../.. && make init"
	names="$(tb_get "/connConfig" >/dev/null 2>&1 && bt_jq -r \
		--arg p "$(lower "$csp")" '[.connectionconfig[]? | select(.configName | startswith($p)) | .configName] | join(", ")')"
	[ -n "$names" ] && fail "  Registered $(lower "$csp") connections: $names"
	return 1
}

ensure_namespace() {
	local body
	if tb_get "/ns/$(urlq "$MATRIX_NS")" >/dev/null 2>&1; then
		info "namespace $MATRIX_NS — already there"
		return 0
	fi
	body="$(jq -n --arg name "$MATRIX_NS" \
		'{name:$name, description:"cm-centipede object storage migration matrix (beetle)"}')"
	if ! tb_post "/ns" "$body"; then
		bt_report "could not create the namespace: $MATRIX_NS"
		return 1
	fi
	ok "namespace created: $MATRIX_NS"
}

# ---------------------------------------------------------------------------
# Inflight markers — the one thing the list API cannot tell us
# ---------------------------------------------------------------------------
# A create whose request reached the CSP but whose record never reached
# cb-tumblebug appears in no list, so cleanup_all cannot find it. bt_async
# already sends a request id of our own making so it can be followed even if the
# 202 never arrives; this writes that id down.
#
# It is not a state file. It records intent for the seconds a create is in
# flight, and nothing reads it to decide what exists — the list API does that.
INFLIGHT_FILE=""

inflight_file() {
	INFLIGHT_FILE="${LOG_DIR:-$BEETLE_ROOT/logs}/.inflight-$(lower "$1").json"
	printf '%s' "$INFLIGHT_FILE"
}

# inflight_mark CSP NAME REQ_ID BUCKET
inflight_mark() {
	local f tmp
	f="$(inflight_file "$1")"
	mkdir -p "$(dirname "$f")" 2>/dev/null || return 0
	[ -f "$f" ] || echo '[]' > "$f"
	tmp="${f}.tmp"
	jq --arg n "$2" --arg r "${3:-}" --arg b "$4" --arg t "$(date '+%F %T')" \
		'[ .[] | select(.name != $n) ] + [{name:$n, requestId:$r, bucket:$b, startedAt:$t}]' \
		"$f" > "$tmp" 2>/dev/null && mv -f "$tmp" "$f"
	rm -f "$tmp"
	return 0
}

# inflight_clear CSP NAME
inflight_clear() {
	local f tmp
	f="$(inflight_file "$1")"
	[ -f "$f" ] || return 0
	tmp="${f}.tmp"
	jq --arg n "$2" '[ .[] | select(.name != $n) ]' "$f" > "$tmp" 2>/dev/null && mv -f "$tmp" "$f"
	rm -f "$tmp"
	return 0
}

# inflight_report CSP "NAME1 NAME2 …" — names the list API did return.
#   Anything marked but absent from that list is reported, never deleted: we
#   cannot delete what cb-tumblebug has no record of, and guessing it does not
#   exist is exactly the mistake that leaves a bucket behind.
inflight_report() {
	local csp="$1" seen="$2" f name req bucket started
	f="$(inflight_file "$csp")"
	[ -f "$f" ] || return 0
	while IFS=$'\t' read -r name req bucket started; do
		[ -n "$name" ] || continue
		case " $seen " in *" $name "*) continue ;; esac
		fail ""
		fail "⚠ $name — a create was requested but cb-tumblebug has no record of it."
		fail "    source bucket ${bucket:-?}   requested at ${started:-?}"
		fail "    request id    ${req:-(none)}"
		[ -n "$req" ] && \
		fail "    curl -s '${BEETLE_URL%/}/request/$req' -u '<user>:<pass>' | jq ."
		fail "    Check the $(upper "$csp") console (region $(csp_env "$csp" REGION)) before assuming it does"
		fail "    not exist. If it is there, delete it in the console — nothing here can reach it."
		fail "    Once handled, remove the entry: $f"
	done <<< "$(jq -r '.[] | [.name, .requestId, .bucket, .startedAt] | @tsv' "$f" 2>/dev/null)"
	return 0
}

# ---------------------------------------------------------------------------
# The managed bucket
# ---------------------------------------------------------------------------
# Three layers of identifier are read, because they answer different questions.
#   OS_ID (the API id)      the name used in beetle/cb-tumblebug paths, and the
#                           value centipede's beetleObjectStorage ref carries
#   OS_UID                  cb-tumblebug's internal handle. For a bucket it is
#                           also the real name in the CSP
#   OS_CSP_NAME / _CSP_ID   what the CSP console shows
# The last layer is what makes a manual console deletion possible when automatic
# deletion has failed.
OS_ID=""; OS_STATUS=""; OS_UID=""; OS_CSP_NAME=""; OS_CSP_ID=""

# os_info CSP NAME — read the bucket into OS_*. 1 when it is not there.
#   A single-resource read is also the refresh: cb-tumblebug asks cb-spider and
#   overwrites status with the live answer before storing it again.
os_info() {
	local name="$2"
	bt_get "$(_os_path)/$(urlq "$name")" || return 1
	OS_ID="$(bt_data '.id')"
	[ -n "$OS_ID" ] || OS_ID="$(bt_data '.name')"
	[ -n "$OS_ID" ] || OS_ID="$name"
	OS_STATUS="$(bt_data '.status')"
	OS_UID="$(bt_data '.uid')"
	OS_CSP_NAME="$(bt_data '.cspResourceName')"
	OS_CSP_ID="$(bt_data '.cspResourceId')"
	return 0
}

os_csp_label() {
	if [ -n "$OS_CSP_NAME" ] || [ -n "$OS_CSP_ID" ] || [ -n "$OS_UID" ]; then
		printf 'CSP bucket %s  CSP id %s  uid %s' \
			"${OS_CSP_NAME:-(none)}" "${OS_CSP_ID:-(none)}" "${OS_UID:-(none)}"
	else
		printf 'no CSP resource info (not created yet, or the lookup failed)'
	fi
}

# os_report_for_console CSP NAME — everything needed to find and delete it by
#   hand. Called wherever a bucket may have been left behind.
os_report_for_console() {
	local csp="$1" name="$2"
	os_info "$csp" "$name" >/dev/null 2>&1 || true
	fail "  To find it in the console:"
	fail "    osId (API)   $name        namespace $MATRIX_NS"
	fail "    CSP bucket   ${OS_CSP_NAME:-${OS_UID:-(unknown)}}"
	fail "    CSP id       ${OS_CSP_ID:-(unknown)}"
	fail "    tumblebug uid ${OS_UID:-(unknown)}   region $(csp_env "$csp" REGION)"
	fail "  Reclaim it with: ./scripts/os-matrix.sh --cleanup"
}

# recommend_objectstorage CSP SRC_BUCKET OBJECTS BYTES — the payload, on stdout.
#
# ── The body describes a SOURCE, not the bucket to create ───────────────────
#   That is the whole idea of the endpoint: hand beetle a bucket that exists, and
#   it answers with a target the CSP can actually hold. The feature flags are
#   what the SOURCE uses, and each one the target CSP cannot honour comes back as
#   a warning — MinIO's buckets here have none of them on, so all four are false.
#
#   bucketName in the request is the source's name and identifies nothing on the
#   target: cb-tumblebug names the real CSP bucket after a uid of its own.
recommend_objectstorage() {
	local csp="$1" bucket="$2" objects="${3:-0}" bytes="${4:-0}" region body result w
	region="$(csp_env "$csp" REGION)"

	body="$(jq -nc --arg csp "$(lower "$csp")" --arg region "$region" \
		--arg bucket "$bucket" \
		--argjson objects "${objects:-0}" --argjson bytes "${bytes:-0}" \
		'{desiredCloud: {csp:$csp, region:$region},
		  sourceObjectStorages: [ {
		    bucketName:        $bucket,
		    versioningEnabled: false,
		    corsEnabled:       false,
		    encryptionEnabled: false,
		    isPublic:          false,
		    totalSizeBytes:    $bytes,
		    objectCount:       $objects,
		    accessFrequency:   "frequent",
		    tags: { createdBy: "beetle-os-test", purpose: "cm-centipede-test" }
		  } ]}')"

	if ! bt_post "/recommendation/middleware/objectStorage?desiredCsp=$(urlq "$(lower "$csp")")&desiredRegion=$(urlq "$region")" "$body"; then
		bt_report "object storage recommendation failed: $csp $bucket"
		return 1
	fi
	result="$(bt_payload)"

	# warnings is where beetle says "I could not do what you asked". Shown as is.
	# This function's stdout is the recommendation JSON, so it must go to stderr.
	while IFS= read -r w; do
		[ -n "$w" ] && warn "beetle: $w" >&2
	done <<< "$(printf '%s' "$result" | jq -r '.warnings[]? // empty')"

	if [ "$(printf '%s' "$result" | jq '(.targetObjectStorages // []) | length')" -eq 0 ]; then
		fail "beetle recommended no bucket for $csp."
		return 1
	fi
	printf '%s' "$result"
}

# inject_bucket_name REC NAME — the recommendation always names the bucket
#   mig-os-01, so every cell would collide. THIS body is what the create call
#   reads, so the name has to be written in here.
inject_bucket_name() {
	printf '%s' "$1" | jq -c --arg name "$2" \
		'.targetObjectStorages = [ .targetObjectStorages[] | .bucketName = $name ]'
}

# create_bucket CSP SRC_BUCKET OBJECTS BYTES — stand up one cell's target bucket.
#   Fills OS_ID and the OS_* set, then returns 0.
create_bucket() {
	local csp="$1" bucket="$2" objects="${3:-0}" bytes="${4:-0}" name rec body req_id
	name="$(os_name "$csp" "$bucket")"
	OS_ID="$name"

	if os_info "$csp" "$name"; then
		# A bucket left by an earlier run is not reused: it may hold that run's
		# objects, and validation compares the target's whole inventory against the
		# source's, so an extra object is a FAIL with a confusing reason.
		fail "$name already exists (status ${OS_STATUS:-?})."
		fail "  $(os_csp_label)"
		fail "  A target bucket is created per cell and deleted with it, so one left over"
		fail "  is from an interrupted run. Reclaim it and try again:"
		fail "    ./scripts/os-matrix.sh --cleanup"
		return 1
	fi

	rec="$(recommend_objectstorage "$csp" "$bucket" "$objects" "$bytes")" || return 1
	body="$(inject_bucket_name "$rec" "$name")"

	printf '%s' "$body" | jq -r '.targetObjectStorages[0] |
		"    versioning  \(.versioningEnabled // false)",
		"    encryption  \(.encryptionEnabled // false)",
		"    public      \(.isPublic // false)"'

	step "creating the target bucket: $name"
	if ! req_id="$(bt_async POST "$(_os_path)" "$body")"; then
		# The request was refused, but it may still have reached the CSP. The
		# marker is written before the verdict for exactly this reason.
		inflight_mark "$csp" "$name" "${BT_REQUEST_ID:-}" "$bucket"
		bt_report "the bucket create request was refused: $name"
		return 1
	fi
	inflight_mark "$csp" "$name" "$req_id" "$bucket"
	[ -n "$req_id" ] && info "request id: $req_id"
	if ! bt_wait "$req_id" "creating $name" "${BUCKET_TIMEOUT:-300}"; then
		# Failed or timed out, the CSP may still have built it. Left unsaid it
		# would sit there unnoticed, so the name is printed here.
		os_report_for_console "$csp" "$name"
		return 1
	fi

	if ! os_info "$csp" "$name"; then
		fail "$name was created but cannot be read back (namespace $MATRIX_NS)."
		os_report_for_console "$csp" "$name"
		return 1
	fi
	ok "$name  [$OS_STATUS]  $(os_csp_label)"
	return 0
}

# ---------------------------------------------------------------------------
# Deleting a bucket — the plain call first, force second
# ---------------------------------------------------------------------------
# After a migration the target bucket holds objects, so the plain DELETE will
# almost always answer 409 BucketNotEmpty. It is still tried first, because it is
# the only call that proves the bucket is gone from the CSP:
#
#   cb-tumblebug, DeleteObjectStorage (src/core/resource/objectStorage.go):
#       if !force { present, err := ResourcePresentOnCsp(...)
#                   if err != nil || present { → ErrDeletionUnconfirmed, record kept } }
#
#   With option=force that gate is skipped entirely, and cb-spider's own
#   DeleteS3Bucket(conn, name, force="true") swallows a failed RemoveBucket and
#   deletes its metadata anyway (api-runtime/common-runtime/S3Manager.go).
#
# So a forced delete answering 204 is not proof the CSP bucket is gone. What
# force does do properly is empty the bucket: ForceEmptyAndDeleteBucket aborts
# incomplete multipart uploads, removes every object, version and delete marker,
# and verifies the bucket is empty before it tries the delete — failing loudly if
# anything remains. That is a real service, and it is why force is the working
# path here rather than a last resort.
#
# ⚠ cb-spider empties one object at a time inside a single 180s context. Fine for
#   this seed (36 objects); a much larger bucket would time out there first.
delete_bucket() {
	local csp="$1" name="$2" label="" attempt=0
	local retries="${DELETE_RETRIES:-3}" wait="${DELETE_RETRY_WAIT:-30}"
	[ -n "$name" ] || return 0

	# Read the CSP name before deleting. Asking afterwards would work most of the
	# time, but a half-removed record can come back empty — the clue for finding it
	# in the console is better secured up front.
	if os_info "$csp" "$name" >/dev/null 2>&1; then
		label="$(os_csp_label)"
	fi

	step "deleting the target bucket: $name"

	# 1) The plain delete. Succeeds only for an empty bucket, and only after
	#    cb-tumblebug has confirmed with the CSP that it is really gone.
	while :; do
		if bt_delete "$(_os_path)/$(urlq "$name")"; then
			ok "bucket deleted: $name  (confirmed gone from the CSP)"
			inflight_clear "$csp" "$name"
			return 0
		fi
		bt_absent && { ok "bucket already gone: $name"; inflight_clear "$csp" "$name"; return 0; }
		bt_not_empty && break
		if ! bt_deletion_unconfirmed || [ "$attempt" -ge "$retries" ]; then break; fi
		attempt=$((attempt + 1))
		warn "$name — the CSP is still releasing it. Retrying in ${wait}s (${attempt}/${retries})"
		sleep "$wait"
	done

	# 2) force — empty it, then delete it.
	if bt_not_empty; then
		info "plain delete refused: the bucket still holds objects — retrying with option=force"
	else
		bt_report_warn "plain delete failed — retrying with option=force"
	fi
	if ! bt_delete "$(_os_path)/$(urlq "$name")?option=force"; then
		if bt_absent; then
			ok "bucket already gone: $name"
			inflight_clear "$csp" "$name"
			return 0
		fi
		bt_report "could not delete the bucket: $name"
		fail "  ${label:-no CSP resource info}   region $(csp_env "$csp" REGION)"
		fail "  curl -X DELETE '${BEETLE_URL%/}$(_os_path)/$name?option=force' -u '<user>:<pass>'"
		return 1
	fi

	# 3) Read the list back. option=force skips cb-tumblebug's CSP existence gate,
	#    so a 204 says only that the record is gone. This at least confirms that
	#    much, and the warning says what it does not confirm.
	inflight_clear "$csp" "$name"
	if bt_get "$(_os_path)" && bt_jq -r '.objectStorage[]? | (.id // .name)' | grep -qxF "$name"; then
		bt_report_warn "the record for $name is still listed after a forced delete"
		fail "  ${label:-no CSP resource info}"
		return 1
	fi
	ok "bucket deleted with force: $name"
	warn "  option=force skips cb-tumblebug's CSP existence check and cb-spider swallows a"
	warn "  failed RemoveBucket, so this 204 confirms the record is gone, not the bucket."
	warn "  ${label:-no CSP resource info}   region $(csp_env "$csp" REGION)"
	return 0
}

# ---------------------------------------------------------------------------
# Cleanup — reclaim what a run left behind
# ---------------------------------------------------------------------------
# cb-tumblebug's namespace is the state. Buckets are recognised by name prefix,
# so nothing has to be remembered locally between runs.
cleanup_all() {
	local csp="$1" prefix name seen="" found=0

	prefix="$(os_name_prefix "$csp")"

	if ! bt_get "$(_os_path)"; then
		bt_report "could not list the managed buckets"
		fail "  Nothing was deleted. A list that could not be read is not an empty list."
		return 1
	fi

	while IFS= read -r name; do
		[ -n "$name" ] || continue
		seen="${seen:+$seen }$name"
	done <<< "$(bt_jq -r '.objectStorage[]? | (.id // .name) // empty')"

	for name in $seen; do
		case "$name" in
		"$prefix"*)
			found=1
			sub "reclaiming: $name"
			delete_bucket "$csp" "$name" || true ;;
		esac
	done
	[ "$found" -eq 0 ] && info "there is no $(upper "$csp") bucket of this matrix to reclaim."

	# Buckets this folder asked for that the namespace never recorded.
	inflight_report "$csp" "$seen"
	return 0
}

# cleanup_namespace — the one cb-tumblebug call in the cleanup path, because
#   cm-beetle has no namespace API.
#
#   Only when the namespace is empty, and only when that was actually read. It is
#   free to keep and the next run reuses it, so failing to delete it is not a
#   failure.
cleanup_namespace() {
	local left
	if [ "${KEEP_NAMESPACE:-0}" = "1" ]; then
		info "KEEP_NAMESPACE=1 — keeping namespace $MATRIX_NS."
		return 0
	fi
	if ! bt_get "$(_os_path)"; then
		warn "keeping namespace $MATRIX_NS — the bucket list could not be re-read."
		return 0
	fi
	left="$(bt_jq -r '[.objectStorage[]?] | length')"
	if [ "${left:-0}" -ne 0 ]; then
		warn "keeping namespace $MATRIX_NS — $left bucket(s) still in it."
		warn "  Some may belong to another run; this folder deletes only what its prefix names."
		return 0
	fi

	step "deleting namespace $MATRIX_NS (cb-tumblebug — cm-beetle has no namespace API)"
	if tb_delete "/ns/$(urlq "$MATRIX_NS")"; then
		ok "namespace deleted: $MATRIX_NS"
	else
		bt_report_warn "could not delete namespace $MATRIX_NS"
		warn "  Harmless to leave — the next run reuses it. By hand:"
		warn "    curl -X DELETE '${TUMBLEBUG_URL%/}/ns/$MATRIX_NS' -u '<user>:<pass>'"
	fi
	return 0
}
