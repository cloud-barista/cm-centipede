#!/usr/bin/env bash
# Target PostgreSQL setup - accounts + empty databases (shop_empty_db, hr_empty_db) only
set -euo pipefail

echo "[DB-Target/PostgreSQL] Starting setup..."

# ── Wait for PostgreSQL ───────────────────────────────────────────────────────
MAX_RETRY=30
RETRY=0
until pg_isready -q 2>/dev/null; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[DB-Target/PostgreSQL] ERROR: PostgreSQL did not start within timeout."
        exit 1
    fi
    echo "[DB-Target/PostgreSQL] Waiting for PostgreSQL... (${RETRY}/${MAX_RETRY})"
    sleep 2
done
echo "[DB-Target/PostgreSQL] PostgreSQL ready."

# ── Set the postgres password + accounts + create empty databases ─────────────
sudo -u postgres psql <<'SQL'
ALTER USER postgres WITH PASSWORD 'testpass123';

-- Migration destination account (may create and write to any database)
CREATE USER centipede WITH PASSWORD 'centipede_pass' SUPERUSER CREATEDB CREATEROLE;

-- Pre-create empty databases on the target (no schema or rows, migration destination)
SELECT 'CREATE DATABASE shop_empty_db OWNER centipede'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'shop_empty_db')\gexec
SELECT 'CREATE DATABASE hr_empty_db OWNER centipede'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'hr_empty_db')\gexec
SQL

echo "[DB-Target/PostgreSQL] Credentials configured; empty databases (shop_empty_db, hr_empty_db) created. Ready to receive migration."
