#!/usr/bin/env bash
#
# lib/beetle.sh — the cm-beetle client (written for this folder)
#
# The provisioner, whole and entire: everything that creates, reads and deletes a
# managed instance, its network and its logical databases lives here. db-matrix.sh
# calls the functions below and knows nothing about cm-beetle itself, which is
# what keeps the matrix body, its verdicts and its table free of provisioner
# detail.
#
# ── Which server answers which call ─────────────────────────────────────────
# Every call about a resource goes through cm-beetle. cb-tumblebug is addressed
# directly in exactly three places, and each is a place beetle has no usable API:
#
#   namespace create/delete   cm-beetle has no namespace API — the routes exist
#                             in pkg/api/rest/server.go but are commented out,
#                             while every migration handler starts by reading the
#                             namespace and fails without it.
#   connection lookup         the connection catalogue is tumblebug's own.
#   engine version lists      beetle's /recommendation/middleware/rdbms/capability
#                             calls GetRDBMSCapability(connectionName), and the
#                             client inside pins dbEngine to "mysql"
#                             (pkg/client/tumblebug/rdbms.go). mariadb's version
#                             list cannot come out of that path.
#
# cb-tumblebug also exposes /ns/{ns}/resources/vNet and friends, and reaching for
# those would be easy. It is not done: that deletes around the back of the
# namespace beetle believes it is managing.
#
#   namespace     POST|DELETE {TUMBLEBUG}/ns[/{ns}]
#   versions      GET  {TUMBLEBUG}/rdbms/capability?dbEngine=…
#   connection    GET  {TUMBLEBUG}/connConfig/{name}
#   network       POST|GET|DELETE /migration/ns/{ns}/resources/vNet | securityGroup
#   recommend     POST /recommendation/middleware/rdbms
#   instance      POST|GET|DELETE /migration/middleware/ns/{ns}/rdbms[/{name}]
#   logical DB    POST|GET|DELETE /migration/middleware/ns/{ns}/rdbms/{name}/database[/{db}]
#   async track   GET  /request/{reqId}
#
# ── Where "what exists" is remembered ───────────────────────────────────────
# There is no local state file. cb-tumblebug's namespace is the state, and the
# list APIs are read back through beetle rather than trusted from disk.
#
# Ownership is carried by the names instead: every resource is
# <prefix>-<csp>-<what> inside a namespace this folder alone uses, so cleanup_all
# can tell its own resources from anyone else's without a record.
#
# One hole is left by that, and inflight_* fills it: a create whose request
# reached the CSP but whose record never reached tumblebug is in no list. See
# the inflight section.
#
# ── Three things to know before changing any of this ────────────────────────
#   - cm-beetle answers an unavailable version with a warning and a *different*
#     version at 200 rather than an error, so three checks guard it:
#     assert_target_versions, recommend_rdbms, and version_compatible after
#     creation. Removing any one of them lets the wrong version get built.
#   - The matrix cannot touch the managed instance's parameters. Columns whose
#     instance sets require_secure_transport=ON cannot have a logical database
#     created at all — see explain_insecure_transport.
#   - Both CSPs need a network. beetle creates a vNet with two subnets in two
#     zones plus a security group; there is no default-VPC shortcut on either.

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

# cm-beetle throttles its cb-tumblebug calls to about 1.6/s and answers a 503
# with Retry-After when there is no room. That is "in a moment", not a failure,
# so it is retried here.
BT_RATE_LIMIT_RETRIES="${MATRIX_RATE_LIMIT_RETRIES:-3}"

# _mask_header HEADER — hide the value of a header that carries a secret.
_mask_header() {
	case "$1" in
	[Xx]-[Aa]dmin-[Uu]ser-[Pp]assword:*) printf 'X-Admin-User-Password: ...' ;;
	*) printf '%s' "$1" ;;
	esac
}

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
  -H '$(sq_escape "$(_mask_header "$h")")'"
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

# tb_request — a direct cb-tumblebug call. Only the three cases in the header.
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
# null, which would turn publicAccess=false into an empty string.
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
#
#   \n inside a JSON string arrives escaped. Left alone it prints as one long
#   line, so it is turned back into real newlines here.
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

# There is no helper that shortens a message to one line. There used to be one
# that cut at 200 characters, and a CSP puts the code and the explanation at the
# END of its sentence, so the cut removed the cause ("... prohibited while
# --require_secure_transport" survived and "=ON" did not).

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

# bt_deletion_unconfirmed — did the delete go out but the CSP still hold it?
#   cb-tumblebug checks the CSP 5x3s after a delete and, if the resource is still
#   there, returns a failure without removing its record (vnet.go, issue #2685).
#   That is deliberate — removing the record first would strand the resource. So
#   this is "not yet", not a failure: wait and call again.
bt_deletion_unconfirmed() {
	printf '%s' "$BT_BODY" | grep -qi 'still exists on the CSP\|deletion unconfirmed'
}

# bt_delete_retry PATH LABEL — call again while a delayed delete finishes.
#   A vNet that held a managed database is not removed until the CSP has finished
#   tidying up behind it. NCP is slow enough that tumblebug's own 15s is not
#   enough.
#
#   action=force is not used. It skips the CSP check and deletes only the record,
#   leaving a VPC nobody knows about on the account. When this gives up it says
#   so and lets a person decide.
bt_delete_retry() {
	local path="$1" label="$2" attempt=0
	local retries="${DELETE_RETRIES:-3}" wait="${DELETE_RETRY_WAIT:-60}"

	while :; do
		if bt_delete "$path"; then return 0; fi
		if bt_absent; then return 0; fi
		if ! bt_deletion_unconfirmed || [ "$attempt" -ge "$retries" ]; then return 1; fi
		attempt=$((attempt + 1))
		warn "$label — the CSP is still releasing it. Retrying in ${wait}s (${attempt}/${retries})"
		sleep "$wait"
	done
}

# ---------------------------------------------------------------------------
# Asynchronous calls — creating a managed RDBMS holds a connection for up to 30
#   minutes when called synchronously. Prefer: respond-async gets a 202 plus a
#   request id, followed with GET /request/{id}.
#
#   The request id is made here rather than read from the response: it has to be
#   known even if the connection drops before the 202 arrives, which is also what
#   makes the inflight marker below useful.
# ---------------------------------------------------------------------------
BT_REQUEST_ID=""
bt_async() {
	local method="$1" path="$2" body="${3:-}"
	BT_REQUEST_ID="cpbdb-$(date +%s)-$RANDOM"
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
	local interval="${RDBMS_POLL_INTERVAL:-15}"
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
			# uncut - cutting loses the end, which is where the cause is.
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
# Credentials
# ---------------------------------------------------------------------------
# The managed DB master password. It lives in .env in plain text, protected by
# the file mode alone - there is no vault in this folder. Everything that needs
# it goes through here, so putting it back behind a secret store later means
# changing this function and nothing else.
#
#   Who needs it: the instance create call (adminUserPassword), every logical
#   database call (X-Admin-User-Password), and centipede's target connection.
db_password() { csp_env "$1" DB_PASSWORD; }

# _assert_ncp_password PASS — NCP's rules, checked before anything is created.
#   Without this the violation comes back as a failed creation about 30 minutes
#   later.
#     - 8 to 20 characters
#     - at least one letter, one digit and one special character
#     - allowed:   ~ ! @ # $ % ^ * ( ) - _ = [ ] { } ; : , . < > ?
#     - forbidden: ` & + \ " ' /  and whitespace
_assert_ncp_password() {
	local p="$1" bad=""
	[ "${#p}" -ge 8 ] && [ "${#p}" -le 20 ] || bad="$bad\n    - must be 8 to 20 characters (it is ${#p})"
	printf '%s' "$p" | grep -q '[A-Za-z]'          || bad="$bad\n    - must contain a letter"
	printf '%s' "$p" | grep -q '[0-9]'             || bad="$bad\n    - must contain a digit"
	printf '%s' "$p" | grep -q '[~!@#$%^*()_=[{};:,.<>?-]' \
		|| bad="$bad\n    - must contain one of ~ ! @ # \$ % ^ * ( ) - _ = [ ] { } ; : , . < > ?"
	printf '%s' "$p" | grep -q '[`&+\\"'"'"'/[:space:]]' \
		&& bad="$bad\n    - must not contain \` & + \\ \" ' / or whitespace"
	[ -z "$bad" ] && return 0
	fail "NCP_DB_PASSWORD does not satisfy NCP's rules:"
	printf '%b\n' "$bad" >&2
	fail "  Example: Cent1pede!2024"
	return 1
}

# assert_db_password CSP — before anything is created.
assert_db_password() {
	local csp pass
	csp="$(lower "$1")"
	pass="$(db_password "$csp")"
	if [ -z "$pass" ]; then
		fail "$(upper "$csp")_DB_PASSWORD is empty."
		fail "  Fill it in in ${ENV_FILE:-.env}. It is the master password of every managed"
		fail "  instance this run creates, and it cannot be read back from the CSP afterwards."
		return 1
	fi
	[ "$csp" = "ncp" ] && { _assert_ncp_password "$pass" || return 1; }
	return 0
}

# ---------------------------------------------------------------------------
# Naming
# ---------------------------------------------------------------------------
# Every resource is <prefix>-<csp>-<what>. The CSP is in the name because one
# namespace is shared by several CSPs and cb-tumblebug's resource ids are unique
# per namespace, not per connection.
#
# This is also what cleanup_all recognises its own resources by. There is no
# state file, so the name is the record.
resource_name() { printf '%s-%s-%s' "$MATRIX_NAME_PREFIX" "$(lower "$1")" "$2"; }
vnet_name()     { resource_name "$1" vnet; }
subnet_name()   { resource_name "$1" "subnet-$2"; }
sg_name()       { resource_name "$1" sg; }

# connection_name CSP — has to equal what cm-beetle's GenerateConnectionName
#   builds: <csp>-<region>. A connection registered under any other name is
#   invisible to beetle.
#
#   Lowercased as a whole, because beetle does that:
#     connectionName := strings.ToLower(fmt.Sprintf("%s-%s", csp, region))
#     (pkg/core/migration/object-storage.go GenerateConnectionName)
#   A region id can be upper case - cloudinfo.yaml has KR for ncp - so lowering
#   only the CSP would give ncp-KR, which no connection has, and the call ends in
#   400 "Cannot find the model.ConnConfig". The region id is used as written
#   elsewhere, so it is lowered here alone.
connection_name() { lower "$(printf '%s-%s' "$1" "$(csp_env "$1" REGION)")"; }

# rdbms_name CSP ENGINE VERSION — the version goes in the name so an instance
#   kept by KEEP_INSTANCE does not block the next version's creation. Dots and
#   the like become hyphens to satisfy cb-tumblebug's id rules.
rdbms_name() {
	local ver
	ver="$(printf '%s' "$3" | tr -c 'a-z0-9' '-' | sed 's/-\{2,\}/-/g; s/-$//')"
	resource_name "$1" "db-$(lower "$2")-${ver}"
}

# rdbms_name_prefix CSP — what every instance this folder creates starts with.
#   cleanup_all deletes exactly the instances whose name begins with this.
rdbms_name_prefix() { resource_name "$1" "db-"; }

_res_path()   { printf '/migration/ns/%s/resources/%s' "$(urlq "$MATRIX_NS")" "$1"; }
_rdbms_path() { printf '/migration/middleware/ns/%s/rdbms' "$(urlq "$MATRIX_NS")"; }

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

# assert_connection CSP — is the connection actually registered, before anything
#   is created? Without this it surfaces when the vNet is created, by which time
#   the namespace exists and the versions have been looked up. Caught here, we
#   can say both what is missing and what is there.
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
		'{name:$name, description:"cm-centipede managed-DB version matrix (beetle)"}')"
	if ! tb_post "/ns" "$body"; then
		bt_report "could not create the namespace: $MATRIX_NS"
		return 1
	fi
	ok "namespace created: $MATRIX_NS"
}

# ---------------------------------------------------------------------------
# Engines — two tables, both per CSP
# ---------------------------------------------------------------------------
# They answer different questions, which is why they are not merged.
#
#   MATRIX_ENGINES_<CSP>   what this CSP sells as a managed service. A fact about
#                          the CSP; it rarely changes.
#   BEETLE_ENGINES_<CSP>   what cm-beetle can actually create there today. This is
#                          the line to edit when beetle grows.
#
# Keeping them apart lets assert_engine_known say which of the two walls was hit,
# and they really are different walls: NCP has no managed MariaDB at all, while
# NCP PostgreSQL exists and beetle simply cannot ask for it yet.
MATRIX_ENGINES_AWS="mysql mariadb postgresql"   # DocumentDB has no public endpoint, so no mongodb
MATRIX_ENGINES_NCP="mysql postgresql mongodb"   # no managed MariaDB

# Overridable from .env so a newly-supported engine can be tried without patching
# this file. Promote the value here once it is confirmed.
BEETLE_ENGINES_AWS="${BEETLE_ENGINES_AWS:-mysql mariadb}"
BEETLE_ENGINES_NCP="${BEETLE_ENGINES_NCP:-mysql}"

csp_engines() {
	local n="MATRIX_ENGINES_$(upper "$1")"
	printf '%s' "${!n:-}"
}
beetle_engines() {
	local n="BEETLE_ENGINES_$(upper "$1")"
	printf '%s' "${!n:-}"
}

_in_list() {
	local want="$1" e
	shift
	for e in $1; do [ "$e" = "$want" ] && return 0; done
	return 1
}

# assert_engine_known ENGINE CSP — checked before any network call.
assert_engine_known() {
	local engine csp
	engine="$(lower "$1")"; csp="$(lower "$2")"

	if _in_list "$engine" "$(beetle_engines "$csp")"; then
		return 0
	fi

	if _in_list "$engine" "$(csp_engines "$csp")"; then
		# The CSP sells it; beetle cannot ask for it yet.
		fail "cm-beetle cannot create '$engine' on $(upper "$csp") yet."
		fail "  Its managed-RDBMS model declares dbEngine as enums:\"mysql,mariadb\", and for"
		fail "  anything else it returns a mysql recommendation instead of an error"
		fail "  (pkg/core/recommendation/rdbms.go), so the wrong engine would be built."
		fail "  Creatable on $(upper "$csp") today: $(beetle_engines "$csp")"
		fail "  If beetle has since gained it, widen BEETLE_ENGINES_$(upper "$csp") in ${ENV_FILE:-.env}."
		fail "  Drop it from $(upper "$csp")_ENGINES."
		return 1
	fi

	# The CSP has nothing this matrix could use.
	case "$csp:$engine" in
	aws:mongodb)
		fail "AWS has no managed MongoDB this matrix can use."
		fail "  DocumentDB exposes no public endpoint, so the matrix host cannot reach it,"
		fail "  and EC2 self-hosting is out of scope. Run it on NCP." ;;
	ncp:mariadb)
		fail "NCP has no managed MariaDB (self-hosted on a server only)."
		fail "  mariadb columns run on AWS only." ;;
	*)
		fail "unknown engine '$engine' ($csp). This CSP offers: $(csp_engines "$csp")" ;;
	esac
	fail "  Drop it from $(upper "$csp")_ENGINES."
	return 1
}

# engine_port ENGINE — the port the managed service listens on.
engine_port() {
	case "$(lower "$1")" in
	mysql|mariadb) printf '3306' ;;
	postgresql)    printf '5432' ;;
	mongodb)       printf '27017' ;;
	*)             printf '' ;;
	esac
}

# ---------------------------------------------------------------------------
# Supported-version lookup
# ---------------------------------------------------------------------------
# Asked of cb-tumblebug with the engine stated. beetle's own capability endpoint
# pins dbEngine to mysql, so mariadb's list cannot come out of it.

# rdbms_supported_versions CSP ENGINE — one per line, version-sorted. Cached per run.
#
#   Sorted low to high because what the CSP returns is in no particular order -
#   the AWS driver sorts, NCP hands back the product list as it came - so a reader
#   cannot rely on it. Lexically "10.11" sorts before "10.6", hence sort -V.
#
#   The list is only ever used as a set, so the order changes no verdict.
rdbms_supported_versions() {
	local csp="$1" engine="$2" cache q
	cache="${MATRIX_TMP:-/tmp}/versions-$(lower "$csp")-$(lower "$engine").txt"
	if [ ! -f "$cache" ]; then
		q="connectionName=$(urlq "$(connection_name "$csp")")"
		q="${q}&providerName=$(urlq "$(lower "$csp")")"
		q="${q}&regionName=$(urlq "$(csp_env "$csp" REGION)")"
		q="${q}&dbEngine=$(urlq "$(lower "$engine")")"
		if tb_get "/rdbms/capability?${q}"; then
			bt_jq -r '.supports.supportedVersions[]? // empty' | sort -V > "$cache"
		else
			: > "$cache"
		fi
	fi
	cat "$cache"
}

# _version_offered "AVAILABLE" WANTED — is WANTED one of the offered versions?
#
#   Three ways to match, in order of strictness:
#     exact           "8.0.36" == "8.0.36"
#     same after norm the same version written differently. NCP has "8.4.8" in the
#                     catalogue and reports "MYSQL8.4.8" on the instance it built.
#     component prefix "8.0" matches "8.0.36" but not "8.40". AWS resolves a prefix
#                     to the current minor, and beetle's selectEngineVersion does
#                     the same, so a prefix is a legitimate way to ask.
_version_offered() {
	local available="$1" wanted="$2" a
	printf '%s\n' "$available" | grep -qxF "$wanted" && return 0
	while IFS= read -r a; do
		[ -n "$a" ] || continue
		[ "$(version_norm "$a")" = "$(version_norm "$wanted")" ] && return 0
		version_compatible "$wanted" "$a" && return 0
	done <<< "$available"
	return 1
}

# assert_target_versions CSP ENGINE "V1 V2 ..." — before anything is created.
#   cm-beetle does not error on a version it cannot offer: selectEngineVersion
#   attaches a warning and returns a different one from the supported list
#   (preferred -> supported[0]) with a 200. So it is stopped here.
assert_target_versions() {
	local csp="$1" engine="$2" wanted="$3" available missing="" v
	available="$(rdbms_supported_versions "$csp" "$engine")"

	if [ -z "$available" ]; then
		fail "$csp $engine: cb-tumblebug returned no supported-version list for this region."
		fail "  Check that the connection $(connection_name "$csp") is registered and that the"
		fail "  catalogue has finished loading. Without a version list an instance could be"
		fail "  built at the wrong version, so this stops here."
		return 1
	fi

	for v in $wanted; do
		_version_offered "$available" "$v" && continue
		missing="${missing:+$missing }$v"
	done

	if [ -n "$missing" ]; then
		fail "$csp $engine: target versions this region does not offer — $missing"
		fail "  Available: $(printf '%s' "$available" | tr '\n' ' ')"
		fail "  Fix $(upper "$csp")_$(upper "$engine")_DST_VERSIONS. Nothing was created."
		return 1
	fi
	info "$csp $engine target versions confirmed: $wanted  (offered: $(printf '%s' "$available" | tr '\n' ' '))"
	return 0
}

# ---------------------------------------------------------------------------
# Network — vNet with two subnets, plus a security group.
#
#   Both CSPs. There is no default-VPC shortcut here, and a managed RDBMS wants
#   subnets in two zones on either CSP.
# ---------------------------------------------------------------------------
NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""; NET_VNET_STATUS=""

# subnet_cidr CSP INDEX — an explicit value if given, otherwise the third octet
#   of the /16 is varied.
subnet_cidr() {
	local csp="$1" index="$2" explicit base prefix
	explicit="$(csp_env "$csp" "SUBNET${index}_CIDR")"
	if [ -n "$explicit" ]; then printf '%s' "$explicit"; return 0; fi
	base="$(csp_env "$csp" VNET_CIDR)"
	prefix="${base#*/}"
	if [ "$prefix" != "16" ]; then
		die "$(upper "$csp")_VNET_CIDR is a /$prefix. Subnets are only derived automatically from a /16.
       Set $(upper "$csp")_SUBNET1_CIDR and _SUBNET2_CIDR yourself."
	fi
	printf '%s' "$base" | awk -F'[./]' -v i="$index" '{ printf "%s.%s.%s.0/24", $1, $2, i }'
}

# firewall_rules CSP ENGINES — only the engine ports (this folder creates no VM,
#   so 22 is not needed).
#   The JSON keys are PascalCase because cb-tumblebug's FirewallRuleReq declares
#   them that way. Lower case is silently ignored and yields a group with no rules.
firewall_rules() {
	local csp="$1" engines="$2" engine port ports=""
	for engine in $engines; do
		port="$(engine_port "$engine")"
		[ -n "$port" ] || continue
		printf ',%s,' "$ports" | grep -q ",${port}," && continue
		ports="${ports:+$ports,}$port"
	done
	[ -n "$ports" ] || ports="3306"
	jq -n --arg ports "$ports" --arg cidr "${MATRIX_ALLOWED_CIDR:-0.0.0.0/0}" \
		'[ { Ports: $ports, Protocol: "TCP", Direction: "inbound", CIDR: $cidr } ]'
}

find_vnet() {
	local name; name="$(vnet_name "$1")"
	bt_get "$(_res_path vNet)" || return 1
	NET_VNET_ID="$(bt_jq -r --arg n "$name" '.vNet[]? | select(.name == $n) | .id // empty')"
	[ -n "$NET_VNET_ID" ] || return 1
	NET_SUBNET_IDS="$(bt_jq -c --arg n "$name" '[ .vNet[]? | select(.name == $n) | .subnetInfoList[]? | .id ]')"
	NET_SUBNET_IDS="${NET_SUBNET_IDS:-[]}"
	# The status is read too. Having a record and being usable are different
	# things - a half-created vNet still appears in the list.
	NET_VNET_STATUS="$(bt_jq -r --arg n "$name" '.vNet[]? | select(.name == $n) | .status // empty')"
	return 0
}

find_sg() {
	local name; name="$(sg_name "$1")"
	bt_get "$(_res_path securityGroup)" || return 1
	NET_SG_ID="$(bt_jq -r --arg n "$name" '.securityGroup[]? | select(.name == $n) | .id // empty')"
	[ -n "$NET_SG_ID" ]
}

create_vnet() {
	local csp="$1" body
	body="$(jq -n \
		--arg name "$(vnet_name "$csp")" --arg conn "$(connection_name "$csp")" \
		--arg cidr "$(csp_env "$csp" VNET_CIDR)" \
		--arg s1 "$(subnet_name "$csp" 1)" --arg c1 "$(subnet_cidr "$csp" 1)" --arg z1 "$(csp_env "$csp" ZONE)" \
		--arg s2 "$(subnet_name "$csp" 2)" --arg c2 "$(subnet_cidr "$csp" 2)" --arg z2 "$(csp_env "$csp" ZONE2)" \
		'{name:$name, connectionName:$conn, cidrBlock:$cidr,
		  description:"Created by the cm-centipede beetle DB version matrix",
		  subnetInfoList:[{name:$s1, ipv4_CIDR:$c1, zone:$z1},
		                  {name:$s2, ipv4_CIDR:$c2, zone:$z2}]}')"
	step "creating the vNet: $(vnet_name "$csp") (zones $(csp_env "$csp" ZONE), $(csp_env "$csp" ZONE2))"
	if ! bt_post "$(_res_path vNet)" "$body"; then
		bt_report "could not create the vNet: $(vnet_name "$csp")"
		return 1
	fi
	find_vnet "$csp" || { fail "the vNet was created but cannot be read back (namespace $MATRIX_NS)"; return 1; }
	ok "vNet $(vnet_name "$csp") ($NET_VNET_ID)"
}

# drop_stale_vnet CSP — remove a vNet record the CSP has been shown not to have.
#   cb-tumblebug's list and single reads both come from its own KV store and never
#   ask the CSP (GetVNet, src/core/resource/vnet.go). So a VPC deleted at the CSP
#   still reads as present, and it only surfaces when a security group is created
#   on top of it. The CSP has just said it does not exist, so this record guards
#   nothing.
drop_stale_vnet() {
	local csp="$1" reason="${2:-an unusable record}"
	warn "$reason — removing the leftover cb-tumblebug record."
	if bt_delete_retry "$(_res_path vNet)/$(urlq "$NET_VNET_ID")?action=withsubnets" "vNet $NET_VNET_ID"; then
		ok "stale vNet record removed: $NET_VNET_ID"
	else
		bt_report "could not remove the stale vNet record: $NET_VNET_ID"
		return 1
	fi
	NET_VNET_ID=""; NET_SUBNET_IDS=""
	return 0
}

# ensure_network CSP ENGINES — idempotent; an existing one is reused by id.
#
#   "in the record" and "at the CSP" are different. The list is served from
#   tumblebug's KV store, so a failed earlier run's leftovers and a VPC deleted
#   from the console both still show. Two places are therefore checked: the
#   record's status, and what the CSP answers when a security group is created on
#   it. If the CSP says the VPC does not exist, the record is dropped and one
#   rebuild is attempted.
ensure_network() {
	local csp="$1" engines="$2" body attempt=0

	while :; do
		if find_vnet "$csp"; then
			case "$NET_VNET_STATUS" in
			Available|"")
				info "vNet $(vnet_name "$csp") — already there ($NET_VNET_ID, ${NET_VNET_STATUS:-status not reported})" ;;
			Creating|Deleting|Registering|Deregistering)
				fail "vNet $(vnet_name "$csp") is $NET_VNET_STATUS — wait for it to settle and run again."
				return 1 ;;
			*)
				drop_stale_vnet "$csp" "vNet $(vnet_name "$csp") has status $NET_VNET_STATUS" || return 1
				create_vnet "$csp" || return 1 ;;
			esac
		else
			create_vnet "$csp" || return 1
		fi

		if find_sg "$csp"; then
			info "security group $(sg_name "$csp") — already there ($NET_SG_ID)"
			break
		fi

		body="$(jq -n \
			--arg name "$(sg_name "$csp")" --arg conn "$(connection_name "$csp")" \
			--arg vnet "$NET_VNET_ID" --argjson rules "$(firewall_rules "$csp" "$engines")" \
			'{name:$name, connectionName:$conn, vNetId:$vnet,
			  description:"Created by the cm-centipede beetle DB version matrix",
			  firewallRules:$rules}')"
		step "creating the security group: $(sg_name "$csp")"
		if bt_post "$(_res_path securityGroup)" "$body"; then
			find_sg "$csp" || { fail "the security group was created but cannot be read back"; return 1; }
			ok "security group $(sg_name "$csp") ($NET_SG_ID)"
			break
		fi

		# If the CSP says it does not know the VPC, the vNet record is stale. Drop
		# it and rebuild once - a second identical answer has another cause.
		case "$(bt_raw_message)" in
		*"does not exist in connection"*|*"VPC"*"does not exist"*|*"not found"*"VPC"*)
			if [ "$attempt" -eq 0 ]; then
				attempt=1
				bt_report_warn "could not create the security group: $(sg_name "$csp")"
				drop_stale_vnet "$csp" "the CSP says that VPC does not exist" || return 1
				continue
			fi ;;
		esac

		bt_report "could not create the security group: $(sg_name "$csp")"
		return 1
	done

	if [ "$(printf '%s' "$NET_SUBNET_IDS" | jq 'length')" -lt 2 ]; then
		warn "the vNet has fewer than 2 subnets — a managed RDBMS wants two zones"
	fi
	return 0
}

# _instances_left CSP — how many managed instances the namespace still holds.
#   Prints a number on success. Fails (and prints nothing) when the list could not
#   be read, which is NOT the same as zero: an unread list must never authorise a
#   deletion. The distinction is the whole reason this returns a status.
_instances_left() {
	bt_get "$(_rdbms_path)" || return 1
	bt_jq -r '[.rdbms[]?] | length'
}

# release_network CSP — the security group first; it references the vNet.
#
#   The vNet is only touched once the instance list has been read AND is empty.
#   Deleting a vNet with a database still in it is either refused by the CSP or
#   strands the database, and a list that could not be read tells us nothing.
release_network() {
	local csp="$1" left
	if ! left="$(_instances_left "$csp")"; then
		bt_report_warn "could not list the managed instances — keeping the network."
		warn "  A list that could not be read is not an empty list. Run again in a moment."
		return 1
	fi
	if [ "${left:-0}" -ne 0 ]; then
		warn "keeping the network — $left managed instance(s) are still in namespace $MATRIX_NS."
		return 1
	fi

	if find_sg "$csp"; then
		step "deleting the security group: $(sg_name "$csp")"
		if bt_delete_retry "$(_res_path securityGroup)/$(urlq "$NET_SG_ID")" "security group $(sg_name "$csp")"; then
			ok "security group deleted"
		else
			bt_report_warn "could not delete the security group: $(sg_name "$csp")"
		fi
	fi
	if find_vnet "$csp"; then
		step "deleting the vNet: $(vnet_name "$csp")"
		# withsubnets — a vNet with subnets left in it is not deleted.
		if bt_delete_retry "$(_res_path vNet)/$(urlq "$NET_VNET_ID")?action=withsubnets" "vNet $(vnet_name "$csp")"; then
			ok "vNet deleted"
		else
			bt_report_warn "could not delete the vNet: $(vnet_name "$csp")"
			if bt_deletion_unconfirmed; then
				warn "  The CSP is still holding the vNet. cb-tumblebug kept its record, so running"
				warn "  again in a moment finishes the job (the same name is reused, so it is safe)."
				warn "  If it persists, delete the VPC in the console and then clear the record:"
				warn "    curl -X DELETE '${BEETLE_URL%/}$(_res_path vNet)/$NET_VNET_ID?action=force' -u '<user>:<pass>'"
				warn "  action=force skips the CSP check and deletes only the record — a VPC left"
				warn "  behind then belongs to nobody."
			fi
		fi
	fi
	NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""; NET_VNET_STATUS=""
	return 0
}

# ---------------------------------------------------------------------------
# Inflight markers — the one thing the list APIs cannot tell us
# ---------------------------------------------------------------------------
# A create whose request reached the CSP but whose record never reached
# cb-tumblebug appears in no list, so cleanup_all cannot find it and it keeps
# billing. bt_async already sends a request id of our own making so it can be
# followed even if the 202 never arrives; this writes that id down.
#
# It is not a state file. It records intent for the seconds a create is in
# flight, and nothing reads it to decide what exists - the list APIs do that.
INFLIGHT_FILE=""

inflight_file() {
	INFLIGHT_FILE="${LOG_DIR:-$BEETLE_ROOT/logs}/.inflight-$(lower "$1").json"
	printf '%s' "$INFLIGHT_FILE"
}

# inflight_mark CSP NAME REQ_ID ENGINE VERSION
inflight_mark() {
	local f tmp
	f="$(inflight_file "$1")"
	mkdir -p "$(dirname "$f")" 2>/dev/null || return 0
	[ -f "$f" ] || echo '[]' > "$f"
	tmp="${f}.tmp"
	jq --arg n "$2" --arg r "${3:-}" --arg e "$4" --arg v "$5" --arg t "$(date '+%F %T')" \
		'[ .[] | select(.name != $n) ] + [{name:$n, requestId:$r, engine:$e, version:$v, startedAt:$t}]' \
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
#   exist is exactly the mistake that leaves an instance billing.
inflight_report() {
	local csp="$1" seen="$2" f name req engine version started
	f="$(inflight_file "$csp")"
	[ -f "$f" ] || return 0
	while IFS=$'\t' read -r name req engine version started; do
		[ -n "$name" ] || continue
		case " $seen " in *" $name "*) continue ;; esac
		fail ""
		fail "⚠ $name — a create was requested but cb-tumblebug has no record of it."
		fail "    engine     ${engine:-?} ${version:-?}   requested at ${started:-?}"
		fail "    request id ${req:-(none)}"
		[ -n "$req" ] && \
		fail "    curl -s '${BEETLE_URL%/}/request/$req' -u '<user>:<pass>' | jq ."
		fail "    Check the $(upper "$csp") console (region $(csp_env "$csp" REGION)) before assuming it does not"
		fail "    exist. If it is there, delete it in the console — nothing here can reach it."
		fail "    Once handled, remove the entry: $f"
	done <<< "$(jq -r '.[] | [.name, .requestId, .engine, .version, .startedAt] | @tsv' "$f" 2>/dev/null)"
	return 0
}

# ---------------------------------------------------------------------------
# The managed instance
# ---------------------------------------------------------------------------
# Three layers of identifier are read, because they answer different questions.
#   RDBMS_NAME (the API id)   the name used in beetle/cb-tumblebug paths
#   RDBMS_UID                 cb-tumblebug's internal handle
#   RDBMS_CSP_NAME / _CSP_ID  what the CSP console shows
# The last layer is what makes a manual console deletion possible when automatic
# deletion has failed.
RDBMS_NAME=""; RDBMS_STATUS=""; RDBMS_ENDPOINT=""; RDBMS_HOST=""; RDBMS_PORT=""
RDBMS_USER=""; RDBMS_ENGINE=""; RDBMS_VERSION=""; RDBMS_PUBLIC=""
RDBMS_UID=""; RDBMS_CSP_NAME=""; RDBMS_CSP_ID=""

# rdbms_info CSP NAME — read the instance into RDBMS_*. 1 when it is not there.
#   A single-resource read is also the refresh: cb-tumblebug asks cb-spider and
#   overwrites endpoint / publicAccess / status with the live answer before
#   storing it again (core/resource/rdbms.go GetRDBMS). That is what makes a
#   public domain issued in the NCP console show up here.
rdbms_info() {
	local csp="$1" name="$2"
	bt_get "$(_rdbms_path)/$(urlq "$name")" || return 1
	RDBMS_STATUS="$(bt_data '.status')"
	RDBMS_ENDPOINT="$(bt_data '.endpoint')"
	RDBMS_USER="$(bt_data '.adminUserName')"
	RDBMS_ENGINE="$(bt_data '.dbEngine')"
	RDBMS_VERSION="$(bt_data '.dbEngineVersion')"
	RDBMS_PUBLIC="$(bt_data '.publicAccess')"
	RDBMS_UID="$(bt_data '.uid')"
	RDBMS_CSP_NAME="$(bt_data '.cspResourceName')"
	RDBMS_CSP_ID="$(bt_data '.cspResourceId')"
	RDBMS_HOST="${RDBMS_ENDPOINT%%:*}"
	RDBMS_PORT="${RDBMS_ENDPOINT##*:}"
	case "$RDBMS_PORT" in
	''|*[!0-9]*) RDBMS_PORT="$(engine_port "$RDBMS_ENGINE")" ;;
	esac
	# A successful read means the instance exists. The endpoint can still be empty
	# (it is being created); returning that as "absent" would send the caller off
	# to create it again under the same name and die on the collision. Whether
	# there is an address is the caller's question, asked through RDBMS_HOST.
	return 0
}

rdbms_csp_label() {
	if [ -n "$RDBMS_CSP_NAME" ] || [ -n "$RDBMS_CSP_ID" ]; then
		printf 'CSP name %s  CSP id %s' "${RDBMS_CSP_NAME:-(none)}" "${RDBMS_CSP_ID:-(none)}"
	else
		printf 'no CSP resource info (not created yet, or the lookup failed)'
	fi
}

# rdbms_report_for_console CSP NAME — everything needed to find and delete it by
#   hand. Called wherever a resource may have been left behind. If the record is
#   already gone the read fails, and the values last read are used instead.
rdbms_report_for_console() {
	local csp="$1" name="$2"
	rdbms_info "$csp" "$name" >/dev/null 2>&1 || true
	fail "  To find it in the console:"
	fail "    name (API)   $name        namespace $MATRIX_NS"
	fail "    CSP name     ${RDBMS_CSP_NAME:-(unknown)}"
	fail "    CSP id       ${RDBMS_CSP_ID:-(unknown)}"
	fail "    tumblebug uid ${RDBMS_UID:-(unknown)}   region $(csp_env "$csp" REGION)"
	fail "  Reclaim it with: ./scripts/$(lower "$csp")-db-matrix.sh --cleanup"
}

# recommend_rdbms CSP ENGINE VERSION — the recommendation payload, on stdout.
#   A recommendation creates nothing. This is where an engine substitution or a
#   version substitution is caught (check 2 of 3).
#
# ── The body describes a SOURCE, not the instance to create ─────────────────
#   That is the whole idea of the endpoint: hand beetle a machine that exists,
#   and it asks cb-tumblebug what the target CSP offers and answers with the
#   engine version, instance spec and storage that fit. So the numbers below are
#   "what our on-premises database runs on", not "what to order".
#
#     dbNode.cpu + dbNode.memory   ->  dbInstanceSpec   (2 cores / 4 GiB -> db.t3.medium)
#     dbNode.rootDisk.totalSize    ->  storageSize
#     dbEngine.engine/engineVersion->  dbEngineVersion, checked below
#
#   Units are the API's, not ours: memory is **GiB** and disks are **GB**
#   (rdbmsmodel.MemoryProperty / DiskProperty). cpus is sockets, cores is cores
#   per socket, threads is logical CPUs per socket — one socket with <vcpu>
#   cores is what a source VM normally looks like.
#
#   ⚠ targetPreferences is a preference, not an instruction. backupRetentionDays:0
#     comes back as 7 from AWS, because beetle applies the CSP's floor. The
#     values that matter to the matrix - engine, version - are verified below and
#     again after creation.
recommend_rdbms() {
	local csp="$1" engine="$2" version="$3" region body result got_engine got_version w
	region="$(csp_env "$csp" REGION)"

	body="$(jq -n \
		--arg csp "$(lower "$csp")" --arg region "$region" --arg zone "$(csp_env "$csp" ZONE)" \
		--arg engine "$(lower "$engine")" --arg version "$version" \
		--arg user "$(csp_env "$csp" DB_ADMIN_USERNAME)" \
		--argjson vcpu "$(csp_env_int "$csp" DB_SRC_VCPU 2)" \
		--argjson memory "$(csp_env_int "$csp" DB_SRC_MEMORY_GB 4)" \
		--argjson storage "$(csp_env_int "$csp" DB_SRC_STORAGE_GB 100)" \
		--argjson port "$(engine_port "$engine")" \
		--argjson public "$(csp_env_bool "$csp" DB_PUBLIC_ACCESS true)" \
		'{desiredCloud:({csp:$csp, region:$region}
		                + (if $zone == "" then {} else {zone:$zone} end)),
		  sourceRDBMSInstances:[{
		    displayName:"matrix-source-01",
		    dbEngine:{engine:$engine, engineVersion:$version, port:$port, role:"primary"},
		    dbNode:{cpu:{cpus:1, cores:$vcpu, threads:$vcpu},
		            memory:{totalSize:$memory},
		            rootDisk:{type:"SSD", totalSize:$storage}}}],
		  targetPreferences:({publicAccess:$public, highAvailability:false,
		                      backupRetentionDays:0}
		                     + (if $user == "" then {} else {adminUserName:$user} end))}')"

	if ! bt_post "/recommendation/middleware/rdbms?desiredCsp=$(urlq "$(lower "$csp")")&desiredRegion=$(urlq "$region")" "$body"; then
		bt_report "RDBMS recommendation failed: $csp $engine $version"
		return 1
	fi
	result="$(bt_payload)"

	# warnings is where beetle says "I could not do what you asked". Shown as is.
	# This function's stdout is the recommendation JSON, so it must go to stderr.
	while IFS= read -r w; do
		[ -n "$w" ] && warn "beetle: $w" >&2
	done <<< "$(printf '%s' "$result" | jq -r '.warnings[]? // empty')"

	got_engine="$(printf '%s' "$result" | jq -r '.targetRDBMSInstances[0].dbEngine // empty')"
	if [ "$(lower "$got_engine")" != "$(lower "$engine")" ]; then
		fail "beetle recommended '$got_engine' for a request for '$engine' ($csp)."
		fail "  It is documented fallback behaviour, but building a different engine than"
		fail "  the one asked for makes a test resource that answers the wrong question."
		fail "  Nothing was created."
		return 1
	fi

	got_version="$(printf '%s' "$result" | jq -r '.targetRDBMSInstances[0].dbEngineVersion // empty')"
	if ! version_compatible "$version" "$got_version"; then
		fail "beetle recommended '$got_version' for a request for '$version' ($csp $engine)."
		fail "  This is selectEngineVersion's fallback path — it is not the version asked for."
		fail "  Nothing was created."
		return 1
	fi

	printf '%s' "$result"
}

# inject_rdbms_target REC CSP NAME — make the create body say what .env says.
#
#   The recommendation is an answer, not an order form. Everything the matrix
#   decides for itself is written in here, over whatever came back, because THIS
#   body is what the create call reads — a value that only reached
#   targetPreferences never reaches the CSP.
#
#   vNetId/subnetIds/securityGroupIds : beetle does not fill these, and creation
#                                       fails when they are empty.
#   adminUserPassword                 : left empty, beetle substitutes its own default.
#   adminUserName                     : <CSP>_DB_ADMIN_USERNAME.
#   rdbmsName                         : the recommendation always says mig-rdbms-01,
#                                       so every column would collide.
#   publicAccess                      : <CSP>_DB_PUBLIC_ACCESS — see below.
#   backupRetentionDays               : <CSP>_DB_BACKUP_RETENTION_DAYS — see below.
#
#   Left as the recommendation returned it: dbInstanceSpec, storageType and
#   storageSize. Those are beetle's answer to the source profile in
#   <CSP>_DB_SRC_*, which is what those keys are for - overriding them here would
#   turn "describe the source" into "name the instance class" and quietly break
#   the one thing the recommendation is good at.
#
# ── Why publicAccess is asserted again ──────────────────────────────────────
#   A recommendation can lower it. On NCP it always does:
#
#     beetle: NCP Cloud DB does not provide external public IP by default;
#             instance(s) will be created within private VPC.
#
#   so a requested true comes back false and, left alone, false is what the CSP
#   is asked for. .env is where that decision belongs, so it is put back.
#
#   ⚠ It does not conjure a public domain on NCP - that is still a console step
#     (see the NCP notes in README.md). It only stops the request from disagreeing
#     with the configuration.
#
# ── Why the backup retention is forced back down ────────────────────────────
#   targetPreferences.backupRetentionDays=0 in the recommendation request comes
#   back as 7, so it has to be set again here, on the instance the create call
#   actually reads.
#
#   It is not about backups. On RDS, automated backups are what turn **binary
#   logging** on, and with binlog on MySQL refuses CREATE FUNCTION from an account
#   without SUPER — which no managed master account has:
#
#     Error 1419 (HY000): You do not have the SUPER privilege and binary logging
#     is enabled (you *might* want to use the less safe
#     log_bin_trust_function_creators variable)
#
#   The seed carries a function on purpose, so every MySQL and MariaDB cell dies
#   at restore while retention is non-zero. Declaring the function READS SQL DATA
#   does not help: that satisfies MySQL's *other* requirement (a logged function
#   must be deterministic or not modify data) and the SUPER check is separate.
#
#   Retention 0 disables automated backups, which is right for an instance that
#   lives for one column and is also quicker to create and delete. The other way
#   out is a parameter group with log_bin_trust_function_creators=1, and there is
#   no parameter-group API anywhere in this stack.
inject_rdbms_target() {
	local rec="$1" csp="$2" name="$3"
	printf '%s' "$rec" | jq -c \
		--arg name "$name" --arg vnet "$NET_VNET_ID" \
		--argjson subnets "${NET_SUBNET_IDS:-[]}" --arg sg "$NET_SG_ID" \
		--arg pass "$(db_password "$csp")" \
		--arg user "$(csp_env "$csp" DB_ADMIN_USERNAME)" \
		--argjson backup "$(csp_env_int "$csp" DB_BACKUP_RETENTION_DAYS 0)" \
		--argjson public "$(csp_env_bool "$csp" DB_PUBLIC_ACCESS true)" \
		'.targetRDBMSInstances = [ .targetRDBMSInstances[] | (
		      .rdbmsName = $name
		    | .vNetId = $vnet
		    | .subnetIds = $subnets
		    | .securityGroupIds = (if $sg == "" then [] else [$sg] end)
		    | .publicAccess = $public
		    | .backupRetentionDays = $backup
		    | .adminUserPassword = $pass
		    | (if $user == "" then . else .adminUserName = $user end)
		  ) ]'
}

# create_rdbms CSP ENGINE VERSION — stand up one column, connection details and all.
#   Fills RDBMS_NAME and the RDBMS_* set, then returns 0.
create_rdbms() {
	local csp="$1" engine="$2" version="$3" name rec body req_id
	name="$(rdbms_name "$csp" "$engine" "$version")"
	RDBMS_NAME="$name"

	if rdbms_info "$csp" "$name"; then
		case "$RDBMS_STATUS" in
		*Failed*|*Undefined*|*Terminated*|*Suspended*)
			fail "$name already exists but its status is $RDBMS_STATUS."
			fail "  The name is taken, so a new one cannot be created either. Delete it and run again."
			rdbms_report_for_console "$csp" "$name"
			return 1 ;;
		esac
		if [ "${REUSE_INSTANCE:-1}" != "1" ]; then
			fail "$name already exists (REUSE_INSTANCE=0, so it is not reused)."
			return 1
		fi
		if [ -z "$RDBMS_HOST" ]; then
			fail "$name already exists but has no endpoint yet (status $RDBMS_STATUS)."
			fail "  It may still be being created. Try again in a moment."
			return 1
		fi
		info "reusing the instance: $name  [$RDBMS_ENGINE $RDBMS_VERSION, $RDBMS_STATUS]"
		info "               $(rdbms_csp_label)"
	else
		rec="$(recommend_rdbms "$csp" "$engine" "$version")" || return 1
		body="$(inject_rdbms_target "$rec" "$csp" "$name")"

		# Printed from the body, not from the recommendation. They disagree wherever
		# .env overrules the answer, and what is about to be sent is the useful one
		# to see - reading the recommendation here would show a publicAccess the CSP
		# is never asked for.
		printf '%s' "$body" | jq -r '.targetRDBMSInstances[0] |
			"    engine      \(.dbEngine) \(.dbEngineVersion)",
			"    instance    \(.dbInstanceSpec // "(tumblebug chooses)")",
			"    storage     \(if (.storageType // "") == "" then "(CSP managed)" else "\(.storageType) \(.storageSize)GB" end)",
			"    admin       \(.adminUserName)",
			"    public      \(.publicAccess)",
			"    backups     \(.backupRetentionDays) day(s)"'

		step "creating the instance: $name (a managed RDBMS takes 5-30 min)"
		if ! req_id="$(bt_async POST "$(_rdbms_path)" "$body")"; then
			# The request was refused, but it may still have reached the CSP. The
			# marker is written before the verdict for exactly this reason.
			inflight_mark "$csp" "$name" "${BT_REQUEST_ID:-}" "$engine" "$version"
			bt_report "the instance create request was refused: $name"
			return 1
		fi
		inflight_mark "$csp" "$name" "$req_id" "$engine" "$version"
		[ -n "$req_id" ] && info "request id: $req_id"
		if ! bt_wait "$req_id" "creating $name" "${RDBMS_TIMEOUT:-2400}"; then
			# Failed or timed out, the CSP may still have built it. Left unsaid it
			# would bill unnoticed, so the name is printed here.
			rdbms_report_for_console "$csp" "$name"
			return 1
		fi

		if ! rdbms_info "$csp" "$name" || [ -z "$RDBMS_HOST" ]; then
			fail "$name was created but its connection details cannot be read (status ${RDBMS_STATUS:-?})."
			rdbms_report_for_console "$csp" "$name"
			return 1
		fi
	fi

	# Check 3 of 3 — was what got built the version that was asked for?
	if ! version_compatible "$version" "$RDBMS_VERSION"; then
		fail "$name was created as '$RDBMS_VERSION', not the requested '$version'."
		fail "  $(rdbms_csp_label)"
		return 1
	fi

	# And is it the engine that was asked for? The version is checked three times
	# and the engine name never was, because nothing depended on it: the matrix
	# used its own loop variable and only printed what beetle reported.
	#
	# Naming the target as a beetleDb reference changed that. centipede now reads
	# this very field and gates twice on it - validateManagedEngine refuses
	# anything but mysql and mariadb, and dbmsIdentityOf compares it, as a string,
	# with the db_type honeybee collected from the source. A decorated or
	# unexpected value therefore surfaces as "dbType mismatch" at plan time, once
	# per cell, after the instance is already built and billing. NCP is known to
	# decorate the version field (MYSQL8.4.8), which is reason enough not to
	# assume the engine field is plain.
	if [ "$(lower "$RDBMS_ENGINE")" != "$(lower "$engine")" ]; then
		fail "$name reports dbEngine '$RDBMS_ENGINE', not the requested '$engine'."
		fail "  cm-centipede reads this field through the beetleDb target reference and"
		fail "  compares it with the source engine cm-honeybee collected, so a mismatch"
		fail "  would fail every cell of this column at plan time with 'dbType mismatch'."
		fail "  $(rdbms_csp_label)"
		return 1
	fi
	ok "$name  [$RDBMS_ENGINE $RDBMS_VERSION]  $RDBMS_ENDPOINT  (admin $RDBMS_USER, public $RDBMS_PUBLIC)"
	ok "  $(rdbms_csp_label)"
	return 0
}

# delete_rdbms CSP NAME — a synchronous delete. cm-beetle declares no Prefer on
#   this call, so the connection stays open until the CSP is done. It has not hung.
delete_rdbms() {
	local csp="$1" name="$2" label=""
	[ -n "$name" ] || return 0

	# Read the CSP name before deleting. Asking afterwards would work most of the
	# time, but a half-removed record can come back empty - the clue for finding it
	# in the console is better secured up front.
	if rdbms_info "$csp" "$name" >/dev/null 2>&1; then
		label="$(rdbms_csp_label)"
	fi

	step "deleting the instance: $name (this takes minutes)"
	[ -n "$label" ] && info "  $label"

	# The same confirmation gate applies here - tumblebug returns "deletion
	# unconfirmed" with the record intact while the CSP still has it (rdbms.go).
	if bt_delete_retry "$(_rdbms_path)/$(urlq "$name")" "instance $name"; then
		ok "instance deleted: $name"
		inflight_clear "$csp" "$name"
		return 0
	fi
	bt_report "could not delete the instance: $name"
	fail "  ⚠ It keeps billing while it exists. Delete it in the console or with:"
	fail "    ${label:-no CSP resource info}   region $(csp_env "$csp" REGION)"
	fail "    curl -X DELETE '${BEETLE_URL%/}$(_rdbms_path)/$name' -u '<user>:<pass>'"
	return 1
}

# ---------------------------------------------------------------------------
# The cell's target database — a logical database inside the managed instance
#
#   CREATE DATABASE over the wire is not possible: a managed instance's master
#   account is granted rights on the databases the service knows about and cannot
#   make new ones (NCP answers Error 1044, Access denied for user 'dbadmin'@'%').
#   So the instance's owner, cm-beetle, is asked instead.
#
#   The API takes a name and the admin password, and nothing else - a character
#   set cannot be specified, so the logical database follows the instance
#   defaults. Table character sets travel inside the dump's CREATE TABLE
#   statements, and centipede reports a database-level difference as a warning.
#
#   This step exists because centipede does not create the target database: it
#   does so only for providerName "onprem", and a managed target is aws or ncp.
#   That check also runs at POST /plans/target, so a missing database fails the
#   plan, not the migration.
# ---------------------------------------------------------------------------

# target_db_kind ENGINE — how this engine's target database is made.
#   logical-db  through cm-beetle's logical database API
#   none        MongoDB: a database begins to exist on the first write, so there
#               is nothing to create in advance
target_db_kind() {
	case "$(lower "$1")" in
	mongodb) printf 'none' ;;
	*)       printf 'logical-db' ;;
	esac
}

logical_db_list() {
	local csp="$1" name="$2"
	bt_header "X-Admin-User-Password: $(db_password "$csp")"
	bt_get "$(_rdbms_path)/$(urlq "$name")/database" || return 1
	bt_jq -r '.databases[]? // empty'
}

logical_db_create() {
	local csp="$1" name="$2" db="$3" body raw
	body="$(jq -n --arg db "$db" --arg pw "$(db_password "$csp")" \
		'{databaseName:$db, adminUserPassword:$pw}')"
	if bt_post "$(_rdbms_path)/$(urlq "$name")/database" "$body"; then
		return 0
	fi
	raw="$(bt_raw_message)"
	bt_report "could not create the logical database: $db  (instance $name)"
	explain_insecure_transport "$raw" "$csp"
	return 1
}

logical_db_drop() {
	local csp="$1" name="$2" db="$3"
	bt_header "X-Admin-User-Password: $(db_password "$csp")"
	if bt_delete "$(_rdbms_path)/$(urlq "$name")/database/$(urlq "$db")"; then
		return 0
	fi
	bt_absent && return 0
	return 1
}

# target_db_clear CSP ENGINE "DB1 DB2 …" — remove anything a previous cell left
#   under these names, so the next cell starts from nothing.
#
#   transx-ex requires the target to exist and be empty, so a leftover fails as
#   target-not-empty. It matters just as much when centipede is the one creating
#   the database: a leftover makes centipede find a database it does not own, and
#   an unowned database takes the other undo path for the rest of the cell.
#
#   Failure to drop is not reported. There is usually nothing there, and whether
#   the name is usable is the next call's question, asked where it can be
#   answered.
target_db_clear() {
	local csp="$1" engine="$2" dbs="$3" db inst
	inst="${ACTIVE_INSTANCE:-$RDBMS_NAME}"

	if [ "$(target_db_kind "$engine")" = "none" ]; then
		mongodb_drop_target "$dbs" || return 1
		return 0
	fi

	for db in $dbs; do
		logical_db_drop "$csp" "$inst" "$db" >/dev/null 2>&1 || true
	done
	return 0
}

# target_db_create CSP ENGINE "DB1 DB2 …" — create the empty target databases.
target_db_create() {
	local csp="$1" engine="$2" dbs="$3" db inst
	inst="${ACTIVE_INSTANCE:-$RDBMS_NAME}"

	if [ "$(target_db_kind "$engine")" = "none" ]; then
		mongodb_drop_target "$dbs" || return 1
		info "target database: $dbs (MongoDB creates it on first write — nothing to make in advance)"
		return 0
	fi

	target_db_clear "$csp" "$engine" "$dbs"
	for db in $dbs; do
		logical_db_create "$csp" "$inst" "$db" || return 1
	done
	info "target database created: $dbs  (instance $inst, instance default character set)"
	return 0
}

# target_db_drop CSP ENGINE "DB1 DB2 …"
target_db_drop() {
	local csp="$1" engine="$2" dbs="$3" db inst rc=0
	inst="${ACTIVE_INSTANCE:-$RDBMS_NAME}"

	if [ "$(target_db_kind "$engine")" = "none" ]; then
		mongodb_drop_target "$dbs" || return 1
		info "target database dropped: $dbs"
		return 0
	fi

	for db in $dbs; do
		logical_db_drop "$csp" "$inst" "$db" || { bt_report_warn "could not drop the logical database: $db"; rc=1; }
	done
	[ "$rc" -eq 0 ] && info "target database dropped: $dbs"
	return "$rc"
}

# ---------------------------------------------------------------------------
# The creation probe — once per column, before any cell runs
# ---------------------------------------------------------------------------
# One logical database is created with the very call the cells use, and dropped
# again. If the probe passes, the cells' creation passes.
#
# It earns its keep. Creating a logical database goes through cb-spider's SQL
# fallback, which connects in plaintext to everything but IBM hosts, so an
# instance with require_secure_transport=ON refuses it - and there is no setting
# in this folder that changes that. Folding the column once and recording the
# sentence the CSP produced reads far better than every cell dying of the same
# error one at a time.
TARGET_PROBE_REASON=""

target_db_probe() {
	local csp="$1" engine="$2" probe
	TARGET_PROBE_REASON=""

	# Nothing to create on MongoDB, so nothing to probe. The connection itself was
	# already confirmed by wait_target_reachable.
	[ "$(target_db_kind "$engine")" = "none" ] && return 0

	probe="${MATRIX_NAME_PREFIX}_probe"
	sub "target database creation probe — $probe (cm-beetle logical database API)"

	if ! target_db_create "$csp" "$engine" "$probe"; then
		TARGET_PROBE_REASON="target database creation probe failed ($probe)"
		return 1
	fi

	# PostgreSQL wants one step more - being able to create a database and being
	# able to create a table in its public schema are different things, and the
	# migration needs the second. That check needs a psql client and this folder
	# runs no container that has one. It is not reachable today anyway, because
	# beetle cannot build a PostgreSQL instance at all; when it can, run the query
	# through the source container's psql, which src/Dockerfile.postgresql already
	# installs.

	target_db_drop "$csp" "$engine" "$probe" >/dev/null 2>&1 || true
	ok "probe passed — this column's cells can create a target database."
	return 0
}

# ---------------------------------------------------------------------------
# PostgreSQL privileges — not reachable yet
# ---------------------------------------------------------------------------
# This would ask the instance whether schema public was writable and whether a
# GRANT could be issued, then record the answer with the result. It needs a psql
# client, and it cannot be reached anyway while beetle refuses to build a
# PostgreSQL instance. The variables stay so db-matrix.sh and centipede.sh need no
# branch, and the function is a no-op that leaves them on the safe side.
PG_GRANT_EFFECTIVE="false"
PG_TARGET_SCHEMA=""

pg_privilege_preflight() {
	PG_GRANT_EFFECTIVE="false"
	PG_TARGET_SCHEMA=""
	return 0
}

# ---------------------------------------------------------------------------
# Cleanup — reclaim what a run left behind
# ---------------------------------------------------------------------------
# cb-tumblebug's namespace is the state. Instances are recognised by name prefix,
# so nothing has to be remembered locally between runs.
#
# Order matters: instances, then the security group, then the vNet, then the
# namespace. Everything but the last is a cm-beetle call.
cleanup_all() {
	local csp="$1" prefix name seen=""

	prefix="$(rdbms_name_prefix "$csp")"

	if ! bt_get "$(_rdbms_path)"; then
		bt_report "could not list the managed RDBMS instances"
		fail "  Nothing was deleted. A list that could not be read is not an empty list."
		return 1
	fi

	while IFS= read -r name; do
		[ -n "$name" ] || continue
		seen="${seen:+$seen }$name"
	done <<< "$(bt_jq -r '.rdbms[]? | (.id // .name) // empty')"

	local found=0
	for name in $seen; do
		case "$name" in
		"$prefix"*)
			found=1
			sub "reclaiming column: $name"
			delete_rdbms "$csp" "$name" || true ;;
		esac
	done
	[ "$found" -eq 0 ] && info "there is no instance of this matrix to reclaim."

	# Instances this folder asked for that the namespace never recorded.
	inflight_report "$csp" "$seen"

	sub "network"
	release_network "$csp" || true

	cleanup_namespace "$csp"
	return 0
}

# cleanup_namespace CSP — the one cb-tumblebug call in the cleanup path, because
#   cm-beetle has no namespace API.
#
#   Only when the namespace is empty, and only when that was actually read. It is
#   free to keep and the next run reuses it, so failing to delete it is not a
#   failure.
cleanup_namespace() {
	local csp="$1" left
	if [ "${KEEP_NETWORK:-0}" = "1" ]; then
		info "KEEP_NETWORK=1 — keeping namespace $MATRIX_NS."
		return 0
	fi
	if ! left="$(_instances_left "$csp")"; then
		warn "keeping namespace $MATRIX_NS — the instance list could not be re-read."
		return 0
	fi
	if [ "${left:-0}" -ne 0 ]; then
		warn "keeping namespace $MATRIX_NS — $left managed instance(s) still in it."
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

# ---------------------------------------------------------------------------
# Explaining a refused plaintext connection
# ---------------------------------------------------------------------------
# require_secure_transport is not the engine's upstream default; it is set by
# whoever created the instance. The official Docker images have it off at every
# version (10.6/11.8/8.0/8.4 checked), and the CSP turns it on through the default
# parameter group it attaches to a managed instance. So the same engine version
# only refuses plaintext when a managed service built it.
#
# The path that creates a logical database, cb-spider's SQL fallback, enables TLS
# for IBM hosts only:
#
#   // Enable TLS for IBM Cloud MySQL (skip server cert verification)
#   if strings.Contains(strings.ToLower(host), ".databases.appdomain.cloud") {
#       tlsSuffix = "?tls=skip-verify"
#   }
#   (cb-spider api-runtime/common-runtime/RDBMSManager.go, openRDBMSSQLConn)
#
# So it connects to AWS in plaintext and the server closes it with 3159. This is
# not something the matrix can work around - it is lower in the stack - so what
# can be done is spelled out instead.
#
# ⚠ TARGET_TLS_MODE does not help here. That value governs the migration
#   transx-ex performs, not the connection cb-spider makes.
# explain_dbtype_mismatch ERROR ENGINE — the source and target engines disagree.
#
#   With a beetleDb target the two sides of this comparison come from different
#   services: the source engine is what cm-honeybee collected, the target engine
#   is cm-beetle's dbEngine field, and centipede's dbmsIdentityOf compares them as
#   strings. create_rdbms already refuses a column whose dbEngine is not what was
#   asked for, so reaching this means the source is the side that differs.
explain_dbtype_mismatch() {
	case "$1" in
	*"dbType mismatch"*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: cm-centipede compares the engine cm-honeybee collected from the source"
	fail "         with the dbEngine cm-beetle reports for the target, as strings."
	fail "         The target side was already checked when the instance was created, so"
	fail "         it is the source that is reporting something unexpected."
	fail "  Check: the source container really runs ${2:-the requested engine} — honeybee's"
	fail "         db_type comes from the ConnectionInfo this script registered, and its"
	fail "         collection result is what the plan carries."
	fail "         See the api log for the import/db response."
}

# explain_centipede_beetle ERROR — cm-centipede could not reach cm-beetle.
#
#   New with the beetleDb target reference. Until then centipede never called
#   beetle from this folder at all, so the endpoint in centipede's own config was
#   load-bearing for nothing and a wrong value cost nothing. Now every plan starts
#   by asking beetle who the target is, and the answer arrives wrapped in
#   centipede's HTTP error, which says the call failed without saying that two
#   services are pointed at different beetles.
#
#   The messages come from pkg/client/beetle/client.go: "beetle unreachable" for a
#   transport failure, "beetle returned NNN" for a refusal, and "has no endpoint
#   yet" for an instance still being built.
explain_centipede_beetle() {
	case "$1" in
	*"beetle unreachable"*|*"beetle returned"*|*"beetle rdbms"*|*"parse beetle response"*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: this is cm-centipede talking to cm-beetle, not this script. The target"
	fail "         is named as a beetleDb reference (nsId=$MATRIX_NS, rdbmsId=${ACTIVE_INSTANCE:-$RDBMS_NAME}),"
	fail "         so centipede resolves the address through beetle itself."
	fail "  Check: centipede's own beetle endpoint has to be the beetle this script uses."
	fail "         this script : BEETLE_URL=$BEETLE_URL"
	fail "         centipede   : centipede.beetle.endpoint in conf/cm-centipede.yaml,"
	fail "                       or CENTIPEDE_BEETLE_ENDPOINT in its environment."
	fail "         centipede appends /beetle itself, so it wants host and port only."
	fail "         If centipede runs in a container, localhost there is not this host."
	fail "  Also  : the namespace must be the one centipede is asked for — $MATRIX_NS."
	fail "          beetle answers 404 for an instance in another namespace."
}

explain_insecure_transport() {
	case "$1" in
	*3159*|*"insecure transport"*|*"require_secure_transport"*|*"Connections using insecure"*|*"no pg_hba.conf entry"*|*"SSL off"*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: this instance has require_secure_transport on and refuses plaintext."
	fail "         Not an engine default — it comes from the parameter group the CSP"
	fail "         attaches to a managed instance. The official Docker images have it off"
	fail "         at every version, so the same engine version refuses plaintext only"
	fail "         when a managed service built it."
	fail "         Creating a logical database goes through cb-spider's SQL fallback, which"
	fail "         enables TLS for IBM hosts only (RDBMSManager.go openRDBMSSQLConn), so it"
	fail "         connects to every other CSP in plaintext. A constraint lower in the"
	fail "         stack; nothing in this folder works around it."
	fail "  Note : TARGET_TLS_MODE does not apply here. It governs the migration transx-ex"
	fail "         performs, not the connection cb-spider makes."
	fail "  What you can do:"
	fail "    1) Target a version whose managed instance is created with the value off."
	fail "       The console's default parameter group shows which. See the list this"
	fail "       region offers with:  ./scripts/$(lower "${2:-aws}")-db-versions.sh"
	fail "    2) Create a parameter group in the console with require_secure_transport=0,"
	fail "       attach it and reboot (manual — no parameter-group API anywhere in the stack)."
	fail "    3) Make cb-spider connect over TLS to other CSPs too (the one condition above)."
}

# explain_pg_maintenance_db ERROR — the "postgres" database transx-ex insists on.
#
#   transx-ex reaches PostgreSQL server-level operations through the maintenance
#   database, and the name is a constant, not a setting:
#
#     transx-ex/dbmsx/driver/postgresql/postgresql.go
#       const pgMaintenanceDB = "postgres"
#
#   Listing the target databases, reading the server version, creating and
#   dropping a database all connect there. NCP's managed PostgreSQL denies its
#   master account that database, so every PostgreSQL cell on NCP fails before any
#   data moves - with the target database already created and reachable.
#
#   This is a finding about centipede, not a setting this matrix got wrong, which
#   is why nothing here offers to work around it.
explain_pg_maintenance_db() {
	case "$1" in
	*'database "postgres"'*|*"database=postgres"*) ;;
	*) return 0 ;;
	esac
	case "$1" in
	*permission*|*denied*|*"does not exist"*|*FATAL*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: transx-ex connects to the \"postgres\" maintenance database for every"
	fail "         server-level PostgreSQL operation, and this instance does not allow"
	fail "         $RDBMS_USER to connect to it. The target database itself was created"
	fail "         and is reachable - only that one connection is refused."
	fail "  Where: transx-ex/dbmsx/driver/postgresql/postgresql.go, const pgMaintenanceDB"
	fail "  What you can do:"
	fail "    Nothing from this folder. The name is a constant, not a connection setting,"
	fail "    so no .env value or CLI option changes it."
	fail "    This is a result about cm-centipede on this CSP, which is what the matrix"
	fail "    exists to find - record the cell as FAIL and take it to centipede."
}
