#!/usr/bin/env bash
# ──────────────────────────────────────────────────────────────────────────────
# [DOCKERENV UP] Start all 12 containers of the role-split test environment
#
# Steps:
#   1. Generate the shared SSH keypair (ssh_keys/id_rsa, first run only)
#   2. docker compose up -d --build --wait  -> wait until all 12 are healthy (init done)
#   3. Inject the SSH public key into every container (root, centipede)
#      + source containers also get the private key (relay: source -> target)
#   4. Collect the source MinIO bucket info (live object counts)
#   5. Print the connection info for every container
#
# Usage:
#   ./dockerenv-up.sh [--no-cache] [--no-build]
#     --no-cache : build images without the cache
#     --no-build : skip the build (start from existing images)
# ──────────────────────────────────────────────────────────────────────────────
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

SSH_KEY_DIR="${SCRIPT_DIR}/ssh_keys"
PRIV_KEY="${SSH_KEY_DIR}/id_rsa"
PUB_KEY="${SSH_KEY_DIR}/id_rsa.pub"
WAIT_TIMEOUT=900

# Pick the compose CLI (docker compose v2 first)
if docker compose version >/dev/null 2>&1; then
    DC=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
    DC=(docker-compose)
else
    echo "!!! docker compose not found." >&2
    exit 1
fi

# DB versions live in versions.env rather than .env, which the repository root
# .gitignore excludes. Compose only auto-loads a file named .env, so the file has
# to be passed explicitly -- and because docker-compose.yml defaults every
# ${*_VERSION}, a missing --env-file would silently build the default versions
# instead of failing.
DC+=(--env-file versions.env)

BUILD_OPTS=""
DO_BUILD=true
for arg in "$@"; do
    case "$arg" in
        --no-cache) BUILD_OPTS="--no-cache" ;;
        --no-build) DO_BUILD=false ;;
    esac
done

# Container list (name  role  ssh-port)
CONTAINERS=(
    "centipede-testenv-fs-source          source 32210"
    "centipede-testenv-fs-target          target 32211"
    "centipede-testenv-minio-source       source 32220"
    "centipede-testenv-minio-target       target 32221"
    "centipede-testenv-mariadb-source     source 32230"
    "centipede-testenv-mariadb-target     target 32231"
    "centipede-testenv-mysql-source       source 32240"
    "centipede-testenv-mysql-target       target 32241"
    "centipede-testenv-postgresql-source  source 32250"
    "centipede-testenv-postgresql-target  target 32251"
    "centipede-testenv-mongodb-source     source 32260"
    "centipede-testenv-mongodb-target     target 32261"
)

# Source MinIO buckets (bucket  top-level contents), created by scripts/02-setup-minio.sh
SOURCE_BUCKETS=(
    "raw-data       sensors/, sales/, events_stream.json"
    "processed-data reports/, aggregated/"
    "images         products/, banners/"
    "documents      contracts/, invoices/"
    "backups        daily/, full_backup"
    "logs           app/, error/"
)

# ── 1. Generate the SSH keypair ───────────────────────────────────────────────
mkdir -p "$SSH_KEY_DIR"
if [ ! -f "$PRIV_KEY" ]; then
    echo ">>> Generating shared SSH keypair: $PRIV_KEY"
    ssh-keygen -t rsa -b 4096 -f "$PRIV_KEY" -N "" -C "centipede-testenv"
    chmod 600 "$PRIV_KEY"
    chmod 644 "$PUB_KEY"
else
    echo ">>> Reusing SSH keypair: $PRIV_KEY"
fi

# ── 2. Build + start + wait for healthy ───────────────────────────────────────
if [ "$DO_BUILD" = true ]; then
    echo ">>> Building images..."
    "${DC[@]}" build $BUILD_OPTS
fi

echo ">>> Starting containers (up -d --wait, timeout ${WAIT_TIMEOUT}s)..."
if ! "${DC[@]}" up -d --wait --wait-timeout "$WAIT_TIMEOUT"; then
    echo "!!! Some containers never became healthy. Check their status:" >&2
    "${DC[@]}" ps
    echo "!!! Init log, for example: docker exec <container> cat /var/log/testenv-init.log" >&2
    exit 1
fi
echo ">>> All 12 containers are healthy (init complete)."

# ── 3. Inject SSH keys ────────────────────────────────────────────────────────
echo ">>> Injecting SSH keys..."
for row in "${CONTAINERS[@]}"; do
    read -r name role _port <<< "$row"

    # Public key -> root
    docker exec "$name" mkdir -p /root/.ssh
    docker exec "$name" chmod 700 /root/.ssh
    docker cp "$PUB_KEY" "$name":/root/.ssh/authorized_keys
    docker exec "$name" chmod 600 /root/.ssh/authorized_keys
    docker exec "$name" chown root:root /root/.ssh/authorized_keys

    # Public key -> centipede
    docker exec "$name" bash -c "mkdir -p /home/centipede/.ssh && chmod 700 /home/centipede/.ssh && chown centipede:centipede /home/centipede/.ssh"
    docker cp "$PUB_KEY" "$name":/home/centipede/.ssh/authorized_keys
    docker exec "$name" bash -c "chmod 600 /home/centipede/.ssh/authorized_keys && chown centipede:centipede /home/centipede/.ssh/authorized_keys"

    # Source containers also get the private key (relay: source -> target SSH)
    if [ "$role" = "source" ]; then
        docker cp "$PRIV_KEY" "$name":/root/.ssh/id_rsa
        docker exec "$name" chmod 600 /root/.ssh/id_rsa
    fi
done
echo ">>> SSH keys injected (pubkey → all, privkey → sources)."

# ── 4. Collect source MinIO bucket info ───────────────────────────────────────
# Counted live rather than hard-coded so a partially failed upload in
# 02-setup-minio.sh is visible right after start-up. The "local" mc alias was
# registered as root inside the container during init, so it is reused here; if
# the lookup fails the count falls back to "?" instead of aborting the script.
echo ">>> Reading source MinIO buckets..."
BUCKET_LINES=""
BUCKET_LABEL="    source buckets   "
for row in "${SOURCE_BUCKETS[@]}"; do
    read -r bucket contents <<< "$row"

    count=$(docker exec centipede-testenv-minio-source \
                mc ls --recursive "local/${bucket}" 2>/dev/null | wc -l | tr -d ' ') || count=""
    [ -z "$count" ] && count="?"

    BUCKET_LINES+="$(printf '%s%-15s: %3s objects  (%s)' "$BUCKET_LABEL" "$bucket" "$count" "$contents")"$'\n'
    BUCKET_LABEL="                     "
done
BUCKET_LINES="${BUCKET_LINES%$'\n'}"

# ── 5. Print connection info ──────────────────────────────────────────────────
cat <<EOF

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  cm-centipede TESTENV — 12 containers ready
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Shared credentials
    SSH   : root / testpass123
            key: ${PRIV_KEY}
    MinIO : minioadmin / minioadmin123
    DB (shared) : centipede / centipede_pass
    DB (admin)  : PostgreSQL -> postgres / testpass123
                  others (MariaDB/MySQL/MongoDB) -> root / testpass123

  ┌─ Filesystem ────────────────────────────────────────────────────────────
    fs-source        SSH  localhost:32210   (/testdata fully populated)
    fs-target        SSH  localhost:32211   (/testdata empty)

  ┌─ Object Storage (MinIO) ────────────────────────────────────────────────
    minio-source     SSH  localhost:32220   API 9000->39000  console http://localhost:39001
    minio-target     SSH  localhost:32221   API 9000->39010  console http://localhost:39011

${BUCKET_LINES}
    target buckets   same 6 buckets, all empty

  ┌─ MariaDB ───────────────────────────────────────────────────────────────
    mariadb-source   SSH  localhost:32230   DB  localhost:33306   (shop_db, hr_db)
    mariadb-target   SSH  localhost:32231   DB  localhost:33307   (empty shop_empty_db, hr_empty_db)

  ┌─ MySQL ─────────────────────────────────────────────────────────────────
    mysql-source     SSH  localhost:32240   DB  localhost:33406   (shop_db, hr_db)
    mysql-target     SSH  localhost:32241   DB  localhost:33407   (empty shop_empty_db, hr_empty_db)

  ┌─ PostgreSQL ────────────────────────────────────────────────────────────
    postgresql-source SSH localhost:32250   DB  localhost:35432   (shop_db, hr_db)
    postgresql-target SSH localhost:32251   DB  localhost:35433   (empty shop_empty_db, hr_empty_db)

  ┌─ MongoDB ───────────────────────────────────────────────────────────────
    mongodb-source   SSH  localhost:32260   DB  localhost:37017   (shop_db, hr_db)
    mongodb-target   SSH  localhost:32261   DB  localhost:37018   (empty shop_empty_db, hr_empty_db)

  Shut down: ./dockerenv-down.sh

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EOF
