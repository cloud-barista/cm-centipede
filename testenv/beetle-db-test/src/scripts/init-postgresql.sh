#!/usr/bin/env bash
# Matrix source init - PostgreSQL: account, database, seed.
ENGINE_LABEL=PostgreSQL
. /opt/matrix/scripts/common.sh

ENCODING="${POSTGRESQL_SRC_ENCODING-UTF8}"
LOCALE="${POSTGRESQL_SRC_LOCALE-}"

log "starting setup (database=$SRC_DB encoding=${ENCODING:-cluster default})"
wait_for "PostgreSQL" pg_isready -q

# A fresh apt install trusts local peer connections, so the postgres role needs
# no password yet. The app account is a superuser because the source side has to
# create a database, load routines and read everything back.
sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
ALTER USER postgres WITH PASSWORD '$DB_ROOT_PASS';
CREATE USER $SRC_DB_USER WITH PASSWORD '$SRC_DB_PASS' SUPERUSER CREATEDB CREATEROLE;
SQL
log "accounts created (postgres, $SRC_DB_USER)"

# Encoding and locale are stated rather than inherited: they are the source axis
# of a charset regression. Naming either one forces template0, because a new
# database may not differ from the template it copies.
CLAUSE=""
if [ -n "$ENCODING" ]; then CLAUSE=" ENCODING '$ENCODING'"; fi
if [ -n "$LOCALE" ];   then CLAUSE="$CLAUSE LC_COLLATE '$LOCALE' LC_CTYPE '$LOCALE'"; fi
if [ -n "$CLAUSE" ];   then CLAUSE="$CLAUSE TEMPLATE template0"; fi
sudo -u postgres psql -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$SRC_DB\" OWNER $SRC_DB_USER$CLAUSE;"
log "database created: $SRC_DB${CLAUSE:- (cluster default)}"

# SET ROLE before the file, so the objects belong to the account the migration
# will connect as - the same shape a real source has. psql runs -c and -f in the
# order they are given, inside one session, which is what makes this work over
# the local socket without needing a password for the app account.
sudo -u postgres psql -v ON_ERROR_STOP=1 -q -d "$SRC_DB" \
	-c "SET ROLE $SRC_DB_USER;" \
	-f /opt/matrix/sql/seed-postgresql.sql
log "seed loaded"
log "setup complete"
