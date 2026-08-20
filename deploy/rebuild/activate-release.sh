#!/bin/sh
set -eu

apply=false
deploy_root=${DEPLOY_ROOT:-/root/cliproxyapi.deployments}
active_link=${ACTIVE_LINK:-/root/cliproxyapi}
prev_link=${PREV_LINK:-/root/cliproxyapi.prev}
next_link=${NEXT_LINK:-/root/cliproxyapi.next}

[ "${1-}" = "--apply" ] && { apply=true; shift; }
[ "$#" -eq 0 ] || { echo "usage: $0 [--apply]" >&2; exit 2; }

case "$deploy_root:$active_link:$prev_link:$next_link" in
    /*:/*:/*:/*) ;;
    *) echo "deployment and link paths must be absolute" >&2; exit 2 ;;
esac
[ -L "$next_link" ] || { echo "next deployment symlink missing: $next_link" >&2; exit 1; }
next_target=$(readlink -f -- "$next_link")
case "$next_target" in "$deploy_root"/*) ;; *) echo "next target escapes deployment root" >&2; exit 1 ;; esac
[ -d "$next_target/state" ] && [ -f "$next_target/approved-artifacts.json" ] || {
    echo "next deployment incomplete: $next_target" >&2
    exit 1
}
config=$next_target/state/config.yaml
[ -f "$config" ] || { echo "next deployment config missing" >&2; exit 1; }
require_config_value() {
    key=$1
    value=$2
    [ "$(grep -Ec "^[[:space:]]*$key:" "$config")" -eq 1 ] &&
        grep -Eq "^[[:space:]]*$key:[[:space:]]*$value([[:space:]]*#.*)?$" "$config" ||
        { echo "unsafe next deployment config: $key must equal $value exactly once" >&2; exit 1; }
}
require_config_value host '127\.0\.0\.1'
require_config_value rtk_enabled false
require_config_value headroom_enabled false
require_config_value caveman_enabled false
require_config_value ponytail_enabled false
if grep -F 'REPLACE_WITH_' "$config" >/dev/null; then
    echo "next deployment config still contains placeholders" >&2
    exit 1
fi
[ "$(grep -Ec '^[[:space:]]*model_allowlist:' "$config")" -eq 1 ] || {
    echo "model_allowlist must appear exactly once" >&2
    exit 1
}
awk '
    /^[[:space:]]*model_allowlist:[[:space:]]*\[[[:space:]]*[^][:space:]][^]]*\][[:space:]]*$/ { valid = 1 }
    /^[[:space:]]*model_allowlist:[[:space:]]*$/ { in_list = 1; next }
    in_list && /^[[:space:]]*-[[:space:]]*[^[:space:]#]+/ { valid = 1 }
    END { exit valid ? 0 : 1 }
' "$config" || { echo "model_allowlist must contain an exact model id" >&2; exit 1; }
bad_permission=$(find "$next_target" \( ! -user root -o -perm /0077 \) -print -quit)
[ -z "$bad_permission" ] || { echo "non-root-only path: $bad_permission" >&2; exit 1; }

if [ "$apply" != true ]; then
    echo "DRY RUN: would atomically activate $next_target and preserve current target at $prev_link"
    exit 0
fi
[ "$(id -u)" -eq 0 ] || { echo "root required for --apply" >&2; exit 1; }

atomic_link() {
    target=$1
    link=$2
    temporary=$link.tmp.$$
    rm -f -- "$temporary"
    ln -s -- "$target" "$temporary"
    mv -Tf -- "$temporary" "$link"
}

if [ -L "$active_link" ]; then
    current_target=$(readlink -f -- "$active_link")
    case "$current_target" in "$deploy_root"/*) ;; *) echo "active target escapes deployment root" >&2; exit 1 ;; esac
    [ "$current_target" != "$next_target" ] || { echo "next deployment is already active" >&2; exit 1; }
    atomic_link "$current_target" "$prev_link"
elif [ -e "$active_link" ]; then
    echo "active path is not a symlink: $active_link" >&2
    exit 1
fi

atomic_link "$next_target" "$active_link"
rm -f -- "$next_link"
echo "active: $next_target"
