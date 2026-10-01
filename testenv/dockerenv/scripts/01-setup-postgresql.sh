#!/usr/bin/env bash
# Source PostgreSQL setup - accounts + load shop_db and hr_db from sql/
set -euo pipefail

echo "[PostgreSQL] Starting setup..."

# ── Wait for PostgreSQL ───────────────────────────────────────────────────────
MAX_RETRY=30
RETRY=0
until pg_isready -q 2>/dev/null; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[PostgreSQL] ERROR: PostgreSQL did not start within timeout."
        exit 1
    fi
    echo "[PostgreSQL] Waiting for PostgreSQL... (${RETRY}/${MAX_RETRY})"
    sleep 2
done
echo "[PostgreSQL] PostgreSQL is ready."

# ── Set the postgres password + create accounts ───────────────────────────────
sudo -u postgres psql <<'SQL'
ALTER USER postgres WITH PASSWORD 'testpass123';

CREATE USER centipede WITH PASSWORD 'centipede_pass' SUPERUSER CREATEDB CREATEROLE;

CREATE USER readonly WITH PASSWORD 'readonly_pass';
SQL

echo "[PostgreSQL] Users created."

# ── Load the test databases ───────────────────────────────────────────────────
echo "[PostgreSQL] Loading shop_db..."
sudo -u postgres psql -f /opt/testenv/sql/shop_db_pg.sql
echo "[PostgreSQL] shop_db loaded."

echo "[PostgreSQL] Loading hr_db..."
sudo -u postgres psql -f /opt/testenv/sql/hr_db_pg.sql
echo "[PostgreSQL] hr_db loaded."

# Grant readonly privileges (after the databases exist)
sudo -u postgres psql <<'SQL'
GRANT CONNECT ON DATABASE shop_db TO readonly;
GRANT CONNECT ON DATABASE hr_db   TO readonly;
\c shop_db
GRANT USAGE ON SCHEMA public TO readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO readonly;
\c hr_db
GRANT USAGE ON SCHEMA public TO readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO readonly;
SQL

echo "[PostgreSQL] Setup complete."
