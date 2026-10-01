#!/usr/bin/env bash
# mysql_test.sh — MySQL integration test runner
# Starts a MySQL 8 container, runs TestMySQL* tests, then removes the container.
#
# Usage:
#   ./mysql_test.sh          # run all TestMySQL* tests
#   ./mysql_test.sh --keep   # keep the container after tests

set -euo pipefail

CONTAINER="dbmsx-mysql-test"
IMAGE="mysql:8"
HOST_PORT=3306
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
    echo "   MYSQL_TEST_DSN=root:pass@tcp(localhost:${HOST_PORT})/testdb_src?parseTime=true"
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
  -e MYSQL_ROOT_PASSWORD=pass \
  -e MYSQL_DATABASE=testdb_src \
  -p "${HOST_PORT}:3306" \
  "${IMAGE}"

# --- wait for ready ---
# Use "SELECT 1" instead of mysqladmin ping: MySQL 8 completes user init
# after the server starts accepting connections, so ping can succeed before
# root authentication is available.
echo ">> Waiting for MySQL to be ready..."
READY=false
for i in $(seq 1 30); do
  if docker exec "${CONTAINER}" mysql -uroot -ppass -e "SELECT 1" >/dev/null 2>&1; then
    echo ">> MySQL ready (attempt ${i})"
    READY=true
    break
  fi
  sleep 2
done
if ! $READY; then
  echo "ERROR: MySQL did not become ready within 60s"
  exit 1
fi

# --- seed: tables (with FK) + view/function/procedure/trigger/event so that
#     schema-object metrics have fixtures to enumerate. ---
echo ">> Seeding testdb_src with test fixtures..."
docker exec "${CONTAINER}" mysql -uroot -ppass testdb_src -e "
  CREATE TABLE IF NOT EXISTS users (
    id   INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL
  );
  CREATE TABLE IF NOT EXISTS orders (
    id      INT AUTO_INCREMENT PRIMARY KEY,
    user_id INT NOT NULL,
    amount  DECIMAL(10,2) NOT NULL,
    CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id)
  );
  INSERT INTO users (name) VALUES ('alice'), ('bob');
  INSERT INTO orders (user_id, amount) VALUES (1, 99.99), (2, 49.50);

  CREATE OR REPLACE VIEW active_users AS SELECT id, name FROM users;
"
docker exec "${CONTAINER}" mysql -uroot -ppass testdb_src -e "
  CREATE FUNCTION user_count() RETURNS INT DETERMINISTIC RETURN (SELECT COUNT(*) FROM users);
  CREATE PROCEDURE add_user(IN uname VARCHAR(100)) INSERT INTO users(name) VALUES (uname);
  CREATE TRIGGER trg_orders_ai AFTER INSERT ON orders FOR EACH ROW SET @last_order = NEW.id;
  CREATE EVENT ev_noop ON SCHEDULE EVERY 1 DAY DO SELECT 1;
"

# --- export env vars ---
# MYSQL_TEST_HOST can be overridden externally (e.g. MYSQL_TEST_HOST=172.x.x.x ./mysql_test.sh).
# Default: 127.0.0.1 (avoids Go resolving "localhost" to ::1 while Docker binds IPv4 only).
export MYSQL_TEST_HOST="${MYSQL_TEST_HOST:-127.0.0.1}"
export MYSQL_TEST_PASS="pass"
export MYSQL_TEST_PORT="${HOST_PORT}"
export MYSQL_TEST_DSN="root:pass@tcp(${MYSQL_TEST_HOST}:${HOST_PORT})/testdb_src?parseTime=true"

echo ">> MYSQL_TEST_HOST=${MYSQL_TEST_HOST}"

# --- run tests ---
echo ">> Running TestMySQL* integration tests..."
cd "${SCRIPT_DIR}"
go test . -run TestMySQL -v -timeout 120s -count=1
