#!/usr/bin/env bash
#
# up.sh — start the matrix stack (OpenBao + the tofu runner)
#
#   1) Start OpenBao
#   2) Initialize it on the first run, unseal it on later ones
#   3) Register credentials (.env -> OpenBao, then blank those keys in .env)
#   4) Start the tofu runner (recreated, so it picks up the new VAULT_TOKEN)
#
# This stack belongs to this folder. Its compose project, container names,
# network and published port all differ from tofuenv's, so both can be up at
# once without mixing.
#
# ⚠ This script does NOT start cm-honeybee or cm-centipede. The matrix checks
#   that both answer in step 1 and stops without creating a single resource if
#   they do not.
#
# Usage:
#   ./scripts/up.sh                 # start everything + register credentials
#   ./scripts/up.sh --keep-env      # register, but keep the values in .env
#   ./scripts/up.sh --no-register   # skip credential registration

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RED='\033[0;31m'; NC='\033[0m'

KEEP_ENV_FLAG=""
DO_REGISTER=true
for arg in "$@"; do
	case "$arg" in
	--keep-env)    KEEP_ENV_FLAG="--keep-env" ;;
	--no-register) DO_REGISTER=false ;;
	-h|--help)     awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "${BASH_SOURCE[0]:-$0}"; exit 0 ;;
	*) echo "unknown option: $arg" >&2; exit 1 ;;
	esac
done

if [ ! -f "$ROOT_DIR/.env" ]; then
	echo -e "${RED}.env does not exist.${NC}" >&2
	echo "  cp .env.example .env && chmod 600 .env" >&2
	echo "  then fill in the CSP keys and the DB password." >&2
	exit 1
fi

# Checked before any container starts, so nothing comes up around a file with
# the wrong permissions.
# shellcheck source=./lib/env-perm.sh
. "$SCRIPT_DIR/lib/env-perm.sh"
check_env_perm "$ROOT_DIR/.env" || exit 1

echo -e "${CYAN}[1/4] starting OpenBao...${NC}"
docker compose up -d openbao

echo -e "${CYAN}[2/4] initializing / unsealing OpenBao...${NC}"
if [ -f "$ROOT_DIR/init/openbao/secrets/openbao-init.json" ]; then
	bash "$ROOT_DIR/init/openbao/openbao-unseal.sh"
else
	bash "$ROOT_DIR/init/openbao/openbao-init.sh"
fi

if $DO_REGISTER; then
	echo -e "${CYAN}[3/4] registering credentials...${NC}"
	bash "$ROOT_DIR/init/openbao/register-creds.sh" $KEEP_ENV_FLAG
else
	echo -e "${YELLOW}[3/4] skipping credential registration (--no-register)${NC}"
fi

echo -e "${CYAN}[4/4] starting the tofu runner...${NC}"
# Recreated on purpose - it has to come up holding the VAULT_TOKEN just written
# into .env.
docker compose up -d --force-recreate tofu-runner

echo -e "${GREEN}=== Ready ===${NC}"
echo "  Run the matrix   :  ./scripts/aws-db-matrix.sh   or   ./scripts/ncp-db-matrix.sh"
echo "  Target versions  :  ./scripts/aws-db-versions.sh  /  ./scripts/ncp-db-versions.sh"
echo "  Stop the stack   :  ./scripts/down.sh"
echo
echo "  ⚠ cm-honeybee and cm-centipede must be started separately. The matrix checks them in step 1."
