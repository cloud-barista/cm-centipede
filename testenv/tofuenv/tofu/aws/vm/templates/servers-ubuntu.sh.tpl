#!/bin/bash
# AWS Ubuntu VM: install the selected servers natively (no Docker).
#   Rendered by tofu/aws/vm when TF_VAR_aws_vm_servers is not empty, copied over
#   SSH and run as root by terraform_data.servers. It deletes itself when it ends,
#   since it holds the password, and never prints it.
#
#   Idempotent: it runs again whenever the server list, a version or the account
#   changes, on the same VM. What is installed is kept (a changed version replaces
#   the binaries, not the data), and every account is created or reset.
#
#     mysql       3306   official tarball  /opt/mysql    cptf-mysql.service
#     mariadb     3307   official tarball  /opt/mariadb  cptf-mariadb.service
#     postgresql  5432   PGDG apt          postgresql.service
#     mongodb     27017  mongodb-org apt   mongod.service
#     minio       9000   GitHub release    cptf-minio.service  (console 9001)
#
#   mysql and mariadb run side by side, so each has its own basedir, datadir,
#   socket and option file, and starts with --defaults-file so neither reads
#   /etc/my.cnf. Every server listens on all interfaces, with a password.
#
#   CLI tools land on PATH as symlinks in /usr/local/bin: all of mysql's, and only
#   the mariadb-* names of mariadb's, so mysql/mysqldump always mean MySQL. apt
#   already puts psql/pg_dump and mongosh/mongodump in /usr/bin.
set -euo pipefail
trap 'rm -f "$0"' EXIT
umask 022
export DEBIAN_FRONTEND=noninteractive

SERVERS=" ${servers} "
SRV_USER='${username}'
SRV_PASS='${password}'
DB_NAME='${db_name}'
MINIO_BUCKET='${minio_bucket}'
MYSQL_VERSION='${mysql_version}'
MARIADB_VERSION='${mariadb_version}'
PG_MAJOR='${postgres_major}'
MONGO_SERIES='${mongodb_series}'
MINIO_RELEASE='${minio_release}'
MINIO_SHA256='${minio_sha256}'
TOOLS='${tools}'

log()  { echo "[cptf-servers] $*"; }
want() { [[ "$SERVERS" == *" $1 "* ]]; }

# retry <cmd...> — boot-time unattended-upgrades can hold the apt lock for minutes.
retry() {
    local n=0
    until "$@"; do
        n=$((n + 1))
        [ "$n" -lt 30 ] || return 1
        sleep 10
    done
}
apt_install() { retry bash -c 'apt-get update -y -q && apt-get install -y -q "$@"' _ "$@"; }

# download <out> <url>... — the first URL that answers wins.
download() {
    local out="$1" url
    shift
    for url in "$@"; do
        if curl -fsSL --retry 5 --retry-delay 5 -o "$out.part" "$url"; then
            mv "$out.part" "$out"
            return 0
        fi
    done
    rm -f "$out.part"
    log "download failed: $*"
    return 1
}

# wait_tcp <port> — until something accepts on 127.0.0.1:<port>.
wait_tcp() {
    local i
    for i in $(seq 1 90); do
        (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return 0
        sleep 2
    done
    log "nothing listens on port $1"
    return 1
}

write_unit() { # <name> <content>
    printf '%s\n' "$2" > "/etc/systemd/system/$1.service"
    systemctl daemon-reload
    systemctl enable "$1" >/dev/null 2>&1
}

# ------------------------------------------------------------------------------
# mysql / mariadb (tarballs)
# ------------------------------------------------------------------------------

# install_tarball <dest> <version-marker> <url>... — unpack a tarball's single top
#   directory into <dest>. Skipped when <dest> already holds that version.
install_tarball() {
    local dest="$1" ver="$2" tmp
    shift 2
    if [ -f "$dest/.cptf-version" ] && [ "$(cat "$dest/.cptf-version")" = "$ver" ]; then
        return 0
    fi
    tmp="$(mktemp -d)"
    download "$tmp/pkg.tar" "$@"
    mkdir -p "$tmp/x"
    tar -xf "$tmp/pkg.tar" -C "$tmp/x" --strip-components=1
    rm -rf "$dest"
    mv "$tmp/x" "$dest"
    echo "$ver" > "$dest/.cptf-version"
    rm -rf "$tmp"
}

# my_sql <client> <socket> — run stdin as root over the socket. root has no
#   password on the first run (initialize-insecure / install-db) and has
#   SRV_PASS on every run after it, so both are tried. The password goes through
#   an option file, never argv.
my_sql() {
    local client="$1" sock="$2" cnf sql rc=0
    cnf="$(mktemp)"; sql="$(mktemp)"
    chmod 600 "$cnf" "$sql"
    cat > "$sql"
    printf '[client]\nuser=root\npassword="%s"\nsocket=%s\n' "$SRV_PASS" "$sock" > "$cnf"
    if ! "$client" --defaults-file="$cnf" -e 'SELECT 1' >/dev/null 2>&1; then
        printf '[client]\nuser=root\nsocket=%s\n' "$sock" > "$cnf"
    fi
    "$client" --defaults-file="$cnf" < "$sql" || rc=$?
    rm -f "$cnf" "$sql"
    return "$rc"
}

account_sql() {
    cat <<SQL
ALTER USER 'root'@'localhost' IDENTIFIED BY '$SRV_PASS';
CREATE USER IF NOT EXISTS '$SRV_USER'@'%' IDENTIFIED BY '$SRV_PASS';
ALTER USER '$SRV_USER'@'%' IDENTIFIED BY '$SRV_PASS';
GRANT ALL PRIVILEGES ON *.* TO '$SRV_USER'@'%' WITH GRANT OPTION;
CREATE DATABASE IF NOT EXISTS $DB_NAME;
FLUSH PRIVILEGES;
SQL
}

# mariadb-install-db leaves anonymous accounts (''@localhost, ''@<host>) and an
# open test database, as mysql_secure_installation would remove. The anonymous
# localhost account also outranks '<user>'@'%' for local logins, which it denies.
mariadb_cleanup_sql() {
    cat <<SQL
DELETE FROM mysql.global_priv WHERE User = '';
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db = 'test' OR Db = 'test\\_%';
SQL
}

setup_mysql() {
    local base=/opt/mysql conf=/etc/cptf/mysql.cnf data=/var/lib/mysql-cptf
    local sock=/run/mysqld-cptf/mysqld.sock
    log "mysql $MYSQL_VERSION ..."
    # The -minimal tarball: the same server and clients without debug builds and
    # tests (66 MB instead of 900). The CDN first, as on windows: the archive
    # gateway on downloads.mysql.com filters by User-Agent and only redirects
    # there. archives/ keeps every release; Downloads/ only the current one.
    local series
    series="$(echo "$MYSQL_VERSION" | cut -d. -f1-2)"
    install_tarball "$base" "$MYSQL_VERSION" \
        "https://cdn.mysql.com/archives/mysql-$series/mysql-$MYSQL_VERSION-linux-glibc2.28-x86_64-minimal.tar.xz" \
        "https://cdn.mysql.com/Downloads/MySQL-$series/mysql-$MYSQL_VERSION-linux-glibc2.28-x86_64-minimal.tar.xz" \
        "https://downloads.mysql.com/archives/get/p/23/file/mysql-$MYSQL_VERSION-linux-glibc2.28-x86_64-minimal.tar.xz"

    mkdir -p /etc/cptf
    cat > "$conf" <<EOF
[mysqld]
user=mysql
basedir=$base
datadir=$data
socket=$sock
pid-file=/run/mysqld-cptf/mysqld.pid
port=3306
bind-address=0.0.0.0
mysqlx=OFF
# shop_db creates functions and triggers, and binary logging is on by default.
log_bin_trust_function_creators=1
EOF
    if [ ! -d "$data/mysql" ]; then
        # --initialize wants the datadir absent or empty.
        install -d -o mysql -g mysql "$data"
        "$base/bin/mysqld" --defaults-file="$conf" --initialize-insecure --user=mysql
    fi
    write_unit cptf-mysql "[Unit]
Description=MySQL $MYSQL_VERSION (cm-centipede test server, tarball)
After=network-online.target
Wants=network-online.target

[Service]
User=mysql
Group=mysql
RuntimeDirectory=mysqld-cptf
ExecStart=$base/bin/mysqld --defaults-file=$conf
LimitNOFILE=65536
TimeoutStartSec=600
Restart=on-failure

[Install]
WantedBy=multi-user.target"
    systemctl restart cptf-mysql
    wait_tcp 3306
    account_sql | my_sql "$base/bin/mysql" "$sock"
}

setup_mariadb() {
    local base=/opt/mariadb conf=/etc/cptf/mariadb.cnf data=/var/lib/mariadb-cptf
    local sock=/run/mariadb-cptf/mariadb.sock
    log "mariadb $MARIADB_VERSION ..."
    install_tarball "$base" "$MARIADB_VERSION" \
        "https://archive.mariadb.org/mariadb-$MARIADB_VERSION/bintar-linux-systemd-x86_64/mariadb-$MARIADB_VERSION-linux-systemd-x86_64.tar.gz"

    mkdir -p /etc/cptf
    cat > "$conf" <<EOF
[mysqld]
user=mysql
basedir=$base
datadir=$data
socket=$sock
pid-file=/run/mariadb-cptf/mariadb.pid
port=3307
bind-address=0.0.0.0
log_bin_trust_function_creators=1
EOF
    if [ ! -d "$data/mysql" ]; then
        install -d -o mysql -g mysql "$data"
        "$base/scripts/mariadb-install-db" --defaults-file="$conf" --basedir="$base" \
            --datadir="$data" --user=mysql >/dev/null
    fi
    write_unit cptf-mariadb "[Unit]
Description=MariaDB $MARIADB_VERSION (cm-centipede test server, tarball)
After=network-online.target
Wants=network-online.target

[Service]
User=mysql
Group=mysql
RuntimeDirectory=mariadb-cptf
ExecStart=$base/bin/mariadbd --defaults-file=$conf
LimitNOFILE=65536
TimeoutStartSec=600
Restart=on-failure

[Install]
WantedBy=multi-user.target"
    systemctl restart cptf-mariadb
    wait_tcp 3307
    { mariadb_cleanup_sql; account_sql; } | my_sql "$base/bin/mariadb" "$sock"
}

# ------------------------------------------------------------------------------
# postgresql (PGDG apt)
# ------------------------------------------------------------------------------
setup_postgresql() {
    local conf="/etc/postgresql/$PG_MAJOR/main"
    log "postgresql $PG_MAJOR ..."
    if [ ! -f /etc/apt/sources.list.d/pgdg.list ]; then
        install -d /usr/share/postgresql-common/pgdg
        download /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
            https://www.postgresql.org/media/keys/ACCC4CF8.asc
        echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt jammy-pgdg main" \
            > /etc/apt/sources.list.d/pgdg.list
    fi
    apt_install "postgresql-$PG_MAJOR"

    sed -i -E "s/^#?listen_addresses\s*=.*/listen_addresses = '*'/" "$conf/postgresql.conf"
    grep -q '^host all all 0.0.0.0/0 scram-sha-256' "$conf/pg_hba.conf" \
        || printf '%s\n' 'host all all 0.0.0.0/0 scram-sha-256' 'host all all ::/0 scram-sha-256' >> "$conf/pg_hba.conf"
    systemctl enable postgresql >/dev/null 2>&1
    systemctl restart postgresql
    wait_tcp 5432

    (cd / && runuser -u postgres -- psql -q -v ON_ERROR_STOP=1) <<SQL
DO \$\$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = '$SRV_USER') THEN
    ALTER ROLE $SRV_USER LOGIN SUPERUSER PASSWORD '$SRV_PASS';
  ELSE
    CREATE ROLE $SRV_USER LOGIN SUPERUSER PASSWORD '$SRV_PASS';
  END IF;
END
\$\$;
SQL
    if ! (cd / && runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'") | grep -q 1; then
        (cd / && runuser -u postgres -- createdb -O "$SRV_USER" "$DB_NAME")
    fi
}

# ------------------------------------------------------------------------------
# mongodb (mongodb-org apt: mongod, mongosh, database tools)
# ------------------------------------------------------------------------------
setup_mongodb() {
    local list="/etc/apt/sources.list.d/mongodb-org-$MONGO_SERIES.list"
    local key="/usr/share/keyrings/mongodb-server-$MONGO_SERIES.gpg"
    log "mongodb $MONGO_SERIES ..."
    if [ ! -f "$list" ]; then
        download /tmp/mongodb.asc "https://www.mongodb.org/static/pgp/server-$MONGO_SERIES.asc"
        gpg --batch --yes --dearmor -o "$key" /tmp/mongodb.asc
        rm -f /tmp/mongodb.asc
        echo "deb [ arch=amd64 signed-by=$key ] https://repo.mongodb.org/apt/ubuntu jammy/mongodb-org/$MONGO_SERIES multiverse" > "$list"
    fi
    apt_install mongodb-org
    systemctl enable mongod >/dev/null 2>&1
    systemctl restart mongod
    wait_tcp 27017

    # The root user first, while only localhost is bound; then authorization and
    # the public bind. Once authorization is on, the script logs in as the user
    # it is about to reset. The credentials reach mongosh through the
    # environment, not argv.
    local auth=0
    grep -q '^security:' /etc/mongod.conf && auth=1
    CPTF_AUTH="$auth" CPTF_U="$SRV_USER" CPTF_P="$SRV_PASS" mongosh --quiet --norc \
        "mongodb://127.0.0.1:27017/admin" --eval '
        const a = db.getSiblingDB("admin");
        if (process.env.CPTF_AUTH === "1") a.auth(process.env.CPTF_U, process.env.CPTF_P);
        if (a.getUser(process.env.CPTF_U)) a.updateUser(process.env.CPTF_U, { pwd: process.env.CPTF_P, roles: ["root"] });
        else a.createUser({ user: process.env.CPTF_U, pwd: process.env.CPTF_P, roles: ["root"] });' >/dev/null

    sed -i -E 's/^(\s*bindIp:).*/\1 0.0.0.0/' /etc/mongod.conf
    if [ "$auth" -eq 0 ]; then
        sed -i -E 's/^#?security:.*//' /etc/mongod.conf
        printf 'security:\n  authorization: enabled\n' >> /etc/mongod.conf
    fi
    systemctl restart mongod
    wait_tcp 27017
}

# ------------------------------------------------------------------------------
# minio (GitHub release binary)
# ------------------------------------------------------------------------------

# create_bucket — a SigV4-signed PUT from python3's standard library. Not
#   curl --aws-sigv4: the 7.81 that jammy ships signs it wrong (403
#   SignatureDoesNotMatch), and mc is not installed. 200 created, 409 exists.
create_bucket() {
    MINIO_ROOT_USER="$SRV_USER" MINIO_ROOT_PASSWORD="$SRV_PASS" python3 - "$MINIO_BUCKET" <<'PY'
import datetime, hashlib, hmac, os, sys, urllib.error, urllib.request

bucket = sys.argv[1]
ak, sk = os.environ["MINIO_ROOT_USER"], os.environ["MINIO_ROOT_PASSWORD"]
host, region = "127.0.0.1:9000", "us-east-1"
now = datetime.datetime.now(datetime.timezone.utc)
amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
payload = hashlib.sha256(b"").hexdigest()
signed = "host;x-amz-content-sha256;x-amz-date"
canon = "\n".join(["PUT", "/" + bucket, "", "host:" + host, "x-amz-content-sha256:" + payload,
                   "x-amz-date:" + amz, "", signed, payload])
scope = "/".join([day, region, "s3", "aws4_request"])
sts = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canon.encode()).hexdigest()])
key = ("AWS4" + sk).encode()
for part in (day, region, "s3", "aws4_request"):
    key = hmac.new(key, part.encode(), hashlib.sha256).digest()
sig = hmac.new(key, sts.encode(), hashlib.sha256).hexdigest()
req = urllib.request.Request("http://" + host + "/" + bucket, method="PUT", data=b"", headers={
    "x-amz-date": amz, "x-amz-content-sha256": payload,
    "Authorization": "AWS4-HMAC-SHA256 Credential=" + ak + "/" + scope
                     + ", SignedHeaders=" + signed + ", Signature=" + sig})
try:
    code = urllib.request.urlopen(req, timeout=30).status
except urllib.error.HTTPError as e:
    code = e.code
print("[cptf-servers] bucket " + bucket + ": HTTP " + str(code))
sys.exit(0 if code in (200, 409) else 1)
PY
}

setup_minio() {
    local bin=/usr/local/bin/minio data=/var/lib/minio-cptf env=/etc/cptf/minio.env
    log "minio $MINIO_RELEASE ..."
    if ! "$bin" --version 2>/dev/null | grep -qF "$MINIO_RELEASE"; then
        local url="https://github.com/minio/minio/releases/download/$MINIO_RELEASE/minio.linux-amd64.$MINIO_RELEASE"
        local sum="$MINIO_SHA256"
        download /tmp/minio "$url"
        if [ -z "$sum" ]; then
            sum="$(curl -fsSL --retry 5 "$url.sha256sum" | cut -d' ' -f1)"
        fi
        echo "$sum  /tmp/minio" | sha256sum -c - >/dev/null
        install -m 0755 /tmp/minio "$bin"
        rm -f /tmp/minio
    fi
    id minio-user >/dev/null 2>&1 || useradd -r -M -s /usr/sbin/nologin minio-user
    install -d -o minio-user -g minio-user "$data"
    mkdir -p /etc/cptf
    install -m 0600 /dev/null "$env"
    printf 'MINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\nMINIO_VOLUMES=%s\nMINIO_OPTS=--address :9000 --console-address :9001\n' \
        "$SRV_USER" "$SRV_PASS" "$data" > "$env"
    write_unit cptf-minio '[Unit]
Description=MinIO (cm-centipede test server)
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=minio-user
Group=minio-user
EnvironmentFile=/etc/cptf/minio.env
ExecStart=/usr/local/bin/minio server $MINIO_VOLUMES $MINIO_OPTS
LimitNOFILE=65536
Restart=on-failure

[Install]
WantedBy=multi-user.target'
    systemctl restart cptf-minio
    local i
    for i in $(seq 1 60); do
        curl -sf http://127.0.0.1:9000/minio/health/ready >/dev/null && break
        sleep 2
    done
    create_bucket
}

# ------------------------------------------------------------------------------
# PATH
# ------------------------------------------------------------------------------
link_tools() {
    local f
    if want mysql; then
        for f in /opt/mysql/bin/*; do ln -sfn "$f" "/usr/local/bin/$(basename "$f")"; done
    fi
    if want mariadb; then
        for f in /opt/mariadb/bin/mariadb*; do ln -sfn "$f" "/usr/local/bin/$(basename "$f")"; done
    fi
    for f in $TOOLS; do
        command -v "$f" >/dev/null || { log "missing on PATH: $f"; return 1; }
    done
}

# ------------------------------------------------------------------------------
log "servers:$SERVERS"
apt_install curl ca-certificates gnupg xz-utils python3 libaio1 libnuma1 libncurses5 libtinfo5
if want mysql || want mariadb; then
    id mysql >/dev/null 2>&1 || useradd -r -M -s /usr/sbin/nologin mysql
fi
want mysql      && setup_mysql
want mariadb    && setup_mariadb
want postgresql && setup_postgresql
want mongodb    && setup_mongodb
want minio      && setup_minio
link_tools
log "done"
