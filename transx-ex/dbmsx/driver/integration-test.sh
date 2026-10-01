#!/usr/bin/env bash
# integration-test.sh — Run all (or selected) DBMS tests
# For each selected DBMS it runs, in order:
#   1. the package Go unit tests (no container; integration tests auto-skip)
#   2. the per-DBMS integration script (*_test.sh — spins up a container)
#
# Usage:
#   ./integration-test.sh                              # run all
#   ./integration-test.sh --mysql --postgres           # selected only
#   ./integration-test.sh --all --keep                 # keep containers after tests
#   ./integration-test.sh --unit                       # unit tests only (skip *_test.sh)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RUN_MYSQL=false
RUN_MARIADB=false
RUN_POSTGRES=false
RUN_MONGODB=false
KEEP_FLAG=""
UNIT_ONLY=false

usage() {
  echo "Usage: $0 [--mysql] [--mariadb] [--postgres] [--mongodb] [--all] [--keep] [--unit]"
  exit 1
}

if [[ $# -eq 0 ]]; then
  RUN_MYSQL=true; RUN_MARIADB=true; RUN_POSTGRES=true; RUN_MONGODB=true
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mysql)    RUN_MYSQL=true ;;
    --mariadb)  RUN_MARIADB=true ;;
    --postgres) RUN_POSTGRES=true ;;
    --mongodb)  RUN_MONGODB=true ;;
    --all)      RUN_MYSQL=true; RUN_MARIADB=true; RUN_POSTGRES=true; RUN_MONGODB=true ;;
    --keep)     KEEP_FLAG="--keep" ;;
    --unit)     UNIT_ONLY=true ;;
    -h|--help)  usage ;;
    *) echo "Unknown option: $1"; usage ;;
  esac
  shift
done

# If only --keep / --unit were passed (no DBMS selected), run all.
if ! $RUN_MYSQL && ! $RUN_MARIADB && ! $RUN_POSTGRES && ! $RUN_MONGODB; then
  RUN_MYSQL=true; RUN_MARIADB=true; RUN_POSTGRES=true; RUN_MONGODB=true
fi

FAILED=()

# run_unit <name> <package-dir>
# Runs the package Go unit tests. Integration tests self-skip without a
# configured *_TEST_DSN, so this stays container-free.
run_unit() {
  local name="$1"
  local dir="$2"
  echo ""
  echo "----------------------------------------"
  echo "  ${name} — unit tests"
  echo "----------------------------------------"
  if (cd "${SCRIPT_DIR}/${dir}" && go test . -v -timeout 120s -count=1); then
    echo ">> ${name} unit tests: PASS"
  else
    echo ">> ${name} unit tests: FAIL"
    FAILED+=("${name} (unit)")
  fi
}

# run_script <name> <script>
# Runs the per-DBMS integration script (spins up a container).
run_script() {
  local name="$1"
  local script="$2"
  echo ""
  echo "----------------------------------------"
  echo "  ${name} — integration (${script##*/})"
  echo "----------------------------------------"
  if bash "${script}" ${KEEP_FLAG}; then
    echo ">> ${name} integration: PASS"
  else
    echo ">> ${name} integration: FAIL"
    FAILED+=("${name} (integration)")
  fi
}

# run_dbms <name> <package-dir> <script>
run_dbms() {
  local name="$1"
  local dir="$2"
  local script="$3"
  echo ""
  echo "========================================"
  echo "  ${name}"
  echo "========================================"
  run_unit "${name}" "${dir}"
  if ! $UNIT_ONLY; then
    run_script "${name}" "${script}"
  fi
}

$RUN_MYSQL    && run_dbms "MySQL"      "mysql"      "${SCRIPT_DIR}/mysql/mysql_test.sh"
$RUN_MARIADB  && run_dbms "MariaDB"    "mariadb"    "${SCRIPT_DIR}/mariadb/mariadb_test.sh"
$RUN_POSTGRES && run_dbms "PostgreSQL" "postgresql" "${SCRIPT_DIR}/postgresql/postgresql_test.sh"
$RUN_MONGODB  && run_dbms "MongoDB"    "mongodb"    "${SCRIPT_DIR}/mongodb/mongodb_test.sh"

echo ""
echo "========================================"
if [[ ${#FAILED[@]} -eq 0 ]]; then
  echo "  All tests PASSED"
else
  echo "  FAILED: ${FAILED[*]}"
  exit 1
fi
echo "========================================"
