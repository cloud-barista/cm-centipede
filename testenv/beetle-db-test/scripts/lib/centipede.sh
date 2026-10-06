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
# ── A downgrade is refused here ─────────────────────────────────────────────
# A cell going from a higher version to a lower one is refused by
# POST /plans/target with a 400 (validateDBMSVersion in pkg/core/plan ->
# transxex.CheckVersion). So a BLOCK verdict comes from the plan step rather than
# the migration, and no migration is created at all.
#
# ⚠ The centipede API offers no way to skip that check. That is why the
#   --skip-version-check option from the days of calling transx-ex directly does
#   not exist in this folder.
#
# ── The matrix creates the target database ──────────────────────────────────
# centipede creates a target database only for providerName "onprem"
# (EnsureTargetDatabase in pkg/core/migration). A managed target is aws or ncp, so
# it does not, and that same check runs at plan time - a missing database fails
# the plan, not the migration. That is why target_db_create (cm-beetle's logical
# database API, lib/beetle.sh) runs first.
#
# ── The target password ─────────────────────────────────────────────────────
# db_password comes from lib/beetle.sh and reads .env. It is the one place the
# managed instance's master password is fetched, so putting it back behind a
# secret store later touches that function alone.
#
# ── Scope and rollback ──────────────────────────────────────────────────────
# centipede's DBMS migration is always ScopeFull and always asynchronous, which
# is why --scope and --async do not exist here either. What happens to a failed
# target is decided by dbmsOnFailure (cleanup|keep).

if [ -n "${MATRIX_CENTIPEDE_SH:-}" ]; then return 0; fi
MATRIX_CENTIPEDE_SH=1

CP_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$CP_LIB_DIR/common.sh"

CP_BASE="${CP_BASE:-http://localhost:8085/centipede}"

# What a cell reads back
CP_PLAN=""            # the plan JSON
CP_ERROR=""           # one line saying why it failed
CP_DOWNGRADE=0        # 1 when the plan refused it as a downgrade
CP_MIGRATION_ID=""
CP_STATUS=""
CP_VALIDATION=""

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
# The target connection — a beetleDb reference, never an inline db connection
# ---------------------------------------------------------------------------
# The target of this folder IS a cm-beetle managed instance, so it is named as
# one. centipede then reads host/port/adminUserName from cm-beetle itself
# (client/beetle GetRDBMSAccessInfo) rather than being told, and the same
# reference is what lets it create the target database through the instance's
# owner - see TARGET_DB_CREATE.
#
# Three consequences worth knowing:
#
#   centipede must reach cm-beetle. Until this reference was used, nothing in
#   this folder required that: the matrix talked to beetle and centipede never
#   did. centipede's own centipede.beetle.endpoint (conf/cm-centipede.yaml, or
#   CENTIPEDE_BEETLE_ENDPOINT) has to name the same beetle as BEETLE_URL here.
#   A mismatch fails at POST /plans/target - see explain_centipede_beetle.
#
#   The password is not looked up. cm-beetle takes one when the instance is
#   created and never hands it back, so it travels in the reference, where it
#   also serves as the admin password on the logical-database calls.
#
#   username is deliberately not sent. Empty means "the instance admin as
#   cm-beetle reports it", which is the same value RDBMS_USER holds, and asking
#   for it twice only creates a way for the two to disagree.
#
# tlsMode is omitted when it is disable: an empty value already means disable in
# transx-ex, and stating it would only leave the impression that TLS was handled.
cp_dst_connection() {
	local engine="$1" tls
	tls="$(target_tls_mode "$engine")"
	jq -n \
		--arg ns "$MATRIX_NS" --arg id "${ACTIVE_INSTANCE:-$RDBMS_NAME}" \
		--arg w "$(db_password "$(lower "$CSP")")" --arg tls "$tls" \
		'{source:"beetleDb", beetleDb:(
		    {nsId:$ns, rdbmsId:$id, password:$w}
		    + (if $tls == "disable" then {} else {tlsMode:$tls} end))}'
}

# ---------------------------------------------------------------------------
# Plan
# ---------------------------------------------------------------------------

# cp_plan SRC_MODEL ENGINE SRC_DB DST_DB
#   On success it fills CP_PLAN and returns 0. On failure it sets CP_ERROR and,
#   for a downgrade, CP_DOWNGRADE=1 as well, and returns 1.
#
#   dbmsFilter is stated explicitly. Left empty, every database honeybee
#   collected becomes a target, which makes a cell depend on whatever else the
#   source image happens to hold. Naming it keeps what was migrated the same on
#   every run, and allows SRC_DB and DST_DB to differ.
cp_plan() {
	local src_model="$1" engine="$2" src_db="$3" dst_db="$4" tmp code body filter
	CP_PLAN=""; CP_ERROR=""; CP_DOWNGRADE=0

	# pgSchemas is added only when the probe found schema public unusable and a
	# schema of this account's own usable instead (PG_TARGET_SCHEMA). It is
	# PostgreSQL-only - validatePgSchemas rejects it for any other engine - so the
	# engine is checked here as well as at the probe.
	local schema=""
	[ "$(lower "$engine")" = "postgresql" ] && schema="${PG_TARGET_SCHEMA:-}"

	filter="$(jq -cn --arg s "$src_db" --arg d "$dst_db" --arg sch "$schema" \
		'{databases:[{targetMapping:(
		    {srcName:$s, dstName:$d}
		    + (if $sch == "" then {}
		       else {pgSchemas:[{srcName:"public", dstName:$sch}]} end))}]}')"

	# plans is an array: one entry per (source entry, destination). This cell
	# migrates one connection to one target, so it holds a single entry, and
	# srcConnection is lifted straight out of the source model - it is the
	# connection honeybee produced that entry with, so nothing has to be threaded
	# in alongside it.
	body="$(jq -cn --argjson s "$src_model" --argjson d "$(cp_dst_connection "$engine")" \
		--argjson f "$filter" \
		'{source:$s,
		  plans:[{
		    srcConnection: $s.sourceDataMigrationModel.databases[0].connection,
		    dstConnection: $d,
		    dbmsFilter: $f
		  }]}')"

	tmp="$(mktemp)"
	code="$(cp_curl POST "/plans/target" "$body" "$tmp")"

	if cp_ok "$code"; then
		CP_PLAN="$(jq -c 'if .success then .data else empty end' "$tmp" 2>/dev/null)"
		rm -f "$tmp"
		[ -n "$CP_PLAN" ] || { CP_ERROR="the plan response was empty"; return 1; }
		return 0
	fi

	CP_ERROR="$(jq -r '.error // .message // "no response"' "$tmp" 2>/dev/null | head -1)"
	rm -f "$tmp"

	# A downgrade is an expected refusal, not a failure. It is told apart by the
	# sentence validateDBMSVersion in pkg/core/plan produces.
	case "$CP_ERROR" in
	*downgrade*|*"older than source"*) CP_DOWNGRADE=1 ;;
	esac
	return 1
}

# cp_plan_summary — one line per database the plan decided to migrate
cp_plan_summary() {
	printf '%s' "$CP_PLAN" | jq -r '
		(.targetDataMigrationModel.databases // [])[]
		| .databases[]
		| "    \(.order). \(.srcName) -> \(.dstName)   rules: \((.rules // []) | length)"' 2>/dev/null
}

# ---------------------------------------------------------------------------
# Migration
# ---------------------------------------------------------------------------

# cp_migrate NAME — turn CP_PLAN into a run (started asynchronously on creation).
#   Fills CP_MIGRATION_ID on success.
cp_migrate() {
	local name="$1" tmp code body
	CP_MIGRATION_ID=""; CP_ERROR=""

	body="$(jq -cn --arg n "$name" \
		--arg d "managed-DB version matrix cell" \
		--arg f "${DBMS_ON_FAILURE:-cleanup}" \
		--argjson p "$CP_PLAN" \
		'{name:$n, description:$d, plan:$p, dbmsOnFailure:$f}')"

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

	# The failed items' reasons are gathered into CP_ERROR - they become the cell's
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
# ⚠ This endpoint is paginated: ListMigrationLogs reads page and pageSize
#   through ParsePageParams, which defaults to 20 and caps at 100. Asking for no
#   page at all therefore returns the first 20 entries and says nothing about the
#   rest - it reports the real count in .data.total, and that is the only sign
#   anything was left out. Everything below walks to the end of the pages rather
#   than reading one.

CP_LOG_PAGE_SIZE="${CP_LOG_PAGE_SIZE:-100}"   # the handler's maximum
CP_LOG_MAX_PAGES="${CP_LOG_MAX_PAGES:-50}"    # a stop, so a bad total cannot spin

# _cp_log_fetch OUT [STATUS] — collect every log entry into OUT as a JSON array.
#   Prints nothing. Returns 1 when the first page could not be read.
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
#
#   Printed as its own step after validation rather than folded into the poll
#   output: the poll line is a progress counter that is overwritten in place,
#   and by the time validation has run the log also carries what the rollback
#   did, which is the part worth reading when a cell failed.
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
		# Counts first: on a wide cell the per-item lines run long, and how many
		# items succeeded is the question actually being asked.
		jq -r 'group_by(.status) | map("\(.[0].status)=\(length)") | "    items: " + join("  ")' \
			"$all" 2>/dev/null
		jq -r '.[]?
			| "    [\(.status // "?")] \(.itemPath // "?")  \(.durationMs // 0)ms"
			  + (if (.sizeBytes // 0) > 0 then "  \(.sizeBytes)B" else "" end)
			  + (if (.targetCreated // false) then "  (target db created by centipede)" else "" end)
			  + (if (.failedObjectKind // "") != ""
			     then "\n        failed object: \(.failedObjectKind) \(.failedObjectName // "")" else "" end)
			  + (if (.errorMsg // "") != ""
			     then "\n        error: " + ((.errorMsg) | rtrimstr("\n") | split("\n") | join("\n               "))
			     else "" end)
			  + (if (.rollbackStatus // "") != ""
			     then "\n        rollback: \(.rollbackStatus)"
			          + (if (.rollbackErrorMsg // "") != "" then " - \((.rollbackErrorMsg)[0:200])" else "" end)
			     else "" end)' \
			"$all" 2>/dev/null
	fi
	# Said out loud rather than left to be noticed: the walk stops at
	# CP_LOG_MAX_PAGES, and a cell wide enough to hit that would otherwise look
	# like it had simply finished.
	[ "${total:-0}" -gt "${shown:-0}" ] 2>/dev/null \
		&& warn "    $shown of $total entries shown (stopped at CP_LOG_MAX_PAGES=$CP_LOG_MAX_PAGES)"
	rm -f "$all" "$all.total"
	return 0
}

# ---------------------------------------------------------------------------
# Validation — this is where the matrix's verdict comes from
# ---------------------------------------------------------------------------
# centipede's validation inspects the source and the target itself: exact row
# counts per table or collection, twelve kinds of schema object (views,
# functions, procedures, triggers, events, foreign keys, sequences, types,
# extensions and so on), and character sets. A charset difference is reported as
# a warning only, because a managed target following its own defaults is normal -
# the same judgement this matrix makes.
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
		# The failed item's message becomes the reason. Failing that, the status.
		#
		# ⚠ The key is data.details, not data.validationDetails: the GET response is
		#   model.ValidationResultResponse, whose Details field is tagged
		#   `json:"details"`. Reading the wrong name silently produced an empty list,
		#   so a failed validation reported only "validation status failed" and every
		#   warning went unprinted.
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
#   Every warning and every failure is printed in full. A warning is centipede
#   saying the two sides differ in a way that is not on its own evidence of a bad
#   migration - a managed target imposing its own character set is the usual one -
#   and "passed with 2 warning(s)" without the two sentences leaves the reader
#   unable to judge that for themselves.
#
#   Items that simply passed are counted, not listed: a wide database produces one
#   per table and column, which would bury the two lines worth reading.
cp_validation_summary() {
	local tmp shown
	tmp="$(mktemp)"
	cp_curl GET "/migration/$CP_MIGRATION_ID/validation" "" "$tmp" >/dev/null

	jq -r '"    status: \(.data.validationStatus // "?")  \(.data.validationMessage // "")"' \
		"$tmp" 2>/dev/null || true

	# Counts by status, so what is listed below can be read against the whole.
	jq -r '(.data.details // []) | if length == 0 then empty else
		 (group_by(.status) | map("\(.[0].status)=\(length)") | "    items: " + join("  ")) end' \
		"$tmp" 2>/dev/null || true

	# The lines worth reading: anything that is not a plain pass, message included.
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
