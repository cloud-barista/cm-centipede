#!/usr/bin/env bash
#
# lib/common.sh — shared utilities: logging, colour, env loading, the API log
#
# Only what this folder needs is defined here, so the scripts stand on their own.
# It is beetle-os-test's common.sh unchanged apart from the masking filter, which
# gains the SSH private key: this folder generates one per run and hands it to
# cm-honeybee in a connection_info body, so it travels through the API log.

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

# in_list WANT "A B C" — 0 when WANT is one of the whitespace-separated words
in_list() {
	local want="$1" e
	for e in $2; do [ "$e" = "$want" ] && return 0; done
	return 1
}

# csp_env CSP SUFFIX — the value of <CSP>_<SUFFIX> (empty when unset)
#   e.g. csp_env aws REGION -> $AWS_REGION
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

# require_cmd — check for the external commands we need
require_cmd() {
	local c
	for c in "$@"; do
		command -v "$c" >/dev/null 2>&1 || die "$c is required."
	done
}

# ---------------------------------------------------------------------------
# API log — how each beetle, honeybee and centipede call was made, and what came back
# ---------------------------------------------------------------------------
# Kept apart from the run log. That one is for a person following the run; this
# one is for reproducing a failure by copying a line out of it, which is why each
# entry is written as a curl command.
#
# The SSH private key travels in honeybee's connection_info body, so it is masked
# before anything reaches the screen or the file. It is the one real secret this
# folder handles: it opens root on the source container, and cm-honeybee hands it
# back out again (RSA-encrypted) for cm-centipede to rsync and checksum with.
#
# ⚠ A new key that carries a secret has to be added to API_MASK_FILTER below.
#   It does not mask by pattern — an unlisted key is logged verbatim.

API_LOG_FILE="${API_LOG_FILE:-}"
QUIET_API_LOG=0   # 1 while the same call repeats, as in a progress poll

API_MASK_FILTER='
  walk(if type == "object"
       then with_entries(if (.key | IN("user","password","private_key","public_key",
                                       "privateKey","publicKey",
                                       "os_access_key_id","os_secret_access_key",
                                       "accessKey","secretKey",
                                       "accessKeyId","secretAccessKey"))
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
#   and minutes, so reading a failure against the run before it is the normal way
#   to work, and a log truncated at startup throws that away exactly when it is
#   wanted. The separator carries the timestamp and the command line that
#   produced what follows, which is what makes one long file navigable — search
#   for "=== run" to step between runs.
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
