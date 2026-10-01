#!/usr/bin/env bash
#
# lib/db-versions.sh — list the managed DB engine versions a CSP actually offers (body)
#
# The entry point (scripts/<csp>-db-versions.sh) sets the CSP and calls db_versions_main.
#
# Why ask at all:
#   The supported-version list exists nowhere in this repository. The CSP answers
#   it live, and the answer varies by region and by date, so whether the target
#   versions in .env are still valid today is a question that has to be asked.
#   The tofu/<csp>/versions module asks it - data sources only, so an apply
#   creates nothing and costs nothing.
#
# ⚠ The AWS list is not complete. The provider has no data source that returns
#   all of them (aws_rds_engine_version returns exactly one), so version lines are
#   probed one at a time and the answers merged. A line nobody probes does not
#   appear even when RDS offers it. Absent from the list is not unavailable: put
#   it in <CSP>_<ENGINE>_DST_VERSIONS and the matrix checks it with a tofu plan
#   per version before creating anything. NCP's catalogue is complete, so what it
#   prints is everything.
#
# ⚠ These are TARGET (managed) versions. Source versions have nothing to do with
#   this list: they are decided by what the vendor's apt repository serves for
#   jammy, and that table is in .env.example.

if [ -n "${MATRIX_DB_VERSIONS_SH:-}" ]; then return 0; fi
MATRIX_DB_VERSIONS_SH=1

DV_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./common.sh
. "$DV_LIB_DIR/common.sh"
# shellcheck source=./tofu.sh
. "$DV_LIB_DIR/tofu.sh"

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

	MATRIX_NAME_PREFIX="${MATRIX_NAME_PREFIX:-cptfm}"
	TOFU_RUNNER="${TOFU_RUNNER:-cptfm-tofu-runner}"
	VAULT_ADDR="${VAULT_ADDR:-http://localhost:38210}"

	require_cmd docker jq curl
	MATRIX_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cptfm-ver.XXXXXX")"
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

	banner "$(upper "$CSP") managed DB supported versions — tofu catalogue lookup (creates nothing)"
	tofu_preflight

	# Every engine this CSP offers as managed is walked, not just <CSP>_ENGINES.
	# This script exists to fill in that list and *_DST_VERSIONS, so filtering by a
	# value not yet filled in would hide the versions of the very engine being added.
	# It costs no more: the lookup runs once per CSP and is cached, so the engine
	# loop only reads that JSON.
	local engines configured engine mark versions key current default_ver probed hint
	local changed=0 missing=0
	engines="$(csp_engines "$CSP")"
	configured=" $(csp_env "$CSP" ENGINES) "

	sub "$CSP — region $region   (offered as managed: $engines)"
	info "* = currently in $(upper "$CSP")_ENGINES (what the matrix actually runs)"
	if [ "$(lower "$CSP")" = "aws" ]; then
		info "⚠ The AWS list is a summary. The provider has no data source that returns"
		info "  all of them, so version lines such as 8.0 and 8.4 are probed one at a"
		info "  time and merged. A line nobody probes does not appear, even when RDS"
		info "  offers it. What gets probed is in probe_version_lines in"
		info "  tofu/aws/versions, and 'probed lines' at the end of each engine row is"
		info "  what was asked for this time."
		while IFS= read -r hint; do info "  $hint"; done \
			<<< "$(aws_full_list_hint mariadb "$region")"
	fi

	for engine in $engines; do
		mark=" "
		case "$configured" in *" $engine "*) mark="*" ;; esac

		versions="$(rdbms_supported_versions "$CSP" "$engine" | tr '\n' ' ')"
		versions="${versions% }"
		key="$(upper "$CSP")_$(upper "$engine")_DST_VERSIONS"
		current="$(csp_env "$CSP" "$(upper "$engine")_DST_VERSIONS")"

		if [ -z "$versions" ]; then
			fail "$mark $(printf '%-11s' "$engine") could not read the supported versions — check that credentials are registered (./scripts/up.sh)."
			missing=$((missing + 1))
			continue
		fi
		default_ver="$(rdbms_default_version "$CSP" "$engine")"
		probed="$(rdbms_probed_lines "$CSP" "$engine")"
		printf '  %s %-11s %s%s\n' "$mark" "$engine" "$versions" \
			"${default_ver:+   (default $default_ver${probed:+, probed lines $probed})}"
		[ -n "$current" ] && printf '    %-11s (currently %s = %s)\n' "" "$key" "$current"

		if [ "$WRITE" = "1" ]; then
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
