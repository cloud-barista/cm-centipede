#!/usr/bin/env bash
# Matrix source init - MariaDB: account, database, seed.
ENGINE_LABEL=MariaDB
. /opt/matrix/scripts/common.sh

CHARSET="${MARIADB_SRC_CHARSET-utf8mb4}"
COLLATION="${MARIADB_SRC_COLLATION-utf8mb4_general_ci}"

log "starting setup (database=$SRC_DB charset=${CHARSET:-server default})"
wait_for "MariaDB" mariadb-admin ping --silent

# One remote account, not two. root stays as the package left it - reachable
# only over the unix socket from inside the container, which is all a human
# debugging with `docker exec` needs. The DB port is published on the host, so a
# remote root with a known password would be open surface for nothing: every
# connection this matrix makes comes in as SRC_DB_USER.
#
# A fresh apt install authenticates root over the socket, so this needs no
# password. (That is also why DB_ROOT_PASS does not apply to root here; it is
# passed to the version query, which the socket ignores, and to MongoDB, where
# it really is the root password.)
mariadb --default-character-set=utf8mb4 -u root <<SQL
CREATE USER IF NOT EXISTS '$SRC_DB_USER'@'%' IDENTIFIED BY '$SRC_DB_PASS';
GRANT ALL PRIVILEGES ON *.* TO '$SRC_DB_USER'@'%' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL
log "account created ($SRC_DB_USER, remote); root is socket-only"

mariadb -u root -e "SET GLOBAL log_bin_trust_function_creators = 1;" 2>/dev/null \
	|| log "note: log_bin_trust_function_creators is unavailable on this version"
mariadb -u root -e "SET GLOBAL event_scheduler = ON;" 2>/dev/null \
	|| log "note: the event scheduler could not be enabled"

CLAUSE=""
if [ -n "$CHARSET" ];   then CLAUSE=" CHARACTER SET $CHARSET"; fi
if [ -n "$COLLATION" ]; then CLAUSE="$CLAUSE COLLATE $COLLATION"; fi
mariadb -u root -e "CREATE DATABASE \`$SRC_DB\`$CLAUSE;"
log "database created: $SRC_DB${CLAUSE:- (server default)}"

mariadb --default-character-set=utf8mb4 -u root "$SRC_DB" < /opt/matrix/sql/seed-mariadb.sql
log "seed loaded"
log "setup complete"
