#!/usr/bin/env bash
#
# lib/common.sh — shared utilities: logging, colour, version comparison, TLS modes
#
# Only what this folder needs is defined here, so the scripts stand on their own.

if [ -n "${MATRIX_COMMON_SH:-}" ]; then return 0; fi
MATRIX_COMMON_SH=1

C_HDR="\033[1;36m"; C_SUB="\033[1;33m"; C_OK="\033[0;32m"
C_ERR="\033[0;31m"; C_WARN="\033[0;33m"; C_OFF="\033[0m"

banner() { echo; echo -e "${C_HDR}========================================================================${C_OFF}"; echo -e "${C_HDR}  $*${C_OFF}"; echo -e "${C_HDR}========================================================================${C_OFF}"; }
sub()    { echo; echo -e "${C_SUB}--- $* ---${C_OFF}"; }
info()   { echo -e "  $*"; }
step()   { echo -e "  ${C_HDR}==>${C_OFF} $*"; }
ok()     { echo -e "  ${C_OK}$*${C_OFF}"; }
warn()   { echo -e "  ${C_WARN}$*${C_OFF}"; }
fail()   { echo -e "  ${C_ERR}$*${C_OFF}" >&2; }
die()    { fail "$*"; exit 1; }

# tty_usable — can /dev/tty actually be opened? It exists as a file in places
#   where it cannot be opened (a session-less container, a background run), so
#   checking for existence is not enough.
tty_usable() { : 2>/dev/null >/dev/tty; }

# pause — wait for a keypress. Skipped when NO_PAUSE=1 or there is no tty.
pause() {
	[ "${NO_PAUSE:-1}" = "1" ] && return 0
	tty_usable || return 0
	echo -e "  ${C_SUB}▶ paused — press any key to continue (Ctrl-C to stop)${C_OFF}" >/dev/tty 2>/dev/null || true
	read -r -n 1 -s _ </dev/tty 2>/dev/null || true
	echo >/dev/tty 2>/dev/null || true
}

# wait_enter MESSAGE — always waits for Enter, whatever NO_PAUSE says.
#   Used where nothing after it means anything until a person finishes something
#   in a console - the NCP public domain. With no tty at all (CI) there is no way
#   to wait, so it warns and moves on.
wait_enter() {
	if ! tty_usable; then
		warn "no tty, so the wait is skipped: $*"
		return 0
	fi
	echo -e "  ${C_SUB}▶ $*${C_OFF}" >/dev/tty 2>/dev/null || true
	echo -e "  ${C_SUB}  press Enter when ready (Ctrl-C to stop)${C_OFF}" >/dev/tty 2>/dev/null || true
	read -r _ </dev/tty 2>/dev/null || true
}

# version_gt A B — 0 when A is a higher version than B
version_gt() {
	[ "$1" = "$2" ] && return 1
	[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" = "$2" ]
}

# version_norm VERSION — normalise for comparison.
#
#   A CSP spells the same version differently in different places. NCP lists
#   "8.4.8" in its catalogue and reports "MYSQL8.4.8" for an instance built from
#   it. The engine name in front is not part of the version, so it comes off,
#   along with forms like NHN's "MYSQL_V8408" that add a separator and a V.
version_norm() {
	lower "$1" | sed -E 's/^(mysql|mariadb|postgresql|postgres)[[:space:]_-]*v?//'
}

# version_compatible REQUESTED RESOLVED — 0 when the requested version and the
#   resolved one name the same thing.
#
#   Compared component by component, split on dots. When every component of the
#   shorter one matches the leading components of the longer, they name the same
#   thing - "asked 8.4, NCP resolved 8.4.6" passes, "11.4 -> 10.6" does not.
#   A plain string prefix would make "8.4" match "8.40", hence per component.
version_compatible() {
	local req got IFS=.
	req="$(version_norm "$1")"
	got="$(version_norm "$2")"
	[ -z "$req" ] && return 0
	[ "$req" = "$got" ] && return 0

	local -a rp gp
	read -r -a rp <<< "$req"
	read -r -a gp <<< "$got"
	local n="${#rp[@]}"
	[ "${#gp[@]}" -lt "$n" ] && n="${#gp[@]}"
	[ "$n" -eq 0 ] && return 1

	local i
	for ((i = 0; i < n; i++)); do
		[ "${rp[$i]}" = "${gp[$i]}" ] || return 1
	done
	return 0
}

# secs_fmt N — seconds as "12s" / "1m03s"
secs_fmt() {
	local s="${1%.*}"
	[ -z "$s" ] && s=0
	if [ "$s" -lt 60 ]; then printf '%ds' "$s"; else printf '%dm%02ds' "$((s / 60))" "$((s % 60))"; fi
}

# urlq VALUE — percent-encoding for a query string or path segment
urlq() { jq -rn --arg v "${1:-}" '$v|@uri'; }

upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# csp_env CSP SUFFIX — the value of <CSP>_<SUFFIX> (empty when unset)
#   e.g. csp_env aws DB_PASSWORD -> $AWS_DB_PASSWORD
csp_env() {
	local name
	name="$(upper "$1")_${2}"
	printf '%s' "${!name:-}"
}

# csp_env_int / csp_env_bool — fall back to the default when empty or malformed
csp_env_int() {
	local v; v="$(csp_env "$1" "$2")"
	case "$v" in ''|*[!0-9]*) printf '%s' "$3" ;; *) printf '%s' "$v" ;; esac
}
csp_env_bool() {
	local v; v="$(lower "$(csp_env "$1" "$2")")"
	case "$v" in true|1|yes) printf 'true' ;; false|0|no) printf 'false' ;; *) printf '%s' "$3" ;; esac
}

# load_env_file PATH — read the env file, but let real shell variables win.
#   Precedence: CLI option > real shell variable > env file > script default
load_env_file() {
	local f="$1" preset
	[ -f "$f" ] || return 0
	# The snapshot is rewritten as export statements. bash's export -p prints
	# `declare -x`, and eval'ing that inside a function makes locals, which
	# restores nothing.
	preset="$(export -p | sed 's/^declare -x /export /')"
	set -a
	# shellcheck disable=SC1090
	. "$f"
	set +a
	eval "$preset" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Target TLS — TARGET_TLS_MODE
# ---------------------------------------------------------------------------
# The same `tlsMode` values transx-ex's DirectConfig.TLSMode takes. Everywhere
# the matrix connects to the target itself (creating and dropping cell databases,
# the creation probe, the PostgreSQL privilege query) uses the same value, so a
# column cannot migrate successfully and then fail its own preparation.
#
#   disable      plaintext
#   prefer       TLS if the server offers it, plaintext otherwise (the default)
#   require      TLS enforced, certificate not verified
#   verify-ca    chain verified
#   verify-full  chain + hostname verified
#
# ⚠ verify-ca / verify-full use the system trust store only. RDS and NCP sign
#   with their own CAs, so verification fails without their bundle —
#   assert_tls_mode in db-matrix.sh warns about it.
#
# It lives here because several places connect to the target and all of them have
# to use one value: the db.tlsMode centipede.sh puts in its request, and
# db-matrix.sh's pre-flight output and result JSON.
#
# ⚠ It does NOT govern creating the target database. That goes through cm-beetle
#   to cb-spider's SQL fallback, which chooses its own transport - see
#   explain_insecure_transport in beetle.sh.
TLS_MODES="disable prefer require verify-ca verify-full"

# MONGODB_PREFER_FALLBACK — what stands in for prefer on MongoDB.
#
#   MongoDB has no prefer: a client either negotiates TLS or it does not, with
#   nowhere to fall back to, and transx-ex's ValidateDBMS rejects it for the same
#   reason. Refusing a whole run because one engine in it is mongodb would stand
#   the mysql and postgresql columns still - a common case when one CSP is run
#   with several engines. So mongodb cells alone drop to this value.
MONGODB_PREFER_FALLBACK="disable"

# target_tls_mode [ENGINE] — the mode this connection actually uses. An empty
#   setting means disable, as it does in transx-ex.
#
#   Given an ENGINE, a value that engine cannot use is substituted - today that is
#   MongoDB's prefer alone, which drops to MONGODB_PREFER_FALLBACK. Called without
#   an engine it returns the configured value as written, so the pre-flight output
#   and the run-wide condition in the result JSON show what the user set.
target_tls_mode() {
	local mode
	mode="$(lower "${TARGET_TLS_MODE:-disable}")"
	if [ "$(lower "${1:-}")" = "mongodb" ] && [ "$mode" = "prefer" ]; then
		printf '%s' "$MONGODB_PREFER_FALLBACK"
		return 0
	fi
	printf '%s' "$mode"
}

# tls_mode_downgraded ENGINE — does this engine run at a different mode than configured?
tls_mode_downgraded() {
	[ "$(target_tls_mode "$1")" != "$(target_tls_mode)" ]
}

# require_cmd — check for the external commands we need
require_cmd() {
	local c
	for c in "$@"; do
		command -v "$c" >/dev/null 2>&1 || die "$c is required."
	done
}

# ---------------------------------------------------------------------------
# API log — how each honeybee and centipede call was made, and what came back
# ---------------------------------------------------------------------------
# Kept apart from the run log. That one is for a person following the run; this
# one is for reproducing a failure by copying a line out of it, which is why each
#
# entry is written as a curl command.
#
# Passwords and private keys travel in request bodies (honeybee stores the PEM
# itself, not a path to it), and beetle's logical-database calls carry the admin
# password in a header as well. All of them are masked before anything reaches
# the screen or the file.
#
# ⚠ A new key that carries a secret has to be added to API_MASK_FILTER below, and
#   a new header carrying one to _mask_header in beetle.sh. Neither masks by
#   pattern - an unlisted key is logged verbatim.

API_LOG_FILE="${API_LOG_FILE:-}"
QUIET_API_LOG=0   # 1 while the same call repeats, as in a progress poll

API_MASK_FILTER='
  walk(if type == "object"
       then with_entries(if (.key | IN("user","password","private_key","db_username","db_password",
                                       "os_access_key_id","os_secret_access_key","privateKey",
                                       "username","accessKeyId","secretAccessKey",
                                       "adminUserName","adminUserPassword"))
                            and (.value | type == "string") and (.value != "")
                         then .value = "..." else . end)
       else . end)
'

# mask_json — replace sensitive fields in stdin's JSON with ... Non-JSON passes through.
mask_json() {
	local in; in="$(cat)"
	[ -n "$in" ] || return 0
	printf '%s' "$in" | jq "$API_MASK_FILTER" 2>/dev/null || printf '%s\n' "$in"
}

# sq_escape TEXT — escape single quotes as '\'' (safe for shell quoting)
sq_escape() { printf '%s' "$1" | sed -e "s/'/'\\\\''/g"; }

# api_log METHOD PATH CMD CODE OUT_FILE
api_log() {
	[ -n "$API_LOG_FILE" ] || return 0
	[ "$QUIET_API_LOG" = "1" ] && return 0
	{
		echo "===== $(date '+%F %T')  $1 $2  → HTTP $4"
		printf '%s\n' "$3"
		echo "--- response ---"
		if [ -s "$5" ]; then mask_json < "$5"; else echo "(empty response)"; fi
		echo
	} >> "$API_LOG_FILE"
}

# log_run_header FILE TITLE [ARG…] — open one run's section in an accumulating log.
#
#   Runs append rather than replace one another. A matrix run costs real money
#   and takes minutes, so reading a failure against the run before it is the
#   normal way to work, and a log truncated at startup throws that away exactly
#   when it is wanted. The separator carries the timestamp and the command line
#   that produced what follows, which is what makes one long file navigable -
#   search for "=== run" to step between runs.
#
#   Nothing rotates these. A full matrix writes a few hundred KB, so the files
#   stay small enough to delete by hand when they stop being interesting.
log_run_header() {
	local file="$1" title="$2"; shift 2
	{
		echo
		echo "################################################################################"
		echo "=== run  $(date '+%F %T')  —  $title"
		echo "### command: $0 $*"
		echo "################################################################################"
		echo
	} >> "$file"
}

# api_log_init TITLE [ARG…] — append this run's section to the API call log.
api_log_init() {
	[ -n "$API_LOG_FILE" ] || return 0
	log_run_header "$API_LOG_FILE" "API call log — $1" "${@:2}"
}
