#!/usr/bin/env bash
#
# init-fs.sh — everything the source container has to do before it is usable.
#
# Run once at boot by matrix-init.service, which is Type=oneshot with
# RemainAfterExit=yes. That makes `systemctl is-active matrix-init.service` mean
# "the key is installed and the dataset is complete" — one question instead of
# two, and it is the only readiness signal lib/source.sh waits on.
#
# ── Why the authorized_keys is copied rather than mounted into place ────────
# A bind mount keeps the host's ownership and mode, and sshd's StrictModes
# refuses a key file it considers unsafe — silently, as an auth failure several
# steps later. Copying it here puts a root-owned 0600 file in /root/.ssh whatever
# the host side looks like.
#
# ⚠ Key authentication is not a preference here, it is a requirement. cm-honeybee
#   refuses a password on an fs connection (HTTP 400), and cm-centipede
#   authenticates SSH by key only (pkg/core/migration/filesystem.go
#   ResolveSSHConfig fills PrivateKey and never a password), so a
#   password-authenticated source cannot even be registered.

set -euo pipefail

LOG=/var/log/matrix-init.log
exec > >(tee -a "$LOG") 2>&1

echo "=== matrix-init $(date '+%F %T') ==="

MATRIX_ENV=/opt/matrix/matrix.env
AUTHORIZED_KEYS_SRC=/opt/matrix/authorized_keys

# ── Settings ────────────────────────────────────────────────────────────────
# systemd does not hand its own environment to the units it starts, so nothing
# passed with `docker run -e` would be visible here. The matrix writes a file and
# bind-mounts it instead; the image ships defaults.env so it also runs on its own.
if [ -f "$MATRIX_ENV" ]; then
	echo "[init] reading $MATRIX_ENV"
	# shellcheck disable=SC1090
	. "$MATRIX_ENV"
else
	echo "[init] $MATRIX_ENV is not there — falling back to /opt/matrix/defaults.env"
	# shellcheck disable=SC1091
	. /opt/matrix/defaults.env
fi

FS_SRC_PATH="${FS_SRC_PATH:-/testdata}"
export FS_SRC_PATH

# ── SSH key ─────────────────────────────────────────────────────────────────
if [ ! -f "$AUTHORIZED_KEYS_SRC" ]; then
	echo "[init] ERROR: $AUTHORIZED_KEYS_SRC is not a file."
	echo "[init]   The matrix generates a key pair per run and bind-mounts the public"
	echo "[init]   half here. Without it nothing can log in, and a filesystem migration"
	echo "[init]   is three SSH logins: honeybee installs its agent and inspects,"
	echo "[init]   centipede rsyncs, centipede checksums for the validation."
	exit 1
fi

install -d -m 700 -o root -g root /root/.ssh
install -m 600 -o root -g root "$AUTHORIZED_KEYS_SRC" /root/.ssh/authorized_keys
echo "[init] installed /root/.ssh/authorized_keys ($(wc -l < /root/.ssh/authorized_keys) key(s))"

# ── The dataset ─────────────────────────────────────────────────────────────
# Rebuilt from scratch every boot. The container is thrown away with the run, so
# there is nothing to preserve, and a half-written tree left by an interrupted
# boot would be reported as a source the matrix then migrates.
rm -rf "${FS_SRC_PATH:?}"
/opt/matrix/seed-fs.sh

echo "[init] $(find "$FS_SRC_PATH" -type f | wc -l) file(s), $(du -sb "$FS_SRC_PATH" | cut -f1) bytes under $FS_SRC_PATH"
echo "[init] done"
