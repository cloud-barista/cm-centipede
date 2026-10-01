#!/usr/bin/env bash
#
# lib/fs-matrix.sh — the matrix runner
#
# scripts/fs-matrix.sh sources this and calls matrix_main.
#
#   run
#     ├ source ──────── one container, one dataset under /testdata
#     ├ collect ─────── one honeybee inspect, reused by every cell
#     └ csp ─────────── cell: network -> node -> probe -> plan -> migrate
#                             -> validate -> delete node -> release network
#
# ── One cell is one CSP ─────────────────────────────────────────────────────
# The object storage matrix has two axes because a bucket is cheap: it makes one
# per source bucket per CSP and throws each away in seconds. A node is neither
# cheap nor quick, and the whole dataset migrates as a single transfer, so there
# is nothing to put on a second axis. What is left is a row per CSP — and the
# cell body is the same six steps beetle-os-test uses, with "create the node"
# where "create the bucket" was.
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
#   SKIP — the cell was never reached, which today means --stop-on-fail ended the
#          run before its turn. NOT "filtered out with --csp": --csp narrows the
#          row list itself, so a CSP left out of it has no row at all.

if [ -n "${MATRIX_FS_MATRIX_SH:-}" ]; then return 0; fi
MATRIX_FS_MATRIX_SH=1

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
declare -A CELL_INFRA CELL_NODE CELL_USER CELL_IP CELL_DST

# The one source, collected once and read by every cell.
SRC_MODEL=""; SRC_SCANROOT=""; SRC_FILES=0; SRC_BYTES=0

# CSPs whose node / network are made and not yet released.
CREATED_NODES=()
CREATED_NETWORKS=()
MATRIX_FAILED=0
SOURCE_UP=0

HB_SG_ID=""
HB_CONN_ID=""

csps_of() { printf '%s' "${ONLY_CSPS:-$FS_CSPS}"; }

usage() {
	cat <<USAGE
Filesystem migration matrix — a container's /testdata -> a cm-beetle node

  $(basename "$0") [options]

  rows   = the CSPs in FS_CSPS
  a cell = the whole source dataset migrated to one node on one CSP

Options:
  --csp "C1 C2"      CSPs to run (default: FS_CSPS = ${FS_CSPS:-unset})
  --keep-node        keep every target node at the end   ⚠ a VM keeps billing
  --keep-on-fail     keep a failed cell's node for inspection   ⚠ same
  --keep-network     keep the vNet, security group and SSH key
  --keep-source      keep the source container running
  --stop-on-fail     stop at the first FAIL
  --cleanup          do not run the matrix; reclaim every node and network this
                     matrix left in the cb-tumblebug namespace, then the namespace
  -h, --help         this help

⚠ Requires cm-honeybee, cm-centipede, cm-beetle and cb-tumblebug to be running,
  with the <csp>-<region> connection registered. Checked before anything is made.

⚠ Cost: a cell creates a real VM. They are removed when the run ends or is
  interrupted with Ctrl-C; --keep-node leaves them running.

Options that do not exist, and why:
  --dirs           the whole dataset migrates as one transfer. A per-directory
                   axis would mean one node per directory, which is a lot of VMs
                   to learn the same thing.
  --dst-path       the destination is the node's own \$HOME plus a directory,
                   read off the node itself. FS_DST_PATH in .env overrides it.
  --strategy       centipede's default is relay. agent-forward is a statement
                   about how centipede is deployed, not about this test.
  --scope/--async  centipede is always full scope and always asynchronous.
USAGE
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--csp)           ONLY_CSPS="$2"; shift 2 ;;
		--keep-node)     KEEP_NODE=1; shift ;;
		--keep-on-fail)  KEEP_ON_FAIL=1; shift ;;
		--keep-network)  KEEP_NETWORK=1; shift ;;
		--keep-source)   KEEP_SOURCE=1; shift ;;
		--stop-on-fail)  STOP_ON_FAIL=1; shift ;;
		--cleanup)       CLEANUP_ONLY=1; shift ;;
		-h|--help)       usage; exit 0 ;;
		--dirs|--dst-path|--strategy|--scope|--async)
			die "$1 does not exist in this folder. See --help for why." ;;
		*) echo "unknown option: $1" >&2; usage; exit 1 ;;
		esac
	done
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

# release_node CSP — drop it from the created list and delete it.
release_node() {
	local csp="$1" i
	delete_node "$csp" || true
	for i in "${!CREATED_NODES[@]}"; do
		[ "${CREATED_NODES[$i]}" = "$csp" ] && unset 'CREATED_NODES[i]'
	done
	CREATED_NODES=("${CREATED_NODES[@]}")
}

release_network_tracked() {
	local csp="$1" i
	release_network "$csp" || true
	for i in "${!CREATED_NETWORKS[@]}"; do
		[ "${CREATED_NETWORKS[$i]}" = "$csp" ] && unset 'CREATED_NETWORKS[i]'
	done
	CREATED_NETWORKS=("${CREATED_NETWORKS[@]}")
}

# cleanup_cell CSP RC — tidy up after a cell.
cleanup_cell() {
	local csp="$1" rc="$2" keep=0

	cp_delete_migration

	if [ "${KEEP_NODE:-0}" = "1" ]; then
		keep=1
		warn "KEEP_NODE=1 — keeping the target node: $(infra_name "$csp")"
	elif [ "$rc" -ne 0 ] && [ "${KEEP_ON_FAIL:-0}" = "1" ]; then
		keep=1
		warn "KEEP_ON_FAIL=1 — keeping the failed cell's node: $(infra_name "$csp")"
		warn "  It holds whatever the transfer had already written. A filesystem migration"
		warn "  has no rollback, so this is partial data, not an empty directory."
	fi

	if [ "$keep" -eq 1 ]; then
		warn "  ⚠ It is a running VM and it is billing. Reclaim it: ./scripts/fs-matrix.sh --cleanup"
		[ -n "${NODE_PUBLIC_IP:-}" ] && [ -n "${NODE_USER:-}" ] && \
			warn "  Look at what landed: ssh $NODE_USER@$NODE_PUBLIC_IP 'ls -R ${CELL_DST[$csp]:-}'"
		return 0
	fi

	release_node "$csp"
	if [ "${KEEP_NETWORK:-0}" = "1" ]; then
		warn "KEEP_NETWORK=1 — keeping the vNet, security group and SSH key for $csp."
	else
		release_network_tracked "$csp"
	fi
}

# run_cell CSP
run_cell() {
	local csp="$1" key="$1"
	local started ended elapsed dst mig_name

	# Clear what the previous cell left. A cell that ends at the plan step never
	# calls cp_migrate, and cleanup_cell would then delete the previous cell's
	# migration record.
	CP_MIGRATION_ID=""; CP_STATUS=""; CP_VALIDATION=""; CP_ERROR=""

	banner "cell [$(upper "$csp")]  $(src_path) -> a cm-beetle node   (source honeybee/ssh, target beetleSsh)"
	started="$(date +%s)"
	info "source: $SRC_SCANROOT   $SRC_FILES file(s), $SRC_BYTES bytes"

	# ── 0) Network ──────────────────────────────────────────────────────────
	# Before the node, because the recommendation does not fill the network in and
	# the create resolves the ids we inject.
	sub "0) network — vNet, security group, SSH key"
	if ! ensure_network "$csp"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "the network could not be prepared"
		cleanup_cell "$csp" 1
		return
	fi
	CREATED_NETWORKS+=("$csp")

	# ── 1) Node ─────────────────────────────────────────────────────────────
	# centipede creates nothing of its own — MigrateFileSystem resolves both ends
	# and hands them to transx-ex — so this comes first.
	sub "1) target node — cm-beetle"
	if ! create_node "$csp"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "the target node could not be created"
		cleanup_cell "$csp" 1
		return
	fi
	CREATED_NODES+=("$csp")
	CELL_INFRA["$key"]="$(infra_name "$csp")"
	CELL_NODE["$key"]="$NODE_ID"

	node_wait_ssh "$csp"

	# ── 2) Probe ────────────────────────────────────────────────────────────
	sub "2) node probe — home directory, rsync, and the key beetle hands out"
	if ! node_probe "$csp"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "the node is not usable as a migration target"
		cleanup_cell "$csp" 1
		return
	fi
	CELL_USER["$key"]="$NODE_USER"
	CELL_IP["$key"]="$NODE_PUBLIC_IP"
	dst="$(node_dst_path)"
	CELL_DST["$key"]="$dst"
	info "destination: $dst"

	# ── 3) Plan ─────────────────────────────────────────────────────────────
	sub "3) target plan — POST /plans/target"
	if ! cp_plan "$SRC_MODEL" "$(infra_name "$csp")" "$NODE_ID" "$SRC_SCANROOT" "$dst"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "plan failed - $CP_ERROR"
		cleanup_cell "$csp" 1
		return
	fi
	cp_plan_summary

	# ── 4) Migration ────────────────────────────────────────────────────────
	mig_name="${MIGRATION_PREFIX:-cpbfs}-${csp}-$(date '+%Y%m%d-%H%M%S')"
	sub "4) migration — POST /migration ($mig_name)"
	if ! cp_migrate "$mig_name"; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		finish_cell "$key" FAIL "$elapsed" "migration could not be created - $CP_ERROR"
		cleanup_cell "$csp" 1
		return
	fi
	CELL_MIGID["$key"]="$CP_MIGRATION_ID"
	info "MIGRATION_ID=$CP_MIGRATION_ID"

	if ! cp_poll; then
		ended="$(date +%s)"; elapsed=$((ended - started))
		# The migration never finished, so validation is not reached and step 6
		# never runs — the log is read here instead, where it says what broke.
		sub "5) migration log — GET /migration/$CP_MIGRATION_ID/logs"
		cp_migration_logs
		finish_cell "$key" FAIL "$elapsed" "[$CP_STATUS] $CP_ERROR"
		cleanup_cell "$csp" 1
		return
	fi

	# ── 5) Validation ───────────────────────────────────────────────────────
	sub "5) validation — POST /migration/$CP_MIGRATION_ID/validation"
	cp_validate || true
	CELL_VALID["$key"]="$CP_VALIDATION"
	cp_validation_summary

	# ── 6) Migration log ────────────────────────────────────────────────────
	sub "6) migration log — GET /migration/$CP_MIGRATION_ID/logs"
	cp_migration_logs

	ended="$(date +%s)"; elapsed=$((ended - started))
	if [ "$CP_VALIDATION" = "passed" ]; then
		finish_cell "$key" PASS "$elapsed" "validation passed ($SRC_FILES files)"
		cleanup_cell "$csp" 0
	else
		finish_cell "$key" FAIL "$elapsed" "validation $CP_VALIDATION - $CP_ERROR"
		cleanup_cell "$csp" 1
	fi
}

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------
cell_color() {
	case "${CELL_STATUS[$1]:-SKIP}" in
	PASS) printf '%b' "$C_OK" ;;
	FAIL) printf '%b' "$C_ERR" ;;
	*)    printf '' ;;
	esac
}

print_matrix() {
	local csp line pass=0 failed=0 skipped=0 st

	banner "Filesystem matrix result — $(src_path) -> a cm-beetle node  (beetle + honeybee + centipede)"

	sub "one row per CSP; the whole dataset is one cell"
	info "source: $SRC_SCANROOT   $SRC_FILES file(s), $SRC_BYTES bytes"
	echo
	line="$(printf '  %-8s │ %-7s %-10s %-12s %-9s %s' 'csp' 'status' 'files' 'bytes' 'elapsed' 'destination')"
	echo -e "${C_HDR}$line${C_OFF}"
	echo "  ---------┼---------------------------------------------------------------------"
	for csp in $(csps_of); do
		st="${CELL_STATUS[$csp]:-SKIP}"
		case "$st" in
		PASS) pass=$((pass + 1)) ;;
		FAIL) failed=$((failed + 1)) ;;
		*)    skipped=$((skipped + 1)) ;;
		esac
		printf '  %-8s │ %b%-7s%b %-10s %-12s %-9s %s\n' \
			"$csp" "$(cell_color "$csp")" "$st" "$C_OFF" \
			"$([ "$st" = "PASS" ] && printf '%s' "$SRC_FILES" || printf '-')" \
			"$([ "$st" = "PASS" ] && printf '%s' "$SRC_BYTES" || printf '-')" \
			"$(secs_fmt "${CELL_ELAPSED[$csp]:-0}")" \
			"${CELL_DST[$csp]:-}"
	done

	echo
	echo -e "  ${C_OK}PASS $pass${C_OFF} / ${C_ERR}FAIL $failed${C_OFF} / SKIP $skipped" \
		"  $((pass + failed + skipped)) cells   elapsed $(secs_fmt "$(( $(date +%s) - RUN_STARTED ))")"

	sub "cell detail"
	for csp in $(csps_of); do
		[ -n "${CELL_STATUS[$csp]:-}" ] || continue
		printf '    %-8s %-5s  %s\n' "$csp" "${CELL_STATUS[$csp]}" "${CELL_DETAIL[$csp]:-}"
	done

	MATRIX_FAILED="$failed"
}

write_result_json() {
	local csp csps="[]"
	for csp in $(csps_of); do
		csps="$(jq -c \
			--arg c "$csp" --arg r "$(csp_env "$csp" REGION)" \
			--arg st "${CELL_STATUS[$csp]:-SKIP}" \
			--arg de "${CELL_DETAIL[$csp]:-}" \
			--arg infra "${CELL_INFRA[$csp]:-}" \
			--arg node "${CELL_NODE[$csp]:-}" \
			--arg user "${CELL_USER[$csp]:-}" \
			--arg ip "${CELL_IP[$csp]:-}" \
			--arg dst "${CELL_DST[$csp]:-}" \
			--arg mid "${CELL_MIGID[$csp]:-}" \
			--arg val "${CELL_VALID[$csp]:-}" \
			--argjson el "${CELL_ELAPSED[$csp]:-0}" \
			'. + [{csp:$c, region:$r, status:$st, elapsedSec:$el,
			       infraId:(if $infra == "" then null else $infra end),
			       nodeId:(if $node == "" then null else $node end),
			       nodeUserName:(if $user == "" then null else $user end),
			       publicIP:(if $ip == "" then null else $ip end),
			       dstPath:(if $dst == "" then null else $dst end),
			       migrationId:$mid, validationStatus:$val, detail:$de}]' <<<"$csps")"
	done
	jq -n --arg prefix "$MATRIX_NAME_PREFIX" --arg ns "$MATRIX_NS" \
		--arg root "$SRC_SCANROOT" --argjson files "${SRC_FILES:-0}" --argjson bytes "${SRC_BYTES:-0}" \
		--arg ts "$(date '+%F %T')" --argjson c "$csps" \
		'{namePrefix:$prefix, nsId:$ns, provisioner:"cm-beetle",
		  migrator:"cm-centipede", collector:"cm-honeybee",
		  srcType:"filesystem", dstType:"beetleSsh",
		  srcScanRoot:$root, srcFileCount:$files, srcSizeBytes:$bytes,
		  finishedAt:$ts, csps:$c}' \
		> "$RESULT_FILE"
	info "result JSON: $RESULT_FILE"
}

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
# cleanup_exit — however the run ends, no node is left behind unless it was asked
#   for. This matters more than it did for a bucket: a node left running bills
#   until someone notices, and Ctrl-C during a five-minute VM create is the normal
#   way to abandon a run.
#
#   The namespace is NOT deleted here. It is free to keep, the next run reuses it,
#   and --cleanup is where removing it belongs.
cleanup_exit() {
	local csp
	trap - EXIT INT TERM

	if [ "${#CREATED_NODES[@]}" -gt 0 ]; then
		if [ "${KEEP_NODE:-0}" = "1" ]; then
			warn "KEEP_NODE=1 — keeping the target nodes:"
			for csp in "${CREATED_NODES[@]}"; do
				warn "  $(infra_name "$csp")  (namespace $MATRIX_NS)   ⚠ billing"
			done
			warn "  Reclaim: ./scripts/fs-matrix.sh --cleanup"
		else
			sub "removing the target nodes left"
			for csp in "${CREATED_NODES[@]}"; do
				delete_node "$csp" || true
			done
			CREATED_NODES=()
		fi
	fi

	if [ "${#CREATED_NETWORKS[@]}" -gt 0 ] && [ "${KEEP_NETWORK:-0}" != "1" ] \
	   && [ "${KEEP_NODE:-0}" != "1" ]; then
		sub "releasing the networks left"
		for csp in "${CREATED_NETWORKS[@]}"; do
			release_network "$csp" || true
		done
		CREATED_NETWORKS=()
	fi

	if [ "$SOURCE_UP" = "1" ] && [ "${KEEP_SOURCE:-0}" != "1" ]; then
		src_stop
	elif [ "$SOURCE_UP" = "1" ]; then
		warn "KEEP_SOURCE=1 — the source container is still running: $(src_container)"
		warn "  Stop it with: docker rm -f $(src_container)"
		warn "  ⚠ The run's SSH key pair lives in $MATRIX_TMP, which is about to be removed,"
		warn "    so the container stays up but nothing here can log in to it any more."
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

	LOG_DIR="${LOG_DIR:-$MATRIX_DIR/logs}"
	LOG_NAME="fs-matrix"
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

	MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cpbfsmatrix.XXXXXX")"
	chmod 700 "$MATRIX_TMP"
	RUN_STARTED="$(date +%s)"
	trap 'cleanup_exit' EXIT INT TERM

	require_cmd docker jq curl ssh ssh-keygen
	docker info >/dev/null 2>&1 || die "the docker daemon is not running."

	local csps csp

	# HOST_IP is worked out rather than configured. Done here because --cleanup
	# needs none of it and should not fail for want of a docker bridge.
	if [ "${CLEANUP_ONLY:-0}" != "1" ] && [ -z "${HOST_IP:-}" ]; then
		HOST_IP="$(detect_host_ip)" || \
			warn "could not read the docker bridge gateway — falling back to $HOST_IP."
	fi

	# --cleanup — do not run the matrix; only reclaim what is left.
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
	[ -n "${csps// }" ] || die "FS_CSPS is empty. Name the CSPs to run, or comment the key out to take the default."

	banner "0. pre-flight — filesystem migration matrix"
	info "name prefix : $MATRIX_NAME_PREFIX   namespace: $MATRIX_NS"
	info "provision   : beetle $BEETLE_URL   tumblebug $TUMBLEBUG_URL"
	info "collect/move: honeybee $HB_BASE   centipede $CP_BASE"
	info "CSPs        : $csps"
	for csp in $csps; do
		info "  $csp — region $(csp_env "$csp" REGION)  zones $(csp_env "$csp" ZONE)/$(csp_env "$csp" ZONE2)  connection $(connection_name "$csp")"
	done
	info "source      : container $(src_container), dataset $(src_path)"
	info "  reached by honeybee and centipede at $(src_endpoint) as $SRC_SSH_USER"
	info "  HOST_IP $(src_host)$([ -z "${HOST_IP_FROM_ENV:-}" ] && printf ' (detected: docker bridge gateway)')"
	info "target      : one node per CSP, deleted with the cell"
	info "  destination is read off the node itself (\$HOME/${FS_DST_DIRNAME:-testdata})${FS_DST_PATH:+, overridden by FS_DST_PATH: $FS_DST_PATH}"

	local missing=""
	for csp in $csps; do
		[ -n "$(csp_env "$csp" REGION)" ]    || missing="$missing $(upper "$csp")_REGION"
		[ -n "$(csp_env "$csp" ZONE)" ]      || missing="$missing $(upper "$csp")_ZONE"
		[ -n "$(csp_env "$csp" ZONE2)" ]     || missing="$missing $(upper "$csp")_ZONE2"
		[ -n "$(csp_env "$csp" VNET_CIDR)" ] || missing="$missing $(upper "$csp")_VNET_CIDR"
	done
	[ -n "$MATRIX_NS" ] || missing="$missing MATRIX_NS"
	[ -n "$missing" ] && die "these settings are empty:$missing
       Fill them in in $ENV_FILE."

	if [ -f "$ENV_FILE" ]; then
		local mode=""
		mode="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || true)"
		case "$mode" in
		''|600|400) ;;
		*) warn "$ENV_FILE is mode $mode — chmod 600 is advised." ;;
		esac
	fi

	# The migration stack is checked first. Learning that honeybee is not running
	# should never cost a created VM.
	sub "1) migration stack — cm-honeybee / cm-centipede"
	hb_preflight || die "honeybee check failed. Nothing was created."
	cp_preflight || die "centipede check failed. Nothing was created."

	sub "2) provisioner — cm-beetle / cb-tumblebug"
	beetle_preflight
	local conn_error=0
	for csp in $csps; do
		assert_connection "$csp" || conn_error=1
		assert_vm_zone "$csp"    || conn_error=1
	done
	[ "$conn_error" -eq 0 ] || die "connection check failed. Nothing was created."
	ensure_namespace || die "could not prepare the namespace. Nothing was created."

	sub "3) source — one container for the whole run"
	src_keygen || die "could not prepare the source key pair."
	local img
	img="$(src_image)" || die "source image build failed."
	src_start "$img" || die "source container failed to start."
	SOURCE_UP=1
	src_wait_ready || die "source initialisation failed."
	assert_src_dataset || die "the source dataset is unusable. Nothing was created."

	local stats
	stats="$(src_stats)"
	SRC_FILES="${stats%% *}"; SRC_BYTES="${stats##* }"

	sub "4) collecting the source — cm-honeybee (once for the whole run)"
	HB_SG_ID="$(hb_source_group "${HB_SOURCE_GROUP:-cpbfs-matrix}")" \
		|| die "could not obtain the honeybee SourceGroup. Nothing was created."
	info "honeybee SourceGroup: ${HB_SOURCE_GROUP:-cpbfs-matrix} ($HB_SG_ID)  type=fs"

	# Not a command substitution: hb_connection reports through globals, and a
	# subshell would swallow the four status variables hb_assert_connection reads.
	hb_connection "$HB_SG_ID" \
		|| die "could not register the source connection. Nothing was created."
	hb_assert_connection || die "the source connection is not usable. Nothing was created."

	hb_import "$HB_SG_ID" "$HB_CONN_ID" || die "honeybee could not inspect the source. Nothing was created."
	SRC_MODEL="$(hb_source_model "$HB_SG_ID" "$HB_CONN_ID")" \
		|| die "honeybee's collection result could not be read. Nothing was created."
	SRC_SCANROOT="$(hb_scan_root "$SRC_MODEL")"
	[ -n "$SRC_SCANROOT" ] || die "honeybee reported no scan root, so the plan would match nothing."
	info "  scan root $SRC_SCANROOT   $(hb_folder_count "$SRC_MODEL") folder(s) listed"
	info "  $SRC_FILES file(s), $SRC_BYTES bytes  (counted on the source, not by the inspect)"

	# ── cells ───────────────────────────────────────────────────────────────
	for csp in $csps; do
		run_cell "$csp"
		if [ "${STOP_ON_FAIL:-0}" = "1" ] && [ "${CELL_STATUS[$csp]}" = "FAIL" ]; then
			warn "STOP_ON_FAIL=1 — stopping at the first failure."
			break
		fi
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
	MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cpbfs}"
	MATRIX_NS="${MATRIX_NS:-cpbfs01}"

	BEETLE_URL="${BEETLE_URL:-http://localhost:8056/beetle}"
	BEETLE_USERNAME="${BEETLE_USERNAME:-default}"
	BEETLE_PASSWORD="${BEETLE_PASSWORD:-default}"
	TUMBLEBUG_URL="${TUMBLEBUG_URL:-http://localhost:1323/tumblebug}"
	TUMBLEBUG_USERNAME="${TUMBLEBUG_USERNAME:-default}"
	TUMBLEBUG_PASSWORD="${TUMBLEBUG_PASSWORD:-default}"

	HB_BASE="${HB_BASE:-http://localhost:8081/honeybee}"
	CP_BASE="${CP_BASE:-http://localhost:8085/centipede}"
	CP_USER="${CP_USER:-default}"
	CP_PASS="${CP_PASS:-default}"
	HB_SOURCE_GROUP="${HB_SOURCE_GROUP:-cpbfs-matrix}"
	# Registering a connection installs the agent, which downloads a binary and
	# then polls for it — minutes, not seconds, on a first run.
	HB_TIMEOUT="${HB_TIMEOUT:-600}"
	MIGRATION_PREFIX="${MIGRATION_PREFIX:-cpbfs}"
	KEEP_MIGRATION="${KEEP_MIGRATION:-1}"
	POLL_INTERVAL="${POLL_INTERVAL:-5}"
	MIGRATION_TIMEOUT="${MIGRATION_TIMEOUT:-1800}"
	VALIDATION_TIMEOUT="${VALIDATION_TIMEOUT:-600}"

	# The one axis. ${VAR-default}, not ${VAR:-default}: an axis someone
	# deliberately emptied has to stay empty so the check can say so.
	FS_CSPS="${FS_CSPS-aws ncp}"

	# The source container
	FS_SRC_SSH_PORT="${FS_SRC_SSH_PORT:-34922}"
	FS_SRC_PATH="${FS_SRC_PATH:-/testdata}"
	FS_KEY_TYPE="${FS_KEY_TYPE:-ed25519}"
	FS_SCAN_MAX_DEPTH="${FS_SCAN_MAX_DEPTH:-0}"
	# HOST_IP is deliberately NOT defaulted here. Empty means "work it out", which
	# detect_host_ip does; a value from .env or the shell is an override.
	[ -n "${HOST_IP:-}" ] && HOST_IP_FROM_ENV=1

	# The destination on the node. Empty FS_DST_PATH means "$HOME/<dirname>",
	# resolved per node by node_dst_path.
	FS_DST_PATH="${FS_DST_PATH:-}"
	FS_DST_DIRNAME="${FS_DST_DIRNAME:-testdata}"

	FS_ALLOWED_CIDR="${FS_ALLOWED_CIDR:-0.0.0.0/0}"

	ONLY_CSPS="${ONLY_CSPS:-}"
	CLEANUP_ONLY="${CLEANUP_ONLY:-0}"
	STOP_ON_FAIL="${STOP_ON_FAIL:-0}"
	KEEP_ON_FAIL="${KEEP_ON_FAIL:-0}"
	KEEP_NODE="${KEEP_NODE:-0}"
	KEEP_NETWORK="${KEEP_NETWORK:-0}"
	KEEP_SOURCE="${KEEP_SOURCE:-0}"
	KEEP_NAMESPACE="${KEEP_NAMESPACE:-0}"
	FS_REBUILD_SOURCE="${FS_REBUILD_SOURCE:-0}"
	SRC_BUILD_TIMEOUT="${SRC_BUILD_TIMEOUT:-900}"
	READY_TIMEOUT="${READY_TIMEOUT:-300}"
	# A node, unlike a bucket, is built in minutes.
	NODE_TIMEOUT="${NODE_TIMEOUT:-1800}"
	NODE_POLL_INTERVAL="${NODE_POLL_INTERVAL:-10}"
	SSH_READY_TIMEOUT="${SSH_READY_TIMEOUT:-900}"
	NODE_PROBE_RETRIES="${NODE_PROBE_RETRIES:-6}"
	DELETE_RETRIES="${DELETE_RETRIES:-3}"
	DELETE_RETRY_WAIT="${DELETE_RETRY_WAIT:-30}"
	NO_PAUSE="${NO_PAUSE:-1}"
	NO_LOG="${NO_LOG:-0}"
	ENV_FILE="${ENV_FILE:-$MATRIX_DIR/.env}"
}
