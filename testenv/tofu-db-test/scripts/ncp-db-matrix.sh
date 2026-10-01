#!/usr/bin/env bash
#
# ncp-db-matrix.sh — NCP managed-RDBMS version matrix
#
# The same matrix as aws-db-matrix.sh, with two additions.
#
#   1) Network — NCP has no default VPC, so tofu/ncp/network creates a VPC and a
#      PUBLIC subnet. The subnet has to be PUBLIC for a public domain to be
#      requestable.
#   2) Public domain — the csp_after_instance_ready hook below.
#
# ⚠ Engines: mysql, postgresql, mongodb. NCP has no managed MariaDB, so mariadb
#   columns run on AWS only.
#
# ⚠ Versions are full strings (8.0.36, not 8.0). The provider normalises state
#   to the version the API reports and engine_version_code forces replacement,
#   so a partial value schedules another 30-minute re-creation on every later
#   plan. tofu/ncp/rdbms catches it at plan time.
#
# ⚠ Time: about 30 minutes per instance, plus one console visit. Three target
#   versions is an afternoon.
#
# Usage:
#   ./scripts/ncp-db-matrix.sh
#   ./scripts/ncp-db-matrix.sh --engines postgresql
#   ./scripts/ncp-db-matrix.sh --cleanup
#
# Settings live in .env at the root of this folder (never committed).

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=./lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
load_env_file "$ENV_FILE"

CSP="ncp"
CSP_TITLE="NCP"

# shellcheck source=./lib/db-matrix.sh
. "$SCRIPT_DIR/lib/db-matrix.sh"

# csp_after_instance_ready ENGINE DVER — called by db-matrix.sh once the
# instance is ready.
#
#   It does not check whether a public domain already exists; it always waits for
#   Enter. Deciding automatically is fine when the guess is right and
#   unrecoverable when it is wrong: the run would sail past the one moment a
#   human could act, and every cell in the column would then fail trying to reach
#   a private domain. Standing still until a person says they are done is better.
csp_after_instance_ready() {
	local engine="$1" dver="$2"

	sub "NCP public domain — waiting on the console step"
	info "instance      : $RDBMS_NAME  (status ${RDBMS_STATUS:-?})"
	info "current address: ${RDBMS_HOST:-(no public domain)}"
	info "private domain: ${RDBMS_PRIVATE_DOMAIN:-(unknown)}"
	info "PUBLIC subnet : ${RDBMS_PUBLIC_SUBNET:-?}   CSP resource: $(rdbms_csp_label)"
	info ""
	info "Request the public domain in the NCP console:"
	info "  Database > Cloud DB for ${engine} > select the DB server"
	info "    > DB Management > Public Domain Management > Request"

	wait_enter "once the console request is done"

	# Read it again after Enter. rdbms_info runs apply -refresh-only first on
	# NCP, so a domain issued in the console lands in state and shows up in the
	# outputs here. Without that refresh a cell would try the private domain and
	# fail. A failed read (not yet propagated) does not stop us - the verdict is
	# made on host.
	rdbms_info "$CSP" "$RDBMS_NAME" || true

	if [ -z "$RDBMS_HOST" ]; then
		# Stop here. Entering the reachability wait without an address spends
		# three minutes to reach the same conclusion.
		fail "there is still no public domain — this column cannot run."
		fail "  private domain: ${RDBMS_PRIVATE_DOMAIN:-(unknown)}  (unreachable outside the VPC)"
		fail "  PUBLIC subnet : ${RDBMS_PUBLIC_SUBNET:-?}  (a non-PUBLIC subnet cannot request one)"
		fail "  Finish the console request and run again. The instance stays in tofu state,"
		fail "  so the next run reuses it under the same name rather than creating another."
		return 1
	fi

	ok "public domain confirmed: $RDBMS_ENDPOINT"
	return 0
}

matrix_main "$@"
