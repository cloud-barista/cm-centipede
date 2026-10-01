#!/usr/bin/env bash
# Target filesystem setup - prepare only an empty /testdata (migration destination)
set -euo pipefail

BASE="/testdata"
echo "[FS-Target] Preparing empty ${BASE}..."
mkdir -p "$BASE"
# Open it up so the migration (centipede/root) can write files
chmod 777 "$BASE"
echo "[FS-Target] Empty ${BASE} ready to receive migration."
