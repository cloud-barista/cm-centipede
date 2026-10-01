#!/usr/bin/env bash
# Matrix source init - MySQL: account, database, seed.
ENGINE_LABEL=MySQL
. /opt/matrix/scripts/common.sh

CHARSET="${MYSQL_SRC_CHARSET-utf8mb4}"
COLLATION="${MYSQL_SRC_COLLATION-utf8mb4_general_ci}"

log "starting setup (database=$SRC_DB charset=${CHARSET:-server default})"
wait_for "MySQL" mysqladmin ping --silent

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
mysql --default-character-set=utf8mb4 -u root <<SQL
CREATE USER IF NOT EXISTS '$SRC_DB_USER'@'%' IDENTIFIED BY '$SRC_DB_PASS';
GRANT ALL PRIVILEGES ON *.* TO '$SRC_DB_USER'@'%' WITH GRANT OPTION;
FLUSH PRIVILEGES;
SQL
log "account created ($SRC_DB_USER, remote); root is socket-only"

# Creating a function while the binary log is on requires either SUPER or this
# variable. It is deprecated and may be gone in a future release, so a failure
# here is not fatal: the seed then fails on its own and says why.
mysql -u root -e "SET GLOBAL log_bin_trust_function_creators = 1;" 2>/dev/null \
	|| log "note: log_bin_trust_function_creators is unavailable on this version"
mysql -u root -e "SET GLOBAL event_scheduler = ON;" 2>/dev/null \
	|| log "note: the event scheduler could not be enabled"

# The character set is stated rather than left to the server: it is the source
# axis of a charset regression, and an unknown collation must fail here, loudly,
# instead of being silently replaced.
CLAUSE=""
if [ -n "$CHARSET" ];   then CLAUSE=" CHARACTER SET $CHARSET"; fi
if [ -n "$COLLATION" ]; then CLAUSE="$CLAUSE COLLATE $COLLATION"; fi
mysql -u root -e "CREATE DATABASE \`$SRC_DB\`$CLAUSE;"
log "database created: $SRC_DB${CLAUSE:- (server default)}"

mysql --default-character-set=utf8mb4 -u root "$SRC_DB" < /opt/matrix/sql/seed-mysql.sql
log "seed loaded"
log "setup complete"
