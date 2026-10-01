#!/usr/bin/env bash
#
# mysql-ver-matrix-migration.sh — MySQL version matrix migration (transx-ex called directly)
#
# Runs every combination of the source version list x the target version list,
# one cell at a time.
#   e.g. source=[8.0 8.4], target=[8.0 8.4] -> 8.0->8.0, 8.0->8.4, 8.4->8.0, 8.4->8.4 (4 cells)
#
# What one cell does:
#   1) prepare the official image (MODE=ssh builds an sshd image once per version)
#   2) start fresh source/target containers and wait until the DB answers queries
#   3) seed the source with a minimal data set (SQL embedded below — tables/view/
#      function/procedure/trigger/event + UTF-8 verification rows)
#   4) write config.json, then run the shared runner -> transxex.MigrateDBMS
#   5) verify by comparing source/target snapshots (object counts, row counts, UTF-8 values)
#   6) remove the containers and record the verdict
# It ends with the matrix table, the per-cell detail and a result JSON.
#
# Verdicts:
#   PASS  — migration succeeded and the source/target snapshots agree
#   BLOCK — a downgrade transx-ex refuses up front with VersionDowngradeError (expected)
#   FAIL  — anything else (the migration failed, or the snapshots disagree)
#   SKIP  — a cell excluded by ONLY_CELLS
#
# Source and target may use different access modes (SRC_MODE / DST_MODE). When both
# are ssh, transx-ex pipes mysqldump -> mysql without writing a dump file; every
# other combination (mixed included) goes through a local staging file in two steps.
#
# The matrix always migrates a whole database, so there is no scope option.
#
# Requirements:
#   - a running docker daemon, go 1.26+, jq
#   - internet access (pulls the official image; MODE=ssh also builds a derived image)
#   - no centipede/honeybee server — transx-ex is called as a library
#
# Usage:
#   ./transx-ex/examples/db-ver-matrix/mysql-ver-matrix-migration.sh
#   ./mysql-ver-matrix-migration.sh --src-versions "8.0" --dst-versions "8.0 8.4"
#   ./mysql-ver-matrix-migration.sh --only 8.4:8.0 --skip-version-check
#   ./mysql-ver-matrix-migration.sh --mode ssh
#   ./mysql-ver-matrix-migration.sh --src-mode ssh --dst-mode direct   # mixed access
#   MODE=ssh ./mysql-ver-matrix-migration.sh
#   SRC_MODE=ssh DST_MODE=direct ./mysql-ver-matrix-migration.sh
#
# Configuration: shared values live in db-ver-matrix.env next to this script (not committed).
#   Precedence: CLI option > real shell variable > db-ver-matrix.env > script default

set -uo pipefail

# ---------------------------------------------------------------------------
# Configuration — load the external env file (not committed)
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
ENV_FILE="${ENV_FILE:-$SCRIPT_DIR/db-ver-matrix.env}"
if [ -f "$ENV_FILE" ]; then
	# Honour the documented precedence: a real shell variable beats the env file.
	# Sourcing the file plainly would overwrite variables that are already set, so
	# snapshot the environment first and restore it once the file has been read.
	__ENV_PRESET="$(export -p)"
	set -a
	# shellcheck disable=SC1090
	. "$ENV_FILE"
	set +a
	eval "$__ENV_PRESET" 2>/dev/null || true
	unset __ENV_PRESET
fi

# ---------------------------------------------------------------------------
# Engine constants
# ---------------------------------------------------------------------------
ENGINE="mysql"
ENGINE_TITLE="MySQL"
IMAGE_REPO="mysql"
SSH_IMAGE_REPO="transxex-vermatrix-mysql-ssh"
INTERNAL_DB_PORT=3306
DB_ADMIN_USER="root"
SRC_CONTAINER="transxex-vermatrix-mysql-src"
DST_CONTAINER="transxex-vermatrix-mysql-dst"

# The runner is shared by every engine script; each keeps its own config and
# result under .run/<engine>/ so parallel runs never touch the same file.
RUNNER_DIR="$SCRIPT_DIR/runner"
RUNNER_BIN="$RUNNER_DIR/runner"
WORK_DIR="$SCRIPT_DIR/.run/$ENGINE"
CONFIG_JSON="$WORK_DIR/config.json"
CELL_RESULT="$WORK_DIR/result.json"

# ---------------------------------------------------------------------------
# Defaults (overridable by the env file or a shell variable)
# ---------------------------------------------------------------------------
MODE="${MODE:-direct}"
# Source and target access modes can be set separately. Empty means "use MODE".
#   e.g. SRC_MODE=ssh DST_MODE=direct — read over SSH, write over a direct connection
SRC_MODE="${SRC_MODE:-}"
DST_MODE="${DST_MODE:-}"
SKIP_VERSION_CHECK="${SKIP_VERSION_CHECK:-0}"
ASYNC="${ASYNC:-0}"
ROLLBACK_ON_FAILURE="${ROLLBACK_ON_FAILURE:-1}"

SRC_VERSIONS="${MYSQL_SRC_VERSIONS:-8.0 8.4}"
DST_VERSIONS="${MYSQL_DST_VERSIONS:-8.0 8.4}"

# HOST_IP — address used to reach the published container ports.
#   Override it only when Docker is not reachable at 127.0.0.1 from this host.
HOST_IP="${HOST_IP:-127.0.0.1}"

DB_ROOT_PASS="${DB_ROOT_PASS:-testpass123}"
SRC_DB="${SRC_DB:-matrix_db}"
DST_DB="${DST_DB:-matrix_db}"

# Database character set — applied when the source/target databases are created.
#   An empty target value falls back to the source value, so the target is built
#   to match the source. An empty value omits the clause and takes the server default.
SRC_CHARSET="${MYSQL_SRC_CHARSET-utf8mb4}"
SRC_COLLATION="${MYSQL_SRC_COLLATION-utf8mb4_general_ci}"
DST_CHARSET="${MYSQL_DST_CHARSET:-$SRC_CHARSET}"
DST_COLLATION="${MYSQL_DST_COLLATION:-$SRC_COLLATION}"

# Compare the character-set snapshots only when both sides were configured the
# same way. A deliberate difference is reported but not treated as a mismatch.
CHARSET_COMPARE=0
[ "$SRC_CHARSET" = "$DST_CHARSET" ] && [ "$SRC_COLLATION" = "$DST_COLLATION" ] && CHARSET_COMPARE=1

SRC_DB_PORT="${MYSQL_SRC_DB_PORT:-45406}"
DST_DB_PORT="${MYSQL_DST_DB_PORT:-45407}"
SRC_SSH_PORT="${MYSQL_SRC_SSH_PORT:-45240}"
DST_SSH_PORT="${MYSQL_DST_SSH_PORT:-45241}"

ONLY_CELLS="${ONLY_CELLS:-}"
STOP_ON_FAIL="${STOP_ON_FAIL:-0}"
KEEP_ON_FAIL="${KEEP_ON_FAIL:-0}"
READY_TIMEOUT="${READY_TIMEOUT:-180}"
NO_PAUSE="${NO_PAUSE:-1}"

# ---------------------------------------------------------------------------
# CLI options
# ---------------------------------------------------------------------------
usage() {
	cat <<USAGE
$ENGINE_TITLE version matrix — usage

  $(basename "$0") [options]

Options:
  --src-versions "V1 V2 ..."   source version list (matrix rows)
  --dst-versions "V1 V2 ..."   target version list (matrix columns)
  --only "SRC:DST ..."         run only these cells (e.g. --only "8.0:8.4")
  --mode direct|ssh            access mode for both sides (default: $MODE)
  --src-mode direct|ssh        access mode for the source only
  --dst-mode direct|ssh        access mode for the target only
  --skip-version-check         attempt the transfer even for a downgrade
  --async                      run asynchronously and poll progress
  --keep-on-fail               keep the containers of a failed cell
  --stop-on-fail               stop at the first FAIL
  -h, --help                   this help

Supported versions (official images): 8.0  8.4
USAGE
}

while [ $# -gt 0 ]; do
	case "$1" in
	--src-versions) SRC_VERSIONS="$2"; shift 2 ;;
	--dst-versions) DST_VERSIONS="$2"; shift 2 ;;
	--only) ONLY_CELLS="$2"; shift 2 ;;
	--mode) MODE="$2"; SRC_MODE=""; DST_MODE=""; shift 2 ;;
	--src-mode) SRC_MODE="$2"; shift 2 ;;
	--dst-mode) DST_MODE="$2"; shift 2 ;;
	--skip-version-check) SKIP_VERSION_CHECK=1; shift ;;
	--async) ASYNC=1; shift ;;
	--keep-on-fail) KEEP_ON_FAIL=1; shift ;;
	--stop-on-fail) STOP_ON_FAIL=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) echo "unknown option: $1" >&2; usage; exit 1 ;;
	esac
done

# Settle the access modes — without a per-side value, MODE applies to both.
SRC_MODE="${SRC_MODE:-$MODE}"
DST_MODE="${DST_MODE:-$MODE}"
if [ "$SRC_MODE" = "$DST_MODE" ]; then
	MODE_LABEL="$SRC_MODE"
else
	MODE_LABEL="$SRC_MODE->$DST_MODE"
fi

# ---------------------------------------------------------------------------
# Logs — overwritten on every run
#   <script dir>/logs/<script name>.log         : the whole screen output, colours stripped
#   <script dir>/logs/<script name>-result.json : the per-cell result matrix
# ---------------------------------------------------------------------------
LOG_DIR="${LOG_DIR:-$SCRIPT_DIR/logs}"
LOG_NAME="$(basename "${BASH_SOURCE[0]:-$0}" .sh)"
LOG_FILE="${LOG_FILE:-$LOG_DIR/$LOG_NAME.log}"
RESULT_FILE="${RESULT_FILE:-$LOG_DIR/$LOG_NAME-result.json}"
if [ "${NO_LOG:-}" != "1" ]; then
	mkdir -p "$LOG_DIR" || { echo "ERROR: cannot create the log directory: $LOG_DIR" >&2; exit 1; }
	exec > >(tee >(sed -u -r 's/\x1b\[[0-9;]*[mK]//g' > "$LOG_FILE")) 2>&1
	LOG_TEE_PID=$!
	echo "run log     : $LOG_FILE"
	echo "result JSON : $RESULT_FILE"
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required." >&2; exit 1; }
command -v jq     >/dev/null 2>&1 || { echo "ERROR: jq is required."     >&2; exit 1; }
command -v go     >/dev/null 2>&1 || { echo "ERROR: go is required."     >&2; exit 1; }
docker info >/dev/null 2>&1        || { echo "ERROR: the docker daemon is not running." >&2; exit 1; }

C_HDR="\033[1;36m"; C_SUB="\033[1;33m"; C_OK="\033[0;32m"; C_ERR="\033[0;31m"; C_WARN="\033[0;33m"; C_OFF="\033[0m"

banner() { echo; echo -e "${C_HDR}========================================================================${C_OFF}"; echo -e "${C_HDR}  $*${C_OFF}"; echo -e "${C_HDR}========================================================================${C_OFF}"; }
sub()    { echo; echo -e "${C_SUB}--- $* ---${C_OFF}"; }
info()   { echo -e "  $*"; }
warn()   { echo -e "  ${C_WARN}$*${C_OFF}"; }
fail()   { echo -e "  ${C_ERR}$*${C_OFF}" >&2; }

pause() {
	[ "${NO_PAUSE:-1}" = "1" ] && return 0
	[ -e /dev/tty ] || return 0
	echo -e "  ${C_SUB}> paused — press any key to continue (Ctrl-C to abort)${C_OFF}" >/dev/tty 2>/dev/null || true
	read -r -n 1 -s _ </dev/tty 2>/dev/null || true
	echo >/dev/tty 2>/dev/null || true
}

# version_gt A B — 0 when A is a higher version than B
version_gt() {
	[ "$1" = "$2" ] && return 1
	[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$2" ]
}

# secs_fmt N — seconds as "12s" / "1m03s"
secs_fmt() {
	local s="${1%.*}"
	[ -z "$s" ] && s=0
	if [ "$s" -lt 60 ]; then printf '%ds' "$s"; else printf '%dm%02ds' "$((s / 60))" "$((s % 60))"; fi
}

# ---------------------------------------------------------------------------
# Containers — image, start, wait, remove
# ---------------------------------------------------------------------------

# ensure_image VER MODE -> prints the image name to use. In ssh mode it builds an
# sshd-derived image once per version and caches it, because the transx-ex
# ssh-tunnel mode runs mysqldump/mysql on the remote host.
ensure_image() {
	local ver="$1" mode="$2" img
	local base="$IMAGE_REPO:$ver"
	if ! docker image inspect "$base" >/dev/null 2>&1; then
		info "pulling image: $base" >&2
		docker pull "$base" >/dev/null 2>&1 || { fail "image pull failed: $base"; return 1; }
	fi
	if [ "$mode" != "ssh" ]; then
		echo "$base"
		return 0
	fi
	img="$SSH_IMAGE_REPO:$ver"
	if docker image inspect "$img" >/dev/null 2>&1; then
		echo "$img"
		return 0
	fi
	info "building the sshd image: $img (once per version)" >&2
	docker build -t "$img" - >/dev/null 2>&1 <<DOCKERFILE
FROM $base
USER root
RUN microdnf install -y openssh-server openssh-clients \
    && microdnf clean all \
    && ssh-keygen -A \
    && mkdir -p /var/empty/sshd /root/.ssh \
    && chmod 700 /root/.ssh
DOCKERFILE
	if [ $? -ne 0 ] || ! docker image inspect "$img" >/dev/null 2>&1; then
		fail "sshd image build failed: $img"
		return 1
	fi
	echo "$img"
}

# ensure_ssh_key — prepare the key pair used in ssh mode (generated when missing)
SSH_KEY="$SCRIPT_DIR/.matrix-ssh/id_rsa"
ensure_ssh_key() {
	[ -f "$SSH_KEY" ] && return 0
	mkdir -p "$(dirname "$SSH_KEY")" || return 1
	ssh-keygen -q -t rsa -b 2048 -N '' -C 'transxex-vermatrix' -f "$SSH_KEY" || return 1
	info "SSH key generated: $SSH_KEY"
}

# start_container ROLE VER IMAGE — ROLE is src|dst
start_container() {
	local role="$1" ver="$2" img="$3" name db_port ssh_port mode
	if [ "$role" = "src" ]; then
		name="$SRC_CONTAINER"; db_port="$SRC_DB_PORT"; ssh_port="$SRC_SSH_PORT"; mode="$SRC_MODE"
	else
		name="$DST_CONTAINER"; db_port="$DST_DB_PORT"; ssh_port="$DST_SSH_PORT"; mode="$DST_MODE"
	fi
	docker rm -f "$name" >/dev/null 2>&1

	# The DB port is published in both modes — seeding and snapshots run inside the
	# container, but a runner connecting directly uses the published port.
	local ports=(-p "$db_port:$INTERNAL_DB_PORT")
	[ "$mode" = "ssh" ] && ports+=(-p "$ssh_port:22")

	# The database is not created by an image init variable: the character set has
	# to come from the env file, so create_database issues CREATE DATABASE after startup.
	docker run -d --name "$name" \
		-e MYSQL_ROOT_PASSWORD="$DB_ROOT_PASS" \
		"${ports[@]}" "$img" >/dev/null 2>&1 \
		|| { fail "container start failed: $name ($img)"; return 1; }
	info "container started: $name <- $img (DB port $db_port)"
}

# wait_ready CONTAINER HOST_PORT — wait until the DB answers over TCP
#   The official image runs a temporary server with --skip-networking while it
#   initialises; only the unix socket is open then. A container is ready once it
#   answers over TCP inside and the published host port is reachable.
wait_ready() {
	local name="$1" port="$2" waited=0
	while [ "$waited" -lt "$READY_TIMEOUT" ]; do
		if docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$name" \
			mysql -h 127.0.0.1 -P "$INTERNAL_DB_PORT" -u"$DB_ADMIN_USER" --batch --silent \
			-e "SELECT 1" >/dev/null 2>&1 \
			&& (echo > "/dev/tcp/$HOST_IP/$port") >/dev/null 2>&1; then
			return 0
		fi
		sleep 3
		waited=$((waited + 3))
	done
	fail "timed out waiting for the DB (${READY_TIMEOUT}s): $name"
	docker logs --tail 20 "$name" 2>&1 | sed 's/^/      /'
	return 1
}

# create_database ROLE — create the database with the character set from the env file.
#   transx-ex does not create the target database; it requires one that exists and
#   is empty, so both sides are created here the same way.
create_database() {
	local role="$1" name db charset collation clause=""
	if [ "$role" = "src" ]; then
		name="$SRC_CONTAINER"; db="$SRC_DB"; charset="$SRC_CHARSET"; collation="$SRC_COLLATION"
	else
		name="$DST_CONTAINER"; db="$DST_DB"; charset="$DST_CHARSET"; collation="$DST_COLLATION"
	fi
	[ -n "$charset" ]   && clause=" CHARACTER SET $charset"
	[ -n "$collation" ] && clause="$clause COLLATE $collation"

	local out
	out="$(docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$name" \
		mysql -h 127.0.0.1 -P "$INTERNAL_DB_PORT" -u"$DB_ADMIN_USER" \
		-e "CREATE DATABASE \`$db\`$clause" 2>&1)"
	if [ $? -ne 0 ]; then
		fail "$role database creation failed: $db$clause"
		printf '      %s\n' "$out"
		return 1
	fi
	info "$role database created: $db${clause:- (server default)}"
}

# start_sshd CONTAINER PORT — install the public key and start sshd (ssh mode)
start_sshd() {
	local name="$1" port="$2" waited=0
	docker exec -u 0 -i "$name" sh -c \
		'mkdir -p /root/.ssh && cat > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys' \
		< "$SSH_KEY.pub" || { fail "could not install authorized_keys: $name"; return 1; }
	docker exec -u 0 -d "$name" /usr/sbin/sshd -D >/dev/null 2>&1
	while [ "$waited" -lt 30 ]; do
		if (echo > "/dev/tcp/$HOST_IP/$port") >/dev/null 2>&1; then
			info "sshd started: $name (SSH port $port)"
			return 0
		fi
		sleep 1
		waited=$((waited + 1))
	done
	fail "timed out waiting for sshd: $name (port $port)"
	return 1
}

remove_containers() {
	docker rm -f "$SRC_CONTAINER" "$DST_CONTAINER" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Seed data — embedded in this script (kept minimal)
#   4 tables (3 FKs) / 1 view / 1 function / 1 procedure / 1 trigger / 1 event
#   + UTF-8 verification rows (Korean, Japanese, emoji)
# ---------------------------------------------------------------------------
seed_source() {
	docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$SRC_CONTAINER" \
		mysql -u"$DB_ADMIN_USER" -e \
		"SET GLOBAL log_bin_trust_function_creators = 1; SET GLOBAL event_scheduler = ON;" >/dev/null 2>&1

	docker exec -i -e MYSQL_PWD="$DB_ROOT_PASS" "$SRC_CONTAINER" \
		mysql -u"$DB_ADMIN_USER" --default-character-set=utf8mb4 "$SRC_DB" <<'SEED_SQL' 2>&1 | sed 's/^/      /'
CREATE TABLE customers (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  name       VARCHAR(100) NOT NULL,
  email      VARCHAR(100) NOT NULL UNIQUE,
  grade      ENUM('bronze','silver','gold') NOT NULL DEFAULT 'bronze',
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE products (
  id    INT AUTO_INCREMENT PRIMARY KEY,
  name  VARCHAR(100) NOT NULL,
  price DECIMAL(10,2) NOT NULL,
  stock INT NOT NULL DEFAULT 0
);

CREATE TABLE orders (
  id          INT AUTO_INCREMENT PRIMARY KEY,
  customer_id INT NOT NULL,
  status      VARCHAR(20) NOT NULL DEFAULT 'pending',
  ordered_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT fk_orders_customer FOREIGN KEY (customer_id) REFERENCES customers(id)
);

CREATE TABLE order_items (
  id         INT AUTO_INCREMENT PRIMARY KEY,
  order_id   INT NOT NULL,
  product_id INT NOT NULL,
  qty        INT NOT NULL DEFAULT 1,
  unit_price DECIMAL(10,2) NOT NULL,
  CONSTRAINT fk_items_order   FOREIGN KEY (order_id)   REFERENCES orders(id),
  CONSTRAINT fk_items_product FOREIGN KEY (product_id) REFERENCES products(id)
);

CREATE VIEW v_order_summary AS
  SELECT c.id AS customer_id, c.name AS customer_name,
         COUNT(DISTINCT o.id) AS order_count,
         IFNULL(SUM(oi.qty * oi.unit_price), 0) AS total_amount
    FROM customers c
    LEFT JOIN orders o       ON c.id = o.customer_id
    LEFT JOIN order_items oi ON o.id = oi.order_id
   GROUP BY c.id, c.name;

DELIMITER //
CREATE FUNCTION fn_order_total(p_order_id INT) RETURNS DECIMAL(12,2) READS SQL DATA
BEGIN
  DECLARE total DECIMAL(12,2) DEFAULT 0;
  SELECT IFNULL(SUM(qty * unit_price), 0) INTO total FROM order_items WHERE order_id = p_order_id;
  RETURN total;
END//

CREATE PROCEDURE sp_customer_orders(IN p_customer_id INT)
BEGIN
  SELECT o.id, o.status, fn_order_total(o.id) AS total
    FROM orders o WHERE o.customer_id = p_customer_id ORDER BY o.id;
END//

CREATE TRIGGER trg_order_items_ai AFTER INSERT ON order_items FOR EACH ROW
BEGIN
  UPDATE products SET stock = stock - NEW.qty WHERE id = NEW.product_id;
END//
DELIMITER ;

CREATE EVENT evt_matrix_touch
  ON SCHEDULE EVERY 1 DAY STARTS CURRENT_TIMESTAMP
  DO UPDATE products SET stock = stock WHERE id = 0;

INSERT INTO customers (name, email, grade) VALUES
  ('Alice Kim',   'alice@example.com',   'gold'),
  ('Bob Lee',     'bob@example.com',     'silver'),
  ('Charlie Park','charlie@example.com', 'bronze'),
  ('김철수 (한글)', 'utf8-ko@example.com', 'gold'),
  ('佐藤 太郎 🎌',  'utf8-ja@example.com', 'silver');

INSERT INTO products (name, price, stock) VALUES
  ('Laptop Pro',     1299.99, 50),
  ('Wireless Mouse',   29.99, 200),
  ('USB-C Hub',        49.99, 150),
  ('4K Monitor',      399.99, 30),
  ('노트북 거치대 🖥',   39.99, 120);

INSERT INTO orders (customer_id, status) VALUES
  (1, 'confirmed'), (2, 'shipped'), (3, 'pending'), (4, 'confirmed'), (5, 'shipped');

INSERT INTO order_items (order_id, product_id, qty, unit_price) VALUES
  (1, 1, 1, 1299.99), (1, 2, 2, 29.99), (2, 3, 1, 49.99), (3, 4, 1, 399.99),
  (4, 5, 2, 39.99),   (4, 2, 1, 29.99), (5, 1, 1, 1299.99), (5, 3, 3, 49.99);
SEED_SQL
}

# ---------------------------------------------------------------------------
# Snapshots — normalised strings compared between source and target
#   schema : object counts  /  data : row counts + UTF-8 values
# ---------------------------------------------------------------------------
snapshot_schema() {
	local name="$1" db="$2"
	docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$name" \
		mysql -u"$DB_ADMIN_USER" --batch --silent --default-character-set=utf8mb4 -e "
SELECT CONCAT(
  'tables=',   (SELECT COUNT(*) FROM information_schema.TABLES    WHERE TABLE_SCHEMA='$db' AND TABLE_TYPE='BASE TABLE'),
  ' views=',   (SELECT COUNT(*) FROM information_schema.VIEWS     WHERE TABLE_SCHEMA='$db'),
  ' funcs=',   (SELECT COUNT(*) FROM information_schema.ROUTINES  WHERE ROUTINE_SCHEMA='$db' AND ROUTINE_TYPE='FUNCTION'),
  ' procs=',   (SELECT COUNT(*) FROM information_schema.ROUTINES  WHERE ROUTINE_SCHEMA='$db' AND ROUTINE_TYPE='PROCEDURE'),
  ' triggers=',(SELECT COUNT(*) FROM information_schema.TRIGGERS  WHERE TRIGGER_SCHEMA='$db'),
  ' events=',  (SELECT COUNT(*) FROM information_schema.EVENTS    WHERE EVENT_SCHEMA='$db'),
  ' fks=',     (SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS WHERE TABLE_SCHEMA='$db' AND CONSTRAINT_TYPE='FOREIGN KEY')
);" 2>/dev/null | tr -d '\r'
}

# snapshot_charset — the database default character set and the per-table collation
snapshot_charset() {
	local name="$1" db="$2"
	docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$name" \
		mysql -u"$DB_ADMIN_USER" --batch --silent --default-character-set=utf8mb4 -e "
SELECT CONCAT(
  'db=',    (SELECT CONCAT(DEFAULT_CHARACTER_SET_NAME, '/', DEFAULT_COLLATION_NAME)
               FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='$db'),
  ' tables=', IFNULL((SELECT GROUP_CONCAT(CONCAT(TABLE_NAME, ':', TABLE_COLLATION) ORDER BY TABLE_NAME SEPARATOR ',')
               FROM information_schema.TABLES
              WHERE TABLE_SCHEMA='$db' AND TABLE_TYPE='BASE TABLE'), '')
);" 2>/dev/null | tr -d '\r'
}

snapshot_data() {
	local name="$1" db="$2"
	docker exec -e MYSQL_PWD="$DB_ROOT_PASS" "$name" \
		mysql -u"$DB_ADMIN_USER" --batch --silent --default-character-set=utf8mb4 "$db" -e "
SELECT CONCAT(
  'rows=', (SELECT COUNT(*) FROM customers), '/', (SELECT COUNT(*) FROM products), '/',
           (SELECT COUNT(*) FROM orders),    '/', (SELECT COUNT(*) FROM order_items),
  ' utf8=', (SELECT CONCAT(name, '|', (SELECT name FROM products WHERE id = 5)) FROM customers WHERE id = 4),
  ' sum=',  (SELECT CAST(SUM(qty * unit_price) AS CHAR) FROM order_items)
);" 2>/dev/null | tr -d '\r'
}

# ---------------------------------------------------------------------------
# Runner — the Go binary that calls transx-ex
# ---------------------------------------------------------------------------
build_runner() {
	sub "building the runner — $RUNNER_DIR"
	mkdir -p "$WORK_DIR" || { fail "cannot create the work directory: $WORK_DIR"; return 1; }
	( cd "$RUNNER_DIR" && go build -o runner . ) 2>&1 | sed 's/^/      /'
	if [ ! -x "$RUNNER_BIN" ]; then
		fail "runner build failed: $RUNNER_DIR"
		return 1
	fi
	info "OK — $RUNNER_BIN"
}

# location_json ROLE — one location object matching that side's access mode
location_json() {
	local role="$1" mode db ssh_port db_port
	if [ "$role" = "src" ]; then
		mode="$SRC_MODE"; db="$SRC_DB"; ssh_port="$SRC_SSH_PORT"; db_port="$SRC_DB_PORT"
	else
		mode="$DST_MODE"; db="$DST_DB"; ssh_port="$DST_SSH_PORT"; db_port="$DST_DB_PORT"
	fi

	if [ "$mode" = "ssh" ]; then
		jq -n --arg t "$ENGINE" --arg db "$db" --arg host "$HOST_IP" --arg key "$SSH_KEY" \
			--arg u "$DB_ADMIN_USER" --arg pw "$DB_ROOT_PASS" \
			--argjson sport "$ssh_port" --argjson iport "$INTERNAL_DB_PORT" '
			{dbmsType:$t, database:$db, providerName:"onprem", accessType:"ssh-tunnel",
			 sshTunnel:{ssh:{host:$host, port:$sport, username:"root", privateKeyPath:$key},
			            dbHost:"127.0.0.1", dbPort:$iport, username:$u, password:$pw}}'
	else
		jq -n --arg t "$ENGINE" --arg db "$db" --arg host "$HOST_IP" \
			--arg u "$DB_ADMIN_USER" --arg pw "$DB_ROOT_PASS" \
			--argjson port "$db_port" '
			{dbmsType:$t, database:$db, providerName:"onprem", accessType:"direct",
			 direct:{host:$host, port:$port, username:$u, password:$pw}}'
	fi
}

# write_config — the config.json for one cell. The two sides may use different
# access modes; transx-ex then stages through a file instead of piping.
#   scope is fixed to "full": the matrix always migrates a whole database.
write_config() {
	local rollback="false"
	[ "$ROLLBACK_ON_FAILURE" = "1" ] && rollback="true"

	jq -n \
		--argjson s "$(location_json src)" \
		--argjson d "$(location_json dst)" \
		--argjson rb "$rollback" \
		'{source:$s, destination:$d, scope:"full", rollbackOnFailure:$rb}' > "$CONFIG_JSON"
	echo "$CONFIG_JSON"
}

# ---------------------------------------------------------------------------
# Cell execution
# ---------------------------------------------------------------------------
declare -A CELL_STATUS CELL_ELAPSED CELL_DETAIL CELL_SRCVER CELL_DSTVER

# run_cell SRC_VER DST_VER
run_cell() {
	local sver="$1" dver="$2"
	local key="$sver:$dver"
	local started ended elapsed cfg rc kind msg simg dimg
	local expect_block=0
	version_gt "$sver" "$dver" && [ "$SKIP_VERSION_CHECK" != "1" ] && expect_block=1

	banner "cell $sver -> $dver   (MODE=$MODE_LABEL)"
	started="$(date +%s)"

	simg="$(ensure_image "$sver" "$SRC_MODE")" || { finish_cell "$key" FAIL 0 "source image not ready ($IMAGE_REPO:$sver)"; return; }
	dimg="$(ensure_image "$dver" "$DST_MODE")" || { finish_cell "$key" FAIL 0 "target image not ready ($IMAGE_REPO:$dver)"; return; }

	sub "1) start the containers"
	start_container src "$sver" "$simg" || { finish_cell "$key" FAIL 0 "source container did not start"; cleanup_cell 1; return; }
	start_container dst "$dver" "$dimg" || { finish_cell "$key" FAIL 0 "target container did not start"; cleanup_cell 1; return; }
	wait_ready "$SRC_CONTAINER" "$SRC_DB_PORT" || { finish_cell "$key" FAIL 0 "source DB never became ready"; cleanup_cell 1; return; }
	wait_ready "$DST_CONTAINER" "$DST_DB_PORT" || { finish_cell "$key" FAIL 0 "target DB never became ready"; cleanup_cell 1; return; }
	if [ "$SRC_MODE" = "ssh" ]; then
		start_sshd "$SRC_CONTAINER" "$SRC_SSH_PORT" || { finish_cell "$key" FAIL 0 "source sshd did not start"; cleanup_cell 1; return; }
	fi
	if [ "$DST_MODE" = "ssh" ]; then
		start_sshd "$DST_CONTAINER" "$DST_SSH_PORT" || { finish_cell "$key" FAIL 0 "target sshd did not start"; cleanup_cell 1; return; }
	fi

	sub "2) create the databases (character set from the env file)"
	create_database src || { finish_cell "$key" FAIL 0 "source database not created ($SRC_CHARSET/$SRC_COLLATION)"; cleanup_cell 1; return; }
	create_database dst || { finish_cell "$key" FAIL 0 "target database not created ($DST_CHARSET/$DST_COLLATION)"; cleanup_cell 1; return; }

	sub "3) seed the source"
	seed_source || { finish_cell "$key" FAIL 0 "seeding failed"; cleanup_cell 1; return; }
	local src_schema src_data src_charset
	src_schema="$(snapshot_schema "$SRC_CONTAINER" "$SRC_DB")"
	src_data="$(snapshot_data "$SRC_CONTAINER" "$SRC_DB")"
	src_charset="$(snapshot_charset "$SRC_CONTAINER" "$SRC_DB")"
	info "source snapshot: $src_schema"
	info "                 $src_data"
	info "                 $src_charset"

	sub "4) migrate — transx-ex"
	cfg="$(write_config)"
	rm -f "$CELL_RESULT"
	local args=(--config="$cfg" --result="$CELL_RESULT")
	[ "$ASYNC" = "1" ] && args+=(--async)
	[ "$SKIP_VERSION_CHECK" = "1" ] && args+=(--skip-version-check)
	"$RUNNER_BIN" "${args[@]}"
	rc=$?
	kind="$(jq -r '.errorKind // ""' "$CELL_RESULT" 2>/dev/null)"
	msg="$(jq -r '.error // ""'     "$CELL_RESULT" 2>/dev/null | head -1 | cut -c1-160)"
	CELL_SRCVER["$key"]="$(jq -r '.sourceVersion // ""' "$CELL_RESULT" 2>/dev/null)"
	CELL_DSTVER["$key"]="$(jq -r '.targetVersion // ""' "$CELL_RESULT" 2>/dev/null)"

	ended="$(date +%s)"
	elapsed=$((ended - started))

	if [ "$rc" -ne 0 ]; then
		if [ "$kind" = "version-downgrade" ] && [ "$expect_block" = "1" ]; then
			finish_cell "$key" BLOCK "$elapsed" "VersionDowngradeError — ${CELL_SRCVER[$key]} -> ${CELL_DSTVER[$key]}"
		else
			finish_cell "$key" FAIL "$elapsed" "[$kind] $msg"
		fi
		cleanup_cell "$rc"
		return
	fi

	sub "5) verify — compare the source and target snapshots"
	local dst_schema dst_data dst_charset detail=""
	dst_schema="$(snapshot_schema "$DST_CONTAINER" "$DST_DB")"
	dst_data="$(snapshot_data "$DST_CONTAINER" "$DST_DB")"
	dst_charset="$(snapshot_charset "$DST_CONTAINER" "$DST_DB")"
	info "target snapshot: $dst_schema"
	info "                 $dst_data"
	info "                 $dst_charset"

	if [ "$src_schema" != "$dst_schema" ]; then
		detail="schema mismatch — src[$src_schema] dst[$dst_schema]"
	elif [ "$src_data" != "$dst_data" ]; then
		detail="data mismatch — src[$src_data] dst[$dst_data]"
	elif [ "$CHARSET_COMPARE" = "1" ] && [ "$src_charset" != "$dst_charset" ]; then
		detail="character set mismatch — src[$src_charset] dst[$dst_charset]"
	fi
	# A difference the operator asked for is intentional, so it is not compared.
	[ "$CHARSET_COMPARE" = "1" ] || warn "character set comparison skipped — source($SRC_CHARSET/$SRC_COLLATION) != target($DST_CHARSET/$DST_COLLATION)"

	if [ -n "$detail" ]; then
		fail "$detail"
		finish_cell "$key" FAIL "$elapsed" "$detail"
		cleanup_cell 1
	else
		finish_cell "$key" PASS "$elapsed" "$src_schema  $src_data  $src_charset"
		cleanup_cell 0
	fi
}

# finish_cell KEY STATUS ELAPSED DETAIL
finish_cell() {
	CELL_STATUS["$1"]="$2"
	CELL_ELAPSED["$1"]="$3"
	CELL_DETAIL["$1"]="$4"
	case "$2" in
	PASS)  echo -e "  ${C_OK}[*] $1 : PASS${C_OFF}  ($(secs_fmt "$3"))" ;;
	BLOCK) echo -e "  ${C_WARN}[*] $1 : BLOCK${C_OFF} ($(secs_fmt "$3")) — $4" ;;
	*)     echo -e "  ${C_ERR}[*] $1 : FAIL${C_OFF}  ($(secs_fmt "$3")) — $4" ;;
	esac
	pause
}

# cleanup_cell RC — tidy up the containers once a cell is done
cleanup_cell() {
	if [ "$1" -ne 0 ] && [ "$KEEP_ON_FAIL" = "1" ]; then
		warn "KEEP_ON_FAIL=1 — keeping the containers ($SRC_CONTAINER, $DST_CONTAINER)"
		KEEP_CONTAINERS=1
		return 0
	fi
	remove_containers
}

# cleanup_exit — exit trap. Containers kept by KEEP_ON_FAIL are left alone.
cleanup_exit() {
	[ "${KEEP_CONTAINERS:-0}" = "1" ] && return 0
	remove_containers
}

# ---------------------------------------------------------------------------
# Matrix output
# ---------------------------------------------------------------------------
cell_text() {
	local key="$1"
	local st="${CELL_STATUS[$key]:-SKIP}"
	case "$st" in
	PASS|FAIL|BLOCK) printf '%-5s %s' "$st" "$(secs_fmt "${CELL_ELAPSED[$key]:-0}")" ;;
	*)               printf 'SKIP' ;;
	esac
}

cell_color() {
	case "${CELL_STATUS[$1]:-SKIP}" in
	PASS)  printf '%b' "$C_OK" ;;
	BLOCK) printf '%b' "$C_WARN" ;;
	FAIL)  printf '%b' "$C_ERR" ;;
	*)     printf '' ;;
	esac
}

print_matrix() {
	local w=11 dver sver key line
	banner "$ENGINE_TITLE version matrix result — MODE=$MODE_LABEL  SKIP_VERSION_CHECK=$SKIP_VERSION_CHECK"

	line="  src \\ dst   |"
	for dver in $DST_VERSIONS; do line+="$(printf ' %-*s' "$w" "$dver")"; done
	echo -e "${C_HDR}$line${C_OFF}"

	line="  ------------+"
	for dver in $DST_VERSIONS; do line+="$(printf -- '-%.0s' $(seq 1 $((w + 1))))"; done
	echo "$line"

	for sver in $SRC_VERSIONS; do
		printf '  %-11s |' "$sver"
		for dver in $DST_VERSIONS; do
			key="$sver:$dver"
			printf ' %b%-*s%b' "$(cell_color "$key")" "$w" "$(cell_text "$key")" "$C_OFF"
		done
		echo
	done

	local pass=0 blocked=0 failed=0 skipped=0 sver dver key
	for sver in $SRC_VERSIONS; do
		for dver in $DST_VERSIONS; do
			key="$sver:$dver"
			case "${CELL_STATUS[$key]:-SKIP}" in
			PASS)  pass=$((pass + 1)) ;;
			BLOCK) blocked=$((blocked + 1)) ;;
			FAIL)  failed=$((failed + 1)) ;;
			*)     skipped=$((skipped + 1)) ;;
			esac
		done
	done

	echo
	echo -e "  ${C_OK}PASS $pass${C_OFF} / ${C_WARN}BLOCKED(expected) $blocked${C_OFF} / ${C_ERR}FAIL $failed${C_OFF} / SKIP $skipped" \
		"  $((pass + blocked + failed + skipped)) cells   took $(secs_fmt "$(( $(date +%s) - RUN_STARTED ))")"

	sub "cell detail"
	for sver in $SRC_VERSIONS; do
		for dver in $DST_VERSIONS; do
			key="$sver:$dver"
			[ "${CELL_STATUS[$key]:-SKIP}" = "SKIP" ] && continue
			printf '    %-6s -> %-6s %-5s  %s\n' "$sver" "$dver" \
				"${CELL_STATUS[$key]}" "${CELL_DETAIL[$key]:-}"
		done
	done

	MATRIX_FAILED="$failed"
}

write_result_json() {
	local sver dver key cells="[]"
	for sver in $SRC_VERSIONS; do
		for dver in $DST_VERSIONS; do
			key="$sver:$dver"
			cells="$(jq -c \
				--arg s "$sver" --arg d "$dver" \
				--arg st "${CELL_STATUS[$key]:-SKIP}" \
				--arg de "${CELL_DETAIL[$key]:-}" \
				--arg sv "${CELL_SRCVER[$key]:-}" \
				--arg dv "${CELL_DSTVER[$key]:-}" \
				--argjson el "${CELL_ELAPSED[$key]:-0}" \
				'. + [{srcVersion:$s, dstVersion:$d, status:$st, elapsedSec:$el,
				       srcServerVersion:$sv, dstServerVersion:$dv, detail:$de}]' <<<"$cells")"
		done
	done
	jq -n --arg e "$ENGINE" --arg sm "$SRC_MODE" --arg dm "$DST_MODE" \
		--arg svc "$SKIP_VERSION_CHECK" --arg ts "$(date '+%F %T')" --argjson c "$cells" \
		'{engine:$e, srcMode:$sm, dstMode:$dm, skipVersionCheck:($svc=="1"),
		  finishedAt:$ts, cells:$c}' \
		> "$RESULT_FILE"
	info "result JSON: $RESULT_FILE"
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------
RUN_STARTED="$(date +%s)"
MATRIX_FAILED=0
KEEP_CONTAINERS=0
trap 'cleanup_exit; exec 1>&- 2>&-; wait "${LOG_TEE_PID:-}" 2>/dev/null' EXIT

banner "0. preflight — $ENGINE_TITLE version matrix"
info "source versions : $SRC_VERSIONS"
info "target versions : $DST_VERSIONS"
info "host address    : $HOST_IP (published ports)"
info "character set   : source ${SRC_CHARSET:-(server default)}/${SRC_COLLATION:-(server default)}  target ${DST_CHARSET:-(server default)}/${DST_COLLATION:-(server default)}"
info "access mode     : source $SRC_MODE -> target $DST_MODE   async: $ASYNC   skip version check: $SKIP_VERSION_CHECK"
[ -n "$ONLY_CELLS" ] && info "cells to run    : $ONLY_CELLS"
if [ "$SRC_MODE" = "ssh" ] || [ "$DST_MODE" = "ssh" ]; then
	command -v ssh-keygen >/dev/null 2>&1 || { fail "ssh-keygen is required for ssh access."; exit 1; }
	ensure_ssh_key || { fail "could not prepare the SSH key"; exit 1; }
fi
build_runner || exit 1
remove_containers

for SVER in $SRC_VERSIONS; do
	for DVER in $DST_VERSIONS; do
		KEY="$SVER:$DVER"
		if [ -n "$ONLY_CELLS" ] && ! grep -qw -- "$KEY" <<<"$ONLY_CELLS"; then
			CELL_STATUS["$KEY"]="SKIP"
			continue
		fi
		run_cell "$SVER" "$DVER"
		if [ "$STOP_ON_FAIL" = "1" ] && [ "${CELL_STATUS[$KEY]}" = "FAIL" ]; then
			warn "STOP_ON_FAIL=1 — stopping the matrix at the first failure."
			break 2
		fi
	done
done

print_matrix
write_result_json

if [ "${NO_LOG:-}" != "1" ]; then
	info "run log    : $LOG_FILE"
fi
[ "$MATRIX_FAILED" -eq 0 ] || exit 1
exit 0
