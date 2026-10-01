#!/usr/bin/env bash
# ==============================================================================
# lib/beetle.sh — HTTP against cm-beetle, and the one call that is not
# ------------------------------------------------------------------------------
#   bt_get/post/...   curl + Basic Auth against BEETLE_URL, never aborts on 4xx/5xx
#   bt_data           read a field out of the last response, envelope or not
#   bt_async          POST with Prefer: respond-async, then wait for the job
#   tb_*              the same against TUMBLEBUG_URL - namespaces only
#
# TWO RESPONSE SHAPES
#   beetle's own handlers answer with ApiResponse[T]: {"success":…,"data":…}.
#   Its resource endpoints (vNet, securityGroup, objectStorage/support) are
#   reverse proxies to cb-tumblebug and pass tumblebug's body through verbatim,
#   with no envelope at all. bt_data() unwraps when there is an envelope and
#   reads the body directly when there is not, so callers never have to know
#   which kind of endpoint they just called.
#
# THERE IS NO RESPONSE TIMEOUT BY DEFAULT
#   A managed RDBMS takes 5 to 30 minutes to build, so any fixed --max-time is a
#   bet on the slowest CSP being faster than the number - and losing that bet
#   reads as a failure while the instance is still on its way. The wait is
#   bounded where it can be judged instead: the polling loops in provision.sh can
#   tell "not ready yet" from "gone wrong". --connect-timeout stays, because
#   reaching beetle at all is a different question from how long it then takes.
#   BEETLEENV_HTTP_TIMEOUT imposes a response timeout if you want one.
# ==============================================================================

if [ -n "${BEETLEENV_BEETLE_SH:-}" ]; then return 0; fi
BEETLEENV_BEETLE_SH=1

# shellcheck source=./common.sh
. "$(dirname "${BASH_SOURCE[0]}")/common.sh"

BT_STATUS=""
BT_BODY=""
BT_RETRY_AFTER=""

# Extra request headers, reset after every call. Set through bt_header().
BT_HEADERS=()
bt_header() { BT_HEADERS+=("$1"); }

# cm-beetle paces its calls to cb-tumblebug at ~1.6 req/s and answers 503 with a
# Retry-After when no slot frees up in time. That is a "come back shortly", not a
# failure, so it is retried here rather than surfaced to every caller.
BT_RATE_LIMIT_RETRIES="${BEETLEENV_RATE_LIMIT_RETRIES:-3}"

# _http_request <base url> <user> <pass> <method> <path> [body]
_http_request() {
    local base="$1" user="$2" pass="$3" method="$4" path="$5" body="${6:-}"
    local url="${base%/}${path}"
    local out hdr code h

    out="$(mktemp "${BEETLEENV_TMP}/resp.XXXXXX")"
    hdr="${out}.hdr"

    local -a args=(
        --silent --show-error
        --output "$out" --dump-header "$hdr" --write-out '%{http_code}'
        --request "$method"
        --header 'Accept: application/json'
        --connect-timeout 15
    )
    if [ -n "$user" ]; then
        args+=(--user "${user}:${pass}")
    fi
    # Unset or 0 means no response timeout - the default. See the note above.
    if [ -n "${BEETLEENV_HTTP_TIMEOUT:-}" ] && [ "${BEETLEENV_HTTP_TIMEOUT}" != "0" ]; then
        args+=(--max-time "$BEETLEENV_HTTP_TIMEOUT")
    fi
    # Quoted expansion, guarded by the length test: a header value contains a
    # space ("Prefer: respond-async") and would otherwise be split into two.
    if [ "${#BT_HEADERS[@]}" -gt 0 ]; then
        for h in "${BT_HEADERS[@]}"; do
            args+=(--header "$h")
        done
    fi
    if [ -n "$body" ]; then
        args+=(--header 'Content-Type: application/json' --data-binary "$body")
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
    rm -f "$out" "$hdr" "${out}.err"

    case "$BT_STATUS" in
        2*) return 0 ;;
        *)  return 1 ;;
    esac
}

# bt_request <method> <path> [body] — a call to cm-beetle, retried while it
#   answers 503 with a Retry-After. Headers queued with bt_header() apply to this
#   call only.
bt_request() {
    local method="$1" path="$2" body="${3:-}"
    local attempt=0 wait
    # Keep the queued headers: bt_request clears BT_HEADERS after each attempt,
    # and a retry has to send the same Prefer and X-Request-Id as the first try.
    local -a headers=()
    if [ "${#BT_HEADERS[@]}" -gt 0 ]; then headers=("${BT_HEADERS[@]}"); fi

    while :; do
        BT_HEADERS=()
        if [ "${#headers[@]}" -gt 0 ]; then BT_HEADERS=("${headers[@]}"); fi
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
        case "$wait" in
            ''|*[!0-9]*) wait=5 ;;
        esac
        log_warn "beetle is rate limited; retrying in ${wait}s (${attempt}/${BT_RATE_LIMIT_RETRIES})"
        sleep "$wait"
    done
}

bt_get()    { bt_request GET    "$1"; }
bt_post()   { bt_request POST   "$1" "${2:-}"; }
bt_delete() { bt_request DELETE "$1" "${2:-}"; }

# tb_request — cb-tumblebug direct. Used for namespaces and nothing else:
#   cm-beetle has no namespace API (the routes are commented out in
#   pkg/api/rest/server.go), and every migration call fails on a namespace that
#   does not exist. Every other call in beetleenv goes through beetle.
tb_request() {
    local method="$1" path="$2" body="${3:-}"
    _http_request "$TUMBLEBUG_URL" "${TUMBLEBUG_USERNAME:-}" "${TUMBLEBUG_PASSWORD:-}" \
                  "$method" "$path" "$body"
}

tb_get()    { tb_request GET    "$1"; }
tb_post()   { tb_request POST   "$1" "${2:-}"; }
tb_delete() { tb_request DELETE "$1" "${2:-}"; }

# ------------------------------------------------------------------------------
# Reading the last response
# ------------------------------------------------------------------------------

# bt_data <jq filter> — apply the filter to the payload of the last response,
#   whichever shape it came in. `.` yields the payload itself.
bt_data() {
    printf '%s' "$BT_BODY" | jq -r "
        (if type == \"object\" and has(\"success\") and has(\"data\")
         then .data else . end) | ${1} // empty
    " 2>/dev/null || printf ''
}

# bt_payload — the payload of the last response as JSON, for feeding into the
#   next request. Empty when the body is not JSON.
bt_payload() {
    printf '%s' "$BT_BODY" | jq -c '
        if type == "object" and has("success") and has("data") then .data else . end
    ' 2>/dev/null || printf ''
}

# bt_jq <jq args...> — run jq over the payload with arbitrary arguments, for the
#   filters bt_data cannot express (--arg, -c, a select over a list).
bt_jq() {
    bt_payload | jq "$@" 2>/dev/null || printf ''
}

# bt_message — one-line description of the last response, for error reporting.
#   Kept to one line: beetle passes a CSP's error body through verbatim, so what
#   arrives is pretty-printed JSON with embedded newlines - an eight-line block
#   for one sentence of information.
bt_message() {
    printf 'HTTP %s: %s' "$BT_STATUS" \
        "$(bt_raw_message | tr '\n\r\t' '   ' | tr -s ' ' | cut -c1-200)"
}

# bt_raw_message — the same text, uncut and with its line breaks intact. The
#   extraction is here rather than in bt_message so that reporting a failure in
#   full does not have to re-derive which field the message was in.
bt_raw_message() {
    local m=""
    m="$(printf '%s' "$BT_BODY" | jq -r '
        if type == "object"
        then (.error // .message // .Message // .text // empty)
        else empty end' 2>/dev/null || true)"
    if [ -z "$m" ]; then
        # Not beetle's own envelope: search the tree for whatever the CSP called
        # its message rather than keeping a per-CSP field list here.
        m="$(printf '%s' "$BT_BODY" | jq -r '
            first(.. | objects | (.returnMessage // .message // .Message // empty))' \
            2>/dev/null || true)"
    fi
    if [ -z "$m" ]; then m="$BT_BODY"; fi
    printf '%s' "$m"
}

# bt_absent — true when the last response means "this resource does not exist".
#   Not every layer answers 404: the proxied tumblebug endpoints do, but beetle's
#   own handlers turn a missing resource into a 500 with a message.
bt_absent() {
    case "$BT_STATUS" in
        404) return 0 ;;
    esac
    printf '%s' "$BT_BODY" | grep -qiE 'not exist|not found|does not exist|no such'
}

# bt_delete_unconfirmed — true when the last delete failed cb-tumblebug's
#   fail-closed gate rather than for a real reason.
#
#   cb-tumblebug refuses to forget a resource the CSP still reports
#   (cloud-barista/cb-tumblebug#2685): rather than orphaning something that is
#   live and billing, it keeps its own record and says so. The three phrasings
#   come from vnet.go, objectStorage.go and common.go, and all end the same
#   way - retry, or force.
bt_delete_unconfirmed() {
    case "$(bt_raw_message)" in
        *"still exists on the CSP"*|*"deletion unconfirmed"*|\
        *"record retained"*|*"record is retained"*) return 0 ;;
    esac
    return 1
}

# bt_bucket_not_empty — true when the last delete was refused because the bucket
#   still holds objects.
#
#   cb-spider answers a plain DELETE on a non-empty bucket with 409 BucketNotEmpty
#   ("Use force=true parameter to force delete"), and counts object versions and
#   delete markers separately from ordinary objects. The whole CSP body is
#   searched rather than bt_raw_message's one field: this arrives nested inside
#   beetle's message as the text cb-spider printed, not as a field of its own.
bt_bucket_not_empty() {
    printf '%s' "$BT_BODY" | grep -qiE 'BucketNotEmpty|is not empty|object versions'
}

# bt_delete_retry <path> <label> — a delete that waits the CSP's own lag out.
#
#   The case this exists for: a vNet cannot go while a managed database's
#   subnets, interfaces and ACGs are still attached, and a CSP releases those
#   asynchronously - so the delete that follows a database teardown fails, and
#   the same delete a minute later succeeds. That is what cb-tumblebug's message
#   asks for in so many words.
#
#   action=force is not the answer and is deliberately not used here: it drops
#   cb-tumblebug's record without deleting anything on the CSP, which turns a
#   resource that would not delete into one nothing can delete, still billing.
#
#   Returns non-zero for every other failure without a second attempt, leaving
#   the caller to tell "already gone" from a real error as it did before.
bt_delete_retry() {
    local path="$1" label="${2:-resource}" attempt=1

    while :; do
        if bt_delete "$path"; then
            return 0
        fi
        if bt_absent || ! bt_delete_unconfirmed; then
            return 1
        fi
        if [ "$attempt" -ge "$DELETE_RETRIES" ]; then
            return 1
        fi
        log_info "${label} is still on the CSP; waiting ${DELETE_RETRY_WAIT}s and asking again (${attempt}/${DELETE_RETRIES})"
        sleep "$DELETE_RETRY_WAIT"
        attempt=$((attempt + 1))
    done
}

# urlq <value> — percent-encode a query-string value.
urlq() { jq -rn --arg v "${1:-}" '$v|@uri'; }

# ------------------------------------------------------------------------------
# Asynchronous migration calls
# ------------------------------------------------------------------------------
# Every migration API runs synchronously by default and answers only once the CSP
# has finished. For a managed RDBMS that is up to half an hour on one open
# connection. `Prefer: respond-async` turns the call into a 202 plus a request id
# that GET /request/{reqId} reports on, which is what beetleenv uses everywhere.
#
# The request id is generated here rather than read back from the response:
# beetle uses the X-Request-Id it is given, so sending our own means the id is
# known even if the connection drops before the 202 arrives.

new_request_id() {
    printf 'beetleenv-%s-%s' "$(date +%s)" "$RANDOM"
}

# bt_async <method> <path> [body] — start an asynchronous call.
#   Prints the request id on 202. A server that ignored the preference and
#   answered 2xx synchronously prints nothing and returns 0: the work is already
#   done, and bt_wait treats an empty id as such.
BT_REQUEST_ID=""
bt_async() {
    local method="$1" path="$2" body="${3:-}"
    BT_REQUEST_ID="$(new_request_id)"

    bt_header 'Prefer: respond-async'
    bt_header "X-Request-Id: ${BT_REQUEST_ID}"
    if ! bt_request "$method" "$path" "$body"; then
        return 1
    fi
    if [ "$BT_STATUS" = "202" ]; then
        printf '%s' "$BT_REQUEST_ID"
    fi
    return 0
}

# BT_FAILURE_FILE — the last asynchronous failure, whole and unshortened.
#   The progress line stays one line and stays cut, because it is printed on a
#   poll that may repeat for half an hour. The reason a run ended is a different
#   thing: a CSP explains a rejected VM in a sentence that starts well past
#   character 200 ("Failed to Create VM instance : [Status ..."), so cutting it
#   there reports that something failed and withholds what.
#
#   A file rather than a variable because poll_for runs its probe in a command
#   substitution (common.sh: res="$("$@" 2>/dev/null)"), and a global assigned
#   inside that subshell is gone by the time the caller reads it.
BT_FAILURE_FILE="${BEETLEENV_TMP}/failure-detail"

# _request_probe <reqId> — poll_for probe over GET /request/{reqId}.
_request_probe() {
    local status detail
    if ! bt_get "/request/$(urlq "$1")"; then
        # A request id that is not known yet right after the 202 is normal.
        printf 'pending:%s' "$(bt_message)"
        return 0
    fi
    status="$(bt_data '.status')"
    case "$status" in
        Success) printf 'ready' ;;
        Error)
            detail="$(bt_data '.errorResponse')"
            detail="${detail:-unknown error}"
            printf '%s' "$detail" > "$BT_FAILURE_FILE" 2>/dev/null || true
            printf 'failed:%s' "$(printf '%s' "$detail" \
                | tr '\n\r\t' '   ' | tr -s ' ' | cut -c1-200)"
            ;;
        *)       printf 'pending:%s' "${status:-Handling}" ;;
    esac
}

# bt_report_failure — print the last failure in full, indented, one line per line
#   the CSP wrote. Called on the way out of a failed run, where there is no later
#   poll to keep short. Silent when the failure was short enough that the
#   progress line already carried all of it.
bt_report_failure() {
    if [ ! -s "$BT_FAILURE_FILE" ]; then
        return 0
    fi
    _bt_print_full "$(cat "$BT_FAILURE_FILE")"
}

# bt_report_message — the same, for a synchronous call that just failed. Reads
#   the current response rather than the async failure file, so it belongs after
#   a bt_delete or bt_post that returned non-zero.
bt_report_message() {
    _bt_print_full "$(bt_raw_message)"
}

_bt_print_full() {
    local detail="$1"
    # Nothing to add when the one-line form already carried all of it: 200 is
    # where bt_message cuts.
    if [ "${#detail}" -le 200 ]; then
        return 0
    fi

    printf '\n' >&2
    # \n arrives escaped inside the JSON string beetle passes through verbatim.
    # Two passes, not one: the split has to finish before the indent runs, or ^
    # matches only the start of the original line and indents just the first.
    printf '%s\n' "$detail" | sed 's/\\n/\n/g' | sed 's/^/       /' >&2
    printf '\n' >&2
}

# bt_wait <reqId> <label> <timeout> — wait for an asynchronous call to finish.
#   An empty id means the call already completed synchronously.
bt_wait() {
    local req_id="$1" label="$2" timeout="$3"
    if [ -z "$req_id" ]; then
        log_ok "${label} completed"
        return 0
    fi
    poll_for "$label" "$timeout" _request_probe "$req_id"
}

# ------------------------------------------------------------------------------
# preflight
# ------------------------------------------------------------------------------

# preflight_beetle — beetle has to answer before any call is worth making, and
#   it must be able to reach tumblebug: every migration endpoint sits behind a
#   middleware that answers 503 until tumblebug reports ready, and the message it
#   gives ("Tumblebug is not responding") is easy to mistake for a beetle fault.
preflight_beetle() {
    if ! bt_get "/readyz"; then
        die "cm-beetle is not reachable at ${BEETLE_URL} ($(bt_message))
       beetleenv does not start beetle - it must already be running."
    fi
    if [ -n "${TUMBLEBUG_URL:-}" ] && ! tb_get "/readyz"; then
        die "cb-tumblebug is not reachable at ${TUMBLEBUG_URL} ($(bt_message))
       beetle proxies every resource call to it, so nothing here would work."
    fi
}
