#!/usr/bin/env bash
# postgresql_test.sh — PostgreSQL integration test runner
# Starts a PostgreSQL 16 container, runs TestPostgreSQL* tests, then removes the container.
#
# Usage:
#   ./postgresql_test.sh          # run all TestPostgreSQL* tests
#   ./postgresql_test.sh --keep   # keep the container after tests

set -euo pipefail

CONTAINER="dbmsx-postgres-test"
IMAGE="postgres:16"
HOST_PORT=5432
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
    echo "   POSTGRES_TEST_DSN=host=localhost port=${HOST_PORT} user=postgres password=pass dbname=testdb sslmode=disable"
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
  -e POSTGRES_PASSWORD=pass \
  -e POSTGRES_DB=testdb \
  -p "${HOST_PORT}:5432" \
  "${IMAGE}"

# --- wait for ready ---
echo ">> Waiting for PostgreSQL to be ready..."
READY=false
for i in $(seq 1 30); do
  if docker exec "${CONTAINER}" pg_isready -U postgres >/dev/null 2>&1; then
    echo ">> PostgreSQL ready (attempt ${i})"
    READY=true
    break
  fi
  sleep 2
done
if ! $READY; then
  echo "ERROR: PostgreSQL did not become ready within 60s"
  exit 1
fi

# --- seed: create testdb_src and test fixtures ---
# pgTestSetup uses loc.Database="testdb" for TestConnection,
# but TestPostgreSQLDumpRestore_SchemaOnly uses srcDB="testdb_src".
echo ">> Seeding: CREATE DATABASE testdb_src and test fixtures..."
docker exec "${CONTAINER}" psql -U postgres -c "CREATE DATABASE testdb_src;" 2>/dev/null || true
# Fixtures include a view, materialized view, function, procedure, trigger,
# sequence, enum type, extension and rule so schema-object metrics have
# something to enumerate. A single-quoted heredoc keeps the SQL (notably the
# $$-quoted routine bodies) safe from shell expansion.
docker exec -i "${CONTAINER}" psql -U postgres -d testdb_src <<'SQL' 2>/dev/null || true
  CREATE EXTENSION IF NOT EXISTS pgcrypto;
  CREATE TYPE mood AS ENUM ('happy', 'sad');
  CREATE SEQUENCE IF NOT EXISTS seq_demo;

  CREATE TABLE IF NOT EXISTS users (
    id   SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL
  );
  CREATE TABLE IF NOT EXISTS orders (
    id      SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id),
    amount  NUMERIC(10,2) NOT NULL
  );
  INSERT INTO users (name) VALUES ('alice'), ('bob');
  INSERT INTO orders (user_id, amount) VALUES (1, 99.99), (2, 49.50);

  CREATE OR REPLACE VIEW active_users AS SELECT id, name FROM users;
  CREATE MATERIALIZED VIEW mv_user_count AS SELECT count(*) AS c FROM users;

  CREATE FUNCTION user_count() RETURNS int LANGUAGE sql AS $$ SELECT count(*)::int FROM users $$;
  CREATE PROCEDURE add_user(uname text) LANGUAGE sql AS $$ INSERT INTO users(name) VALUES (uname) $$;

  CREATE FUNCTION trg_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$;
  CREATE TRIGGER trg_orders_ai AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION trg_fn();

  CREATE RULE rule_noop AS ON INSERT TO active_users DO INSTEAD NOTHING;
SQL

# --- export env vars ---
# POSTGRES_TEST_HOST can be overridden externally (e.g. POSTGRES_TEST_HOST=172.x.x.x ./postgresql_test.sh).
# Default: 127.0.0.1 (avoids Go resolving "localhost" to ::1 while Docker binds IPv4 only).
export POSTGRES_TEST_HOST="${POSTGRES_TEST_HOST:-127.0.0.1}"
export POSTGRES_TEST_PASS="pass"
export POSTGRES_TEST_PORT="${HOST_PORT}"
export POSTGRES_TEST_DSN="host=${POSTGRES_TEST_HOST} port=${HOST_PORT} user=postgres password=pass dbname=testdb sslmode=disable"

echo ">> POSTGRES_TEST_HOST=${POSTGRES_TEST_HOST}"

# --- run tests ---
echo ">> Running TestPostgreSQL* integration tests..."
cd "${SCRIPT_DIR}"
go test . -run TestPostgreSQL -v -timeout 120s -count=1
