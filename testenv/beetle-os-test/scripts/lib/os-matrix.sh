#!/usr/bin/env bash
#
# lib/os-matrix.sh — the matrix runner
#
# scripts/os-matrix.sh sources this and calls matrix_main.
#
#   run
#     ├ source ──────── one MinIO container, six seeded buckets
#     ├ collect ─────── one honeybee inspect per bucket, cached for the whole run
#     └ csp
#          └ bucket ─── cell: create target bucket -> plan -> migrate -> validate -> delete
#
# The number of target buckets equals the number of cells, and each is created
# and destroyed inside its cell. That is affordable here in a way it is not for a
# managed database: a bucket is made in seconds and costs nothing while empty.
#
# ── The migration and the verdict belong to someone else ────────────────────
# This matrix does not call transx-ex. cm-honeybee inspects the source and
# cm-centipede plans, migrates and validates; this file calls the two and turns
# the answers into a table. So a verdict rests on centipede's validationStatus
# rather than on a listing this folder made itself.
#
# Verdicts
#   PASS — migration completed and validation passed
#   FAIL — anything else
#   SKIP — filtered out, or the source bucket is not there

if [ -n "${MATRIX_OS_MATRIX_SH:-}" ]; then return 0; fi
MATRIX_OS_MATRIX_SH=1

LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$LIB_DIR/common.sh"
# shellcheck source=./beetle.sh
. "$LIB_DIR/beetle.sh"
# shellcheck source=./source.sh
. "$LIB_DIR/source.sh"
# shellcheck source=./honeybee.sh
. "$LIB_DIR/honeybee.sh"
# shellcheck source=./centipede.sh
. "$LIB_DIR/centipede.sh"

MATRIX_DIR="$(cd "$LIB_DIR/../.." && pwd)"

declare -A CELL_STATUS CELL_ELAPSED CELL_DETAIL CELL_MIGID CELL_VALID
declare -A CELL_OSID CELL_CSPNAME
declare -A SRC_MODEL SRC_OBJECTS SRC_BYTES SRC_SCANROOT

# Buckets created and not yet released, as "<csp>|<osId>".
CREATED_BUCKETS=()
ACTIVE_CSP=""
ACTIVE_OSID=""
MATRIX_FAILED=0
SOURCE_UP=0

# honeybee's SourceGroup id — obtained once per run.
HB_SG_ID=""

# ---------------------------------------------------------------------------
# Reading the settings
# ---------------------------------------------------------------------------
csps_of()    { printf '%s' "${ONLY_CSPS:-$OS_CSPS}"; }
buckets_of() { printf '%s' "${ONLY_BUCKETS:-$OS_SRC_BUCKETS}"; }

usage() {
	cat <<USAGE
Object storage migration matrix — MinIO -> CSP object storage, over cm-beetle

  $(basename "$0") [options]

  rows    = the source buckets in OS_SRC_BUCKETS
  columns = the CSPs in OS_CSPS
  a cell  = one source bucket migrated into one target bucket on one CSP

Options:
  --csp "C1 C2"          CSPs to run (default: OS_CSPS = ${OS_CSPS:-unset})
  --buckets "B1 B2"      source buckets to run (default: OS_SRC_BUCKETS)
  --only "CSP:BUCKET .."  run only these cells
  --keep-bucket          keep every target bucket at the end (they hold objects)
  --keep-on-fail         keep a failed cell's target bucket (it holds partial data)
  --keep-source          keep the MinIO source container at the end
  --stop-on-fail         stop at the first FAIL
  --cleanup              do not run the matrix; reclaim every bucket this matrix
                         left in the cb-tumblebug namespace, then the namespace
  -h, --help             this help

⚠ Requires cm-honeybee, cm-centipede, cm-beetle and cb-tumblebug to be running,
  with the <csp>-<region> connection registered. Checked before anything is made.

Options that do not exist, and why:
  --mode ssh       object storage access is always direct. honeybee's ssh-tunnel
                   inspect needs an agent on the source host, and transx-ex has no
                   tunnelled transfer path for storage at all.
  --prefix         whole buckets are what this folder migrates. A sub-prefix would
                   be flattened onto the target bucket root, because a
                   beetleObjectStorage destination has no prefix of its own.
  --tls-mode       an S3 endpoint's transport is decided by the provider table,
                   not by a setting this folder owns.
  --scope/--async  centipede is always full scope and always asynchronous.
USAGE
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--csp)          ONLY_CSPS="$2"; shift 2 ;;
		--buckets)      ONLY_BUCKETS="$2"; shift 2 ;;
		--only)         ONLY_CELLS="$2"; shift 2 ;;
		--keep-bucket)  KEEP_BUCKET=1; shift ;;
		--keep-on-fail) KEEP_ON_FAIL=1; shift ;;
		--keep-source)  KEEP_SOURCE=1; shift ;;
		--stop-on-fail) STOP_ON_FAIL=1; shift ;;
		--cleanup)      CLEANUP_ONLY=1; shift ;;
		-h|--help)      usage; exit 0 ;;
		--mode|--prefix|--tls-mode|--scope|--async)
			die "$1 does not exist in this folder. See --help for why." ;;
		*) echo "unknown option: $1" >&2; usage; exit 1 ;;
		esac
	done
}

# cell_selected CSP BUCKET — the ONLY_CELLS filter
cell_selected() {
	[ -z "$ONLY_CELLS" ] && return 0
	grep -qw -- "$1:$2" <<<"$ONLY_CELLS" && return 0
	return 1
}

# ---------------------------------------------------------------------------
# Cells
# ---------------------------------------------------------------------------
finish_cell() {
	CELL_STATUS["$1"]="$2"; CELL_ELAPSED["$1"]="$3"; CELL_DETAIL["$1"]="$4"
	case "$2" in
	PASS) echo -e "  ${C_OK}▣ $1 : PASS${C_OFF}  ($(secs_fmt "$3"))" ;;
	SKIP) echo -e "  ▣ $1 : SKIP — $4" ;;
	*)    echo -e "  ${C_ERR}▣ $1 : FAIL${C_OFF}  ($(secs_fmt "$3")) — $4" ;;
	esac
	pause
}

# release_bucket CSP OSID — drop it from the created list and delete it.
release_bucket() {
	local csp="$1" os_id="$2" i key
	[ -n "$os_id" ] || return 0
	delete_bucket "$csp" "$os_id" || true
	key="$csp|$os_id"
	for i in "${!CREATED_BUCKETS[@]}"; do
		[ "${CREATED_BUCKETS[$i]}" = "$key" ] && unset 'CREATED_BUCKETS[i]'
	done
	CREATED_BUCKETS=("${CREATED_BUCKETS[@]}")
}

# cleanup_cell CSP OSID RC — tidy up after a cell.
cleanup_cell() {
	local csp="$1" os_id="$2" rc="$3"
	cp_delete_migration
	ACTIVE_CSP=""; ACTIVE_OSID=""
	if [ -z "$os_id" ]; then return 0; fi
	if [ "${KEEP_BUCKET:-0}" = "1" ]; then
		warn "KEEP_BUCKET=1 — keeping the target bucket: $os_id"
		warn "  It holds the migrated objects. Reclaim it later: ./scripts/os-matrix.sh --cleanup"
		return 0
	fi
	if [ "$rc" -ne 0 ] && [ "${KEEP_ON_FAIL:-0}" = "1" ]; then
		warn "KEEP_ON_FAIL=1 — keeping the failed cell's target bucket: $os_id"
		warn "  ⚠ It holds whatever the transfer had already written. An object storage"
		warn "    migration has no rollback, so this is partial data, not an empty bucket."
		warn "  Reclaim it later: ./scripts/os-matrix.sh --cleanup"
		return 0
	fi
	release_bucket "$csp" "$os_id"
}

# run_cell CSP BUCKET
run_cell() {
	local csp="$1" bucket="$2"
	local key="$csp:$bucket"
	local started ended elapsed os_id mig_name

	# Clear what the previous cell left. If this cell ends at the plan step,
	# cp_migrate is never called, and cleanup_cell would then delete the previous
	# cell's migration record.
	CP_MIGRATION_ID=""; CP_STATUS=""; CP_VALIDATION=""; CP_ERROR=""

	banner "cell [$(upper "$csp")] $bucket -> managed bucket   (source direct, target beetleObjectStorage)"
	started="$(date +%s)"
	# The bucket and the scan root, not a URL. "<endpoint>/<bucket>" would be a
	# path-style S3 address and is not wrong, but nothing here addresses the bucket
	# that way - honeybee is handed os_endpoint and bucket as separate fields, and
	# transx-ex takes a resolved endpoint plus the path "<bucket>/". Printing a URL
	# invites someone to fetch it, and a plain GET against an S3 API endpoint
	# answers AccessDenied. The scan root is what matters: it is the srcPath the
	# plan will carry, so this line and the plan summary two steps later can be
	# read against each other.
	info "source: bucket $bucket   scan root ${SRC_SCANROOT[$bucket]:-?}   ${SRC_OBJECTS[$bucket]:-?} object(s), ${SRC_BYTES[$bucket]:-?} bytes"

	# ── 1) Target bucket ────────────────────────────────────────────────────
	# centipede creates no bucket of its own — MigrateObjectStorage resolves both
	# ends and hands them to transx-ex — so this comes first.
	sub "1) target bucket — cm-beetle"
	if ! create_bucket "$csp" "$bucket" "${SRC_OBJECTS[$bucket]:-0}" "${SRC_BYTES[$bucket]:-0}"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "target bucket could not be created"
		cleanup_cell "$csp" "" 1
		return
	fi
	os_id="$OS_ID"
	CREATED_BUCKETS+=("$csp|$os_id")
	ACTIVE_CSP="$csp"; ACTIVE_OSID="$os_id"
	CELL_OSID["$key"]="$os_id"
	CELL_CSPNAME["$key"]="${OS_CSP_NAME:-$OS_UID}"

	# ── 2) Plan ─────────────────────────────────────────────────────────────
	sub "2) target plan — POST /plans/target"
	if ! cp_plan "${SRC_MODEL[$bucket]}" "$os_id"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "plan failed - $CP_ERROR"
		cleanup_cell "$csp" "$os_id" 1
		return
	fi
	cp_plan_summary

	# ── 3) Migration ────────────────────────────────────────────────────────
	mig_name="${MIGRATION_PREFIX:-cpbos}-${csp}-${bucket}-$(date '+%Y%m%d-%H%M%S')"
	sub "3) migration — POST /migration ($mig_name)"
	if ! cp_migrate "$mig_name"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "migration could not be created - $CP_ERROR"
		cleanup_cell "$csp" "$os_id" 1
		return
	fi
	CELL_MIGID["$key"]="$CP_MIGRATION_ID"
	info "MIGRATION_ID=$CP_MIGRATION_ID"

	if ! cp_poll; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		# The migration never finished, so validation is not reached and step 5
		# never runs — the log is read here instead, where it says what broke.
		sub "4) migration log — GET /migration/$CP_MIGRATION_ID/logs"
		cp_migration_logs
		finish_cell "$key" FAIL "$elapsed" "[$CP_STATUS] $CP_ERROR"
		cleanup_cell "$csp" "$os_id" 1
		return
	fi

	# ── 4) Validation ───────────────────────────────────────────────────────
	sub "4) validation — POST /migration/$CP_MIGRATION_ID/validation"
	cp_validate || true
	CELL_VALID["$key"]="$CP_VALIDATION"
	cp_validation_summary

	# ── 5) Migration log ────────────────────────────────────────────────────
	# Read after validation rather than before it: the verdict above is easier to
	# read against the per-item detail than the other way round.
	sub "5) migration log — GET /migration/$CP_MIGRATION_ID/logs"
	cp_migration_logs

	ended="$(date +%s)"; elapsed=$((ended - started))
	if [ "$CP_VALIDATION" = "passed" ]; then
		finish_cell "$key" PASS "$elapsed" "validation passed (${SRC_OBJECTS[$bucket]:-?} objects)"
		cleanup_cell "$csp" "$os_id" 0
	else
		finish_cell "$key" FAIL "$elapsed" "validation $CP_VALIDATION - $CP_ERROR"
		cleanup_cell "$csp" "$os_id" 1
	fi
}

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------
cell_text() {
	local st="${CELL_STATUS[$1]:-SKIP}"
	case "$st" in
	PASS|FAIL) printf '%-5s %s' "$st" "$(secs_fmt "${CELL_ELAPSED[$1]:-0}")" ;;
	*)         printf 'SKIP' ;;
	esac
}
cell_color() {
	case "${CELL_STATUS[$1]:-SKIP}" in
	PASS) printf '%b' "$C_OK" ;;
	FAIL) printf '%b' "$C_ERR" ;;
	*)    printf '' ;;
	esac
}

print_matrix() {
	local csp bucket key line w=11
	local pass=0 failed=0 skipped=0
	local bw=14

	banner "Object storage matrix result — MinIO -> managed bucket  (beetle + honeybee + centipede)"

	sub "rows = source bucket, columns = CSP"
	line="$(printf '  %-*s │' "$bw" 'bucket \ csp')"
	for csp in $(csps_of); do line+="$(printf ' %-*s' "$w" "$csp")"; done
	echo -e "${C_HDR}$line${C_OFF}"
	line="  $(printf '%*s' "$((bw + 1))" '' | tr ' ' '-')┼"
	for csp in $(csps_of); do line+="$(printf -- '-%.0s' $(seq 1 $((w + 1))))"; done
	echo "$line"
	for bucket in $(buckets_of); do
		printf '  %-*s │' "$bw" "$bucket"
		for csp in $(csps_of); do
			key="$csp:$bucket"
			printf ' %b%-*s%b' "$(cell_color "$key")" "$w" "$(cell_text "$key")" "$C_OFF"
		done
		echo
	done

	for csp in $(csps_of); do
		for bucket in $(buckets_of); do
			case "${CELL_STATUS[$csp:$bucket]:-SKIP}" in
			PASS) pass=$((pass + 1)) ;;
			FAIL) failed=$((failed + 1)) ;;
			*)    skipped=$((skipped + 1)) ;;
			esac
		done
	done

	echo
	echo -e "  ${C_OK}PASS $pass${C_OFF} / ${C_ERR}FAIL $failed${C_OFF} / SKIP $skipped" \
		"  $((pass + failed + skipped)) cells   elapsed $(secs_fmt "$(( $(date +%s) - RUN_STARTED ))")"

	sub "cell detail"
	for csp in $(csps_of); do
		for bucket in $(buckets_of); do
			key="$csp:$bucket"
			[ -n "${CELL_STATUS[$key]:-}" ] || continue
			printf '    %-6s %-16s %-5s  %s\n' "$csp" "$bucket" \
				"${CELL_STATUS[$key]}" "${CELL_DETAIL[$key]:-}"
		done
	done

	MATRIX_FAILED="$failed"
}

write_result_json() {
	local csp bucket key csps="[]" cells
	for csp in $(csps_of); do
		cells="[]"
		for bucket in $(buckets_of); do
			key="$csp:$bucket"
			cells="$(jq -c \
				--arg b "$bucket" \
				--arg st "${CELL_STATUS[$key]:-SKIP}" \
				--arg de "${CELL_DETAIL[$key]:-}" \
				--arg os "${CELL_OSID[$key]:-}" \
				--arg cn "${CELL_CSPNAME[$key]:-}" \
				--arg mid "${CELL_MIGID[$key]:-}" \
				--arg val "${CELL_VALID[$key]:-}" \
				--arg root "${SRC_SCANROOT[$bucket]:-}" \
				--argjson el "${CELL_ELAPSED[$key]:-0}" \
				--argjson oc "${SRC_OBJECTS[$bucket]:-0}" \
				--argjson by "${SRC_BYTES[$bucket]:-0}" \
				'. + [{bucket:$b, status:$st, elapsedSec:$el,
				       osId:(if $os == "" then null else $os end),
				       cspBucketName:(if $cn == "" then null else $cn end),
				       srcScanRoot:(if $root == "" then null else $root end),
				       srcObjectCount:$oc, srcSizeBytes:$by,
				       migrationId:$mid, validationStatus:$val, detail:$de}]' <<<"$cells")"
		done
		csps="$(jq -c --arg c "$csp" --arg r "$(csp_env "$csp" REGION)" --argjson cl "$cells" \
			'. + [{csp:$c, region:$r, cells:$cl}]' <<<"$csps")"
	done
	jq -n --arg prefix "$MATRIX_NAME_PREFIX" --arg ns "$MATRIX_NS" \
		--arg ts "$(date '+%F %T')" --argjson c "$csps" \
		'{namePrefix:$prefix, nsId:$ns, provisioner:"cm-beetle",
		  migrator:"cm-centipede", collector:"cm-honeybee",
		  srcMode:"direct", dstMode:"direct",
		  srcType:"minio", dstType:"beetleObjectStorage",
		  finishedAt:$ts, csps:$c}' \
		> "$RESULT_FILE"
	info "result JSON: $RESULT_FILE"
}

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
# cleanup_exit — however the run ends, no target bucket is left behind unless it
#   was asked for. A bucket costs little, but one left holding migrated objects
#   makes the next run's validation fail with a confusing reason.
#
#   The namespace is NOT deleted here. It is free to keep, the next run reuses it,
#   and --cleanup is where removing it belongs.
cleanup_exit() {
	local entry csp os_id
	trap - EXIT INT TERM

	if [ "${#CREATED_BUCKETS[@]}" -gt 0 ]; then
		if [ "${KEEP_BUCKET:-0}" = "1" ]; then
			warn "KEEP_BUCKET=1 — keeping the target buckets:"
			for entry in "${CREATED_BUCKETS[@]}"; do
				warn "  ${entry#*|}  (namespace $MATRIX_NS)"
			done
			warn "  Reclaim: ./scripts/os-matrix.sh --cleanup"
		else
			sub "removing the target buckets left"
			for entry in "${CREATED_BUCKETS[@]}"; do
				csp="${entry%%|*}"; os_id="${entry#*|}"
				delete_bucket "$csp" "$os_id" || true
			done
			CREATED_BUCKETS=()
		fi
	fi

	if [ "$SOURCE_UP" = "1" ] && [ "${KEEP_SOURCE:-0}" != "1" ]; then
		src_stop
	elif [ "$SOURCE_UP" = "1" ]; then
		warn "KEEP_SOURCE=1 — the source container is still running: $(src_container)"
		warn "  Stop it with: docker rm -f $(src_container)"
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
	parse_args "$@"

	# Logs — the two .log files accumulate across runs, each run opening with a
	# separator (see log_run_header). The result JSON is not a log but a single
	# document, so it is replaced.
	LOG_DIR="${LOG_DIR:-$MATRIX_DIR/logs}"
	LOG_NAME="os-matrix"
	LOG_FILE="${LOG_FILE:-$LOG_DIR/$LOG_NAME.log}"
	API_LOG_FILE="${API_LOG_FILE:-$LOG_DIR/$LOG_NAME-api.log}"
	RESULT_FILE="${RESULT_FILE:-$LOG_DIR/$LOG_NAME-result.json}"

	# Created outside the NO_LOG check, because NO_LOG only silences the two .log
	# files — write_result_json still writes the result JSON in here.
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

	MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cpbosmatrix.XXXXXX")"
	RUN_STARTED="$(date +%s)"
	trap 'cleanup_exit' EXIT INT TERM

	require_cmd docker jq curl
	docker info >/dev/null 2>&1 || die "the docker daemon is not running."

	local csps csp buckets bucket

	# --cleanup — do not run the matrix; only reclaim what is left.
	#   cb-tumblebug's namespace is the state, so this needs nothing from a previous
	#   run: the buckets are found by name prefix in the list beetle returns.
	if [ "${CLEANUP_ONLY:-0}" = "1" ]; then
		banner "reclaiming what this matrix left in namespace $MATRIX_NS"
		beetle_preflight
		for csp in $(csps_of); do
			sub "$(upper "$csp")"
			cleanup_all "$csp" || true
		done
		cleanup_namespace
		src_stop
		trap - EXIT INT TERM
		rm -rf "${MATRIX_TMP:-/nonexistent}"
		exit 0
	fi

	csps="$(csps_of)"
	buckets="$(buckets_of)"
	[ -n "${csps// }" ]    || die "OS_CSPS is empty. Name the CSPs to run, or comment the key out to take the default."
	[ -n "${buckets// }" ] || die "OS_SRC_BUCKETS is empty. Name the source buckets to run, or comment the key out to take the default."

	banner "0. pre-flight — object storage migration matrix"
	info "name prefix : $MATRIX_NAME_PREFIX   namespace: $MATRIX_NS"
	info "provision   : beetle $BEETLE_URL   tumblebug $TUMBLEBUG_URL"
	info "collect/move: honeybee $HB_BASE   centipede $CP_BASE"
	info "CSPs        : $csps"
	for csp in $csps; do
		info "  $csp — region $(csp_env "$csp" REGION)   connection $(connection_name "$csp")"
	done
	info "buckets     : $buckets"
	info "source      : MinIO container $(src_container) at $(src_endpoint)"
	info "target      : one managed bucket per cell, deleted with the cell"
	[ -n "$ONLY_CELLS" ] && info "cells       : $ONLY_CELLS"

	local missing=""
	for csp in $csps; do
		[ -n "$(csp_env "$csp" REGION)" ] || missing="$missing $(upper "$csp")_REGION"
	done
	[ -n "$MATRIX_NS" ] || missing="$missing MATRIX_NS"
	[ -n "$missing" ] && die "these settings are empty:$missing
       Fill them in in $ENV_FILE."

	# The env file holds the MinIO root account in plain text, and the file mode is
	# the whole protection.
	if [ -f "$ENV_FILE" ]; then
		local mode=""
		mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || true)"
		case "$mode" in
		''|600|400) ;;
		*) warn "$ENV_FILE is mode $mode — it holds credentials, so chmod 600 is advised." ;;
		esac
	fi

	assert_minio_account || die "source account check failed. Nothing was created."

	# The migration stack is checked first. Learning that honeybee is not running
	# should never cost a created bucket.
	sub "1) migration stack — cm-honeybee / cm-centipede"
	hb_preflight || die "honeybee check failed. Nothing was created."
	cp_preflight || die "centipede check failed. Nothing was created."

	sub "2) provisioner — cm-beetle / cb-tumblebug"
	beetle_preflight
	assert_os_support "$csps" || die "object storage support check failed. Nothing was created."
	local conn_error=0
	for csp in $csps; do
		assert_connection "$csp" || conn_error=1
	done
	[ "$conn_error" -eq 0 ] || die "connection check failed. Nothing was created."
	ensure_namespace || die "could not prepare the namespace. Nothing was created."

	sub "3) source — one MinIO container for the whole run"
	local img
	img="$(src_image)" || die "source image build failed."
	src_start "$img" || die "source container failed to start."
	SOURCE_UP=1
	src_wait_ready || die "source initialization failed."

	# What is actually there, against what was asked for. A missing bucket costs
	# its own row and nothing else; a source with nothing in it at all is a
	# different thing and stops the run, because every cell would fail the same way.
	assert_src_buckets "$buckets"
	case $? in
	2) die "the source container holds no buckets. Nothing was created." ;;
	esac
	for bucket in ${SRC_MISSING_BUCKETS:-}; do
		for csp in $csps; do
			CELL_STATUS["$csp:$bucket"]="SKIP"
			CELL_ELAPSED["$csp:$bucket"]=0
			CELL_DETAIL["$csp:$bucket"]="the source has no bucket named '$bucket'"
		done
	done

	sub "4) collecting the source — cm-honeybee (once per bucket)"
	HB_SG_ID="$(hb_source_group "${HB_SOURCE_GROUP:-cpbos-matrix}")" \
		|| die "could not obtain the honeybee SourceGroup. Nothing was created."
	info "honeybee SourceGroup: ${HB_SOURCE_GROUP:-cpbos-matrix} ($HB_SG_ID)  type=minio provider=onprem"

	local conn stats collected=0
	for bucket in $buckets; do
		in_list "$bucket" "${SRC_MISSING_BUCKETS:-}" && continue

		# Skip a bucket every cell of which is filtered out — an inspect walks the
		# whole bucket on the source, so it is not free.
		local any=0
		for csp in $csps; do
			if cell_selected "$csp" "$bucket"; then any=1; break; fi
		done
		if [ "$any" -eq 0 ]; then
			for csp in $csps; do
				CELL_STATUS["$csp:$bucket"]="SKIP"
				CELL_ELAPSED["$csp:$bucket"]=0
				CELL_DETAIL["$csp:$bucket"]="excluded by filter"
			done
			continue
		fi

		stats="$(src_bucket_stats "$bucket")"
		SRC_OBJECTS["$bucket"]="${stats%% *}"
		SRC_BYTES["$bucket"]="${stats##* }"

		conn="$(hb_connection "$HB_SG_ID" "$bucket")" || {
			for csp in $csps; do
				CELL_STATUS["$csp:$bucket"]="SKIP"; CELL_ELAPSED["$csp:$bucket"]=0
				CELL_DETAIL["$csp:$bucket"]="honeybee connection could not be obtained"
			done
			continue
		}
		if ! hb_import "$HB_SG_ID" "$conn" "$bucket"; then
			for csp in $csps; do
				CELL_STATUS["$csp:$bucket"]="SKIP"; CELL_ELAPSED["$csp:$bucket"]=0
				CELL_DETAIL["$csp:$bucket"]="honeybee could not inspect the bucket"
			done
			continue
		fi
		SRC_MODEL["$bucket"]="$(hb_source_model "$HB_SG_ID" "$conn")" || {
			for csp in $csps; do
				CELL_STATUS["$csp:$bucket"]="SKIP"; CELL_ELAPSED["$csp:$bucket"]=0
				CELL_DETAIL["$csp:$bucket"]="honeybee collection result could not be read"
			done
			continue
		}
		SRC_SCANROOT["$bucket"]="$(hb_scan_root "${SRC_MODEL[$bucket]}")"
		collected=$((collected + 1))
		info "  $bucket — scan root ${SRC_SCANROOT[$bucket]}  ${SRC_OBJECTS[$bucket]} object(s), $(hb_folder_count "${SRC_MODEL[$bucket]}") prefix(es), ${SRC_BYTES[$bucket]} bytes"
	done
	[ "$collected" -gt 0 ] || die "no source bucket could be collected. Nothing was created."

	# ── cells ───────────────────────────────────────────────────────────────
	local stop=0
	for csp in $csps; do
		for bucket in $buckets; do
			[ -n "${CELL_STATUS[$csp:$bucket]:-}" ] && continue
			if ! cell_selected "$csp" "$bucket"; then
				CELL_STATUS["$csp:$bucket"]="SKIP"; CELL_ELAPSED["$csp:$bucket"]=0
				CELL_DETAIL["$csp:$bucket"]="excluded by filter"
				continue
			fi
			run_cell "$csp" "$bucket"
			if [ "${STOP_ON_FAIL:-0}" = "1" ] && [ "${CELL_STATUS[$csp:$bucket]}" = "FAIL" ]; then
				warn "STOP_ON_FAIL=1 — stopping at the first failure."
				stop=1
				break
			fi
		done
		[ "$stop" -eq 1 ] && break
	done

	print_matrix
	write_result_json
	[ "${NO_LOG:-0}" != "1" ] && info "run log : $LOG_FILE"

	[ "$MATRIX_FAILED" -eq 0 ] || exit 1
	exit 0
}

# ---------------------------------------------------------------------------
# Defaults — the env file and real shell variables win (${VAR:-default})
#   Precedence: CLI option > real shell variable > .env > here
# ---------------------------------------------------------------------------
matrix_defaults() {
	MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cpbos}"
	MATRIX_NS="${MATRIX_NS:-cpbos01}"

	# The provisioner. Every resource call goes to beetle; tumblebug answers the
	# namespace and the connection catalogue only.
	BEETLE_URL="${BEETLE_URL:-http://localhost:8056/beetle}"
	BEETLE_USERNAME="${BEETLE_USERNAME:-default}"
	BEETLE_PASSWORD="${BEETLE_PASSWORD:-default}"
	TUMBLEBUG_URL="${TUMBLEBUG_URL:-http://localhost:1323/tumblebug}"
	TUMBLEBUG_USERNAME="${TUMBLEBUG_USERNAME:-default}"
	TUMBLEBUG_PASSWORD="${TUMBLEBUG_PASSWORD:-default}"

	# The migration stack
	HB_BASE="${HB_BASE:-http://localhost:8081/honeybee}"
	CP_BASE="${CP_BASE:-http://localhost:8085/centipede}"
	CP_USER="${CP_USER:-default}"
	CP_PASS="${CP_PASS:-default}"
	HB_SOURCE_GROUP="${HB_SOURCE_GROUP:-cpbos-matrix}"
	MIGRATION_PREFIX="${MIGRATION_PREFIX:-cpbos}"
	KEEP_MIGRATION="${KEEP_MIGRATION:-1}"
	# POLL_INTERVAL is centipede's. beetle's async waits use BUCKET_POLL_INTERVAL:
	# a bucket appears in seconds and a migration settles in seconds too, so the
	# two are close here — unlike the managed-DB matrix, where they are minutes apart.
	POLL_INTERVAL="${POLL_INTERVAL:-5}"
	MIGRATION_TIMEOUT="${MIGRATION_TIMEOUT:-1800}"
	VALIDATION_TIMEOUT="${VALIDATION_TIMEOUT:-600}"

	# The CSPs and the source buckets — the two axes of the matrix.
	#
	# ${VAR-default}, not ${VAR:-default}: an axis someone deliberately emptied has
	# to stay empty so the check below can say so. Substituting the default there
	# would answer "OS_CSPS=" by running every CSP, which is the opposite of what
	# was asked. An unset value still takes the default, so commenting the key out
	# behaves as it reads.
	OS_CSPS="${OS_CSPS-aws ncp}"
	OS_SRC_BUCKETS="${OS_SRC_BUCKETS-raw-data processed-data images documents backups logs}"

	# The source container
	HOST_IP="${HOST_IP:-127.0.0.1}"
	MINIO_SRC_API_PORT="${MINIO_SRC_API_PORT:-33900}"
	MINIO_SRC_CONSOLE_PORT="${MINIO_SRC_CONSOLE_PORT:-33901}"
	MINIO_SRC_SSH_PORT="${MINIO_SRC_SSH_PORT:-33922}"
	MINIO_ROOT_USER="${MINIO_ROOT_USER:-minioadmin}"
	MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-minioadmin123}"
	MINIO_VERSION="${MINIO_VERSION:-}"

	ONLY_CSPS="${ONLY_CSPS:-}"
	ONLY_BUCKETS="${ONLY_BUCKETS:-}"
	ONLY_CELLS="${ONLY_CELLS:-}"
	CLEANUP_ONLY="${CLEANUP_ONLY:-0}"
	STOP_ON_FAIL="${STOP_ON_FAIL:-0}"
	KEEP_ON_FAIL="${KEEP_ON_FAIL:-0}"
	KEEP_BUCKET="${KEEP_BUCKET:-0}"
	KEEP_SOURCE="${KEEP_SOURCE:-0}"
	KEEP_NAMESPACE="${KEEP_NAMESPACE:-0}"
	SRC_BUILD_TIMEOUT="${SRC_BUILD_TIMEOUT:-900}"
	READY_TIMEOUT="${READY_TIMEOUT:-300}"
	# Creating a bucket, and how often the async request is polled.
	BUCKET_TIMEOUT="${BUCKET_TIMEOUT:-300}"
	BUCKET_POLL_INTERVAL="${BUCKET_POLL_INTERVAL:-5}"
	# cb-tumblebug answers "deletion unconfirmed" while the CSP is still releasing
	# a bucket. 30s rather than the DB matrix's 60s: a bucket is not an instance.
	DELETE_RETRIES="${DELETE_RETRIES:-3}"
	DELETE_RETRY_WAIT="${DELETE_RETRY_WAIT:-30}"
	NO_PAUSE="${NO_PAUSE:-1}"
	NO_LOG="${NO_LOG:-0}"
	ENV_FILE="${ENV_FILE:-$MATRIX_DIR/.env}"
}
