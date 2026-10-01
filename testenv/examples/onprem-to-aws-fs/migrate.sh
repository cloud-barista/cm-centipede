#!/usr/bin/env bash
#
# onprem-to-aws-fs — migrate an on-premises directory onto an AWS VM.
#
# Every API call the README walks through, in order, written out as plain curl.
# This is the happy path and nothing else: no retries, no error handling, no
# cleanup. Each step assumes the one before it worked, which is what keeps the
# sequence readable as a description of the API.
#
# The request bodies are written out where they are sent, with each value read
# straight from its variable ($NAME, $HOST_IP, ...) at the point it appears. jq
# shows up only to pull one value out of a response, never to build a body.
#
# Three things have to exist first (README steps 1-3):
#   the stack          make up                    (from the repo root)
#   the source         testenv/dockerenv/dockerenv-up.sh
#   the target VM      testenv/beetleenv/scripts/provision.sh aws vm
#
# Cleanup is the README's step 8, not this script's job.

set -euo pipefail
cd "$(dirname "$0")"

# ── Servers ──────────────────────────────────────────────────────────────────
HB_BASE=${HB_BASE:-http://localhost:8081/honeybee}
CP_BASE=${CP_BASE:-http://localhost:8085/centipede}
CP_AUTH=${CP_AUTH:-default:default}

# ── Source — the on-premises machine (testenv/dockerenv fs-source) ───────────
# HOST_IP is the address of the host machine the source container runs on. The
# source is reached through the port it publishes (SRC_PORT below), so this is the
# host's address — not the container's, and not 127.0.0.1. Set it every run:
#   sudo HOST_IP=172.24.78.163 ./migrate.sh
HOST_IP=${HOST_IP:-127.0.0.1}
SRC_PORT=${SRC_PORT:-32210}
SRC_USER=${SRC_USER:-root}
SRC_KEY=${SRC_KEY:-../../dockerenv/ssh_keys/id_rsa}
SRC_PATH=${SRC_PATH:-/testdata}

# ── Target — the AWS VM (testenv/beetleenv) ──────────────────────────────────
# All three ids come from: testenv/beetleenv/scripts/conn-info.sh aws --ids
NS_ID=${NS_ID:-cpbt01}
INFRA_ID=${INFRA_ID:-cpbt-aws-infra}
NODE_ID=${NODE_ID:-vm-beetleenv-source-01-1}
DST_PATH=${DST_PATH:-/home/cb-user/testdata}

NAME=${NAME:-onprem-to-aws-fs}
POLL=${POLL:-5}

# =============================================================================
# 1. Register the source group with cm-honeybee
# =============================================================================
# Type "fs" is not a choice: a filesystem inspect is refused for a source group
# of any other type.

echo "==> 1/6  Registering the source group"

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME\",
        \"description\": \"on-premises filesystem source\",
        \"type\": \"fs\"
      }")

echo "$RESPONSE"
SG_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 2. Register the connection — this also installs the honeybee agent
# =============================================================================
# cm-honeybee does not read a remote filesystem itself; it runs its agent on the
# source over SSH, and this call is what installs it. The install downloads a
# binary from the internet, so the source needs outbound access and the call
# takes tens of seconds.
#
# It answers 200 whether or not that worked. connection_status and agent_status
# in the response are the only report, which is why the whole response is printed.

echo "==> 2/6  Registering the connection (this installs the honeybee agent)"

# jq -Rs turns the PEM file into a complete JSON string — quotes and \n escapes
# included — so $SRC_KEY_JSON goes into the body below without quotes around it.
SRC_KEY_JSON=$(jq -Rs . <"$SRC_KEY")

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME-src\",
        \"description\": \"on-premises host, key authentication\",
        \"ip_address\": \"$HOST_IP\",
        \"ssh_port\": \"$SRC_PORT\",
        \"user\": \"$SRC_USER\",
        \"private_key\": $SRC_KEY_JSON,
        \"fs_scan_path\": \"$SRC_PATH\"
      }")

echo "$RESPONSE"
CONN_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 3. Inspect the source
# =============================================================================
# The path is not in this request: it is the connection's fs_scan_path, set in
# step 2. Only how much detail to collect is asked for here.
#
# Every metric is asked for. Each one costs a full walk of the tree on the
# source, so a large dataset is a reason to ask for fewer.
# max_depth 0 lists the whole tree.

echo "==> 3/6  Inspecting $SRC_PATH"

curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/import/fs" \
  -H 'Content-Type: application/json' \
  -d "{
        \"max_depth\": 0,
        \"metric\": {
          \"total_size\": true,
          \"file_count\": true,
          \"extension_count\": true,
          \"folder_mod_time\": true,
          \"folder_perms\": true,
          \"folder_file_count\": true,
          \"folder_file_size\": true
        }
      }" >/dev/null

# /fs/refined returns the whole SourceDataMigrationModel wrapper, which is exactly
# what POST /plans/target takes as its "source" — it goes in unchanged below.
# Not printed: the folder listing is long enough to bury everything else.
SRC_MODEL=$(curl -s "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/fs/refined")

# The scan root is read back rather than assumed, because the plan's srcName has
# to match it exactly.
SCAN_ROOT=$(echo "$SRC_MODEL" | jq -r '.sourceDataMigrationModel.fileSystems[0].path')
echo "    scan root $SCAN_ROOT"

# =============================================================================
# 4. Build the plan
# =============================================================================
# plans is an array: one entry per (source entry, destination). This example has
# one connection going to one node, so it holds a single entry. A source group
# with several connections would send several, each naming its own destination —
# which is why srcConnection is there, saying which entry of the source model
# this destination is for.
#
# dstConnection is a reference, not credentials: beetleSsh names a node that
# already exists, and cm-centipede resolves its address, account and private key
# through cm-beetle.
#
# targetMapping is not optional. Without it the destination path equals the source
# path, and the transfer would try to write "/testdata" at the ROOT of the target
# VM as an ordinary user.
#
# rules are the filter: everything migrates except what they exclude. Validation
# knows about them, so an excluded file is reported as skipped, not as missing.
# ">=" rather than ">" on purpose — the two .mp4 files in this dataset are exactly
# 1048576 bytes, so ">" would keep both and the size rule would exclude nothing.

echo "==> 4/6  Planning $SCAN_ROOT -> $DST_PATH"

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
            \"source\": \"beetleSsh\",
            \"beetleSsh\": {
              \"nsId\": \"$NS_ID\",
              \"infraId\": \"$INFRA_ID\",
              \"nodeId\": \"$NODE_ID\"
            }
          },
          \"fileSystemFilter\": {
            \"targetMapping\": {
              \"srcName\": \"$SCAN_ROOT\",
              \"dstName\": \"$DST_PATH\"
            },
            \"rules\": [
              { \"action\": \"exclude\", \"type\": \"glob\", \"pattern\": \"**/*.log\" },
              { \"action\": \"exclude\", \"type\": \"size\", \"op\": \">=\", \"value\": 1048576 }
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
        \"description\": \"on-premises filesystem to an AWS VM\",
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
# cm-centipede re-reads both ends over SSH and compares a SHA256 per file. Files
# the filter excluded are counted in one "skipped" line rather than reported as
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
# sign that anything was left out. A filesystem migration logs one entry per
# migrated folder, so one page is plenty here.

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
