#!/usr/bin/env bash
# Source MongoDB setup - accounts + load shop_db and hr_db from sql/ (mongosh)
set -euo pipefail

echo "[MongoDB] Starting setup..."

# ── Wait for MongoDB ──────────────────────────────────────────────────────────
MAX_RETRY=30
RETRY=0
until mongosh --quiet --eval "db.adminCommand('ping').ok" 2>/dev/null | grep -q "1"; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge $MAX_RETRY ]]; then
        echo "[MongoDB] ERROR: MongoDB did not start within timeout."
        exit 1
    fi
    echo "[MongoDB] Waiting for MongoDB... (${RETRY}/${MAX_RETRY})"
    sleep 2
done
echo "[MongoDB] MongoDB is ready."

# ── Create accounts (the first one needs no auth, via the localhost exception) ───
mongosh admin --quiet --eval '
db.createUser({
    user: "root",
    pwd: "testpass123",
    roles: [ { role: "root", db: "admin" } ]
});
'

# Application + read-only accounts (created while authenticated as root)
mongosh admin -u root -p testpass123 --authenticationDatabase admin --quiet --eval '
db.createUser({
    user: "centipede",
    pwd: "centipede_pass",
    roles: [ { role: "root", db: "admin" } ]
});
db.createUser({
    user: "readonly",
    pwd: "readonly_pass",
    roles: [
        { role: "read", db: "shop_db" },
        { role: "read", db: "hr_db" }
    ]
});

// The connection URI cm-centipede builds is mongodb://.../<db>?authSource=..., and
// authSource defaults to "admin" — so it is the centipede account above, in admin,
// that authenticates whichever database the path names. The same account is created
// inside shop_db and hr_db as well, to cover a connection that points db.authSource
// at the path database instead.
db.getSiblingDB("shop_db").createUser({
    user: "centipede",
    pwd: "centipede_pass",
    roles: [ { role: "readWrite", db: "shop_db" } ]
});
db.getSiblingDB("hr_db").createUser({
    user: "centipede",
    pwd: "centipede_pass",
    roles: [ { role: "readWrite", db: "hr_db" } ]
});
'
echo "[MongoDB] Users created."

# ── Initialize the test databases ─────────────────────────────────────────────
echo "[MongoDB] Loading shop_db..."
mongosh -u root -p testpass123 --authenticationDatabase admin < /opt/testenv/sql/shop_db_mongo.js
echo "[MongoDB] shop_db loaded."

echo "[MongoDB] Loading hr_db..."
mongosh -u root -p testpass123 --authenticationDatabase admin < /opt/testenv/sql/hr_db_mongo.js
echo "[MongoDB] hr_db loaded."

echo "[MongoDB] Setup complete."
