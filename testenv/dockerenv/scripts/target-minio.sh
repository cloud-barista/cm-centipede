#!/usr/bin/env bash
# Target MinIO setup - create only 6 empty buckets (no objects, migration destination)
set -euo pipefail

echo "[MinIO-Target] Starting setup..."

MINIO_ENDPOINT="http://localhost:9000"
ALIAS="local"

# ── Wait for MinIO ────────────────────────────────────────────────────────────
RETRY=0
until curl -sf "${MINIO_ENDPOINT}/minio/health/live" >/dev/null 2>&1; do
    RETRY=$((RETRY + 1))
    if [[ $RETRY -ge 40 ]]; then
        echo "[MinIO-Target] ERROR: MinIO did not start within timeout."
        exit 1
    fi
    echo "[MinIO-Target] Waiting for MinIO... (${RETRY}/40)"
    sleep 3
done
echo "[MinIO-Target] MinIO is ready."

mc alias set "$ALIAS" "$MINIO_ENDPOINT" minioadmin minioadmin123 >/dev/null

for bucket in raw-data processed-data images documents backups logs; do
    mc mb "${ALIAS}/${bucket}" 2>/dev/null \
        && echo "[MinIO-Target] Bucket created: $bucket" \
        || echo "[MinIO-Target] Bucket already exists: $bucket"
done

echo "[MinIO-Target] 6 empty buckets ready (no objects). Ready to receive migration."
