#!/usr/bin/env bash
# ==============================================================================
# lib/recommend.sh — ask cm-beetle what to create, then check what it answered
# ------------------------------------------------------------------------------
#   Every resource follows the same two steps: a recommendation call that turns
#   a source description into a concrete target specification, and a migration
#   call that creates it. This file is the first step, plus the guards that stand
#   between the two.
#
# WHY THE GUARDS EXIST
#   A recommendation is advice, and beetle's advice includes substitutions it
#   makes silently. Asked for MariaDB on a CSP that has no managed MariaDB, it
#   answers 200 with mysql and a line in `warnings`
#   (pkg/core/recommendation/rdbms.go, "Check MariaDB support"). An engine it
#   does not know at all - postgresql today - becomes mysql the same way.
#
#   For a recommendation engine that is the right behaviour: the caller asked for
#   somewhere to put their data, and mysql is closer than nothing. For beetleenv
#   it is not: `provision.sh aws database --engine mariadb` that quietly builds a
#   MySQL instance gives cm-centipede the wrong thing to migrate, and the mistake
#   only surfaces much later. So the answer is compared against the request, and
#   a substitution stops the run.
# ==============================================================================

if [ -n "${BEETLEENV_RECOMMEND_SH:-}" ]; then return 0; fi
BEETLEENV_RECOMMEND_SH=1

# shellcheck source=./network.sh
. "$(dirname "${BASH_SOURCE[0]}")/network.sh"

# The engines cm-beetle can create today. Its dbEngine field is declared
# enums:"mysql,mariadb" and its recommendation maps anything else to mysql, so
# an engine outside this list is refused here rather than silently substituted.
# PostgreSQL and MongoDB are the planned additions: cb-tumblebug and cb-spider
# already carry postgresql, and once beetle's engine list grows this constant is
# the only line that has to change.
BEETLEENV_SUPPORTED_ENGINES="mysql mariadb"

# The source network beetle is told about. It never reaches a CSP - beetleenv's
# real network comes from network.sh - but the recommendation reads it, and an
# absent or empty block is answered with a 500 rather than a validation error.
BEETLEENV_SOURCE_CIDR="192.168.0.0/24"

# ------------------------------------------------------------------------------
# Request bodies
# ------------------------------------------------------------------------------
#
#   Each of the three recommendation endpoints takes a description of a source
#   resource, and each is built here from .env. They used to be JSON files under
#   templates/ that these functions patched; they are written out in full at the
#   call site instead, because the two halves only ever change together. beetle's
#   managed RDBMS request grew a nested dbEngine/dbNode shape and the patching
#   jq kept filling in fields that no longer existed - a request that was valid
#   JSON, valid against nothing, and refused with "engine is required".
#
#   What is NOT in these bodies is as deliberate as what is. Each was reduced
#   against a live cm-beetle until what was left either changes its answer, is
#   declared required by its schema, or names the source in the answer it gives
#   back. The descriptions are therefore thinner than a real inventory would be -
#   no interfaces, no routing table - and that is the point: a field beetle
#   ignores is a field that goes stale without anything noticing. The OS block
#   used to prove it. It carried a full Ubuntu identity while beetle read two of
#   its fields, so BEETLEENV_<CSP>_VM_SRC_OS="rocky 9" produced a node that was
#   Rocky by id and Ubuntu by name, codename jammy, in the same request.

# print_warnings <json> — a recommendation's warnings are how beetle reports what
#   the target CSP could not honour. They are informational by design, so they
#   are shown rather than acted on; the checks that do act follow separately.
print_warnings() {
    local w
    w="$(printf '%s' "$1" | jq -r '.warnings[]? // empty')"
    if [ -n "$w" ]; then
        while IFS= read -r line; do
            [ -n "$line" ] && log_warn "beetle: ${line}"
        done <<< "$w"
    fi
}

# ------------------------------------------------------------------------------
# Managed RDBMS
# ------------------------------------------------------------------------------

# assert_engine_known <engine> — before any network call.
assert_engine_known() {
    local engine e
    engine="$(csp_lower "$1")"
    for e in $BEETLEENV_SUPPORTED_ENGINES; do
        if [ "$e" = "$engine" ]; then return 0; fi
    done
    case "$engine" in
        postgresql|mongodb)
            die "engine '${engine}' is not supported by cm-beetle yet.
       Its managed RDBMS API declares enums:\"mysql,mariadb\", and a request for
       anything else is answered with a mysql recommendation instead of an error.
       Remove it from BEETLEENV_$(csp_upper "${2:-CSP}")_DB_ENGINES until beetle carries it." ;;
        *)
            die "unknown engine '${engine}'. Supported: ${BEETLEENV_SUPPORTED_ENGINES}" ;;
    esac
}

# rdbms_support <csp> — GET /recommendation/middleware/rdbms/support, cached for
#   the run. The map comes from cb-tumblebug, so "which CSP has what" is never a
#   list in beetleenv.
rdbms_support() {
    local csp cache
    csp="$(csp_lower "$1")"
    cache="${BEETLEENV_TMP}/rdbms-support-${csp}.json"

    if [ ! -f "$cache" ]; then
        if ! bt_get "/recommendation/middleware/rdbms/support?cspType=$(urlq "$csp")"; then
            log_warn "could not read the RDBMS support matrix for ${csp} - $(bt_message)"
            printf '{}' > "$cache"
        else
            bt_payload > "$cache"
        fi
    fi
    cat "$cache"
}

# assert_rdbms_supported <csp> — stop before creating a network for a CSP that
#   has no managed RDBMS at all.
assert_rdbms_supported() {
    local csp supported
    csp="$(csp_lower "$1")"
    supported="$(rdbms_support "$csp" | jq -r --arg c "$csp" '.supports[$c].supported // empty')"

    case "$supported" in
        false) die "managed RDBMS is not supported on '${csp}' according to cb-tumblebug" ;;
        true)  return 0 ;;
        *)     log_warn "cb-tumblebug did not report RDBMS support for '${csp}'; continuing"
               return 0 ;;
    esac
}

# assert_engine_supported <csp> <engine> — the per-engine check. An empty engine
#   list means cb-tumblebug has nothing to say, in which case beetle falls back to
#   a static table and the run continues; the post-recommendation comparison
#   still catches a substitution.
assert_engine_supported() {
    local csp engine engines
    csp="$(csp_lower "$1")"
    engine="$(csp_lower "$2")"
    engines="$(rdbms_support "$csp" | jq -r --arg c "$csp" '.supports[$c].supportedDBEngines[]? // empty' | tr 'A-Z' 'a-z')"

    if [ -z "$engines" ]; then
        log_info "${csp}: no engine list from cb-tumblebug; relying on the post-recommendation check"
        return 0
    fi
    if printf '%s\n' "$engines" | grep -qx "$engine"; then
        return 0
    fi
    die "${csp} does not offer a managed ${engine}.
       Engines it does offer: $(printf '%s' "$engines" | tr '\n' ' ')
       Drop ${engine} from BEETLEENV_$(csp_upper "$csp")_DB_ENGINES."
}

# recommend_rdbms <csp> <engine> — prints the RecommendedRDBMS payload.
#
#   The source version key is per engine - _DB_SRC_ENGINE_VERSION_MYSQL,
#   _DB_SRC_ENGINE_VERSION_MARIADB - because the two number their releases in
#   unrelated ranges. One shared key set to "8.0" would be sent as the MariaDB
#   source version too, and beetle would answer with a warning and a version of
#   its own choosing rather than the one that was asked for. Per engine also
#   means pinning one leaves the others on automatic selection.
#
#   The source instance is described as software (dbEngine) plus the host it runs
#   on (dbNode) - the flat instanceName/engine/vcpu/memoryMb form beetle took
#   before is answered with "instance 'rdbms-node': engine is required", the name
#   in that message being the default it falls back to when displayName is unset.
#
#   Two conversions happen here because .env and the API do not agree on units or
#   on what belongs to the source: _DB_SRC_MEMORY_MB is sent as GiB, and
#   _DB_PUBLIC_ACCESS and _DB_BACKUP_RETENTION_DAYS are target preferences rather
#   than properties of the source. A preference is a request, not an instruction:
#   NCP answers publicAccess:true with a warning and false, which is why
#   inject_rdbms_target has the last word on both.
#
#   engineVersion empty means beetle picks the newest version the CSP supports.
#   No innerDatabases: beetleenv creates the instance and nothing inside it.
recommend_rdbms() {
    local csp="$1" engine="$2" body result got
    local region port

    region="$(csp_region "$csp")"
    case "$engine" in
        postgresql) port=5432 ;;
        *)          port=3306 ;;
    esac

    body="$(jq -nc \
        --arg csp "$csp" \
        --arg region "$region" \
        --arg engine "$engine" \
        --arg version "$(csp_env "$csp" "DB_SRC_ENGINE_VERSION_$(csp_upper "$engine")")" \
        --argjson vcpu "$(int_env "$csp" DB_SRC_VCPU 2)" \
        --argjson memory_mb "$(int_env "$csp" DB_SRC_MEMORY_MB 4096)" \
        --argjson storage "$(int_env "$csp" DB_SRC_STORAGE_GB 100)" \
        --argjson port "$port" \
        --argjson public "$(bool_env "$csp" DB_PUBLIC_ACCESS true)" \
        --argjson backup "$(int_env "$csp" DB_BACKUP_RETENTION_DAYS 0)" \
        '
        # MB to GiB, rounded up and never below 1: memory.totalSize is an
        # integer count of GiB, so 4096 -> 4 and anything under 1024 -> 1.
        def gib: [ ((. + 1023) / 1024 | floor), 1 ] | max;
        {
          desiredCloud: { csp: $csp, region: $region },
          sourceRDBMSInstances: [ {
            # Carried through to the recommendation as sourceInstanceName.
            displayName: "beetleenv-source-01",
            dbEngine: {
              engine:        $engine,
              engineVersion: $version,
              port:          $port,
              role:          "primary"
            },
            dbNode: {
              hostname: "beetleenv-source-01",
              cpu:      { cpus: 1, cores: $vcpu, threads: $vcpu, architecture: "x86_64" },
              memory:   { totalSize: ($memory_mb | gib) },
              # SSD is what a source database is assumed to sit on. A CSP that
              # cannot be told which storage to use says so in a warning.
              rootDisk: { type: "SSD", totalSize: $storage }
            }
          } ],
          targetPreferences: {
            publicAccess:        $public,
            backupRetentionDays: $backup,
            highAvailability:    false
          }
        }')"

    if ! bt_post "/recommendation/middleware/rdbms?desiredCsp=$(urlq "$csp")&desiredRegion=$(urlq "$region")" "$body"; then
        die "the RDBMS recommendation failed - $(bt_message)"
    fi
    result="$(bt_payload)"

    print_warnings "$result"

    # The substitution check. See the note at the top of this file.
    got="$(printf '%s' "$result" | jq -r '.targetRDBMSInstances[0].dbEngine // empty')"
    if [ "$(csp_lower "$got")" != "$(csp_lower "$engine")" ]; then
        die "beetle recommended '${got}' for a requested '${engine}' on ${csp}.
       That is its documented fallback, not an error on its side - but creating a
       ${got} instance when a ${engine} was asked for would be the wrong test
       resource, so nothing has been created.
       ./scripts/catalog.sh ${csp} rdbms lists the engines ${csp} actually offers."
    fi

    printf '%s' "$result"
}

# inject_rdbms_target <recommendation json> <csp> <name> — fill in what the
#   recommendation leaves empty, and re-assert the two values it answers with
#   something other than what was asked for.
#
#   vNetId, subnetIds and securityGroupIds: beetle never sets them, and
#   cb-tumblebug's autoFillDefaults covers engine version, instance spec and
#   storage but not the network, so the create fails resolving an empty vNet id.
#
#   adminUserPassword: also never set. Left empty, cm-beetle substitutes its own
#   built-in default ("BeetleRdbms1234!", pkg/core/migration/rdbms.go), which
#   would put a published password on an internet-reachable database.
#
#   rdbmsName: the recommendation names every instance mig-rdbms-01, whatever
#   engine it is for. Two engines on one CSP would then collide. See the naming
#   note in provision.sh for why the name is set here rather than with nameSeed.
#
#   publicAccess: sent as a target preference too, but a recommendation is free to
#   overrule one - NCP answers true with a warning and publicAccess:false, because
#   its Cloud DB has no public endpoint until a public domain is issued. That is a
#   statement about the endpoint the instance is created with, not about what may
#   be asked for: the domain is a console action with no API behind it
#   (./scripts/ncp-db-domain.sh), and the request has to carry the flag it was
#   given for the console step to have anything to reflect. So .env decides here.
#
#   backupRetentionDays: sent as a preference too, and there the default of 0 is
#   indistinguishable from an unset field - beetle reads the Go zero value as
#   "no preference" and answers 7. So the .env value is applied here, where a 0
#   means what it says. beetleenv's default is 0 because these instances exist to
#   be migrated from and then deleted, which makes a backup cost with no reader.
inject_rdbms_target() {
    local recommendation="$1" csp="$2" name="$3" password
    password="$(csp_env "$csp" DB_PASSWORD)"

    printf '%s' "$recommendation" | jq -c \
        --arg vnet "$NET_VNET_ID" \
        --argjson subnets "${NET_SUBNET_IDS:-[]}" \
        --arg sg "$NET_SG_ID" \
        --arg pass "$password" \
        --arg user "$(csp_env "$csp" DB_ADMIN_USERNAME)" \
        --arg name "$name" \
        --argjson public "$(bool_env "$csp" DB_PUBLIC_ACCESS true)" \
        --argjson backup "$(int_env "$csp" DB_BACKUP_RETENTION_DAYS 0)" \
        '
        .targetRDBMSInstances = [ .targetRDBMSInstances[] | (
              .rdbmsName = $name
            | .vNetId = $vnet
            | .subnetIds = $subnets
            | .securityGroupIds = (if $sg == "" then [] else [$sg] end)
            | .adminUserPassword = $pass
            | (if $user == "" then . else .adminUserName = $user end)
            | .publicAccess = $public
            | .backupRetentionDays = $backup
          ) ]'
}

# ------------------------------------------------------------------------------
# Object storage
# ------------------------------------------------------------------------------

# recommend_objectstorage <csp> — prints the RecommendedObjectStorage payload.
#
#   There is nothing from .env in the source description and no key for one: the
#   bucket's name in the CSP is a uid cb-tumblebug generates
#   (src/core/resource/objectStorage.go), so bucketName only identifies it on the
#   tumblebug side and nothing here has to be globally unique.
#
#   The feature flags are all false because beetle reads them as "what the source
#   bucket uses", and each one the target CSP cannot honour comes back as a
#   warning. Turning one on to see that warning is the only reason to change them.
recommend_objectstorage() {
    local csp="$1" body result region
    region="$(csp_region "$csp")"

    body="$(jq -nc --arg csp "$csp" --arg region "$region" \
        '{
          desiredCloud: { csp: $csp, region: $region },
          sourceObjectStorages: [ {
            bucketName:        "beetleenv-source-bucket-01",
            versioningEnabled: false,
            corsEnabled:       false,
            encryptionEnabled: false,
            isPublic:          false,
            totalSizeBytes:    1073741824,
            objectCount:       100,
            accessFrequency:   "frequent",
            tags: { createdBy: "beetleenv", purpose: "cm-centipede-test" }
          } ]
        }')"

    if ! bt_post "/recommendation/middleware/objectStorage?desiredCsp=$(urlq "$csp")&desiredRegion=$(urlq "$region")" "$body"; then
        die "the object storage recommendation failed - $(bt_message)"
    fi
    result="$(bt_payload)"
    print_warnings "$result"

    if [ "$(printf '%s' "$result" | jq '.targetObjectStorages | length')" -eq 0 ]; then
        die "beetle recommended no bucket for ${csp}"
    fi
    printf '%s' "$result"
}

# inject_bucket_name <recommendation json> <name> — the recommendation names the
#   bucket mig-os-01. The name only identifies it inside cb-tumblebug: the bucket
#   in the CSP is named after a uid tumblebug generates
#   (src/core/resource/objectStorage.go), which is why beetleenv has no globally
#   unique bucket name to configure.
inject_bucket_name() {
    printf '%s' "$1" | jq -c --arg name "$2" \
        '.targetObjectStorages = [ .targetObjectStorages[] | .bucketName = $name ]'
}

# ------------------------------------------------------------------------------
# VM infrastructure
# ------------------------------------------------------------------------------

# infra_request_body <csp> — the source infrastructure, as every endpoint that
#   takes one wants it. /recommendation/infra, /resources/specs and
#   /resources/osImages all bind the same RecommendInfraRequest, which is what
#   lets catalog.sh ask the last two the same question this one asks the first.
infra_request_body() {
    local csp="$1" os_raw os_id os_ver arch

    # The OS fields that matter are id and versionId, not prettyName. beetle
    # builds its image search key as `node.OS.ID + " " + node.OS.VersionID`
    # (pkg/core/recommendation/resource-node-image.go) and never looks at
    # prettyName, so those three are all this sends.
    #
    # The defaults are here rather than in .env because an empty _VM_SRC_OS has
    # to describe something: a node with no OS is answered with no image at all.
    os_raw="$(csp_lower "$(csp_env "$csp" VM_SRC_OS)")"
    if [ -z "$os_raw" ]; then os_raw="ubuntu 22.04"; fi
    os_id="${os_raw%% *}"
    os_ver=""
    if [ "$os_raw" != "$os_id" ]; then os_ver="${os_raw#* }"; fi

    arch="$(csp_env "$csp" VM_SRC_ARCH)"
    if [ -z "$arch" ]; then arch="x86_64"; fi

    jq -nc \
        --arg csp "$csp" \
        --arg region "$(csp_region "$csp")" \
        --argjson cores "$(int_env "$csp" VM_SRC_VCPU 2)" \
        --argjson memory "$(int_env "$csp" VM_SRC_MEMORY_GB 4)" \
        --argjson disk "$(int_env "$csp" VM_SRC_DISK_GB 20)" \
        --arg osid "$os_id" \
        --arg osver "$os_ver" \
        --arg arch "$arch" \
        --arg cidr "$BEETLEENV_SOURCE_CIDR" \
        '{
          desiredCspAndRegionPair: { csp: $csp, region: $region },
          onpremiseInfraModel: {
            # Not the network anything is created in - see BEETLEENV_SOURCE_CIDR.
            network: { ipv4Networks: { cidrBlocks: [ $cidr ] } },
            nodes: [ {
              # Reported back as targetInfra.nodeGroups[].label.sourceMachineId.
              hostname:  "beetleenv-source-01",
              machineId: "beetleenv-source-01",
              role:      "standalone",
              cpu:      { architecture: $arch, cpus: 1, cores: $cores, threads: $cores },
              memory:   { type: "DDR4", totalSize: $memory },
              rootDisk: { label: "root", type: "SSD", totalSize: $disk },
              os: {
                id:         $osid,
                versionId:  $osver,
                prettyName: ($osid + (if $osver == "" then "" else " " + $osver end))
              }
            } ]
          }
        }'
}

# image_search_key <csp> — the two values beetle will search cb-tumblebug with,
#   printed as "<osType>|<architecture>". Shown by catalog.sh so a search that
#   finds nothing can be compared against what the catalogue actually holds.
image_search_key() {
    printf '%s' "$(infra_request_body "$1")" | jq -r '
        .onpremiseInfraModel.nodes[0] |
        "\(.os.id) \(.os.versionId)|\(.cpu.architecture)"'
}

recommend_infra() {
    local csp="$1" body result region

    region="$(csp_region "$csp")"
    body="$(infra_request_body "$csp")"

    # limit=1: beetleenv wants one VM, not a shortlist to choose between.
    if ! bt_post "/recommendation/infra?desiredCsp=$(urlq "$csp")&desiredRegion=$(urlq "$region")&limit=1" "$body"; then
        die "the infrastructure recommendation failed - $(bt_message)"
    fi

    # An empty candidate list comes back as a 200 with no "data" key at all -
    # beetle's ApiResponse declares Data with omitempty, so a zero-length slice
    # is omitted rather than sent as [].
    result="$(bt_jq -c '.[0] // empty')"
    if [ -z "$result" ]; then
        die "beetle found no VM spec and OS image pair for ${csp} ${region}, so it
       recommended nothing. It answers 200 with an empty list rather than an
       error, which is why this is reported here and not by the call itself.

       The usual cause is that cb-tumblebug has no spec or image catalogue for
       this connection yet: it loads those on initialization and the load takes
       several minutes. A failed or interrupted 'make init' leaves it empty.

       Which half is missing:
         ./scripts/catalog.sh ${csp} vmspec
         ./scripts/catalog.sh ${csp} vmimage

       If both list entries, the source profile in .env asks for something the
       catalogue cannot match - lower BEETLEENV_$(csp_upper "$csp")_VM_SRC_VCPU
       and _VM_SRC_MEMORY_GB, or check _VM_SRC_OS names a distribution the CSP
       offers."
    fi
    print_warnings "$result"
    printf '%s' "$result"
}

# inject_infra_network <recommendation json> <csp> — point the recommendation at
#   the network beetleenv owns, and name everything itself.
#
#   NO nameSeed IS USED FOR THE VM, and that is deliberate. beetle's
#   ApplyNameSeed prefixes nodeGroups[].vNetId, subnetId and securityGroupIds
#   along with the names, so a seed applied on top of the real ids injected here
#   would ask for "cpbt-cpbt-vnet" and find nothing. Either the seed names
#   everything or this does; mixing the two silently breaks the references.
#
#   With useExisting=true the ids below are what beetle matches against, and it
#   creates from targetVNet/targetSecurityGroupList only if the match fails.
inject_infra_network() {
    local recommendation="$1" csp="$2"

    printf '%s' "$recommendation" | jq -c \
        --arg vnet "$(vnet_name "$csp")" \
        --arg sub1 "$(subnet_name "$csp" 1)" \
        --arg sub2 "$(subnet_name "$csp" 2)" \
        --arg sg "$(sg_name "$csp")" \
        --arg sshkey "$(resource_name "$csp" sshkey)" \
        --arg infra "$(resource_name "$csp" infra)" \
        --arg conn "$(connection_name "$csp")" \
        --arg cidr "$(csp_env "$csp" VNET_CIDR)" \
        --arg c1 "$(subnet_cidr "$csp" 1)" --arg z1 "$(csp_env "$csp" ZONE)" \
        --arg c2 "$(subnet_cidr "$csp" 2)" --arg z2 "$(csp_env "$csp" ZONE2)" \
        --argjson rules "$(firewall_rules "$csp")" \
        '
        .targetVNet = {
            name: $vnet, connectionName: $conn, cidrBlock: $cidr,
            description: "Created by beetleenv",
            subnetInfoList: [
                { name: $sub1, ipv4_CIDR: $c1, zone: $z1 },
                { name: $sub2, ipv4_CIDR: $c2, zone: $z2 }
            ]
        }
        | .targetSshKey.name = $sshkey
        | .targetSshKey.connectionName = $conn
        | .targetSecurityGroupList = [ {
            name: $sg, connectionName: $conn, vNetId: $vnet,
            description: "Created by beetleenv",
            firewallRules: $rules
        } ]
        | .targetInfra.name = $infra
        | .targetInfra.nodeGroups = [ .targetInfra.nodeGroups[] | (
              .vNetId = $vnet
            | .subnetId = $sub1
            | .subnetIds = [$sub1]
            | .securityGroupIds = [$sg]
            | .sshKeyId = $sshkey
          ) ]'
}
