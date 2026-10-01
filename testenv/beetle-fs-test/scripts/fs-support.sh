#!/usr/bin/env bash
#
# fs-support.sh — can this stack actually build a node where you are asking?
#
# Read-only. It creates nothing and deletes nothing, and it exists because every
# way a cell fails before the plan is a question that can be asked cheaply first:
#
#   - are the four servers up
#   - is the <csp>-<region> connection registered
#   - is the connection's zone the zone .env asks for
#   - does cb-tumblebug's catalogue hold a spec and an image for this profile
#   - what does cm-beetle actually recommend for it
#
# The last two are the ones worth having. A catalogue that has not finished
# loading answers a recommendation with 200 and an empty list, and the matrix can
# only report that after it has already built a vNet — here it costs one call.
#
# Usage:
#   ./scripts/fs-support.sh              every CSP in FS_CSPS
#   ./scripts/fs-support.sh aws          just this one
#   ./scripts/fs-support.sh aws --recommend   also ask for a recommendation

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=./lib/common.sh
. "$SCRIPT_DIR/lib/common.sh"

ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
load_env_file "$ENV_FILE"

# shellcheck source=./lib/fs-matrix.sh
. "$SCRIPT_DIR/lib/fs-matrix.sh"

matrix_defaults

WANT_CSPS=""
DO_RECOMMEND=0
while [ $# -gt 0 ]; do
	case "$1" in
	--recommend) DO_RECOMMEND=1; shift ;;
	-h|--help)
		sed -n '2,22p' "$0" | sed 's/^#\{1,2\} \{0,1\}//'
		exit 0 ;;
	-*) echo "unknown option: $1" >&2; exit 1 ;;
	*)  WANT_CSPS="${WANT_CSPS:+$WANT_CSPS }$1"; shift ;;
	esac
done
[ -n "$WANT_CSPS" ] || WANT_CSPS="$FS_CSPS"

require_cmd jq curl

# Read-only: no run log, no API log. Nothing here is worth reproducing from a
# curl line, and writing a run header for a status check makes the matrix's own
# log harder to read.
API_LOG_FILE=""

banner "beetle-fs-test — what this stack can build"
info "beetle $BEETLE_URL   tumblebug $TUMBLEBUG_URL"
info "honeybee $HB_BASE   centipede $CP_BASE"
info "namespace $MATRIX_NS   prefix $MATRIX_NAME_PREFIX"

RC=0

sub "servers"
beetle_preflight
hb_preflight || RC=1
cp_preflight || RC=1

for csp in $WANT_CSPS; do
	banner "$(upper "$csp")"
	region="$(csp_env "$csp" REGION)"
	if [ -z "$region" ]; then
		fail "$(upper "$csp")_REGION is not set — nothing else can be checked."
		RC=1
		continue
	fi
	info "region $region   zones $(csp_env "$csp" ZONE) / $(csp_env "$csp" ZONE2)"
	info "node profile: $(csp_env_int "$csp" NODE_VCPU 2) vCPU, $(csp_env_int "$csp" NODE_MEMORY_GB 4) GB, $(csp_env_int "$csp" NODE_DISK_GB 20) GB disk, $(csp_env "$csp" NODE_OS)"

	sub "connection"
	assert_connection "$csp" || { RC=1; continue; }
	assert_vm_zone "$csp"    || RC=1

	# What beetle would settle on for this node profile, asked one half at a time.
	#
	#   POST /recommendation/infra needs a compatible spec AND image and reports
	#   neither when it has neither, so "nothing was recommended" there does not say
	#   which half is missing. These two endpoints take the same source description
	#   and answer separately, which turns one dead end into a specific one.
	#
	#   Asked through cm-beetle rather than by listing cb-tumblebug's catalogue:
	#   beetle's search is what actually runs at provision time, and the catalogue
	#   listing endpoints move between tumblebug versions.
	candidates() {
		local kind="$1" path field body
		case "$kind" in
		spec)  path="/recommendation/resources/specs"    ; field="recommendedSpecList"    ;;
		image) path="/recommendation/resources/osImages" ; field="recommendedOsImageList" ;;
		esac
		body="$(infra_request_body "$csp")"
		if ! bt_post "${path}?desiredProvider=$(urlq "$csp")&desiredRegion=$(urlq "$region")" "$body"; then
			bt_report_warn "the $kind lookup failed"
			return 1
		fi
		# "none" is beetle's NothingRecommended — the search ran and matched
		# nothing. Not the same as an empty list, and it is the answer that stops a
		# node being built, so it gets the same treatment.
		local n none
		n="$(bt_payload | jq -r --arg f "$field" '[.[$f][]?] | length' 2>/dev/null)"
		none="$(bt_payload | jq -r --arg f "$field" '[.[$f][]? | select(.status == "none")] | length' 2>/dev/null)"
		if [ "${n:-0}" = "0" ] || [ "${none:-0}" != "0" ]; then
			fail "$kind : nothing recommended"
			return 1
		fi
		case "$kind" in
		spec)  bt_payload | jq -r '"  spec  : " + ((.recommendedSpecList[0].targetSpec // {})
		           | "\(.id // "-")  \(.vCPU // "?") vCPU, \(.memoryGiB // "?") GiB")' 2>/dev/null ;;
		image) bt_payload | jq -r '"  image : " + ((.recommendedOsImageList[0].targetOsImage // {})
		           | "\(.id // "-")  \(.osType // .name // "-")")' 2>/dev/null ;;
		esac
		return 0
	}

	sub "catalogue — what cm-beetle can find for this node profile"
	# The search key, so a "nothing recommended" answer can be read against what
	# was actually asked for. beetle builds it from the source node's OS id and
	# version, not from any pretty name.
	info "searching with osType \"$(infra_request_body "$csp" | jq -r '.onpremiseInfraModel.nodes[0].os | "\(.id) \(.versionId)"')\", architecture \"$(infra_request_body "$csp" | jq -r '.onpremiseInfraModel.nodes[0].cpu.architecture')\""
	candidates spec  || { RC=1; warn "  lower $(upper "$csp")_NODE_VCPU / _NODE_MEMORY_GB, or check the catalogue has loaded"; }
	candidates image || { RC=1; warn "  $(upper "$csp")_NODE_OS is an os id plus a version, not a pretty name — e.g. \"ubuntu 22.04\""; }

	if [ "$DO_RECOMMEND" = "1" ]; then
		sub "recommendation — POST /recommendation/infra"
		if rec="$(recommend_infra "$csp")"; then
			printf '%s' "$rec" | jq -r '.targetInfra.nodeGroups[0] |
				"  spec      \(.specId // "-")",
				"  image     \(.imageId // "-")",
				"  root disk \(if (.rootDiskSize // 0) == 0 then "(CSP default)" else "\(.rootDiskSize)GB" end)"' 2>/dev/null
			ok "cm-beetle can build a node here"
		else
			RC=1
		fi
	fi
done

echo
if [ "$RC" -eq 0 ]; then
	ok "nothing here would stop a run."
	[ "$DO_RECOMMEND" = "1" ] || info "Add --recommend to also ask cm-beetle for a spec and an image."
else
	fail "something above would stop a run. Fix it before ./scripts/fs-matrix.sh."
fi
exit "$RC"
