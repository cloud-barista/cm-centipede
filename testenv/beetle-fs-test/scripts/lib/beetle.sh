#!/usr/bin/env bash
#
# lib/beetle.sh — the cm-beetle client (written for this folder)
#
# The provisioner, whole and entire: everything that creates, reads and deletes a
# migrated node lives here. fs-matrix.sh calls the functions below and knows
# nothing about cm-beetle itself, which is what keeps the matrix body, its
# verdicts and its table free of provisioner detail.
#
# ── Which server answers which call ─────────────────────────────────────────
# Every call about a resource goes through cm-beetle. cb-tumblebug is addressed
# directly in exactly two places, and each is a place beetle has no usable API:
#
#   namespace create/delete   cm-beetle has no namespace API — the routes exist
#                             in pkg/api/rest/server.go but are commented out,
#                             while every migration handler starts by reading the
#                             namespace and fails without it.
#   connection lookup         the connection catalogue is tumblebug's own, and
#                             the zone check reads it too.
#
#   namespace     POST|DELETE {TUMBLEBUG}/ns[/{ns}]
#   connection    GET  {TUMBLEBUG}/connConfig/{name}
#   recommend     POST /recommendation/infra
#   network       POST|GET|DELETE /migration/ns/{ns}/resources/{vNet,securityGroup,sshKey}
#   node          POST|GET|DELETE /migration/ns/{ns}/infra[/{infraId}]
#   ssh readiness GET  /migration/ns/{ns}/infra/{infraId}/ssh-ready
#   async track   GET  /request/{reqId}
#
# ── A node needs a network; a bucket did not ────────────────────────────────
# This is the main structural difference from beetle-os-test. An object storage
# bucket sits outside the VPC and is created on its own. A node is created into
# one, and the recommendation does not fill the network in — beetle leaves
# vNetId, subnetId and securityGroupIds empty and expects the caller to say. So
# the vNet, its two subnets, a security group and an SSH key have to exist first
# and their ids have to be injected into the recommendation, which is what
# ensure_network and inject_infra_network do.
#
# The network is per CSP and brackets that CSP's cell: it is created on the way to
# the node and released once the node is gone. Splitting it out from the node
# rather than letting beetle create its own is what makes the teardown orderable —
# a vNet cannot go while something is still attached to it.
#
# ── NO nameSeed ─────────────────────────────────────────────────────────────
# The migration API offers a nameSeed query parameter that prefixes every
# recommended name at creation time. It is not used, and cannot be: beetle's
# ApplyNameSeed prefixes nodeGroups[].vNetId, subnetId and securityGroupIds along
# with the names, so a seed applied on top of the real ids injected here would
# ask for "cpbfs-cpbfs-vnet" and find nothing. Either the seed names everything
# or this does. This does.
#
# ── Where "what exists" is remembered ───────────────────────────────────────
# There is no local state file. cb-tumblebug's namespace is the state, and the
# list API is read back through beetle rather than trusted from disk.
#
# Ownership is carried by the names instead: everything is <prefix>-<csp>-<what>
# inside a namespace this folder alone uses, so cleanup_all can tell its own
# resources from anyone else's without a record.
#
# One hole is left by that, and inflight_* fills it: a create whose request
# reached the CSP but whose record never reached tumblebug is in no list. For a
# node that matters far more than it did for a bucket — an abandoned VM bills.

if [ -n "${MATRIX_BEETLE_SH:-}" ]; then return 0; fi
MATRIX_BEETLE_SH=1

# shellcheck source=./common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

BEETLE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BEETLE_ROOT="$(cd "$BEETLE_LIB_DIR/../.." && pwd)"

BEETLE_URL="${BEETLE_URL:-http://localhost:8056/beetle}"
BEETLE_USERNAME="${BEETLE_USERNAME:-default}"
BEETLE_PASSWORD="${BEETLE_PASSWORD:-default}"
TUMBLEBUG_URL="${TUMBLEBUG_URL:-http://localhost:1323/tumblebug}"
TUMBLEBUG_USERNAME="${TUMBLEBUG_USERNAME:-default}"
TUMBLEBUG_PASSWORD="${TUMBLEBUG_PASSWORD:-default}"

# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------
BT_STATUS=""
BT_BODY=""
BT_RETRY_AFTER=""
BT_LAST_METHOD=""
BT_LAST_URL=""
BT_HEADERS=()
bt_header() { BT_HEADERS+=("$1"); }

# cm-beetle throttles its cb-tumblebug calls and answers a 503 with Retry-After
# when there is no room. That is "in a moment", not a failure, so it is retried.
BT_RATE_LIMIT_RETRIES="${MATRIX_RATE_LIMIT_RETRIES:-3}"

# _http_request BASE USER PASS METHOD PATH [BODY]
_http_request() {
	local base="$1" user="$2" pass="$3" method="$4" path="$5" body="${6:-}"
	local url="${base%/}${path}" out hdr code h cmd

	# Kept so a failure can name the request that produced it. The same sentence
	# comes back from several calls, and the response alone does not say which.
	BT_LAST_METHOD="$method"
	BT_LAST_URL="$url"

	out="$(mktemp "${MATRIX_TMP:-/tmp}/resp.XXXXXX")"
	hdr="${out}.hdr"

	local -a args=(
		--silent --show-error
		--output "$out" --dump-header "$hdr" --write-out '%{http_code}'
		--request "$method"
		--header 'Accept: application/json'
		--connect-timeout 15
	)
	[ -n "$user" ] && args+=(--user "${user}:${pass}")
	# Header values contain spaces (e.g. "Prefer: respond-async"), so they must be
	# quoted on expansion.
	cmd="curl -s -X $method '$(sq_escape "$url")' -u '$user:...'"
	if [ "${#BT_HEADERS[@]}" -gt 0 ]; then
		for h in "${BT_HEADERS[@]}"; do
			args+=(--header "$h")
			cmd="$cmd \\
  -H '$(sq_escape "$h")'"
		done
	fi
	if [ -n "$body" ]; then
		args+=(--header 'Content-Type: application/json' --data-binary "$body")
		cmd="$cmd \\
  -H 'Content-Type: application/json' \\
  -d '$(sq_escape "$(printf '%s' "$body" | mask_json)")'"
	fi

	if code="$(curl "${args[@]}" "$url" 2>"${out}.err")"; then
		BT_BODY="$(cat "$out")"
	else
		code="000"
		BT_BODY="$(cat "${out}.err")"
	fi
	BT_STATUS="$code"
	BT_RETRY_AFTER="$(grep -i '^retry-after:' "$hdr" 2>/dev/null | tail -1 \
		| sed 's/^[^:]*: *//' | tr -d '\r' || true)"
	api_log "$method" "$path" "$cmd" "$code" "$out"
	rm -f "$out" "$hdr" "${out}.err"

	case "$BT_STATUS" in 2*) return 0 ;; *) return 1 ;; esac
}

# bt_request METHOD PATH [BODY] — a cm-beetle call. 503 + Retry-After is retried.
#   Headers added with bt_header apply to this call only.
bt_request() {
	local method="$1" path="$2" body="${3:-}" attempt=0 wait
	local -a headers=()
	[ "${#BT_HEADERS[@]}" -gt 0 ] && headers=("${BT_HEADERS[@]}")

	while :; do
		BT_HEADERS=()
		[ "${#headers[@]}" -gt 0 ] && BT_HEADERS=("${headers[@]}")
		if _http_request "$BEETLE_URL" "${BEETLE_USERNAME:-}" "${BEETLE_PASSWORD:-}" \
		                 "$method" "$path" "$body"; then
			BT_HEADERS=()
			return 0
		fi
		if [ "$BT_STATUS" != "503" ] || [ -z "$BT_RETRY_AFTER" ] \
		   || [ "$attempt" -ge "$BT_RATE_LIMIT_RETRIES" ]; then
			BT_HEADERS=()
			return 1
		fi
		attempt=$((attempt + 1))
		wait="$BT_RETRY_AFTER"
		case "$wait" in ''|*[!0-9]*) wait=5 ;; esac
		warn "beetle is rate limiting — retrying in ${wait}s (${attempt}/${BT_RATE_LIMIT_RETRIES})"
		sleep "$wait"
	done
}

bt_get()    { bt_request GET    "$1"; }
bt_post()   { bt_request POST   "$1" "${2:-}"; }
bt_delete() { bt_request DELETE "$1" "${2:-}"; }

# tb_request — a direct cb-tumblebug call. Only the two cases in the header.
tb_request() {
	local method="$1" path="$2" body="${3:-}"
	_http_request "$TUMBLEBUG_URL" "${TUMBLEBUG_USERNAME:-}" "${TUMBLEBUG_PASSWORD:-}" \
	              "$method" "$path" "$body"
}
tb_get()    { tb_request GET    "$1"; }
tb_post()   { tb_request POST   "$1" "${2:-}"; }
tb_delete() { tb_request DELETE "$1" "${2:-}"; }

# ---------------------------------------------------------------------------
# Reading the last response — beetle wraps in {success,data}, the cb-tumblebug
# proxy endpoints hand the payload back bare. Both shapes are accepted.
# ---------------------------------------------------------------------------
bt_payload() {
	printf '%s' "$BT_BODY" | jq -c '
		if type == "object" and has("success") and has("data") then .data else . end
	' 2>/dev/null || printf ''
}
# `// empty` is avoided: jq's alternative operator swallows false as well as
# null, which would turn a false flag into an empty string.
bt_data() {
	printf '%s' "$BT_BODY" | jq -r "
		(if type == \"object\" and has(\"success\") and has(\"data\") then .data else . end)
		| ${1}
		| if . == null then empty else . end
	" 2>/dev/null || printf ''
}
bt_jq() { bt_payload | jq "$@" 2>/dev/null || printf ''; }

# bt_raw_message — what beetle said, in full, uncut.
#
#   beetle passes a CSP's answer through untouched, so which field holds it is
#   not fixed. The known ones are tried top down, and when none is present the
#   whole response is shown — hiding a shape we do not recognise would remove the
#   only way to see the cause.
bt_raw_message() {
	local m=""
	m="$(printf '%s' "$BT_BODY" | jq -r '
		if type == "object" then (.error // .message // .Message // .text // empty) else empty end' 2>/dev/null || true)"
	if [ -z "$m" ]; then
		m="$(printf '%s' "$BT_BODY" | jq -r \
			'first(.. | objects | (.returnMessage // .message // .Message // .error // empty))' 2>/dev/null || true)"
	fi
	if [ -z "$m" ]; then
		m="$(printf '%s' "$BT_BODY" | jq -e . 2>/dev/null)" || m="$BT_BODY"
	fi
	[ -n "$m" ] || m="(the response body was empty)"
	printf '%s' "$m" | sed 's/\\n/\n/g; s/\\t/\t/g'
}

# _bt_report_lines PRINTER CONTEXT — one failure in three parts: what we were
#   doing, what the request was, and what the server said.
_bt_report_lines() {
	local printer="$1" context="$2" line
	"$printer" "$context"
	"$printer" "  request : ${BT_LAST_METHOD:-?} ${BT_LAST_URL:-?}  →  HTTP ${BT_STATUS:-?}"
	"$printer" "  beetle says:"
	while IFS= read -r line; do
		"$printer" "    $line"
	done <<< "$(bt_raw_message)"
}

bt_report()      { _bt_report_lines fail "$1"; }
bt_report_warn() { _bt_report_lines warn "$1"; }

# bt_absent — did the last response mean "no such resource"?
bt_absent() {
	[ "$BT_STATUS" = "404" ] && return 0
	printf '%s' "$BT_BODY" | grep -qi 'does not exist\|not found\|no such' && return 0
	return 1
}

# bt_deletion_unconfirmed — did the delete go out but the CSP still hold it?
#   cb-tumblebug checks the CSP after a DELETE and, if the resource is still
#   there, returns a 409 without removing its record. That is deliberate —
#   removing the record first would strand the resource — so this is "not yet",
#   not a failure: wait and call again.
bt_deletion_unconfirmed() {
	printf '%s' "$BT_BODY" | grep -qi 'still exists on the CSP\|deletion unconfirmed'
}

# bt_in_use — did the last response mean "something is still attached to it"?
#   A vNet cannot go while a node or a security group references it, and every
#   CSP words that differently. Used only to order the network teardown's retries.
bt_in_use() {
	printf '%s' "$BT_BODY" | grep -qi 'in use\|dependenc\|still attached\|has resources'
}

# ---------------------------------------------------------------------------
# Asynchronous calls — a node takes minutes, so Prefer: respond-async is not a
#   convenience here the way it was for a bucket: a synchronous create would hold
#   one HTTP connection open for the whole build, and a connection dropped in the
#   middle would abandon a VM that is still being created and still billing.
#
#   The request id is made here rather than read from the response: it has to be
#   known even if the connection drops before the 202 arrives, which is also what
#   makes the inflight marker below useful.
# ---------------------------------------------------------------------------
BT_REQUEST_ID=""
bt_async() {
	local method="$1" path="$2" body="${3:-}"
	BT_REQUEST_ID="${MATRIX_NAME_PREFIX:-cpbfs}-$(date +%s)-$RANDOM"
	bt_header 'Prefer: respond-async'
	bt_header "X-Request-Id: ${BT_REQUEST_ID}"
	bt_request "$method" "$path" "$body" || return 1
	# Anything but 202 means the server finished it synchronously — nothing to wait for.
	[ "$BT_STATUS" = "202" ] && printf '%s' "$BT_REQUEST_ID"
	return 0
}

# bt_wait REQ_ID LABEL TIMEOUT — wait for an async call. An empty id is already done.
bt_wait() {
	local req_id="$1" label="$2" timeout="$3" waited=0 status detail line
	local interval="${NODE_POLL_INTERVAL:-10}"
	if [ -z "$req_id" ]; then
		ok "$label done"
		return 0
	fi
	while [ "$waited" -lt "$timeout" ]; do
		QUIET_API_LOG=1
		bt_get "/request/$(urlq "$req_id")" && status="$(bt_data '.status')" || status="pending"
		QUIET_API_LOG=0
		case "$status" in
		Success)
			printf '\r%-110s\n' "" >&2
			ok "$label done ($(secs_fmt "$waited"))"
			return 0 ;;
		Error)
			# errorResponse carries the CSP's own sentence. Printed line by line and
			# uncut — cutting loses the end, which is where the cause is.
			detail="$(bt_data '.errorResponse')"
			printf '\r%-110s\n' "" >&2
			fail "$label failed"
			fail "  request : GET ${BT_LAST_URL:-?}  →  status Error"
			fail "  beetle says:"
			while IFS= read -r line; do
				fail "    $line"
			done <<< "$(printf '%s' "${detail:-(errorResponse was empty)}" | sed 's/\\n/\n/g; s/\\t/\t/g')"
			return 1 ;;
		esac
		printf '\r    %-100s' "$label — ${status:-Handling} ($(secs_fmt "$waited"))" >&2
		sleep "$interval"
		waited=$((waited + interval))
	done
	printf '\r%-110s\n' "" >&2
	fail "$label timed out after ${timeout}s. The resource may still be being created."
	return 1
}

# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Naming
# ---------------------------------------------------------------------------
# Every resource is <prefix>-<csp>-<what>. The CSP is in the name because one
# namespace is shared by several CSPs and cb-tumblebug's resource ids are unique
# per namespace, not per connection — a plain cpbfs-infra would mean the AWS node
# and the NCP node at once, and a per-CSP cleanup would delete the wrong one.
#
# This is also what cleanup_all recognises its own resources by. There is no
# state file, so the name is the record.
resource_name() { printf '%s-%s-%s' "$MATRIX_NAME_PREFIX" "$(lower "$1")" "$2"; }

infra_name()  { resource_name "$1" "infra"; }
vnet_name()   { resource_name "$1" "vnet"; }
sg_name()     { resource_name "$1" "sg"; }
sshkey_name() { resource_name "$1" "sshkey"; }
subnet_name() { resource_name "$1" "subnet-$2"; }

# connection_name CSP — cb-tumblebug names a connection "<csp>-<region>", lower
#   case. The region comes from .env and has to match what `make init` registered.
connection_name() { lower "$(printf '%s-%s' "$1" "$(csp_env "$1" REGION)")"; }

_infra_path() { printf '/migration/ns/%s/infra' "$(urlq "$MATRIX_NS")"; }
_res_path()   { printf '/migration/ns/%s/resources/%s' "$(urlq "$MATRIX_NS")" "$1"; }

# ---------------------------------------------------------------------------
# Pre-flight and namespace
# ---------------------------------------------------------------------------
beetle_preflight() {
	if ! bt_get "/readyz"; then
		bt_report "cannot reach cm-beetle: $BEETLE_URL"
		die "This folder starts neither beetle nor tumblebug — they must already be up."
	fi
	if ! tb_get "/readyz"; then
		bt_report "cannot reach cb-tumblebug: $TUMBLEBUG_URL"
		die "beetle hands every resource call to tumblebug, so nothing would work."
	fi
	ok "cm-beetle and cb-tumblebug answer"
}

# assert_connection CSP — is the connection actually registered, before anything
#   is created? Caught here, we can say both what is missing and what is there.
assert_connection() {
	local csp="$1" conn names
	conn="$(connection_name "$csp")"
	if tb_get "/connConfig/$(urlq "$conn")"; then
		info "connection confirmed: $conn"
		return 0
	fi
	bt_report "cb-tumblebug has no connection called '$conn'"
	fail "  Check $(upper "$csp")_REGION matches the registered region (currently: $(csp_env "$csp" REGION))."
	fail "  Credentials are registered by the deployments stack, not by this folder:"
	fail "    cd ../.. && make init"
	names="$(tb_get "/connConfig" >/dev/null 2>&1 && bt_jq -r \
		--arg p "$(lower "$csp")" '[.connectionconfig[]? | select(.configName | startswith($p)) | .configName] | join(", ")')"
	[ -n "$names" ] && fail "  Registered $(lower "$csp") connections: $names"
	return 1
}

# assert_vm_zone CSP — the node's zone has to be the connection's zone.
#
#   A node always lands in the first subnet (inject_infra_network), so this checks
#   <CSP>_ZONE alone. cb-spider looks a VM spec up per zone, and the zone it uses
#   is the one the CONNECTION is assigned to, not the one the subnet is in. When
#   the two disagree, beetle recommends a spec that exists in the connection's
#   zone and the node is created in a subnet where it does not. NCP answers that
#   with returnCode 3010022, "An error occurred during the requested server
#   operation. Contact Customer Center", which names neither the zone nor the
#   spec — a long read for a two-line .env fix, and the node is billing meanwhile.
#
#   Anything unreadable here returns 0: a check that cannot run must not be a
#   check that blocks.
assert_vm_zone() {
	local csp="$1" conn zone assigned

	conn="$(connection_name "$csp")"
	zone="$(csp_env "$csp" ZONE)"
	[ -n "$zone" ] || return 0

	tb_get "/connConfig/$(urlq "$conn")" || return 0
	assigned="$(printf '%s' "$BT_BODY" | jq -r '.regionZoneInfo.assignedZone // empty' 2>/dev/null || true)"
	[ -n "$assigned" ] || return 0
	[ "$(lower "$assigned")" = "$(lower "$zone")" ] && return 0

	fail "the node would go in ${zone}, but connection ${conn} is assigned to ${assigned}."
	fail "  cb-spider looks a VM spec up in the connection's zone, so the spec beetle"
	fail "  recommends is not offered where the node is being placed. The CSP rejects that"
	fail "  with an error that names neither zone nor spec."
	fail "  The node always uses the first subnet, so swap the two zones:"
	fail "    $(upper "$csp")_ZONE=${assigned}"
	fail "    $(upper "$csp")_ZONE2=${zone}"
	fail "  A vNet that already exists keeps the zones it was built with, so remove it too:"
	fail "    ./scripts/fs-matrix.sh --cleanup"
	return 1
}

ensure_namespace() {
	local body
	if tb_get "/ns/$(urlq "$MATRIX_NS")" >/dev/null 2>&1; then
		info "namespace $MATRIX_NS — already there"
		return 0
	fi
	body="$(jq -n --arg name "$MATRIX_NS" \
		'{name:$name, description:"cm-centipede filesystem migration matrix (beetle)"}')"
	if ! tb_post "/ns" "$body"; then
		bt_report "could not create the namespace: $MATRIX_NS"
		return 1
	fi
	ok "namespace created: $MATRIX_NS"
}

# ---------------------------------------------------------------------------
# Inflight markers — the one thing the list API cannot tell us
# ---------------------------------------------------------------------------
# A create whose request reached the CSP but whose record never reached
# cb-tumblebug appears in no list, so cleanup_all cannot find it. bt_async
# already sends a request id of our own making so it can be followed even if the
# 202 never arrives; this writes that id down.
#
# It is not a state file. It records intent for the minutes a create is in
# flight, and nothing reads it to decide what exists — the list API does that.
#
# ⚠ This matters more here than in the object storage matrix. An orphaned bucket
#   is a line on an inventory; an orphaned VM runs until someone notices.
INFLIGHT_FILE=""

inflight_file() {
	INFLIGHT_FILE="${LOG_DIR:-$BEETLE_ROOT/logs}/.inflight-$(lower "$1").json"
	printf '%s' "$INFLIGHT_FILE"
}

# inflight_mark CSP NAME REQ_ID KIND
inflight_mark() {
	local f tmp
	f="$(inflight_file "$1")"
	mkdir -p "$(dirname "$f")" 2>/dev/null || return 0
	[ -f "$f" ] || echo '[]' > "$f"
	tmp="${f}.tmp"
	jq --arg n "$2" --arg r "${3:-}" --arg k "$4" --arg t "$(date '+%F %T')" \
		'[ .[] | select(.name != $n) ] + [{name:$n, requestId:$r, kind:$k, startedAt:$t}]' \
		"$f" > "$tmp" 2>/dev/null && mv -f "$tmp" "$f"
	rm -f "$tmp"
	return 0
}

# inflight_clear CSP NAME
inflight_clear() {
	local f tmp
	f="$(inflight_file "$1")"
	[ -f "$f" ] || return 0
	tmp="${f}.tmp"
	jq --arg n "$2" '[ .[] | select(.name != $n) ]' "$f" > "$tmp" 2>/dev/null && mv -f "$tmp" "$f"
	rm -f "$tmp"
	return 0
}

# inflight_report CSP "NAME1 NAME2 …" — names the list API did return.
#   Anything marked but absent from that list is reported, never deleted: we
#   cannot delete what cb-tumblebug has no record of, and guessing it does not
#   exist is exactly the mistake that leaves a VM running.
inflight_report() {
	local csp="$1" seen="$2" f name req kind started
	f="$(inflight_file "$csp")"
	[ -f "$f" ] || return 0
	while IFS=$'\t' read -r name req kind started; do
		[ -n "$name" ] || continue
		case " $seen " in *" $name "*) continue ;; esac
		fail ""
		fail "⚠ $name — a create was requested but cb-tumblebug has no record of it."
		fail "    kind ${kind:-?}   requested at ${started:-?}"
		fail "    request id    ${req:-(none)}"
		[ -n "$req" ] && \
		fail "    curl -s '${BEETLE_URL%/}/request/$req' -u '<user>:<pass>' | jq ."
		fail "    Check the $(upper "$csp") console (region $(csp_env "$csp" REGION)) before assuming it"
		fail "    does not exist. A VM that was created but never recorded keeps billing, and"
		fail "    nothing here can reach it — it has to go from the console."
		fail "    Once handled, remove the entry: $f"
	done <<< "$(jq -r '.[] | [.name, .requestId, .kind, .startedAt] | @tsv' "$f" 2>/dev/null)"
	return 0
}

# ---------------------------------------------------------------------------
# The network every node in one CSP shares
# ---------------------------------------------------------------------------
# Created on the way to the CSP's node and released once that node is gone.
# Idempotent: a second call finds what the first made and publishes the same ids,
# so a re-run after an interrupted one adopts the network instead of failing.
NET_VNET_ID=""
NET_SUBNET_IDS=""      # JSON array, in subnet order
NET_SG_ID=""
NET_SSHKEY_ID=""

# subnet_cidr CSP INDEX — an explicit <CSP>_SUBNET<n>_CIDR wins. Otherwise the
#   two /24s are derived from the vNet block by replacing the third octet, which
#   holds for the /16 .env.example ships. A block smaller than a /16 needs the
#   explicit keys, and saying so beats silently deriving a subnet outside it.
subnet_cidr() {
	local csp="$1" index="$2" explicit base prefix
	explicit="$(csp_env "$csp" "SUBNET${index}_CIDR")"
	if [ -n "$explicit" ]; then printf '%s' "$explicit"; return 0; fi

	base="$(csp_env "$csp" VNET_CIDR)"
	prefix="${base#*/}"
	if [ "$prefix" != "16" ]; then
		die "$(upper "$csp")_VNET_CIDR is /${prefix}, and the two subnet blocks are only
       derived automatically from a /16. Set $(upper "$csp")_SUBNET1_CIDR and
       _SUBNET2_CIDR explicitly in $ENV_FILE."
	fi
	printf '%s' "$base" | awk -F'[./]' -v i="$index" '{ printf "%s.%s.%s.0/24", $1, $2, i }'
}

# firewall_rules CSP — SSH inbound, and nothing else.
#
#   22 is the whole requirement: cm-centipede rsyncs over SSH and checksums over
#   SSH, and there is no second port because the node is a plain destination, not
#   a service. The JSON keys are PascalCase because that is what cb-tumblebug's
#   FirewallRuleReq declares; lower case ones are silently dropped, which produces
#   a security group with no rules in it — a node that exists and cannot be
#   reached, which reads exactly like a network problem.
#
# ⚠ FS_ALLOWED_CIDR defaults to 0.0.0.0/0 because the address cm-centipede leaves
#   from is not something this folder can know: it is the docker host's egress
#   address, NAT'd, and it changes. Narrow it when you know it. Local testing
#   only, either way.
firewall_rules() {
	jq -n --arg cidr "${FS_ALLOWED_CIDR:-0.0.0.0/0}" \
		'[ { Ports: "22", Protocol: "TCP", Direction: "inbound", CIDR: $cidr } ]'
}

# find_vnet CSP — publish NET_VNET_ID and NET_SUBNET_IDS when the vNet exists.
find_vnet() {
	local name subnets
	name="$(vnet_name "$1")"
	bt_get "$(_res_path vNet)" || return 1
	NET_VNET_ID="$(bt_jq -r --arg n "$name" '.vNet[]? | select(.name == $n) | .id // empty')"
	[ -n "$NET_VNET_ID" ] || return 1
	# Subnet order matters: the node always takes the first.
	subnets="$(bt_jq -c --arg n "$name" '[ .vNet[]? | select(.name == $n) | .subnetInfoList[]? | .id ]')"
	NET_SUBNET_IDS="${subnets:-[]}"
	return 0
}

find_sg() {
	local name
	name="$(sg_name "$1")"
	bt_get "$(_res_path securityGroup)" || return 1
	NET_SG_ID="$(bt_jq -r --arg n "$name" '.securityGroup[]? | select(.name == $n) | .id // empty')"
	[ -n "$NET_SG_ID" ]
}

find_sshkey() {
	local name
	name="$(sshkey_name "$1")"
	bt_get "$(_res_path sshKey)" || return 1
	NET_SSHKEY_ID="$(bt_jq -r --arg n "$name" '.sshKey[]? | select(.name == $n) | .id // empty')"
	[ -n "$NET_SSHKEY_ID" ]
}

ensure_vnet() {
	local csp="$1" body conn
	if find_vnet "$csp"; then
		info "vNet $(vnet_name "$csp") already exists ($NET_VNET_ID)"
		return 0
	fi
	conn="$(connection_name "$csp")"
	body="$(jq -n \
		--arg name "$(vnet_name "$csp")" --arg conn "$conn" \
		--arg cidr "$(csp_env "$csp" VNET_CIDR)" \
		--arg s1 "$(subnet_name "$csp" 1)" --arg c1 "$(subnet_cidr "$csp" 1)" --arg z1 "$(csp_env "$csp" ZONE)" \
		--arg s2 "$(subnet_name "$csp" 2)" --arg c2 "$(subnet_cidr "$csp" 2)" --arg z2 "$(csp_env "$csp" ZONE2)" \
		'{ name:$name, connectionName:$conn, cidrBlock:$cidr,
		   description:"Created by beetle-fs-test",
		   subnetInfoList:[ {name:$s1, ipv4_CIDR:$c1, zone:$z1},
		                    {name:$s2, ipv4_CIDR:$c2, zone:$z2} ] }')"

	step "creating vNet $(vnet_name "$csp") ($(csp_env "$csp" ZONE), $(csp_env "$csp" ZONE2))"
	if ! bt_post "$(_res_path vNet)" "$body"; then
		bt_report "could not create the vNet"
		return 1
	fi
	if ! find_vnet "$csp"; then
		fail "the vNet was created but cannot be read back — check namespace $MATRIX_NS"
		return 1
	fi
	ok "vNet $(vnet_name "$csp") ($NET_VNET_ID)"
}

ensure_sg() {
	local csp="$1" body
	if find_sg "$csp"; then
		info "security group $(sg_name "$csp") already exists ($NET_SG_ID)"
		return 0
	fi
	body="$(jq -n \
		--arg name "$(sg_name "$csp")" --arg conn "$(connection_name "$csp")" \
		--arg vnet "$NET_VNET_ID" --argjson rules "$(firewall_rules "$csp")" \
		'{ name:$name, connectionName:$conn, vNetId:$vnet,
		   description:"Created by beetle-fs-test", firewallRules:$rules }')"

	step "creating security group $(sg_name "$csp") (SSH from ${FS_ALLOWED_CIDR:-0.0.0.0/0})"
	if ! bt_post "$(_res_path securityGroup)" "$body"; then
		bt_report "could not create the security group"
		return 1
	fi
	if ! find_sg "$csp"; then
		fail "the security group was created but cannot be read back"
		return 1
	fi
	ok "security group $(sg_name "$csp") ($NET_SG_ID)"
}

# ensure_sshkey CSP — the key pair beetle gives the node.
#
#   Not this folder's source key: that one opens the source container. This is
#   the node's, created by cb-tumblebug, and cm-centipede reads its private half
#   back through beetle when it resolves the beetleSsh reference. Nothing here
#   ever holds it — node_probe asks beetle for it when it needs to log in.
ensure_sshkey() {
	local csp="$1" body
	if find_sshkey "$csp"; then
		info "SSH key $(sshkey_name "$csp") already exists ($NET_SSHKEY_ID)"
		return 0
	fi
	body="$(jq -n --arg name "$(sshkey_name "$csp")" --arg conn "$(connection_name "$csp")" \
		'{ name:$name, connectionName:$conn, description:"Created by beetle-fs-test" }')"
	step "creating SSH key $(sshkey_name "$csp")"
	if ! bt_post "$(_res_path sshKey)" "$body"; then
		bt_report "could not create the SSH key"
		return 1
	fi
	if ! find_sshkey "$csp"; then
		fail "the SSH key was created but cannot be read back"
		return 1
	fi
	ok "SSH key $(sshkey_name "$csp") ($NET_SSHKEY_ID)"
}

# ensure_network CSP — the entry point.
ensure_network() {
	local csp="$1"
	NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""; NET_SSHKEY_ID=""
	ensure_vnet "$csp"   || return 1
	ensure_sg "$csp"     || return 1
	ensure_sshkey "$csp" || return 1
	return 0
}

# release_network CSP — security group first, then the key, then the vNet.
#
#   Order is not a preference. The security group references the vNet and every
#   CSP refuses to delete a vNet while something is attached to it, so a vNet-first
#   teardown leaves both behind and reports only the vNet's failure.
release_network() {
	local csp="$1" rc=0
	if find_sg "$csp"; then
		step "deleting security group $(sg_name "$csp")"
		if bt_delete "$(_res_path securityGroup)/$(urlq "$NET_SG_ID")"; then
			ok "deleted security group $(sg_name "$csp")"
		elif bt_absent; then
			info "security group $(sg_name "$csp") is already gone"
		else
			bt_report_warn "could not delete security group $(sg_name "$csp")"; rc=1
		fi
	fi

	if find_sshkey "$csp"; then
		step "deleting SSH key $(sshkey_name "$csp")"
		if bt_delete "$(_res_path sshKey)/$(urlq "$NET_SSHKEY_ID")"; then
			ok "deleted SSH key $(sshkey_name "$csp")"
		elif bt_absent; then
			info "SSH key $(sshkey_name "$csp") is already gone"
		else
			bt_report_warn "could not delete SSH key $(sshkey_name "$csp")"; rc=1
		fi
	fi

	if find_vnet "$csp"; then
		step "deleting vNet $(vnet_name "$csp")"
		# withsubnets is tumblebug's default action and is spelled out because a
		# vNet with subnets left in it cannot be deleted.
		if bt_delete "$(_res_path vNet)/$(urlq "$NET_VNET_ID")?action=withsubnets"; then
			ok "deleted vNet $(vnet_name "$csp")"
		elif bt_absent; then
			info "vNet $(vnet_name "$csp") is already gone"
		else
			bt_report_warn "could not delete vNet $(vnet_name "$csp")"
			bt_in_use && warn "  Something is still attached to it — a node this run did not remove."
			rc=1
		fi
	fi

	NET_VNET_ID=""; NET_SUBNET_IDS=""; NET_SG_ID=""; NET_SSHKEY_ID=""
	return "$rc"
}

# ---------------------------------------------------------------------------
# The migrated node
# ---------------------------------------------------------------------------
# Read out of the infra response and used by the cell.
NODE_ID=""; NODE_NAME=""; NODE_PUBLIC_IP=""; NODE_SSH_PORT=""; NODE_USER=""
NODE_SSHKEY_ID=""; NODE_STATUS=""
NODE_HOME=""; NODE_HAS_RSYNC=""

# The source machine beetle is told about. It never reaches a CSP — the real
# network is the one above — but the recommendation reads it, and an absent or
# empty block is answered with a 500 rather than a validation error.
FS_SOURCE_CIDR="192.168.0.0/24"

# infra_request_body CSP — the source description the recommendation turns into a
#   concrete target node.
#
#   The OS fields that matter are id and versionId, not prettyName: beetle builds
#   its image search key as `node.OS.ID + " " + node.OS.VersionID`
#   (pkg/core/recommendation/resource-node-image.go) and never reads prettyName.
#
#   The defaults are here rather than in .env because an empty value has to
#   describe something — a node with no OS is answered with no image at all.
infra_request_body() {
	local csp="$1" os_raw os_id os_ver arch
	os_raw="$(lower "$(csp_env "$csp" NODE_OS)")"
	[ -n "$os_raw" ] || os_raw="ubuntu 22.04"
	os_id="${os_raw%% *}"
	os_ver=""
	[ "$os_raw" != "$os_id" ] && os_ver="${os_raw#* }"
	arch="$(csp_env "$csp" NODE_ARCH)"
	[ -n "$arch" ] || arch="x86_64"

	jq -nc \
		--arg csp "$csp" --arg region "$(csp_env "$csp" REGION)" \
		--argjson cores "$(csp_env_int "$csp" NODE_VCPU 2)" \
		--argjson memory "$(csp_env_int "$csp" NODE_MEMORY_GB 4)" \
		--argjson disk "$(csp_env_int "$csp" NODE_DISK_GB 20)" \
		--arg osid "$os_id" --arg osver "$os_ver" --arg arch "$arch" \
		--arg cidr "$FS_SOURCE_CIDR" \
		'{
		  desiredCspAndRegionPair: { csp: $csp, region: $region },
		  onpremiseInfraModel: {
		    network: { ipv4Networks: { cidrBlocks: [ $cidr ] } },
		    nodes: [ {
		      hostname:  "beetle-fs-test-source-01",
		      machineId: "beetle-fs-test-source-01",
		      role:      "standalone",
		      cpu:      { architecture: $arch, cpus: 1, cores: $cores, threads: $cores },
		      memory:   { type: "DDR4", totalSize: $memory },
		      rootDisk: { label: "root", type: "SSD", totalSize: $disk },
		      os: { id: $osid, versionId: $osver,
		            prettyName: ($osid + (if $osver == "" then "" else " " + $osver end)) }
		    } ]
		  }
		}'
}

# recommend_infra CSP — the target node specification, on stdout.
recommend_infra() {
	local csp="$1" body result region
	region="$(csp_env "$csp" REGION)"
	body="$(infra_request_body "$csp")"

	# limit=1: this folder wants one node, not a shortlist to choose between.
	if ! bt_post "/recommendation/infra?desiredCsp=$(urlq "$csp")&desiredRegion=$(urlq "$region")&limit=1" "$body"; then
		bt_report "the infrastructure recommendation failed"
		return 1
	fi

	# An empty candidate list comes back as a 200 with no "data" key at all —
	# beetle's ApiResponse declares Data with omitempty, so a zero-length slice is
	# omitted rather than sent as [].
	result="$(bt_jq -c '.[0] // empty')"
	if [ -z "$result" ]; then
		fail "cm-beetle found no VM spec and OS image pair for $csp $region, so it"
		fail "  recommended nothing. It answers 200 with an empty list rather than an error,"
		fail "  which is why this is reported here and not by the call itself."
		fail "  The usual cause is that cb-tumblebug has no spec or image catalogue for this"
		fail "  connection yet: it loads those on initialisation and the load takes several"
		fail "  minutes. A failed or interrupted 'make init' leaves it empty."
		fail "    ./scripts/fs-support.sh   prints what the catalogue holds"
		fail "  If both list entries, the node profile asks for something the catalogue cannot"
		fail "  match — lower $(upper "$csp")_NODE_VCPU / _NODE_MEMORY_GB, or check"
		fail "  $(upper "$csp")_NODE_OS names a distribution the CSP offers."
		return 1
	fi
	printf '%s' "$BT_BODY" | jq -r '(.. | objects | select(has("warnings")) | .warnings[]?) // empty' 2>/dev/null \
		| while IFS= read -r w; do [ -n "$w" ] && warn "  recommendation: $w"; done
	printf '%s' "$result"
}

# inject_infra_network RECOMMENDATION CSP — point it at our network, and name
#   everything ourselves.
#
#   With useExisting=true the ids below are what beetle matches against; it
#   creates from targetVNet/targetSecurityGroupList only if the match fails.
inject_infra_network() {
	local rec="$1" csp="$2"
	printf '%s' "$rec" | jq -c \
		--arg vnet "$(vnet_name "$csp")" \
		--arg sub1 "$(subnet_name "$csp" 1)" --arg sub2 "$(subnet_name "$csp" 2)" \
		--arg sg "$(sg_name "$csp")" --arg sshkey "$(sshkey_name "$csp")" \
		--arg infra "$(infra_name "$csp")" --arg conn "$(connection_name "$csp")" \
		--arg cidr "$(csp_env "$csp" VNET_CIDR)" \
		--arg c1 "$(subnet_cidr "$csp" 1)" --arg z1 "$(csp_env "$csp" ZONE)" \
		--arg c2 "$(subnet_cidr "$csp" 2)" --arg z2 "$(csp_env "$csp" ZONE2)" \
		--argjson rules "$(firewall_rules "$csp")" \
		'
		.targetVNet = {
			name: $vnet, connectionName: $conn, cidrBlock: $cidr,
			description: "Created by beetle-fs-test",
			subnetInfoList: [ {name:$sub1, ipv4_CIDR:$c1, zone:$z1},
			                  {name:$sub2, ipv4_CIDR:$c2, zone:$z2} ]
		}
		| .targetSshKey.name = $sshkey
		| .targetSshKey.connectionName = $conn
		| .targetSecurityGroupList = [ {
			name: $sg, connectionName: $conn, vNetId: $vnet,
			description: "Created by beetle-fs-test", firewallRules: $rules
		} ]
		| .targetInfra.name = $infra
		| .targetInfra.nodeGroups = [ .targetInfra.nodeGroups[] | (
		      .vNetId = $vnet
		    | .subnetId = $sub1
		    | .subnetIds = [$sub1]
		    | .securityGroupIds = [$sg]
		    | .sshKeyId = $sshkey
		  ) ]'
}

# node_info CSP — read the infra into NODE_*. 1 when it is not there.
#
#   ⚠ The single-infra GET is the one that carries nodeUserName. The list view
#     (GET .../infra) is served from an in-memory store and leaves it out on
#     purpose, along with the SSH host key info — so reading the list here would
#     produce a node with no user and a cell that fails at the plan.
node_info() {
	local csp="$1" name
	name="$(infra_name "$csp")"
	NODE_ID=""; NODE_NAME=""; NODE_PUBLIC_IP=""; NODE_SSH_PORT=""; NODE_USER=""
	NODE_SSHKEY_ID=""; NODE_STATUS=""

	bt_get "$(_infra_path)/$(urlq "$name")" || return 1
	NODE_STATUS="$(bt_data '.status')"
	NODE_ID="$(bt_jq -r '.node[0].id // empty')"
	NODE_NAME="$(bt_jq -r '.node[0].name // empty')"
	NODE_PUBLIC_IP="$(bt_jq -r '.node[0].publicIP // empty')"
	NODE_SSH_PORT="$(bt_jq -r '.node[0].sshPort // empty')"
	NODE_USER="$(bt_jq -r '.node[0].nodeUserName // empty')"
	NODE_SSHKEY_ID="$(bt_jq -r '.node[0].sshKeyId // empty')"
	[ -n "$NODE_SSH_PORT" ] || NODE_SSH_PORT=22
	[ -n "$NODE_ID" ]
}

# node_private_key — the node's private key, from cm-beetle, on stdout.
#
#   The sshKey endpoint is a transparent proxy onto cb-tumblebug, so the key
#   comes through unmasked. This is the same pair of calls cm-centipede makes to
#   resolve a beetleSsh reference (pkg/client/beetle GetSSHAccessInfo); doing it
#   here first means a key centipede could not use is found by the probe rather
#   than by the transfer.
node_private_key() {
	[ -n "$NODE_SSHKEY_ID" ] || return 1
	bt_get "$(_res_path sshKey)/$(urlq "$NODE_SSHKEY_ID")" || return 1
	bt_data '.privateKey'
}

# create_node CSP — recommend, inject, create, wait. Fills NODE_* on success.
create_node() {
	local csp="$1" name rec body req
	name="$(infra_name "$csp")"

	# Already there from an interrupted run? Adopt it rather than failing: it costs
	# money whether or not this run made it, and adopting is what makes a re-run
	# after Ctrl-C cheap instead of leaving two.
	if node_info "$csp"; then
		info "infra $name already exists (status ${NODE_STATUS:-?}) — reusing it"
		return 0
	fi

	rec="$(recommend_infra "$csp")" || return 1
	printf '%s' "$rec" | jq -r '.targetInfra.nodeGroups[0] |
		"    spec      \(.specId // "-")",
		"    image     \(.imageId // "-")",
		"    root disk \(if (.rootDiskSize // 0) == 0 then "(CSP default)" else "\(.rootDiskSize)GB" end)"' 2>/dev/null

	body="$(inject_infra_network "$rec" "$csp")"

	# Marked before the call, not after: the point of the marker is the window in
	# which a create has reached the CSP and no record has reached tumblebug.
	inflight_mark "$csp" "$name" "" "infra"

	# useExisting=true is what makes beetle adopt the vNet, subnet, security group
	# and SSH key named in the body instead of creating a second set.
	step "creating the target node: $name"
	req="$(bt_async POST "$(_infra_path)?useExisting=true" "$body")" || {
		bt_report "could not ask for the node: $name"
		return 1
	}
	[ -n "$req" ] && inflight_mark "$csp" "$name" "$req" "infra"

	if ! bt_wait "$req" "node $name" "${NODE_TIMEOUT:-1800}"; then
		fail "  The node may still be being created. It is left in place for --cleanup"
		fail "  to reclaim rather than deleted from under a create that is still running."
		return 1
	fi
	inflight_clear "$csp" "$name"

	if ! node_info "$csp"; then
		bt_report "the node was created but cannot be read back: $name"
		return 1
	fi
	ok "node $NODE_NAME ($NODE_ID)  status ${NODE_STATUS:-?}  public ${NODE_PUBLIC_IP:-none}"
	return 0
}

# node_wait_ssh CSP — until beetle says every node answers SSH.
#
#   ⚠ The endpoint is rate limited by beetle itself: one check per infrastructure
#     every 30 seconds, 429 in between (middlewares/ssh-check-cooldown.go). Polling
#     faster than that spends every other request on a rejection, which is why the
#     interval here is 35 and not POLL_INTERVAL.
node_wait_ssh() {
	local csp="$1" name waited=0 ready total interval=35
	name="$(infra_name "$csp")"
	while [ "$waited" -lt "${SSH_READY_TIMEOUT:-900}" ]; do
		QUIET_API_LOG=1
		if bt_get "$(_infra_path)/$(urlq "$name")/ssh-ready"; then
			ready="$(bt_data '.ready')"
			total="$(bt_data '.readyNodes')/$(bt_data '.totalNodes')"
		else
			ready="false"; total="$(bt_raw_message | head -1 | cut -c1-60)"
		fi
		QUIET_API_LOG=0
		if [ "$ready" = "true" ]; then
			printf '\r%-110s\r' "" >&2
			ok "SSH is up on $name ($(secs_fmt "$waited"))"
			return 0
		fi
		printf '\r    %-100s' "node $name — waiting for SSH: ${total:-?} ($(secs_fmt "$waited"))" >&2
		sleep "$interval"
		waited=$((waited + interval))
	done
	printf '\r%-110s\r' "" >&2
	warn "beetle still does not report SSH as ready on $name after $(secs_fmt "$waited")."
	warn "  Carrying on anyway — the probe below is the check that actually matters,"
	warn "  and a CSP that is slow to report is not the same as one that is not listening."
	return 0
}

# node_probe CSP — the one SSH session this folder opens to the node itself.
#
#   It answers three questions at once, which is the whole reason it exists:
#
#     $HOME     where the migrated tree can go. Built from beetle's nodeUserName
#               a destination would assume the home directory follows the user
#               name, and CSP images disagree about that often enough to matter.
#               This is what becomes the plan's dstName.
#     rsync     whether the transfer can happen at all. transx-ex shells out to
#               `rsync -e ssh`, and rsync has to exist on BOTH ends. Without this
#               check a missing rsync fails every cell identically, minutes into
#               a transfer, with an error about a closed connection.
#     the key   whether the beetleSsh reference resolves. These are the same two
#               beetle calls cm-centipede makes (GetSSHAccessInfo), so a key or a
#               user name beetle cannot supply is found here rather than in the
#               middle of a migration.
#
#   Failure is the cell's failure. Everything it checks is a precondition of the
#   transfer, so there is nothing to be gained by going on.
node_probe() {
	local csp="$1" key out rc
	NODE_HOME=""; NODE_HAS_RSYNC=""

	if [ -z "$NODE_PUBLIC_IP" ]; then
		fail "the node has no public IP, so nothing can reach it."
		fail "  cm-centipede reaches a beetleSsh target directly — there is no bastion path"
		fail "  (pkg/client/beetle/client.go says so in as many words). Check that the"
		fail "  recommendation asked for a public address on this CSP."
		return 1
	fi
	if [ -z "$NODE_USER" ]; then
		fail "cm-beetle reports no nodeUserName for $NODE_ID."
		fail "  centipede builds its SSH login from exactly this field, so the migration"
		fail "  would fail with an empty user. Read it yourself:"
		fail "    curl -s '${BEETLE_URL%/}$(_infra_path)/$(infra_name "$csp")' -u '<user>:<pass>' | jq '.node[0]'"
		return 1
	fi

	key="${MATRIX_TMP:-/tmp}/node-$(lower "$csp").key"
	if ! node_private_key > "$key" 2>/dev/null || [ ! -s "$key" ]; then
		rm -f "$key"
		fail "cm-beetle returned no private key for sshKey ${NODE_SSHKEY_ID:-?}."
		fail "  centipede resolves a beetleSsh reference with the same call, so it could not"
		fail "  log in either."
		return 1
	fi
	chmod 600 "$key"

	# Retried: beetle's ssh-ready can go true a moment before sshd accepts a key,
	# and a first-boot cloud-init can still be writing authorized_keys.
	local attempt=0
	while :; do
		out="$(ssh -i "$key" -p "${NODE_SSH_PORT:-22}" \
			-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
			-o BatchMode=yes -o ConnectTimeout=15 -o LogLevel=ERROR \
			"$NODE_USER@$NODE_PUBLIC_IP" \
			'printf "HOME=%s\n" "$HOME"; if command -v rsync >/dev/null 2>&1; then echo RSYNC=yes; else echo RSYNC=no; fi' \
			2>&1)"
		rc=$?
		[ "$rc" -eq 0 ] && break
		attempt=$((attempt + 1))
		if [ "$attempt" -ge "${NODE_PROBE_RETRIES:-6}" ]; then
			rm -f "$key"
			fail "could not log in to the node over SSH: $NODE_USER@$NODE_PUBLIC_IP:${NODE_SSH_PORT:-22}"
			fail "  ssh says: $(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
			fail "  The security group opens 22 to ${FS_ALLOWED_CIDR:-0.0.0.0/0}; if that was"
			fail "  narrowed, it has to include the address THIS host leaves from."
			return 1
		fi
		printf '\r    %-100s' "node — waiting for sshd to accept the key (attempt $attempt)" >&2
		sleep 10
	done
	printf '\r%-110s\r' "" >&2
	rm -f "$key"

	NODE_HOME="$(printf '%s' "$out" | sed -n 's/^HOME=//p' | head -1)"
	NODE_HAS_RSYNC="$(printf '%s' "$out" | sed -n 's/^RSYNC=//p' | head -1)"

	if [ -z "$NODE_HOME" ]; then
		fail "the node did not report a home directory."
		fail "  ssh returned: $(printf '%s' "$out" | head -3 | tr '\n' ' ')"
		return 1
	fi
	if [ "$NODE_HAS_RSYNC" != "yes" ]; then
		fail "rsync is not installed on the node ($NODE_USER@$NODE_PUBLIC_IP)."
		fail "  transx-ex transfers a filesystem by shelling out to \`rsync -e ssh\`, so it has"
		fail "  to be present on both ends. Every cell on this CSP would fail the same way,"
		fail "  minutes into a transfer, with an error about the connection closing."
		fail "  Either pick an image that ships it ($(upper "$csp")_NODE_OS), or install it"
		fail "  on the node and re-run:"
		fail "    ssh $NODE_USER@$NODE_PUBLIC_IP 'sudo apt-get install -y rsync'"
		return 1
	fi

	ok "node probe: home $NODE_HOME   rsync present   user $NODE_USER"
	return 0
}

# node_dst_path — where this cell's data goes, on stdout.
#   $HOME plus one directory. Not "/testdata": the node's own user cannot create a
#   directory at the filesystem root, and transx-ex's remote mkdir -p carries no
#   sudo (storagex/executor-rsync.go ensureRemoteDir).
node_dst_path() {
	if [ -n "${FS_DST_PATH:-}" ]; then printf '%s' "$FS_DST_PATH"; return 0; fi
	printf '%s/%s' "${NODE_HOME%/}" "${FS_DST_DIRNAME:-testdata}"
}

# delete_node CSP [NAME] — terminate and remove the infrastructure.
#
#   NAME defaults to this CSP's own infra name, which is what a cell deletes: it
#   created exactly that one. cleanup_all is the caller that passes a name,
#   because it deletes what it found in the namespace rather than what this run
#   would have made — the two are the same today, and assuming so would mean
#   deleting the canonical name once per match and leaving any other prefixed
#   leftover in place, billing.
#
#   option=terminate rather than a plain delete: cb-tumblebug refuses to remove an
#   infrastructure whose nodes are still running, and "running" is the state every
#   node is in when a cell ends. force is the fallback, and it is a real fallback
#   rather than the default — it drops tumblebug's record whether or not the CSP
#   confirmed, which is how a VM ends up billing with nothing tracking it.
delete_node() {
	local csp="$1" name="${2:-}" attempt=0
	local retries="${DELETE_RETRIES:-3}" wait="${DELETE_RETRY_WAIT:-30}"
	[ -n "$name" ] || name="$(infra_name "$csp")"

	step "deleting the target node: $name"
	while :; do
		if bt_delete "$(_infra_path)/$(urlq "$name")?option=terminate"; then
			ok "node deleted: $name"
			inflight_clear "$csp" "$name"
			return 0
		fi
		bt_absent && { ok "node already gone: $name"; inflight_clear "$csp" "$name"; return 0; }
		if ! bt_deletion_unconfirmed || [ "$attempt" -ge "$retries" ]; then break; fi
		attempt=$((attempt + 1))
		warn "$name — the CSP is still releasing it. Retrying in ${wait}s (${attempt}/${retries})"
		sleep "$wait"
	done

	bt_report_warn "terminate failed — retrying with option=force"
	if ! bt_delete "$(_infra_path)/$(urlq "$name")?option=force"; then
		if bt_absent; then
			ok "node already gone: $name"
			inflight_clear "$csp" "$name"
			return 0
		fi
		bt_report "could not delete the node: $name"
		fail "  ⚠ A node that is not deleted keeps billing. Check the $(upper "$csp") console"
		fail "    (region $(csp_env "$csp" REGION)) and remove it there if this keeps failing."
		fail "    curl -X DELETE '${BEETLE_URL%/}$(_infra_path)/$name?option=force' -u '<user>:<pass>'"
		return 1
	fi
	inflight_clear "$csp" "$name"
	ok "node deleted with force: $name"
	warn "  option=force drops cb-tumblebug's record without confirming the CSP released"
	warn "  the VM. Worth one look at the $(upper "$csp") console (region $(csp_env "$csp" REGION))."
	return 0
}

# ---------------------------------------------------------------------------
# Cleanup — reclaim what a run left behind
# ---------------------------------------------------------------------------
# cb-tumblebug's namespace is the state. Everything is recognised by name prefix,
# so nothing has to be remembered locally between runs.
cleanup_all() {
	local csp="$1" prefix name seen="" found=0

	prefix="$(printf '%s-%s-' "$MATRIX_NAME_PREFIX" "$(lower "$csp")")"

	if ! bt_get "$(_infra_path)"; then
		bt_report "could not list the migrated infrastructures"
		fail "  Nothing was deleted. A list that could not be read is not an empty list."
		return 1
	fi
	while IFS= read -r name; do
		[ -n "$name" ] || continue
		seen="${seen:+$seen }$name"
	done <<< "$(bt_jq -r '.infra[]? // .Infra[]? | (.id // .name) // empty')"

	# Deleted by the name the namespace actually returned, not by the name this
	# run would have used. One infra per CSP is the shape a run creates, but a
	# reclaim is exactly the situation where that may not hold any more.
	for name in $seen; do
		case "$name" in
		"$prefix"*)
			found=1
			sub "reclaiming: $name"
			delete_node "$csp" "$name" || true ;;
		esac
	done
	[ "$found" -eq 0 ] && info "there is no $(upper "$csp") node of this matrix to reclaim."

	# Nodes this folder asked for that the namespace never recorded.
	inflight_report "$csp" "$seen"

	# The network last: a vNet cannot go while a node is still attached to it.
	sub "network"
	release_network "$csp" || true
	return 0
}

# cleanup_namespace — the one cb-tumblebug call in the cleanup path, because
#   cm-beetle has no namespace API.
#
#   Only when the namespace holds no infrastructure, and only when that was
#   actually read. It is free to keep and the next run reuses it, so failing to
#   delete it is not a failure.
cleanup_namespace() {
	local left
	if [ "${KEEP_NAMESPACE:-0}" = "1" ]; then
		info "KEEP_NAMESPACE=1 — keeping namespace $MATRIX_NS."
		return 0
	fi
	if ! bt_get "$(_infra_path)"; then
		warn "keeping namespace $MATRIX_NS — the infrastructure list could not be re-read."
		return 0
	fi
	left="$(bt_jq -r '[.infra[]? // .Infra[]?] | length')"
	if [ "${left:-0}" -ne 0 ]; then
		warn "keeping namespace $MATRIX_NS — $left infrastructure(s) still in it."
		warn "  Some may belong to another run; this folder deletes only what its prefix names."
		return 0
	fi

	step "deleting namespace $MATRIX_NS (cb-tumblebug — cm-beetle has no namespace API)"
	if tb_delete "/ns/$(urlq "$MATRIX_NS")"; then
		ok "namespace deleted: $MATRIX_NS"
	else
		bt_report_warn "could not delete namespace $MATRIX_NS"
		warn "  Harmless to leave — the next run reuses it. By hand:"
		warn "    curl -X DELETE '${TUMBLEBUG_URL%/}/ns/$MATRIX_NS' -u '<user>:<pass>'"
	fi
	return 0
}
