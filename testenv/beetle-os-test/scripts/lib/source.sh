#!/usr/bin/env bash
#
# lib/source.sh — the whole source side: image build, container lifetime, addresses.
#
# ── One container for the whole run ─────────────────────────────────────────
# The managed-DB matrix rebuilds its source per cell, because the row axis is the
# source engine version and each cell needs a different server. Here the row axis
# is the bucket, and one MinIO holds all six — so the image is built once, the
# container starts once, and every cell reads from it. That is the single biggest
# difference in cost between the two folders.
#
# ── Why a systemd image ─────────────────────────────────────────────────────
# An official Docker image runs one process, but the source here plays the part
# of an on-premises machine: `systemctl` works, sshd is a unit, and MinIO is a
# service that starts at boot. src/Dockerfile.minio puts systemd into
# ubuntu:22.04, which costs three things — the first build is slow, the container
# runs --privileged, and six things have to be right for systemd to be PID 1.
#
# ── The readiness signal ────────────────────────────────────────────────────
# matrix-init.service is Type=oneshot with RemainAfterExit=yes, so the unit stays
# active once init-minio.sh has returned. That makes one `systemctl is-active`
# mean "MinIO is up, the buckets exist and every seed object is uploaded".
#
# ── Access is always direct ─────────────────────────────────────────────────
# There is no ssh mode here. honeybee's object storage import reaches an
# ssh-tunnel connection only through an agent installed on the source host, and
# transx-ex has no tunnelled path for storage at all — MinioConnConfig says so in
# as many words. sshd is in the image for interactive debugging, nothing else.

if [ -n "${MATRIX_SOURCE_SH:-}" ]; then return 0; fi
MATRIX_SOURCE_SH=1

SOURCE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$SOURCE_LIB_DIR/common.sh"

SOURCE_ROOT="$(cd "$SOURCE_LIB_DIR/../.." && pwd)"
SRC_BUILD_DIR="$SOURCE_ROOT/src"

# The MinIO server's own port inside the container, and the console's.
SRC_INTERNAL_API_PORT=9000
SRC_INTERNAL_CONSOLE_PORT=9001

# What minio.service bakes into the unit. systemd reads a unit's Environment= at
# load time, so the running server uses these whatever .env says — see
# assert_minio_account.
MINIO_UNIT_USER="minioadmin"
MINIO_UNIT_PASS="minioadmin123"

# Names stay clear of every other environment in this repo, because several being
# up at once is the normal case: dockerenv (centipede-testenv-*) serves the
# docker-to-docker scenario, beetle-db-test serves managed databases, and this one
# serves managed buckets.
src_container()  { printf 'beetle-os-test-minio-src'; }
src_image_repo() { printf 'beetle-os-test-src-minio'; }

# src_endpoint — how honeybee reaches the source. The scheme is stated because
#   honeybee treats one typed into os_endpoint as an explicit statement about TLS
#   and lets it win over os_use_ssl (resolveS3Endpoint, objectStorageEndpoint.go).
src_endpoint() { printf 'http://%s:%s' "${HOST_IP:-127.0.0.1}" "${MINIO_SRC_API_PORT:-33900}"; }

# assert_minio_account — refuse a root account the image cannot actually use.
#
#   minio.service carries MINIO_ROOT_USER/PASSWORD as Environment=, so changing
#   them in .env alone leaves the server running on the old pair while honeybee
#   is handed the new one. That fails as a 403 during collection, several steps
#   later, with nothing pointing at the cause. Refuse it here instead.
assert_minio_account() {
	local u="${MINIO_ROOT_USER:-$MINIO_UNIT_USER}" p="${MINIO_ROOT_PASSWORD:-$MINIO_UNIT_PASS}"
	if [ "$u" = "$MINIO_UNIT_USER" ] && [ "$p" = "$MINIO_UNIT_PASS" ]; then
		return 0
	fi
	fail "MINIO_ROOT_USER / MINIO_ROOT_PASSWORD do not match what the image starts MinIO with."
	fail "  .env asks for      : $u / $(printf '%*s' "${#p}" '' | tr ' ' '*')"
	fail "  the image runs with: $MINIO_UNIT_USER / ********"
	fail "  systemd reads a unit's Environment= when the unit is loaded, so the server would"
	fail "  keep the baked-in pair and honeybee would be handed the other one — a 403 several"
	fail "  steps later with nothing pointing here."
	fail "  To change them, edit src/services/minio.service and src/Dockerfile.minio's"
	fail "  defaults.env together, then delete the cached image:"
	fail "    docker image rm $(src_image_repo):latest"
	return 1
}

# ---------------------------------------------------------------------------
# Image
# ---------------------------------------------------------------------------

# src_image -> the image name to use, on stdout. Built once and cached by tag.
src_image() {
	local img dockerfile
	img="$(src_image_repo):${MINIO_VERSION:-latest}"
	dockerfile="$SRC_BUILD_DIR/Dockerfile.minio"

	if [ ! -f "$dockerfile" ]; then
		fail "no source Dockerfile: $dockerfile"
		return 1
	fi
	if docker image inspect "$img" >/dev/null 2>&1; then
		printf '%s' "$img"
		return 0
	fi

	info "building the source image: $img  (once, takes minutes)" >&2
	if ! timeout "${SRC_BUILD_TIMEOUT:-900}" \
		docker build -f "$dockerfile" \
			--build-arg "MINIO_VERSION=${MINIO_VERSION:-}" \
			-t "$img" "$SRC_BUILD_DIR" >/dev/null 2>&1; then
		fail "source image build failed: $img"
		fail "  To see the build log yourself:"
		fail "    docker build -f $dockerfile --build-arg MINIO_VERSION='${MINIO_VERSION:-}' -t $img $SRC_BUILD_DIR"
		return 1
	fi
	printf '%s' "$img"
}

# ---------------------------------------------------------------------------
# Container
# ---------------------------------------------------------------------------

# _src_env_file -> path to a file holding this run's settings, on stdout.
#
#   Settings go in as a file rather than as environment variables because PID 1
#   here is systemd, and systemd does not hand its own environment to the units
#   it starts. Anything passed with `docker run -e` is invisible to
#   matrix-init.service.
_src_env_file() {
	local f
	f="${MATRIX_TMP:-/tmp}/src-minio.env"
	# Quoted, because init-minio.sh sources this file: an unquoted OS_SRC_BUCKETS
	# would have the shell try to run "processed-data" as a command, and
	# matrix-init.service would report failed with that as its only clue.
	{
		printf 'MINIO_ROOT_USER="%s"\n'     "${MINIO_ROOT_USER:-$MINIO_UNIT_USER}"
		printf 'MINIO_ROOT_PASSWORD="%s"\n' "${MINIO_ROOT_PASSWORD:-$MINIO_UNIT_PASS}"
		printf 'OS_SRC_BUCKETS="%s"\n'      "${OS_SRC_BUCKETS:-}"
	} > "$f"
	chmod 600 "$f" 2>/dev/null || true
	printf '%s' "$f"
}

# src_start IMAGE — start the container.
#
#   Running systemd as PID 1 needs six things. Miss one and the boot never
#   settles, or the units cannot write their runtime state:
#     --privileged              cgroup manipulation
#     --cgroupns=host           compose's `cgroup: host`
#     /sys/fs/cgroup rw         systemd reads and writes it
#     --tmpfs /run              the units' runtime directory
#     --tmpfs /tmp:exec         an executable scratch space
#     --stop-signal SIGRTMIN+3  systemd's graceful shutdown signal
src_start() {
	local img="$1" name env_file
	name="$(src_container)"
	env_file="$(_src_env_file)"
	docker rm -f "$name" >/dev/null 2>&1

	local args=(
		-d --name "$name" --hostname minio-src
		--privileged --cgroupns=host
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw
		--tmpfs /run --tmpfs /tmp:exec
		--stop-signal SIGRTMIN+3
		-v "$env_file:/opt/matrix/matrix.env:ro"
		-p "${MINIO_SRC_API_PORT:-33900}:$SRC_INTERNAL_API_PORT"
		-p "${MINIO_SRC_CONSOLE_PORT:-33901}:$SRC_INTERNAL_CONSOLE_PORT"
	)
	[ -n "${MINIO_SRC_SSH_PORT:-}" ] && args+=(-p "${MINIO_SRC_SSH_PORT}:22")

	if ! docker run "${args[@]}" "$img" >/dev/null 2>&1; then
		fail "source container failed to start: $name ($img)"
		fail "  A systemd container needs --privileged and a /sys/fs/cgroup mount."
		fail "  A published port may also be taken — this folder uses the 33xxx band"
		fail "  (API ${MINIO_SRC_API_PORT:-33900}, console ${MINIO_SRC_CONSOLE_PORT:-33901}, SSH ${MINIO_SRC_SSH_PORT:-33922})."
		return 1
	fi
	info "source container started: $name <- $img"
	info "  S3 API $(src_endpoint)   console http://${HOST_IP:-127.0.0.1}:${MINIO_SRC_CONSOLE_PORT:-33901}"
}

# assert_env_mounted — the settings file has to be a file inside the container.
#
#   docker creates a bind mount whose source path does not exist as an empty
#   DIRECTORY, and says nothing. The source path here is on the docker host, so
#   this happens whenever the matrix runs inside a container against a shared
#   daemon - and the seed then falls back to the image's defaults, quietly
#   building six buckets when three were asked for.
#
#   Caught here the run stops with the reason. The retry is for the seconds
#   between `docker run` returning and the container accepting an exec.
assert_env_mounted() {
	local name waited=0 out
	name="$(src_container)"
	while [ "$waited" -lt 30 ]; do
		out="$(docker exec "$name" sh -c '[ -f /opt/matrix/matrix.env ] && echo file || { [ -d /opt/matrix/matrix.env ] && echo dir || echo none; }' 2>/dev/null)"
		case "$out" in
		file) return 0 ;;
		dir)
			fail "the settings file did not mount: /opt/matrix/matrix.env is a directory inside $name."
			fail "  docker creates a bind mount whose source does not exist as an empty directory."
			fail "  The source path is on the DOCKER HOST:"
			fail "    $(_src_env_file)"
			fail "  If this matrix runs inside a container against a shared docker daemon, that"
			fail "  path exists only in this container and the host has nothing there. Run the"
			fail "  matrix on the docker host, or point TMPDIR at a directory both can see."
			fail "  Left alone the seed falls back to the image defaults and builds all six"
			fail "  buckets whatever OS_SRC_BUCKETS says."
			return 1 ;;
		esac
		sleep 2
		waited=$((waited + 2))
	done
	fail "could not check the settings file inside $name (no exec response in 30s)."
	return 1
}

# src_wait_ready — until the seed has finished and the published port is open.
#
#   Two things are checked. matrix-init.service being active means the work
#   inside the container is done; the published port has to be open for honeybee
#   to reach it from outside. A unit that ended up failed is not worth waiting
#   on, so the log is printed and the wait stops there.
src_wait_ready() {
	local name port waited=0 state
	name="$(src_container)"
	port="${MINIO_SRC_API_PORT:-33900}"

	assert_env_mounted || return 1

	# The wait is reported as it goes. Seeding takes tens of seconds and the port
	# check can fail for a whole READY_TIMEOUT, and a run that prints nothing for
	# five minutes reads as hung rather than as waiting - which is the wrong thing
	# to believe, because what it is usually waiting on is a HOST_IP that cannot
	# reach the published port.
	local phase
	while [ "$waited" -lt "${READY_TIMEOUT:-300}" ]; do
		state="$(docker exec "$name" systemctl is-active matrix-init.service 2>/dev/null || true)"
		case "$state" in
		active)
			if (echo > "/dev/tcp/${HOST_IP:-127.0.0.1}/$port") >/dev/null 2>&1; then
				printf '\r%-110s\r' "" >&2
				ok "source ready: $name  (matrix-init active, ${HOST_IP:-127.0.0.1}:$port open)"
				return 0
			fi
			phase="seed done — waiting for ${HOST_IP:-127.0.0.1}:$port to answer" ;;
		failed)
			printf '\r%-110s\r' "" >&2
			fail "source initialization failed: $name"
			docker exec "$name" cat /var/log/matrix-init.log 2>/dev/null | tail -30 | sed 's/^/      /'
			return 1 ;;
		*)
			phase="booting and seeding (matrix-init ${state:-starting})" ;;
		esac
		printf '\r    %-100s' "source — $phase ($(secs_fmt "$waited"))" >&2
		sleep 3
		waited=$((waited + 3))
	done
	printf '\r%-110s\r' "" >&2

	fail "timed out waiting for the source (${READY_TIMEOUT:-300}s): $name  (matrix-init=${state:-none})"
	if [ "$state" = "active" ]; then
		# The seed finished; only the published port never answered. That is almost
		# always HOST_IP, so say it here rather than leaving the log below to be read
		# for an error that is not in it.
		fail "  The seed finished — what did not answer is ${HOST_IP:-127.0.0.1}:$port from this machine."
		fail "  HOST_IP has to be an address that reaches the container's published port."
		fail "  If this matrix (or cm-honeybee) runs inside a container, 127.0.0.1 is that"
		fail "  container itself; use the docker bridge gateway instead:"
		fail "    HOST_IP=\$(ip route | awk '/default/{print \$3}')   # usually 172.17.0.1"
		fail "  The container's own address works too, but with the internal port:"
		fail "    $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name" 2>/dev/null):$SRC_INTERNAL_API_PORT"
	fi
	docker exec "$name" systemctl --failed --no-legend 2>/dev/null | sed 's/^/      /' || true
	docker exec "$name" cat /var/log/matrix-init.log 2>/dev/null | tail -30 | sed 's/^/      /' || true
	return 1
}

src_stop() { docker rm -f "$(src_container)" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Reading the source back — through the container's own mc
# ---------------------------------------------------------------------------
# The host running this matrix needs no S3 client. mc is already in the image and
# already has an alias, because the seed script uploaded through it.

_src_mc() {
	docker exec "$(src_container)" mc "$@" 2>/dev/null
}

# src_buckets — the bucket names MinIO actually holds, one per line.
#   Read rather than assumed: OS_SRC_BUCKETS says what was asked for, and this
#   says what is there. assert_src_buckets compares the two.
src_buckets() {
	_src_mc ls --json local | jq -r 'select(.type=="folder") | .key | rtrimstr("/")' 2>/dev/null
}

# src_bucket_stats BUCKET — "OBJECTCOUNT BYTES" on stdout, "0 0" when unreadable.
#   Only used to describe the source to beetle's recommendation, and to print
#   something a person can compare with the validation result.
src_bucket_stats() {
	local out
	out="$(_src_mc ls --recursive --json "local/$1" \
		| jq -s '[ .[] | select(.type=="file") ] | "\(length) \([.[].size] | add // 0)"' -r 2>/dev/null)"
	[ -n "$out" ] || out="0 0"
	printf '%s' "$out"
}

# assert_src_buckets "B1 B2 …" — every bucket asked for has to exist.
#
#   0  every bucket is there
#   1  some are missing; their names are in SRC_MISSING_BUCKETS. The caller marks
#      those rows SKIP rather than stopping — one absent bucket should not cost
#      the other rows.
#   2  the source holds no buckets at all. Not the same thing: every cell would
#      fail identically, so the caller stops instead.
SRC_MISSING_BUCKETS=""
assert_src_buckets() {
	local wanted="$1" present bucket
	SRC_MISSING_BUCKETS=""
	present="$(src_buckets | tr '\n' ' ')"
	if [ -z "$present" ]; then
		fail "the source container holds no buckets at all."
		fail "  matrix-init.service reported success, so this is unexpected. Look at the log:"
		fail "    docker exec $(src_container) cat /var/log/matrix-init.log"
		return 2
	fi
	for bucket in $wanted; do
		in_list "$bucket" "$present" && continue
		SRC_MISSING_BUCKETS="${SRC_MISSING_BUCKETS:+$SRC_MISSING_BUCKETS }$bucket"
	done
	info "source buckets: $(printf '%s' "$present" | sed 's/ $//')"
	if [ -n "$SRC_MISSING_BUCKETS" ]; then
		warn "asked for but not in the source: $SRC_MISSING_BUCKETS"
		warn "  Those rows are SKIPped. init-minio.sh seeds only the names it knows;"
		warn "  a new one needs a seed_<name> function there."
		return 1
	fi
	return 0
}
