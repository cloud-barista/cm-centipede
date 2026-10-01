#!/usr/bin/env bash
#
# ncp-to-aws-mysql — migrate an NCP Cloud DB for MySQL database into AWS RDS.
#
# Every API call the README walks through, in order, written out as plain curl.
# This is the happy path and nothing else: no retries, no error handling, no
# cleanup. Each step assumes the one before it worked, which is what keeps the
# sequence readable as a description of the API.
#
# The request bodies are written out where they are sent, with each value read
# straight from its variable ($NAME, $SRC_HOST, ...) at the point it appears. jq
# shows up only to pull one value out of a response, never to build a body.
#
# Three things have to exist first (README steps 1-3):
#   the stack            make up                    (from the repo root)
#   the source database  testenv/tofuenv: provision.sh ncp database, then a
#                        PUBLIC DOMAIN issued in the NCP console, then
#                        ncp-db-domain.sh + gen-data.sh
#   the target instance  testenv/beetleenv/scripts/provision.sh aws database --engine mysql
#
# Cleanup is the README's step 8, not this script's job.

set -euo pipefail
cd "$(dirname "$0")"

# ── Servers ──────────────────────────────────────────────────────────────────
HB_BASE=${HB_BASE:-http://localhost:8081/honeybee}
CP_BASE=${CP_BASE:-http://localhost:8085/centipede}
CP_AUTH=${CP_AUTH:-default:default}

# ── Source — the NCP managed MySQL (testenv/tofuenv) ─────────────────────────
# Unlike the object storage example there is no region to resolve here: a db
# connection carries its own host and port, so provider_name records where the
# data lives and nothing is derived from it.
#
# SRC_HOST is the PUBLIC DOMAIN of the DB server, and it has no default because
# it cannot be known in advance: it is issued once per server from the NCP
# console — there is no API for it — and only then does tofu report it.
#
#   testenv/tofuenv/scripts/conn-info.sh ncp database
#
# SRC_DB_PASS has no default either: tofuenv keeps it in OpenBao (secret/db/ncp)
# and blanks NCP_DB_PASSWORD out of .env, so it is passed in rather than read
# from a file.
SRC_PROVIDER=${SRC_PROVIDER:-ncp}
SRC_HOST=${SRC_HOST:?required — the MySQL public domain, from tofuenv: ./scripts/conn-info.sh ncp database}
SRC_PORT=${SRC_PORT:-3306}
SRC_DB_USER=${SRC_DB_USER:-dbadmin}
SRC_DB_PASS=${SRC_DB_PASS:?required — the NCP DB master password, from OpenBao secret/db/ncp}
SRC_DB=${SRC_DB:-testdb}

# ── Target — the AWS RDS instance (testenv/beetleenv) ────────────────────────
# NS_ID and RDBMS_ID come from: testenv/beetleenv/scripts/conn-info.sh aws --ids
#
# DB_PASSWORD is the instance's master password, and it has NO default on
# purpose: cm-beetle takes it when the instance is created and never hands it
# back, so cm-centipede has to be told. It is the same value as
# BEETLEENV_AWS_DB_PASSWORD in testenv/beetleenv/.env. Unset, the line below
# stops the script here rather than at the plan call.
NS_ID=${NS_ID:-cpbt01}
RDBMS_ID=${RDBMS_ID:-cpbt-aws-db-mysql}
DB_PASSWORD=${DB_PASSWORD:?required — the RDS instance master password, the same value as BEETLEENV_AWS_DB_PASSWORD in testenv/beetleenv/.env}
DST_DB=${DST_DB:-testdb}

NAME=${NAME:-ncp-to-aws-mysql}
POLL=${POLL:-5}

# =============================================================================
# 1. Register the source group with cm-honeybee
# =============================================================================
# Type "db" is not a choice: a database inspect is refused for a source group of
# any other type.
#
# provider_name is required and validated against the same table a minio group
# uses — but nothing is derived from it. A db connection carries its own host and
# port, so there is no region or endpoint rule to apply, which is why there is no
# region_name here even though the object storage example needs one for ncp.

echo "==> 1/6  Registering the source group"

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME\",
        \"description\": \"NCP Cloud DB for MySQL source\",
        \"type\": \"db\",
        \"provider_name\": \"$SRC_PROVIDER\"
      }")

echo "$RESPONSE"
SG_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 2. Register the connection
# =============================================================================
# No agent — honeybee connects to MySQL over the wire, which is what
# db_access_type "direct" says.
#
# db_tls_mode "prefer" encrypts where the server offers TLS and still connects
# where it does not. It is worth stating here and not in the on-premises example:
# this connection crosses the public internet, because a managed NCP DB is
# reached through its public domain.
#
# db_name is deliberately left out. Empty means "every user database on this
# server", and honeybee hides the ones MySQL ships with — so on this server it
# collects testdb and nothing else.

echo "==> 2/6  Registering the connection"

RESPONSE=$(curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME-src\",
        \"description\": \"NCP Cloud DB for MySQL, direct access\",
        \"db_type\": \"mysql\",
        \"db_access_type\": \"direct\",
        \"db_host\": \"$SRC_HOST\",
        \"db_port\": \"$SRC_PORT\",
        \"db_username\": \"$SRC_DB_USER\",
        \"db_password\": \"$SRC_DB_PASS\",
        \"db_tls_mode\": \"prefer\"
      }")

echo "$RESPONSE"
CONN_ID=$(echo "$RESPONSE" | jq -r .id)

# =============================================================================
# 3. Inspect the source
# =============================================================================
# The server version, the database size and the table list are always collected.
# Everything below is opt-in, and every option MySQL has is asked for here — the
# schema objects are what validation compares later, so leaving them out would
# make the verdict weaker. (materialized_views, sequences, types, extensions and
# rules are PostgreSQL's, validators and sample_size MongoDB's; honeybee drops
# the fields the connection's db_type has no equivalent for.)

echo "==> 3/6  Inspecting the databases on $SRC_HOST:$SRC_PORT"

curl -s -X POST "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/import/db" \
  -H 'Content-Type: application/json' \
  -d "{
        \"metric\": {
          \"row_count_exact\": true,
          \"columns\": true,
          \"indexes\": true,
          \"views\": true,
          \"foreign_keys\": true,
          \"functions\": true,
          \"procedures\": true,
          \"triggers\": true,
          \"events\": true
        }
      }" >/dev/null

# /db/refined returns the whole SourceDataMigrationModel wrapper, which is exactly
# what POST /plans/target takes as its "source" — it goes in unchanged below.
# Not printed: the table and schema-object listing buries everything else.
SRC_MODEL=$(curl -s "$HB_BASE/source_group/$SG_ID/connection_info/$CONN_ID/db/refined")

echo "    collected: $(echo "$SRC_MODEL" | jq -r '[.sourceDataMigrationModel.databases[0].databases[].database] | join(", ")')"

# =============================================================================
# 4. Build the plan
# =============================================================================
# plans is an array: one entry per (source entry, destination). This example has
# one connection going to one instance, so it holds a single entry. A source
# group with several connections would send several, each naming its own
# destination — which is why srcConnection is there, saying which entry of the
# source model this destination is for.
#
# The two ends are named in completely different ways. The source arrives inside
# "source" as honeybee collected it, carrying a honeybee reference cm-centipede
# resolves back into the NCP host and the account it was given in step 2. The
# target is a cm-beetle reference plus one secret: beetleDb names an instance
# that already exists and cm-centipede reads its host, port and admin account
# from cm-beetle — but not the password, which cm-beetle never hands back, so it
# travels here. It doubles as the admin password for creating the target
# database, which cm-centipede does itself.
#
# username is deliberately not sent: empty means "the instance admin as cm-beetle
# reports it", and naming it too only creates a way for the two to disagree.
#
# tlsMode "prefer" encrypts where the instance offers TLS and still connects
# where it does not — the same value the source connection uses.
#
# dbmsFilter names the one database on the server and drops one view from it.
# Validation knows about the rule, so the view is reported as excluded rather
# than as missing.

echo "==> 4/6  Planning $SRC_DB -> $DST_DB on $RDBMS_ID"

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
            \"source\": \"beetleDb\",
            \"beetleDb\": {
              \"nsId\": \"$NS_ID\",
              \"rdbmsId\": \"$RDBMS_ID\",
              \"password\": \"$DB_PASSWORD\",
              \"tlsMode\": \"prefer\"
            }
          },
          \"dbmsFilter\": {
            \"databases\": [
              {
                \"targetMapping\": { \"srcName\": \"$SRC_DB\", \"dstName\": \"$DST_DB\" },
                \"rules\": [
                  { \"type\": \"object_exclude\", \"kind\": \"view\", \"name\": \"v_low_stock_alert\" }
                ]
              }
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
#
# dbmsOnFailure is DBMS-only and decides what happens to a target database a
# failed run left behind: "cleanup" drops it, "keep" leaves it to be inspected.

echo "==> 5/6  Migrating"

RESPONSE=$(curl -s -X POST "$CP_BASE/migration" -u "$CP_AUTH" \
  -H 'Content-Type: application/json' \
  -d "{
        \"name\": \"$NAME\",
        \"description\": \"NCP Cloud DB for MySQL to AWS RDS\",
        \"plan\": $PLAN,
        \"dbmsOnFailure\": \"cleanup\"
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
# cm-centipede re-reads both databases and compares exact row counts per table,
# twelve kinds of schema object, and the character sets. Objects the filter
# excluded are reported as excluded rather than as missing.

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
# sign that anything was left out. A DBMS migration logs one entry per database,
# so one page is plenty here.

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
