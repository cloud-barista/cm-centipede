#!/usr/bin/env bash
# common.sh - shared by every init-<engine>.sh, sourced not executed.
#
# Settings reach the container as a FILE, not as environment variables. systemd
# is PID 1 here and does not hand its own environment to the units it starts, so
# anything passed with `docker run -e` would be invisible to matrix-init.service.
# lib/source.sh writes a per-cell env file on the host and bind-mounts it at
# /opt/matrix/matrix.env; the image ships defaults.env so the image also runs on
# its own, without the matrix - except for the passwords, which only matrix.env
# carries.
#
# Precedence: matrix.env (mounted, per cell) > defaults.env (baked into image)

set -euo pipefail

. /opt/matrix/defaults.env
if [ -f /opt/matrix/matrix.env ]; then
	. /opt/matrix/matrix.env
fi

SRC_DB="${SRC_DB:-matrix_db}"
SRC_DB_USER="${SRC_DB_USER:-centipede}"

# No password has a default. Each comes from matrix.env, written by the matrix
# from .env; an empty one would create an account nobody can log in with.
for _v in DB_ROOT_PASS SRC_DB_PASS; do
	if [ -z "${!_v:-}" ]; then
		echo "[init] $_v is empty or not set - mount /opt/matrix/matrix.env with it." >&2
		exit 1
	fi
done

log() { echo "[$ENGINE_LABEL] $*"; }

# wait_for DESCRIPTION COMMAND... - poll until the command succeeds.
#   The unit's TimeoutStartSec is the real upper bound; this loop only decides
#   how the failure is reported, and reporting it as "did not become ready"
#   beats a systemd timeout with no explanation in the log.
wait_for() {
	local what="$1"; shift
	local tries=0 max="${INIT_WAIT_TRIES:-60}"
	until "$@" >/dev/null 2>&1; do
		tries=$((tries + 1))
		if [ "$tries" -ge "$max" ]; then
			log "ERROR: $what did not become ready within $((max * 2))s"
			return 1
		fi
		log "waiting for $what ... ($tries/$max)"
		sleep 2
	done
	log "$what is ready"
}
