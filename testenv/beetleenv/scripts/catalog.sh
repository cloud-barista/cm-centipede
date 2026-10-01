#!/usr/bin/env bash
# ==============================================================================
# catalog.sh — what a CSP actually offers, read-only
# ------------------------------------------------------------------------------
#   Creates nothing and changes nothing. This is the answer to "which engines
#   does ncp have", "what versions can I ask for" and "why did my mariadb request
#   stop", asked before a provision rather than after one fails.
#
#   Everything here comes from cb-tumblebug through cm-beetle, so it reflects the
#   live catalogue rather than a list kept in beetleenv.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/recommend.sh
. "${SCRIPT_DIR}/lib/recommend.sh"

usage() {
    cat <<'EOF'
Usage: ./scripts/catalog.sh <csp> <kind> [--json]

Kinds:
  rdbms            engines, versions, instance specs and storage for managed RDBMS
  engines          just the engine list, one per line
  objectstorage    object storage feature support
  vmspec           VM specs beetle would recommend for the source profile in .env
  vmimage          OS images beetle would recommend for the same
  imageprobe       when vmimage finds nothing: which filter empties the result,
                   and the osType values the catalogue does hold
  connection       the connection name beetle will use, and whether it resolves

vmspec and vmimage are the two halves of what "provision.sh <csp> vm" needs. An
empty answer from either is why a VM recommendation can come back with nothing.

Examples:
  ./scripts/catalog.sh aws rdbms
  ./scripts/catalog.sh ncp engines
  ./scripts/catalog.sh aws vmspec
  ./scripts/catalog.sh aws rdbms --json | jq .
EOF
}

CSP=""
KIND=""
AS_JSON=0

while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --json)    AS_JSON=1 ;;
        -*)        usage >&2; die "unknown option: $1" ;;
        *)
            if   [ -z "$CSP" ];  then CSP="$1"
            elif [ -z "$KIND" ]; then KIND="$1"
            else usage >&2; die "unexpected argument: $1"
            fi
            ;;
    esac
    shift
done

if [ -z "$CSP" ] || [ -z "$KIND" ]; then
    usage >&2
    exit 1
fi

preflight
validate_csp "$CSP"
CSP="$(csp_lower "$CSP")"

if [ -z "$(csp_region "$CSP")" ]; then
    die "BEETLEENV_$(csp_upper "$CSP")_REGION is not set - every lookup is per region"
fi

CONN="$(connection_name "$CSP")"

# ------------------------------------------------------------------------------

show_engines() {
    local engines
    engines="$(rdbms_support "$CSP" | jq -r --arg c "$CSP" '.supports[$c].supportedDBEngines[]? // empty')"
    if [ -z "$engines" ]; then
        log_warn "cb-tumblebug reports no engine list for ${CSP}"
        return 0
    fi
    printf '%s\n' "$engines"
}

show_rdbms() {
    local support cap engines

    support="$(rdbms_support "$CSP")"
    if [ "$AS_JSON" -eq 1 ]; then
        if bt_get "/recommendation/middleware/rdbms/capability?connectionName=$(urlq "$CONN")"; then
            cap="$(bt_payload)"
        else
            cap='{}'
        fi
        jq -n --argjson support "$support" --argjson capability "$cap" \
              '{support: $support, capability: $capability}'
        return 0
    fi

    printf '\n'
    log_step "managed RDBMS support — ${CSP}"
    printf '%s' "$support" | jq -r --arg c "$CSP" '
        .supports[$c] // {} |
        "  supported            \(.supported // "unknown")",
        "  engines              \((.supportedDBEngines // ["(not reported)"]) | join(", "))",
        "  db operation method  \(.dbOperationMethod // "-")",
        "  storage selectable   \(.storageTypeSelectable // false)",
        (if .note then "  note                 \(.note)" else empty end)'

    # cm-beetle's capability endpoint takes a connection name and no engine, so
    # what comes back describes the connection's default engine. Versions and
    # specs per engine are what the recommendation resolves at provision time.
    if ! bt_get "/recommendation/middleware/rdbms/capability?connectionName=$(urlq "$CONN")"; then
        log_warn "no live capability for ${CONN} - $(bt_message)"
        printf '\n'
        return 0
    fi

    printf '\n'
    log_step "live capability — ${CONN}"
    bt_payload | jq -r '
        .supports // {} |
        "  engine               \(.dbEngine // "-")",
        "  versions             \((.supportedVersions // []) | join(", "))",
        "  instance specs       \((.dbInstanceSpecOptions // []) | length) options" +
            (if ((.dbInstanceSpecOptions // []) | length) > 0
             then ", e.g. \((.dbInstanceSpecOptions // [])[0:5] | join(", "))" else "" end),
        "  storage types        \((.storageTypeOptions // ["(not selectable)"]) | join(", "))",
        "  storage size         \(.storageSizeRange.min // "-") - \(.storageSizeRange.max // "-") GB",
        "  requires subnet      \(.requiresSubnet // false)",
        "  requires sec. group  \(.requiresSecurityGroup // false)",
        "  public access        \(.supportsPublicAccess // false)",
        "  high availability    \(.supportsHighAvailability // false)"'

    engines="$(show_engines 2>/dev/null || true)"
    if [ -n "$engines" ]; then
        printf '\n'
        printf '  Set BEETLEENV_%s_DB_ENGINES to a subset of: %s\n' \
            "$(csp_upper "$CSP")" "$(printf '%s' "$engines" | tr '\n' ' ')"
        printf '  cm-beetle can create: %s\n' "$BEETLEENV_SUPPORTED_ENGINES"
    fi
    printf '\n'
}

show_objectstorage() {
    if ! bt_get "/recommendation/middleware/objectStorage/support"; then
        die "could not read the object storage support matrix - $(bt_message)"
    fi
    if [ "$AS_JSON" -eq 1 ]; then
        bt_payload | jq '.'
        return 0
    fi

    printf '\n'
    log_step "object storage support — ${CSP}"
    bt_payload | jq -r --arg c "$CSP" '
        (.supports[$c] // {}) as $s |
        if $s == {} then "  (nothing reported for this CSP)"
        else ($s | to_entries[] | "  \(.key)\(" " * (22 - (.key | length)))\(.value)")
        end'
    printf '\n'
}

# show_vm_candidates <kind> — vmspec and vmimage differ only in the endpoint and
#   the field names, so they share one function.
#
#   Both take the same source infrastructure as POST /recommendation/infra, and
#   are asked here for exactly the reason that call can answer with nothing: it
#   needs a compatible spec AND image, and reports neither when it has neither.
#   The recommendation itself is one level down: each entry in the list is a
#   per-source-server result, and the spec or image it settled on sits under
#   .targetSpec / .targetOsImage. The entry's own status and description say
#   which source node it answers for.
show_vm_candidates() {
    local kind="$1" path field row body

    case "$kind" in
        vmspec)
            path="/recommendation/resources/specs"
            field="recommendedSpecList"
            row='.targetSpec as $t |
                 "    \($t.id // "-")\n" +
                 "      \($t.vCPU // "?") vCPU, \($t.memoryGiB // "?") GiB" +
                 (if ($t.cspSpecName // "") == "" then "" else "   csp \($t.cspSpecName)" end) +
                 "\n      for \((.sourceServers // []) | join(", "))  [\(.status // "-")]"'
            ;;
        vmimage)
            path="/recommendation/resources/osImages"
            field="recommendedOsImageList"
            row='.targetOsImage as $t |
                 "    \($t.id // "-")\n" +
                 "      \($t.osType // $t.name // "-")" +
                 (if ($t.cspImageName // "") == "" then "" else "   csp \($t.cspImageName)" end) +
                 "\n      for \((.sourceServers // []) | join(", "))  [\(.status // "-")]"'
            ;;
    esac

    body="$(infra_request_body "$CSP")"
    if ! bt_post "${path}?desiredProvider=$(urlq "$CSP")&desiredRegion=$(urlq "$(csp_region "$CSP")")" "$body"; then
        die "the ${kind} lookup failed - $(bt_message)"
    fi

    if [ "$AS_JSON" -eq 1 ]; then
        bt_payload | jq '.'
        return 0
    fi

    printf '\n'
    log_step "${kind} — ${CONN}, for the source profile in .env"

    # The search key, so a "none" answer can be compared against what the
    # catalogue holds. beetle derives it from the source node, not from anything
    # named after an image.
    if [ "$kind" = "vmimage" ]; then
        local key
        key="$(image_search_key "$CSP")"
        printf '  searching for        osType "%s", architecture "%s"\n' \
            "${key%%|*}" "${key##*|}"
    fi

    # count is the length of the list, not a number of things considered.
    bt_payload | jq -r --arg f "$field" "
        \"  recommended          \\(.count // ([.[\$f][]?] | length))\",
        (.[\$f][]? | ${row})" 2>/dev/null

    # "none" is beetle's NothingRecommended: the search ran and matched nothing.
    # That is not the same as an empty list, and it is the answer that stops a VM
    # provision, so it gets the same treatment.
    if [ "$(bt_payload | jq -r --arg f "$field" '[.[$f][]? | select(.status == "none")] | length')" != "0" ] \
       || [ "$(bt_payload | jq -r --arg f "$field" '[.[$f][]?] | length')" = "0" ]; then
        printf '\n'
        log_warn "nothing was recommended."
        if [ "$kind" = "vmimage" ]; then
            printf '  cb-tumblebug holds no image matching that osType and architecture for\n'
            printf '  %s. What beetle searched with comes from BEETLEENV_%s_VM_SRC_OS\n' \
                "$CONN" "$(csp_upper "$CSP")"
            printf '  and _VM_SRC_ARCH - it is os id + version, not a pretty name.\n\n'
            printf '  To see what the catalogue does hold:\n'
            printf '    curl -s -X POST "${TUMBLEBUG_URL}/ns/system/resources/searchImage" \\\n'
            printf '      -u "${TUMBLEBUG_USERNAME}:${TUMBLEBUG_PASSWORD}" \\\n'
            printf '      -H "Content-Type: application/json" \\\n'
            printf '      -d %s | jq -r ".imageList[].osType" | sort -u\n' \
                "'{\"providerName\":\"${CSP}\",\"regionName\":\"$(csp_region "$CSP")\"}'"
        else
            printf '  The source profile asks for something the catalogue cannot match -\n'
            printf '  see BEETLEENV_%s_VM_SRC_* in .env.\n' "$(csp_upper "$CSP")"
        fi
    fi
    printf '\n'
}

# probe_image_filters — narrow down which of beetle's image filters empties the
#   result, by running the same search against cb-tumblebug with one filter added
#   at a time.
#
#   This is the one place beetleenv queries tumblebug for something other than a
#   namespace, and it is diagnostic and read-only. There is no beetle endpoint
#   for a raw image search, and without it "nothing was recommended" is a dead
#   end: the filters are ANDed inside tumblebug and the answer says only that the
#   whole conjunction matched nothing.
#
#   beetle's search (pkg/core/recommendation/resource-node-image.go) is:
#     providerName, regionName, osType, osArchitecture,
#     isGPUImage=false, includeBasicImageOnly=true
probe_image_filters() {
    local key os_type arch region step body count

    if [ -z "${TUMBLEBUG_URL:-}" ]; then
        die "TUMBLEBUG_URL is not set, and the probe queries cb-tumblebug directly"
    fi

    key="$(image_search_key "$CSP")"
    os_type="${key%%|*}"
    arch="${key##*|}"
    region="$(csp_region "$CSP")"

    printf '\n'
    log_step "image filter probe — ${CONN}"
    printf '  Each row adds one of the filters beetle uses. The first 0 is the cause.\n\n'

    # search <label> <extra jq object> — count what tumblebug returns. Returns 1
    # when the call itself fails, which ends the ladder: every later row would
    # fail the same way, and five copies of one error bury the answer.
    search() {
        local label="$1" extra="$2"
        body="$(jq -cn --arg p "$CSP" --arg r "$region" --arg o "$os_type" --arg a "$arch" \
            "{providerName: \$p, regionName: \$r} + ${extra}")"
        if ! tb_request POST "/ns/system/resources/searchImage" "$body"; then
            printf '  %-38s call failed\n' "$label"
            report_search_failure "$(bt_message)"
            return 1
        fi
        count="$(printf '%s' "$BT_BODY" | jq -r '[.imageList[]?] | length' 2>/dev/null || printf '?')"
        printf '  %-38s %s\n' "$label" "$count"
    }

    search "provider + region" '{}' \
        && search "+ osType ${os_type}" '{osType: $o}' \
        && search "+ osArchitecture ${arch}" '{osType: $o, osArchitecture: $a}' \
        && search "+ isGPUImage=false" '{osType: $o, osArchitecture: $a, isGPUImage: false}' \
        && search "+ includeBasicImageOnly=true" '{osType: $o, osArchitecture: $a, isGPUImage: false, includeBasicImageOnly: true}' \
        || return 0

    printf '\n'
    printf '  osType values the catalogue holds for %s %s:\n' "$CSP" "$region"
    body="$(jq -cn --arg p "$CSP" --arg r "$region" '{providerName: $p, regionName: $r}')"
    if tb_request POST "/ns/system/resources/searchImage" "$body"; then
        printf '%s' "$BT_BODY" | jq -r '[.imageList[]?.osType] | unique | .[]' 2>/dev/null \
            | sed 's/^/    /' | head -30
    fi
    printf '\n'
}

# report_search_failure — explain a searchImage call that did not return a list.
#   The one failure worth naming is cb-tumblebug's own: SearchImage filters on
#   image_infos.deletion_requested_at. On a server whose schema predates that
#   column, every image search dies on SQLSTATE 42703 and the message reads like
#   a beetleenv fault when it is not one. Upgrading cb-tumblebug fixes it.
report_search_failure() {
    local msg="$1"

    printf '\n'
    printf '  %s\n' "$msg"

    case "$msg" in
        *deletion_requested_at*|*42703*)
            printf '\n'
            printf '  This is a cb-tumblebug bug, not a beetleenv or catalogue problem.\n'
            printf '  SearchImage filters on image_infos.deletion_requested_at, and on this\n'
            printf '  server the column was never created - so every image search fails and\n'
            printf '  cm-beetle sees no VM image at all. Databases and buckets are unaffected,\n'
            printf '  which is why only "provision.sh %s vm" breaks.\n' "$CSP"
            printf '\n'
            printf '  Upgrade cb-tumblebug past the fix (2026-08-28) and restart. AutoMigrate\n'
            printf '  adds the column on start-up; no image asset needs reloading. Check the\n'
            printf '  start-up log for "database schemas migrated successfully" - a failure\n'
            printf '  there is only logged and the server still comes up. Failing that:\n'
            printf '    ALTER TABLE image_infos ADD COLUMN IF NOT EXISTS deletion_requested_at text;\n'
            ;;
    esac
    printf '\n'
}

show_connection() {
    printf '\n'
    log_step "connection — ${CSP}"
    printf '  connection name  %s\n' "$CONN"
    printf '  region           %s\n' "$(csp_region "$CSP")"
    printf '  zones            %s, %s\n' "$(csp_env "$CSP" ZONE)" "$(csp_env "$CSP" ZONE2)"

    # There is no "resolve this connection" endpoint on beetle, so the capability
    # lookup doubles as one: it fails for a connection cb-tumblebug does not hold.
    if bt_get "/recommendation/middleware/rdbms/capability?connectionName=$(urlq "$CONN")"; then
        printf '  resolves         yes\n'
    else
        printf '  resolves         no - %s\n' "$(bt_message)"
        printf '\n'
        printf '  cm-beetle looks connections up as <csp>-<region> and nothing else.\n'
        printf '  Register it on cb-tumblebug under exactly that name.\n'
    fi
    printf '\n'
}

case "$KIND" in
    rdbms)          show_rdbms ;;
    engines)        show_engines ;;
    objectstorage)  show_objectstorage ;;
    vmspec|vmimage) show_vm_candidates "$KIND" ;;
    imageprobe)     probe_image_filters ;;
    connection)     show_connection ;;
    *) usage >&2; die "unknown kind: ${KIND}" ;;
esac
