#!/usr/bin/env bash
# ==============================================================================
# conn-info.sh — how to reach what beetleenv created, and what to call it
# ------------------------------------------------------------------------------
#   ./scripts/conn-info.sh <csp> [--reveal] [--ssh] [--ids] [--json]
#
#   Everything is read live from cb-tumblebug through cm-beetle, so what is shown
#   is what exists, not what was once created.
#
# THREE LAYERS OF IDENTIFIER
#   Every resource carries all three, and they are for different things:
#
#     id               the cm-beetle API path parameter - nsId, rdbmsId, osId,
#                      infraId, nodeId, vNetId, sgId, sshKeyId. This is what goes
#                      into /migration/ns/{nsId}/infra/{infraId}.
#     uid              cb-tumblebug's internal handle. For a bucket it is also
#                      the real name in the CSP, because tumblebug generates the
#                      bucket name rather than passing ours through.
#     cspResourceId    what the CSP console shows - vpc-0abc…, i-0abc…, db-ABC…
#     cspResourceName  the same for resources named rather than numbered.
#
#   The labels in the output are the API parameter names on purpose: what is
#   printed beside "infraId" is exactly what an infraId path segment takes.
#
#   Secrets are masked by default. --reveal prints the database password (out of
#   .env, not out of any API - cb-tumblebug does not hand it back) and the VM's
#   private key, which is the only way to log in to the VM. --ids and --json
#   never print either, whatever else is passed.
#
# LOGGING IN — --ssh
#   --ssh writes each node's private key to keys/<ns>/<prefix>/<csp>/<sshKeyId>.pem (0600) and
#   prints the ssh, scp and rsync commands for that node with every value filled
#   in. The key goes to a file rather than to stdout on purpose: --reveal leaves
#   it in the terminal scrollback, and in anything that tees the output, on disk
#   in a world-readable log.
#
#   The key is fetched per node, from the node's own sshKeyId, so a namespace
#   holding more than one infrastructure pairs each node with the key that
#   actually opens it.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/beetle.sh
. "${SCRIPT_DIR}/lib/beetle.sh"
# shellcheck source=./lib/namespace.sh
. "${SCRIPT_DIR}/lib/namespace.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/conn-info.sh <csp> [--reveal] [--ssh] [--ids] [--json]

  --reveal   print the RDBMS password and the VM private key in full
  --ssh      save each node's private key under keys/ (0600) and print the
             ssh / scp / rsync commands for it, with every value filled in
  --ids      just the cm-beetle API identifiers, for pasting into a call
  --json     machine-readable output, with all three identifier layers

--ids and --json never print secrets, whatever else is passed.
--ssh writes the key to a file instead of printing it.
EOF
}

CSP=""
REVEAL=0
AS_JSON=0
AS_IDS=0
AS_SSH=0

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --reveal)  REVEAL=1 ;;
        --ssh)     AS_SSH=1 ;;
        --json)    AS_JSON=1 ;;
        --ids)     AS_IDS=1 ;;
        -*)        usage >&2; die "unknown option: $1" ;;
        *)
            if [ -z "$CSP" ]; then CSP="$1"
            else usage >&2; die "unexpected argument: $1"
            fi
            ;;
    esac
    shift
done

if [ -z "$CSP" ]; then usage >&2; exit 1; fi
if [ "$AS_JSON" -eq 1 ] && [ "$AS_IDS" -eq 1 ]; then
    die "--ids and --json are two ways to ask the same question; pass one"
fi
if [ "$AS_SSH" -eq 1 ] && { [ "$AS_JSON" -eq 1 ] || [ "$AS_IDS" -eq 1 ]; }; then
    die "--ssh writes a key and prints commands, which --ids and --json do not do; pass one"
fi

preflight
validate_csp "$CSP"
CSP="$(csp_lower "$CSP")"
require_ns

NS_PATH="$(urlq "$BEETLEENV_NS")"
# Resources of ours on THIS CSP. One namespace holds every CSP's, so the filter
# has to be per CSP and not just per prefix.
OUR_PREFIX="$(name_prefix_of "$CSP")"

# ------------------------------------------------------------------------------
# One pass over the namespace
# ------------------------------------------------------------------------------
# Fetched once and rendered three ways. beetle paces its calls to cb-tumblebug,
# so a mode that re-fetched per section would be slower for no gain.

fetch() {
    if bt_get "$1"; then bt_payload; else printf '{}'; fi
}

# refresh_entries <json> <jq path> <key> <base path> — replace each entry of ours
#   with what a single-resource GET returns.
#
#   The list endpoints do not talk to the CSP. cb-tumblebug serves them from its
#   own key-value store (ListResource), while GetRDBMS and GetObjectStorage ask
#   cb-spider, overwrite endpoint, publicAccess and status from the live answer,
#   and write the record back. So the list is whatever was last observed, not
#   what is true.
#
#   That gap is visible whenever the CSP changes something on its own side. The
#   one that comes up here is the NCP public domain: it is issued from the
#   console, and until something reads the instance singly, this script keeps
#   printing the private domain as the endpoint - the one screen you would look
#   at to check the change is the one that does not show it.
#
#   Only entries of ours are refreshed, so the cost is one call per resource this
#   setup created, not per resource in the namespace.
refresh_entries() {
    local all="$1" path="$2" key="$3" base="$4"
    local ids id fresh

    ids="$(ours "$all" "$path" '.id // .name')"
    for id in $ids; do
        if ! bt_get "${base}/$(urlq "$id")"; then
            continue    # keep the listed entry; stale beats absent
        fi
        fresh="$(bt_payload)"
        if [ -z "$fresh" ] || [ "$fresh" = "null" ]; then
            continue
        fi
        all="$(printf '%s' "$all" | jq -c \
            --arg k "$key" --arg id "$id" --argjson new "$fresh" \
            '.[$k] = [ .[$k][]? | if (.id // .name) == $id then $new else . end ]' \
            2>/dev/null || printf '%s' "$all")"
    done
    printf '%s' "$all"
}

RDBMS_ALL="$(fetch "/migration/middleware/ns/${NS_PATH}/rdbms")"
BUCKET_ALL="$(fetch "/migration/middleware/ns/${NS_PATH}/objectStorage")"
INFRA_ALL="$(fetch "/migration/ns/${NS_PATH}/infra")"
VNET_ALL="$(fetch "/migration/ns/${NS_PATH}/resources/vNet")"
SG_ALL="$(fetch "/migration/ns/${NS_PATH}/resources/securityGroup")"
SSHKEY_ALL="$(fetch "/migration/ns/${NS_PATH}/resources/sshKey")"

# ours <json> <jq path> <filter body> — the entries of ours, as one jq run.
ours() {
    printf '%s' "$1" | jq -r --arg p "$OUR_PREFIX" \
        "${2}? | select((.id // .name) | startswith(\$p)) | ${3}" 2>/dev/null || true
}

# ours_json <json> <jq path> <object body> — the same, as a JSON array.
ours_json() {
    printf '%s' "$1" | jq -c --arg p "$OUR_PREFIX" \
        "[ ${2}? | select((.id // .name) | startswith(\$p)) | ${3} ]" 2>/dev/null || printf '[]'
}

# Called here rather than beside the fetches above because refresh_entries reads
# through ours(), and every render mode below wants the refreshed records.
RDBMS_ALL="$(refresh_entries "$RDBMS_ALL" '.rdbms[]' rdbms \
    "/migration/middleware/ns/${NS_PATH}/rdbms")"
BUCKET_ALL="$(refresh_entries "$BUCKET_ALL" '.objectStorage[]' objectStorage \
    "/migration/middleware/ns/${NS_PATH}/objectStorage")"
# The infrastructure list leaves nodeUserName null; the single read fills it in.
# That is the account the node actually accepts, and the one cm-centipede logs in
# as when it resolves a beetleSsh connection - it reads this same endpoint. Without
# the refresh every mode here would have to guess at the user, and the guess would
# disagree with centipede on any image that is not cb-tumblebug's default.
INFRA_ALL="$(refresh_entries "$INFRA_ALL" '.infra[]' infra \
    "/migration/ns/${NS_PATH}/infra")"

# ------------------------------------------------------------------------------
# --json
# ------------------------------------------------------------------------------

if [ "$AS_JSON" -eq 1 ]; then
    jq -n \
        --arg csp "$CSP" \
        --arg nsId "$BEETLEENV_NS" \
        --arg conn "$(connection_name "$CSP")" \
        --argjson rdbms "$(ours_json "$RDBMS_ALL" '.rdbms[]' '{
            rdbmsId: (.id // .name), uid: .uid,
            cspResourceId: .cspResourceId, cspResourceName: .cspResourceName,
            engine: .dbEngine, version: .dbEngineVersion,
            endpoint: .endpoint, adminUserName: .adminUserName,
            publicAccess: .publicAccess, status: .status }')" \
        --argjson buckets "$(ours_json "$BUCKET_ALL" '.objectStorage[]' '{
            osId: (.id // .name), uid: .uid,
            cspResourceId: .cspResourceId, cspResourceName: .cspResourceName,
            status: .status }')" \
        --argjson infra "$(ours_json "$INFRA_ALL" '.infra[]' '{
            infraId: (.id // .name), uid: .uid, status: .status,
            nodes: [ .node[]? | {
                nodeId: (.id // .name), uid: .uid,
                cspResourceId: .cspResourceId, cspResourceName: .cspResourceName,
                publicIP: .publicIP, privateIP: .privateIP,
                sshPort: .sshPort, userName: .nodeUserName,
                sshKeyId: .sshKeyId, status: .status } ] }')" \
        --argjson vnets "$(ours_json "$VNET_ALL" '.vNet[]' '{
            vNetId: (.id // .name), uid: .uid,
            cspResourceId: .cspResourceId, cspResourceName: .cspResourceName,
            cidrBlock: .cidrBlock, status: .status,
            subnets: [ .subnetInfoList[]? | {
                subnetId: (.id // .name), cspResourceId: .cspResourceId,
                ipv4_CIDR: .ipv4_CIDR, zone: .zone } ] }')" \
        --argjson sgs "$(ours_json "$SG_ALL" '.securityGroup[]' '{
            sgId: (.id // .name), uid: .uid,
            cspResourceId: .cspResourceId, vNetId: .vNetId }')" \
        --argjson keys "$(ours_json "$SSHKEY_ALL" '.sshKey[]' '{
            sshKeyId: (.id // .name), uid: .uid,
            cspResourceId: .cspResourceId, username: .username,
            fingerprint: .fingerprint }')" \
        '{csp: $csp, nsId: $nsId, connectionName: $conn,
          rdbms: $rdbms, objectStorage: $buckets, infra: $infra,
          vNet: $vnets, securityGroup: $sgs, sshKey: $keys}
         # Drop the keys the API did not fill in, the way the Go models do with
         # omitempty. A null here would mean "cb-tumblebug returned nothing for
         # this", which is what an absent key already says.
         | walk(if type == "object" then with_entries(select(.value != null)) else . end)'
    exit 0
fi

# ------------------------------------------------------------------------------
# --ids
# ------------------------------------------------------------------------------
# The API parameters and nothing else. Every line is <name> <value>, so a value
# is one cut away:  ./scripts/conn-info.sh aws --ids | awk '$1=="infraId"{print $2}'

if [ "$AS_IDS" -eq 1 ]; then
    id_rows() {
        local label="$1" values="$2" first=1 line
        if [ -z "$values" ]; then return 0; fi
        while IFS= read -r line; do
            [ -z "$line" ] && continue
            if [ "$first" -eq 1 ]; then
                printf '%-11s %s\n' "$label" "$line"
                first=0
            else
                printf '%-11s %s\n' "" "$line"
            fi
        done <<< "$values"
    }

    printf '%-11s %s\n' "nsId" "$BEETLEENV_NS"
    id_rows "rdbmsId"  "$(ours "$RDBMS_ALL"  '.rdbms[]'         '.id // .name')"
    id_rows "osId"     "$(ours "$BUCKET_ALL" '.objectStorage[]' '.id // .name')"
    id_rows "infraId"  "$(ours "$INFRA_ALL"  '.infra[]'         '.id // .name')"
    id_rows "nodeId"   "$(ours "$INFRA_ALL"  '.infra[]'         '.node[]? | .id // .name')"
    id_rows "vNetId"   "$(ours "$VNET_ALL"   '.vNet[]'          '.id // .name')"
    id_rows "subnetId" "$(ours "$VNET_ALL"   '.vNet[]'          '.subnetInfoList[]? | .id // .name')"
    id_rows "sgId"     "$(ours "$SG_ALL"     '.securityGroup[]' '.id // .name')"
    id_rows "sshKeyId" "$(ours "$SSHKEY_ALL" '.sshKey[]'        '.id // .name')"
    exit 0
fi

# ------------------------------------------------------------------------------
# --ssh
# ------------------------------------------------------------------------------
# Everything needed to log in, with nothing left to assemble by hand.
#
# The key is fetched from the node's own sshKeyId rather than from "the first key
# in the namespace": one namespace can hold several infrastructures, and pairing
# a node with the wrong key gives a command that looks right and fails.

if [ "$AS_SSH" -eq 1 ]; then
    printf '\n'
    log_step "${CSP} — SSH access, nsId ${BEETLEENV_NS}"

    # Joined on US (0x1f), not on a tab: read splits on IFS, and a tab is IFS
    # whitespace, so consecutive tabs collapse into one. Two of these columns are
    # routinely empty - publicIP on a private node, nodeUserName on every CSP that
    # does not report it - and with tabs every column after an empty one would
    # shift up by one, silently pairing a node with the wrong user.
    NODE_ROWS="$(ours "$INFRA_ALL" '.infra[]' '
        (.id // .name) as $infra
        | .node[]?
        | [ $infra, (.id // .name), (.status // "-"),
            (.publicIP // ""), ((.sshPort // 22) | tostring),
            (.nodeUserName // ""), (.sshKeyId // "") ]
        | join("\u001f")')"

    if [ -z "$NODE_ROWS" ]; then
        printf '\n  (no VM node in this namespace)\n\n'
        log_info "create one with: ./scripts/provision.sh ${CSP} vm"
        exit 0
    fi

    KEYS_SEEN=""    # sshKeyIds already fetched in this run, one call per key

    while IFS=$'\037' read -r INFRA_ID NODE_ID NODE_STATUS PUB_IP SSH_PORT NODE_USER KEY_ID; do
        if [ -z "$NODE_ID" ]; then continue; fi

        printf '\n  nodeId        %s   (infraId %s, %s)\n' \
            "$NODE_ID" "$INFRA_ID" "$NODE_STATUS"

        # ── the key ───────────────────────────────────────────────────────────
        # One call per key, not per node: nodes of one infrastructure share it.
        # The path is printed for every node all the same, so a node block can be
        # read on its own.
        KEY_FILE=""
        KEY_NOTE=""
        if [ -n "$KEY_ID" ]; then
            KEY_FILE="$(key_path "$CSP" "$KEY_ID")"
            case " ${KEYS_SEEN} " in
                *" ${KEY_ID} "*) ;;   # already fetched and written in this run
                *)
                    if bt_get "/migration/ns/${NS_PATH}/resources/sshKey/$(urlq "$KEY_ID")"; then
                        PEM="$(bt_data '.privateKey')"
                    else
                        PEM=""
                    fi
                    if [ -n "$PEM" ] && [ "$PEM" != "null" ]; then
                        KEY_NOTE="  (0600, $(key_write "$CSP" "$KEY_ID" "$PEM"))"
                        KEYS_SEEN="${KEYS_SEEN} ${KEY_ID}"
                    else
                        log_warn "sshKey ${KEY_ID}: cb-tumblebug returned no private key"
                        KEY_FILE=""
                    fi
                    ;;
            esac
            if [ -n "$KEY_FILE" ]; then
                printf '    key         %s%s\n' "$KEY_FILE" "$KEY_NOTE"
            fi
        else
            log_warn "node ${NODE_ID} has no sshKeyId — it was not created with a key pair"
        fi

        # ── the address ───────────────────────────────────────────────────────
        if [ -z "$PUB_IP" ]; then
            printf '    (no public IP — reaching a private-only node needs a bastion,\n'
            printf '     and this API does not report one)\n'
            continue
        fi
        # WHERE THE LOGIN USER COMES FROM
        #   nodeUserName, read from the single-infrastructure GET above - the same
        #   endpoint and the same field cm-centipede reads for a beetleSsh
        #   connection, so this command and centipede log in as one user.
        #
        #   The fallbacks are for a CSP that reports none even singly. cb-tumblebug
        #   creates every VM it provisions with cb-user, and BEETLEENV_<CSP>_VM_USER
        #   overrides that for an image shipping a different account.
        USER_SRC="nodeUserName"
        if [ -z "$NODE_USER" ]; then
            NODE_USER="$(csp_env "$CSP" VM_USER)"
            USER_SRC="BEETLEENV_$(csp_upper "$CSP")_VM_USER"
        fi
        if [ -z "$NODE_USER" ]; then
            NODE_USER="cb-user"
            USER_SRC="cb-tumblebug default"
        fi
        if [ "$USER_SRC" != "nodeUserName" ]; then
            printf '    user        %s   (the API reports none; %s)\n' "$NODE_USER" "$USER_SRC"
        fi

        TARGET="${NODE_USER}@${PUB_IP}"
        IDENTITY="${KEY_FILE:-<private key file>}"

        # accept-new rather than the default ask: a re-created VM presents a new
        # host key, and the prompt would block an otherwise unattended command.
        SSH_OPTS="-i ${IDENTITY} -o StrictHostKeyChecking=accept-new"
        SSH_PORT_OPT=""
        SCP_PORT_OPT=""
        if [ "$SSH_PORT" != "22" ]; then
            SSH_PORT_OPT=" -p ${SSH_PORT}"
            SCP_PORT_OPT=" -P ${SSH_PORT}"
        fi

        printf '    ssh         ssh %s%s %s\n' "$SSH_OPTS" "$SSH_PORT_OPT" "$TARGET"
        printf '    look        ssh %s%s %s '"'"'ls -al /home/%s'"'"'\n' \
            "$SSH_OPTS" "$SSH_PORT_OPT" "$TARGET" "$NODE_USER"
        printf '    copy up     scp %s%s ./file %s:/home/%s/\n' \
            "$SSH_OPTS" "$SCP_PORT_OPT" "$TARGET" "$NODE_USER"
        printf '    sync up     rsync -av -e '"'"'ssh %s%s'"'"' ./dir/ %s:/home/%s/dir/\n' \
            "$SSH_OPTS" "$SSH_PORT_OPT" "$TARGET" "$NODE_USER"
    done <<< "$NODE_ROWS"

    printf '\n'
    log_info "keys are removed by: ./scripts/deprovision.sh ${CSP} vm"
    printf '\n'
    exit 0
fi

# ------------------------------------------------------------------------------
# Human output
# ------------------------------------------------------------------------------

printf '\n'
log_step "${CSP} — nsId ${BEETLEENV_NS}, connection $(connection_name "$CSP")"

# ── network ───────────────────────────────────────────────────────────────────
printf '\n  network\n'
ROWS="$(ours "$VNET_ALL" '.vNet[]' '
    "    vNetId        \(.id // .name)\(if (.cspResourceId // "") == "" then "" else "  (csp \(.cspResourceId))" end)",
    "    subnetId      \([ .subnetInfoList[]? | .id // .name ] | join(", "))"')"
printf '%s\n' "${ROWS:-    (none)}"
ROWS="$(ours "$SG_ALL" '.securityGroup[]' '
    "    sgId          \(.id // .name)\(if (.cspResourceId // "") == "" then "" else "  (csp \(.cspResourceId))" end)"')"
if [ -n "$ROWS" ]; then printf '%s\n' "$ROWS"; fi

# ── managed RDBMS ─────────────────────────────────────────────────────────────
printf '\n  managed RDBMS\n'
ROWS="$(ours "$RDBMS_ALL" '.rdbms[]' '
    "    rdbmsId       \(.id // .name)\(if (.name // "") != "" and .name != .id then "  (name \(.name))" else "" end)\n" +
    "      engine      \(.dbEngine // "-") \(.dbEngineVersion // "")\n" +
    "      status      \(.status // "-")\n" +
    "      endpoint    \(if (.endpoint // "") == "" then "(not assigned)" else .endpoint end)\n" +
    "      admin user  \(.adminUserName // "-")\n" +
    "      csp id      \(.cspResourceId // .cspResourceName // "-")"')"
if [ -n "$ROWS" ]; then
    printf '%s\n' "$ROWS"
    # One password for every instance on this CSP, so it is printed once for the
    # section rather than repeated under each. cb-tumblebug never hands it back;
    # this comes straight out of .env.
    if [ "$REVEAL" -eq 1 ]; then
        printf '    admin password for all of the above: %s\n' "$(csp_env "$CSP" DB_PASSWORD)"
    else
        printf '    admin password for all of the above: %s  (--reveal, or BEETLEENV_%s_DB_PASSWORD in .env)\n' \
            "$(mask "$(csp_env "$CSP" DB_PASSWORD)")" "$(csp_upper "$CSP")"
    fi
else
    printf '    (none)\n'
fi

# ── object storage ────────────────────────────────────────────────────────────
printf '\n  object storage\n'
ROWS="$(ours "$BUCKET_ALL" '.objectStorage[]' '
    "    osId          \(.id // .name)\n" +
    "      status      \(.status // "-")\n" +
    "      csp name    \(.cspResourceName // .uid // "-")   <- the bucket name in the CSP\n" +
    "      csp id      \(.cspResourceId // "-")"')"
printf '%s\n' "${ROWS:-    (none)}"

# ── VM infrastructure ─────────────────────────────────────────────────────────
printf '\n  VM infrastructure\n'
ROWS="$(ours "$INFRA_ALL" '.infra[]' '
    "    infraId       \(.id // .name)  (\(.status // "-"))",
    (.node[]? |
        "      nodeId      \(.id // .name)\n" +
        "        public \(.publicIP // "-")  private \(.privateIP // "-")  user \(.nodeUserName // "(CSP default)")\n" +
        "        csp id    \(.cspResourceId // .cspResourceName // "-")")')"
printf '%s\n' "${ROWS:-    (none)}"

# ── SSH key ───────────────────────────────────────────────────────────────────
# The private key is generated by cb-tumblebug and never leaves it unless asked
# for, so this is the only place it can be got from.
KEY_ID="$(ours "$SSHKEY_ALL" '.sshKey[]' '.id // .name' | head -1)"
if [ -n "$KEY_ID" ]; then
    printf '\n  SSH key\n'
    printf '    sshKeyId      %s\n' "$KEY_ID"
    if [ "$REVEAL" -eq 1 ]; then
        if bt_get "/migration/ns/${NS_PATH}/resources/sshKey/$(urlq "$KEY_ID")"; then
            printf '    username      %s\n' "$(bt_data '.username // .verifiedUsername // "-"')"
            printf '\n%s\n\n' "$(bt_data '.privateKey')"
            # The command with the values already in it, rather than a shape to
            # fill in. The user is the node's own nodeUserName, not the key
            # record's username, because that is the account the node accepts.
            FIRST_NODE="$(ours "$INFRA_ALL" '.infra[]' '
                .node[]?
                | select((.publicIP // "") != "")
                | [ (.nodeUserName // ""), .publicIP ] | @tsv' | head -1)"
            if [ -n "$FIRST_NODE" ]; then
                printf '    Save it, chmod 600 it, then: ssh -i <file> %s@%s\n' \
                    "$(printf '%s' "$FIRST_NODE" | cut -f1)" \
                    "$(printf '%s' "$FIRST_NODE" | cut -f2)"
            else
                printf '    Save it, chmod 600 it, then: ssh -i <file> <user>@<publicIP>\n'
            fi
            printf '    Or let --ssh do all of that: ./scripts/conn-info.sh %s --ssh\n' "$CSP"
        fi
    else
        printf '    private key   %s  (--reveal to print it, --ssh to save it)\n' "$(mask x)"
    fi
fi

printf '\n'
