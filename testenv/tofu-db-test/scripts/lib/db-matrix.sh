#!/usr/bin/env bash
#
# lib/db-matrix.sh — the CSP-agnostic matrix runner
#
# The entry point (scripts/<csp>-db-matrix.sh) sets the CSP and title, then calls
# matrix_main.
#
#   engine
#     └ one target version ─── tofu creates the managed instance (one workspace, 5-30 min)
#          └ source versions ── cell: source container -> target DB -> honeybee -> centipede -> validation
#        ─── tofu destroy
#
# The number of instances equals the number of target versions, not the number of
# cells. The source is not an official image but one this folder builds with
# systemd in it, so it is built once per version and cached (lib/source.sh).
#
# ── The migration and the verdict belong to someone else ────────────────────
# This matrix does not call transx-ex. cm-honeybee collects the source and
# cm-centipede plans, migrates and validates; this file calls the two and turns
# the answers into a table. So a verdict rests on centipede's validationStatus
# rather than on a snapshot string comparison.
#
# Verdicts
#   PASS  — migration completed and validation passed
#   BLOCK — a higher-to-lower pair that POST /plans/target refused with a 400 (expected)
#   FAIL  — anything else
#   SKIP  — filtered out by ONLY_CELLS/ONLY_ENGINES, or the column could not be
#           prepared (instance failed, endpoint unreachable, creation probe failed)

if [ -n "${MATRIX_DB_MATRIX_SH:-}" ]; then return 0; fi
MATRIX_DB_MATRIX_SH=1

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$LIB_DIR/common.sh"
# shellcheck source=./tofu.sh
. "$LIB_DIR/tofu.sh"
# shellcheck source=./source.sh
. "$LIB_DIR/source.sh"
# shellcheck source=./honeybee.sh
. "$LIB_DIR/honeybee.sh"
# shellcheck source=./centipede.sh
. "$LIB_DIR/centipede.sh"

MATRIX_DIR="$(cd "$LIB_DIR/../.." && pwd)"

# Updated per column - carried into the result JSON as is.
COLUMN_SECURE_TRANSPORT=""
COLUMN_GRANT_PUBLIC=""
# The schema PostgreSQL cells were restored into. Empty means public, the normal
# case; a name means public was unusable and the probe found this one workable, so
# a PASS in that column is a PASS into this schema.
COLUMN_PG_SCHEMA=""

declare -A CELL_STATUS CELL_ELAPSED CELL_DETAIL CELL_SRCVER CELL_MIGID CELL_VALID
CREATED_INSTANCES=()
ACTIVE_TARGET_DB=""
ACTIVE_INSTANCE=""
ACTIVE_ENGINE=""
MATRIX_FAILED=0
COLUMN_ERRORS=0
NETWORK_READY=0

# honeybee's SourceGroup id — obtained once per run.
HB_SG_ID=""

# ---------------------------------------------------------------------------
# Reading the settings
# ---------------------------------------------------------------------------
engines_of()      { printf '%s' "${ONLY_ENGINES:-$(csp_env "$CSP" ENGINES)}"; }
src_versions_of() { csp_env "$CSP" "$(upper "$1")_SRC_VERSIONS"; }
dst_versions_of() { csp_env "$CSP" "$(upper "$1")_DST_VERSIONS"; }

usage() {
	cat <<USAGE
$CSP_TITLE managed-RDBMS version matrix — usage

  $(basename "$0") [options]

Options:
  --engines "E1 E2"            engines to run (default: $(upper "$CSP")_ENGINES)
  --src-versions "V1 V2"       source versions — only with a single engine
  --dst-versions "V1 V2"       target versions — only with a single engine
  --only "SRC:DST ..."         run only these cells ("engine:SRC:DST" also works)
  --mode direct|ssh            how honeybee reaches the source (default: $MODE)
  --keep-instance              keep the managed instances at the end (they keep billing)
  --keep-on-fail               keep a failed cell's source container
  --stop-on-fail               stop at the first FAIL
  --tls-mode MODE              transport security for the target (default: $TARGET_TLS_MODE)
                               disable|prefer|require|verify-ca|verify-full
  --secure-transport MODE      csp-default|off (default: $AWS_TARGET_SECURE_TRANSPORT, AWS only)
  --cleanup                    do not run the matrix; reclaim everything left in tofu state
  -h, --help                   this help

⚠ Requires cm-honeybee and cm-centipede to be running. Checked in step 1.

Options that do not exist, and why:
  --scope                centipede's DBMS migration is always full scope.
  --async                centipede is always asynchronous; polling is the only way.
  --skip-version-check   the centipede API has no way to skip the version check.

One managed instance is created per target version, and creating one takes 5-30
minutes. If an interrupted run left resources behind, --cleanup reclaims them
from tofu state.
USAGE
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--engines)      ONLY_ENGINES="$2"; shift 2 ;;
		--src-versions) CLI_SRC_VERSIONS="$2"; shift 2 ;;
		--dst-versions) CLI_DST_VERSIONS="$2"; shift 2 ;;
		--only)         ONLY_CELLS="$2"; shift 2 ;;
		--mode)         MODE="$2"; shift 2 ;;
		--keep-instance) KEEP_INSTANCE=1; shift ;;
		--keep-on-fail) KEEP_ON_FAIL=1; shift ;;
		--stop-on-fail) STOP_ON_FAIL=1; shift ;;
		--tls-mode)     TARGET_TLS_MODE="$2"; shift 2 ;;
		--secure-transport) AWS_TARGET_SECURE_TRANSPORT="$2"; shift 2 ;;
		--cleanup)      CLEANUP_ONLY=1; shift ;;
		-h|--help)      usage; exit 0 ;;
		--scope|--async|--skip-version-check)
			die "$1 does not exist in this folder — the centipede API has no counterpart. See --help." ;;
		*) echo "unknown option: $1" >&2; usage; exit 1 ;;
		esac
	done
}

# assert_tls_mode ENGINES — check the target TLS mode is usable before anything is created.
assert_tls_mode() {
	local engines="$1" mode m engine known=0
	mode="$(target_tls_mode)"

	for m in $TLS_MODES; do
		[ "$m" = "$mode" ] && { known=1; break; }
	done
	if [ "$known" -ne 1 ]; then
		fail "unknown TARGET_TLS_MODE value: '$TARGET_TLS_MODE'"
		fail "  Allowed values: $TLS_MODES"
		return 1
	fi

	# MongoDB has no prefer: a client either negotiates TLS or it does not, with
	# nowhere to fall back to, and transx-ex's ValidateDBMS rejects it for the same
	# reason. It does not stop the run: when one CSP is run with several engines,
	# standing the other columns still over one mongodb costs too much. The mongodb
	# cells alone drop, and the result records which mode they ran at.
	for engine in $engines; do
		tls_mode_downgraded "$engine" || continue
		warn "$engine cannot use TLS mode $mode, so it runs at $(target_tls_mode "$engine")."
		warn "  To check it over TLS, run with --tls-mode require (it applies to every engine)."
	done

	case "$mode" in
	verify-ca|verify-full)
		warn "TLS mode $mode uses the system trust store only."
		warn "  RDS and NCP sign with their own CAs, so verification may fail." ;;
	esac
	return 0
}

# cell_selected ENGINE SVER DVER — the ONLY_CELLS filter
cell_selected() {
	[ -z "$ONLY_CELLS" ] && return 0
	grep -qw -- "$1:$2:$3" <<<"$ONLY_CELLS" && return 0
	grep -qw -- "$2:$3"    <<<"$ONLY_CELLS" && return 0
	return 1
}

# ---------------------------------------------------------------------------
# Cells
# ---------------------------------------------------------------------------
finish_cell() {
	CELL_STATUS["$1"]="$2"; CELL_ELAPSED["$1"]="$3"; CELL_DETAIL["$1"]="$4"
	case "$2" in
	PASS)  echo -e "  ${C_OK}▣ $1 : PASS${C_OFF}  ($(secs_fmt "$3"))" ;;
	BLOCK) echo -e "  ${C_WARN}▣ $1 : BLOCK${C_OFF} ($(secs_fmt "$3")) — $4" ;;
	SKIP)  echo -e "  ▣ $1 : SKIP — $4" ;;
	*)     echo -e "  ${C_ERR}▣ $1 : FAIL${C_OFF}  ($(secs_fmt "$3")) — $4" ;;
	esac
	pause
}

# cleanup_cell ENGINE RC — tidy up after a cell. The target database always goes.
cleanup_cell() {
	local engine="$1" rc="$2"
	if [ -n "$ACTIVE_TARGET_DB" ]; then
		target_db_drop "$CSP" "$engine" "$ACTIVE_TARGET_DB" >/dev/null 2>&1
		ACTIVE_TARGET_DB=""
	fi
	cp_delete_migration
	if [ "$rc" -ne 0 ] && [ "$KEEP_ON_FAIL" = "1" ]; then
		warn "KEEP_ON_FAIL=1 — keeping the source container ($(src_container "$engine"))"
		KEEP_CONTAINERS=1
		return 0
	fi
	src_stop "$engine"
}

# run_cell ENGINE SVER DVER
run_cell() {
	local engine="$1" sver="$2" dver="$3"
	local key="$engine:$sver:$dver"
	local started ended elapsed img conn_id db_json src_model mig_name expect_block=0
	version_gt "$sver" "$dver" && expect_block=1

	# Clear what the previous cell left. If this cell ends at the plan step,
	# cp_migrate is never called, and cleanup_cell would then delete the previous
	# cell's migration record.
	CP_MIGRATION_ID=""; CP_STATUS=""; CP_VALIDATION=""; CP_ERROR=""

	banner "cell [$engine] $sver -> $dver   (source $MODE -> target direct, TLS=$(target_tls_mode "$engine"))"
	started="$(date +%s)"

	# ── 1) Source ───────────────────────────────────────────────────────────
	sub "1) source container — image, start, wait for initialization"
	img="$(src_image "$engine" "$sver")" \
		|| { finish_cell "$key" FAIL 0 "source image build failed ($engine:$sver)"; return; }
	src_start "$engine" "$sver" "$img" "$MODE" \
		|| { finish_cell "$key" FAIL 0 "source container failed to start"; cleanup_cell "$engine" 1; return; }
	src_wait_ready "$engine" \
		|| { finish_cell "$key" FAIL 0 "source initialization failed (matrix-init.service)"; cleanup_cell "$engine" 1; return; }
	if [ "$MODE" = "ssh" ]; then
		src_inject_ssh_key "$engine" \
			|| { finish_cell "$key" FAIL 0 "source SSH setup failed"; cleanup_cell "$engine" 1; return; }
	fi
	CELL_SRCVER["$key"]="$(src_server_version "$engine")"
	info "source server version: ${CELL_SRCVER[$key]:-(unreadable)}   (asked for $sver)"

	# ── 2) Target database ──────────────────────────────────────────────────
	# centipede creates a target database only for providerName "onprem". A managed
	# target is aws or ncp, so it does not - and that check also runs at plan time,
	# which is why this step comes first.
	sub "2) creating the target database — $ACTIVE_INSTANCE"
	target_db_create "$CSP" "$engine" "$DST_DB" \
		|| { finish_cell "$key" FAIL 0 "target database creation failed ($DST_DB)"; cleanup_cell "$engine" 1; return; }
	ACTIVE_TARGET_DB="$DST_DB"

	# ── 3) honeybee ─────────────────────────────────────────────────────────
	sub "3) collecting the source — cm-honeybee"
	conn_id="$(hb_connection "$HB_SG_ID" "$engine")" \
		|| { finish_cell "$key" FAIL 0 "honeybee connection could not be obtained"; cleanup_cell "$engine" 1; return; }
	hb_import "$HB_SG_ID" "$conn_id" \
		|| { finish_cell "$key" FAIL 0 "honeybee source collection failed"; cleanup_cell "$engine" 1; return; }
	db_json="$(hb_databases "$HB_SG_ID" "$conn_id")" \
		|| { finish_cell "$key" FAIL 0 "honeybee collection result could not be read"; cleanup_cell "$engine" 1; return; }

	local db_count
	db_count="$(printf '%s' "$db_json" | jq -r '(.databases // []) | length' 2>/dev/null)"
	if [ "${db_count:-0}" -eq 0 ]; then
		finish_cell "$key" FAIL 0 "no databases collected (check the source account and address)"
		cleanup_cell "$engine" 1; return
	fi
	printf '%s' "$db_json" | jq -r '(.databases // [])[] | "    - \(.database)  (tables: \(.tables // [] | length))"'
	src_model="$(hb_source_model "$conn_id" "$db_json")"

	# ── 4) Plan ─────────────────────────────────────────────────────────────
	sub "4) target plan — POST /plans/target"
	if ! cp_plan "$src_model" "$engine" "$SRC_DB" "$DST_DB"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		if [ "$CP_DOWNGRADE" = "1" ] && [ "$expect_block" = "1" ]; then
			# Higher-to-lower is an expected refusal; no migration was even created.
			# It is not a failure, so cleanup runs with rc=0: there is no reason to
			# keep the source container even with KEEP_ON_FAIL on, and one left
			# behind would block the next cell's port.
			finish_cell "$key" BLOCK "$elapsed" "plan refused (downgrade) - $CP_ERROR"
			cleanup_cell "$engine" 0
		else
			finish_cell "$key" FAIL "$elapsed" "plan failed - $CP_ERROR"
			explain_insecure_transport "$CP_ERROR" "$CSP"
			explain_pg_maintenance_db "$CP_ERROR"
			cleanup_cell "$engine" 1
		fi
		return
	fi
	cp_plan_summary

	# ── 5) Migration ────────────────────────────────────────────────────────
	mig_name="${MIGRATION_PREFIX:-cptfm}-$engine-${sver//./_}-${dver//./_}-$(date '+%Y%m%d-%H%M%S')"
	sub "5) migration — POST /migration ($mig_name)"
	if ! cp_migrate "$mig_name"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "migration could not be created - $CP_ERROR"
		cleanup_cell "$engine" 1
		return
	fi
	CELL_MIGID["$key"]="$CP_MIGRATION_ID"
	info "MIGRATION_ID=$CP_MIGRATION_ID   dbmsOnFailure=${DBMS_ON_FAILURE:-cleanup}"

	if ! cp_poll; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		# The migration never finished, so validation is not reached and step 7
		# never runs - the log is read here instead, where it says what broke.
		sub "6) migration log — GET /migration/$CP_MIGRATION_ID/logs"
		cp_migration_logs
		finish_cell "$key" FAIL "$elapsed" "[$CP_STATUS] $CP_ERROR"
		explain_insecure_transport "$CP_ERROR" "$CSP"
		explain_pg_maintenance_db "$CP_ERROR"
		cleanup_cell "$engine" 1
		return
	fi

	# ── 6) Validation ───────────────────────────────────────────────────────
	sub "6) validation — POST /migration/$CP_MIGRATION_ID/validation"
	cp_validate || true
	CELL_VALID["$key"]="$CP_VALIDATION"
	cp_validation_summary

	# ── 7) Migration log ────────────────────────────────────────────────────
	# Read after validation rather than before it: by now the log also carries
	# what any rollback did, and the verdict above is easier to read against the
	# per-item detail than the other way round.
	sub "7) migration log — GET /migration/$CP_MIGRATION_ID/logs"
	cp_migration_logs

	ended="$(date +%s)"; elapsed=$((ended - started))
	if [ "$CP_VALIDATION" = "passed" ]; then
		finish_cell "$key" PASS "$elapsed" "validation passed (source ${CELL_SRCVER[$key]:-?})"
		cleanup_cell "$engine" 0
	else
		finish_cell "$key" FAIL "$elapsed" "validation $CP_VALIDATION - $CP_ERROR"
		cleanup_cell "$engine" 1
	fi
}

# ---------------------------------------------------------------------------
# Running one column (one target version)
# ---------------------------------------------------------------------------
mark_column_skipped() {
	local engine="$1" dver="$2" reason="$3" sver key
	for sver in $(src_versions_of "$engine"); do
		key="$engine:$sver:$dver"
		[ -n "${CELL_STATUS[$key]:-}" ] && continue
		CELL_STATUS["$key"]="SKIP"; CELL_ELAPSED["$key"]=0; CELL_DETAIL["$key"]="$reason"
	done
}

# wait_target_reachable — wait until the endpoint accepts a TCP connection.
wait_target_reachable() {
	local waited=0 timeout="${TARGET_REACH_TIMEOUT:-180}" waiting=0
	if [ -z "$RDBMS_HOST" ] || [ -z "$RDBMS_PORT" ]; then
		fail "the target address is unknown (host='${RDBMS_HOST}' port='${RDBMS_PORT}')."
		return 1
	fi
	while [ "$waited" -lt "$timeout" ]; do
		if (echo > "/dev/tcp/$RDBMS_HOST/$RDBMS_PORT") >/dev/null 2>&1; then
			[ "$waiting" -eq 1 ] && printf '\n' >&2
			ok "target reachable: $RDBMS_HOST:$RDBMS_PORT"
			return 0
		fi
		waiting=1
		printf '\r    waiting for the target — %s:%s (%s)' "$RDBMS_HOST" "$RDBMS_PORT" "$(secs_fmt "$waited")" >&2
		sleep 5
		waited=$((waited + 5))
	done
	printf '\n' >&2
	fail "cannot reach the target endpoint: $RDBMS_HOST:$RDBMS_PORT (${timeout}s)"
	fail "  Check public access (public_access=$RDBMS_PUBLIC) and the inbound rule ($MATRIX_ALLOWED_CIDR)."
	if [ "$(lower "$CSP")" = "ncp" ]; then
		fail "  On NCP, check the public domain has been issued too (private domain: ${RDBMS_PRIVATE_DOMAIN:-none})."
		# Which group the rule landed on is worth stating: a rule attached to an ACG
		# that governs nothing applies cleanly and leaves the port shut.
		fail "  Inbound rule attached to ACG ${RDBMS_ACG_NO:-(unknown)}; the instance's ACGs: ${RDBMS_ACG_LIST:-(unknown)}"
	fi
	return 1
}

run_column() {
	local engine="$1" dver="$2" sver key

	# No instance is created for a column with no cell to run.
	local any=0
	for sver in $(src_versions_of "$engine"); do
		if cell_selected "$engine" "$sver" "$dver"; then any=1; break; fi
	done
	if [ "$any" -eq 0 ]; then
		mark_column_skipped "$engine" "$dver" "excluded by filter"
		info "[$engine $dver] no cell to run, so no instance is created."
		return 0
	fi

	banner "[$engine] target version $dver — preparing the managed instance"
	if ! create_rdbms "$CSP" "$engine" "$dver"; then
		mark_column_skipped "$engine" "$dver" "target instance could not be prepared"
		if [ -n "$RDBMS_NAME" ]; then
			CREATED_INSTANCES+=("$RDBMS_NAME")
			ACTIVE_INSTANCE="$RDBMS_NAME"
		fi
		return 1
	fi
	CREATED_INSTANCES+=("$RDBMS_NAME")
	ACTIVE_INSTANCE="$RDBMS_NAME"
	ACTIVE_ENGINE="$engine"

	# Per-CSP hook — NCP waits here for the public-domain console step.
	if declare -F csp_after_instance_ready >/dev/null; then
		if ! csp_after_instance_ready "$engine" "$dver"; then
			mark_column_skipped "$engine" "$dver" "target access could not be prepared"
			return 1
		fi
	fi

	if ! wait_target_reachable; then
		mark_column_skipped "$engine" "$dver" "target endpoint unreachable"
		return 1
	fi

	COLUMN_SECURE_TRANSPORT="${RDBMS_SECURE_TRANSPORT:-}"
	COLUMN_GRANT_PUBLIC=""
	COLUMN_PG_SCHEMA=""

	# PostgreSQL is asked first whether a GRANT is needed. This is value gathering,
	# not a gate: whether this column can run is decided by the probe below, which
	# actually creates something.
	if [ "$(lower "$engine")" = "postgresql" ]; then
		pg_privilege_preflight "$CSP" || true
		COLUMN_GRANT_PUBLIC="$PG_GRANT_EFFECTIVE"
	fi

	# Create a target database once and drop it. Folding the whole column and
	# recording the sentence the CSP actually produced reads better than every cell
	# dying of the same error one at a time.
	if ! target_db_probe "$CSP" "$engine"; then
		mark_column_skipped "$engine" "$dver" "${TARGET_PROBE_REASON:-target database creation probe failed}"
		return 1
	fi
	# The probe, not the privilege query, decides which schema the cells write into,
	# so it is read after the probe has run.
	COLUMN_PG_SCHEMA="${PG_TARGET_SCHEMA:-}"

	for sver in $(src_versions_of "$engine"); do
		key="$engine:$sver:$dver"
		if ! cell_selected "$engine" "$sver" "$dver"; then
			CELL_STATUS["$key"]="SKIP"; CELL_ELAPSED["$key"]=0; CELL_DETAIL["$key"]="excluded by filter"
			continue
		fi
		run_cell "$engine" "$sver" "$dver"
		if [ "$STOP_ON_FAIL" = "1" ] && [ "${CELL_STATUS[$key]}" = "FAIL" ]; then
			warn "STOP_ON_FAIL=1 — stopping at the first failure."
			return 2
		fi
	done
	return 0
}

# release_column NAME — destroy the instance this column used.
release_column() {
	local name="$1" i
	[ -n "$name" ] || return 0
	if [ "${KEEP_INSTANCE:-0}" = "1" ]; then
		warn "KEEP_INSTANCE=1 — keeping the instance: $name  (it keeps billing)"
		warn "  $(rdbms_csp_label)"
		warn "  Reclaim it later: ./scripts/$(lower "$CSP")-db-matrix.sh --cleanup"
		ACTIVE_INSTANCE=""
		return 0
	fi
	delete_rdbms "$CSP" "$name"
	for i in "${!CREATED_INSTANCES[@]}"; do
		[ "${CREATED_INSTANCES[$i]}" = "$name" ] && unset 'CREATED_INSTANCES[i]'
	done
	CREATED_INSTANCES=("${CREATED_INSTANCES[@]}")
	ACTIVE_INSTANCE=""
}

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------
cell_text() {
	local st="${CELL_STATUS[$1]:-SKIP}"
	case "$st" in
	PASS|FAIL|BLOCK) printf '%-5s %s' "$st" "$(secs_fmt "${CELL_ELAPSED[$1]:-0}")" ;;
	*)               printf 'SKIP' ;;
	esac
}
cell_color() {
	case "${CELL_STATUS[$1]:-SKIP}" in
	PASS)  printf '%b' "$C_OK" ;;
	BLOCK) printf '%b' "$C_WARN" ;;
	FAIL)  printf '%b' "$C_ERR" ;;
	*)     printf '' ;;
	esac
}

print_matrix() {
	local engine sver dver key line w=11
	local pass=0 blocked=0 failed=0 skipped=0

	local cond="  tls=$(target_tls_mode)"
	[ "$(lower "$CSP")" = "aws" ] && cond="$cond  secure_transport=$AWS_TARGET_SECURE_TRANSPORT"
	[ -n "$COLUMN_GRANT_PUBLIC" ] && cond="$cond  publicGrant=$COLUMN_GRANT_PUBLIC"
	[ -n "$COLUMN_PG_SCHEMA" ] && cond="$cond  pgTargetSchema=$COLUMN_PG_SCHEMA"
	banner "$CSP_TITLE version matrix result — source $MODE -> target direct  (honeybee + centipede)$cond"

	for engine in $(engines_of); do
		sub "$engine  (rows = source version, columns = target managed version)$(tls_mode_downgraded "$engine" && printf '  ⚠ TLS=%s' "$(target_tls_mode "$engine")")"
		line="  src \\ dst   │"
		for dver in $(dst_versions_of "$engine"); do line+="$(printf ' %-*s' "$w" "$dver")"; done
		echo -e "${C_HDR}$line${C_OFF}"
		line="  ────────────┼"
		for dver in $(dst_versions_of "$engine"); do line+="$(printf -- '-%.0s' $(seq 1 $((w + 1))))"; done
		echo "$line"
		for sver in $(src_versions_of "$engine"); do
			printf '  %-11s │' "$sver"
			for dver in $(dst_versions_of "$engine"); do
				key="$engine:$sver:$dver"
				printf ' %b%-*s%b' "$(cell_color "$key")" "$w" "$(cell_text "$key")" "$C_OFF"
			done
			echo
		done
	done

	for engine in $(engines_of); do
		for sver in $(src_versions_of "$engine"); do
			for dver in $(dst_versions_of "$engine"); do
				case "${CELL_STATUS[$engine:$sver:$dver]:-SKIP}" in
				PASS)  pass=$((pass + 1)) ;;
				BLOCK) blocked=$((blocked + 1)) ;;
				FAIL)  failed=$((failed + 1)) ;;
				*)     skipped=$((skipped + 1)) ;;
				esac
			done
		done
	done

	echo
	echo -e "  ${C_OK}PASS $pass${C_OFF} / ${C_WARN}BLOCKED(expected) $blocked${C_OFF} / ${C_ERR}FAIL $failed${C_OFF} / SKIP $skipped" \
		"  $((pass + blocked + failed + skipped)) cells   elapsed $(secs_fmt "$(( $(date +%s) - RUN_STARTED ))")"
	if [ "$COLUMN_ERRORS" -gt 0 ]; then
		fail "  $COLUMN_ERRORS column(s) had no target instance — their cells are left SKIP."
	fi

	sub "cell detail"
	for engine in $(engines_of); do
		for sver in $(src_versions_of "$engine"); do
			for dver in $(dst_versions_of "$engine"); do
				key="$engine:$sver:$dver"
				[ -n "${CELL_STATUS[$key]:-}" ] || continue
				printf '    %-8s %-8s → %-8s %-5s  %s\n' "$engine" "$sver" "$dver" \
					"${CELL_STATUS[$key]}" "${CELL_DETAIL[$key]:-}"
			done
		done
	done

	MATRIX_FAILED="$failed"
}

write_result_json() {
	local engine sver dver key engines="[]" cells
	for engine in $(engines_of); do
		cells="[]"
		for sver in $(src_versions_of "$engine"); do
			for dver in $(dst_versions_of "$engine"); do
				key="$engine:$sver:$dver"
				cells="$(jq -c \
					--arg s "$sver" --arg d "$dver" \
					--arg st "${CELL_STATUS[$key]:-SKIP}" \
					--arg de "${CELL_DETAIL[$key]:-}" \
					--arg sv "${CELL_SRCVER[$key]:-}" \
					--arg mid "${CELL_MIGID[$key]:-}" \
					--arg val "${CELL_VALID[$key]:-}" \
					--argjson el "${CELL_ELAPSED[$key]:-0}" \
					'. + [{srcVersion:$s, dstVersion:$d, status:$st, elapsedSec:$el,
					       srcServerVersion:$sv, migrationId:$mid,
					       validationStatus:$val, detail:$de}]' <<<"$cells")"
			done
		done
		# tlsMode is recorded per engine. MongoDB cannot use prefer and drops to
		# disable, so a run-wide targetTLSMode alone would make that column's PASS
		# read as something it is not.
		engines="$(jq -c --arg e "$engine" --argjson c "$cells" \
			--arg tls "$(target_tls_mode "$engine")" \
			'. + [{engine:$e, tlsMode:$tls, cells:$c}]' <<<"$engines")"
	done
	# The conditions the result was obtained under are recorded with it. A PASS won
	# by attaching a parameter group that allows plaintext is not the same statement
	# as a PASS against untouched CSP defaults.
	jq -n --arg csp "$CSP" --arg prefix "$MATRIX_NAME_PREFIX" --arg sm "$MODE" \
		--arg ts "$(date '+%F %T')" --argjson e "$engines" \
		--arg st "$AWS_TARGET_SECURE_TRANSPORT" --arg gp "$COLUMN_GRANT_PUBLIC" \
		--arg pgs "$COLUMN_PG_SCHEMA" \
		--arg tls "$(target_tls_mode)" --arg onfail "${DBMS_ON_FAILURE:-cleanup}" \
		'{csp:$csp, namePrefix:$prefix, provisioner:"opentofu", migrator:"cm-centipede",
		  collector:"cm-honeybee", srcMode:$sm, dstMode:"direct",
		  targetTLSMode:$tls, dbmsOnFailure:$onfail,
		  secureTransport:(if $csp == "aws" then $st else "csp-default" end),
		  grantedPublicSchema:(if $gp == "" then null else ($gp == "true") end),
		  pgTargetSchema:(if $pgs == "" then null else $pgs end),
		  finishedAt:$ts, engines:$e}' \
		> "$RESULT_FILE"
	info "result JSON: $RESULT_FILE"
}

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
# cleanup_exit — however the run ends, no managed resource is left behind. An
#   instance surviving a Ctrl-C keeps billing. Only KEEP_INSTANCE=1 keeps one.
cleanup_exit() {
	local engine name
	trap - EXIT INT TERM

	if [ -n "$ACTIVE_TARGET_DB" ] && [ -n "$ACTIVE_INSTANCE" ]; then
		target_db_drop "$CSP" "$ACTIVE_ENGINE" "$ACTIVE_TARGET_DB" >/dev/null 2>&1
	fi
	if [ "${KEEP_CONTAINERS:-0}" != "1" ]; then
		for engine in $(engines_of); do src_stop "$engine"; done
	fi

	if [ "${#CREATED_INSTANCES[@]}" -gt 0 ]; then
		if [ "${KEEP_INSTANCE:-0}" = "1" ]; then
			warn "KEEP_INSTANCE=1 — keeping the instances (they keep billing). Columns left:"
			for name in "${CREATED_INSTANCES[@]}"; do
				warn "  workspace $name"
			done
			warn "  Reclaim: ./scripts/$(lower "$CSP")-db-matrix.sh --cleanup"
		else
			sub "removing the managed instances left"
			for name in "${CREATED_INSTANCES[@]}"; do delete_rdbms "$CSP" "$name"; done
			CREATED_INSTANCES=()
		fi
	fi

	if [ "$NETWORK_READY" = "1" ] && [ "${KEEP_INSTANCE:-0}" != "1" ] && [ "${KEEP_NETWORK:-0}" != "1" ]; then
		sub "removing the network"
		release_network "$CSP"
	fi

	rm -rf "${MATRIX_TMP:-/nonexistent}"
	exec 1>&- 2>&-
	wait "${LOG_TEE_PID:-}" 2>/dev/null
}

# ---------------------------------------------------------------------------
# The body
# ---------------------------------------------------------------------------
matrix_main() {
	matrix_defaults
	CLI_SRC_VERSIONS=""; CLI_DST_VERSIONS=""
	parse_args "$@"

	# Logs — the two .log files accumulate across runs, each run opening with a
	# separator (see log_run_header). The result JSON is not a log but a single
	# document, so it is replaced; the run that produced it is in the run log.
	LOG_DIR="${LOG_DIR:-$MATRIX_DIR/logs}"
	LOG_NAME="$(lower "$CSP")-db-matrix"
	LOG_FILE="${LOG_FILE:-$LOG_DIR/$LOG_NAME.log}"
	API_LOG_FILE="${API_LOG_FILE:-$LOG_DIR/$LOG_NAME-api.log}"
	RESULT_FILE="${RESULT_FILE:-$LOG_DIR/$LOG_NAME-result.json}"

	# Created outside the NO_LOG check, because NO_LOG only silences the two .log
	# files - write_result_json still writes the result JSON in here. Creating it
	# only when logging is on would leave that write to fail at the very end of a
	# run, and these scripts run under `set -uo pipefail` without -e, so a failed
	# redirect does not stop anything: the run would print "result JSON: <path>"
	# and exit 0 having written nothing.
	mkdir -p "$LOG_DIR" || { echo "ERROR: could not create the log directory: $LOG_DIR" >&2; exit 1; }

	if [ "${NO_LOG:-0}" != "1" ]; then
		# The separator goes in before the tee is attached, so it lands in the file
		# without also being echoed to the terminal.
		log_run_header "$LOG_FILE" "$LOG_NAME" "$@"
		exec > >(tee >(sed -u -r 's/\x1b\[[0-9;]*[mK]//g' >> "$LOG_FILE")) 2>&1
		LOG_TEE_PID=$!
		api_log_init "$LOG_NAME" "$@"
		echo "run log     : $LOG_FILE  (appended)"
		echo "API call log: $API_LOG_FILE  (appended)"
		echo "result JSON : $RESULT_FILE  (replaced)"
	else
		API_LOG_FILE=""
	fi

	MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cpmatrix.XXXXXX")"
	RUN_STARTED="$(date +%s)"
	KEEP_CONTAINERS=0
	trap 'cleanup_exit' EXIT INT TERM

	require_cmd docker jq curl
	docker info >/dev/null 2>&1 || die "the docker daemon is not running."

	# --cleanup — do not run the matrix; only reclaim what is left.
	if [ "${CLEANUP_ONLY:-0}" = "1" ]; then
		banner "$CSP_TITLE — reclaiming what is left in tofu state"
		tofu_preflight
		cleanup_all_workspaces "$CSP"
		trap - EXIT INT TERM
		rm -rf "${MATRIX_TMP:-/nonexistent}"
		exit 0
	fi

	local engines engine
	engines="$(engines_of)"
	[ -n "$engines" ] || die "$(upper "$CSP")_ENGINES is empty. Name the engines to run."

	# --src-versions/--dst-versions are allowed with a single engine only. One list
	# across several engines would ask mariadb for mysql 8.0.
	if [ -n "$CLI_SRC_VERSIONS$CLI_DST_VERSIONS" ]; then
		[ "$(printf '%s\n' $engines | wc -l)" -eq 1 ] \
			|| die "--src-versions/--dst-versions need a single engine (name one with --engines)."
		local up; up="$(upper "$CSP")_$(upper "$engines")"
		[ -n "$CLI_SRC_VERSIONS" ] && export "${up}_SRC_VERSIONS=$CLI_SRC_VERSIONS"
		[ -n "$CLI_DST_VERSIONS" ] && export "${up}_DST_VERSIONS=$CLI_DST_VERSIONS"
	fi

	banner "0. pre-flight — $CSP_TITLE managed-RDBMS version matrix"
	info "name prefix : $MATRIX_NAME_PREFIX   region: $(csp_env "$CSP" REGION)$([ "$(lower "$CSP")" = "ncp" ] && printf '   zone: %s' "$(csp_env "$CSP" ZONE)")"
	info "tofu runner : $TOFU_RUNNER   OpenBao: ${VAULT_ADDR:-http://localhost:38210}"
	info "collect/move: honeybee $HB_BASE   centipede $CP_BASE"
	info "engines     : $engines   (offered as managed by this CSP: $(csp_engines "$CSP"))"
	for engine in $engines; do
		info "  $engine — source [$(src_versions_of "$engine")]  ->  target [$(dst_versions_of "$engine")]"
	done
	info "source access: $MODE ($HOST_IP)   target access: direct"
	info "databases   : source $SRC_DB -> target $DST_DB"
	info "target TLS  : TARGET_TLS_MODE=$(target_tls_mode)   (the migration; setup uses the best transport the server offers)"
	for engine in $engines; do
		tls_mode_downgraded "$engine" \
			&& info "              $engine alone runs at $(target_tls_mode "$engine") — it does not support $(target_tls_mode)"
	done
	if [ "$(lower "$CSP")" = "aws" ]; then
		info "plaintext   : secure_transport=$AWS_TARGET_SECURE_TRANSPORT   (csp-default = the CSP setting untouched)"
	fi
	info "on failure  : dbmsOnFailure=${DBMS_ON_FAILURE:-cleanup}"
	[ -n "$ONLY_CELLS" ] && info "cells       : $ONLY_CELLS"

	assert_tls_mode "$engines" || die "TLS mode check failed. Nothing was created."

	case "$PG_TARGET_TEMPLATE" in
	template0|template1) ;;
	*) die "PG_TARGET_TEMPLATE must be template0 or template1: '$PG_TARGET_TEMPLATE'
       Nothing was created." ;;
	esac

	local missing=""
	[ -n "$(csp_env "$CSP" REGION)" ] || missing="$missing $(upper "$CSP")_REGION"
	if [ "$(lower "$CSP")" = "ncp" ]; then
		[ -n "$(csp_env "$CSP" ZONE)" ] || missing="$missing NCP_ZONE"
	fi
	[ -n "$missing" ] && die "these settings are empty:$missing
       Fill them in in $ENV_FILE."

	# Every engine is checked first: learning that the fourth engine is impossible
	# after building three instances wastes all three.
	local engine_error=0
	for engine in $engines; do
		if ! assert_engine_known "$engine" "$CSP"; then
			engine_error=1
			continue
		fi
		[ -n "$(src_versions_of "$engine")" ] || die "$(upper "$CSP")_$(upper "$engine")_SRC_VERSIONS is empty."
		[ -n "$(dst_versions_of "$engine")" ] || die "$(upper "$CSP")_$(upper "$engine")_DST_VERSIONS is empty."
	done
	[ "$engine_error" -eq 0 ] || die "engine check failed. Nothing was created."

	assert_src_ports "$engines" || die "source port check failed. Nothing was created."

	# MongoDB runs direct-only: transx-ex requires both sides to use the same
	# accessType, and a managed target has no shell, so it is always direct.
	if [ "$MODE" = "ssh" ]; then
		case " $engines " in
		*" mongodb "*) die "mongodb cannot run with MODE=ssh (both sides need the same accessType, and the target is direct).
       Drop it from --engines, or run with --mode direct." ;;
		esac
	fi

	# The env file holds the OpenBao root token.
	if [ -f "$ENV_FILE" ]; then
		local mode=""
		mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || true)"
		case "$mode" in
		''|600|400) ;;
		*) warn "$ENV_FILE is mode $mode — it holds passwords, so chmod 600 is advised." ;;
		esac
	fi

	# The migration stack is checked first. Learning that honeybee is not running
	# should never cost a 30-minute instance.
	sub "1) migration stack — cm-honeybee / cm-centipede"
	hb_preflight || die "honeybee check failed. Nothing was created."
	cp_preflight || die "centipede check failed. Nothing was created."
	HB_SG_ID="$(hb_source_group "${HB_SOURCE_GROUP:-cptfm-matrix}")" \
		|| die "could not obtain the honeybee SourceGroup. Nothing was created."
	info "honeybee SourceGroup: ${HB_SOURCE_GROUP:-cptfm-matrix} ($HB_SG_ID)"

	sub "2) tofu stack / credentials"
	tofu_preflight
	assert_db_password "$CSP" || die "credential check failed. Nothing was created."

	sub "3) target versions"
	local version_error=0
	for engine in $engines; do
		assert_target_versions "$CSP" "$engine" "$(dst_versions_of "$engine")" || version_error=1
	done
	[ "$version_error" -eq 0 ] || die "target version check failed. Nothing was created."

	if [ "$MODE" = "ssh" ]; then
		require_cmd ssh-keygen
		ensure_ssh_key || die "could not prepare the SSH key"
	fi

	for engine in $engines; do src_stop "$engine"; done

	sub "4) network"
	# AWS uses the default VPC as is, so there is nothing to do; only NCP creates a
	# VPC and a subnet.
	NETWORK_READY=1
	ensure_network "$CSP" "$engines" || die "could not prepare the network."

	local dver rc
	for engine in $engines; do
		for dver in $(dst_versions_of "$engine"); do
			run_column "$engine" "$dver"
			rc=$?
			release_column "$ACTIVE_INSTANCE"
			[ "$rc" -eq 1 ] && COLUMN_ERRORS=$((COLUMN_ERRORS + 1))
			if [ "$rc" -eq 2 ]; then break 2; fi
		done
	done

	print_matrix
	write_result_json
	[ "${NO_LOG:-0}" != "1" ] && info "run log : $LOG_FILE"

	# A column that could not be prepared is a failure too. Ending with 0 while
	# leaving only SKIPs would read as success in CI.
	{ [ "$MATRIX_FAILED" -eq 0 ] && [ "$COLUMN_ERRORS" -eq 0 ]; } || exit 1
	exit 0
}

# ---------------------------------------------------------------------------
# Defaults — the env file and real shell variables win (${VAR:-default})
#   Precedence: CLI option > real shell variable > .env > here
# ---------------------------------------------------------------------------
matrix_defaults() {
	MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cptfm}"
	MATRIX_ALLOWED_CIDR="${MATRIX_ALLOWED_CIDR:-0.0.0.0/0}"

	TOFU_RUNNER="${TOFU_RUNNER:-cptfm-tofu-runner}"
	VAULT_ADDR="${VAULT_ADDR:-http://localhost:38210}"

	# The migration stack
	HB_BASE="${HB_BASE:-http://localhost:8081/honeybee}"
	CP_BASE="${CP_BASE:-http://localhost:8085/centipede}"
	CP_USER="${CP_USER:-default}"
	CP_PASS="${CP_PASS:-default}"
	HB_SOURCE_GROUP="${HB_SOURCE_GROUP:-cptfm-matrix}"
	MIGRATION_PREFIX="${MIGRATION_PREFIX:-cptfm}"
	KEEP_MIGRATION="${KEEP_MIGRATION:-1}"
	DBMS_ON_FAILURE="${DBMS_ON_FAILURE:-cleanup}"
	POLL_INTERVAL="${POLL_INTERVAL:-5}"
	MIGRATION_TIMEOUT="${MIGRATION_TIMEOUT:-1800}"
	VALIDATION_TIMEOUT="${VALIDATION_TIMEOUT:-600}"

	# How the source is reached. The target is managed and therefore always direct,
	# so there is no DST_MODE.
	MODE="${MODE:-direct}"

	HOST_IP="${HOST_IP:-127.0.0.1}"
	DB_ROOT_PASS="${DB_ROOT_PASS:-testpass123}"
	SRC_DB_USER="${SRC_DB_USER:-centipede}"
	SRC_DB_PASS="${SRC_DB_PASS:-centipede_pass}"
	SRC_DB="${SRC_DB:-matrix_db}"
	DST_DB="${DST_DB:-matrix_db}"
	SRC_PROVIDER="${SRC_PROVIDER:-onprem}"

	# The admin database created with the instance. The matrix connects here to
	# create and drop each cell's target database - you cannot DROP the database you
	# are connected to, which is why a separate one is needed.
	ADMIN_DB="${ADMIN_DB:-matrixadm}"

	# Transport security for the target. prefer is the default because a managed
	# instance offers TLS either way, so the connection is encrypted in practice,
	# and it still reaches the ones that refuse plaintext (MariaDB 11.8+, RDS
	# PostgreSQL).
	TARGET_TLS_MODE="${TARGET_TLS_MODE:-prefer}"
	MONGODB_AUTH_SOURCE="${MONGODB_AUTH_SOURCE:-admin}"

	# Whether plaintext is allowed on an AWS target. A PASS won by switching the
	# parameter off does not answer "does this migrate in a real customer
	# environment", so the default leaves the CSP setting alone.
	AWS_TARGET_SECURE_TRANSPORT="${AWS_TARGET_SECURE_TRANSPORT:-csp-default}"

	# The template a cell's target database is copied from. A new database inherits
	# that template's public ACL, which is the value the GRANT decision reads.
	# AWS only - on NCP the CSP creates the database.
	PG_TARGET_TEMPLATE="${PG_TARGET_TEMPLATE:-template1}"

	ONLY_ENGINES="${ONLY_ENGINES:-}"
	ONLY_CELLS="${ONLY_CELLS:-}"
	CLEANUP_ONLY="${CLEANUP_ONLY:-0}"
	STOP_ON_FAIL="${STOP_ON_FAIL:-0}"
	KEEP_ON_FAIL="${KEEP_ON_FAIL:-0}"
	KEEP_INSTANCE="${KEEP_INSTANCE:-0}"
	KEEP_NETWORK="${KEEP_NETWORK:-0}"
	REUSE_INSTANCE="${REUSE_INSTANCE:-1}"
	SRC_BUILD_TIMEOUT="${SRC_BUILD_TIMEOUT:-900}"
	READY_TIMEOUT="${READY_TIMEOUT:-300}"
	TARGET_REACH_TIMEOUT="${TARGET_REACH_TIMEOUT:-180}"
	DELETE_RETRIES="${DELETE_RETRIES:-1}"
	DELETE_RETRY_WAIT="${DELETE_RETRY_WAIT:-60}"
	NO_PAUSE="${NO_PAUSE:-1}"
	NO_LOG="${NO_LOG:-0}"
	ENV_FILE="${ENV_FILE:-$MATRIX_DIR/.env}"
}
