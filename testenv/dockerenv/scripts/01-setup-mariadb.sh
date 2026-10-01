#!/usr/bin/env bash
# Source MariaDB setup - accounts + load shop_db and hr_db from sql/
set -euo pipefail

echo "[MariaDB] Starting setup..."

# ── Wait for MariaDB ──────────────────────────────────────────────────────────
MAX_RETRY=30
RETRY=0
until mysqladmin ping --silent 2>/dev/null; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[MariaDB] ERROR: MariaDB did not start within timeout."
        exit 1
    fi
    echo "[MariaDB] Waiting for MariaDB... (${RETRY}/${MAX_RETRY})"
    sleep 2
done
echo "[MariaDB] MariaDB is ready."

# ── Set the root password + grant access ──────────────────────────────────────
mysql --default-character-set=utf8mb4 -u root <<'SQL'
ALTER USER 'root'@'localhost' IDENTIFIED BY 'testpass123';
DELETE FROM mysql.user WHERE User='';
DELETE FROM mysql.user WHERE User='root' AND Host NOT IN ('localhost','127.0.0.1','::1');
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db='test' OR Db='test\\_%';

-- root from any host, so a direct connection through the published port can log
-- in as root. The flush reloads the grant tables the DELETEs above edited
-- directly; without it CREATE USER still sees the in-memory accounts.
FLUSH PRIVILEGES;
CREATE USER IF NOT EXISTS 'root'@'%' IDENTIFIED BY 'testpass123';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'%' WITH GRANT OPTION;

-- Application account
CREATE USER IF NOT EXISTS 'centipede'@'%' IDENTIFIED BY 'centipede_pass';
GRANT ALL PRIVILEGES ON *.* TO 'centipede'@'%' WITH GRANT OPTION;

-- Read-only account
CREATE USER IF NOT EXISTS 'readonly'@'%' IDENTIFIED BY 'readonly_pass';
GRANT SELECT ON shop_db.* TO 'readonly'@'%';
GRANT SELECT ON hr_db.*   TO 'readonly'@'%';

FLUSH PRIVILEGES;
SQL

echo "[MariaDB] Users created."

# ── Load the test databases ───────────────────────────────────────────────────
echo "[MariaDB] Loading shop_db..."
mysql --default-character-set=utf8mb4 -u root -ptestpass123 < /opt/testenv/sql/shop_db_mariadb.sql
echo "[MariaDB] shop_db loaded."

echo "[MariaDB] Loading hr_db..."
mysql --default-character-set=utf8mb4 -u root -ptestpass123 < /opt/testenv/sql/hr_db_mariadb.sql
echo "[MariaDB] hr_db loaded."

echo "[MariaDB] Setup complete."
