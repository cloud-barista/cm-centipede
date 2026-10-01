#!/usr/bin/env bash
# ==============================================================================
# deprovision.sh — delete resources (tofu destroy)
# ------------------------------------------------------------------------------
#   ./scripts/deprovision.sh <csp> <resource>
#     aws : bucket | vm | database
#     ncp : bucket | vm | database
#
#   Examples:
#     ./scripts/deprovision.sh aws database
#     ./scripts/deprovision.sh ncp database
#
#   One environment per prefix:
#     Only the workspace of the prefix .env sets is destroyed (see
#     scripts/lib/workspace.sh). To remove an environment made under another prefix,
#     put that prefix in .env and run this again. A workspace the destroy empties is
#     deleted, so ./scripts/list.sh stops listing it.
#
#   Retries:
#     A failed destroy is retried once, 60 seconds later (2 attempts in total). NCP
#     answers some delete calls with 500 / returnCode 1300 while the server the
#     resource belongs to is still being released, and the same call then succeeds.
#     If the second attempt fails too, the error is printed again at the end and the
#     script exits non-zero.
#
#   Stale NCP login keys:
#     A login key is the one failure that does not recover that way, and it is not
#     retried at all. When a destroy has removed everything except ncloud_login_key,
#     NCP keeps answering 500 / returnCode 1300 to the delete call - observed
#     unchanged over 12 minutes, and calling deleteLoginKeys directly answers the
#     same - so waiting only delays an identical failure and leaves the module stuck,
#     blocking the tofu/ncp/network cleanup. Recovery is immediate and runs in two
#     reported steps: drop the key from state, then re-run destroy to clear the
#     outputs.
#     Forgetting a key that may still exist is safe here because login key names carry
#     a random suffix (see random_id in tofu/ncp/vm and tofu/ncp/database): the next
#     provisioning asks for a different name, so a leftover cannot collide with it.
#
#   NCP notes:
#     - ncp/bucket is emptied before it is destroyed. ncloud_objectstorage_bucket
#       has no force_destroy the way aws_s3_bucket does, and DeleteBucket fails
#       with 409 BucketNotEmpty while any object is left, so gendata deletes them
#       first (./scripts/gen-data.sh --target bucket --cleanup, needs go).
#     - tofu/ncp/network is handled automatically, mirroring provision.sh. Only
#       tofu/ncp/vm and tofu/ncp/database resolve it by name, so once neither of
#       them holds state in the prefix any more this script destroys its network too.
#     - A module that tracks no resource is skipped rather than destroyed: the
#       destroy would be a no-op that still costs a full plan, and on NCP that plan
#       reads the by-name lookups.
#     - If NCP has not finished releasing the servers yet, destroying the network
#       can fail. That is reported as a warning, not an error: the requested
#       resource is already gone, and re-running this command converges.
#     - The ncloud provider hard-codes its delete wait (MySQL 5 min,
#       PostgreSQL/MongoDB 10 min) and does not support a timeouts block.
#       If the wait times out the delete API call has already been sent, so
#       simply re-running this command converges.
# ==============================================================================
set -euo pipefail

RUNNER="tofuenv-runner"
GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'

usage() {
    cat >&2 <<'EOF'
Usage: deprovision.sh <csp> <resource>
  aws : bucket | vm | database
  ncp : bucket | vm | database

  ncp/network is destroyed automatically once neither vm nor database remains.
EOF
    exit 1
}

CSP="${1:-}"; RESOURCE="${2:-}"
[ -z "$CSP" ] || [ -z "$RESOURCE" ] && usage

case "$CSP" in
    aws|ncp) ;;
    *) echo "invalid csp: $CSP (supported: aws, ncp)" >&2; usage ;;
esac

case "$RESOURCE" in
    bucket|vm|database) ;;
    *) echo "invalid resource for ${CSP}: $RESOURCE" >&2; usage ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE="tofu/${CSP}/${RESOURCE}"

if [ ! -d "$ROOT_DIR/$MODULE" ]; then
    echo -e "${RED}module not found: $MODULE${NC}" >&2; exit 1
fi
if ! docker ps --format '{{.Names}}' | grep -q "^${RUNNER}$"; then
    echo -e "${RED}The ${RUNNER} container is not running. Run ./scripts/up.sh first.${NC}" >&2; exit 1
fi

# shellcheck source=./lib/workspace.sh
. "$SCRIPT_DIR/lib/workspace.sh"
ws_load "$CSP"

# has_managed_state <module> — true when the module still tracks a real resource.
#   Data sources are filtered out: a module can hold nothing but its vault lookup, and
#   destroying that is a no-op that still costs a full plan. Outputs alone are not a
#   reliable signal here, since a half-destroyed module keeps resources without them.
has_managed_state() {
    local mod="$1" out
    out="$(ws_exec bash -c '
        cd "/work/'"$mod"'" 2>/dev/null || exit 0
        tofu state list 2>/dev/null || true
    ' | grep -v '^data\.' || true)"
    [ -n "$(printf %s "$out" | tr -d '[:space:]')" ]
}

# managed_state <module> — the module's managed resource addresses, one per line.
managed_state() {
    local mod="$1"
    ws_exec bash -c '
        cd "/work/'"$mod"'" 2>/dev/null || exit 0
        tofu state list 2>/dev/null || true
    ' | grep -v '^data\.' || true
}

# login_keys_only <module> — true when the only CSP resource left is a login key, i.e.
#   the destroy got everything else and stopped on the key alone.
#   random_/tls_/local_ entries are ignored. They exist only in state, are destroyed
#   without any API call, and are still listed here purely because the login key that
#   depends on them could not go first - they are never why a destroy is stuck. The
#   random_id that makes the key name unique is exactly such an entry.
LOCAL_ONLY_PREFIXES='^(random_|tls_|local_)'
login_keys_only() {
    local mod="$1" left
    left="$(managed_state "$mod" | grep -vE "$LOCAL_ONLY_PREFIXES" || true)"
    [ -n "$left" ] && ! printf '%s\n' "$left" | grep -qv '^ncloud_login_key\.'
}

# has_csp_state <module> — true when the module still holds a resource that exists at
#   the CSP. Used to decide whether anything still needs tofu/ncp/network: a leftover
#   random_id or generated key file lives only in state, does not sit in the VPC, and
#   must not keep the network alive on its own.
has_csp_state() {
    [ -n "$(managed_state "$1" | grep -vE "$LOCAL_ONLY_PREFIXES" || true)" ]
}

# login_key_addrs <module> — the ncloud_login_key addresses in state.
login_key_addrs() {
    managed_state "$1" | grep '^ncloud_login_key\.' || true
}

# login_key_name <module> <address> — the key_name NCP knows this resource by.
login_key_name() {
    local mod="$1" addr="$2"
    ws_exec bash -c "
        cd /work/${mod}
        tofu state show -no-color '${addr}' 2>/dev/null || true
    " | sed -n 's/^ *key_name *= *"\(.*\)"$/\1/p' | head -1
}

# drop_login_keys <module> — forget the login keys, reporting each name so the leftover
#   can be found in the console.
drop_login_keys() {
    local mod="$1" addr key
    while read -r addr; do
        [ -n "$addr" ] || continue
        key="$(login_key_name "$mod" "$addr")"
        echo "  tofu state rm ${addr}${key:+   (${key})}"
        ws_exec bash -c '
            cd "/work/'"$mod"'"
            tofu state rm "'"$addr"'"
        ' >/dev/null
    done <<< "$(login_key_addrs "$mod")"
}

# empty_bucket — delete every object in the NCP bucket, so the destroy can run.
#   ncloud_objectstorage_bucket has no force_destroy (tofu/aws/bucket sets one on
#   aws_s3_bucket, which is why only NCP needs this), and the S3 API answers
#   DeleteBucket with 409 BucketNotEmpty while a single object is left:
#     Error: DELETING ERROR
#     operation error S3: DeleteBucket, https response error StatusCode: 409,
#     api error BucketNotEmpty: The bucket you tried to delete is not empty.
#   gendata is what speaks S3 here - nothing in the runner image does - so the
#   emptying goes through it, and it needs go on the host like gen-data.sh does.
#   Not being able to empty the bucket is reported and then left to the destroy:
#   an already empty bucket is deleted regardless, and if it was not empty the
#   error above says so precisely.
empty_bucket() {
    echo -e "${CYAN}=== emptying the bucket first ===${NC}"
    echo "  ncloud_objectstorage_bucket has no force_destroy, so NCP refuses to"
    echo "  delete a bucket that still holds objects."
    if ! command -v go >/dev/null 2>&1; then
        echo -e "${YELLOW}  go is not installed, so gendata cannot run - skipping this step.${NC}" >&2
        manual_empty_hint
        return 0
    fi
    if ! "$SCRIPT_DIR/gen-data.sh" --provider "$CSP" --target bucket --cleanup; then
        echo -e "${YELLOW}  Could not empty the bucket; destroying anyway.${NC}" >&2
        manual_empty_hint
    fi
    echo
}

# manual_empty_hint — what to do when the bucket could not be emptied from here.
manual_empty_hint() {
    echo -e "${YELLOW}  If the destroy below fails with BucketNotEmpty, empty the bucket by hand${NC}" >&2
    echo -e "${YELLOW}  (NCP console > Object Storage > the bucket > delete every object,${NC}" >&2
    echo -e "${YELLOW}  incomplete multipart uploads included) and re-run this command.${NC}" >&2
}

# destroy_module <module> — init + destroy inside the runner
destroy_module() {
    local mod="$1"
    ws_exec bash -c '
        set -euo pipefail
        set -a; . /work/.env; set +a
        export VAULT_ADDR=http://openbao:8200
        mkdir -p /work/.tofu-plugin-cache /work/ssh_keys
        cd "/work/'"$mod"'"
        '"$WS_INIT"'
        tofu destroy -auto-approve
    '
}

# destroy_with_retry <module> [report_error] — destroy, retrying once after a pause.
#   NCP answers some delete calls with 500 / returnCode 1300 ("Temporarily out of
#   service") while the server the resource belongs to is still being released; the
#   very same call succeeds a moment later. Output is streamed as it happens and also
#   captured, so the failure can be repeated at the end where it is easy to see.
#
#   A login key is the exception and is not retried at all. Once it is the only thing
#   left, the same 1300 keeps coming back - observed unchanged over 12 minutes, and
#   calling deleteLoginKeys directly answers the same - so a retry only spends a
#   minute to fail identically. The caller drops the key from state instead.
# Retries AFTER the first attempt, so 1 means the destroy runs at most twice.
DESTROY_RETRIES=1
DESTROY_RETRY_WAIT=60

destroy_with_retry() {
    local mod="$1" report="${2:-yes}" retry=0 log rc
    log="$(mktemp)"
    while true; do
        rc=0
        destroy_module "$mod" 2>&1 | tee "$log" || rc=$?
        if [ "$rc" -eq 0 ]; then
            rm -f "$log"
            return 0
        fi
        # Nothing but the login key left: waiting will not change the answer, so
        # hand back to the stale-key recovery without burning the retry.
        if [ "$CSP" = "ncp" ] && login_keys_only "$mod"; then
            rm -f "$log"
            return "$rc"
        fi
        if [ "$retry" -ge "$DESTROY_RETRIES" ]; then
            if [ "$report" = "yes" ]; then
                echo >&2
                echo -e "${RED}=== destroy failed, ${retry} retry/retries used: ${mod} ===${NC}" >&2
                grep -E 'Error|^│|^╷|^╵' "$log" >&2 || tail -n 20 "$log" >&2
            fi
            rm -f "$log"
            return "$rc"
        fi
        retry=$((retry + 1))
        echo
        echo -e "${YELLOW}destroy failed — retry ${retry}/${DESTROY_RETRIES} in ${DESTROY_RETRY_WAIT}s${NC}" >&2
        sleep "$DESTROY_RETRY_WAIT"
    done
}

echo -e "${CYAN}=== deprovision(destroy): ${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) ===${NC}"

# Skip a module that tracks nothing but data sources. Destroying it is a no-op that
# still runs a full plan - and on NCP that plan reads the by-name lookups.
if has_managed_state "$MODULE"; then
    if [ "$CSP" = "ncp" ] && [ "$RESOURCE" = "bucket" ]; then
        empty_bucket
    fi

    RC=0
    destroy_with_retry "$MODULE" || RC=$?

    # A destroy that got everything except the login key is the known NCP case: the key
    # is already gone from the console, but DeleteLoginKey answers 500 / returnCode 1300
    # instead of "not found", so tofu never gets to mark it destroyed. There is nothing
    # to wait for - the answer has been seen unchanged over 12 minutes, and destroy_with_retry
    # skips its retry for this very reason - so the key is dropped from state right away,
    # which empties the module and lets the network cleanup below run.
    if [ "$RC" -ne 0 ] && [ "$CSP" = "ncp" ] && login_keys_only "$MODULE"; then
        echo
        echo -e "${CYAN}=== stale login key recovery: ${MODULE} ===${NC}"
        echo "  Everything else is destroyed. What is left:"
        managed_state "$MODULE" | sed 's/^/    /'
        echo

        echo -e "${YELLOW}[1/2] dropping the key from state${NC}"
        drop_login_keys "$MODULE"
        echo -e "${GREEN}      state updated.${NC}"
        echo -e "${YELLOW}      The key above may still exist at NCP. Login key names carry a${NC}"
        echo -e "${YELLOW}      random suffix, so it blocks nothing, but it stays in the account:${NC}"
        echo -e "${YELLOW}      delete it under Server > Login Key when convenient.${NC}"
        echo

        # Nothing is left to destroy; this run only clears the module's outputs so the
        # next provision.sh does not read them as a provisioned resource.
        echo -e "${YELLOW}[2/2] re-running destroy to clear the module's outputs${NC}"
        RC=0
        destroy_with_retry "$MODULE" || RC=$?
        if [ "$RC" -eq 0 ]; then
            echo -e "${GREEN}      module is empty.${NC}"
        fi
        echo
    fi

    [ "$RC" -eq 0 ] || exit "$RC"
    echo -e "${GREEN}=== deleted: ${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) ===${NC}"
else
    echo -e "${YELLOW}${CSP}/${RESOURCE} (prefix ${WS_PREFIX}) holds no resources - nothing to destroy.${NC}"
fi
ws_drop "$MODULE"

# tofu/ncp/network is only referenced by ncp/vm and ncp/database. Once neither of
# them holds state in this prefix, nothing needs its VPC any more, so clean it up as
# well. Other prefixes have networks of their own and are not looked at.
if [ "$CSP" = "ncp" ] && { [ "$RESOURCE" = "vm" ] || [ "$RESOURCE" = "database" ]; }; then
    if has_managed_state "tofu/ncp/network"; then
        if has_csp_state "tofu/ncp/vm" || has_csp_state "tofu/ncp/database"; then
            echo -e "${YELLOW}Keeping ncp/network: another module still uses it.${NC}"
            # Guarded with if, not `cmd && echo`: under set -e a false AND-list is the
            # statement's exit status and would end the script here.
            if has_csp_state "tofu/ncp/vm"; then
                echo "  - ncp/vm is still provisioned"
            fi
            if has_csp_state "tofu/ncp/database"; then
                echo "  - ncp/database is still provisioned"
            fi
        else
            echo
            echo -e "${CYAN}=== nothing uses ncp/network any more — destroying it ===${NC}"
            # Not fatal: the requested resource is already gone, and NCP sometimes
            # needs a moment before it lets the subnet or VPC go. The error is not
            # reported twice - the warning below says what to do about it.
            if destroy_with_retry "tofu/ncp/network" no; then
                ws_drop "tofu/ncp/network"
                echo -e "${GREEN}=== deleted: ncp/network (prefix ${WS_PREFIX}) ===${NC}"
            else
                echo -e "${YELLOW}Warning: ncp/network could not be destroyed yet.${NC}" >&2
                echo "  NCP may still be releasing the servers. Re-run this command in a few minutes:" >&2
                echo "    ./scripts/deprovision.sh ncp ${RESOURCE}" >&2
            fi
        fi
    fi
fi
