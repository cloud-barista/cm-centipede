#!/usr/bin/env bash
#
# lib/source.sh — the whole source side: the key pair, the image, the container,
#                 and the addresses the other services reach it on.
#
# ── One container for the whole run ─────────────────────────────────────────
# Every CSP column reads the same source. The image is built once, the container
# starts once, the dataset is inspected once, and every cell migrates that one
# inspection to its own target node. That is the same shape beetle-os-test uses,
# and for the same reason: the thing that varies per cell is the target.
#
# ── Why a systemd image ─────────────────────────────────────────────────────
# cm-honeybee does not inspect a filesystem from the outside. It installs an
# agent on the source host over SSH and asks the agent, which means the source
# has to be a machine: systemctl has to work, sshd has to be a unit, and a
# service the agent's installer enables has to actually start. src/Dockerfile.fs
# puts systemd into ubuntu:22.04, which costs three things — a slow first build,
# a --privileged container, and six things that have to be right for systemd to
# be PID 1.
#
# ── The readiness signal ────────────────────────────────────────────────────
# matrix-init.service is Type=oneshot with RemainAfterExit=yes, so the unit stays
# active once init-fs.sh has returned. One `systemctl is-active` therefore means
# "the SSH key is installed and the dataset is complete".
#
# ── The key pair is made per run, not baked ─────────────────────────────────
# Three consumers share one key: cm-honeybee logs in to install its agent and to
# run each inspect, cm-centipede rsyncs the data out, and cm-centipede logs in
# again to checksum both ends for the validation. A key in the image would be a
# published private key; one made per run lives in the run's temporary directory
# and dies with it.

if [ -n "${MATRIX_SOURCE_SH:-}" ]; then return 0; fi
MATRIX_SOURCE_SH=1

SOURCE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$SOURCE_LIB_DIR/common.sh"

SOURCE_ROOT="$(cd "$SOURCE_LIB_DIR/../.." && pwd)"
SRC_BUILD_DIR="$SOURCE_ROOT/src"

# sshd's own port inside the container.
SRC_INTERNAL_SSH_PORT=22

# The user honeybee and centipede log in as. root, because cm-honeybee's agent
# installer (copyAgent.sh) exits immediately when EUID is not 0 and is invoked as
# `sudo /tmp/copyAgent.sh` — root is the one account for which that needs nothing
# set up. The image permits root by key only.
SRC_SSH_USER="root"

# Filled in by src_keygen.
SRC_KEY_FILE=""
SRC_PUBKEY_FILE=""

# Names stay clear of every other environment in this repo, because several being
# up at once is the normal case: dockerenv (centipede-testenv-*) serves the
# docker-to-docker scenario, beetle-os-test serves managed buckets, beetle-db-test
# managed databases, and this one migrated nodes.
src_container()  { printf 'beetle-fs-test-fs-src'; }
src_image_repo() { printf 'beetle-fs-test-src-fs'; }

src_ssh_port() { printf '%s' "${FS_SRC_SSH_PORT:-34922}"; }
src_path()     { printf '%s' "${FS_SRC_PATH:-/testdata}"; }

# ---------------------------------------------------------------------------
# Where the other services reach this container
# ---------------------------------------------------------------------------

# detect_host_ip — the address cm-honeybee and cm-centipede reach the source's
#   published port on, printed on stdout.
#
#   Not 127.0.0.1. Both services normally run as containers of the deployments
#   stack, and there 127.0.0.1 is the service itself — a loopback probe that
#   succeeds locally and then fails for the only two callers that matter.
#
#   The docker bridge gateway is the one address that works for all three cases.
#   A published port binds 0.0.0.0 on the host, so a container on any bridge
#   network reaches it through the gateway, and a service running as a host
#   process reaches the same address because it is a host interface.
#
#   HOST_IP in .env overrides this and is meant as an escape hatch, not a setting
#   to fill in: a host with an unusual docker setup, or a stack reached across
#   machines, which is outside what this folder assumes.
detect_host_ip() {
	local gw
	gw="$(docker network inspect bridge -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' 2>/dev/null | head -1)"
	if [ -n "$gw" ]; then printf '%s' "$gw"; return 0; fi
	# docker0 by name, for a daemon whose default bridge is not called "bridge".
	gw="$(ip -4 addr show docker0 2>/dev/null | awk '/inet /{sub(/\/.*/,"",$2); print $2; exit}')"
	if [ -n "$gw" ]; then printf '%s' "$gw"; return 0; fi
	printf '127.0.0.1'
	return 1
}

src_host() { printf '%s' "${HOST_IP:-127.0.0.1}"; }

# src_endpoint — "host:port", how honeybee and centipede address this container.
src_endpoint() { printf '%s:%s' "$(src_host)" "$(src_ssh_port)"; }

# ---------------------------------------------------------------------------
# The key pair
# ---------------------------------------------------------------------------

# src_keygen — make this run's key pair in the run's temporary directory.
#
#   ed25519 by default: every consumer parses it (cm-honeybee and cm-centipede
#   both use golang.org/x/crypto/ssh, and the rsync relay hands the file to a
#   real OpenSSH client), and it is short enough to read in a log line. FS_KEY_TYPE
#   switches it to rsa for a stack that needs one.
src_keygen() {
	local dir type
	dir="${MATRIX_TMP:-/tmp}"
	type="${FS_KEY_TYPE:-ed25519}"
	SRC_KEY_FILE="$dir/src-id_${type}"
	SRC_PUBKEY_FILE="$SRC_KEY_FILE.pub"

	rm -f "$SRC_KEY_FILE" "$SRC_PUBKEY_FILE"
	local -a args=(-t "$type" -N '' -C "beetle-fs-test-$(date +%Y%m%d-%H%M%S)" -f "$SRC_KEY_FILE")
	[ "$type" = "rsa" ] && args+=(-b 4096)
	if ! ssh-keygen "${args[@]}" >/dev/null 2>&1; then
		fail "could not generate the source SSH key pair ($type) in $dir"
		fail "  ssh-keygen is required. FS_KEY_TYPE=rsa if ed25519 is unavailable."
		return 1
	fi
	chmod 600 "$SRC_KEY_FILE"
	info "source key pair: $type  ($SRC_KEY_FILE)"
}

# src_private_key — the private key's text, for honeybee's connection_info body.
src_private_key() { cat "$SRC_KEY_FILE"; }

# ---------------------------------------------------------------------------
# Image
# ---------------------------------------------------------------------------

# src_image -> the image name to use, on stdout. Built once and cached by tag.
src_image() {
	local img dockerfile
	img="$(src_image_repo):latest"
	dockerfile="$SRC_BUILD_DIR/Dockerfile.fs"

	if [ ! -f "$dockerfile" ]; then
		fail "no source Dockerfile: $dockerfile"
		return 1
	fi
	if [ "${FS_REBUILD_SOURCE:-0}" != "1" ] && docker image inspect "$img" >/dev/null 2>&1; then
		printf '%s' "$img"
		return 0
	fi

	info "building the source image: $img  (once, takes minutes)" >&2
	if ! timeout "${SRC_BUILD_TIMEOUT:-900}" \
		docker build -f "$dockerfile" -t "$img" "$SRC_BUILD_DIR" >/dev/null 2>&1; then
		fail "source image build failed: $img"
		fail "  To see the build log yourself:"
		fail "    docker build -f $dockerfile -t $img $SRC_BUILD_DIR"
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
	f="${MATRIX_TMP:-/tmp}/src-fs.env"
	# Quoted, because init-fs.sh sources this file.
	printf 'FS_SRC_PATH="%s"\n' "$(src_path)" > "$f"
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
#
#   /tmp being executable is not decoration here. cm-honeybee's agent install
#   SFTPs busybox and copyAgent.sh into /tmp and then executes them; a /tmp
#   mounted noexec makes the install fail with a permission error that names the
#   script rather than the mount.
src_start() {
	local img="$1" name env_file
	name="$(src_container)"
	env_file="$(_src_env_file)"
	docker rm -f "$name" >/dev/null 2>&1

	local args=(
		-d --name "$name" --hostname fs-src
		--privileged --cgroupns=host
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw
		--tmpfs /run --tmpfs /tmp:exec
		--stop-signal SIGRTMIN+3
		-v "$env_file:/opt/matrix/matrix.env:ro"
		-v "$SRC_PUBKEY_FILE:/opt/matrix/authorized_keys:ro"
		-p "$(src_ssh_port):$SRC_INTERNAL_SSH_PORT"
	)

	if ! docker run "${args[@]}" "$img" >/dev/null 2>&1; then
		fail "source container failed to start: $name ($img)"
		fail "  A systemd container needs --privileged and a /sys/fs/cgroup mount."
		fail "  The published port may also be taken — this folder uses the 34xxx band"
		fail "  (SSH $(src_ssh_port)); beetle-os-test uses 33xxx."
		return 1
	fi
	info "source container started: $name <- $img"
	info "  SSH $(src_endpoint) as $SRC_SSH_USER   dataset $(src_path)"
}

# assert_source_mounts — both bind-mounted files have to be files inside.
#
#   docker creates a bind mount whose source path does not exist as an empty
#   DIRECTORY, silently. For the env file that means the dataset path falls back
#   to the image default; for the public key it means sshd has no key to accept
#   and every login fails with no explanation. Both are worth catching here, with
#   the reason, rather than several steps later.
assert_source_mounts() {
	local name waited=0 out f bad=0
	name="$(src_container)"
	while [ "$waited" -lt 30 ]; do
		out="$(docker exec "$name" sh -c 'for f in /opt/matrix/matrix.env /opt/matrix/authorized_keys; do
			if [ -f "$f" ]; then echo "file $f"; elif [ -d "$f" ]; then echo "dir $f"; else echo "none $f"; fi
		done' 2>/dev/null)"
		if [ -n "$out" ]; then
			bad=0
			while read -r kind f; do
				[ -n "$kind" ] || continue
				case "$kind" in
				file) ;;
				dir)
					fail "the bind mount did not land: $f is a directory inside $name."
					fail "  docker creates a bind mount whose source does not exist as an empty directory."
					bad=1 ;;
				*)
					fail "the bind mount is missing: $f is not present inside $name."
					bad=1 ;;
				esac
			done <<< "$out"
			if [ "$bad" -eq 0 ]; then return 0; fi
			fail "  The source paths are on the DOCKER HOST:"
			fail "    $(_src_env_file)"
			fail "    $SRC_PUBKEY_FILE"
			fail "  Both live under TMPDIR. If this matrix runs inside a container against a"
			fail "  shared docker daemon, those paths exist only in that container and the host"
			fail "  has nothing there — run the matrix on the docker host, or point TMPDIR at a"
			fail "  directory both can see."
			return 1
		fi
		sleep 2
		waited=$((waited + 2))
	done
	fail "could not check the bind mounts inside $name (no exec response in 30s)."
	return 1
}

# src_wait_ready — until the dataset is built and the published SSH port answers.
#
#   Two things, because they fail for different reasons. matrix-init being active
#   means the work inside the container is done; the published port answering
#   means the address the other services were given actually reaches it. A unit
#   that ended up failed is not worth waiting on, so its log is printed and the
#   wait stops there.
src_wait_ready() {
	local name port waited=0 state phase
	name="$(src_container)"
	port="$(src_ssh_port)"

	assert_source_mounts || return 1

	while [ "$waited" -lt "${READY_TIMEOUT:-300}" ]; do
		state="$(docker exec "$name" systemctl is-active matrix-init.service 2>/dev/null || true)"
		case "$state" in
		active)
			if (echo > "/dev/tcp/$(src_host)/$port") >/dev/null 2>&1; then
				printf '\r%-110s\r' "" >&2
				ok "source ready: $name  (matrix-init active, $(src_endpoint) open)"
				return 0
			fi
			phase="dataset built — waiting for $(src_endpoint) to answer" ;;
		failed)
			printf '\r%-110s\r' "" >&2
			fail "source initialisation failed: $name"
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
		fail "  The dataset is built — what did not answer is $(src_endpoint) from this machine."
		fail "  HOST_IP has to be an address that reaches the container's published port."
		fail "  It is detected as the docker bridge gateway; set HOST_IP in .env to override."
		fail "  The container's own address works too, but with the internal port:"
		fail "    $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name" 2>/dev/null):$SRC_INTERNAL_SSH_PORT"
	fi
	docker exec "$name" systemctl --failed --no-legend 2>/dev/null | sed 's/^/      /' || true
	docker exec "$name" cat /var/log/matrix-init.log 2>/dev/null | tail -30 | sed 's/^/      /' || true
	return 1
}

src_stop() { docker rm -f "$(src_container)" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------
# Reading the source back — through the container itself
# ---------------------------------------------------------------------------
# The host running this matrix needs no SSH client for these: docker exec is
# enough, and it answers even when the published port does not — which is exactly
# the case where the difference matters.

# src_stats -> "FILECOUNT BYTES" on stdout, "0 0" when unreadable.
#   Used to describe the source in the run output and in the result JSON, so a
#   reader can hold it against what the validation compared.
src_stats() {
	local out
	out="$(docker exec "$(src_container)" sh -c \
		'printf "%s %s" "$(find '"$(src_path)"' -type f 2>/dev/null | wc -l)" "$(du -sb '"$(src_path)"' 2>/dev/null | cut -f1)"' 2>/dev/null)"
	case "$out" in
	''|*[!0-9\ ]*) printf '0 0' ;;
	*)             printf '%s' "$out" ;;
	esac
}

# src_top_dirs — the first level under the dataset root, space separated.
#   Printed so the run output names what is about to be migrated, and so a source
#   that seeded nothing is visible before a node is created for it.
src_top_dirs() {
	docker exec "$(src_container)" sh -c \
		'find '"$(src_path)"' -mindepth 1 -maxdepth 1 -type d -printf "%f\n" 2>/dev/null | sort' 2>/dev/null | tr '\n' ' '
}

# assert_src_dataset — the source has to hold something.
#
#   0  there are files
#   1  the dataset root is there but empty, or missing. Every cell would fail the
#      same way, so the caller stops rather than creating a node per CSP first.
assert_src_dataset() {
	local stats files bytes
	stats="$(src_stats)"
	files="${stats%% *}"; bytes="${stats##* }"
	if [ "${files:-0}" -eq 0 ] 2>/dev/null; then
		fail "the source holds no files under $(src_path)."
		fail "  matrix-init.service reported success, so this is unexpected. Look at the log:"
		fail "    docker exec $(src_container) cat /var/log/matrix-init.log"
		return 1
	fi
	info "source dataset: $files file(s), $bytes bytes under $(src_path)"
	info "  top level: $(src_top_dirs)"
	return 0
}
