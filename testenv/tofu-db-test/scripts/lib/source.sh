#!/usr/bin/env bash
#
# lib/source.sh — the whole source side: image builds, container lifetime, addresses.
#
# This used to be engine.sh plus four engine-<engine>.sh files, some 1,400 lines
# in all. Most of what they did now lives elsewhere.
#
#   database + seed          -> src/scripts/init-<engine>.sh, run by systemd inside
#   target database create   -> tofu/{my,pg}-target
#   snapshot comparison      -> centipede's validation API
#
# What is left is "stand up one machine to be the source, and say where it is",
# which fits in one file. Everything engine-specific is the three small tables
# below: internal port, systemd unit, version query.
#
# ── Why a systemd image ─────────────────────────────────────────────────────
# An official Docker image runs one process, but the source here plays the part
# of an on-premises machine. `systemctl` has to work, sshd has to be a unit and
# the database has to be a service that starts at boot, or nothing can be
# deployed onto it later - a collection agent, say. So src/Dockerfile.<engine>
# puts systemd into ubuntu:22.04 and installs the server from the vendor's apt
# repository. It costs three things, written up in the README: the first build is
# slow, the container runs --privileged, and the version choice narrows to what
# the apt repository serves.
#
# ── The readiness signal ────────────────────────────────────────────────────
# matrix-init.service is Type=oneshot with RemainAfterExit=yes, so the unit stays
# active once the init script has returned. That makes one `systemctl is-active`
# mean "the server is up, the account exists and the seed is loaded" all at once.

if [ -n "${MATRIX_SOURCE_SH:-}" ]; then return 0; fi
MATRIX_SOURCE_SH=1

SOURCE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$SOURCE_LIB_DIR/common.sh"

SOURCE_ROOT="$(cd "$SOURCE_LIB_DIR/../.." && pwd)"
SRC_BUILD_DIR="$SOURCE_ROOT/src"

# ---------------------------------------------------------------------------
# What differs per engine — three things only
# ---------------------------------------------------------------------------

# src_internal_port ENGINE — the port the server listens on inside the container
src_internal_port() {
	case "$(lower "$1")" in
	mysql|mariadb) printf '3306' ;;
	postgresql)    printf '5432' ;;
	mongodb)       printf '27017' ;;
	*)             die "unknown engine: $1" ;;
	esac
}

# src_service ENGINE — the systemd unit name in the image (for diagnostics)
src_service() {
	case "$(lower "$1")" in
	mysql)      printf 'mysql' ;;
	mariadb)    printf 'mariadb' ;;
	postgresql) printf 'postgresql' ;;
	mongodb)    printf 'mongod' ;;
	*)          die "unknown engine: $1" ;;
	esac
}

# _src_version_cmd ENGINE — the command that asks the server its real version.
#
#   What the server says, not what was configured. The build argument is "8.0"
#   while what got installed is "8.0.43", and the latter is what belongs in the
#   result.
_src_version_cmd() {
	local engine pass
	engine="$(lower "$1")"
	pass="${DB_ROOT_PASS:-testpass123}"
	#   Asked over the local socket. Over TCP, 127.0.0.1 resolves to localhost, and
	#   root@localhost on MySQL and MariaDB uses socket authentication by package
	#   default, so no password works there. PostgreSQL needs peer authentication
	#   for the same reason. Asking a server its own version needs no outside
	#   connection anyway.
	case "$engine" in
	mysql)      printf 'mysql -u root -p%s -N -B -e "SELECT VERSION()"' "$pass" ;;
	mariadb)    printf 'mariadb -u root -p%s -N -B -e "SELECT VERSION()"' "$pass" ;;
	postgresql) printf 'sudo -u postgres psql -Atc "SHOW server_version"' ;;
	mongodb)    printf 'mongosh --quiet -u root -p %s --authenticationDatabase admin --eval "db.version()"' "$pass" ;;
	esac
}

# ---------------------------------------------------------------------------
# Names and ports
# ---------------------------------------------------------------------------
# Names and ports stay clear of dockerenv (centipede-testenv-*), because both
# being up at once is the normal case: dockerenv serves the docker-to-docker
# scenario and this matrix serves managed targets, and one task can want both.
src_container() { printf 'tofu-db-test-%s-src' "$(lower "$1")"; }
src_image_repo() { printf 'tofu-db-test-src-%s' "$(lower "$1")"; }

# src_db_port / src_ssh_port ENGINE — <ENGINE>_SRC_DB_PORT / _SSH_PORT
#   These only read the value. Whether it is empty is checked once, before the
#   run, by assert_src_ports: both are called inside command substitution, so
#   dying here would kill only the subshell and hand the caller an empty string,
#   which then quietly becomes the wrong port.
src_db_port() {
	local name; name="$(upper "$1")_SRC_DB_PORT"
	printf '%s' "${!name:-}"
}
src_ssh_port() {
	local name; name="$(upper "$1")_SRC_SSH_PORT"
	printf '%s' "${!name:-}"
}

# assert_src_ports "ENGINE ..." — check .env has ports for the engines being run.
#   The ssh port is needed only for MODE=ssh.
assert_src_ports() {
	local engine missing=""
	for engine in $1; do
		[ -n "$(src_db_port "$engine")" ] || missing="$missing $(upper "$engine")_SRC_DB_PORT"
		if [ "${MODE:-direct}" = "ssh" ]; then
			[ -n "$(src_ssh_port "$engine")" ] || missing="$missing $(upper "$engine")_SRC_SSH_PORT"
		fi
	done
	[ -z "$missing" ] && return 0
	fail "source port settings are empty:$missing"
	fail "  Fill in the [SOURCE] section of .env (.env.example ships 36xxx defaults)."
	return 1
}

# ---------------------------------------------------------------------------
# Images
# ---------------------------------------------------------------------------

# src_image ENGINE VER -> the image name to use, on stdout.
#   Built once per version and cached by tag. A version the vendor's apt
#   repository does not serve fails here, before a container ever starts.
src_image() {
	local engine="$1" ver="$2" img dockerfile
	img="$(src_image_repo "$engine"):$ver"
	dockerfile="$SRC_BUILD_DIR/Dockerfile.$(lower "$engine")"

	if [ ! -f "$dockerfile" ]; then
		fail "no source Dockerfile: $dockerfile"
		return 1
	fi
	if docker image inspect "$img" >/dev/null 2>&1; then
		printf '%s' "$img"
		return 0
	fi

	info "building the source image: $img  (once per version, takes minutes)" >&2
	if ! timeout "${SRC_BUILD_TIMEOUT:-900}" \
		docker build -f "$dockerfile" --build-arg "DB_VERSION=$ver" \
			-t "$img" "$SRC_BUILD_DIR" >/dev/null 2>&1; then
		fail "source image build failed: $img"
		fail "  This engine's apt repository may not serve $ver for jammy."
		fail "  To see the build log yourself:"
		fail "    docker build -f $dockerfile --build-arg DB_VERSION=$ver -t $img $SRC_BUILD_DIR"
		return 1
	fi
	printf '%s' "$img"
}

# ---------------------------------------------------------------------------
# Containers
# ---------------------------------------------------------------------------

# _src_env_file ENGINE -> path to a file holding this cell's settings, on stdout.
#
#   Settings go in as a file rather than as environment variables because PID 1
#   here is systemd, and systemd does not hand its own environment to the units
#   it starts. Anything passed with `docker run -e` is invisible to
#   matrix-init.service.
_src_env_file() {
	local engine="$1" f
	f="${MATRIX_TMP:-/tmp}/src-$(lower "$engine").env"
	{
		printf 'SRC_DB=%s\n'       "${SRC_DB:-matrix_db}"
		printf 'DB_ROOT_PASS=%s\n' "${DB_ROOT_PASS:-testpass123}"
		printf 'SRC_DB_USER=%s\n'  "${SRC_DB_USER:-centipede}"
		printf 'SRC_DB_PASS=%s\n'  "${SRC_DB_PASS:-centipede_pass}"
		case "$(lower "$engine")" in
		mysql)
			printf 'MYSQL_SRC_CHARSET=%s\n'   "${MYSQL_SRC_CHARSET-utf8mb4}"
			printf 'MYSQL_SRC_COLLATION=%s\n' "${MYSQL_SRC_COLLATION-utf8mb4_general_ci}" ;;
		mariadb)
			printf 'MARIADB_SRC_CHARSET=%s\n'   "${MARIADB_SRC_CHARSET-utf8mb4}"
			printf 'MARIADB_SRC_COLLATION=%s\n' "${MARIADB_SRC_COLLATION-utf8mb4_general_ci}" ;;
		postgresql)
			printf 'POSTGRESQL_SRC_ENCODING=%s\n' "${POSTGRESQL_SRC_ENCODING-UTF8}"
			printf 'POSTGRESQL_SRC_LOCALE=%s\n'   "${POSTGRESQL_SRC_LOCALE-}" ;;
		mongodb)
			printf 'MONGODB_SRC_COLLATION_LOCALE=%s\n' "${MONGODB_SRC_COLLATION_LOCALE-}" ;;
		esac
	} > "$f"
	chmod 600 "$f" 2>/dev/null || true
	printf '%s' "$f"
}

# src_start ENGINE VER IMAGE MODE — start the container.
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
	local engine="$1" ver="$2" img="$3" mode="$4" name db_port ssh_port env_file
	name="$(src_container "$engine")"
	db_port="$(src_db_port "$engine")"
	ssh_port="$(src_ssh_port "$engine")"
	env_file="$(_src_env_file "$engine")"
	docker rm -f "$name" >/dev/null 2>&1

	local args=(
		-d --name "$name" --hostname "$(lower "$engine")-src"
		--privileged --cgroupns=host
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw
		--tmpfs /run --tmpfs /tmp:exec
		--stop-signal SIGRTMIN+3
		-v "$env_file:/opt/matrix/matrix.env:ro"
		-p "$db_port:$(src_internal_port "$engine")"
	)
	[ "$mode" = "ssh" ] && args+=(-p "$ssh_port:22")

	if ! docker run "${args[@]}" "$img" >/dev/null 2>&1; then
		fail "source container failed to start: $name ($img)"
		fail "  A systemd container needs --privileged and a /sys/fs/cgroup mount."
		return 1
	fi
	info "source container started: $name <- $img  (DB port $db_port$([ "$mode" = "ssh" ] && printf ', SSH %s' "$ssh_port"))"
}

# src_wait_ready ENGINE — until init has finished and the host port is open.
#
#   Two things are checked. matrix-init.service being active means the work
#   inside the container is done; the published port has to be open for honeybee
#   to reach it from outside. A unit that ended up failed is not worth waiting
#   on, so the log is printed and the wait stops there.
src_wait_ready() {
	local engine="$1" name port waited=0 state
	name="$(src_container "$engine")"
	port="$(src_db_port "$engine")"

	while [ "$waited" -lt "${READY_TIMEOUT:-300}" ]; do
		state="$(docker exec "$name" systemctl is-active matrix-init.service 2>/dev/null || true)"
		case "$state" in
		active)
			if (echo > "/dev/tcp/$HOST_IP/$port") >/dev/null 2>&1; then
				ok "source ready: $name  (matrix-init active, $HOST_IP:$port open)"
				return 0
			fi ;;
		failed)
			fail "source initialization failed: $name"
			docker exec "$name" cat /var/log/matrix-init.log 2>/dev/null | tail -30 | sed 's/^/      /'
			return 1 ;;
		esac
		sleep 3
		waited=$((waited + 3))
	done

	fail "timed out waiting for the source (${READY_TIMEOUT:-300}s): $name  (matrix-init=${state:-none})"
	docker exec "$name" systemctl --failed --no-legend 2>/dev/null | sed 's/^/      /' || true
	docker exec "$name" cat /var/log/matrix-init.log 2>/dev/null | tail -30 | sed 's/^/      /' || true
	return 1
}

src_stop() { docker rm -f "$(src_container "$1")" >/dev/null 2>&1; }

# src_server_version ENGINE — the version the server reports. Empty when unreadable.
#
#   Each engine decorates it - MariaDB with "10.6.18-MariaDB-1:10.6...",
#   PostgreSQL with "14.13 (Ubuntu ...)". Only the leading digits and dots are kept.
src_server_version() {
	local engine="$1" out
	out="$(docker exec "$(src_container "$engine")" \
		sh -c "$(_src_version_cmd "$engine")" 2>/dev/null | tr -d '\r' | head -1)"
	printf '%s' "$out" | sed -E 's/^[^0-9]*([0-9]+(\.[0-9]+)*).*/\1/'
}

# ---------------------------------------------------------------------------
# SSH — only for MODE=ssh
# ---------------------------------------------------------------------------
# sshd is already enabled as a unit in the image, so nothing has to be started -
# only the public key goes in.
#
# ⚠ honeybee stores the key itself (RSA-wrapped), not a path to it, so the
#   private key travels in the request body - which is why private_key is in
#   honeybee.sh's masking filter.

ensure_ssh_key() {
	local key="${MATRIX_SSH_KEY:-$SOURCE_ROOT/.matrix-ssh/id_rsa}"
	[ -f "$key" ] && return 0
	mkdir -p "$(dirname "$key")" || return 1
	ssh-keygen -q -t rsa -b 2048 -N '' -C 'tofu-db-test' -f "$key" || return 1
	info "SSH key generated: $key"
}

src_ssh_key_path() { printf '%s' "${MATRIX_SSH_KEY:-$SOURCE_ROOT/.matrix-ssh/id_rsa}"; }

# src_inject_ssh_key ENGINE — write the public key in and check sshd accepts a connection.
src_inject_ssh_key() {
	local engine="$1" name port key waited=0
	name="$(src_container "$engine")"
	port="$(src_ssh_port "$engine")"
	key="$(src_ssh_key_path)"

	docker exec -u 0 -i "$name" sh -c \
		'mkdir -p /root/.ssh && cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys' \
		< "$key.pub" || { fail "could not write authorized_keys: $name"; return 1; }

	while [ "$waited" -lt 60 ]; do
		if (echo > "/dev/tcp/$HOST_IP/$port") >/dev/null 2>&1; then
			info "SSH ready: $name ($HOST_IP:$port)"
			return 0
		fi
		sleep 2
		waited=$((waited + 2))
	done
	fail "the SSH port never opened: $name ($HOST_IP:$port)"
	docker exec "$name" systemctl status ssh.service --no-pager 2>&1 | sed 's/^/      /' || true
	return 1
}

# ---------------------------------------------------------------------------
# MongoDB target cleanup — borrowing the source container's mongosh
# ---------------------------------------------------------------------------
# MongoDB has no CREATE DATABASE, so there is nowhere to put a tofu module: one
# with no resource to manage holds nothing in state and its destroy does nothing.
# So this one case is dropped with the client inside the source container, which
# keeps mongosh off the host running the matrix.
#
# mongodb_drop_target "DB1 DB2 …"
mongodb_drop_target() {
	local dbs="$1" name db out rc=0 tls=""
	name="$(src_container mongodb)"

	case "$(target_tls_mode mongodb)" in
	require)     tls="--tls --tlsAllowInvalidCertificates" ;;
	verify-ca)   tls="--tls --tlsAllowInvalidHostnames" ;;
	verify-full) tls="--tls" ;;
	esac

	for db in $dbs; do
		out="$(docker exec "$name" mongosh --quiet $tls \
			--host "$RDBMS_HOST" --port "$RDBMS_PORT" \
			-u "$RDBMS_USER" -p "$(vault_db_password "$(lower "$CSP")")" \
			--authenticationDatabase "${MONGODB_AUTH_SOURCE:-admin}" \
			"$db" --eval 'db.dropDatabase()' 2>&1)" || true
		# Dropping a database that is not there is not an error. Only a failure to
		# connect is reported.
		case "$out" in
		*MongoServerError*|*MongoNetworkError*|*"Authentication failed"*)
			fail "could not clean up the target MongoDB: $db"
			printf '      %s\n' "$out" >&2
			rc=1 ;;
		esac
	done
	return $rc
}
