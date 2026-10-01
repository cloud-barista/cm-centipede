#!/usr/bin/env bash
#
# ncp-to-aws-os — migrate an NCP Object Storage bucket into an AWS bucket.
#
# Every API call the README walks through, in order, written out as plain curl.
# This is the happy path and nothing else: no retries, no error handling, no
# cleanup. Each step assumes the one before it worked, which is what keeps the
# sequence readable as a description of the API.
#
# The request bodies are written out where they are sent, with each value read
# straight from its variable ($NAME, $SRC_BUCKET, ...) at the point it appears.
# jq shows up only to pull one value out of a response, never to build a body.
#
# Three things have to exist first (README steps 1-3):
#   the stack          make up                    (from the repo root)
#   the source bucket  testenv/tofuenv: provision.sh ncp bucket + gen-data.sh
#   the target bucket  testenv/beetleenv/scripts/provision.sh aws bucket
#
# Cleanup is the README's step 8, not this script's job.

set -euo pipefail
cd "$(dirname "$0")"

# ── Servers ──────────────────────────────────────────────────────────────────
HB_BASE=${HB_BASE:-http://localhost:8081/honeybee}
CP_BASE=${CP_BASE:-http://localhost:8085/centipede}
CP_AUTH=${CP_AUTH:-default:default}

# ── Source — the NCP bucket (testenv/tofuenv) ────────────────────────────────
# There is no address to supply: NCP Object Storage is somewhere both cm-honeybee
# and cm-centipede already know how to find. What they cannot work out is which
# NCP, so the region is named — one value, on the source group. See the README's
# step 4.
#
#   SRC_REGION  region_name on the source group, and the region that appears in
#               the HOST: "<region>.object.ncloudstorage.com". Lowercase:
#               tofuenv writes TF_VAR_ncp_region as "KR" and the host is "kr.…".
#
# NCP signs its requests with "kr-standard" rather than "kr", but that is not
# named here and there is no connection field for it: honeybee leaves the signing
# region unset for NCP, which makes the S3 SDK ask the bucket for its location
# and sign with the answer.
#
# The keys have no defaults: tofuenv moves them into OpenBao and blanks them out
# of .env, so they are passed in rather than read from a file.
SRC_PROVIDER=${SRC_PROVIDER:-ncp}
SRC_REGION=${SRC_REGION:-kr}
SRC_BUCKET=${SRC_BUCKET:-cptf-ncp-bucket-test}
SRC_ACCESS_KEY=${SRC_ACCESS_KEY:?required — the NCP access key, from OpenBao secret/csp/ncp}
SRC_SECRET_KEY=${SRC_SECRET_KEY:?required — the NCP secret key, from OpenBao secret/csp/ncp}

# ── Target — the AWS bucket (testenv/beetleenv) ──────────────────────────────
# Both ids come from: testenv/beetleenv/scripts/conn-info.sh aws --ids
NS_ID=${NS_ID:-cpbt01}
OS_ID=${OS_ID:-cpbt-aws-bucket}

NAME=${NAME:-ncp-to-aws-os}
POLL=${POLL:-5}

# =============================================================================
# 1. Register the source group with cm-honeybee
# =============================================================================
# Type "minio" is not a choice, and it is not a statement about the software
# either: an object storage inspect is refused for a source group of any other
# type, and provider_name is what says which cloud it actually is.
#
# region_name is required for ncp and is what builds the endpoint host.

echo "==> 1/6  Registering the source group"

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME\",
        \"description\": \"NCP Object Storage source\",
        \"type\": \"minio\",
        \"provider_name\": \"$SRC_PROVIDER\",
        \"region_name\": \"$SRC_REGION\"
      }")

echo "$RESPONSE"
SG_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 2. Register the connection
# =============================================================================
# No agent, no endpoint and no region: honeybee already knows where NCP Object
# Storage is, and the source group's region_name is the only region it needs.
#
# os_use_ssl is absent on purpose. It is honoured only where the caller supplies
# the host (onprem, openstack); for ncp the transport is the provider's own
# fixed value, and stating it here would only suggest otherwise.

echo "==> 2/6  Registering the connection"

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME-src\",
        \"description\": \"NCP Object Storage, direct S3 access\",
        \"os_access_type\": \"direct\",
        \"os_access_key_id\": \"$SRC_ACCESS_KEY\",
        \"os_secret_access_key\": \"$SRC_SECRET_KEY\"
      }")

echo "$RESPONSE"
CONN_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 3. Inspect the source bucket
# =============================================================================
# The bucket is not in this request: it is the connection's os_scan_bucket, set
# in step 2. One connection holds one bucket, and one result — pointing an
# existing connection at another bucket marks its collected result stale, so a
# second bucket means a second connection.
#
# Every metric is asked for here. Each one costs a full pass over the bucket's
# keys, so a bucket of any size is a reason to ask for fewer. "prefix" is object
# storage's answer to the filesystem's "folder": the prefix_* fields count only
# the objects directly under each prefix, while the first three cover the whole
# scanned bucket.

echo "==> 3/6  Inspecting bucket $SRC_BUCKET"

curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/import/objectstorage" \
  -H 'Content-Type: application/json' \
  -d "{
        \"metric\": {
          \"total_size\": true,
          \"object_count\": true,
          \"extension_count\": true,
          \"prefix_mod_time\": true,
          \"prefix_object_count\": true,
          \"prefix_object_size\": true
        }
      }" >/dev/null

# /objectstorage/refined returns the whole SourceDataMigrationModel wrapper, which
# is exactly what POST /plans/target takes as its "source" — it goes in unchanged
# below. Not printed: the prefix listing is long enough to bury everything else.
SRC_MODEL=$(curl -s "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/objectstorage/refined")

# The scan root is read back rather than assumed. honeybee builds it as
# "<bucket>/<prefix>" and normalises an empty prefix to "<bucket>/" — trailing
# slash and all — and the plan's srcName has to match it exactly.
SCAN_ROOT=$(echo "$SRC_MODEL" | jq -r '.sourceDataMigrationModel.objectStorages[0].path')
echo "    scan root $SCAN_ROOT"

# =============================================================================
# 4. Build the plan
# =============================================================================
# plans is an array: one entry per (source entry, destination). This example has
# one connection going to one bucket, so it holds a single entry. A source group
# with several connections would send several, each naming its own destination —
# which is why srcConnection is there, saying which entry of the source model
# this destination is for.
#
# The two ends are named in completely different ways, and both are worth seeing
# side by side: the source carries its own S3 keys, while the target is a
# reference cm-centipede resolves through cm-beetle and cb-tumblebug, with no key
# anywhere in this file.
#
# targetMapping carries no dstName, so the plan rewrites the destination's first
# segment to the osId and the objects land at the root of the target bucket,
# keeping their prefixes. To put them under a prefix instead, write the osId
# yourself: "dstName": "<osId>/archive/".
#
# rules are the filter: everything migrates except what they exclude. Validation
# knows about them, so an excluded object is reported as skipped, not as missing.

echo "==> 4/6  Planning $SCAN_ROOT -> $OS_ID"

RESPONSE=$(curl -s -X POST "$CP_BASE/plans/target" -u "$CP_AUTH" \
  -H 'Content-Type: application/json' \
  -d "{
        \"source\": $SRC_MODEL,
        \"plans\": [{
          \"srcConnection\": {
            \"source\": \"honeybee\",
            \"honeybee\": { \"connectionId\": \"$CONN_ID\" }
          },
          \"dstConnection\": {
            \"source\": \"beetleObjectStorage\",
            \"beetleObjectStorage\": {
              \"nsId\": \"$NS_ID\",
              \"osId\": \"$OS_ID\"
            }
          },
          \"objectStorageFilter\": {
            \"targetMapping\": {
              \"srcName\": \"$SCAN_ROOT\"
            },
            \"rules\": [
              { \"action\": \"exclude\", \"type\": \"glob\", \"pattern\": \"**/*.zip\" }
            ]
          }
        }]
      }")

# Every cm-centipede response is wrapped in {"success":..., "data":...}, and the
# migration call below wants the plan itself — so the envelope comes off here.
PLAN=$(echo "$RESPONSE" | jq -c .data)
echo "$PLAN"

# =============================================================================
# 5. Run the migration
# =============================================================================
# Creating it starts it, so the call returns immediately and the run is polled.

echo "==> 5/6  Migrating"

RESPONSE=$(curl -s -X POST "$CP_BASE/migration" -u "$CP_AUTH" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME\",
        \"description\": \"NCP Object Storage bucket to an AWS bucket\",
        \"plan\": $PLAN
      }")

MIG_ID=$(echo "$RESPONSE" | jq -r .data.id)
echo "    migrationId $MIG_ID"

while :; do
  STATUS=$(curl -s "$CP_BASE/migration/$MIG_ID" -u "$CP_AUTH" | jq -r .data.status)
  echo "    status=$STATUS"
  [ "$STATUS" = completed ] && break
  sleep "$POLL"
done

# =============================================================================
# 6. Validate
# =============================================================================
# cm-centipede re-lists both buckets and compares them object by object: size
# first, then the ETag where the ETag means what it appears to mean. Objects the
# filter excluded are counted in one "skipped" line rather than reported as
# missing from the destination.

echo "==> 6/6  Validating"

curl -s -X POST "$CP_BASE/migration/$MIG_ID/validation" -u "$CP_AUTH" >/dev/null

while :; do
  STATUS=$(curl -s "$CP_BASE/migration/$MIG_ID/validation" -u "$CP_AUTH" | jq -r .data.validationStatus)
  echo "    validation=$STATUS"
  [ "$STATUS" != running ] && break
  sleep "$POLL"
done

curl -s "$CP_BASE/migration/$MIG_ID/validation" -u "$CP_AUTH"
echo

# =============================================================================
# The migration log
# =============================================================================
# Paginated: pageSize caps at 100 and defaults to 20, and .data.total is the only
# sign that anything was left out. An object storage migration logs one entry per
# migrated bucket, so one page is plenty here.

echo "==> Migration log"

curl -s "$CP_BASE/migration/$MIG_ID/logs?page=1&pageSize=100" -u "$CP_AUTH"
echo

# =============================================================================
# Cleaning up
# =============================================================================
# Printed rather than run: the migration record is what holds the plan, the logs
# and the validation result, so it is worth reading before it is dropped. The
# three ids this run created are filled in, so the lines below can be pasted as
# they are. Delete the record first, then what it referred to.

echo
echo "==> Clean up when you are done with this run"
echo
echo "  # the migration record — the plan, the logs and the validation go with it"
echo "  curl -s -X DELETE -u $CP_AUTH $CP_BASE/migration/$MIG_ID"
echo
echo "  # the honeybee registration"
echo "  curl -s -X DELETE $HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID"
echo "  curl -s -X DELETE $HB_BASE/source_group/$SG_ID"
