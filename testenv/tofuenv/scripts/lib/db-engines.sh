#!/usr/bin/env bash
# ==============================================================================
# db-engines.sh — which database engines a database module runs
# ------------------------------------------------------------------------------
#   Sourced by provision.sh and deprovision.sh for --engine. tofu/<csp>/database
#   gives each engine a count driven by var.<csp>_db_engines, and these helpers
#   work out that list from the command line and from what the state holds.
#
#   Two spellings, kept apart on purpose:
#     command line : mysql | mariadb | postgresql | mongodb  (same as gen-data.sh)
#     tofu         : mysql | mariadb | postgres   | mongodb  (the module's names)
#   Everything below works in tofu names; only db_parse_engines reads the other.
#
#   The caller sets CSP, RUNNER and WS_PREFIX (ws_load) first.
# ==============================================================================

# db_all_engines — every engine the CSP's module can run, in tofu names and in the
#   module's own order. Lists built here always come out in this order, so the
#   value handed to tofu does not change between runs that mean the same thing.
db_all_engines() {
    case "$CSP" in
        aws) echo "mysql mariadb postgres" ;;
        ncp) echo "mysql postgres mongodb" ;;
    esac
}

# db_cli_name <tofu name> — how the command line spells an engine.
db_cli_name() {
    [ "$1" = "postgres" ] && echo "postgresql" || echo "$1"
}

# db_cli_names <list> — a list of tofu names, spelled for the command line.
db_cli_names() {
    local e out=""
    for e in $1; do out="${out:+$out }$(db_cli_name "$e")"; done
    echo "$out"
}

# db_parse_engines <csv> — validate an --engine value and print it as a list of
#   tofu names. Returns non-zero, with the reason on stderr, for an engine the CSP
#   does not serve.
db_parse_engines() {
    local csv="$1" want="" e name all supported
    all="$(db_all_engines)"
    supported="$(db_cli_names "$all")"
    for e in ${csv//,/ }; do
        # Only the command-line spelling is accepted, so "postgres" is refused
        # rather than quietly meaning the same as "postgresql".
        case "$e" in
            postgresql) name="postgres" ;;
            postgres)   name="" ;;
            *)          name="$e" ;;
        esac
        if [ -z "$name" ] || ! db_has "$all" "$name"; then
            echo "invalid engine for ${CSP}: $e (supported: ${supported// /, })" >&2
            return 1
        fi
        want="$want $name"
    done
    if [ -z "${want// /}" ]; then
        echo "--engine needs at least one engine (supported: ${supported// /, })" >&2
        return 1
    fi
    db_sorted "$want"
}

# db_has <list> <engine> — true when the list holds the engine.
db_has() {
    case " $1 " in *" $2 "*) return 0 ;; esac
    return 1
}

# db_sorted <list> — the engines of the list, deduplicated, in module order.
db_sorted() {
    local e out=""
    for e in $(db_all_engines); do
        db_has "$1" "$e" && out="${out:+$out }$e"
    done
    echo "$out"
}

# db_union <a> <b> / db_minus <a> <b> / db_intersect <a> <b>
db_union()     { db_sorted "$1 $2"; }
db_minus()     { local e out=""; for e in $1; do db_has "$2" "$e" || out="$out $e"; done; db_sorted "$out"; }
db_intersect() { local e out=""; for e in $1; do db_has "$2" "$e" && out="$out $e"; done; db_sorted "$out"; }

# db_state_engines <module> — the engines the prefix's state holds an instance of.
#   The bare address is matched as well as [0]: a state written before the engines
#   took a count still holds the bare one until the next apply moves it.
db_state_engines() {
    local mod="$1" re
    case "$CSP" in
        aws) re='^aws_db_instance\.(mysql|mariadb|postgres)(\[0\])?$' ;;
        ncp) re='^ncloud_(mysql|postgresql|mongodb)\.this(\[0\])?$' ;;
    esac
    ws_exec bash -c '
        cd "/work/'"$mod"'" 2>/dev/null || exit 0
        tofu state list 2>/dev/null || true
    ' | sed -nE "s/${re}/\1/p" | sed 's/^postgresql$/postgres/' | tr '\n' ' ' \
      | { read -r list || true; db_sorted "$list"; }
}

# db_engines_export <list> — the shell line that hands the list to tofu, for use
#   inside a ws_exec'd bash -c after .env is sourced (so .env cannot override it).
#   The names are validated tokens, so they need no further quoting.
db_engines_export() {
    local e json=""
    for e in $1; do json="${json:+$json,}\"$e\""; done
    echo "export TF_VAR_${CSP}_db_engines='[${json}]'"
}

# db_engine_targets <list> — -target flags for every resource that belongs to the
#   engines alone, so a destroy removes them and nothing they share with the rest.
db_engine_targets() {
    local e out=""
    for e in $1; do
        case "$CSP:$e" in
            aws:*)         out="$out -target=aws_db_instance.$e -target=aws_db_parameter_group.$e" ;;
            ncp:mysql)     out="$out -target=ncloud_mysql.this -target=ncloud_access_control_group_rule.mysql" ;;
            ncp:postgres)  out="$out -target=ncloud_postgresql.this -target=ncloud_access_control_group_rule.postgresql" ;;
            ncp:mongodb)   out="$out -target=ncloud_mongodb.this -target=ncloud_access_control_group_rule.mongodb" ;;
        esac
    done
    echo "${out# }"
}
