#!/usr/bin/env bash
#
# lib/honeybee.sh — the cm-honeybee client (source collection)
#
# The matrix asks honeybee for two things.
#   1) hold one connection pointing at the source container
#   2) inspect the dataset through it
#
# ── Registering the connection installs an agent ────────────────────────────
# This is the one place where a filesystem source differs fundamentally from an
# object storage one, and everything else in this folder follows from it.
#
# honeybee does not read a remote filesystem itself. doImportFS refuses any
# source group that is not type "fs" — "filesystem inspection is supported only
# for fs source group" — and then reaches the data by running
# `curl localhost:8082/honeybee-agent/fs?...` INSIDE the source host over SSH
# (lib/ssh/ssh.go SendGetRequestToAgent). The agent has to be there first.
#
# Nothing here installs it. Registering the connection does:
# doGetConnectionInfo(id, refresh=true) calls ssh.RunAgent, and refresh is true
# on create (POST), on update (PUT) and on the explicit refresh endpoint. So
# hb_connection below — a plain POST-or-PUT — is also the install step.
#
# ⚠ doGetConnectionInfo switches on the group type and has no "fs" arm, so an fs
#   connection falls to the default — the same one an on-prem VM takes,
#   NewClientConn followed by RunAgent. The k8s escape at the top of that arm
#   cannot catch it either: an fs group takes no resource_type, so the field is
#   empty.
#
# What that costs is worth knowing before reading a slow run:
#   - RunAgent SFTPs busybox and copyAgent.sh in, runs the script under sudo,
#     and the script DOWNLOADS the agent binary from the internet
#   - it then polls the agent's readyz once a second, up to 30 times
# A first registration therefore takes tens of seconds and needs the source
# container to have outbound network access.
#
# ⚠ And it reports failure in the body, not in the status code. A connection
#   whose agent never came up still comes back 200, with agent_status "failed"
#   and the reason in agent_failed_message. hb_assert_connection reads both
#   rather than letting the next call fail with something unrelated.
#
# ── One connection for the whole run ────────────────────────────────────────
# beetle-os-test needs one connection per bucket, because honeybee stores one
# bucket's result per connection. A filesystem inspect is keyed by connection
# too, but there is only one dataset here and every CSP column migrates the same
# one — so one connection, one inspect, reused by every cell.
#
# ⚠ The SSH private key travels in the connection_info body, which is why
#   private_key is in common.sh's masking filter.

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
			--max-time "${HB_TIMEOUT:-600}" \
			-H 'Content-Type: application/json' -d "$data")"
		cmd="$(printf "curl -s -X %s '%s' \\\\\n  -H 'Content-Type: application/json' \\\\\n  -d '%s'" \
			"$method" "$(sq_escape "$HB_BASE$path")" \
			"$(sq_escape "$(printf '%s' "$data" | mask_json)")")"
	else
		code="$(curl -s -o "$out" -w '%{http_code}' -X "$method" "$HB_BASE$path" \
			--max-time "${HB_TIMEOUT:-600}")"
		cmd="curl -s -X $method '$(sq_escape "$HB_BASE$path")'"
	fi
	api_log "$method" "$path" "$cmd" "$code" "$out"
	printf '%s' "$code"
}

hb_ok() { [ "${1:-000}" -ge 200 ] && [ "${1:-000}" -lt 300 ]; }

# ---------------------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------------------
# Called before a single node is created. Learning that honeybee is not running
# should never cost a created VM first.
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
#
#   Type "fs" is not a choice. doImportFS rejects every other type by name.
#   It is the data-side counterpart of "onprem": the group says what is
#   collected from the host, not how the host is reached.
#
#   ⚠ A group found by name is reused as it is, so one carrying another type
#     would be picked up here and only rejected at the inspect, several minutes
#     and one agent install later. The type is checked rather than trusted.
hb_source_group() {
	local name="$1" id tmp body found
	tmp="$(mktemp)"
	hb_curl GET "/source_group" "" "$tmp" >/dev/null
	id="$(jq -r --arg n "$name" '.source_group[]? | select(.name==$n) | .id' "$tmp" 2>/dev/null | head -1)"
	if [ -n "$id" ] && [ "$id" != "null" ]; then
		found="$(jq -r --arg n "$name" '.source_group[]? | select(.name==$n) | .type' "$tmp" 2>/dev/null | head -1)"
		rm -f "$tmp"
		if [ "$found" != "fs" ]; then
			fail "the honeybee SourceGroup \"$name\" is type \"$found\", not \"fs\"."
			fail "  A filesystem inspect is refused for any other type."
			fail "  Delete it, or point HB_SOURCE_GROUP at another name:"
			fail "    curl -s -X DELETE $HB_BASE/source_group/$id"
			return 1
		fi
		printf '%s' "$id"
		return 0
	fi

	body="$(jq -cn --arg n "$name" --arg d "cm-centipede filesystem migration matrix" \
		'{name:$n, description:$d, type:"fs"}')"
	hb_curl POST "/source_group" "$body" "$tmp" >/dev/null
	id="$(jq -r '.id // empty' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	if [ -z "$id" ]; then
		fail "could not create the honeybee SourceGroup: $name"
		return 1
	fi
	printf '%s' "$id"
}

# _hb_conn_body NAME -> the ConnectionInfo body for the source container.
#
#   ssh_port is a string in honeybee's model, not a number, so it is passed as
#   one. No password is sent: the image refuses password authentication, and
#   cm-honeybee refuses a password on an fs connection anyway (HTTP 400) — the
#   key is the only credential it accepts.
#
#   fs_scan_path is what the inspect reads: the scan root belongs to the
#   connection, not to the import request, so it is set here once and every
#   later import uses it.
_hb_conn_body() {
	local name="$1"
	jq -n --arg n "$name" --arg d "beetle-fs-test source container (key auth, root)" \
		--arg ip "$(src_host)" \
		--arg port "$(src_ssh_port)" \
		--arg user "$SRC_SSH_USER" \
		--arg key "$(src_private_key)" \
		--arg path "$(src_path)" \
		'{name:$n, description:$d,
		  ip_address:$ip, ssh_port:$port, user:$user, private_key:$key,
		  fs_scan_path:$path}'
}

# What the last hb_connection call produced and reported about itself.
#
# ⚠ These are set, not printed, and hb_connection returns a status rather than an
#   id. A function that printed the id would have to be called in a command
#   substitution, and a command substitution is a subshell — the four status
#   variables would be assigned in it and lost on the way out, leaving
#   hb_assert_connection to judge a registration by four empty strings and call a
#   working source broken.
HB_CONN_ID=""
HB_CONN_STATUS=""
HB_CONN_MESSAGE=""
HB_AGENT_STATUS=""
HB_AGENT_MESSAGE=""

# hb_connection SG_ID — register (or re-register) the source connection.
#   Fills HB_CONN_ID and the four status variables. 0 when the call itself
#   succeeded; hb_assert_connection judges what it achieved.
#
#   Either verb installs the agent, so both are slow the first time and both are
#   worth the same check afterwards. PUT rather than "leave it alone" because the
#   key pair is new every run: a connection left holding the previous run's key
#   would authenticate against nothing.
hb_connection() {
	local sg="$1" name id tmp body code
	name="${HB_SOURCE_GROUP:-cpbfs-matrix}-src"
	body="$(_hb_conn_body "$name")"
	tmp="$(mktemp)"
	HB_CONN_ID=""; HB_CONN_STATUS=""; HB_CONN_MESSAGE=""
	HB_AGENT_STATUS=""; HB_AGENT_MESSAGE=""

	hb_curl GET "/source_group/$sg/connection_info" "" "$tmp" >/dev/null
	id="$(jq -r --arg n "$name" '.connection_info[]? | select(.name==$n) | .id' "$tmp" 2>/dev/null | head -1)"
	if [ -n "$id" ] && [ "$id" != "null" ]; then
		info "  updating the existing connection ($name) — this reinstalls the agent"
		code="$(hb_curl PUT "/source_group/$sg/connection_info/$id" "$body" "$tmp")"
	else
		info "  registering the connection ($name) — honeybee installs its agent now"
		code="$(hb_curl POST "/source_group/$sg/connection_info" "$body" "$tmp")"
		id="$(jq -r '.id // empty' "$tmp" 2>/dev/null)"
	fi

	HB_CONN_STATUS="$(jq -r '.connection_status // ""' "$tmp" 2>/dev/null)"
	HB_CONN_MESSAGE="$(jq -r '.connection_failed_message // ""' "$tmp" 2>/dev/null)"
	HB_AGENT_STATUS="$(jq -r '.agent_status // ""' "$tmp" 2>/dev/null)"
	HB_AGENT_MESSAGE="$(jq -r '.agent_failed_message // ""' "$tmp" 2>/dev/null)"

	if ! hb_ok "$code"; then
		fail "could not register the connection (HTTP $code): $name"
		mask_json < "$tmp" | sed 's/^/      /' >&2
		rm -f "$tmp"
		return 1
	fi
	rm -f "$tmp"
	[ -n "$id" ] || { fail "could not obtain ConnectionInfo: $name"; return 1; }
	HB_CONN_ID="$id"
	info "  ConnectionInfo: $id"
	return 0
}

# hb_assert_connection — read what the registration actually achieved.
#
#   A registration answers 200 whether or not honeybee could log in and whether
#   or not the agent came up; the two status fields are the only report. Checked
#   here the run stops with the reason, rather than at the inspect with a message
#   about filesystem information that says nothing about SSH or about a download.
hb_assert_connection() {
	local bad=0

	case "$HB_CONN_STATUS" in
	success)
		ok "  honeybee reached the source over SSH ($(src_endpoint) as $SRC_SSH_USER)" ;;
	*)
		bad=1
		fail "honeybee could not reach the source over SSH — connection_status=${HB_CONN_STATUS:-unknown}"
		[ -n "$HB_CONN_MESSAGE" ] && fail "  honeybee says: $HB_CONN_MESSAGE"
		fail "  It was handed $(src_endpoint), user $SRC_SSH_USER, key authentication."
		fail "  HOST_IP has to be an address HONEYBEE can reach, which is rarely the same as"
		fail "  one this script can: honeybee normally runs as a container, and 127.0.0.1"
		fail "  there is honeybee itself. It is detected as the docker bridge gateway"
		fail "  (currently ${HOST_IP:-unset}); set HOST_IP in .env to override." ;;
	esac

	case "$HB_AGENT_STATUS" in
	success)
		ok "  the cm-honeybee agent is installed and answering on the source" ;;
	*)
		bad=1
		fail "the cm-honeybee agent is not running on the source — agent_status=${HB_AGENT_STATUS:-unknown}"
		[ -n "$HB_AGENT_MESSAGE" ] && fail "  honeybee says: $HB_AGENT_MESSAGE"
		fail "  honeybee installs it by running copyAgent.sh on the source, which DOWNLOADS"
		fail "  the binary from raw.githubusercontent.com and media.githubusercontent.com."
		fail "  The source container needs outbound access to both. That URL is hard-coded"
		fail "  in cm-honeybee, so nothing in this folder can point it elsewhere."
		fail "  What the install itself logged, on the source:"
		fail "    docker exec $(src_container) cat /tmp/honeybee-agent-install.log"
		fail "    docker exec $(src_container) systemctl status cm-honeybee-agent" ;;
	esac

	[ "$bad" -eq 0 ]
}

# ---------------------------------------------------------------------------
# Collection
# ---------------------------------------------------------------------------

# hb_import SG_ID CONN_ID — inspect the dataset through this connection.
#
#   No metric is requested. Each one costs a full walk of the tree on the source,
#   and the plan reads Path and Folders[].Path and nothing else; the file count
#   and byte total this folder prints come from the container's own find and du
#   instead (src_stats).
#
#   max_depth is left at its default too. The plan is built against the scan root
#   rather than any listed folder — the whole dataset migrates as one — so the
#   depth of the listing changes what is reported, not what moves.
#
#   The scan root is not sent: it is the connection's fs_scan_path, set by
#   hb_connection. What is left in the body describes this one collection.
hb_import() {
	local sg="$1" conn="$2" tmp code body
	body="$(jq -cn --argjson d "${FS_SCAN_MAX_DEPTH:-0}" \
		'if $d > 0 then {max_depth:$d} else {} end')"
	tmp="$(mktemp)"
	code="$(hb_curl POST "/source_group/$sg/connection_info/$conn/import/fs" "$body" "$tmp")"
	if ! hb_ok "$code"; then
		fail "source collection failed (HTTP $code) — honeybee could not inspect $(src_path)."
		fail "  The inspect runs inside the source, through the agent, over SSH. If the"
		fail "  registration reported both statuses as success, the agent has since stopped:"
		fail "    docker exec $(src_container) systemctl status cm-honeybee-agent"
		mask_json < "$tmp" | sed 's/^/      /' >&2
		rm -f "$tmp"
		return 1
	fi
	rm -f "$tmp"
}

# hb_source_model SG_ID CONN_ID -> centipede's source model JSON on stdout.
#
#   /fs/refined already returns the whole SourceDataMigrationModel wrapper —
#   connection ref included — which is exactly what POST /plans/target takes as
#   its "source". Nothing is assembled here; the response goes through unchanged.
hb_source_model() {
	local sg="$1" conn="$2" tmp code out
	tmp="$(mktemp)"
	code="$(hb_curl GET "/source_group/$sg/connection_info/$conn/fs/refined" "" "$tmp")"
	if ! hb_ok "$code"; then
		fail "could not read the collection result (HTTP $code)"
		rm -f "$tmp"
		return 1
	fi
	out="$(jq -c '.' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	if [ -z "$out" ] || [ "$(printf '%s' "$out" | jq -r '(.sourceDataMigrationModel.fileSystems // []) | length')" = "0" ]; then
		fail "honeybee returned no filesystem in the collection result."
		return 1
	fi
	printf '%s' "$out"
}

# hb_scan_root SRC_MODEL — the path the plan will treat as the source.
#
#   Read back rather than assumed, because it is what the plan's targetMapping
#   has to match exactly: selectMigrationPath compares targetMapping.srcName
#   against this string and the listed folders, and a mapping that matches
#   neither is a 400 rather than a silently wider migration.
hb_scan_root() {
	printf '%s' "$1" | jq -r '.sourceDataMigrationModel.fileSystems[0].path // ""' 2>/dev/null
}

# hb_folder_count SRC_MODEL — how many directories the inspect listed.
hb_folder_count() {
	printf '%s' "$1" | jq -r '(.sourceDataMigrationModel.fileSystems[0].folders // []) | length' 2>/dev/null
}
