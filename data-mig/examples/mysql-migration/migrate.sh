#!/usr/bin/env bash
# migrate.sh — Build and run the MySQL direct-to-direct migration example
#
# One config file serves every scope: --scope overrides the scope recorded in it.
#
# setup_environment.sh all generates config.json; run it first (or copy
# template-config.json) when it is missing.
#
# Usage:
#   ./migrate.sh [full|schema-only|data-only] [--async] [--inspect]
#                [--src-version VER] [--dst-version VER]
#
# Examples:
#   ./migrate.sh full
#   ./migrate.sh schema-only
#   ./migrate.sh full --async --inspect
#   ./migrate.sh full --src-version 5.7 --dst-version 8.0
#   ./migrate.sh full --src-version 8.0 --dst-version 8.4 --inspect

set -euo pipefail

# =============================================================================
# Color output
# =============================================================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="${SCRIPT_DIR}/main"

# =============================================================================
# Defaults
# =============================================================================
SCOPE="full"
ASYNC=false
INSPECT=false
SRC_VERSION="latest"
DST_VERSION="latest"

# =============================================================================
# Parse args
# =============================================================================
show_usage() {
  echo -e "${BLUE}MySQL Direct-to-Direct Migration Runner${NC}"
  echo -e "${YELLOW}Usage:${NC}"
  echo -e "  $0 [full|schema-only|data-only] [flags]"
  echo -e ""
  echo -e "${YELLOW}Scopes:${NC}"
  echo -e "  full         Migrate schema + data (default)"
  echo -e "  schema-only  Migrate schema objects only (no rows)"
  echo -e "  data-only    Migrate rows only (tables must exist on target)"
  echo -e ""
  echo -e "${YELLOW}Flags:${NC}"
  echo -e "  --async           Run asynchronously with progress polling"
  echo -e "  --inspect         Inspect source DB before migration"
  echo -e "  --src-version     MySQL version for source container (default: latest)"
  echo -e "  --dst-version     MySQL version for target container (default: latest)"
  echo -e "  -h, --help        Show this help"
  echo -e ""
  echo -e "${YELLOW}Examples:${NC}"
  echo -e "  $0 full"
  echo -e "  $0 full --async --inspect"
  echo -e "  $0 schema-only --src-version 5.7 --dst-version 8.0"
  echo -e "  $0 full --src-version 8.0 --dst-version 8.4 --inspect"
}

# First positional arg may be scope
if [[ $# -gt 0 ]] && [[ "$1" =~ ^(full|schema-only|data-only)$ ]]; then
  SCOPE="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --async)        ASYNC=true; shift ;;
    --inspect)      INSPECT=true; shift ;;
    --src-version)  SRC_VERSION="$2"; shift 2 ;;
    --dst-version)  DST_VERSION="$2"; shift 2 ;;
    -h|--help)      show_usage; exit 0 ;;
    *) echo -e "${RED}Unknown option: $1${NC}"; show_usage; exit 1 ;;
  esac
done

# =============================================================================
# Settings — .env (cp .env.example .env)
# =============================================================================
# A real shell variable wins over the file: snapshot the environment, read the
# file, then restore the snapshot.
ENV_FILE="${ENV_FILE:-${SCRIPT_DIR}/.env}"
if [[ -f "${ENV_FILE}" ]]; then
  __ENV_PRESET="$(export -p)"
  set -a; . "${ENV_FILE}"; set +a
  eval "${__ENV_PRESET}"
  unset __ENV_PRESET
fi

# The container passwords have no default. ChangeMe is what the .example ships;
# empty is refused by the mysql image; " and \ would break the generated config.json.
check_passwords() {
  local v val
  for v in SRC_PASS DST_PASS; do
    val="${!v:-}"
    if [[ -z "${val}" ]]; then
      echo -e "${RED}${v} is empty — set it in ${ENV_FILE} (cp .env.example .env).${NC}" >&2; exit 1
    elif [[ "$(printf '%s' "${val}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')" == "changeme" ]]; then
      echo -e "${RED}${v} is still ChangeMe — change it in ${ENV_FILE}.${NC}" >&2; exit 1
    elif [[ "${val}" == *[\"\\]* ]]; then
      echo -e "${RED}${v} must not contain \" or \\ (it is written into config.json).${NC}" >&2; exit 1
    fi
  done
}
check_passwords

CONFIG_FILE="config.json"

# =============================================================================
# Build binary
# =============================================================================
build_binary() {
  echo -e "${CYAN}>> Building migration binary...${NC}"
  cd "${SCRIPT_DIR}"
  go build -o main .
  echo -e "${GREEN}  ✓ Binary built: ${BINARY}${NC}"
}

if [[ ! -f "${BINARY}" ]]; then
  build_binary
fi

# =============================================================================
# Check containers
# =============================================================================
SRC_CONTAINER="mysql_src"
DST_CONTAINER="mysql_dst"

check_containers() {
  local src_running dst_running
  src_running=$(docker ps -q -f "name=^${SRC_CONTAINER}$" 2>/dev/null || true)
  dst_running=$(docker ps -q -f "name=^${DST_CONTAINER}$" 2>/dev/null || true)

  if [[ -z "${src_running}" ]] || [[ -z "${dst_running}" ]]; then
    echo -e "${YELLOW}>> Containers not running — starting environment...${NC}"
    "${SCRIPT_DIR}/setup_environment.sh" all \
      --src-version "${SRC_VERSION}" \
      --dst-version "${DST_VERSION}"
  else
    echo -e "${GREEN}  ✓ Source container running (${SRC_CONTAINER})${NC}"
    echo -e "${GREEN}  ✓ Target container running (${DST_CONTAINER})${NC}"
  fi
}

# =============================================================================
# Show pre/post DB status via docker exec
# =============================================================================
# SRC_PASS / DST_PASS come from .env (checked above).
SRC_DB="testdb_src"
DST_DB="testdb_dst"

show_db_status() {
  local label="$1" container="$2" pass="$3" db="$4"

  echo -e "${CYAN}  [${label}] ${db}${NC}"
  docker exec "${container}" mysql -uroot -p"${pass}" "${db}" --silent \
    -e "
SELECT CONCAT('    Tables=',
  (SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='${db}' AND TABLE_TYPE='BASE TABLE'),
  '  Views=',
  (SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='${db}'),
  '  Procs=',
  (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='${db}' AND ROUTINE_TYPE='PROCEDURE'),
  '  Funcs=',
  (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='${db}' AND ROUTINE_TYPE='FUNCTION'),
  '  Triggers=',
  (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='${db}'),
  '  Events=',
  (SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='${db}')
);
" 2>/dev/null || echo -e "    ${YELLOW}(not accessible)${NC}"

  docker exec "${container}" mysql -uroot -p"${pass}" "${db}" --silent \
    -e "SELECT CONCAT('    Rows: ',
  GROUP_CONCAT(CONCAT(TABLE_NAME,'=',TABLE_ROWS) ORDER BY TABLE_NAME SEPARATOR '  '))
FROM information_schema.TABLES
WHERE TABLE_SCHEMA='${db}' AND TABLE_TYPE='BASE TABLE';" \
    2>/dev/null || true
}

# =============================================================================
# Main
# =============================================================================
echo -e "${BLUE}=========================================${NC}"
echo -e "${BLUE} MySQL Direct-to-Direct Migration${NC}"
echo -e "${BLUE}=========================================${NC}"
echo -e "  Scope:       ${SCOPE}"
echo -e "  Config:      ${CONFIG_FILE}"
echo -e "  Mode:        $([ "${ASYNC}" = true ] && echo 'async' || echo 'sync')"
echo -e "  Src version: mysql:${SRC_VERSION}"
echo -e "  Dst version: mysql:${DST_VERSION}"
echo ""

check_containers
echo ""

# setup_environment.sh writes the config files, so this check comes after it.
if [[ ! -f "${SCRIPT_DIR}/${CONFIG_FILE}" ]]; then
  echo -e "${RED}Config file not found: ${SCRIPT_DIR}/${CONFIG_FILE}${NC}"
  echo -e "${YELLOW}Run ./setup_environment.sh all to generate it, or copy template-config.json${NC}"
  exit 1
fi

# config.json is written once, by setup_environment.sh all, and read as it is from
# then on. A .env edited after that is not in it, so the run would use the old
# hosts or passwords - stop and say which values to carry over.
if [[ -f "${ENV_FILE}" ]] && [[ "${ENV_FILE}" -nt "${SCRIPT_DIR}/${CONFIG_FILE}" ]]; then
  echo -e "${RED}${ENV_FILE} has changed since ${CONFIG_FILE} was written.${NC}" >&2
  echo -e "${YELLOW}Update these values in ${SCRIPT_DIR}/${CONFIG_FILE} to match .env:${NC}" >&2
  echo -e "  source.direct.host           ← SRC_HOST  (${SRC_HOST:-127.0.0.1})" >&2
  echo -e "  source.direct.password       ← SRC_PASS" >&2
  echo -e "  destination.direct.host      ← DST_HOST  (${DST_HOST:-127.0.0.1})" >&2
  echo -e "  destination.direct.password  ← DST_PASS" >&2
  echo -e "${YELLOW}A password changed in .env is not in the running containers either -${NC}" >&2
  echo -e "${YELLOW}they keep the one they were created with until ./setup_environment.sh all.${NC}" >&2
  exit 1
fi

echo -e "${CYAN}>> Pre-migration status:${NC}"
show_db_status "Source" "${SRC_CONTAINER}" "${SRC_PASS}" "${SRC_DB}"
show_db_status "Target" "${DST_CONTAINER}" "${DST_PASS}" "${DST_DB}"
echo ""

# Construct args for the Go binary.
# --scope wins over the scope field recorded in the config file.
CMD_ARGS=("--config=${CONFIG_FILE}" "--scope=${SCOPE}")
[[ "${ASYNC}" = true ]]    && CMD_ARGS+=("--async")
[[ "${INSPECT}" = true ]]  && CMD_ARGS+=("--inspect")

echo -e "${BLUE}>> Running: ${BINARY} ${CMD_ARGS[*]}${NC}"
echo ""

cd "${SCRIPT_DIR}"
if "${BINARY}" "${CMD_ARGS[@]}"; then
  echo ""
  echo -e "${CYAN}>> Post-migration status:${NC}"
  show_db_status "Target" "${DST_CONTAINER}" "${DST_PASS}" "${DST_DB}"
  echo ""
  echo -e "${GREEN}Migration completed successfully!${NC}"
else
  echo ""
  echo -e "${RED}Migration failed!${NC}"
  exit 1
fi
