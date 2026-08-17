#!/bin/sh
set -eu

umask 077
unset CREDENTIALS_DIRECTORY

program=cliproxyapi-management-panel-update
app_dir=${CLIPROXYAPI_HOME:-/root/cliproxyapi}
approval_file=${APPROVED_ARTIFACTS_FILE:-$app_dir/approved-artifacts.json}
config_file=${CLIPROXYAPI_CONFIG_FILE:-$app_dir/config.yaml}
panel_path=${MANAGEMENT_PANEL_FILE:-$app_dir/static/management.html}
state_dir=${PANEL_UPDATE_STATE_DIR:-$app_dir/.panel-update}
repository=${PANEL_RELEASE_REPOSITORY-}
tmp_parent=${TMPDIR:-/tmp}
tmp_dir=

log() {
    printf '%s: %s\n' "$program" "$1" >&2
}

cleanup() {
    if [ -n "$tmp_dir" ]; then
        case "$tmp_dir" in
            "$tmp_parent"/cliproxyapi-panel-update.*) rm -rf -- "$tmp_dir" ;;
        esac
    fi
}
trap cleanup EXIT HUP INT TERM

die() {
    log "$1"
    exit "${2:-2}"
}

require_absolute_path() {
    case "$2" in /*) ;; *) die "$1 must be an absolute path" ;; esac
}

mode_is_trusted() {
    case "$1" in *[2367][0-9]|*[0-9][2367]) return 1 ;; *) return 0 ;; esac
}

trusted_file() {
    [ -f "$1" ] && [ ! -L "$1" ] || return 1
    metadata=$(stat -c '%u %a' -- "$1" 2>/dev/null) || return 1
    metadata_uid=${metadata%% *}
    metadata_mode=${metadata#* }
    [ "$metadata_uid" != "$metadata" ] && [ "$metadata_uid" = 0 ] && mode_is_trusted "$metadata_mode"
}

trusted_directory() {
    [ -d "$1" ] && [ ! -L "$1" ] || return 1
    metadata=$(stat -c '%u %a' -- "$1" 2>/dev/null) || return 1
    metadata_uid=${metadata%% *}
    metadata_mode=${metadata#* }
    [ "$metadata_uid" != "$metadata" ] && [ "$metadata_uid" = 0 ] && mode_is_trusted "$metadata_mode"
}

valid_sha256() {
    [ "${#1}" -eq 64 ] || return 1
    case "$1" in *[!0-9a-f]*) return 1 ;; *) return 0 ;; esac
}

valid_tag() {
    [ -n "$1" ] || return 1
    case "$1" in *[!0-9A-Za-z._+-]*) return 1 ;; *) return 0 ;; esac
}

restore_lkg() {
    lkg=$state_dir/management.html.lkg
    lkg_hash_file=$state_dir/management.html.lkg.sha256
    if ! trusted_file "$lkg" || ! trusted_file "$lkg_hash_file"; then
        die "panel_rollback_unavailable" 4
    fi
    expected_lkg=$(tr -d '\r\n' <"$lkg_hash_file")
    valid_sha256 "$expected_lkg" || die "panel_rollback_hash_invalid" 4
    rollback_tmp=$panel_path.rollback.$$
    cp -p -- "$lkg" "$rollback_tmp" || die "panel_rollback_copy_failed" 4
    mv -f -- "$rollback_tmp" "$panel_path" || die "panel_rollback_install_failed" 4
    actual_lkg=$(sha256sum "$panel_path" | awk '{ print $1 }')
    [ "$actual_lkg" = "$expected_lkg" ] || die "panel_rollback_verification_failed" 4
    die "panel_install_verification_failed; last-known-good restored" 3
}

[ "$#" -eq 1 ] || die "usage: update-management-panel.sh <exact-release-tag>"
requested_tag=$1
valid_tag "$requested_tag" || die "panel_tag_invalid"
[ "$(id -u)" = 0 ] || die "must_run_as_root"

for named_path in \
    "app_dir:$app_dir" \
    "approval_file:$approval_file" \
    "config_file:$config_file" \
    "panel_path:$panel_path" \
    "state_dir:$state_dir" \
    "tmp_parent:$tmp_parent"
do
    name=${named_path%%:*}
    value=${named_path#*:}
    require_absolute_path "$name" "$value"
done

case "$repository" in
    */*) ;;
    *) die "PANEL_RELEASE_REPOSITORY must be owner/repository" ;;
esac
case "$repository" in
    *[!0-9A-Za-z._/-]*|/*|*/|*..*) die "panel_repository_invalid" ;;
esac

trusted_directory "$app_dir" || die "app_directory_untrusted"
trusted_file "$approval_file" || die "approval_untrusted"
trusted_file "$config_file" || die "config_untrusted"
if [ -e "$panel_path" ]; then
    trusted_file "$panel_path" || die "installed_panel_untrusted"
fi

approval_shape='type == "object" and (keys | sort) == ["cli","panel","plugin","schema_version","verifier_schema"] and .schema_version == 1 and .verifier_schema == 1 and (.panel | type == "object" and (keys | sort) == ["sha256","version"])'
jq -e "$approval_shape" "$approval_file" >/dev/null 2>&1 || die "approval_invalid"
approved_tag=$(jq -er '.panel.version' "$approval_file") || die "approval_invalid"
approved_sha=$(jq -er '.panel.sha256' "$approval_file") || die "approval_invalid"
[ "$requested_tag" = "$approved_tag" ] || die "panel_tag_not_approved=$requested_tag"
valid_sha256 "$approved_sha" || die "panel_hash_invalid"

configured_repository=$(awk '
    /^[^[:space:]#][^:]*:/ { in_remote = ($0 ~ /^remote-management:[[:space:]]*($|#)/) }
    in_remote && /^[[:space:]]+disable-auto-update-panel:[[:space:]]*true([[:space:]]*#.*)?$/ { disabled = 1 }
    in_remote && /^[[:space:]]+panel-github-repository:/ {
        line = $0
        sub(/^[^:]*:[[:space:]]*/, "", line)
        sub(/[[:space:]]*#.*$/, "", line)
        gsub(/^["]|["]$/, "", line)
        repository = line
    }
    END {
        if (!disabled || repository == "") exit 1
        print repository
    }
' "$config_file") || die "built_in_panel_latest_not_disabled"
[ "$configured_repository" = "https://github.com/$repository" ] || die "configured_panel_repository_mismatch"

panel_dir=$(dirname -- "$panel_path")
mkdir -p -- "$state_dir" "$panel_dir"
chmod 700 "$state_dir"
trusted_directory "$state_dir" || die "panel_state_directory_untrusted"
trusted_directory "$panel_dir" || die "panel_directory_untrusted"
tmp_dir=$(mktemp -d "$tmp_parent/cliproxyapi-panel-update.XXXXXX")
asset_url=https://github.com/$repository/releases/download/$requested_tag/management.html
curl --fail --silent --show-error --location --retry 3 "$asset_url" -o "$tmp_dir/management.html" || die "panel_download_failed"
downloaded_sha=$(sha256sum "$tmp_dir/management.html" | awk '{ print $1 }')
[ "$downloaded_sha" = "$approved_sha" ] || die "panel_download_hash_mismatch" 3

if [ -f "$panel_path" ]; then
    current_sha=$(sha256sum "$panel_path" | awk '{ print $1 }')
    valid_sha256 "$current_sha" || die "installed_panel_hash_failed"
    cp -p -- "$panel_path" "$state_dir/management.html.lkg.new"
    printf '%s\n' "$current_sha" >"$state_dir/management.html.lkg.sha256.new"
    chmod 600 "$state_dir/management.html.lkg.new" "$state_dir/management.html.lkg.sha256.new"
    mv -f -- "$state_dir/management.html.lkg.new" "$state_dir/management.html.lkg"
    mv -f -- "$state_dir/management.html.lkg.sha256.new" "$state_dir/management.html.lkg.sha256"
fi

install_tmp=$panel_path.install.$$
cp -- "$tmp_dir/management.html" "$install_tmp"
chmod 644 "$install_tmp"
mv -f -- "$install_tmp" "$panel_path" || restore_lkg
installed_sha=$(sha256sum "$panel_path" | awk '{ print $1 }')
[ "$installed_sha" = "$approved_sha" ] || restore_lkg
log "panel_update_accepted tag=$requested_tag sha256=$installed_sha"
