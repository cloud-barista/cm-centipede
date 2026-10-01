#!/usr/bin/env bash
#
# os-support.sh — what cm-beetle can hold a managed bucket on, and where.
#
# Read-only. Creates nothing, deletes nothing, and is safe to run at any time.
# Three questions, answered in the order you hit them when a run refuses to start:
#
#   1) which CSPs does cm-beetle offer object storage on
#   2) is this folder's OS_CSPS inside that set
#   3) is each of those CSPs' <csp>-<region> connection actually registered
#
# The managed-DB matrix keeps a hand-maintained BEETLE_ENGINES_<CSP> table for
# the same purpose, because cb-tumblebug's capability endpoint pins dbEngine to
# mysql and cannot be asked. Object storage has an endpoint that answers, so
# there is nothing here to keep in step — this script only prints it.
#
# Usage:
#   ./scripts/os-support.sh
#   ./scripts/os-support.sh --json

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=./lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
load_env_file "$ENV_FILE"

AS_JSON=0
case "${1:-}" in
--json) AS_JSON=1 ;;
-h|--help)
	sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'
	exit 0 ;;
"") ;;
*) echo "unknown option: $1" >&2; exit 1 ;;
esac

# shellcheck source=./lib/beetle.sh
. "$SCRIPT_DIR/lib/beetle.sh"

MATRIX_NS="${MATRIX_NS:-cpbos01}"
MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cpbos}"
OS_CSPS="${OS_CSPS:-aws ncp}"
MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cpbossupport.XXXXXX")"
trap 'rm -rf "$MATRIX_TMP"' EXIT INT TERM

require_cmd jq curl

if [ "$AS_JSON" -eq 1 ]; then
	bt_get "/recommendation/middleware/objectStorage/support" \
		|| die "could not read the object storage support matrix."
	bt_payload | jq '.'
	exit 0
fi

banner "object storage support — cm-beetle"
beetle_preflight

available="$(os_supported_csps)" || die "could not read the object storage support matrix."

sub "CSPs cm-beetle offers object storage on"
if [ -z "$available" ]; then
	warn "none reported. Check that cb-tumblebug and cb-spider are up and the catalogue has loaded."
else
	# The per-CSP values are cb-tumblebug's bucket-level feature flags: whether it
	# can set CORS, whether it can turn versioning on, whether it can sign a
	# presigned URL. This matrix uses none of them — it creates a plain bucket and
	# moves objects into it — so they are shown as context, not as a gate.
	bt_get "/recommendation/middleware/objectStorage/support" >/dev/null 2>&1
	bt_jq -r '(.supports // {}) | to_entries[]
		| "  \(.key)\(" " * (12 - (.key|length)))cors=\(.value.cors)  versioning=\(.value.versioning)  presignedUrl=\(.value.presignedUrl)"'
fi

sub "this folder's OS_CSPS"
info "OS_CSPS = $OS_CSPS"
bad=0
for csp in $OS_CSPS; do
	if in_list "$csp" "$available"; then
		ok "  $csp — supported"
	else
		fail "  $csp — cm-beetle cannot create object storage here"
		bad=1
	fi
done
[ "$bad" -eq 1 ] && fail "Fix OS_CSPS in $ENV_FILE."

sub "connections"
for csp in $OS_CSPS; do
	region="$(csp_env "$csp" REGION)"
	if [ -z "$region" ]; then
		fail "  $csp — $(upper "$csp")_REGION is empty in $ENV_FILE"
		bad=1
		continue
	fi
	if tb_get "/connConfig/$(urlq "$(connection_name "$csp")")" >/dev/null 2>&1; then
		ok "  $(connection_name "$csp") — registered"
	else
		fail "  $(connection_name "$csp") — not registered"
		fail "    Credentials are registered by the deployments stack: cd ../.. && make init"
		bad=1
	fi
done

sub "buckets this matrix currently holds in namespace $MATRIX_NS"
if bt_get "$(_os_path)"; then
	rows="$(bt_jq -r --arg p "$MATRIX_NAME_PREFIX" \
		'.objectStorage[]? | select((.id // .name) | startswith($p))
		 | "  \(.id // .name)  [\(.status // "?")]  csp=\(.cspResourceName // .uid // "?")"')"
	if [ -n "$rows" ]; then
		printf '%s\n' "$rows"
		info ""
		info "Reclaim them with: ./scripts/os-matrix.sh --cleanup"
	else
		info "  none"
	fi
else
	warn "  the namespace does not exist yet, or the list could not be read"
fi

echo
[ "$bad" -eq 0 ] || exit 1
exit 0
