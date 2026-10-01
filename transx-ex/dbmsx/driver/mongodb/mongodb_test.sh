#!/usr/bin/env bash
# mongodb_test.sh — MongoDB integration test runner
# Starts a MongoDB 7 container, runs TestMongoDB* tests, then removes the container.
#
# Usage:
#   ./mongodb_test.sh          # run all TestMongoDB* tests
#   ./mongodb_test.sh --keep   # keep the container after tests

set -euo pipefail

CONTAINER="dbmsx-mongo-test"
IMAGE="mongo:7"
HOST_PORT=27017
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KEEP=false
for arg in "$@"; do
  case "$arg" in
    --keep) KEEP=true ;;
    *) echo "Unknown option: $arg"; exit 1 ;;
  esac
done

# --- cleanup trap ---
cleanup() {
  if $KEEP; then
    echo ">> Container ${CONTAINER} kept running (--keep)"
    echo "   MONGODB_TEST_URI=mongodb://localhost:${HOST_PORT}"
  else
    echo ">> Removing container ${CONTAINER}..."
    docker rm -f "${CONTAINER}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# --- remove stale container ---
docker rm -f "${CONTAINER}" 2>/dev/null || true

# --- start container ---
echo ">> Starting ${IMAGE}..."
docker run -d \
  --name "${CONTAINER}" \
  -p "${HOST_PORT}:27017" \
  "${IMAGE}"

# --- wait for ready ---
echo ">> Waiting for MongoDB to be ready..."
READY=false
for i in $(seq 1 30); do
  if docker exec "${CONTAINER}" mongosh --eval "db.adminCommand('ping')" --quiet >/dev/null 2>&1; then
    echo ">> MongoDB ready (attempt ${i})"
    READY=true
    break
  fi
  sleep 2
done
if ! $READY; then
  echo "ERROR: MongoDB did not become ready within 60s"
  exit 1
fi

# --- seed: documents + a validated collection + a view + an index so that
#     schema-object metrics (views, validators) have fixtures to enumerate. ---
echo ">> Seeding testdb_src with test fixtures..."
MONGO_HOST="${MONGODB_TEST_HOST:-127.0.0.1}"
docker exec "${CONTAINER}" mongosh "mongodb://127.0.0.1:27017/testdb_src" --eval "
  db.users.insertMany([
    { name: 'alice', age: 30 },
    { name: 'bob',   age: 25 }
  ]);
  db.orders.insertMany([
    { user: 'alice', amount: 99.99 },
    { user: 'bob',   amount: 49.50 }
  ]);
  db.users.createIndex({ name: 1 }, { unique: true });
  db.createCollection('accounts', {
    validator: { \$jsonSchema: {
      bsonType: 'object',
      required: ['owner'],
      properties: { owner: { bsonType: 'string' } }
    } }
  });
  db.createCollection('adult_users', {
    viewOn: 'users',
    pipeline: [ { \$match: { age: { \$gte: 18 } } } ]
  });
" --quiet

# --- export env vars ---
# MONGODB_TEST_HOST can be overridden externally (e.g. MONGODB_TEST_HOST=172.x.x.x ./mongodb_test.sh).
# Default: 127.0.0.1 (avoids Go resolving "localhost" to ::1 while Docker binds IPv4 only).
# mongodb_test.go parses MONGODB_TEST_URI to extract host:port for DirectConfig.
export MONGODB_TEST_HOST="${MONGODB_TEST_HOST:-127.0.0.1}"
export MONGODB_TEST_URI="mongodb://${MONGODB_TEST_HOST}:${HOST_PORT}"

echo ">> MONGODB_TEST_HOST=${MONGODB_TEST_HOST}"

# --- run tests ---
echo ">> Running TestMongoDB* integration tests..."
cd "${SCRIPT_DIR}"
go test . -run TestMongoDB -v -timeout 120s -count=1
