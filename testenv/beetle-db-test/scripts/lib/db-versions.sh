#!/usr/bin/env bash
#
# lib/db-versions.sh — list the managed DB engine versions a CSP actually offers (body)
#
# The entry point (scripts/<csp>-db-versions.sh) sets the CSP and calls db_versions_main.
#
# Why ask at all:
#   The supported-version list exists nowhere in this repository. The CSP answers
#   it live through cb-spider (AWS DescribeDBEngineVersions, NCP
#   GetCloudMysqlImageProductList), and the answer varies by region and by date,
#   so whether the target versions in .env are still valid today is a question
#   that has to be asked. This is a read-only lookup and creates nothing.
#
# ⚠ Asked of cb-tumblebug, not of cm-beetle. beetle's own capability endpoint
#   calls GetRDBMSCapability(connectionName) and the client inside pins dbEngine
#   to "mysql" (pkg/client/tumblebug/rdbms.go), so mariadb's list cannot come out
#   of it. This is one of the three places this folder addresses tumblebug
#   directly - see the header of lib/beetle.sh.
#
# ⚠ What is listed is what the CSP offers, not what cm-beetle can build. beetle
#   creates only the engines in BEETLE_ENGINES_<CSP>, so a version listed for an
#   engine it cannot create is information, not an option.
#
# ⚠ These are TARGET (managed) versions. Source versions have nothing to do with
#   this list: they are decided by what the vendor's apt repository serves for
#   jammy, and that table is in .env.example.

if [ -n "${MATRIX_DB_VERSIONS_SH:-}" ]; then return 0; fi
MATRIX_DB_VERSIONS_SH=1

DV_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$DV_LIB_DIR/common.sh"
# shellcheck source=./beetle.sh
. "$DV_LIB_DIR/beetle.sh"

DV_ROOT="$(cd "$DV_LIB_DIR/../.." && pwd)"

dv_usage() {
	cat <<USAGE
$(upper "$CSP") managed DB supported versions — usage

  $(basename "$0") [options]

Options:
  --write        update $(upper "$CSP")_<ENGINE>_DST_VERSIONS in .env with the result
  -h, --help     this help

A read-only lookup that creates nothing. --write changes only the
*_DST_VERSIONS lines, and keeps the file it replaced as <file>.bak.
USAGE
}

# set_env_key FILE KEY VALUE — replace the line when present, append when not.
dv_set_env_key() {
	local file="$1" key="$2" value="$3" tmp
	tmp="$MATRIX_TMP/env.$$"
	if grep -q "^${key}=" "$file"; then
		awk -v k="$key" -v v="$value" '
			$0 ~ "^" k "=" { print k "=\"" v "\""; next }
			{ print }' "$file" > "$tmp" && mv "$tmp" "$file"
	else
		printf '%s="%s"\n' "$key" "$value" >> "$file"
	fi
}

db_versions_main() {
	local WRITE=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--write)   WRITE=1; shift ;;
		-h|--help) dv_usage; exit 0 ;;
		*)         die "unknown option: $1" ;;
		esac
	done

	MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cpbdb}"
	MATRIX_NS="${MATRIX_NS:-cpbdb01}"
	BEETLE_URL="${BEETLE_URL:-http://localhost:8056/beetle}"
	TUMBLEBUG_URL="${TUMBLEBUG_URL:-http://localhost:1323/tumblebug}"

	require_cmd jq curl
	MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cpbdb-ver.XXXXXX")"
	trap 'rm -rf "$MATRIX_TMP"' EXIT

	local region
	region="$(csp_env "$CSP" REGION)"
	[ -n "$region" ] || die "$(upper "$CSP")_REGION is empty ($ENV_FILE)."

	if [ "$WRITE" = "1" ]; then
		[ -f "$ENV_FILE" ] || die ".env does not exist. cp .env.example .env && chmod 600 .env"
		cp "$ENV_FILE" "$ENV_FILE.bak"
		chmod 600 "$ENV_FILE.bak" 2>/dev/null || true
		info "original backed up: $ENV_FILE.bak"
	fi

	banner "$(upper "$CSP") managed DB supported versions — cb-tumblebug catalogue (creates nothing)"
	beetle_preflight
	assert_connection "$CSP" || die "connection check failed."

	# Every engine this CSP offers as managed is walked, not just <CSP>_ENGINES.
	# This script exists to fill in that list and *_DST_VERSIONS, so filtering by a
	# value not yet filled in would hide the versions of the very engine being added.
	# It costs no more: the lookup runs once per CSP and is cached, so the engine
	# loop only reads that JSON.
	local engines beetleable configured engine mark versions key current
	local changed=0 missing=0
	engines="$(csp_engines "$CSP")"
	beetleable=" $(beetle_engines "$CSP") "
	configured=" $(csp_env "$CSP" ENGINES) "

	sub "$CSP — region $region   connection $(connection_name "$CSP")"
	info "offered as managed by $(upper "$CSP") : $engines"
	info "creatable by cm-beetle       : $(beetle_engines "$CSP")"
	info "* = currently in $(upper "$CSP")_ENGINES (what the matrix actually runs)"
	info "- = cm-beetle cannot create this engine here, so it cannot be a target yet"

	for engine in $engines; do
		mark=" "
		case "$beetleable" in *" $engine "*) ;; *) mark="-" ;; esac
		case "$configured" in *" $engine "*) mark="*" ;; esac

		versions="$(rdbms_supported_versions "$CSP" "$engine" | tr '\n' ' ')"
		versions="${versions% }"
		key="$(upper "$CSP")_$(upper "$engine")_DST_VERSIONS"
		current="$(csp_env "$CSP" "$(upper "$engine")_DST_VERSIONS")"

		if [ -z "$versions" ]; then
			# Only an engine beetle could actually build counts as a failure. A CSP
			# engine it cannot create is listed as information, and an empty answer
			# for one of those must not fail the script.
			if [ "$mark" = "-" ]; then
				printf '  %s %-11s (no list; not creatable by cm-beetle here anyway)\n' "$mark" "$engine"
				continue
			fi
			fail "$mark $(printf '%-11s' "$engine") could not read the supported versions."
			fail "    The catalogue may still be loading, or the connection may not cover this engine."
			missing=$((missing + 1))
			continue
		fi
		printf '  %s %-11s %s\n' "$mark" "$engine" "$versions"
		[ -n "$current" ] && printf '    %-11s (currently %s = %s)\n' "" "$key" "$current"

		# Only engines beetle can build are written back. Filling *_DST_VERSIONS for
		# an engine that cannot be a target would put a list in .env that the matrix
		# refuses to run.
		if [ "$WRITE" = "1" ] && [ "$mark" != "-" ]; then
			dv_set_env_key "$ENV_FILE" "$key" "$versions"
			changed=$((changed + 1))
		fi
	done

	echo
	if [ "$WRITE" = "1" ]; then
		ok "wrote ${changed} key(s) into $ENV_FILE."
		warn "One managed instance is created per target version — leaving the list as is costs that much time and money."
		warn "Trim it to the versions you actually want to run."
	else
		info "To save it: $(basename "$0") --write"
	fi
	[ "$missing" -eq 0 ] || exit 1
	exit 0
}
