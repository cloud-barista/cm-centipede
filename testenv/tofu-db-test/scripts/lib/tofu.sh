#!/usr/bin/env bash
#
# lib/tofu.sh — the OpenTofu client (written for this folder)
#
# Why tofu provisions the managed instances
#   - tofu creates the resources and the state file remembers them, so there is
#     no separate inventory that can drift out of step with the CSP.
#   - The requested version travels as written: the string is handed to the CSP,
#     which fails when it does not exist rather than substituting another.
#   - The matrix controls the managed instance's parameters (AWS), so columns
#     that require_secure_transport would otherwise close stay open.
#
# One column = one tofu workspace
#   The workspace is named <engine>-<version>. State split per column means an
#   interrupted run still says what exists, and --cleanup reclaims all of it.
#
# Credentials
#   The CSP keys and the DB password live in OpenBao, and the modules read them
#   directly through the vault provider. The only one the matrix holds is the DB
#   password (a cell has to connect to the target), and even that is fetched on
#   demand by vault_db_password.

if [ -n "${MATRIX_TOFU_SH:-}" ]; then return 0; fi
MATRIX_TOFU_SH=1

# shellcheck source=./common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

TOFU_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOFU_ROOT="$(cd "$TOFU_LIB_DIR/../.." && pwd)"

TOFU_RUNNER="${TOFU_RUNNER:-cptfm-tofu-runner}"
TOFU_OPENBAO="${TOFU_OPENBAO:-cptfm-openbao}"

# The last tofu run's output — reported verbatim when something fails.
TOFU_OUT=""
TOFU_LAST=""

# ---------------------------------------------------------------------------
# Driving the runner
# ---------------------------------------------------------------------------

# _in_runner MODULE SCRIPT — run inside the runner container, cwd at the module.
#
#   .env is re-read inside the container. The container holds the VAULT_TOKEN it
#   started with, and up.sh may have written a new one since.
#   VAULT_ADDR is overwritten with the compose network address - the value in the
#   env file is the one for the host.
_in_runner() {
	local mod="$1" script="$2"
	docker exec "$TOFU_RUNNER" bash -c '
		set -euo pipefail
		set -a; . /work/.env; set +a
		export VAULT_ADDR=http://openbao:8200
		mkdir -p /work/.tofu-plugin-cache
		cd "/work/tofu/'"$mod"'"
		'"$script"'
	' 2>&1
}

# tofu_run MODULE ARGS... — one tofu call. The output goes into TOFU_OUT and
#   through to the screen.
tofu_run() {
	local mod="$1"; shift
	local args="" a
	for a in "$@"; do args="$args $(printf '%q' "$a")"; done
	TOFU_LAST="tofu$args  ($mod)"
	TOFU_OUT="$(_in_runner "$mod" "tofu$args")"
	local rc=$?
	printf '%s\n' "$TOFU_OUT" | sed 's/^/      /'
	return $rc
}

# tofu_quiet MODULE ARGS... — prints nothing (for lookups). TOFU_OUT is still filled.
tofu_quiet() {
	local mod="$1"; shift
	local args="" a
	for a in "$@"; do args="$args $(printf '%q' "$a")"; done
	TOFU_LAST="tofu$args  ($mod)"
	TOFU_OUT="$(_in_runner "$mod" "tofu$args")"
}

# tofu_report CONTEXT — report a failure in full. Nothing is truncated: a CSP
#   error puts the cause at the end of the sentence, so a cut loses the reason.
tofu_report() {
	local line
	fail "$1"
	fail "  ran: ${TOFU_LAST:-?}"
	fail "  tofu output:"
	while IFS= read -r line; do fail "    $line"; done <<< "${TOFU_OUT:-(the output was empty)}"
}
tofu_report_warn() {
	local line
	warn "$1"
	warn "  ran: ${TOFU_LAST:-?}"
	warn "  tofu output:"
	while IFS= read -r line; do warn "    $line"; done <<< "${TOFU_OUT:-(the output was empty)}"
}

# tofu_init MODULE — once per run. init fetches providers, so repeating it is slow.
tofu_init() {
	local mod="$1" marker
	marker="${MATRIX_TMP:-/tmp}/init-$(printf '%s' "$mod" | tr '/' '-')"
	[ -f "$marker" ] && return 0
	if ! tofu_quiet "$mod" init -input=false; then
		tofu_report "tofu init failed: $mod"
		return 1
	fi
	: > "$marker"
}

# tofu_vars MODULE JSON — write matrix.auto.tfvars.json into the module.
#
#   Not passed with -var, because a value crossing docker exec -> bash -c -> tofu
#   gives quoting three chances to break, and leaves no record of what was
#   applied. As a file, destroy reuses the very same values.
tofu_vars() {
	local mod="$1" json="$2"
	printf '%s\n' "$json" | jq . > "$TOFU_ROOT/tofu/$mod/matrix.auto.tfvars.json"
}

# tofu_workspace MODULE NAME — select it, creating it when absent.
tofu_workspace() {
	local mod="$1" ws="$2"
	if ! tofu_quiet "$mod" workspace select -or-create "$ws"; then
		tofu_report "could not select the workspace: $ws ($mod)"
		return 1
	fi
}

# tofu_output MODULE — the outputs as JSON on stdout.
tofu_output() {
	local mod="$1"
	_in_runner "$mod" 'tofu output -json 2>/dev/null || echo "{}"'
}

# tofu_out_value MODULE KEY — one output value. Empty when absent.
#   `// empty` is avoided: jq's alternative operator swallows false as well as
#   null, which would turn public_access=false into an empty string.
tofu_out_value() {
	printf '%s' "$1" | jq -r --arg k "$2" '
		.[$k].value | if . == null then "" else . end' 2>/dev/null || printf ''
}

# tofu_has_state MODULE — does it actually hold managed resources?
#   Data sources are not counted. Destroying a module that holds nothing but a
#   vault lookup does nothing and spends plan time doing it.
tofu_has_state() {
	local out
	out="$(_in_runner "$1" 'tofu state list 2>/dev/null || true' | grep -v '^data\.' || true)"
	[ -n "$(printf %s "$out" | tr -d '[:space:]')" ]
}

# ---------------------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------------------

# tofu_preflight — is the stack in a usable state?
#
#   A merely sealed vault is unsealed here (the common state after a restart). An
#   uninitialized one is not: initializing writes the unseal key and the root
#   token to files and is hard to undo, which is not something the matrix should
#   do on the side.
tofu_preflight() {
	if ! docker ps --format '{{.Names}}' | grep -qx "$TOFU_RUNNER"; then
		fail "the tofu runner container is not running: $TOFU_RUNNER"
		die "Run ./scripts/up.sh first."
	fi

	local status initialized sealed
	status="$(curl -sf "${VAULT_ADDR:-http://localhost:38210}/v1/sys/seal-status" 2>/dev/null || echo '{}')"
	initialized="$(printf '%s' "$status" | jq -r '.initialized')"
	sealed="$(printf '%s' "$status" | jq -r '.sealed')"

	if [ "$initialized" != "true" ]; then
		fail "OpenBao is uninitialized or not answering (${VAULT_ADDR:-http://localhost:38210})."
		die "Run ./scripts/up.sh to finish initialization and credential registration."
	fi
	if [ "$sealed" != "false" ]; then
		warn "OpenBao is sealed — unsealing..."
		if ! bash "$TOFU_ROOT/init/openbao/openbao-unseal.sh" >/dev/null 2>&1; then
			die "unsealing failed. Run ./scripts/up.sh."
		fi
		ok "unsealed"
	fi

	if [ -z "${VAULT_TOKEN:-}" ]; then
		die "VAULT_TOKEN is empty. Run ./scripts/up.sh."
	fi
	ok "the tofu runner and OpenBao answer"
}

# vault_db_password CSP — the managed DB master password. Cached for the run.
#
#   The modules read it through the vault provider and do not need this. The shell
#   needs it for its own reasons: a cell connects to the target to create its
#   database and centipede is handed it in the request.
vault_db_password() {
	local csp key cache
	csp="$(lower "$1")"
	key="$(upper "$csp")_DB_PASSWORD"
	cache="${MATRIX_TMP:-/tmp}/dbpass-$csp"
	if [ ! -f "$cache" ]; then
		curl -sf -H "X-Vault-Token: ${VAULT_TOKEN:-}" \
			"${VAULT_ADDR:-http://localhost:38210}/v1/secret/data/db/$csp" 2>/dev/null \
			| jq -r --arg k "$key" '.data.data[$k] // ""' > "$cache" 2>/dev/null || : > "$cache"
		chmod 600 "$cache" 2>/dev/null || true
	fi
	cat "$cache"
}

# assert_db_password CSP — check the password is really stored, before anything is created.
assert_db_password() {
	local csp="$1"
	if [ -z "$(vault_db_password "$csp")" ]; then
		fail "OpenBao has no $(upper "$csp")_DB_PASSWORD under secret/db/$(lower "$csp")."
		fail "  Put the value in .env and run ./scripts/up.sh again."
		return 1
	fi
	return 0
}

# ---------------------------------------------------------------------------
# Engines — only what the CSP offers as managed
# ---------------------------------------------------------------------------
# The source is built here, so all four engines work there; a target can only be
# what the CSP offers as a managed service. Self-hosting (on EC2, or docker on a
# server) is out of scope for this matrix.
#
#   aws  mysql mariadb postgresql   — the only managed MongoDB is DocumentDB,
#                                     which exposes no public endpoint, so the
#                                     matrix host cannot reach it
#   ncp  mysql postgresql mongodb   — there is no managed MariaDB
MATRIX_ENGINES_AWS="mysql mariadb postgresql"
MATRIX_ENGINES_NCP="mysql postgresql mongodb"

csp_engines() {
	case "$(lower "$1")" in
	aws) printf '%s' "$MATRIX_ENGINES_AWS" ;;
	ncp) printf '%s' "$MATRIX_ENGINES_NCP" ;;
	*)   printf '' ;;
	esac
}

# csp_engine_name CSP ENGINE — the name the CSP uses. AWS calls postgresql
#   postgres; the matrix and transx-ex say postgresql throughout.
csp_engine_name() {
	local csp engine
	csp="$(lower "$1")"; engine="$(lower "$2")"
	if [ "$csp" = "aws" ] && [ "$engine" = "postgresql" ]; then
		printf 'postgres'
	else
		printf '%s' "$engine"
	fi
}

# assert_engine_known ENGINE CSP — checked before any network call.
assert_engine_known() {
	local engine csp e
	engine="$(lower "$1")"; csp="$(lower "$2")"
	for e in $(csp_engines "$csp"); do
		[ "$e" = "$engine" ] && return 0
	done
	case "$csp:$engine" in
	aws:mongodb)
		fail "AWS has no managed MongoDB this matrix can use."
		fail "  DocumentDB exposes no public endpoint, so the matrix host cannot reach it,"
		fail "  and EC2 self-hosting is out of scope. Run it on NCP." ;;
	ncp:mariadb)
		fail "NCP has no managed MariaDB (self-hosted docker only)."
		fail "  mariadb columns run on AWS only." ;;
	*)
		fail "unknown engine '$engine' ($csp). This CSP supports: $(csp_engines "$csp")" ;;
	esac
	fail "  Drop it from $(upper "$csp")_ENGINES."
	return 1
}

# ---------------------------------------------------------------------------
# Supported-version lookup
# ---------------------------------------------------------------------------

# _versions_json CSP — apply the versions module (data sources only, so nothing is
#   created) and cache its outputs.
_versions_json() {
	local csp cache mod
	csp="$(lower "$1")"
	mod="$csp/versions"
	cache="${MATRIX_TMP:-/tmp}/versions-$csp.json"
	if [ ! -f "$cache" ]; then
		tofu_init "$mod" || { echo '{}' > "$cache"; cat "$cache"; return 1; }
		if ! tofu_quiet "$mod" apply -auto-approve -input=false; then
			tofu_report_warn "$csp catalogue lookup failed ($mod)"
			echo '{}' > "$cache"
		else
			tofu_output "$mod" > "$cache"
		fi
	fi
	cat "$cache"
}

# rdbms_supported_versions CSP ENGINE — supported versions, one per line, version-sorted.
#
#   ⚠ The AWS list is not complete. The provider has no data source that returns
#   all of them (aws_rds_engine_version returns exactly one), so only the default
#   version and the upgrade targets reachable from it appear. The real verdict is
#   the plan in assert_target_versions.
rdbms_supported_versions() {
	local csp engine key
	csp="$(lower "$1")"
	engine="$(csp_engine_name "$csp" "$2")"
	_versions_json "$csp" | jq -r --arg e "$engine" \
		'(.suggested_versions.value[$e] // []) | .[]' 2>/dev/null | sort -V
}

# rdbms_default_version CSP ENGINE — the region's default engine version. AWS only;
#   NCP returns an empty string (its catalogue is complete, so there is no floor).
#
#   It says where the AWS summary starts. "suggested" is the default version plus
#   what an upgrade reaches from it, which only goes forward, so a version below
#   the default cannot appear even when RDS offers it. Without knowing that, it
#   reads as "has 10.6 been dropped?", when in fact it was never asked about.
rdbms_default_version() {
	local csp engine
	csp="$(lower "$1")"
	[ "$csp" = "aws" ] || return 0
	engine="$(csp_engine_name "$csp" "$2")"
	_versions_json "$csp" | jq -r --arg e "$engine" \
		'.rds_versions.value[$e].default_version // ""' 2>/dev/null
}

# aws_full_list_hint ENGINE REGION — how to see the full version list by hand. It
#   prints one line at a time, so the caller wraps it in info or fail as fits.
#
#   ⚠ The matrix never runs the aws command. tofu's provider reaches AWS with the
#     credentials in OpenBao; this hint is for a person looking the list up
#     themselves. Its absence blocks nothing, but following the hint needs it
#     installed, so where to get it is printed when it is missing.
aws_full_list_hint() {
	local engine="$1" region="$2" cmd
	cmd="aws rds describe-db-engine-versions --engine $engine --region $region --query 'DBEngineVersions[].EngineVersion'"
	if command -v aws >/dev/null 2>&1; then
		printf 'To see the full list yourself: %s\n' "$cmd"
		return 0
	fi
	printf 'Seeing the full list yourself needs the AWS CLI, which is not installed here.\n'
	printf '  (The matrix does not need it - the tofu provider reaches AWS.)\n'
	printf '  Install: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html\n'
	printf '  Then: %s\n' "$cmd"
}

# rdbms_probed_lines CSP ENGINE — the version lines the AWS summary probed. AWS only.
#
#   The module widens the list by asking aws_rds_engine_version once per version
#   line (probe_version_lines in tofu/aws/versions). When the list still looks
#   short, this value tells a missing version from a line nobody asked about.
rdbms_probed_lines() {
	local csp engine
	csp="$(lower "$1")"
	[ "$csp" = "aws" ] || return 0
	engine="$(csp_engine_name "$csp" "$2")"
	_versions_json "$csp" | jq -r --arg e "$engine" \
		'.rds_versions.value[$e].probed_lines // ""' 2>/dev/null
}

# _aws_version_plan ENGINE VERSION — plan only, creating nothing.
#
#   Whether the region offers that version, and whether it can be ordered with the
#   configured instance class, both surface here - better than after a 5-30 minute
#   apply has started.
_aws_version_plan() {
	local engine="$1" version="$2" mod="aws/rdbms"
	tofu_init "$mod" || return 1
	tofu_workspace "$mod" precheck || return 1
	tofu_vars "$mod" "$(_aws_rdbms_vars "$engine" "$version")"
	tofu_quiet "$mod" plan -input=false -detailed-exitcode -lock=false
	# -detailed-exitcode: 0=no changes, 2=changes (expected), 1=error
	case "$?" in
	0|2) return 0 ;;
	*)   return 1 ;;
	esac
}

# assert_target_versions CSP ENGINE "V1 V2 ..." — checked before anything is created.
assert_target_versions() {
	local csp engine wanted available missing="" v
	csp="$(lower "$1")"; engine="$(lower "$2")"; wanted="$3"
	available="$(rdbms_supported_versions "$csp" "$engine")"

	if [ "$csp" = "ncp" ]; then
		# NCP's catalogue is complete - anything not in it is refused outright.
		if [ -z "$available" ]; then
			fail "$csp $engine: no supported-version list came back from the NCP catalogue."
			fail "  Check that credentials are registered (./scripts/up.sh)."
			return 1
		fi
		for v in $wanted; do
			printf '%s\n' "$available" | grep -qxF "$v" && continue
			missing="${missing:+$missing }$v"
		done
		if [ -n "$missing" ]; then
			fail "$csp $engine: target versions this region does not offer — $missing"
			fail "  Available: $(printf '%s' "$available" | tr '\n' ' ')"
			fail "  NCP needs full version strings (8.0.36, not 8.0)."
			fail "  Fix NCP_$(upper "$engine")_DST_VERSIONS. Nothing was created."
			return 1
		fi
		info "$csp $engine target versions confirmed: $wanted"
		return 0
	fi

	# AWS — the list is only a hint; each version is judged by a plan.
	local aws_engine line failed=0
	aws_engine="$(csp_engine_name aws "$engine")"
	for v in $wanted; do
		printf '%s\n' "$available" | grep -qxF "$v" \
			|| info "  $engine $v — not in the catalogue summary (which is not the full list, so a plan decides)."
		if _aws_version_plan "$aws_engine" "$v"; then
			info "  $engine $v — can be ordered"
		else
			fail "  $engine $v — cannot be created in this region."
			tofu_report_warn "    plan failed"
			failed=1
		fi
	done
	if [ "$failed" -ne 0 ]; then
		fail "$csp $engine: target version check failed. Nothing was created."
		while IFS= read -r line; do fail "  $line"; done \
			<<< "$(aws_full_list_hint "$aws_engine" "$(csp_env aws REGION)")"
		return 1
	fi
	info "$csp $engine target versions confirmed: $wanted"
	return 0
}

# ---------------------------------------------------------------------------
# Network — AWS uses the default VPC as is, so there is nothing to do. NCP only.
# ---------------------------------------------------------------------------
ensure_network() {
	local csp="$1"
	[ "$(lower "$csp")" = "ncp" ] || { info "AWS uses the default VPC — there is no network to create."; return 0; }

	local mod="ncp/network"
	tofu_init "$mod" || return 1
	tofu_vars "$mod" "$(jq -n \
		--arg region "$(csp_env ncp REGION)" \
		--arg zone "$(csp_env ncp ZONE)" \
		--arg prefix "$MATRIX_NAME_PREFIX" \
		--arg cidr "$(csp_env ncp VPC_CIDR)" \
		'{ncp_region:$region, ncp_zone:$zone, name_prefix:$prefix}
		 + (if $cidr == "" then {} else {ncp_vpc_cidr:$cidr} end)')"

	if tofu_has_state "$mod"; then
		local existing
		existing="$(tofu_out_value "$(tofu_output "$mod")" vpc_name)"
		if [ -n "$existing" ] && [ "$existing" != "${MATRIX_NAME_PREFIX}-vpc" ]; then
			fail "the existing ncp/network was created under a different prefix."
			fail "  existing: $existing        current: ${MATRIX_NAME_PREFIX}-vpc"
			fail "  The rdbms module looks the VPC and subnet up by name, so the lookup comes back empty."
			fail "  Put MATRIX_NAME_PREFIX back to '${existing%-vpc}', or destroy the network and rebuild it:"
			fail "    ./scripts/ncp-db-matrix.sh --cleanup"
			return 1
		fi
		info "ncp/network — already there ($existing)"
		return 0
	fi

	step "creating ncp/network (VPC + PUBLIC subnet)"
	if ! tofu_run "$mod" apply -auto-approve -input=false; then
		tofu_report "could not create ncp/network"
		return 1
	fi
	ok "ncp/network ready"
}

release_network() {
	local csp="$1" mod="ncp/network"
	[ "$(lower "$csp")" = "ncp" ] || return 0
	tofu_has_state "$mod" || return 0
	step "destroying ncp/network"
	if tofu_run "$mod" destroy -auto-approve -input=false; then
		ok "ncp/network destroyed"
		return 0
	fi
	# The CSP may still be releasing the servers. What was asked for is already
	# gone, so this is a warning rather than a failure; running again finishes it.
	tofu_report_warn "could not destroy ncp/network — run again in a moment to finish it."
	return 1
}

# ---------------------------------------------------------------------------
# The managed instance
# ---------------------------------------------------------------------------
RDBMS_NAME=""; RDBMS_STATUS=""; RDBMS_ENDPOINT=""; RDBMS_HOST=""; RDBMS_PORT=""
RDBMS_USER=""; RDBMS_ENGINE=""; RDBMS_VERSION=""; RDBMS_PUBLIC=""
RDBMS_CSP_NAME=""; RDBMS_CSP_ID=""; RDBMS_ADMIN_DB=""; RDBMS_SECURE_TRANSPORT=""
# NCP only: the managed-DB SERVICE number the per-database APIs address. Empty on
# AWS, where tofu/{my,pg}-target connect to the instance instead.
RDBMS_INSTANCE_NO=""
# NCP only: which ACG the inbound rule was attached to, and every ACG the instance
# has. Both are reported when the endpoint cannot be reached - a rule on the wrong
# group applies cleanly and leaves the port shut.
RDBMS_ACG_NO=""; RDBMS_ACG_LIST=""
RDBMS_PRIVATE_DOMAIN=""; RDBMS_PUBLIC_SUBNET=""

# rdbms_workspace ENGINE VERSION — the workspace name for one column.
rdbms_workspace() {
	printf '%s-%s' "$(lower "$1")" "$(printf '%s' "$2" | tr -c 'a-z0-9' '-' | sed 's/-\{2,\}/-/g; s/-$//')"
}

rdbms_module() { printf '%s/rdbms' "$(lower "$1")"; }

_aws_rdbms_vars() {
	jq -n \
		--arg engine "$1" --arg version "$2" \
		--arg region "$(csp_env aws REGION)" \
		--arg prefix "$MATRIX_NAME_PREFIX" \
		--arg db "$ADMIN_DB" \
		--arg user "$(csp_env aws DB_ADMIN_USERNAME)" \
		--arg cidr "$MATRIX_ALLOWED_CIDR" \
		--arg class "$(csp_env aws DB_INSTANCE_CLASS)" \
		--argjson storage "$(csp_env_int aws DB_STORAGE_GB 20)" \
		--arg secure "${AWS_TARGET_SECURE_TRANSPORT:-csp-default}" \
		'{engine:$engine, engine_version:$version, aws_region:$region,
		  name_prefix:$prefix, db_name:$db, allowed_cidr:$cidr,
		  db_allocated_storage:$storage, secure_transport:$secure}
		 + (if $user  == "" then {} else {db_username:$user} end)
		 + (if $class == "" then {} else {db_instance_class:$class} end)'
}

_ncp_rdbms_vars() {
	jq -n \
		--arg engine "$1" --arg version "$2" \
		--arg region "$(csp_env ncp REGION)" \
		--arg prefix "$MATRIX_NAME_PREFIX" \
		--arg db "$ADMIN_DB" \
		--arg user "$(csp_env ncp DB_ADMIN_USERNAME)" \
		--arg cidr "$MATRIX_ALLOWED_CIDR" \
		'{engine:$engine, engine_version:$version, ncp_region:$region,
		  name_prefix:$prefix, db_name:$db, allowed_cidr:$cidr}
		 + (if $user == "" then {} else {db_username:$user} end)'
}

# _rdbms_read CSP — read the outputs into RDBMS_*.
_rdbms_read() {
	local csp="$1" mod out
	mod="$(rdbms_module "$csp")"
	out="$(tofu_output "$mod")"
	[ "$(printf '%s' "$out" | jq -r 'length' 2>/dev/null)" = "0" ] && return 1

	RDBMS_HOST="$(tofu_out_value "$out" host)"
	RDBMS_PORT="$(tofu_out_value "$out" port)"
	RDBMS_USER="$(tofu_out_value "$out" username)"
	RDBMS_ADMIN_DB="$(tofu_out_value "$out" database)"
	RDBMS_ENGINE="$(tofu_out_value "$out" engine)"
	RDBMS_VERSION="$(tofu_out_value "$out" engine_version)"
	RDBMS_STATUS="$(tofu_out_value "$out" status)"
	RDBMS_PUBLIC="$(tofu_out_value "$out" public_access)"
	RDBMS_CSP_NAME="$(tofu_out_value "$out" csp_name)"
	RDBMS_CSP_ID="$(tofu_out_value "$out" csp_id)"
	RDBMS_INSTANCE_NO="$(tofu_out_value "$out" instance_no)"
	RDBMS_ACG_NO="$(tofu_out_value "$out" acg_no)"
	RDBMS_ACG_LIST="$(printf '%s' "$out" | jq -r '(.acg_no_list.value // []) | join(", ")' 2>/dev/null || printf '')"
	RDBMS_SECURE_TRANSPORT="$(tofu_out_value "$out" secure_transport)"
	RDBMS_PRIVATE_DOMAIN="$(tofu_out_value "$out" private_domain)"
	RDBMS_PUBLIC_SUBNET="$(tofu_out_value "$out" public_subnet)"
	RDBMS_ENDPOINT="${RDBMS_HOST:+$RDBMS_HOST:$RDBMS_PORT}"
	[ -n "$RDBMS_HOST" ]
}

# rdbms_info CSP NAME — re-read the current column. On NCP the refresh is the
#   update: a public domain issued in the console only appears once state is
#   read again.
rdbms_info() {
	local csp="$1" mod
	mod="$(rdbms_module "$csp")"
	if [ "$(lower "$csp")" = "ncp" ]; then
		tofu_quiet "$mod" apply -refresh-only -auto-approve -input=false || true
	fi
	_rdbms_read "$csp"
}

# rdbms_csp_label — one line for finding it in the console.
rdbms_csp_label() {
	if [ -n "$RDBMS_CSP_NAME" ] || [ -n "$RDBMS_CSP_ID" ]; then
		printf 'CSP name %s  CSP id %s' "${RDBMS_CSP_NAME:-(none)}" "${RDBMS_CSP_ID:-(none)}"
	else
		printf 'no CSP resource info (not created yet, or the lookup failed)'
	fi
}

rdbms_report_for_console() {
	local csp="$1" name="$2"
	fail "  To find it in the console:"
	fail "    workspace   $name"
	fail "    CSP name    ${RDBMS_CSP_NAME:-(unknown)}"
	fail "    CSP id      ${RDBMS_CSP_ID:-(unknown)}   region $(csp_env "$csp" REGION)"
	fail "  The state is still there, so this reclaims it:"
	fail "    ./scripts/$(lower "$csp")-db-matrix.sh --cleanup"
}

# create_rdbms CSP ENGINE VERSION — stand up one column.
create_rdbms() {
	local csp engine version mod ws csp_engine
	csp="$(lower "$1")"; engine="$(lower "$2")"; version="$3"
	mod="$(rdbms_module "$csp")"
	ws="$(rdbms_workspace "$engine" "$version")"
	csp_engine="$(csp_engine_name "$csp" "$engine")"
	RDBMS_NAME="$ws"

	tofu_init "$mod" || return 1
	tofu_workspace "$mod" "$ws" || return 1

	if [ "$csp" = "aws" ]; then
		tofu_vars "$mod" "$(_aws_rdbms_vars "$csp_engine" "$version")"
	else
		tofu_vars "$mod" "$(_ncp_rdbms_vars "$csp_engine" "$version")"
	fi

	if tofu_has_state "$mod"; then
		if [ "${REUSE_INSTANCE:-1}" != "1" ]; then
			fail "$ws already holds state (REUSE_INSTANCE=0, so it is not reused)."
			return 1
		fi
		if _rdbms_read "$csp"; then
			info "reusing the instance: $ws  [$RDBMS_ENGINE $RDBMS_VERSION]"
			info "               $(rdbms_csp_label)"
			ok "$ws  $RDBMS_ENDPOINT  (admin $RDBMS_USER)"
			return 0
		fi
		warn "$ws holds state but no address — continuing with an apply."
	fi

	step "creating the instance: $ws (5-30 min for a managed DB, about 30 on NCP)"
	info "  engine     $csp_engine $version"
	[ "$csp" = "aws" ] && info "  plaintext  secure_transport=${AWS_TARGET_SECURE_TRANSPORT:-csp-default}"

	if ! tofu_run "$mod" apply -auto-approve -input=false; then
		tofu_report "could not create the instance: $ws"
		# An apply that died halfway may still have created something at the CSP.
		# It is in state, so the name is printed along with how --cleanup reclaims it.
		_rdbms_read "$csp" >/dev/null 2>&1 || true
		rdbms_report_for_console "$csp" "$ws"
		return 1
	fi

	if ! _rdbms_read "$csp"; then
		# On NCP, host stays empty until the public domain exists - not a failure.
		if [ "$csp" = "ncp" ]; then
			warn "$ws was created but has no public domain yet (it must be requested in the console)."
			return 0
		fi
		fail "$ws was created but its address cannot be read."
		rdbms_report_for_console "$csp" "$ws"
		return 1
	fi

	# A last check that it was built at the requested version. AWS resolves a prefix
	# (8.0) to the current minor, so this compares component by component.
	if ! version_compatible "$version" "$RDBMS_VERSION"; then
		fail "$ws was created as '$RDBMS_VERSION', not the requested '$version'."
		fail "  $(rdbms_csp_label)"
		return 1
	fi

	ok "$ws  [$RDBMS_ENGINE $RDBMS_VERSION]  $RDBMS_ENDPOINT  (admin $RDBMS_USER)"
	ok "  $(rdbms_csp_label)"
	return 0
}

# delete_rdbms CSP NAME — destroy one column: select the workspace, then destroy.
#
#   Retried once. While NCP is still releasing a server it answers some delete
#   calls with 500 / returnCode 1300, and the same call succeeds a moment later.
delete_rdbms() {
	local csp="$1" ws="$2" mod attempt=0
	local retries="${DELETE_RETRIES:-1}" wait="${DELETE_RETRY_WAIT:-60}"
	mod="$(rdbms_module "$csp")"
	[ -n "$ws" ] || return 0

	tofu_workspace "$mod" "$ws" || return 1
	if ! tofu_has_state "$mod"; then
		info "$ws — nothing to destroy."
		_tofu_drop_workspace "$mod" "$ws"
		return 0
	fi

	step "destroying the instance: $ws (this takes minutes)"
	while :; do
		if tofu_run "$mod" destroy -auto-approve -input=false; then
			ok "instance destroyed: $ws"
			_tofu_drop_workspace "$mod" "$ws"
			return 0
		fi
		if [ "$attempt" -ge "$retries" ]; then break; fi
		attempt=$((attempt + 1))
		warn "$ws destroy failed — retrying in ${wait}s (${attempt}/${retries})"
		sleep "$wait"
	done

	tofu_report "could not destroy the instance: $ws"
	fail "  ⚠ It keeps billing while it exists. The state is intact, so this retries:"
	fail "    ./scripts/$(lower "$csp")-db-matrix.sh --cleanup"
	fail "    $(rdbms_csp_label)   region $(csp_env "$csp" REGION)"
	return 1
}

# _tofu_drop_workspace — remove an emptied workspace. Left behind, --cleanup would
#   walk the empty ones every time. Switch to default first - a workspace in use
#   cannot be deleted.
_tofu_drop_workspace() {
	local mod="$1" ws="$2"
	[ "$ws" = "default" ] && return 0
	tofu_quiet "$mod" workspace select default || return 0
	tofu_quiet "$mod" workspace delete "$ws" || true
}

# cleanup_all_workspaces CSP — reclaim every column still standing.
#   This is where what an interrupted run left behind is collected. State knows
#   what exists, so nothing has to be recognised by hand.
cleanup_all_workspaces() {
	local csp="$1" mod ws found=0
	mod="$(rdbms_module "$csp")"
	tofu_init "$mod" || return 1

	tofu_quiet "$mod" workspace list
	while IFS= read -r ws; do
		ws="$(printf '%s' "$ws" | sed 's/^[* ]*//; s/[[:space:]]*$//')"
		[ -z "$ws" ] && continue
		[ "$ws" = "default" ] && continue
		found=1
		sub "reclaiming column: $ws"
		delete_rdbms "$csp" "$ws" || true
	done <<< "$TOFU_OUT"

	# A cell module may have died halfway - cell-level resources are collected too.
	# init comes first: with state but no provider, destroy cannot even start.
	local cell_mod
	for cell_mod in pg-target my-target ncp-target; do
		tofu_init "$cell_mod" >/dev/null 2>&1 || true
		if tofu_has_state "$cell_mod"; then
			sub "reclaiming leftover $cell_mod"
			tofu_run "$cell_mod" destroy -auto-approve -input=false \
				|| tofu_report_warn "could not reclaim $cell_mod"
		fi
	done

	[ "$found" -eq 0 ] && info "there is no column to reclaim."
	release_network "$csp" || true
	return 0
}

# ---------------------------------------------------------------------------
# PostgreSQL privilege query — once per column, just before the creation probe
# ---------------------------------------------------------------------------
# ⚠ This is not a gate. Whether a column can run is decided by target_db_probe,
#   which actually creates something. All this does is settle the grant_public
#   value the module is given, and record the diagnostics (public ACL, owner,
#   rolcreatedb) to read when the probe fails.
#
#   This used to gate the column on rolcreatedb. That judgement was wrong once
#   (the privilege to create a schema and the privilege to CREATE DATABASE are
#   different things), and creating one removes the need to infer anything, so
#   the gate moved to the probe.
# A GRANT on schema public only succeeds when the role running it owns the schema
# or is a member of the owner, so it can be wrong in both directions.
#
#   AWS PG 13/14 — public still carries its default grant to PUBLIC, so no GRANT
#                  is needed. Issuing one anyway fails with "must be owner of
#                  schema public" and breaks a column that works today.
#   NCP          — that PUBLIC grant is revoked (public ends up as
#                  {postgres=UC/postgres}), so no table can be created without it.
#
# So it is not turned on unconditionally; the instance is asked, and its answer
# decides.
PG_CAN_CREATE=""; PG_CAN_GRANT=""; PG_CAN_CREATEDB=""; PG_CAN_CREATE_SCHEMA=""
PG_PUBLIC_ACL=""; PG_DB_OWNER=""
PG_GRANT_EFFECTIVE="false"

# The schema a cell's data is restored into. Empty means schema public, which is
# what a migration normally uses. It is filled in by target_db_probe when public
# turns out not to be writable - see the probe for why that happens on NCP and
# what it costs.
PG_TARGET_SCHEMA=""
# Why a column was stopped now lives in TARGET_PROBE_REASON. The values read here
# are appended to that reason and say which server it happened on.

# _psql_query HOST PORT USER PASS DB SQL — a one-line query through the runner's psql.
#   PGSSLMODE is pinned to prefer for the same reason tofu/pg-target pins it:
#   this query gathers diagnostics about the instance, it is not part of what the
#   run measures, and refusing to connect over a transport the server cannot
#   offer would lose the diagnostics rather than record a result.
_psql_query() {
	local host="$1" port="$2" user="$3" pass="$4" db="$5" sql="$6"
	docker exec -e PGPASSWORD="$pass" -e PGSSLMODE=prefer "$TOFU_RUNNER" \
		psql -h "$host" -p "$port" -U "$user" -d "$db" \
		     -v ON_ERROR_STOP=1 -At -F '|' -c "$sql" 2>&1
}

# pg_privilege_preflight — ask the instance RDBMS_* points at, and fill in PG_*.
#   It always returns 0: the probe, not this, decides whether the column runs.
pg_privilege_preflight() {
	local csp="$1" pass out
	pass="$(vault_db_password "$csp")"

	# CREATE is asked about twice, and the two are different things, so they are
	# named apart.
	#   rolcreatedb (a role attribute) — may this role run CREATE DATABASE? It is
	#     not an object privilege and is not inherited through role membership (a
	#     member needs SET ROLE to use it). Kept as a diagnostic.
	#   has_database_privilege(...,'CREATE')  — may a schema be created in this
	#     database? Unrelated to CREATE DATABASE. It is what the fallback path
	#     (migrating into a non-public schema) would need, so it is kept as a
	#     diagnostic. It is true by definition when the connecting user owns the
	#     admin database, so on its own it filters nothing.
	#
	# The second column asks whether a GRANT on schema public could be issued,
	# which means: is this user a member of the role that owns that schema. The
	# owner is looked up rather than named. It used to be spelled 'postgres',
	# which is what NCP calls it - RDS has no such role (its bootstrap superuser
	# is rdsadmin and the master account is whatever .env named), and pg_has_role
	# raises an error on a role that does not exist rather than returning false.
	# With ON_ERROR_STOP=1 that one column aborted the whole SELECT, so every AWS
	# PostgreSQL column lost all six diagnostics - the values the probe's failure
	# message is built from - and fell back to grant_public=false by accident
	# rather than by measurement. Reading nspowner works on both CSPs and on
	# PostgreSQL 15+, where the owner is pg_database_owner rather than a login role.
	sub "PostgreSQL privileges — $RDBMS_HOST:$RDBMS_PORT/$RDBMS_ADMIN_DB"
	out="$(_psql_query "$RDBMS_HOST" "$RDBMS_PORT" "$RDBMS_USER" "$pass" "$RDBMS_ADMIN_DB" "
SELECT has_schema_privilege(current_user,'public','CREATE'),
       coalesce((SELECT pg_has_role(current_user, nspowner, 'member')
                   FROM pg_namespace WHERE nspname='public'),false),
       coalesce((SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname=current_user),false),
       has_database_privilege(current_user,current_database(),'CREATE'),
       coalesce((SELECT array_to_string(nspacl,',') FROM pg_namespace WHERE nspname='public'),''),
       (SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database());")"

	if ! printf '%s' "$out" | grep -q '|'; then
		# Not a stop. A failed query leaves grant_public on the safe side (false)
		# and hands the decision to the probe - if the connection itself is the
		# problem, the probe reports that error verbatim.
		warn "the privilege query failed — running the probe without a GRANT."
		printf '%s\n' "$out" | sed 's/^/      /' >&2
		PG_GRANT_EFFECTIVE="false"

# The schema a cell's data is restored into. Empty means schema public, which is
# what a migration normally uses. It is filled in by target_db_probe when public
# turns out not to be writable - see the probe for why that happens on NCP and
# what it costs.
PG_TARGET_SCHEMA=""
		return 0
	fi

	IFS='|' read -r PG_CAN_CREATE PG_CAN_GRANT PG_CAN_CREATEDB PG_CAN_CREATE_SCHEMA \
		PG_PUBLIC_ACL PG_DB_OWNER <<< "$(printf '%s' "$out" | tail -1)"

	info "can create in public : $PG_CAN_CREATE      public ACL : ${PG_PUBLIC_ACL:-(none)}"
	info "can grant            : $PG_CAN_GRANT      DB owner   : ${PG_DB_OWNER:-?}"
	info "CREATE DATABASE      : $PG_CAN_CREATEDB      can create a schema here : $PG_CAN_CREATE_SCHEMA"

	# rolcreatedb does not gate anything here. A managed service may withhold that
	# role attribute from the master user (NCP does), and the database may still be
	# creatable through the CSP's own path. Whether it works is the probe's answer.
	if [ "$PG_CAN_CREATEDB" != "t" ]; then
		warn "$RDBMS_USER has no rolcreatedb — the probe decides whether one can actually be created."
	fi

	if [ "$PG_CAN_CREATE" = "t" ]; then
		PG_GRANT_EFFECTIVE="false"

# The schema a cell's data is restored into. Empty means schema public, which is
# what a migration normally uses. It is filled in by target_db_probe when public
# turns out not to be writable - see the probe for why that happens on NCP and
# what it costs.
PG_TARGET_SCHEMA=""
		ok "it can already create in public — no GRANT needed."
	elif [ "$PG_CAN_GRANT" = "t" ]; then
		PG_GRANT_EFFECTIVE="true"
		ok "public is closed but a GRANT can be issued — granted per cell."
	else
		# A GRANT needs the schema's owner, or membership of the owner role. With
		# neither there is no way to issue one, so this stays false and the probe's
		# table creation is what catches it.
		PG_GRANT_EFFECTIVE="false"

# The schema a cell's data is restored into. Empty means schema public, which is
# what a migration normally uses. It is filled in by target_db_probe when public
# turns out not to be writable - see the probe for why that happens on NCP and
# what it costs.
PG_TARGET_SCHEMA=""
		warn "cannot create in public, and cannot grant either (ACL ${PG_PUBLIC_ACL:-none}, owner ${PG_DB_OWNER:-?})."
		warn "  Running the probe without a GRANT — if it cannot create a table, this column is SKIPped."
		warn "  What that would need: creating the database through the CSP with an explicit owner"
		warn "  (ncloud_postgresql_databases), or migrating into a non-public schema (transx-ex destination.pgSchema)."
	fi
	return 0
}

# ---------------------------------------------------------------------------
# The cell's target databases — tofu both creates and destroys them
#
#   One module per engine:
#     postgresql        tofu/pg-target   postgresql_database + a conditional GRANT
#     mysql · mariadb   tofu/my-target   mysql_database
#     mongodb           none             — nothing to create (it begins on first
#                                          write). Only leftovers are dropped.
#
#   The two modules sit at the top of tofu/ rather than under tofu/aws/ or
#   tofu/ncp/ by design: they speak the database's own wire protocol rather than a
#   CSP API, so one module serves an RDS instance and an NCP managed one alike.
#
#   A cell's lifetime: destroy (leftovers) -> apply -> ...migration... -> destroy.
#   No workspace is used: columns are split by workspace, but only one cell module
#   is alive at a time, so default alone is enough.
#
#   This step exists because centipede does not create the target database: it
#   does so only for providerName "onprem", and a managed target is aws or ncp.
#   That check also runs at POST /plans/target, so a missing database fails the
#   plan, not the migration.
# ---------------------------------------------------------------------------

# target_db_module CSP ENGINE — this cell's database module. Empty for mongodb.
#
#   The CSP decides how a database is made, not just which engine it is.
#
#     aws  my-target / pg-target   connect to the instance and run CREATE DATABASE.
#                                  The RDS master account is a real administrator,
#                                  so this works and needs no CSP API.
#     ncp  ncp-target              go through the CSP API. NCP's master account is
#                                  granted rights on the databases the service
#                                  knows about and cannot create new ones itself:
#                                  Error 1044, Access denied for user 'dbadmin'@'%'
#                                  to database 'cptfm_probe'. On PostgreSQL the API
#                                  also takes an owner, which removes the schema
#                                  public problem instead of working around it.
#
#   MongoDB has no CREATE DATABASE on either CSP, so it has no module at all.
target_db_module() {
	local csp="$(lower "$1")" engine="$(lower "$2")"
	case "$engine" in
	mongodb) printf ''; return ;;
	esac
	if [ "$csp" = "ncp" ]; then
		printf 'ncp-target'
		return
	fi
	case "$engine" in
	postgresql)     printf 'pg-target' ;;
	mysql|mariadb)  printf 'my-target' ;;
	*)              printf '' ;;
	esac
}

# _target_db_vars ENGINE PASS "DB1 DB2 ..." — the tfvars JSON for the module.
#
#   No TLS setting is passed. TARGET_TLS_MODE is a condition of the migration,
#   carried in the connection centipede makes; these modules only create the
#   database that migration needs, and they connect on the best terms the server
#   offers - see tofu/my-target/provider.tf. Applying the experiment's condition
#   to the setup made NCP Cloud DB for MySQL, which offers no TLS at all, SKIP
#   every column instead of reporting how its migrations went.
_target_db_vars() {
	local csp="$1" engine="$2" pass="$3" dbs="$4" json
	json="$(printf '%s\n' $dbs | jq -R . | jq -sc .)"

	# NCP: the CSP API, addressed by the service instance number. No host, port or
	# password - nothing connects to the database here.
	if [ "$(lower "$csp")" = "ncp" ]; then
		jq -n \
			--arg engine "$(lower "$engine")" --arg no "$RDBMS_INSTANCE_NO" \
			--argjson dbs "$json" --arg owner "$RDBMS_USER" \
			--arg region "$(csp_env ncp REGION)" \
			'{engine:$engine, instance_no:$no, databases:$dbs, owner:$owner}
			 + (if $region == "" then {} else {ncp_region:$region} end)'
		return
	fi

	if [ "$(lower "$engine")" = "postgresql" ]; then
		jq -n \
			--arg host "$RDBMS_HOST" --argjson port "${RDBMS_PORT:-5432}" \
			--arg user "$RDBMS_USER" --arg pass "$pass" \
			--arg admin "$RDBMS_ADMIN_DB" --argjson dbs "$json" \
			--arg template "${PG_TARGET_TEMPLATE:-template1}" \
			--argjson grant "$PG_GRANT_EFFECTIVE" \
			'{host:$host, port:$port, username:$user, password:$pass,
			  admin_database:$admin, databases:$dbs,
			  template:$template, grant_public:$grant}'
	else
		jq -n \
			--arg host "$RDBMS_HOST" --argjson port "${RDBMS_PORT:-3306}" \
			--arg user "$RDBMS_USER" --arg pass "$pass" \
			--argjson dbs "$json" \
			'{host:$host, port:$port, username:$user, password:$pass,
			  databases:$dbs}'
	fi
}

# target_db_create CSP ENGINE "DB1 DB2 ..." — create the empty target databases.
target_db_create() {
	local csp="$1" engine="$2" dbs="$3" mod pass
	mod="$(target_db_module "$csp" "$engine")"

	# MongoDB has no CREATE DATABASE: a database begins to exist when its first
	# collection is created, so there is nothing to create - only what the previous
	# cell left has to go. A tofu module with no resource holds nothing in state and
	# its destroy does nothing, so this one case uses the source container's mongosh.
	if [ -z "$mod" ]; then
		mongodb_drop_target "$dbs" || return 1
		info "target database: $dbs (MongoDB creates it on first write — nothing to make in advance)"
		return 0
	fi

	pass="$(vault_db_password "$csp")"
	tofu_init "$mod" || return 1
	tofu_vars "$mod" "$(_target_db_vars "$csp" "$engine" "$pass" "$dbs")"

	# Leftovers from a previous cell go first. transx-ex requires the target to
	# exist and be empty, so anything left behind fails as target-not-empty.
	tofu_quiet "$mod" destroy -auto-approve -input=false || true

	if ! tofu_quiet "$mod" apply -auto-approve -input=false; then
		tofu_report "could not create the target database: $dbs"
		explain_insecure_transport "$TOFU_OUT" "$csp"
		return 1
	fi

	if [ "$(lower "$engine")" = "postgresql" ]; then
		if [ "$mod" = "ncp-target" ]; then
			# ncp-target creates through the CSP API, which takes an owner and no
			# template, and issues no GRANT - so neither of those is reported here.
			info "target database created: $dbs  (owner $RDBMS_USER, via the NCP API)"
		else
			info "target database created: $dbs  (owner $RDBMS_USER, template ${PG_TARGET_TEMPLATE:-template1}, public GRANT=$PG_GRANT_EFFECTIVE)"
		fi
	else
		info "target database created: $dbs  (instance default charset; setup connected on the best transport the server offers)"
	fi
	return 0
}

# target_db_drop CSP ENGINE "DB1 DB2 …"
target_db_drop() {
	local csp="$1" engine="$2" dbs="$3" mod
	mod="$(target_db_module "$csp" "$engine")"

	if [ -z "$mod" ]; then
		mongodb_drop_target "$dbs" || return 1
		info "target database dropped: $dbs"
		return 0
	fi

	tofu_quiet "$mod" destroy -auto-approve -input=false || {
		tofu_report_warn "could not drop the target database: $dbs"
		return 1
	}
	info "target database dropped: $dbs"
	return 0
}

# ---------------------------------------------------------------------------
# The creation probe — once per column, before any cell runs
# ---------------------------------------------------------------------------
# Rather than inferring "we could create one" from a privilege query, one is
# actually created with the very module the cells use, and dropped again.
#
#   That removes the question of which privilege is the right one to ask about. If
#   the probe passes, the cells' creation passes - same module, same tfvars, same
#   connection. This matrix got that inference wrong once (it confused the
#   privilege to create a schema with the privilege to CREATE DATABASE, and lost a
#   16-minute instance on the first cell).
#
#   A failure becomes the whole column's SKIP reason. Folding one column and
#   recording the sentence the CSP produced reads better than every cell dying of
#   the same error one at a time.
TARGET_PROBE_REASON=""

target_db_probe() {
	local csp="$1" engine="$2" probe mod
	TARGET_PROBE_REASON=""
	# Reset for every column: only the PostgreSQL branch below sets it, and a value
	# left over from a previous engine would be sent as a pgSchemas mapping.
	PG_TARGET_SCHEMA=""
	mod="$(target_db_module "$csp" "$engine")"

	# Nothing to create on MongoDB, so nothing to probe. The connection itself was
	# already confirmed by wait_target_reachable.
	[ -z "$mod" ] && return 0

	probe="${MATRIX_NAME_PREFIX}_probe"
	sub "target database creation probe — $probe ($mod)"

	if ! target_db_create "$csp" "$engine" "$probe"; then
		TARGET_PROBE_REASON="target database creation probe failed ($probe)"
		# On PostgreSQL the values the privilege query read are appended to the
		# reason: why it failed is in the tofu output, but which server it happened
		# on is here.
		if [ "$(lower "$engine")" = "postgresql" ] && [ -n "$PG_PUBLIC_ACL$PG_DB_OWNER" ]; then
			TARGET_PROBE_REASON="$TARGET_PROBE_REASON - rolcreatedb=${PG_CAN_CREATEDB:-?}, public ACL ${PG_PUBLIC_ACL:-none}, owner ${PG_DB_OWNER:-?}"
		fi
		return 1
	fi

	# PostgreSQL goes one step further. Being able to create a database and being
	# able to create a table in its public schema are different things, and the
	# migration needs the second. A new database inherits the template's public ACL,
	# so failing here means every cell dies of the same thing at restore time.
	if [ "$(lower "$engine")" = "postgresql" ]; then
		local out pass schema
		pass="$(vault_db_password "$csp")"
		out="$(_psql_query "$RDBMS_HOST" "$RDBMS_PORT" "$RDBMS_USER" "$pass" "$probe" \
			"CREATE TABLE ${MATRIX_NAME_PREFIX}_probe_t (i int); DROP TABLE ${MATRIX_NAME_PREFIX}_probe_t;")"

		if printf '%s' "$out" | grep -qi 'error\|denied'; then
			# ── public is unusable. Try a schema of this account's own ──────────
			# Owning the database is not the same as owning its schema public. NCP
			# revokes the PUBLIC grant and leaves public owned by postgres, so on
			# PostgreSQL 13/14 - where public is owned by a login role rather than
			# by pg_database_owner - even the database's owner cannot create in it,
			# and cannot GRANT itself the right either. Creating the database
			# through the CSP API with an explicit owner does not change that: the
			# unqualified CREATE TABLE above fails with "no schema has been
			# selected to create in", meaning search_path held no schema this
			# account may create in.
			#
			# What the owner CAN do is create a schema of its own in the database it
			# owns, which is what has_database_privilege(...,'CREATE') reported. So
			# that is tried before the column is given up on. transx-ex restores
			# into a renamed schema by emitting CREATE SCHEMA IF NOT EXISTS itself
			# (dbmsx/driver/postgresql), so nothing has to be created in advance -
			# only proven possible here.
			#
			# ⚠ It changes what a PASS means: the data lands in this schema rather
			#   than in public. Recorded as pgTargetSchema in the result JSON so the
			#   two are never read as the same statement.
			schema="${MATRIX_NAME_PREFIX}"
			warn "schema public is not writable here — trying a schema of our own (\"$schema\")."
			printf '%s\n' "$out" | sed 's/^/      /' >&2
			out="$(_psql_query "$RDBMS_HOST" "$RDBMS_PORT" "$RDBMS_USER" "$pass" "$probe" \
				"CREATE SCHEMA IF NOT EXISTS $schema;
				 CREATE TABLE $schema.${MATRIX_NAME_PREFIX}_probe_t (i int);
				 DROP TABLE $schema.${MATRIX_NAME_PREFIX}_probe_t;
				 DROP SCHEMA $schema;")"
			if printf '%s' "$out" | grep -qi 'error\|denied'; then
				fail "could not create a table in the probe database, in public or in a schema of our own."
				printf '%s\n' "$out" | sed 's/^/      /' >&2
				TARGET_PROBE_REASON="cannot create a table in schema public nor create a schema - public ACL ${PG_PUBLIC_ACL:-none}, owner ${PG_DB_OWNER:-?}, grant attempted=${PG_GRANT_EFFECTIVE}, can create a schema=${PG_CAN_CREATE_SCHEMA:-?}"
				target_db_drop "$csp" "$engine" "$probe" >/dev/null 2>&1 || true
				return 1
			fi
			PG_TARGET_SCHEMA="$schema"
			ok "a schema of our own works — this column's cells migrate into \"$schema\" instead of public."
			warn "  ⚠ A PASS here means the migration succeeded into schema \"$schema\", not into public."
			warn "    Recorded as pgTargetSchema; centipede carries it as targetMapping.pgSchemas."
		fi
	fi

	target_db_drop "$csp" "$engine" "$probe" >/dev/null 2>&1 || true
	ok "probe passed — this column's cells can create a target database and write in it."
	return 0
}

# ---------------------------------------------------------------------------
# Explaining a refused plaintext connection
# ---------------------------------------------------------------------------
# Two ways out. Usually raising TARGET_TLS_MODE is enough (the default, prefer,
# never produces this error at all), and failing that a parameter group can allow
# plaintext on AWS.
# explain_pg_maintenance_db ERROR — the "postgres" database transx-ex insists on.
#
#   transx-ex reaches PostgreSQL server-level operations through the maintenance
#   database, and the name is a constant, not a setting:
#
#     transx-ex/dbmsx/driver/postgresql/postgresql.go
#       const pgMaintenanceDB = "postgres"
#
#   Listing the target databases, reading the server version, creating and
#   dropping a database all connect there. NCP's managed PostgreSQL denies its
#   master account that database, so every PostgreSQL cell on NCP fails before
#   any data moves - with the target database already created and reachable.
#
#   This is a finding about centipede, not a setting this matrix got wrong, which
#   is why nothing here offers to work around it. Said plainly so it is not read
#   as a misconfiguration.
explain_pg_maintenance_db() {
	case "$1" in
	*'database "postgres"'*|*"database=postgres"*) ;;
	*) return 0 ;;
	esac
	case "$1" in
	*permission*|*denied*|*"does not exist"*|*FATAL*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: transx-ex connects to the \"postgres\" maintenance database for every"
	fail "         server-level PostgreSQL operation, and this instance does not allow"
	fail "         $RDBMS_USER to connect to it. The target database itself was created"
	fail "         and is reachable - only that one connection is refused."
	fail "  Where: transx-ex/dbmsx/driver/postgresql/postgresql.go, const pgMaintenanceDB"
	fail "  What you can do:"
	fail "    Nothing from this folder. The name is a constant, not a connection setting,"
	fail "    so no .env value or CLI option changes it."
	fail "    This is a result about cm-centipede on this CSP, which is what the matrix"
	fail "    exists to find - record the cell as FAIL and take it to centipede."
}

explain_insecure_transport() {
	case "$1" in
	*3159*|*"insecure transport"*|*"require_secure_transport"*|*"no pg_hba.conf entry"*|*"SSL off"*) ;;
	*) return 0 ;;
	esac
	fail ""
	fail "  Cause: this instance refuses plaintext connections."
	fail "         It is not the engine default but the parameter the CSP attaches to"
	fail "         a managed instance (MariaDB 11.8+ defaults to"
	fail "         require_secure_transport=ON, PostgreSQL to rds.force_ssl=1)."
	fail "  Current TLS mode: TARGET_TLS_MODE=$(target_tls_mode)"
	fail "  What you can do:"
	fail "    Use TLS with --tls-mode prefer (the default) or --tls-mode require."
	fail "    disable demands plaintext, so it cannot reach this instance at all."
	if [ "$(lower "${2:-}")" = "aws" ]; then
		fail "    If plaintext itself is what you need, --secure-transport off attaches a"
		fail "    parameter group, on AWS only. Currently: ${AWS_TARGET_SECURE_TRANSPORT:-csp-default}"
		fail "    ⚠ A result obtained that way is not a result against CSP defaults."
	else
		fail "    On NCP the matrix cannot switch this parameter off — there is no API for it."
	fi
}
