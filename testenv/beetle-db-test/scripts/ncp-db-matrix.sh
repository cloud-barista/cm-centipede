#!/usr/bin/env bash
#
# ncp-db-matrix.sh — NCP managed-RDBMS version matrix
#
# The same matrix as aws-db-matrix.sh, with one addition: the public domain, in
# the csp_after_instance_ready hook below. The network is not a difference here —
# on the beetle path both CSPs get a vNet, two subnets and a security group.
#
# ⚠ Engines: mysql only. NCP has no managed MariaDB at all, and its managed
#   PostgreSQL and MongoDB exist but cm-beetle cannot ask for them yet
#   (BEETLE_ENGINES_NCP in lib/beetle.sh). mariadb columns run on AWS.
#
# ⚠ Versions: write them in full (8.0.36, not 8.0). NCP's catalogue lists full
#   strings, and beetle resolves a prefix through selectEngineVersion — which
#   works, but leaves which minor you got up to the catalogue's order rather than
#   to you.
#
# ⚠ Time: about 30 minutes per instance, plus one console visit. Three target
#   versions is an afternoon.
#
# Usage:
#   ./scripts/ncp-db-matrix.sh
#   ./scripts/ncp-db-matrix.sh --dst-versions "8.4.8"
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
	info "current address: ${RDBMS_ENDPOINT:-(none)}   publicAccess=${RDBMS_PUBLIC:-?}"
	info "CSP resource  : $(rdbms_csp_label)"
	info ""
	info "Request the public domain in the NCP console:"
	info "  Database > Cloud DB for ${engine} > select the DB server"
	info "    > DB Management > Public Domain Management > Request"

	wait_enter "once the console request is done"

	# Read it again after Enter. A single-resource read is also the refresh:
	# cb-tumblebug asks cb-spider and overwrites endpoint / publicAccess with the
	# live answer before storing it again. Without that the cell would keep using
	# the private domain.
	if ! rdbms_info "$CSP" "$RDBMS_NAME"; then
		bt_report "could not read the instance back: $RDBMS_NAME"
		return 1
	fi

	if [ "$RDBMS_PUBLIC" = "true" ]; then
		ok "public domain confirmed: $RDBMS_ENDPOINT"
		return 0
	fi

	# Not a stop. publicAccess is an observation - cb-spider sets it from whether
	# the instance actually has a public domain (ncp/resources/RDBMSHandler.go) -
	# but the record may simply not have caught up. Whether the address is reachable
	# is decided by the check that follows, which is the question that matters.
	warn "publicAccess is still false (${RDBMS_ENDPOINT:-no address}) — moving on to the reachability check."
	warn "  If it times out, the console request has not taken effect: the address above is"
	warn "  the private domain, which cannot be reached from outside the VPC. Finish the"
	warn "  request and run again — the instance is reused under the same name."
	return 0
}

matrix_main "$@"
