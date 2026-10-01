#!/usr/bin/env bash
# ==============================================================================
# lib/network.sh — the one vNet and security group every resource shares
# ------------------------------------------------------------------------------
#   ensure_network <csp>   create-if-absent, then publish the ids
#   release_network <csp>  delete the security group, then the vNet
#
# WHY BEETLEENV OWNS A NETWORK AT ALL
#   The RDBMS recommendation does not fill in vNetId, subnetIds or
#   securityGroupIds - beetle leaves them empty and tumblebug's autoFillDefaults
#   only covers engine version, instance spec and storage. The create then fails
#   resolving an empty vNet id. So the network has to exist before the database
#   is asked for, and its ids have to be injected into the recommendation.
#
#   Two subnets, in two different zones, because every managed RDBMS wants them.
#   That is also why BEETLEENV_<CSP>_ZONE2 is mandatory.
#
#   The VM shares the same vNet rather than getting its own. cm-centipede's whole
#   reason for these resources is migrating data between them, and a VM that
#   cannot reach the database is not a test environment. POST
#   /migration/ns/{ns}/infra takes useExisting=true, so handing it our vNet and
#   security group makes it adopt them instead of creating a second pair.
# ==============================================================================

if [ -n "${BEETLEENV_NETWORK_SH:-}" ]; then return 0; fi
BEETLEENV_NETWORK_SH=1

# shellcheck source=./beetle.sh
. "$(dirname "${BASH_SOURCE[0]}")/beetle.sh"

# Set by ensure_network / load_network.
NET_VNET_ID=""
NET_SUBNET_IDS=""      # JSON array
NET_SG_ID=""

# All three take the CSP: one namespace holds every CSP's resources, so the name
# has to say which one it belongs to. See resource_name in lib/common.sh.
vnet_name()   { resource_name "$1" "vnet"; }
sg_name()     { resource_name "$1" "sg"; }
subnet_name() { resource_name "$1" "subnet-$2"; }

_res_path() { printf '/migration/ns/%s/resources/%s' "$(urlq "$BEETLEENV_NS")" "$1"; }

# ------------------------------------------------------------------------------
# CIDR
# ------------------------------------------------------------------------------

# subnet_cidr <csp> <index> — an explicit BEETLEENV_<CSP>_SUBNET<n>_CIDR wins.
#   Otherwise the two /24s are derived from the vNet block by replacing the third
#   octet, which holds for the /16 .env.example ships. A vNet block smaller than
#   a /16 needs the explicit keys, and saying so beats silently deriving a subnet
#   that falls outside it.
subnet_cidr() {
    local csp="$1" index="$2" explicit base prefix
    explicit="$(csp_env "$csp" "SUBNET${index}_CIDR")"
    if [ -n "$explicit" ]; then
        printf '%s' "$explicit"
        return 0
    fi

    base="$(csp_env "$csp" VNET_CIDR)"
    prefix="${base#*/}"
    if [ "$prefix" != "16" ]; then
        die "BEETLEENV_$(csp_upper "$csp")_VNET_CIDR is /${prefix}, and the two subnet
       blocks are only derived automatically from a /16.
       Set BEETLEENV_$(csp_upper "$csp")_SUBNET1_CIDR and _SUBNET2_CIDR explicitly."
    fi
    printf '%s' "$base" | awk -F'[./]' -v i="$index" '{ printf "%s.%s.%s.0/24", $1, $2, i }'
}

# ------------------------------------------------------------------------------
# Firewall rules
# ------------------------------------------------------------------------------

# engine_port <engine> — the port to open for a managed database engine.
#   postgresql and mongodb are listed although cm-beetle cannot create them yet:
#   the rule costs nothing now and is one less thing to remember when it can.
engine_port() {
    case "$(csp_lower "$1")" in
        mysql|mariadb) printf '3306' ;;
        postgresql)    printf '5432' ;;
        mongodb)       printf '27017' ;;
        *)             printf '' ;;
    esac
}

# firewall_rules <csp> — SSH for the VM, plus one port per engine in
#   BEETLEENV_<CSP>_DB_ENGINES. The JSON keys are PascalCase because that is what
#   cb-tumblebug's FirewallRuleReq declares; lower case ones are silently
#   dropped, which produces a security group with no rules in it.
firewall_rules() {
    local csp="$1" cidr engine port
    local ports="22"

    cidr="${BEETLEENV_ALLOWED_CIDR:-0.0.0.0/0}"
    for engine in $(csp_env "$csp" DB_ENGINES); do
        port="$(engine_port "$engine")"
        # mysql and mariadb are both 3306, and a repeated port in the list is
        # rejected as a duplicate rule by some CSPs.
        if [ -n "$port" ] && ! printf ',%s,' "$ports" | grep -q ",${port},"; then
            ports="${ports},${port}"
        fi
    done

    jq -n --arg ports "$ports" --arg cidr "$cidr" '
        [ { Ports: $ports, Protocol: "TCP", Direction: "inbound", CIDR: $cidr } ]'
}

# ------------------------------------------------------------------------------
# Lookup
# ------------------------------------------------------------------------------

# find_vnet <csp> — publish NET_VNET_ID and NET_SUBNET_IDS when the vNet exists.
#   Returns 1 when it does not, and 2 when the list itself could not be read - an
#   unread list is not an empty one. Callers that only test success see both as
#   "not found".
find_vnet() {
    local name subnets
    name="$(vnet_name "$1")"

    if ! bt_get "$(_res_path vNet)"; then
        return 2
    fi
    NET_VNET_ID="$(bt_jq -r --arg n "$name" '.vNet[]? | select(.name == $n) | .id // empty')"
    if [ -z "$NET_VNET_ID" ]; then
        return 1
    fi

    # Subnet order matters: the first zone is where a single-AZ database lands.
    subnets="$(bt_jq -c --arg n "$name" \
        '[ .vNet[]? | select(.name == $n) | .subnetInfoList[]? | .id ]')"
    NET_SUBNET_IDS="${subnets:-[]}"
    return 0
}

# find_sg <csp> — publish NET_SG_ID when the security group exists.
find_sg() {
    local name
    name="$(sg_name "$1")"
    if ! bt_get "$(_res_path securityGroup)"; then
        return 1
    fi
    NET_SG_ID="$(bt_jq -r --arg n "$name" '.securityGroup[]? | select(.name == $n) | .id // empty')"
    [ -n "$NET_SG_ID" ]
}

# load_network <csp> — read the ids without creating anything. Returns 1 when the
#   network is not there and 2 when the vNet list could not be read, as find_vnet.
load_network() {
    NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""
    find_vnet "$1" || return $?
    find_sg "$1" || true
    return 0
}

# ------------------------------------------------------------------------------
# Zone agreement
# ------------------------------------------------------------------------------

# assert_vm_zone <csp> — the VM's zone has to be the connection's zone.
#
#   A VM always lands in the first subnet (inject_infra_network), while a managed
#   RDBMS spans both - which is why this checks BEETLEENV_<CSP>_ZONE alone and
#   why a database can be fine on a setup where a VM is not.
#
#   cb-spider looks VM specs up per zone, and the zone it uses is the one the
#   connection is assigned to, not the one the subnet is in:
#     GetServerSpecListRequest{ RegionCode, ZoneCode: connection zone,
#                               ServerImageNo }   (ncp/resources/VMSpecHandler.go)
#   So when the two disagree, beetle recommends a spec that exists in the
#   connection's zone and the VM is created in a subnet where it does not. NCP
#   answers that with returnCode 3010022, "An error occurred during the requested
#   server operation. Contact Customer Center", which names neither the zone nor
#   the spec - twenty minutes of reading to reach a two-line .env fix.
#
#   Read from cb-tumblebug directly, like the image probe in catalog.sh: cm-beetle
#   uses connConfig internally (client/tumblebug/common-utility.go) but exposes no
#   route to it. Anything unreadable here returns 0 - a check that cannot run must
#   not be a check that blocks.
assert_vm_zone() {
    local csp="$1" conn zone assigned

    if [ -z "${TUMBLEBUG_URL:-}" ]; then return 0; fi

    conn="$(connection_name "$csp")"
    zone="$(csp_env "$csp" ZONE)"
    if [ -z "$zone" ]; then return 0; fi

    if ! tb_request GET "/connConfig/$(urlq "$conn")" ""; then return 0; fi
    assigned="$(printf '%s' "$BT_BODY" \
        | jq -r '.regionZoneInfo.assignedZone // empty' 2>/dev/null || true)"
    if [ -z "$assigned" ]; then return 0; fi

    if [ "$(csp_lower "$assigned")" = "$(csp_lower "$zone")" ]; then
        return 0
    fi

    die "the VM would go in ${zone}, but connection ${conn} is assigned to ${assigned}.
       cb-spider looks a VM spec up in the connection's zone, so the spec beetle
       recommends is not offered where the VM is being placed. The CSP rejects
       that with an error that names neither zone nor spec.

       The VM always uses the first subnet, so swap the two zones:
         BEETLEENV_$(csp_upper "$csp")_ZONE=${assigned}
         BEETLEENV_$(csp_upper "$csp")_ZONE2=${zone}

       A vNet that already exists keeps the zones it was built with, so remove it
       too if there is one:
         ./scripts/deprovision.sh ${csp} all"
}

# ------------------------------------------------------------------------------
# Creation
# ------------------------------------------------------------------------------

ensure_vnet() {
    local csp="$1" body conn

    if find_vnet "$csp"; then
        log_info "vNet $(vnet_name "$csp") already exists (${NET_VNET_ID})"
        return 0
    fi

    conn="$(connection_name "$csp")"
    body="$(jq -n \
        --arg name "$(vnet_name "$csp")" \
        --arg conn "$conn" \
        --arg cidr "$(csp_env "$csp" VNET_CIDR)" \
        --arg s1 "$(subnet_name "$csp" 1)" --arg c1 "$(subnet_cidr "$csp" 1)" --arg z1 "$(csp_env "$csp" ZONE)" \
        --arg s2 "$(subnet_name "$csp" 2)" --arg c2 "$(subnet_cidr "$csp" 2)" --arg z2 "$(csp_env "$csp" ZONE2)" \
        '{
            name: $name,
            connectionName: $conn,
            cidrBlock: $cidr,
            description: "Created by beetleenv",
            subnetInfoList: [
                { name: $s1, ipv4_CIDR: $c1, zone: $z1 },
                { name: $s2, ipv4_CIDR: $c2, zone: $z2 }
            ]
        }')"

    log_step "creating vNet $(vnet_name "$csp") with two subnets ($(csp_env "$csp" ZONE), $(csp_env "$csp" ZONE2))"
    if ! bt_post "$(_res_path vNet)" "$body"; then
        die "failed to create the vNet - $(bt_message)"
    fi

    if ! find_vnet "$csp"; then
        die "the vNet was created but cannot be read back - check namespace ${BEETLEENV_NS}"
    fi
    log_ok "vNet $(vnet_name "$csp") (${NET_VNET_ID})"
}

ensure_sg() {
    local csp="$1" body

    if find_sg "$csp"; then
        log_info "security group $(sg_name "$csp") already exists (${NET_SG_ID})"
        return 0
    fi

    body="$(jq -n \
        --arg name "$(sg_name "$csp")" \
        --arg conn "$(connection_name "$csp")" \
        --arg vnet "$NET_VNET_ID" \
        --argjson rules "$(firewall_rules "$csp")" \
        '{
            name: $name,
            connectionName: $conn,
            vNetId: $vnet,
            description: "Created by beetleenv",
            firewallRules: $rules
        }')"

    log_step "creating security group $(sg_name "$csp")"
    if ! bt_post "$(_res_path securityGroup)" "$body"; then
        die "failed to create the security group - $(bt_message)"
    fi

    if ! find_sg "$csp"; then
        die "the security group was created but cannot be read back"
    fi
    log_ok "security group $(sg_name "$csp") (${NET_SG_ID})"
}

# ensure_network <csp> — the entry point. Idempotent: a second call finds what
#   the first one made and publishes the same ids.
ensure_network() {
    local csp="$1"
    ensure_vnet "$csp"
    ensure_sg "$csp"

    if [ "$(printf '%s' "$NET_SUBNET_IDS" | jq 'length')" -lt 2 ]; then
        log_warn "the vNet has fewer than two subnets; a managed RDBMS needs two zones"
    fi
}

# ------------------------------------------------------------------------------
# Deletion
# ------------------------------------------------------------------------------

# release_network <csp> — security group first: it references the vNet, and every
#   CSP refuses to delete a vNet while something is attached to it.
release_network() {
    local csp="$1"

    if find_sg "$csp"; then
        log_step "deleting security group $(sg_name "$csp")"
        if bt_delete_retry "$(_res_path securityGroup)/$(urlq "$NET_SG_ID")" "security group $(sg_name "$csp")"; then
            log_ok "deleted security group $(sg_name "$csp")"
        elif bt_absent; then
            log_info "security group $(sg_name "$csp") is already gone"
        else
            record_delete_failure "securityGroup $(sg_name "$csp")" "$(bt_message)"
            return 1
        fi
    fi

    if find_vnet "$csp"; then
        log_step "deleting vNet $(vnet_name "$csp")"
        # withsubnets is tumblebug's default action and is spelled out here
        # because a vNet with subnets left in it cannot be deleted.
        if bt_delete_retry "$(_res_path vNet)/$(urlq "$NET_VNET_ID")?action=withsubnets" "vNet $(vnet_name "$csp")"; then
            log_ok "deleted vNet $(vnet_name "$csp")"
        elif bt_absent; then
            log_info "vNet $(vnet_name "$csp") is already gone"
        else
            record_delete_failure "vNet $(vnet_name "$csp")" "$(bt_message)"
            return 1
        fi
    fi

    NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""
    return 0
}
