#!/usr/bin/env bash
# ==============================================================================
# provision.sh — create test resources through the cm-beetle API
# ------------------------------------------------------------------------------
#   ./scripts/provision.sh <csp> <database|bucket|vm> [--engine <engine>]
#
#   Only the three things worth asking for by name. The vNet, its subnets and the
#   security group are a means to those, not a goal in themselves: database and
#   vm call ensure_shared_network() on the way in, which creates them if they are
#   absent and reuses them otherwise. A bucket needs no network at all and
#   creates none.
#
# ONE SHAPE FOR EVERY RESOURCE
#   recommend -> check -> inject -> migrate -> wait.
#
#   The recommendation turns a description of a source resource into a concrete
#   target specification, resolving everything that differs per CSP: engine
#   versions, instance specs, storage types, VM specs and images. That is why
#   .env has no key for any of them, and why changing the region needs no lookup.
#
#   The injection step fills in what the recommendation deliberately leaves out -
#   the network, the admin password, the names - and is documented per resource
#   in lib/recommend.sh.
#
# NAMING: beetleenv NAMES EVERYTHING ITSELF
#   The migration APIs offer a `nameSeed` query parameter that prefixes the
#   recommended names at creation time. beetleenv does not use it, anywhere.
#
#   Two reasons. The recommendation names every RDBMS instance mig-rdbms-01
#   regardless of engine, so a seed would give the mysql and the mariadb
#   instance the same name. And for the VM, beetle's ApplyNameSeed prefixes the
#   nodeGroups' vNetId, subnetId and securityGroupIds along with the names, so a
#   seed on top of the real resource ids injected here would look for
#   "cpbt-cpbt-vnet" and find nothing.
#
#   So the names are set in the request body instead, all <prefix>-<csp>-<what>:
#     cpbt-aws-vnet, cpbt-aws-subnet-1, cpbt-aws-subnet-2, cpbt-aws-sg
#     cpbt-aws-db-mysql, cpbt-aws-bucket, cpbt-aws-infra, cpbt-aws-sshkey
#
#   The CSP is in the name because one namespace holds every CSP's resources and
#   a cb-tumblebug id is unique per namespace, not per connection.
#
# ASYNCHRONOUS BY DEFAULT
#   A managed RDBMS takes 5 to 30 minutes and a VM several. Every migration call
#   is sent with `Prefer: respond-async` and followed through GET /request/{id},
#   so the progress is visible and a dropped connection does not abandon a
#   resource that is still being built.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/recommend.sh
. "${SCRIPT_DIR}/lib/recommend.sh"
# shellcheck source=./lib/namespace.sh
. "${SCRIPT_DIR}/lib/namespace.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/provision.sh <csp> <resource> [options]

Resources:
  database    one managed RDBMS instance per engine
  bucket      one object storage bucket
  vm          one VM, in the same vNet as the database

The vNet, its two subnets and the security group are not listed here on purpose:
database and vm create them on the way in, and share one set between them.

Options:
  --engine <engine>   database only: create just this engine, instead of every
                      engine in BEETLEENV_<CSP>_DB_ENGINES
  --sync              send the migration call synchronously (no Prefer header).
                      One long-held connection instead of polling; useful when
                      something between here and beetle rewrites headers.

Examples:
  ./scripts/provision.sh aws database
  ./scripts/provision.sh aws database --engine mariadb
  ./scripts/provision.sh ncp vm
EOF
}

CSP=""
RESOURCE=""
ENGINE=""
SYNC=0

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --engine)  ENGINE="${2:-}"; shift ;;
        --sync)    SYNC=1 ;;
        -*)        usage >&2; die "unknown option: $1" ;;
        *)
            if   [ -z "$CSP" ];      then CSP="$1"
            elif [ -z "$RESOURCE" ]; then RESOURCE="$1"
            else usage >&2; die "unexpected argument: $1"
            fi
            ;;
    esac
    shift
done

if [ -z "$CSP" ] || [ -z "$RESOURCE" ]; then
    usage >&2
    exit 1
fi

case "$RESOURCE" in
    database|bucket|vm) ;;
    network)
        die "'network' is not a resource you provision directly.
       The vNet, its subnets and the security group are created on the way into
       'database' and 'vm', and shared between them.
       On their own there would be nothing to create them for, and
       ./scripts/deprovision.sh removes them once nothing is left inside." ;;
    *) usage >&2; die "unknown resource: ${RESOURCE}" ;;
esac
if [ -n "$ENGINE" ] && [ "$RESOURCE" != "database" ]; then
    die "--engine applies to 'database' only"
fi

preflight
validate_csp "$CSP"
CSP="$(csp_lower "$CSP")"
require_csp_env "$CSP" || exit 1
require_ns

NS_PATH="$(urlq "$BEETLEENV_NS")"

# migrate <method> <path> <body> <label> <timeout> — the one call that creates
#   something, asynchronous unless --sync was given.
migrate() {
    local method="$1" path="$2" body="$3" label="$4" timeout="$5"
    local req_id

    if [ "$SYNC" -eq 1 ]; then
        log_info "${label}: sending synchronously; this holds one connection open until the CSP is done"
        if ! bt_request "$method" "$path" "$body"; then
            die "${label} failed - $(bt_message)"
        fi
        log_ok "${label} completed"
        return 0
    fi

    if ! req_id="$(bt_async "$method" "$path" "$body")"; then
        die "${label} was refused - $(bt_message)"
    fi
    if [ -n "$req_id" ]; then
        log_info "${label}: request ${req_id}"
    fi
    if ! bt_wait "$req_id" "$label" "$timeout"; then
        # Before the die: the one-line progress form above is cut at 200
        # characters, and a CSP's actual reason usually starts after that.
        bt_report_failure
        die "${label} did not complete.
       Nothing has been rolled back - the resource may still be on its way.
       ./scripts/conn-info.sh ${CSP} shows what exists now."
    fi
}

# skip_if_present <label> <path> <jq program> — true when the resource is already
#   there, in which case it is left exactly as it is.
#
#   Re-running provision.sh is the normal way to add the one thing that failed
#   last time, so it has to be a no-op for everything that succeeded. Without
#   this it is the opposite: cb-tumblebug rejects every create on a name it
#   already holds - "already exists, RDBMS", "already exists, object storage",
#   "The infra %s already exists." - and migrate() turns that into a die. Worse
#   with several engines, where the first existing one ends the run before the
#   missing one is ever reached.
#
#   This is the same create-if-absent rule ensure_network already follows, and
#   there is deliberately no --force beside it: replacing a resource is
#   deprovision.sh followed by provision.sh, which is explicit about the delete.
#
#   A lookup that fails for any other reason returns 1 and the create is
#   attempted, which is what happened before this existed.
#
#   EXISTING IS NOT THE SAME AS USABLE
#   A create that failed part-way leaves the record behind holding the name -
#   an infrastructure at "Failed:1 (R:0/1)", a database that never came up. Both
#   of the obvious things to do with one are wrong: creating over it is refused
#   because the name is taken, and skipping it quietly ends the run with "done"
#   over a resource that does not work. So it stops here instead, and says which
#   two commands replace it. Deleting it automatically is not the answer either -
#   a failed VM can still have a running instance behind it, and that is a bill.
skip_if_present() {
    local label="$1" path="$2" program="$3" status

    if ! bt_get "$path"; then
        return 1
    fi

    status="$(bt_payload | jq -r '.status // empty' 2>/dev/null || true)"

    case "$status" in
        *Failed*|*Undefined*|*Terminated*|*Suspended*)
            log_error "${label} already exists, but its status is ${status}"
            bt_payload | jq -r "$program" 2>/dev/null || true
            die "this ${RESOURCE} cannot be used, and nothing can be created over it
       while the name is taken. Remove it and provision again:
         ./scripts/deprovision.sh ${CSP} ${RESOURCE}
         ./scripts/provision.sh ${CSP} ${RESOURCE}
       ./scripts/conn-info.sh ${CSP} shows what is still there in the meantime."
            ;;
    esac

    log_info "${label} already exists; leaving it alone"
    bt_payload | jq -r "$program" 2>/dev/null || true
    return 0
}

# ------------------------------------------------------------------------------
# network — internal
# ------------------------------------------------------------------------------

# ensure_shared_network — the vNet, its two subnets and the security group that
#   database and vm both sit in. Called by each of them rather than being a
#   resource of its own: on its own it would be an empty network nobody asked
#   for, and the two callers must not end up with one each.
#
#   Idempotent. The second caller finds what the first one made and reuses it,
#   which is what puts the VM and the database on the same network - the point of
#   the whole arrangement, since cm-centipede migrates between them.
#
#   Publishes NET_VNET_ID, NET_SUBNET_IDS and NET_SG_ID for the injection step.
ensure_shared_network() {
    ensure_network "$CSP"

    state_write "$CSP" network "$(jq -n \
        --arg vnet "$NET_VNET_ID" --argjson subnets "${NET_SUBNET_IDS:-[]}" --arg sg "$NET_SG_ID" \
        '{vNetId: $vnet, subnetIds: $subnets, securityGroupId: $sg}')"
}

# ------------------------------------------------------------------------------
# database
# ------------------------------------------------------------------------------

rdbms_name() { resource_name "$CSP" "db-$1"; }

provision_database_engine() {
    local engine="$1" name rec body

    name="$(rdbms_name "$engine")"
    log_step "${CSP}: managed ${engine} as ${name}"

    if skip_if_present "$name" "/migration/middleware/ns/${NS_PATH}/rdbms/$(urlq "$name")" '
        "  status           \(.status // "-")",
        "  engine           \(.dbEngine // "-") \(.dbEngineVersion // "")",
        "  endpoint         \(if (.endpoint // "") == "" then "(not assigned yet)" else .endpoint end)"'; then
        return 0
    fi

    rec="$(recommend_rdbms "$CSP" "$engine")"
    body="$(inject_rdbms_target "$rec" "$CSP" "$name")"

    # Read from the request rather than from the recommendation, because the two
    # no longer agree: publicAccess and backupRetentionDays are re-asserted during
    # injection (see the note on inject_rdbms_target), and it is what is about to
    # be created that is worth printing. Named fields only - the body holds the
    # admin password.
    printf '%s' "$body" | jq -r '.targetRDBMSInstances[0] |
        "  engine           \(.dbEngine) \(.dbEngineVersion)",
        "  instance spec    \(.dbInstanceSpec // "(chosen by cb-tumblebug)")",
        "  storage          \(if .storageType == "" or .storageType == null then "(managed by the CSP)" else "\(.storageType) \(.storageSize)GB" end)",
        "  public access    \(.publicAccess)",
        "  backups          \(if .backupRetentionDays == 0 then "disabled" else "\(.backupRetentionDays) days" end)"'

    # The recommendation is recorded, not the request body: state/ never holds a
    # secret.
    #
    # Also before the create rather than after it, on purpose. state/ is a record
    # of what was asked for, not a cache of what exists - nothing reads it back,
    # and deprovision.sh only deletes from it. The case that decides the order is
    # an async create that outlives its timeout: migrate() dies saying the
    # resource may still be on its way, and a write placed after it would leave
    # no trace of the one resource most likely to turn up unlisted. Re-running
    # cannot overwrite this with a mismatched recommendation, because
    # skip_if_present returns above before ever reaching here.
    state_write "$CSP" "db-${engine}" "$(jq -n \
        --arg name "$name" --argjson rec "$rec" \
        '{name: $name, recommendation: $rec}')"

    migrate POST "/migration/middleware/ns/${NS_PATH}/rdbms" "$body" \
        "${name} (a managed RDBMS takes 5-30 minutes)" "$RDBMS_TIMEOUT"

    if bt_get "/migration/middleware/ns/${NS_PATH}/rdbms/$(urlq "$name")"; then
        bt_payload | jq -r '
            "  status           \(.status // "-")",
            "  endpoint         \(if (.endpoint // "") == "" then "(not assigned yet)" else .endpoint end)",
            "  admin user       \(.adminUserName // "-")"'
    fi
}

provision_database() {
    local engines engine

    assert_rdbms_supported "$CSP"

    engines="${ENGINE:-$(csp_env "$CSP" DB_ENGINES)}"
    if [ -z "$engines" ]; then
        die "no engines to create.
       Set BEETLEENV_$(csp_upper "$CSP")_DB_ENGINES, or pass --engine."
    fi

    # Every engine is checked before the first one is created: finding out at
    # instance three that engine four is unsupported wastes the first three.
    for engine in $engines; do
        engine="$(csp_lower "$engine")"
        assert_engine_known "$engine" "$CSP"
        assert_engine_supported "$CSP" "$engine"
    done

    ensure_shared_network

    for engine in $engines; do
        provision_database_engine "$(csp_lower "$engine")"
    done
}

# ------------------------------------------------------------------------------
# bucket
# ------------------------------------------------------------------------------

provision_bucket() {
    local name rec body

    name="$(resource_name "$CSP" bucket)"
    log_step "${CSP}: object storage as ${name}"

    if skip_if_present "$name" "/migration/middleware/ns/${NS_PATH}/objectStorage/$(urlq "$name")" '
        "  status           \(.status // "-")",
        "  CSP bucket name  \(.cspResourceName // .uid // "-")"'; then
        return 0
    fi

    rec="$(recommend_objectstorage "$CSP")"
    rec="$(inject_bucket_name "$rec" "$name")"

    printf '%s' "$rec" | jq -r '.targetObjectStorages[0] |
        "  versioning       \(.versioningEnabled // false)",
        "  encryption       \(.encryptionEnabled // false)",
        "  public           \(.isPublic // false)"'

    state_write "$CSP" bucket "$(jq -n --arg name "$name" --argjson rec "$rec" \
        '{name: $name, recommendation: $rec}')"

    body="$rec"
    migrate POST "/migration/middleware/ns/${NS_PATH}/objectStorage" "$body" \
        "${name}" "$BUCKET_TIMEOUT"

    if bt_get "/migration/middleware/ns/${NS_PATH}/objectStorage/$(urlq "$name")"; then
        bt_payload | jq -r '
            "  status           \(.status // "-")",
            "  CSP bucket name  \(.cspResourceName // .uid // "-")"'
    fi
}

# ------------------------------------------------------------------------------
# vm
# ------------------------------------------------------------------------------

# The SSH readiness endpoint is rate limited by beetle itself: one check per
# infrastructure every 30 seconds, 429 in between (middlewares/ssh-check-cooldown.go).
# Polling faster than that would spend every other request on a rejection.
SSH_POLL_INTERVAL=35

ssh_ready_probe() {
    if ! bt_get "/migration/ns/${NS_PATH}/infra/$(urlq "$1")/ssh-ready"; then
        printf 'pending:%s' "$(bt_message)"
        return 0
    fi
    case "$(bt_data '.ready')" in
        true)  printf 'ready' ;;
        *)     printf 'pending:%s' "$(bt_data '.readyNodes')/$(bt_data '.totalNodes') nodes" ;;
    esac
}

provision_vm() {
    local name rec body

    name="$(resource_name "$CSP" infra)"
    log_step "${CSP}: VM infrastructure as ${name}"

    # Ahead of the network, unlike the two above: an infrastructure that exists
    # was built on a vNet that exists, so there is nothing for
    # ensure_shared_network to find out.
    if skip_if_present "$name" "/migration/ns/${NS_PATH}/infra/$(urlq "$name")" '
        "  status           \(.status // "-")",
        (.node[]? | "  node             \(.name)  \(.publicIP // "-")  (private \(.privateIP // "-"))")'; then
        printf '  private key      ./scripts/conn-info.sh %s --reveal\n' "$CSP"
        return 0
    fi

    # Before the network, and before the recommendation: a zone disagreement is
    # only visible as a CSP error twenty minutes later, and by then there is a
    # vNet and a Failed infrastructure to clean up.
    assert_vm_zone "$CSP"

    # The VM shares the database's vNet, so the network comes first here too.
    ensure_shared_network

    rec="$(recommend_infra "$CSP")"
    printf '%s' "$rec" | jq -r '.targetInfra.nodeGroups[0] |
        "  spec             \(.specId // "-")",
        "  image            \(.imageId // "-")",
        "  root disk        \(if (.rootDiskSize // 0) == 0 then "(CSP default)" else "\(.rootDiskSize)GB" end)"'

    rec="$(inject_infra_network "$rec" "$CSP")"

    state_write "$CSP" vm "$(jq -n --arg name "$name" --argjson rec "$rec" \
        '{name: $name, recommendation: $rec}')"

    body="$rec"
    # useExisting=true is what makes beetle adopt the vNet, subnet, security
    # group and SSH key named in the body instead of creating a second set.
    migrate POST "/migration/ns/${NS_PATH}/infra?useExisting=true" "$body" \
        "${name}" "$VM_TIMEOUT"

    log_step "waiting for SSH on ${name}"
    local saved_interval="$POLL_INTERVAL"
    POLL_INTERVAL="$SSH_POLL_INTERVAL"
    if ! poll_for "${name} ssh" "$VM_TIMEOUT" ssh_ready_probe "$name"; then
        log_warn "the VM exists but SSH is not answering yet; check the security group"
    fi
    POLL_INTERVAL="$saved_interval"

    if bt_get "/migration/ns/${NS_PATH}/infra/$(urlq "$name")"; then
        bt_payload | jq -r '
            "  status           \(.status // "-")",
            (.node[]? | "  node             \(.name)  \(.publicIP // "-")  (private \(.privateIP // "-"))")'
    fi
    printf '  private key      ./scripts/conn-info.sh %s --reveal\n' "$CSP"
}

# ------------------------------------------------------------------------------

case "$RESOURCE" in
    database) provision_database ;;
    bucket)   provision_bucket ;;
    vm)       provision_vm ;;
esac

printf '\n'
log_ok "${CSP} ${RESOURCE} done. ./scripts/conn-info.sh ${CSP} shows how to reach it."
