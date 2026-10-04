#!/usr/bin/env bash
# common.sh - sourced first by every role's init.sh, never run on its own.
#
# DOCKERENV_PASSWORD is the one password every account here uses: OS root and
# centipede (SSH), the DB admin, centipede and readonly accounts, and the MinIO
# root. It comes from .env through docker-compose.yml's environment and the
# unit's PassEnvironment=, so it is set when the container starts and never baked
# into an image. dockerenv-up.sh validates it before building; this is the same
# refusal for a container started some other way.

if [ -z "${DOCKERENV_PASSWORD:-}" ]; then
    echo "[init] ERROR: DOCKERENV_PASSWORD is empty or not set - set it in .env." >&2
    exit 1
fi
if [ "$(printf '%s' "$DOCKERENV_PASSWORD" | tr '[:upper:]' '[:lower:]')" = "changeme" ]; then
    echo "[init] ERROR: DOCKERENV_PASSWORD is still ChangeMe - change it in .env." >&2
    exit 1
fi

# The OS accounts used to get these passwords at build time; now at start.
printf 'root:%s\ncentipede:%s\n' "$DOCKERENV_PASSWORD" "$DOCKERENV_PASSWORD" | chpasswd
