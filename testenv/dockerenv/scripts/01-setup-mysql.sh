#!/usr/bin/env bash
# Source MySQL setup - accounts + load shop_db and hr_db from sql/
set -euo pipefail
. /opt/testenv/scripts/common.sh

echo "[MySQL] Starting setup..."

# ── Wait for MySQL ────────────────────────────────────────────────────────────
MAX_RETRY=30
RETRY=0
until mysqladmin ping --silent 2>/dev/null; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[MySQL] ERROR: MySQL did not start within timeout."
        exit 1
    fi
    echo "[MySQL] Waiting for MySQL... (${RETRY}/${MAX_RETRY})"
    sleep 2
done
echo "[MySQL] MySQL is ready."

# ── Set the root password + grant access ──────────────────────────────────────
# Unquoted heredoc, so the password is substituted; backslashes are doubled to
# survive it. DOCKERENV_PASSWORD carries no quote, backslash or $ (validated).
mysql --default-character-set=utf8mb4 -u root <<SQL
ALTER USER 'root'@'localhost' IDENTIFIED BY '${DOCKERENV_PASSWORD}';
DELETE FROM mysql.user WHERE User='';
DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost','127.0.0.1','::1');
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db='test' OR Db='test\\\\_%';

-- root from any host, so a direct connection through the published port can log
-- in as root. The flush reloads the grant tables the DELETEs above edited
-- directly; without it CREATE USER still sees the in-memory accounts.
FLUSH PRIVILEGES;
CREATE USER IF NOT EXISTS 'root'@'%' IDENTIFIED BY '${DOCKERENV_PASSWORD}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' WITH GRANT OPTION;

-- Application account
CREATE USER IF NOT EXISTS 'centipede'@'%' IDENTIFIED BY '${DOCKERENV_PASSWORD}';
GRANT ALL PRIVILEGES ON *.* TO 'centipede'@'%' WITH GRANT OPTION;

-- Read-only account
CREATE USER IF NOT EXISTS 'readonly'@'%' IDENTIFIED BY '${DOCKERENV_PASSWORD}';
GRANT SELECT ON shop_db.* TO 'readonly'@'%';
GRANT SELECT ON hr_db.*   TO 'readonly'@'%';

FLUSH PRIVILEGES;
SQL

echo "[MySQL] Users created."

# ── Load the test databases ───────────────────────────────────────────────────
echo "[MySQL] Loading shop_db..."
MYSQL_PWD="$DOCKERENV_PASSWORD" mysql --default-character-set=utf8mb4 -u root < /opt/testenv/sql/shop_db_mysql.sql
echo "[MySQL] shop_db loaded."

echo "[MySQL] Loading hr_db..."
MYSQL_PWD="$DOCKERENV_PASSWORD" mysql --default-character-set=utf8mb4 -u root < /opt/testenv/sql/hr_db_mysql.sql
echo "[MySQL] hr_db loaded."

echo "[MySQL] Setup complete."
