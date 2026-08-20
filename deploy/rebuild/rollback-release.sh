#!/bin/sh
set -eu

apply=false
deploy_root=${DEPLOY_ROOT:-/root/cliproxyapi.deployments}
active_link=${ACTIVE_LINK:-/root/cliproxyapi}
prev_link=${PREV_LINK:-/root/cliproxyapi.prev}

[ "${1-}" = "--apply" ] && { apply=true; shift; }
[ "$#" -eq 0 ] || { echo "usage: $0 [--apply]" >&2; exit 2; }

case "$deploy_root:$active_link:$prev_link" in
    /*:/*:/*) ;;
    *) echo "deployment and link paths must be absolute" >&2; exit 2 ;;
esac
[ -L "$active_link" ] || { echo "active deployment symlink missing: $active_link" >&2; exit 1; }
[ -L "$prev_link" ] || { echo "previous deployment symlink missing: $prev_link" >&2; exit 1; }
active_target=$(readlink -f -- "$active_link")
rollback_target=$(readlink -f -- "$prev_link")
case "$active_target:$rollback_target" in
    "$deploy_root"/*:"$deploy_root"/*) ;;
    *) echo "deployment target escapes deployment root" >&2; exit 1 ;;
esac
[ "$active_target" != "$rollback_target" ] || { echo "previous deployment equals active deployment" >&2; exit 1; }
[ -d "$rollback_target/state" ] || {
    echo "previous deployment incomplete: $rollback_target" >&2
    exit 1
}
if [ ! -f "$rollback_target/approved-artifacts.json" ] && [ ! -f "$rollback_target/legacy-artifacts.json" ]; then
    echo "previous deployment has no artifact identity: $rollback_target" >&2
    exit 1
fi
config=$rollback_target/state/config.yaml
[ -f "$config" ] || { echo "previous deployment config missing" >&2; exit 1; }
require_config_value() {
    key=$1
    value=$2
    [ "$(grep -Ec "^[[:space:]]*$key:" "$config")" -eq 1 ] &&
        grep -Eq "^[[:space:]]*$key:[[:space:]]*$value([[:space:]]*#.*)?$" "$config" ||
        { echo "unsafe previous deployment config: $key must equal $value exactly once" >&2; exit 1; }
}
require_config_value host '127\.0\.0\.1'
require_config_value rtk_enabled false
require_config_value headroom_enabled false
require_config_value caveman_enabled false
require_config_value ponytail_enabled false
if grep -F 'REPLACE_WITH_' "$config" >/dev/null; then
    echo "previous deployment config still contains placeholders" >&2
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
bad_permission=$(find "$rollback_target" \( ! -user root -o -perm /0077 \) -print -quit)
[ -z "$bad_permission" ] || { echo "non-root-only path: $bad_permission" >&2; exit 1; }

if [ "$apply" != true ]; then
    echo "DRY RUN: would atomically roll back to $rollback_target and retain $active_target at $prev_link"
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

atomic_link "$rollback_target" "$active_link"
atomic_link "$active_target" "$prev_link"
echo "active: $rollback_target"
