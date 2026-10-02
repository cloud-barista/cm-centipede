#!/usr/bin/env bash
# ==============================================================================
# gen-data.sh — generate test data (builds and runs tofuenv/gendata)
# ------------------------------------------------------------------------------
#   ./scripts/gen-data.sh [gendata options...]
#
#   Examples:
#     ./scripts/gen-data.sh --target all                       # bucket + VM + DB
#     ./scripts/gen-data.sh --target bucket
#     ./scripts/gen-data.sh --target database --engine mysql
#     ./scripts/gen-data.sh --target database --engine postgresql
#         (the engine is named postgresql, as cb-spider names it; the tofu
#          outputs it is read from stay postgres_host / postgres_port)
#     ./scripts/gen-data.sh --provider ncp --target all         # NCP resources
#     ./scripts/gen-data.sh --target all --dry-run             # generate only, skip upload/transfer/load
#     ./scripts/gen-data.sh --target bucket --force            # skip the existing-data check
#     ./scripts/gen-data.sh --provider ncp --target bucket --cleanup
#         (the reverse: empties the whole bucket instead of filling it. NCP has no
#          force_destroy on its bucket resource, so deprovision.sh runs this before
#          destroying ncp/bucket - see gendata/README.md)
#     ./scripts/gen-data.sh -h                                 # the full gendata option list
#
#   How it works:
#     This script assembles the connection info and hands it to gendata on stdin.
#     gendata itself looks nothing up - it is told where the resources are:
#
#       tofu output -json   through the tofuenv-runner container   addresses, DB password
#                           (the workspace of the prefix .env sets)
#       OpenBao             secret/csp/<provider>                   object-storage key pair
#       .env                VAULT_ADDR, VAULT_TOKEN, region fallback
#
#     How much data to generate travels separately, as GENDATA_* environment
#     variables: .env is sourced with `set -a`, so gendata inherits them. See the
#     "gendata test data size" block in .env.example.
#
#     Only the modules the requested --target needs are read, so asking for one
#     target does not require the other two to be provisioned.
#
#     The resources are those of the prefix .env sets (TF_VAR_<provider>_name_prefix),
#     and the run summary is written per environment:
#     gendata/runs/<provider>-<prefix>-last-run.json.
#
#   Why stdin rather than a file:
#     The assembled JSON carries the database password and the object-storage
#     secret key. A file would leave both on disk once the run is over, outliving
#     OpenBao itself - discarding the store would not touch it. argv is no better,
#     since ps shows it. Every environment that drives gendata hands its inputs
#     over the same way, which is what lets gendata have a single input path.
#
#   Prerequisites:
#     - ./scripts/up.sh has run (the tofuenv-runner container, and OpenBao holds
#       the credentials)
#     - the target resources are provisioned (./scripts/provision.sh <csp> <resource>)
#     - go, jq and curl on the host
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GENDATA_DIR="$ROOT_DIR/gendata"
ENV_FILE="$ROOT_DIR/.env"

# shellcheck source=./lib/env-perm.sh
. "$SCRIPT_DIR/lib/env-perm.sh"

die() { echo -e "${RED}$*${NC}" >&2; exit 1; }

# --- What has to be read --------------------------------------------------------
# gendata's flags are passed straight through; these two also decide what this
# script has to look up, so they are read here as well. Both spellings the Go
# flag package accepts are handled.
PROVIDER="aws"
TARGET="all"
HELP=0
prev=""
for arg in "$@"; do
    case "$arg" in
        --provider=*|-provider=*) PROVIDER="${arg#*=}" ;;
        --target=*|-target=*)     TARGET="${arg#*=}" ;;
        -h|--help|-help)          HELP=1 ;;
        *)
            case "$prev" in
                --provider|-provider) PROVIDER="$arg" ;;
                --target|-target)     TARGET="$arg" ;;
            esac
            ;;
    esac
    prev="$arg"
done
PROVIDER="$(echo "$PROVIDER" | tr '[:upper:]' '[:lower:]')"

want() {
    case "$TARGET" in
        all) return 0 ;;
        *)   [[ ",${TARGET}," == *",${1},"* ]] ;;
    esac
}

# --- Preconditions ------------------------------------------------------------
if [ ! -d "$GENDATA_DIR" ]; then
    die "gendata module not found: $GENDATA_DIR"
fi
if ! command -v go >/dev/null 2>&1; then
    die "go is not installed (required to build gendata)."
fi

build_gendata() {
    echo -e "${CYAN}=== build: gendata ===${NC}"
    ( cd "$GENDATA_DIR" && GOWORK=off GOFLAGS=-mod=mod go build -o gendata . )
}

# -h needs no connection info: the flag package prints the usage and exits.
if [ "$HELP" -eq 1 ]; then
    build_gendata
    set +e
    ( cd "$GENDATA_DIR" && ./gendata "$@" )
    exit $?
fi

case "$PROVIDER" in
    aws|ncp) ;;
    *) die "unknown provider '${PROVIDER}' (aws|ncp)" ;;
esac
for t in ${TARGET//,/ }; do
    case "$t" in
        bucket|filesystem|database|all) ;;
        *) die "unknown target '${t}' (bucket|filesystem|database|all)" ;;
    esac
done
for tool in jq curl; do
    command -v "$tool" >/dev/null 2>&1 || die "${tool} is not installed (required to assemble the connection info)."
done
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2
    echo -e "${YELLOW}(the connection info comes from tofu outputs, which are read inside it)${NC}" >&2
    exit 1
fi
[ -f "$ENV_FILE" ] || die ".env is missing. Run 'cp .env.example .env && chmod 600 .env' first."
check_env_perm "$ENV_FILE" || exit 1

# set -a exports everything in .env, which is also how the GENDATA_* sizes reach
# gendata: it inherits this shell's environment, so they need no plumbing of their
# own. They are deliberately not part of the JSON below - that carries facts about
# the infrastructure, which only this script knows, while a size is the operator's
# intent and belongs to whoever edits .env.
set -a; . "$ENV_FILE"; set +a
VAULT_ADDR="${VAULT_ADDR:-http://localhost:38200}"

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"
ws_load "$PROVIDER"
MANIFEST="runs/${PROVIDER}-${WS_PREFIX}-last-run.json"

# Built before the lookups rather than after, so a compile error or a misspelled
# GENDATA_ key is reported now instead of a minute of tofu and OpenBao calls later.
build_gendata

# check_gendata_env — warn about GENDATA_* keys in .env that gendata does not read.
#
# This is the one weakness of passing settings through the environment: an unknown
# variable is not rejected, it is simply never looked at, and the run quietly uses
# the config.json default. GENDATA_DB_SIZE=1024 would generate no rows at all and
# say nothing about why. The list of valid keys comes from the binary rather than
# from a copy kept here, so the two cannot drift apart.
check_gendata_env() {
    local known unknown=() key
    known="$( cd "$GENDATA_DIR" && ./gendata --env-keys 2>/dev/null )" || return 0
    # read, not word splitting: the key list is newline-separated and IFS is the
    # caller's to set.
    while IFS= read -r key; do
        [ -n "$key" ] || continue
        grep -qxF -- "$key" <<<"$known" || unknown+=("$key")
    done < <(grep -oE '^[[:space:]]*(export[[:space:]]+)?GENDATA_[A-Za-z0-9_]+' "$ENV_FILE" \
             | grep -oE 'GENDATA_[A-Za-z0-9_]+' | sort -u)
    if [ ${#unknown[@]} -gt 0 ]; then
        echo -e "${YELLOW}Unknown GENDATA_* keys in .env (gendata ignores them):${NC}" >&2
        printf '  %s\n' "${unknown[@]}" >&2
        echo -e "${YELLOW}  gendata reads:${NC}" >&2
        sed 's/^/    /' <<<"$known" >&2
    fi
}
check_gendata_env

# --- Readers ------------------------------------------------------------------

# tofu_output <module> — the module's outputs as JSON, from the prefix's workspace.
tofu_output() {
    local module="$1" out compact
    if ! out="$(ws_exec bash -c "cd /work/tofu/${PROVIDER}/${module} && tofu output -json" 2>&1)"; then
        die "cannot read ${PROVIDER}/${module} outputs:
  ${out}
  Is ${RUNNER} running (./scripts/up.sh) and ${PROVIDER}/${module} provisioned?"
    fi
    compact="${out//[[:space:]]/}"
    if [ -z "$compact" ] || [ "$compact" = "{}" ]; then
        die "${PROVIDER}/${module} (prefix ${WS_PREFIX}) has no outputs - run ./scripts/provision.sh ${PROVIDER} ${module} first"
    fi
    printf '%s' "$out"
}

# out_val <outputs-json> <name> — one scalar output, empty when absent.
out_val() {
    printf '%s' "$1" | jq -r --arg k "$2" '
        (if has($k) then .[$k].value else null end)
        | if . == null then "" elif type == "string" then . else tostring end'
}

# vault_read <path> <key> — one field of secret/<path>, empty when absent.
vault_read() {
    local body
    if ! body="$(curl -sf --max-time 10 -H "X-Vault-Token: ${VAULT_TOKEN:-}" \
                      "${VAULT_ADDR%/}/v1/secret/data/${1}" 2>/dev/null)"; then
        return 1
    fi
    printf '%s' "$body" | jq -r --arg k "$2" '.data.data[$k] // ""'
}

# --- Assemble -----------------------------------------------------------------

BUCKET_NAME=""; REGION=""; ACCESS_KEY=""; SECRET_KEY=""
VM_HOST=""; VM_USER=""; VM_KEY=""; VM_PATH=""; VM_VOLUME_GB=0
DB_USER=""; DB_PASSWORD=""; DB_NAME=""; DB_STORAGE_GB=0
MYSQL_HOST=""; MYSQL_PORT=""; MARIADB_HOST=""; MARIADB_PORT=""
PG_HOST=""; PG_PORT=""; MONGO_HOST=""; MONGO_PORT=""

if want bucket; then
    BO="$(tofu_output bucket)"
    BUCKET_NAME="$(out_val "$BO" bucket_name)"
    REGION="$(out_val "$BO" bucket_region)"
    if [ -z "$REGION" ]; then
        region_var="TF_VAR_${PROVIDER}_region"
        REGION="${!region_var:-}"
    fi

    # The CSP keys are not in .env - up.sh blanks them once they are in OpenBao.
    case "$PROVIDER" in
        ncp) AK_KEY="NCP_ACCESS_KEY"; SK_KEY="NCP_SECRET_KEY" ;;
        *)   AK_KEY="AWS_ACCESS_KEY_ID"; SK_KEY="AWS_SECRET_ACCESS_KEY" ;;
    esac
    if ! ACCESS_KEY="$(vault_read "csp/${PROVIDER}" "$AK_KEY")"; then
        die "cannot read OpenBao secret/csp/${PROVIDER} at ${VAULT_ADDR}.
  Is it running and unsealed, and is VAULT_TOKEN in .env current? (./scripts/up.sh)"
    fi
    SECRET_KEY="$(vault_read "csp/${PROVIDER}" "$SK_KEY" || true)"
    if [ -z "$ACCESS_KEY" ] || [ -z "$SECRET_KEY" ]; then
        die "OpenBao secret/csp/${PROVIDER} has no ${AK_KEY}/${SK_KEY} (run ./scripts/register-creds.sh)"
    fi
fi

if want filesystem; then
    VO="$(tofu_output vm)"
    VM_HOST="$(out_val "$VO" public_ip)"
    VM_USER="$(out_val "$VO" ssh_user)"
    if [ -z "$VM_USER" ]; then
        # The login account of the CSP-provided Linux images.
        case "$PROVIDER" in ncp) VM_USER="root" ;; *) VM_USER="ubuntu" ;; esac
    fi
    VM_KEY="$(out_val "$VO" key_file)"
    # gendata runs from its own directory, so the key path has to be absolute.
    case "$VM_KEY" in ""|/*) ;; *) VM_KEY="${ROOT_DIR}/${VM_KEY}" ;; esac

    # The vm module publishes data_path; both do, and it wins. Without one - an
    # older state that predates the output - the path follows the login account
    # rather than falling through to gendata's /home/ubuntu/testdata default,
    # which only fits AWS. SFTP does not sudo and cannot create /home/<someone
    # else>, so a path outside that account's home fails on the first mkdir.
    VM_PATH="$(out_val "$VO" data_path)"
    if [ -z "$VM_PATH" ] && [ -n "$VM_USER" ]; then
        case "$VM_USER" in
            root) VM_PATH="/root/testdata" ;;
            *)    VM_PATH="/home/${VM_USER}/testdata" ;;
        esac
    fi

    # How much the volume holds, so gendata can refuse a dummy size that would
    # fill it. AWS sizes its root volume from .env. NCP reads the size from the
    # vm module's volume_size output - what the VM was actually built with - since
    # a VM made before TF_VAR_ncp_vm_volume_size existed has the old 10 GB disk
    # whatever .env says now. A VM whose state has no such output yet sends 0, and
    # gendata skips the check rather than guessing.
    #
    # A data path on the VM's EFS mount does not touch the root volume at all, and
    # EFS grows with what is written, so there is no size to check it against:
    # 0 is sent then as well. The mount path comes from the vm module's outputs,
    # which is what the VM was built with - .env may have changed since.
    if [ "$PROVIDER" = "aws" ]; then
        VM_VOLUME_GB="${TF_VAR_aws_vm_volume_size:-20}"
        NFS_PATH="$(out_val "$VO" nfs_mount_path)"
        if [ "$(out_val "$VO" nfs_enabled)" = "true" ] && [ -n "$NFS_PATH" ]; then
            NFS_PATH="${NFS_PATH%/}"
            case "${VM_PATH%/}/" in
                "${NFS_PATH}/"*)
                    VM_VOLUME_GB=0
                    echo -e "${CYAN}filesystem: ${VM_PATH} is on the EFS mount (${NFS_PATH}) - no volume size limit${NC}"
                    ;;
            esac
        fi
    else
        VM_VOLUME_GB="$(out_val "$VO" volume_size)"
        VM_VOLUME_GB="${VM_VOLUME_GB%.*}"
        VM_VOLUME_GB="${VM_VOLUME_GB:-0}"
    fi
fi

if want database; then
    DO="$(tofu_output database)"
    DB_USER="$(out_val "$DO" db_username)"
    DB_PASSWORD="$(out_val "$DO" db_password)"
    DB_NAME="$(out_val "$DO" db_name)"
    MYSQL_HOST="$(out_val "$DO" mysql_host)";     MYSQL_PORT="$(out_val "$DO" mysql_port)"
    MARIADB_HOST="$(out_val "$DO" mariadb_host)"; MARIADB_PORT="$(out_val "$DO" mariadb_port)"
    PG_HOST="$(out_val "$DO" postgres_host)";     PG_PORT="$(out_val "$DO" postgres_port)"
    MONGO_HOST="$(out_val "$DO" mongodb_host)";   MONGO_PORT="$(out_val "$DO" mongodb_port)"

    # The storage each instance was given, so gendata can refuse a bulk size that
    # would not fit. This matters more than it sounds: a managed instance that
    # fills its volume goes read-only, and on both CSPs the way back is
    # deprovision + provision, not a delete.
    #
    #   aws  var.aws_db_allocated_storage, 20 GB unless .env overrides it
    #   ncp  none - Cloud DB storage starts small and grows on its own as data is
    #        written, so there is no allocation to measure against. 0 makes
    #        gendata skip the check, as it does for an unknown VM volume.
    case "$PROVIDER" in
        aws) DB_STORAGE_GB="${TF_VAR_aws_db_allocated_storage:-20}" ;;
        ncp) DB_STORAGE_GB=0 ;;
    esac
fi

# Empty ports, DBUser and DBName are filled in by gendata's own defaults.
INPUTS="$(jq -n \
    --arg bucket "$BUCKET_NAME" --arg region "$REGION" \
    --arg ak "$ACCESS_KEY"      --arg sk "$SECRET_KEY" \
    --arg vmhost "$VM_HOST" --arg vmuser "$VM_USER" \
    --arg vmkey "$VM_KEY"   --arg vmpath "$VM_PATH" \
    --arg dbuser "$DB_USER" --arg dbpass "$DB_PASSWORD" --arg dbname "$DB_NAME" \
    --arg myh "$MYSQL_HOST"   --arg myp "$MYSQL_PORT" \
    --arg mah "$MARIADB_HOST" --arg map "$MARIADB_PORT" \
    --arg pgh "$PG_HOST"      --arg pgp "$PG_PORT" \
    --arg mgh "$MONGO_HOST"   --arg mgp "$MONGO_PORT" \
    --argjson vmgb "${VM_VOLUME_GB:-0}" --argjson dbgb "${DB_STORAGE_GB:-0}" \
    '{
        BucketName: $bucket, Region: $region, AccessKey: $ak, SecretKey: $sk,
        VMHost: $vmhost, VMUser: $vmuser, VMKeyPath: $vmkey, VMDataPath: $vmpath,
        DBUser: $dbuser, DBPassword: $dbpass, DBName: $dbname,
        MySQLHost: $myh,    MySQLPort: $myp,
        MariaDBHost: $mah,  MariaDBPort: $map,
        PostgresHost: $pgh, PostgresPort: $pgp,
        MongoHost: $mgh,    MongoPort: $mgp,
        VMVolumeGB: $vmgb,  DBStorageGB: $dbgb
     }')"

# The connection info, without the two secrets in it. Only the rows this run is
# about: a database load has nothing to say about the bucket, and a "-" next to
# one reads like something is missing rather than like something is not asked for.
if want bucket; then
    printf '%s' "$INPUTS" | jq -r '"  bucket     : " + (if .BucketName == "" then "-" else .BucketName end)'
fi
if want filesystem; then
    printf '%s' "$INPUTS" | jq -r '"  vm         : " + (if .VMHost == "" then "-" else .VMUser + "@" + .VMHost end)'
fi
if want database; then
    # Only the engines the provider actually serves: AWS has no MongoDB and NCP has
    # no MariaDB, so listing them would print a "-" for something never provisioned.
    printf '%s' "$INPUTS" | jq -r '
        "  mysql      : " + (if .MySQLHost == "" then "-" else .MySQLHost end),
        "  postgresql : " + (if .PostgresHost == "" then "-" else .PostgresHost end)'
    if [ "$PROVIDER" = "ncp" ]; then
        printf '%s' "$INPUTS" | jq -r '"  mongodb    : " + (if .MongoHost == "" then "-" else .MongoHost end)'
    else
        printf '%s' "$INPUTS" | jq -r '"  mariadb    : " + (if .MariaDBHost == "" then "-" else .MariaDBHost end)'
    fi
fi

# --- Run ----------------------------------------------------------------------
echo -e "${CYAN}=== run: gendata ${*:-} ===${NC}"
# pipefail is off for this one pipeline: when gendata exits early - a rejected
# flag, say - printf dies of EPIPE, and 141 would then mask gendata's own status.
set +e +o pipefail
( cd "$GENDATA_DIR" && printf '%s' "$INPUTS" | ./gendata --inputs-file - --manifest "$MANIFEST" "$@" )
RC=$?
set -e -o pipefail
[ "$RC" -eq 0 ] || exit "$RC"

echo -e "${GREEN}=== Done ===${NC}"
echo "  Run summary (manifest): $GENDATA_DIR/$MANIFEST"
