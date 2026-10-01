#!/usr/bin/env bash
#
# mongodb-ver-matrix-migration.sh — MongoDB version matrix migration (transx-ex called directly)
#
# Runs every combination of the source version list x the target version list,
# one cell at a time.
#   e.g. source=[6.0 7.0], target=[6.0 7.0] -> 6.0->6.0, 6.0->7.0, 7.0->6.0, 7.0->7.0 (4 cells)
#
# What one cell does:
#   1) prepare the official image (MODE=ssh builds an sshd image once per version)
#   2) start fresh source/target containers and wait until the DB answers queries
#   3) seed the source with a minimal data set (mongosh script embedded below —
#      collections/view/indexes/validator + UTF-8 verification documents)
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
# WARNING: MongoDB needs the same access mode on both sides. direct produces JSON
# and ssh produces a mongodump archive, and neither can restore the other, so a mixed
# pair is refused during preflight (transx-ex rejects it in Validate as well).
#
# With both sides on ssh, transx-ex pipes mongodump -> mongorestore without writing an
# archive file; with both on direct it stages through local JSON files.
#
# The matrix always migrates a whole database, so there is no scope option.
#
# Requirements:
#   - a running docker daemon, go 1.26+, jq
#   - internet access (pulls the official image; MODE=ssh also builds a derived image)
#   - no centipede/honeybee server — transx-ex is called as a library
#
# Usage:
#   ./transx-ex/examples/db-ver-matrix/mongodb-ver-matrix-migration.sh
#   ./mongodb-ver-matrix-migration.sh --src-versions "6.0" --dst-versions "6.0 7.0"
#   ./mongodb-ver-matrix-migration.sh --only 7.0:6.0 --skip-version-check
#   ./mongodb-ver-matrix-migration.sh --mode ssh
#   MODE=ssh ./mongodb-ver-matrix-migration.sh
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
ENGINE="mongodb"
ENGINE_TITLE="MongoDB"
IMAGE_REPO="mongo"
SSH_IMAGE_REPO="transxex-vermatrix-mongodb-ssh"
INTERNAL_DB_PORT=27017
DB_ADMIN_USER="root"
SRC_CONTAINER="transxex-vermatrix-mongodb-src"
DST_CONTAINER="transxex-vermatrix-mongodb-dst"

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

SRC_VERSIONS="${MONGODB_SRC_VERSIONS:-6.0 7.0 8.0}"
DST_VERSIONS="${MONGODB_DST_VERSIONS:-6.0 7.0 8.0}"

# HOST_IP — address used to reach the published container ports.
#   Override it only when Docker is not reachable at 127.0.0.1 from this host.
HOST_IP="${HOST_IP:-127.0.0.1}"

DB_ROOT_PASS="${DB_ROOT_PASS:-testpass123}"
# MONGO_AUTH_SOURCE — the authentication database the admin account is created in
MONGO_AUTH_SOURCE="${MONGODB_AUTH_SOURCE:-admin}"
SRC_DB="${SRC_DB:-matrix_db}"
DST_DB="${DST_DB:-matrix_db}"

# MongoDB is UTF-8 only, so there is no character set to choose — only the default
# collation locale of a collection. Empty means none at all (binary ordering).
#   An empty target value falls back to the source value.
SRC_CHARSET=""
DST_CHARSET=""
SRC_COLLATION="${MONGODB_SRC_COLLATION_LOCALE-}"
DST_COLLATION="${MONGODB_DST_COLLATION_LOCALE:-$SRC_COLLATION}"

# Compare the collation snapshots only when both sides were configured the
# same way. A deliberate difference is reported but not treated as a mismatch.
CHARSET_COMPARE=0
[ "$SRC_CHARSET" = "$DST_CHARSET" ] && [ "$SRC_COLLATION" = "$DST_COLLATION" ] && CHARSET_COMPARE=1

SRC_DB_PORT="${MONGODB_SRC_DB_PORT:-45017}"
DST_DB_PORT="${MONGODB_DST_DB_PORT:-45018}"
SRC_SSH_PORT="${MONGODB_SRC_SSH_PORT:-45260}"
DST_SSH_PORT="${MONGODB_DST_SSH_PORT:-45261}"

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
  --only "SRC:DST ..."         run only these cells (e.g. --only "6.0:7.0")
  --mode direct|ssh            access mode for both sides (default: $MODE)
  --src-mode direct|ssh        access mode for the source only
  --dst-mode direct|ssh        access mode for the target only
  --skip-version-check         attempt the transfer even for a downgrade
  --async                      run asynchronously and poll progress
  --keep-on-fail               keep the containers of a failed cell
  --stop-on-fail               stop at the first FAIL
  -h, --help                   this help

Supported versions (official images): 6.0  7.0  8.0
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
# ssh-tunnel mode runs mongodump/mongorestore on the remote host.
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
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-server \
    && (command -v mongodump >/dev/null 2>&1 \
        || apt-get install -y --no-install-recommends mongodb-database-tools) \
    && rm -rf /var/lib/apt/lists/* \
    && ssh-keygen -A \
    && mkdir -p /run/sshd /root/.ssh \
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

	# The SSH port is published only when that side uses ssh; the two sides may differ.
	local ports=(-p "$db_port:$INTERNAL_DB_PORT")
	[ "$mode" = "ssh" ] && ports+=(-p "$ssh_port:22")

	# MongoDB creates a database on first write, so there is no empty target database
	# to prepare here. Only the admin account comes from an init variable.
	docker run -d --name "$name" \
		-e MONGO_INITDB_ROOT_USERNAME="$DB_ADMIN_USER" \
		-e MONGO_INITDB_ROOT_PASSWORD="$DB_ROOT_PASS" \
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
		if docker exec "$name" mongosh --quiet \
			--host 127.0.0.1 --port "$INTERNAL_DB_PORT" \
			-u "$DB_ADMIN_USER" -p "$DB_ROOT_PASS" --authenticationDatabase "$MONGO_AUTH_SOURCE" \
			--eval "db.adminCommand({ping:1}).ok" >/dev/null 2>&1 \
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

# mongosh_run CONTAINER DB SCRIPT_PATH — run a script with mongosh inside the container
mongosh_run() {
	docker exec "$1" mongosh --quiet \
		--host 127.0.0.1 --port "$INTERNAL_DB_PORT" \
		-u "$DB_ADMIN_USER" -p "$DB_ROOT_PASS" --authenticationDatabase "$MONGO_AUTH_SOURCE" \
		"$2" "$3"
}

# create_database ROLE — nothing to create for MongoDB.
#   A database appears on the first write, and the transx-ex CheckTargetEmpty step
#   treats "no collections" as empty, so there is nothing to prepare up front.
create_database() {
	local role="$1"
	if [ "$role" = "src" ]; then
		info "src database: $SRC_DB (created on first write — collation ${SRC_COLLATION:-none})"
	else
		info "dst database: $DST_DB (created on first write — collation ${DST_COLLATION:-none})"
	fi
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
#   4 collections / 1 view / 2 indexes / 1 validator
#   + UTF-8 verification documents (Korean, Japanese, emoji)
# ---------------------------------------------------------------------------
seed_source() {
	# Write the collation constant first, so the seed below can stay a fixed script.
	local coll_js="null"
	[ -n "$SRC_COLLATION" ] && coll_js="{ locale: \"$SRC_COLLATION\" }"
	docker exec -i "$SRC_CONTAINER" sh -c "cat > /tmp/matrix-seed.js" <<JS_PRELUDE
const COLLATION = $coll_js;
function withCollation(opts) {
  return COLLATION ? Object.assign({}, opts, { collation: COLLATION }) : opts;
}
JS_PRELUDE
	docker exec -i "$SRC_CONTAINER" sh -c 'cat >> /tmp/matrix-seed.js' <<'SEED_JS'
db.createCollection("customers", withCollation({
  validator: {
    $jsonSchema: {
      bsonType: "object",
      required: ["name", "email"],
      properties: {
        name:  { bsonType: "string" },
        email: { bsonType: "string" }
      }
    }
  }
}));
db.createCollection("products",    withCollation({}));
db.createCollection("orders",      withCollation({}));
db.createCollection("order_items", withCollation({}));

db.customers.insertMany([
  { _id: 1, name: "Alice Kim",    email: "alice@example.com",   grade: "gold" },
  { _id: 2, name: "Bob Lee",      email: "bob@example.com",     grade: "silver" },
  { _id: 3, name: "Charlie Park", email: "charlie@example.com", grade: "bronze" },
  { _id: 4, name: "김철수 (한글)",  email: "utf8-ko@example.com", grade: "gold" },
  { _id: 5, name: "佐藤 太郎 🎌",   email: "utf8-ja@example.com", grade: "silver" }
]);

db.products.insertMany([
  { _id: 1, name: "Laptop Pro",     price: 1299.99, stock: 50 },
  { _id: 2, name: "Wireless Mouse", price: 29.99,   stock: 200 },
  { _id: 3, name: "USB-C Hub",      price: 49.99,   stock: 150 },
  { _id: 4, name: "4K Monitor",     price: 399.99,  stock: 30 },
  { _id: 5, name: "노트북 거치대 🖥",  price: 39.99,   stock: 120 }
]);

db.orders.insertMany([
  { _id: 1, customer_id: 1, status: "confirmed" },
  { _id: 2, customer_id: 2, status: "shipped" },
  { _id: 3, customer_id: 3, status: "pending" },
  { _id: 4, customer_id: 4, status: "confirmed" },
  { _id: 5, customer_id: 5, status: "shipped" }
]);

db.order_items.insertMany([
  { _id: 1, order_id: 1, product_id: 1, qty: 1, unit_price: 1299.99 },
  { _id: 2, order_id: 1, product_id: 2, qty: 2, unit_price: 29.99 },
  { _id: 3, order_id: 2, product_id: 3, qty: 1, unit_price: 49.99 },
  { _id: 4, order_id: 3, product_id: 4, qty: 1, unit_price: 399.99 },
  { _id: 5, order_id: 4, product_id: 5, qty: 2, unit_price: 39.99 },
  { _id: 6, order_id: 4, product_id: 2, qty: 1, unit_price: 29.99 },
  { _id: 7, order_id: 5, product_id: 1, qty: 1, unit_price: 1299.99 },
  { _id: 8, order_id: 5, product_id: 3, qty: 3, unit_price: 49.99 }
]);

db.orders.createIndex({ customer_id: 1 });
db.order_items.createIndex({ order_id: 1, product_id: 1 });

db.createView("v_order_summary", "orders", [
  { $group: { _id: "$customer_id", order_count: { $sum: 1 } } }
]);
SEED_JS
	mongosh_run "$SRC_CONTAINER" "$SRC_DB" /tmp/matrix-seed.js 2>&1 | sed 's/^/      /'
}

# ---------------------------------------------------------------------------
# Snapshots — normalised strings compared between source and target
#   schema : object counts  /  data : document counts + UTF-8 values
# ---------------------------------------------------------------------------
snapshot_schema() {
	local name="$1" db="$2"
	docker exec -i "$name" sh -c 'cat > /tmp/matrix-snap-schema.js' <<'SNAP_JS'
const infos = db.getCollectionInfos().filter(i => !i.name.startsWith("system."));
const colls = infos.filter(i => i.type === "collection").map(i => i.name).sort();
const views = infos.filter(i => i.type === "view").map(i => i.name).sort();
let indexes = 0;
colls.forEach(c => { indexes += db.getCollection(c).getIndexes().length; });
const validators = infos.filter(i => i.options && i.options.validator).length;
print("collections=" + colls.length + " views=" + views.length +
      " indexes=" + indexes + " validators=" + validators +
      " names=" + colls.join(",") + "|" + views.join(","));
SNAP_JS
	mongosh_run "$name" "$db" /tmp/matrix-snap-schema.js 2>/dev/null | tr -d '\r' | tr -d '\n'
}

# snapshot_charset — the default collation locale of each collection
snapshot_charset() {
	local name="$1" db="$2"
	docker exec -i "$name" sh -c 'cat > /tmp/matrix-snap-collation.js' <<'SNAP_JS'
const infos = db.getCollectionInfos()
  .filter(i => i.type === "collection" && !i.name.startsWith("system."))
  .sort((a, b) => a.name.localeCompare(b.name));
print("collation=" + infos
  .map(i => i.name + ":" + (i.options && i.options.collation ? i.options.collation.locale : "-"))
  .join(","));
SNAP_JS
	mongosh_run "$name" "$db" /tmp/matrix-snap-collation.js 2>/dev/null | tr -d '\r' | tr -d '\n'
}

snapshot_data() {
	local name="$1" db="$2"
	docker exec -i "$name" sh -c 'cat > /tmp/matrix-snap-data.js' <<'SNAP_JS'
const colls = db.getCollectionInfos()
  .filter(i => i.type === "collection" && !i.name.startsWith("system."))
  .map(i => i.name).sort();
const counts = colls.map(c => db.getCollection(c).countDocuments({})).join("/");
const u = db.customers.findOne({ _id: 4 });
const p = db.products.findOne({ _id: 5 });
const agg = db.order_items.aggregate([
  { $group: { _id: null, total: { $sum: { $multiply: ["$qty", "$unit_price"] } } } }
]).toArray();
print("rows=" + counts +
      " utf8=" + (u ? u.name : "-") + "|" + (p ? p.name : "-") +
      " sum=" + (agg.length ? agg[0].total.toFixed(2) : "0.00"));
SNAP_JS
	mongosh_run "$name" "$db" /tmp/matrix-snap-data.js 2>/dev/null | tr -d '\r' | tr -d '\n'
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
			--arg u "$DB_ADMIN_USER" --arg pw "$DB_ROOT_PASS" --arg auth "$MONGO_AUTH_SOURCE" \
			--argjson sport "$ssh_port" --argjson iport "$INTERNAL_DB_PORT" '
			{dbmsType:$t, database:$db, providerName:"onprem", accessType:"ssh-tunnel",
			 sshTunnel:{ssh:{host:$host, port:$sport, username:"root", privateKeyPath:$key},
			            dbHost:"127.0.0.1", dbPort:$iport, username:$u, password:$pw,
			            authSource:$auth}}'
	else
		jq -n --arg t "$ENGINE" --arg db "$db" --arg host "$HOST_IP" \
			--arg u "$DB_ADMIN_USER" --arg pw "$DB_ROOT_PASS" --arg auth "$MONGO_AUTH_SOURCE" \
			--argjson port "$db_port" '
			{dbmsType:$t, database:$db, providerName:"onprem", accessType:"direct",
			 direct:{host:$host, port:$port, username:$u, password:$pw, authSource:$auth}}'
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

	sub "2) prepare the databases"
	create_database src || { finish_cell "$key" FAIL 0 "source database not prepared"; cleanup_cell 1; return; }
	create_database dst || { finish_cell "$key" FAIL 0 "target database not prepared"; cleanup_cell 1; return; }

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
		detail="collation mismatch — src[$src_charset] dst[$dst_charset]"
	fi
	# A difference the operator asked for is intentional, so it is not compared.
	[ "$CHARSET_COMPARE" = "1" ] || warn "collation comparison skipped — source(${SRC_COLLATION:-none}) != target(${DST_COLLATION:-none})"

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
info "collation       : source ${SRC_COLLATION:-(none)}  target ${DST_COLLATION:-(none)}"
info "access mode     : source $SRC_MODE -> target $DST_MODE   async: $ASYNC   skip version check: $SKIP_VERSION_CHECK"
[ -n "$ONLY_CELLS" ] && info "cells to run    : $ONLY_CELLS"
# MongoDB needs both sides on the same access mode: direct stages JSON while
# ssh-tunnel produces a mongodump archive, and neither can restore the other.
# transx-ex refuses the combination in Validate (SPEC appendix D).
if [ "$SRC_MODE" != "$DST_MODE" ]; then
	fail "MongoDB cannot mix access modes (source $SRC_MODE, target $DST_MODE)."
	fail "direct produces JSON and ssh produces an archive; neither restores the other."
	fail "Set both sides the same way, e.g. --mode direct or --mode ssh."
	exit 1
fi
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
