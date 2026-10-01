#!/usr/bin/env bash
# ==============================================================================
# aws-db-versions.sh — list the AWS RDS engine versions, instance classes and AMI
# ------------------------------------------------------------------------------
#   ./scripts/aws-db-versions.sh [mysql|mariadb|postgres|ec2|all]
#
#   Why:
#     To fill in .env without guessing:
#       TF_VAR_aws_{mysql,mariadb,postgres}_version  RDS engine versions
#       TF_VAR_aws_db_instance_class                 RDS instance class
#       TF_VAR_aws_instance_type                     EC2 instance type
#     RDS accepts a partial version such as "8.0" and resolves it to the current
#     minor release, so unlike NCP a partial value is safe and causes no replacement.
#
#   How it works:
#     tofu/aws/versions only holds data sources (it creates nothing), so applying
#     it is safe and free.
#
#   Limitation:
#     The AWS provider cannot list every engine version - aws_rds_engine_version
#     returns one. Reported here: the default version, the latest version and the
#     upgrade targets reachable from the default. For the exhaustive list use the CLI:
#       aws rds describe-db-engine-versions --engine mysql --region <region> \
#         --query 'DBEngineVersions[].EngineVersion'
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
MODULE="tofu/aws/versions"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[0;33m'; NC='\033[0m'

usage() { echo "Usage: $0 [mysql|mariadb|postgres|ec2|all]" >&2; exit 1; }

WHAT="${1:-all}"
case "$WHAT" in
    mysql|mariadb|postgres|ec2|all) ;;
    -h|--help) usage ;;
    *) echo -e "${RED}unknown argument: $WHAT${NC}" >&2; usage ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ ! -d "$ROOT_DIR/$MODULE" ]; then
    echo -e "${RED}module not found: $MODULE${NC}" >&2; exit 1
fi
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

echo -e "${CYAN}=== querying AWS catalog (data sources only, nothing is created) ===${NC}"
JSON="$(docker exec "$RUNNER" bash -c '
    set -euo pipefail
    set -a; . /work/.env; set +a
    export VAULT_ADDR=http://openbao:8200
    mkdir -p /work/.tofu-plugin-cache
    cd "/work/'"$MODULE"'"
    tofu init -input=false >/dev/null
    tofu apply -auto-approve >/dev/null
    tofu output -json
')"

REGION="$(echo "$JSON" | jq -r '.region.value // "unknown"')"

# print_engine <title> <engine key> — the engine version report, in a fixed field order
print_engine() {
    local title="$1" engine="$2" body
    body="$(echo "$JSON" | jq -r --arg e "$engine" '
        (.rds_versions.value[$e] // {}) as $v
        | if ($v | length) == 0 then "" else
            [ "default version        \($v.default_version)",
              "latest version         \($v.latest_version)",
              "minor upgrade targets  \($v.minor_upgrade_targets)",
              "major upgrade targets  \($v.major_upgrade_targets)",
              "parameter group family \($v.parameter_group_family)" ]
            | map("  " + .) | join("\n")
          end
    ')"
    echo -e "${GREEN}${title}${NC}"
    if [ -z "$body" ]; then
        echo "  (none)"
    else
        echo "$body"
    fi
    echo
}

print_map() {
    local title="$1" key="$2" arrow="$3" body
    body="$(echo "$JSON" | jq -r --arg k "$key" '
        (.[$k].value // {})
        | to_entries
        | sort_by(.key)
        | map("  \(.key)  '"$arrow"'  \(.value)")
        | join("\n")
    ')"
    echo -e "${GREEN}${title}${NC}"
    if [ -z "$body" ]; then
        echo "  (none)"
    else
        echo "$body"
    fi
    echo
}

print_list() {
    local title="$1" key="$2" body
    body="$(echo "$JSON" | jq -r --arg k "$key" '(.[$k].value // []) | sort | map("  \(.)") | join("\n")')"
    echo -e "${GREEN}${title}${NC}"
    if [ -z "$body" ]; then
        echo "  (none)"
    else
        echo "$body"
    fi
    echo
}

echo
echo -e "${CYAN}region: ${REGION}${NC}"
echo
case "$WHAT" in
    mysql)    print_engine "MySQL engine versions (TF_VAR_aws_mysql_version)"           mysql ;;
    mariadb)  print_engine "MariaDB engine versions (TF_VAR_aws_mariadb_version)"       mariadb ;;
    postgres) print_engine "PostgreSQL engine versions (TF_VAR_aws_postgres_version)"   postgres ;;
    ec2)
        print_list "EC2 instance types in region (TF_VAR_aws_instance_type)" ec2_instance_types
        print_map  "Ubuntu AMI used by the aws modules"                      ubuntu_ami "->"
        ;;
    all)
        print_engine "MySQL engine versions (TF_VAR_aws_mysql_version)"         mysql
        print_engine "MariaDB engine versions (TF_VAR_aws_mariadb_version)"     mariadb
        print_engine "PostgreSQL engine versions (TF_VAR_aws_postgres_version)" postgres
        print_map    "RDS instance classes (TF_VAR_aws_db_instance_class)"      rds_instance_classes "->"
        print_list   "EC2 instance types in region (TF_VAR_aws_instance_type)"  ec2_instance_types
        print_map    "Ubuntu AMI used by the aws modules"                       ubuntu_ami "->"
        ;;
esac

echo -e "${YELLOW}Either a full version (8.0.42) or a prefix (8.0) is valid for TF_VAR_aws_*_version;${NC}"
echo -e "${YELLOW}RDS resolves a prefix to the current minor release without replacing the instance.${NC}"
