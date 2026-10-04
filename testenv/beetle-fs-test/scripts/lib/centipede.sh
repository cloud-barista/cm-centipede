#!/usr/bin/env bash
#
# lib/centipede.sh — the cm-centipede client (plan, migration, validation)
#
# One cell makes four calls to centipede.
#
#   POST /plans/target               source model + target connection -> a runnable plan
#   POST /migration                  turn the plan into a run, started asynchronously
#   GET  /migration/{id}             poll progress until it settles
#   POST /migration/{id}/validation  + polling -> the verdict for this cell
#
# ── The target node is created by cm-beetle, not by centipede ───────────────
# Nothing in MigrateFileSystem creates anything: it resolves both ends and hands
# them to transx-ex. A beetleSsh destination is a reference to a node that has to
# exist already, which is what create_node (lib/beetle.sh) is for and why it is
# step 1 of a cell rather than step 2.
#
# ── The one filter this folder sends, and why it has to ─────────────────────
# beetle-os-test sends no filter at all: with none, selectMigrationPath takes the
# scan root as both source and destination, and for a beetleObjectStorage target
# the destination is rewritten to the osId anyway.
#
# Here the same "no filter" would resolve to dstPath == srcPath == "/testdata",
# and resolveObjectStorageDstPath leaves a beetleSsh destination alone — so the
# transfer would try to write the migrated tree to the target node's filesystem
# ROOT. It does not get that far: before rsync runs, transx-ex opens an SSH
# session and runs `mkdir -p /testdata` as the node's own user, with no sudo
# (storagex/executor-rsync.go ensureRemoteDir). That is a permission denied on
# every CSP image.
#
# So a targetMapping is sent, and nothing else:
#
#   fileSystemFilter.targetMapping = { srcName: <scan root>, dstName: <dst path> }
#
# rules stays empty, so the whole dataset still migrates — only where it lands
# changes. srcName must equal the scan root exactly, which is why it comes from
# honeybee's answer (hb_scan_root) rather than from .env.
#
# ── Why the destination path is asked for rather than configured ────────────
# dstName is the node's own $HOME plus a directory, read over SSH when the node
# is checked for rsync (node_probe in lib/beetle.sh). Building it from beetle's
# nodeUserName would assume the home directory follows the user name, and CSP
# images disagree about that often enough to matter.
#
# ── Strategy: relay ─────────────────────────────────────────────────────────
# FileSystemStrategy is left at centipede's default, which is relay: pull from
# the source to centipede's local staging, then push to the node. The
# alternative, agent-forward, needs an SSH agent set up on whatever host
# centipede runs on — a requirement about centipede's deployment rather than
# about this test, so it is not the thing to exercise by default.
#
# ── No dbmsOnFailure ────────────────────────────────────────────────────────
# It is DBMS-only, and CreateMigrationReq says why: a filesystem transfer leaves
# whatever it had already written, with or without the field. A cell has no
# rollback, which is why every cell gets a node of its own that is deleted with it.

if [ -n "${MATRIX_CENTIPEDE_SH:-}" ]; then return 0; fi
MATRIX_CENTIPEDE_SH=1

CP_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$CP_LIB_DIR/common.sh"

CP_BASE="${CP_BASE:-http://localhost:8085/centipede}"

# What a cell reads back
CP_PLAN=""            # the plan JSON
CP_ERROR=""           # one line saying why it failed
CP_MIGRATION_ID=""
CP_STATUS=""
CP_VALIDATION=""
CP_SRC_PATH=""        # what the plan decided to read from
CP_DST_PATH=""        # what the plan decided to write to

# ---------------------------------------------------------------------------
# HTTP — centipede requires BasicAuth on every call
# ---------------------------------------------------------------------------
cp_curl() {
	local method="$1" path="$2" data="${3:-}" out="$4" code cmd
	if [ -n "$data" ]; then
		code="$(curl -s -o "$out" -w '%{http_code}' -X "$method" "$CP_BASE$path" \
			-u "$CP_USER:$CP_PASS" -H 'Content-Type: application/json' -d "$data")"
		cmd="$(printf "curl -s -X %s '%s' \\\\\n  -u '%s:...' -H 'Content-Type: application/json' \\\\\n  -d '%s'" \
			"$method" "$(sq_escape "$CP_BASE$path")" "$CP_USER" \
			"$(sq_escape "$(printf '%s' "$data" | mask_json)")")"
	else
		code="$(curl -s -o "$out" -w '%{http_code}' -X "$method" "$CP_BASE$path" -u "$CP_USER:$CP_PASS")"
		cmd="curl -s -X $method '$(sq_escape "$CP_BASE$path")' -u '$CP_USER:...'"
	fi
	api_log "$method" "$path" "$cmd" "$code" "$out"
	printf '%s' "$code"
}

cp_ok() { [ "${1:-000}" -ge 200 ] && [ "${1:-000}" -lt 300 ]; }

cp_preflight() {
	local tmp code
	tmp="$(mktemp)"
	code="$(cp_curl GET "/readyz" "" "$tmp")"
	rm -f "$tmp"
	if ! cp_ok "$code"; then
		fail "cannot reach cm-centipede: $CP_BASE (HTTP $code)"
		fail "  Check that it is running and that CP_BASE / CP_USER / CP_PASS in .env are right."
		return 1
	fi
	ok "cm-centipede answers: $CP_BASE"
}

# ---------------------------------------------------------------------------
# The target connection — a beetleSsh reference
# ---------------------------------------------------------------------------
# A reference, not credentials. centipede resolves it through cm-beetle, which
# takes two calls to do it: the infra read supplies the address and the user, and
# the sshKey read supplies the private key (pkg/client/beetle GetSSHAccessInfo).
# No SSH key for the target exists anywhere in this folder, which is the whole
# reason the beetle path is worth testing separately from an inline ssh target.
#
# ⚠ This is the least-exercised destination in the repo: beetle-db-test uses
#   beetleDb and beetle-os-test uses beetleObjectStorage, so beetleSsh has had no
#   environment of its own until this one.
cp_dst_connection() {
	jq -n --arg ns "$MATRIX_NS" --arg infra "$1" --arg node "$2" \
		'{source:"beetleSsh", beetleSsh:{nsId:$ns, infraId:$infra, nodeId:$node}}'
}

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

# cp_plan SRC_MODEL INFRA_ID NODE_ID SCAN_ROOT DST_PATH
#   SRC_MODEL is honeybee's /fs/refined response, which is already a
#   SourceDataMigrationModel and goes in unchanged.
#   On success it fills CP_PLAN, CP_SRC_PATH and CP_DST_PATH and returns 0.
#
#   plans is an array: one entry per (source entry, destination). This cell
#   migrates one connection to one node, so it holds a single entry, and
#   srcConnection is lifted straight out of the source model - it is the
#   connection honeybee produced that entry with, so nothing has to be threaded
#   in alongside it.
cp_plan() {
	local src_model="$1" infra="$2" node="$3" scan_root="$4" dst_path="$5" tmp code body
	CP_PLAN=""; CP_ERROR=""; CP_SRC_PATH=""; CP_DST_PATH=""

	body="$(jq -cn --argjson s "$src_model" \
		--argjson d "$(cp_dst_connection "$infra" "$node")" \
		--arg src "$scan_root" --arg dst "$dst_path" \
		'{source:$s,
		  plans:[{
		    srcConnection: $s.sourceDataMigrationModel.fileSystems[0].connection,
		    dstConnection: $d,
		    fileSystemFilter:{targetMapping:{srcName:$src, dstName:$dst}}
		  }]}')"

	tmp="$(mktemp)"
	code="$(cp_curl POST "/plans/target" "$body" "$tmp")"

	if cp_ok "$code"; then
		CP_PLAN="$(jq -c 'if .success then .data else empty end' "$tmp" 2>/dev/null)"
		rm -f "$tmp"
		[ -n "$CP_PLAN" ] || { CP_ERROR="the plan response was empty"; return 1; }
		CP_SRC_PATH="$(printf '%s' "$CP_PLAN" | jq -r '.targetDataMigrationModel.fileSystems[0].folders[0].srcPath // ""' 2>/dev/null)"
		CP_DST_PATH="$(printf '%s' "$CP_PLAN" | jq -r '.targetDataMigrationModel.fileSystems[0].folders[0].dstPath // ""' 2>/dev/null)"
		if [ -z "$CP_SRC_PATH" ]; then
			CP_ERROR="the plan carries no filesystem folder (nothing would be migrated)"
			return 1
		fi
		return 0
	fi

	CP_ERROR="$(jq -r '.error // .message // "no response"' "$tmp" 2>/dev/null | head -1)"
	rm -f "$tmp"
	return 1
}

# cp_plan_summary — what the plan decided to move where.
#
#   dstPath is worth printing on its own. It is the one value in this folder that
#   is neither configured nor recommended but read off the target node itself, and
#   seeing it here — under the node's home rather than at "/testdata" — is the
#   confirmation that the transfer will have somewhere it is allowed to write.
cp_plan_summary() {
	printf '%s' "$CP_PLAN" | jq -r '
		(.targetDataMigrationModel.fileSystems // [])[]
		| "    strategy: \(.strategy // "?")   dstType: \(.dstType // "?")",
		  (.folders[]
		   | "    \(.order). \(.srcPath) -> \(.dstPath)   rules: \((.rules // []) | length)")' 2>/dev/null
}
# ---------------------------------------------------------------------------
# Migration
# ---------------------------------------------------------------------------

# cp_migrate NAME — turn CP_PLAN into a run (started asynchronously on creation).
cp_migrate() {
	local name="$1" tmp code body
	CP_MIGRATION_ID=""; CP_ERROR=""

	body="$(jq -cn --arg n "$name" \
		--arg d "filesystem migration matrix cell" \
		--argjson p "$CP_PLAN" \
		'{name:$n, description:$d, plan:$p}')"

	tmp="$(mktemp)"
	code="$(cp_curl POST "/migration" "$body" "$tmp")"
	if ! cp_ok "$code"; then
		CP_ERROR="$(jq -r '.error // .message // "no response"' "$tmp" 2>/dev/null | head -1)"
		rm -f "$tmp"
		return 1
	fi
	CP_MIGRATION_ID="$(jq -r 'if .success then .data.id else empty end' "$tmp" 2>/dev/null)"
	rm -f "$tmp"
	[ -n "$CP_MIGRATION_ID" ] || { CP_ERROR="no migration id came back"; return 1; }
}

# cp_poll — until CP_MIGRATION_ID settles. The final state lands in CP_STATUS.
cp_poll() {
	local waited=0 tmp line
	tmp="$(mktemp)"
	QUIET_API_LOG=1
	while :; do
		cp_curl GET "/migration/$CP_MIGRATION_ID" "" "$tmp" >/dev/null
		CP_STATUS="$(jq -r '.data.status // "unknown"' "$tmp" 2>/dev/null)"
		# at= is the file, object or table the server says it is on right now. It
		# is the only part of this line that moves while one large item copies, and
		# it is absent once nothing is in flight. Long paths keep their tail: the
		# end is what tells two of them apart.
		line="$(jq -r '"    status=\(.data.status // "?")  done=\(.data.processedItems // 0)/\(.data.totalItems // 0)  failed=\(.data.failedItems // 0)  sent=\(.data.transferredBytes // 0)B\(.data.currentObject // "" | if . == "" then "" else "  at=" + (if length > 40 then "..." + .[-37:] else . end) end)"' "$tmp" 2>/dev/null)"
		printf '\r%-110s' "$line" >&2
		case "$CP_STATUS" in
		completed|failed|cancelled) break ;;
		esac
		if [ "$waited" -ge "${MIGRATION_TIMEOUT:-1800}" ]; then
			CP_STATUS="timeout"
			break
		fi
		sleep "${POLL_INTERVAL:-5}"
		waited=$((waited + ${POLL_INTERVAL:-5}))
	done
	QUIET_API_LOG=0
	printf '\n' >&2
	rm -f "$tmp"

	# The failed items' reasons are gathered into CP_ERROR — they become the cell's
	# verdict detail.
	CP_ERROR=""
	if [ "$CP_STATUS" != "completed" ]; then
		CP_ERROR="$(cp_failed_items | head -1)"
		[ -n "$CP_ERROR" ] || CP_ERROR="status $CP_STATUS"
	fi
	[ "$CP_STATUS" = "completed" ]
}

# ---------------------------------------------------------------------------
# The migration log — GET /migration/{id}/logs
# ---------------------------------------------------------------------------
# ⚠ This endpoint is paginated: ListMigrationLogs reads page and pageSize through
#   ParsePageParams, which defaults to 20 and caps at 100. Asking for no page at
#   all returns the first 20 entries and says nothing about the rest — it reports
#   the real count in .data.total, and that is the only sign anything was left
#   out. Everything below walks to the end of the pages rather than reading one.
#
#   A filesystem cell logs one entry per migrated folder, and this folder sends
#   exactly one, so a page is plenty today. The walk is kept anyway: it costs one
#   call and it is what makes the count in the output trustworthy.

CP_LOG_PAGE_SIZE="${CP_LOG_PAGE_SIZE:-100}"   # the handler's maximum
CP_LOG_MAX_PAGES="${CP_LOG_MAX_PAGES:-50}"    # a stop, so a bad total cannot spin

# _cp_log_fetch OUT [STATUS] — collect every log entry into OUT as a JSON array.
_cp_log_fetch() {
	local out="$1" status="${2:-}" tmp page=1 total=0 got=0 code query
	printf '[]' > "$out"
	[ -n "$CP_MIGRATION_ID" ] || return 1
	tmp="$(mktemp)"
	while [ "$page" -le "$CP_LOG_MAX_PAGES" ]; do
		query="page=$page&pageSize=$CP_LOG_PAGE_SIZE"
		[ -n "$status" ] && query="$query&status=$status"
		code="$(cp_curl GET "/migration/$CP_MIGRATION_ID/logs?$query" "" "$tmp")"
		if ! cp_ok "$code"; then
			rm -f "$tmp"
			[ "$page" = "1" ] && return 1
			return 0
		fi
		total="$(jq -r '.data.total // 0' "$tmp" 2>/dev/null)"
		# A page that came back empty ends the walk whatever total claims.
		[ "$(jq -r '(.data.items // []) | length' "$tmp" 2>/dev/null)" = "0" ] && break
		jq -s '.[0] + (.[1].data.items // [])' "$out" "$tmp" > "$out.next" 2>/dev/null \
			&& mv "$out.next" "$out"
		got="$(jq -r 'length' "$out" 2>/dev/null)"
		[ "$got" -ge "$total" ] && break
		page=$((page + 1))
	done
	rm -f "$tmp" "$out.next"
	printf '%s' "$total" > "$out.total"
}

# cp_failed_items — the failed items as "path: reason"
cp_failed_items() {
	local all
	all="$(mktemp)"
	QUIET_API_LOG=1
	_cp_log_fetch "$all" failed
	QUIET_API_LOG=0
	jq -r '.[]? | "\(.itemPath): " +
	       ((.errorMsg // "no reason given") | rtrimstr("\n") | gsub("\n\\s*"; " | "))' \
		"$all" 2>/dev/null | cut -c1-400
	rm -f "$all" "$all.total"
}

# cp_migration_logs — the whole migration log, for a person to read.
cp_migration_logs() {
	local all total shown
	[ -n "$CP_MIGRATION_ID" ] || return 0
	all="$(mktemp)"
	if ! _cp_log_fetch "$all"; then
		warn "    the migration log could not be read (GET /migration/$CP_MIGRATION_ID/logs)"
		rm -f "$all" "$all.total"
		return 0
	fi
	total="$(cat "$all.total" 2>/dev/null || echo 0)"
	shown="$(jq -r 'length' "$all" 2>/dev/null || echo 0)"

	if [ "$shown" = "0" ]; then
		info "    the migration log is empty"
	else
		jq -r 'group_by(.status) | map("\(.[0].status)=\(length)") | "    items: " + join("  ")' \
			"$all" 2>/dev/null
		jq -r '.[]?
			| "    [\(.status // "?")] \(.itemPath // "?")  \(.durationMs // 0)ms"
			  + (if (.sizeBytes // 0) > 0 then "  \(.sizeBytes)B" else "" end)
			  + (if (.errorMsg // "") != ""
			     then "\n        error: " + ((.errorMsg) | rtrimstr("\n") | split("\n") | join("\n               "))
			     else "" end)' \
			"$all" 2>/dev/null
	fi
	[ "${total:-0}" -gt "${shown:-0}" ] 2>/dev/null \
		&& warn "    $shown of $total entries shown (stopped at CP_LOG_MAX_PAGES=$CP_LOG_MAX_PAGES)"
	rm -f "$all" "$all.total"
	return 0
}

# ---------------------------------------------------------------------------
# Validation — this is where the matrix's verdict comes from
# ---------------------------------------------------------------------------
# centipede re-reads both ends itself, over SSH, and compares SHA256 per file:
#
#   find <root> -type f -print0 | sort -z | xargs -0 sha256sum
#
# run on the source and on the node, with each side's root stripped to leave a
# relative path -> hash map (pkg/core/validation/validator.go remoteChecksums).
# A file missing in the destination, a file the source does not have, and a hash
# that differs are all reported per path.
#
# This is a stronger verdict than the object storage matrix gets. There is no
# ETag caveat to carry: the hash is computed on both machines by the same
# algorithm over the bytes themselves, so nothing about file size or how the
# transfer chunked it changes what equality means.
#
# ⚠ It does mean validation reads every file twice over SSH. 2.7 MB is nothing;
#   a much larger dataset would make this, rather than the transfer, the slow
#   half of a cell.
#
# ⚠ print0/xargs -0 is also what makes the multibyte filenames in the seed
#   meaningful: a comparison that split on whitespace would mangle them into
#   mismatches that are the validator's fault rather than the transfer's.
#
# ⚠ ON A CENTIPEDE OLDER THAN THE STAGING FIX, this reports a failure that is not
#   the target's fault. A validation that fails with a list of "file exists in
#   destination but not in source" for paths under a "relay-XXXXXXXX/" directory
#   is reading leftovers from someone else's transfer.
#
#   That was found here, by this folder's first loopback run, and fixed in
#   transx-ex: every relay now stages in a directory of its own and removes it
#   when the pipeline ends. Before the fix the filesystem relay staged through the
#   shared root and the push step sent everything in it.
#
#   Against an older build, clear the root first:
#     docker exec cm-centipede rm -rf /tmp/transxex-staging/storage/*
#   See the README's "Known issues".
cp_validate() {
	local tmp code waited=0
	CP_VALIDATION=""; CP_ERROR=""

	tmp="$(mktemp)"
	code="$(cp_curl POST "/migration/$CP_MIGRATION_ID/validation" "" "$tmp")"
	if ! cp_ok "$code"; then
		CP_ERROR="could not start validation (HTTP $code)"
		rm -f "$tmp"
		return 1
	fi

	QUIET_API_LOG=1
	while :; do
		cp_curl GET "/migration/$CP_MIGRATION_ID/validation" "" "$tmp" >/dev/null
		CP_VALIDATION="$(jq -r '.data.validationStatus // "unknown"' "$tmp" 2>/dev/null)"
		printf '\r    validation status=%-20s' "$CP_VALIDATION" >&2
		[ "$CP_VALIDATION" = "running" ] || break
		if [ "$waited" -ge "${VALIDATION_TIMEOUT:-600}" ]; then
			CP_VALIDATION="timeout"
			break
		fi
		sleep "${POLL_INTERVAL:-5}"
		waited=$((waited + ${POLL_INTERVAL:-5}))
	done
	QUIET_API_LOG=0
	printf '\n' >&2

	if [ "$CP_VALIDATION" != "passed" ]; then
		# ⚠ The key is data.details, not data.validationDetails: the GET response is
		#   model.ValidationResultResponse, whose Details field is tagged
		#   `json:"details"`.
		CP_ERROR="$(jq -r '(.data.details // [])[]? | select(.status=="failed")
		                   | "\(.itemPath): " + ((.message // "no message") | gsub("\n\\s*"; " | "))' \
			"$tmp" 2>/dev/null | head -1 | cut -c1-400)"
		[ -n "$CP_ERROR" ] || CP_ERROR="validation status $CP_VALIDATION"
	fi
	rm -f "$tmp"
	[ "$CP_VALIDATION" = "passed" ]
}

# cp_validation_summary — the validation result, for a person to read.
#
#   Every warning and every failure is printed in full; items that simply passed
#   are counted. A dataset with a mismatched file produces one line per path, and
#   which paths they are is the whole content of the finding.
cp_validation_summary() {
	local tmp shown
	tmp="$(mktemp)"
	cp_curl GET "/migration/$CP_MIGRATION_ID/validation" "" "$tmp" >/dev/null

	jq -r '"    status: \(.data.validationStatus // "?")  \(.data.validationMessage // "")"' \
		"$tmp" 2>/dev/null || true

	jq -r '(.data.details // []) | if length == 0 then empty else
		 (group_by(.status) | map("\(.[0].status)=\(length)") | "    items: " + join("  ")) end' \
		"$tmp" 2>/dev/null || true

	jq -r '(.data.details // [])[]? | select(.status != "passed")
	       | "    [\(.status)] \(.itemPath // "?")"
	         + (if (.message // "") != ""
	            then "\n        " + ((.message) | rtrimstr("\n") | split("\n") | join("\n        "))
	            else "" end)' \
		"$tmp" 2>/dev/null || true

	shown="$(jq -r '(.data.details // []) | map(select(.status == "passed")) | length' "$tmp" 2>/dev/null || echo 0)"
	[ "${shown:-0}" -gt 0 ] 2>/dev/null && info "    ($shown item(s) passed with nothing to report)"
	rm -f "$tmp"
	return 0
}

# cp_delete_migration — with KEEP_MIGRATION=0, drop the record as the cell ends.
cp_delete_migration() {
	local tmp
	[ -n "$CP_MIGRATION_ID" ] || return 0
	[ "${KEEP_MIGRATION:-1}" = "1" ] && return 0
	tmp="$(mktemp)"
	cp_curl DELETE "/migration/$CP_MIGRATION_ID" "" "$tmp" >/dev/null
	rm -f "$tmp"
}
