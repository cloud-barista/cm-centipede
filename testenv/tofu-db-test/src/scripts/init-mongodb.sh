#!/usr/bin/env bash
# Matrix source init - MongoDB: accounts, database, seed.
ENGINE_LABEL=MongoDB
. /opt/matrix/scripts/common.sh

COLLATION_LOCALE="${MONGODB_SRC_COLLATION_LOCALE-}"

log "starting setup (database=$SRC_DB collation=${COLLATION_LOCALE:-none})"
# Authorization is on from the first boot, but MongoDB's localhost exception
# still allows creating the first user. Everything after that needs credentials.
wait_for "MongoDB" mongosh --quiet --eval 'db.adminCommand({ping:1})'

mongosh --quiet admin --eval \
	"db.createUser({ user: 'root', pwd: '$DB_ROOT_PASS', roles: [ { role: 'root', db: 'admin' } ] })" >/dev/null
log "root account created"

# Two accounts for one user, on purpose. transx-ex builds
# mongodb://.../<db>?authSource=..., and authSource defaults to "admin" - so the
# admin account is the one that authenticates. The second, inside the source
# database, covers a connection that names that database as its authSource
# instead. Both roles point at admin so the account can list databases, which is
# what honeybee's collection step does first.
cat > /tmp/matrix-users.js <<JS
db.createUser({ user: "$SRC_DB_USER", pwd: "$SRC_DB_PASS", roles: [ { role: "root", db: "admin" } ] });
db.getSiblingDB("$SRC_DB").createUser({
  user: "$SRC_DB_USER", pwd: "$SRC_DB_PASS", roles: [ { role: "root", db: "admin" } ]
});
JS
mongosh --quiet -u root -p "$DB_ROOT_PASS" --authenticationDatabase admin \
	admin /tmp/matrix-users.js >/dev/null
log "app account created in admin and $SRC_DB ($SRC_DB_USER)"

# The prelude carries the one value that is substituted; the seed file itself
# stays literal. A MongoDB database begins to exist on its first write, so there
# is no CREATE DATABASE step here.
COLLATION_JS="null"
if [ -n "$COLLATION_LOCALE" ]; then
	COLLATION_JS="{ locale: \"$COLLATION_LOCALE\" }"
fi
cat > /tmp/matrix-seed-prelude.js <<JS
const COLLATION = $COLLATION_JS;
function withCollation(opts) {
  return COLLATION ? Object.assign({}, opts, { collation: COLLATION }) : opts;
}
JS
# Concatenated into one file and passed as a file argument, not piped. Reading a
# script from stdin puts mongosh in its interactive shell, which echoes a prompt
# and the return value of every statement into the log this container keeps.
cat /tmp/matrix-seed-prelude.js /opt/matrix/sql/seed-mongodb.js > /tmp/matrix-seed.js
mongosh --quiet -u root -p "$DB_ROOT_PASS" --authenticationDatabase admin \
	"$SRC_DB" /tmp/matrix-seed.js
log "seed loaded"
log "setup complete"
