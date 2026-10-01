#!/usr/bin/env bash
# ==============================================================================
# deprovision.sh — delete what provision.sh created
# ------------------------------------------------------------------------------
#   ./scripts/deprovision.sh <csp|all> <database|bucket|vm|all> [options]
#
#   Everything beetleenv deletes, it deletes from here. status.sh only reports
#   and provision.sh only creates.
#
#   THE NETWORK IS NOT A RESOURCE HERE EITHER. It is not created by name and it
#   is not deleted by name: every run ends by checking whether anything of ours
#   is still inside the vNet, and removing it once nothing is. Which is the only
#   safe rule - deleting the vNet while a database sits in it would either be
#   refused by the CSP or strand the database.
#
#   THE NAMESPACE IS NEVER DELETED. up.sh creates it once and it outlives every
#   resource in it, so a teardown can be followed straight by another provision -
#   and anything else sharing the namespace is unaffected either way.
#
#   What exists is read from the list APIs, not from state/. cb-tumblebug is the
#   record of what is really there; a state file that was never written, or was
#   written by a run that then failed, must not be able to leave a database
#   running. Anything in the namespace named <prefix>-<csp>-* is ours; another
#   CSP's resources sit in the same namespace and are left alone.
#
#   Deleting is not symmetric with creating. The RDBMS delete endpoint has no
#   asynchronous mode - cm-beetle declares `Prefer: respond-async` on the infra
#   and object storage deletes and not on that one - so it is sent synchronously
#   and the call simply takes as long as the CSP does.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/recommend.sh
. "${SCRIPT_DIR}/lib/recommend.sh"
# shellcheck source=./lib/namespace.sh
. "${SCRIPT_DIR}/lib/namespace.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/deprovision.sh <csp|all> <resource> [options]

CSP:
  <csp>       one CSP
  all         every CSP in BEETLEENV_CSPS

Resources:
  database    every managed RDBMS instance named <prefix>-<csp>-db-*
  bucket      the object storage bucket
  vm          the VM infrastructure and its SSH key
  all         vm, then database, then bucket

The vNet and its security group are not listed: every run removes them once no
database and no VM of ours is left inside them, and keeps them while one is.
Running "all" with nothing left is therefore how a stranded vNet is cleaned up.

Options:
  --engine <engine>   database only: delete just this engine's instance
  --force             pass option=force to the delete calls, for a resource the
                      CSP has already removed but cb-tumblebug still lists.
                      It drops the cb-tumblebug record without touching the CSP,
                      so on a VM that is actually running it leaves the instance
                      up and billing with nothing left to delete it by. Without
                      this flag a VM is terminated first, which is what you want.
                      A bucket is the exception and needs no flag: option=force
                      empties it there, so a non-empty bucket is retried with it
                      automatically.
  --yes               skip the confirmation that "all all" asks for

The namespace is never deleted, whatever is passed here.
These resources cost money. Deleting them is the normal end of a session.

Examples:
  ./scripts/deprovision.sh aws database --engine mysql
  ./scripts/deprovision.sh aws all
  ./scripts/deprovision.sh all all
EOF
}

CSP_ARG=""
RESOURCE=""
ENGINE=""
FORCE=0
ASSUME_YES=0

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --engine)  ENGINE="${2:-}"; shift ;;
        --force)   FORCE=1 ;;
        --yes|-y)  ASSUME_YES=1 ;;
        -*)        usage >&2; die "unknown option: $1" ;;
        *)
            if   [ -z "$CSP_ARG" ];  then CSP_ARG="$1"
            elif [ -z "$RESOURCE" ]; then RESOURCE="$1"
            else usage >&2; die "unexpected argument: $1"
            fi
            ;;
    esac
    shift
done

if [ -z "$CSP_ARG" ] || [ -z "$RESOURCE" ]; then
    usage >&2
    exit 1
fi

case "$RESOURCE" in
    database|bucket|vm|all) ;;
    network)
        die "'network' is not a resource you delete directly.
       The vNet and its security group are removed automatically once no database
       and no VM of ours is left inside them, at the end of every run here.
       To clear a vNet that outlived everything else:
         ./scripts/deprovision.sh ${CSP_ARG} all" ;;
    *) usage >&2; die "unknown resource: ${RESOURCE}" ;;
esac

preflight

# CSPS is what the loop at the bottom walks. "all" is not a CSP name, so it is
# resolved before validate_csp sees it.
if [ "$CSP_ARG" = "all" ]; then
    CSPS="${BEETLEENV_CSPS:-}"
    if [ -z "$CSPS" ]; then
        die "BEETLEENV_CSPS is empty, so 'all' selects nothing"
    fi
    for c in $CSPS; do validate_csp "$c"; done
else
    validate_csp "$CSP_ARG"
    CSPS="$(csp_lower "$CSP_ARG")"
fi

require_ns

NS_PATH="$(urlq "$BEETLEENV_NS")"
FORCE_Q=""
if [ "$FORCE" -eq 1 ]; then FORCE_Q="?option=force"; fi

# INFRA_Q — the infrastructure delete needs option=terminate, and beetleenv has
#   to send it explicitly.
#
#   cb-tumblebug only deletes an infrastructure that is already Terminated (or
#   Failed/Undefined/Preparing/Prepared/Empty). A Running one is refused outright:
#     "Infra %s is Running:1 (R:1/1), which is not directly deletable.
#      Use option=terminate to safely clean it up"
#   option=terminate is what refines and terminates the nodes first, then deletes
#   (manageInfo.go DelInfra), which is the ordinary path for a VM that is up -
#   and every VM beetleenv creates is up.
#
#   cm-beetle documents this option as default(terminate), but that is the
#   swagger annotation only: the handler reads c.QueryParam("option"), accepts an
#   empty string, and passes it through unchanged
#   (controller/migration.go DeleteInfra). So the default never applies and the
#   delete fails on a running VM unless the option is on the URL.
#
#   --force still wins, and still means what it says: it drops the cb-tumblebug
#   records without terminating anything on the CSP, so the instance keeps
#   running and keeps billing. That is why it is not the default here.
INFRA_Q="?option=terminate"
if [ "$FORCE" -eq 1 ]; then INFRA_Q="$FORCE_Q"; fi

# CSP and OUR_PREFIX are set per iteration by the loop at the bottom; the
# deprovision_* functions read them as globals.
CSP=""
OUR_PREFIX=""

# ours <name> — a resource beetleenv created for THIS CSP. Anything else in the
#   namespace - another CSP's resources included - is left alone, which is what
#   makes a per-CSP teardown safe in a shared namespace.
ours() {
    case "$1" in
        "${OUR_PREFIX}"*) return 0 ;;
        *) return 1 ;;
    esac
}

# delete_try <label> <path> — the delete itself, without the verdict: 0 when the
#   resource is gone, or was already, and 1 when it is still there.
#
#   Split out of delete_one for the one caller that has a second call to make -
#   the bucket's option=force retry below - and has to see why the first one was
#   refused before anything is reported as a failure.
delete_try() {
    local label="$1" path="$2"
    log_step "deleting ${label}"
    if bt_delete_retry "$path" "$label"; then
        log_ok "deleted ${label}"
        return 0
    fi
    bt_absent && { log_info "${label} is already gone"; return 0; }
    return 1
}

# delete_failed <label> — report a delete that nothing else is going to retry.
delete_failed() {
    log_error "could not delete $1 - $(bt_message)"
    # The summary at the end reprints the one-line form; this is the rest of it,
    # once, where a CSP's reason runs past bt_message's cut.
    bt_report_message
    record_delete_failure "$1" "$(bt_message)"
}

# delete_one <label> <path> — a delete that treats "already gone" as done.
delete_one() {
    delete_try "$1" "$2" && return 0
    delete_failed "$1"
    return 1
}

# ------------------------------------------------------------------------------

deprovision_database() {
    local ids id engine_of count=0
    local strip="${OUR_PREFIX}db-"

    if ! bt_get "/migration/middleware/ns/${NS_PATH}/rdbms"; then
        log_warn "could not list managed RDBMS instances - $(bt_message)"
        return 0
    fi
    ids="$(bt_jq -r '.rdbms[]? | .id // .name')"

    for id in $ids; do
        ours "$id" || continue
        if [ -n "$ENGINE" ] && [ "$id" != "${strip}$(csp_lower "$ENGINE")" ]; then
            continue
        fi
        count=$((count + 1))
        # No Prefer header here: this endpoint has no asynchronous mode, so the
        # call is held open for as long as the CSP takes to tear the instance
        # down - minutes on AWS, longer on NCP.
        log_info "this call waits for the CSP; a managed RDBMS takes several minutes to remove"
        if delete_one "RDBMS ${id}" "/migration/middleware/ns/${NS_PATH}/rdbms/$(urlq "$id")${FORCE_Q}"; then
            engine_of="${id#"$strip"}"
            state_delete "$CSP" "db-${engine_of}"
        fi
    done

    if [ "$count" -eq 0 ]; then
        log_info "no managed RDBMS instance of ours in ${BEETLEENV_NS}"
    fi
}

# delete_bucket <id> — the plain delete first, option=force second.
#
#   A bucket that was migrated into is not empty, and a plain DELETE on a bucket
#   with objects in it is refused - cb-spider answers 409 BucketNotEmpty, "Use
#   force=true parameter to force delete". So for the resource this environment
#   actually creates, the forced call is the normal path out, not a last resort.
#
#   option=force means something else on a bucket than it does on a VM, which is
#   why it is used here without --force being asked for. On an infrastructure it
#   drops cb-tumblebug's record and leaves the instance running and billing; on
#   an object storage cb-spider's ForceEmptyAndDeleteBucket aborts incomplete
#   multipart uploads, removes every object, version and delete marker, checks
#   the bucket is empty and only then deletes it. Nothing is stranded.
#
#   What force does drop is cb-tumblebug's check that the bucket really went from
#   the CSP (DeleteObjectStorage, objectStorage.go), and cb-spider deletes its
#   metadata even if the final RemoveBucket failed - so a 204 from the forced call
#   proves the record is gone, not the bucket. That is why the plain call is still
#   tried first: when it succeeds, the deletion is confirmed.
#
#   ⚠ cb-spider empties one object at a time inside a single 180s context, so a
#     bucket much larger than a test seed will time out there rather than here.
delete_bucket() {
    local id="$1" label="bucket ${id}"
    local path="/migration/middleware/ns/${NS_PATH}/objectStorage/$(urlq "$id")"

    delete_try "$label" "${path}${FORCE_Q}" && return 0

    # Either force was on the call already, or this is not the refusal force
    # answers - a bucket the CSP is still releasing, say, which bt_delete_retry
    # has already waited out.
    if [ "$FORCE" -eq 1 ] || ! bt_bucket_not_empty; then
        delete_failed "$label"
        return 1
    fi

    log_info "${label} still holds objects - asking again with option=force, which empties it first"
    delete_one "$label" "${path}?option=force" || return 1
    log_warn "option=force skips cb-tumblebug's confirmation that the CSP released the bucket"
    return 0
}

deprovision_bucket() {
    local ids id count=0

    if ! bt_get "/migration/middleware/ns/${NS_PATH}/objectStorage"; then
        log_warn "could not list object storages - $(bt_message)"
        return 0
    fi
    ids="$(bt_jq -r '.objectStorage[]? | .id // .name')"

    for id in $ids; do
        ours "$id" || continue
        count=$((count + 1))
        if delete_bucket "$id"; then
            state_delete "$CSP" bucket
        fi
    done

    if [ "$count" -eq 0 ]; then
        log_info "no bucket of ours in ${BEETLEENV_NS}"
    fi
}

deprovision_vm() {
    local ids id count=0 stuck=0

    if ! bt_get "/migration/ns/${NS_PATH}/infra"; then
        log_warn "could not list infrastructures - $(bt_message)"
        return 0
    fi
    ids="$(bt_jq -r '.infra[]? | .id // .name')"

    for id in $ids; do
        ours "$id" || continue
        count=$((count + 1))
        # Terminating the nodes is part of this call, so it runs for as long as
        # the CSP takes to stop them - minutes, and cb-tumblebug allows up to an
        # hour for a large infrastructure.
        if delete_one "infra ${id}" "/migration/ns/${NS_PATH}/infra/$(urlq "$id")${INFRA_Q}"; then
            state_delete "$CSP" vm
        else
            stuck=1
        fi
    done

    if [ "$count" -eq 0 ]; then
        log_info "no VM infrastructure of ours in ${BEETLEENV_NS}"
    fi

    # The SSH key outlives the infrastructure it was made for, and a later
    # provision would reuse it - so it goes with the VM rather than with the
    # network, which nothing else references.
    #
    # Not attempted while an infrastructure is still standing. cb-tumblebug
    # refuses a key its nodes reference - "still referenced by 1 object(s)" - so
    # trying anyway turns one failure into two, and the second one describes the
    # first rather than anything new. Same rule the vNet follows below.
    if [ "$stuck" -eq 1 ]; then
        log_info "keeping the SSH key - the infrastructure that references it is still there"
        return 0
    fi

    if bt_get "/migration/ns/${NS_PATH}/resources/sshKey"; then
        local key
        key="$(bt_jq -r --arg n "$(resource_name "$CSP" sshkey)" '.sshKey[]? | select(.name == $n) | .id // empty')"
        if [ -n "$key" ]; then
            delete_one "sshKey ${key}" "/migration/ns/${NS_PATH}/resources/sshKey/$(urlq "$key")" || true
        fi
    fi

    # A key file conn-info.sh --ssh saved opens nothing now, and leaving it would
    # have the next provision's ssh command silently offer the old key. It goes
    # here rather than with the network because it is the key pair's own copy.
    #
    # Only this setup's key: the file is named after the sshKeyId, which carries
    # the prefix, and another prefix's key can sit in the same directory.
    local key_file
    key_file="$(key_path "$CSP" "$(resource_name "$CSP" sshkey)")"
    if [ "$(key_delete "$CSP" "$(resource_name "$CSP" sshkey)")" = "removed" ]; then
        log_ok "removed the saved private key ${key_file#"${BEETLEENV_ROOT}/"}"
    fi
}

# ------------------------------------------------------------------------------
# network — internal, and last
# ------------------------------------------------------------------------------

# network_dependents — the databases and VMs of ours still in the namespace, as
#   one line for the log. Buckets are deliberately not counted: an object storage
#   bucket sits outside the vNet and nothing about it needs one.
#
#   NET_DEPENDENTS_KNOWN is 0 when a list call failed. An unread list is not an
#   empty one, and the difference decides whether a vNet gets deleted, so it is
#   reported separately rather than folded into "nothing left".
NET_DEPENDENTS=""
NET_DEPENDENTS_KNOWN=1

network_dependents() {
    local part
    NET_DEPENDENTS=""
    NET_DEPENDENTS_KNOWN=1

    if bt_get "/migration/middleware/ns/${NS_PATH}/rdbms"; then
        NET_DEPENDENTS="$(bt_jq -r --arg p "$OUR_PREFIX" \
            '[ .rdbms[]? | select((.id // .name) | startswith($p)) | .id // .name ] | join(", ")')"
    else
        NET_DEPENDENTS_KNOWN=0
    fi

    if bt_get "/migration/ns/${NS_PATH}/infra"; then
        part="$(bt_jq -r --arg p "$OUR_PREFIX" \
            '[ .infra[]? | select((.id // .name) | startswith($p)) | .id // .name ] | join(", ")')"
        if [ -n "$part" ]; then
            NET_DEPENDENTS="${NET_DEPENDENTS}${NET_DEPENDENTS:+, }${part}"
        fi
    else
        NET_DEPENDENTS_KNOWN=0
    fi
}

# release_network_if_unused — the last thing every run does.
#
#   The vNet is shared by the database and the VM, so no single delete can decide
#   its fate: "delete the network" is not a step in a teardown, it is what is true
#   once the teardown has emptied it. Whichever resource leaves last takes it.
#
#   Deciding from the list APIs rather than from state/ matters most here. A
#   database that state never recorded is still a database in that vNet, and
#   deleting the vNet around it would either be refused by the CSP or strand it.
release_network_if_unused() {
    local rc=0
    load_network "$CSP" || rc=$?
    if [ "$rc" -eq 2 ]; then
        log_warn "keeping the network record - could not list vNets - $(bt_message)"
        return 0
    fi
    if [ "$rc" -ne 0 ]; then
        # No vNet of ours - removed outside beetleenv, or by a tumblebug reset.
        # The record describes nothing now, so it goes like any other resource
        # found already gone.
        state_delete "$CSP" network
        return 0
    fi

    network_dependents

    if [ "$NET_DEPENDENTS_KNOWN" -eq 0 ]; then
        log_warn "keeping the network - could not read what is still inside it"
        return 0
    fi
    if [ -n "$NET_DEPENDENTS" ]; then
        log_info "keeping the network - still used by: ${NET_DEPENDENTS}"
        return 0
    fi

    if release_network "$CSP"; then
        state_delete "$CSP" network
    fi
}

# ------------------------------------------------------------------------------
# Confirmation
# ------------------------------------------------------------------------------
# Only for "all all". A named CSP or a named resource is already an explicit
# statement of what to delete; "everything, everywhere" is the one that can be
# typed by mistake and take another CSP's environment with it.

if [ "$CSP_ARG" = "all" ] && [ "$RESOURCE" = "all" ] && [ "$ASSUME_YES" -eq 0 ]; then
    printf '\n'
    log_warn "this deletes every ${BEETLEENV_NAME_PREFIX}-<csp>-* resource on: ${CSPS}"
    printf '  namespace %s is kept.\n' "$BEETLEENV_NS"
    printf '  Type yes to continue: '
    read -r answer
    if [ "$answer" != "yes" ]; then
        die "cancelled"
    fi
fi

# ------------------------------------------------------------------------------

for CSP in $CSPS; do
    OUR_PREFIX="$(name_prefix_of "$CSP")"

    if [ "$CSP_ARG" = "all" ]; then
        printf '\n'
        log_step "${CSP}"
    fi

    case "$RESOURCE" in
        database) deprovision_database ;;
        bucket)   deprovision_bucket ;;
        vm)       deprovision_vm ;;
        all)
            # Order matters: everything that sits in the vNet has to go before
            # the network sweep below, and the bucket is independent of both.
            deprovision_vm
            deprovision_database
            deprovision_bucket
            ;;
    esac

    # Every path ends here, including "bucket" and a run that deleted nothing:
    # that is what makes "no orphaned vNet after a deprovision" true in general,
    # and what lets an already-empty "all" clean up a stranded one.
    release_network_if_unused
done

printf '\n'
if report_delete_failures; then
    log_ok "${CSP_ARG} ${RESOURCE} deprovisioned. Namespace ${BEETLEENV_NS} is unchanged."
else
    exit 1
fi
