#!/usr/bin/env bash
#
# down.sh — stop the matrix stack
#
#   Containers only. OpenBao's storage (container-volume/openbao-data) and its
#   unseal key (init/openbao/secrets/) are kept, so the next ./scripts/up.sh
#   unseals rather than initializes.
#
# ⚠ This script deletes no CSP resources. The managed instances the matrix
#   creates are destroyed by the matrix itself when it ends. If an interruption
#   left something behind, reclaim it before taking the stack down:
#
#     ./scripts/aws-db-matrix.sh --cleanup     # destroy everything left in tofu state
#
# ⚠ It does not remove source containers either. The matrix removes them per
#   cell, but --keep-on-fail leaves one behind; remove those by hand:
#
#     docker rm -f $(docker ps -aq --filter "name=tofu-db-test")
#
# Usage:
#   ./scripts/down.sh              # stop the containers
#   ./scripts/down.sh --volumes    # containers + OpenBao storage (credentials are lost)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

YELLOW='\033[1;33m'; GREEN='\033[0;32m'; NC='\033[0m'

WIPE=0
for arg in "$@"; do
	case "$arg" in
	--volumes) WIPE=1 ;;
	-h|--help) awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "${BASH_SOURCE[0]:-$0}"; exit 0 ;;
	*) echo "unknown option: $arg" >&2; exit 1 ;;
	esac
done

docker compose down

if [ "$WIPE" = "1" ]; then
	echo -e "${YELLOW}--volumes — removing OpenBao's storage and unseal key.${NC}"
	echo "  The stored CSP keys and DB passwords go with them, so the next ./scripts/up.sh needs them again."
	rm -rf "$ROOT_DIR/container-volume/openbao-data" \
	       "$ROOT_DIR/init/openbao/secrets/openbao-init.json"
	echo -e "${GREEN}Removed.${NC}"
fi
