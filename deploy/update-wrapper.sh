#!/bin/sh
set -eu

umask 077

program=cliproxyapi-token-saver-update
minimum_systemd_version=247
official_repository=router-for-me/CLIProxyAPI

# LoadCredential exports only the directory path. Keep it in a non-exported shell
# variable, remove it before starting any helper, and restore it only for the Go
# verifier process. The wrapper never opens the credential file.
credential_directory=${CREDENTIALS_DIRECTORY-}
unset CREDENTIALS_DIRECTORY

app_dir=${CLIPROXYAPI_HOME:-/root/cliproxyapi}
approval_file=${APPROVED_ARTIFACTS_FILE:-$app_dir/approved-artifacts.json}
override_file=${SECURITY_OVERRIDES_FILE:-$app_dir/security-overrides.json}
state_dir=${TOKEN_SAVER_UPDATE_STATE_DIR:-$app_dir/.token-saver-update}
failed_file=${FAILED_FINGERPRINT_FILE:-$state_dir/failed-candidate.json}
lock_dir=${UPDATE_WRAPPER_LOCK_DIR:-$state_dir/update-wrapper.lock}
quarantine_dir=${TOKEN_SAVER_QUARANTINE_DIR:-$state_dir/quarantine}
updater=${OFFICIAL_UPDATER:-$app_dir/update.sh}
cli_path=${CLIPROXYAPI_BINARY:-$app_dir/cli-proxy-api}
version_file=${CLIPROXYAPI_VERSION_FILE:-$app_dir/version.txt}
service_file=${CLIPROXYAPI_SERVICE_FILE:-$app_dir/cliproxyapi.service}
plugin_path=${TOKEN_SAVER_PLUGIN_FILE-}
panel_path=${MANAGEMENT_PANEL_FILE:-$app_dir/static/management.html}
compat_probe=${COMPAT_PROBE_FILE:-$app_dir/compat-probe}
verifier=${UPDATE_VERIFIER_FILE:-$app_dir/update-verifier}
service_name=${CLIPROXYAPI_SERVICE_NAME:-cliproxyapi.service}
timer_name=${CLIPROXYAPI_UPDATE_TIMER:-cliproxyapi-update.timer}
tmp_parent=${TMPDIR:-/tmp}

tmp_dir=
lock_held=false
rollback_backup_dir=

log() {
    printf '%s: %s\n' "$program" "$1" >&2
}

alert() {
    message=$1
    printf '%s: ALERT %s\n' "$program" "$message" >&2
    if command -v systemd-cat >/dev/null 2>&1; then
        printf '%s\n' "$message" | systemd-cat --priority=alert --identifier="$program" || true
    elif command -v logger >/dev/null 2>&1; then
        logger -p user.alert -t "$program" "$message" || true
    fi
}

cleanup() {
    if [ -n "$tmp_dir" ]; then
        case "$tmp_dir" in
            "$tmp_parent"/cliproxyapi-token-saver-update.*)
                rm -rf -- "$tmp_dir"
                ;;
        esac
    fi
    if [ "$lock_held" = true ]; then
        rmdir -- "$lock_dir" 2>/dev/null || true
    fi
}
trap cleanup EXIT HUP INT TERM

die() {
    log "$1"
    exit "${2:-2}"
}

require_absolute_path() {
    case "$2" in
        /*) ;;
        *) die "$1 must be an absolute path" ;;
    esac
}

mode_is_trusted() {
    case "$1" in
        *[2367][0-9]|*[0-9][2367]) return 1 ;;
        *) return 0 ;;
    esac
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
    case "$1" in
        *[!0-9a-f]*) return 1 ;;
        *) return 0 ;;
    esac
}

valid_token() {
    [ -n "$1" ] || return 1
    case "$1" in
        *[!0-9A-Za-z._+-]*) return 1 ;;
        *) return 0 ;;
    esac
}

require_systemd_credential_support() {
    version=$(systemd --version 2>/dev/null | awk 'NR == 1 && $1 == "systemd" { print $2 }') || true
    case "$version" in
        ''|*[!0-9]*) die "systemd_version_unknown; LoadCredential requires systemd >= $minimum_systemd_version" ;;
    esac
    if [ "$version" -lt "$minimum_systemd_version" ]; then
        die "systemd_version_unsupported=$version; LoadCredential requires systemd >= $minimum_systemd_version"
    fi
}

disable_timer() {
    systemctl --user disable --now "$timer_name" >/dev/null 2>&1 || true
}

record_failed_fingerprint() {
    failure_code=$1
    tmp_failed=$state_dir/failed-candidate.json.$$
    timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
    printf '%s\n' \
        "{\"schema_version\":1,\"fingerprint\":\"$fingerprint\",\"cli_version\":\"$cli_version\",\"cli_sha256\":\"$cli_sha\",\"arch\":\"$cli_arch\",\"plugin_sha256\":\"$plugin_sha\",\"verifier_schema\":$verifier_schema,\"code\":\"$failure_code\",\"recorded_at\":\"$timestamp\"}" \
        >"$tmp_failed"
    chmod 600 "$tmp_failed"
    mv -f -- "$tmp_failed" "$failed_file"
}

verifier_result() {
    phase=$1
    checked_cli=$2
    checked_approval=$3
    output=$4
    set +e
    CREDENTIALS_DIRECTORY=$credential_directory "$verifier" \
        -base-url http://127.0.0.1:8317 \
        -approval "$checked_approval" \
        -cli "$checked_cli" \
        -plugin "$plugin_path" \
        -panel "$panel_path" \
        -phase "$phase" >"$output"
    verifier_rc=$?
    set -e
    return "$verifier_rc"
}

report_code() {
    jq -er '.code | select(type == "string" and length > 0)' "$1" 2>/dev/null || printf '%s\n' invalid_verifier_report
}

security_override_reason() {
    [ -f "$override_file" ] || return 1
    jq -er \
        --arg version "$cli_version" \
        --arg sha "$cli_sha" \
        --arg arch "$cli_arch" \
        '[.overrides[] | select(.cli.version == $version and .cli.sha256 == $sha and .cli.arch == $arch)] | select(length == 1) | .[0].reason' \
        "$override_file" 2>/dev/null
}

plugin_compatibility_code() {
    case "$1" in
        plugin_missing|plugin_not_registered|plugin_not_effective|plugin_version_mismatch|abi_mismatch|rpc_mismatch|fixture_mismatch|config_invalid|runtime_unhealthy|self_test_failed)
            return 0
            ;;
        *) return 1 ;;
    esac
}

rollback_failure() {
    failure_code=$1
    record_failed_fingerprint "$failure_code" || true
    disable_timer
    alert "rollback_failed code=$failure_code timer=$timer_name disabled; preserve backups and inspect immediately"
    exit 4
}

rollback_candidate() {
    failure_code=$1
    record_candidate=$2
    [ -n "$rollback_backup_dir" ] || rollback_failure "backup_directory_missing"
    backup_cli=$rollback_backup_dir/cli-proxy-api
    backup_version=$rollback_backup_dir/version.txt
    backup_service=$rollback_backup_dir/cliproxyapi.service

    for backup in "$backup_cli" "$backup_version" "$backup_service"; do
        trusted_file "$backup" || rollback_failure "backup_untrusted"
    done

    rollback_cli=$cli_path.rollback.$$
    rollback_version=$version_file.rollback.$$
    rollback_service=$service_file.rollback.$$
    cp -p -- "$backup_cli" "$rollback_cli" || rollback_failure "backup_copy_failed"
    cp -p -- "$backup_version" "$rollback_version" || rollback_failure "backup_copy_failed"
    cp -p -- "$backup_service" "$rollback_service" || rollback_failure "backup_copy_failed"
    mv -f -- "$rollback_service" "$service_file" || rollback_failure "service_restore_failed"
    mv -f -- "$rollback_cli" "$cli_path" || rollback_failure "binary_restore_failed"
    mv -f -- "$rollback_version" "$version_file" || rollback_failure "version_restore_failed"
    systemctl --user daemon-reload >/dev/null 2>&1 || rollback_failure "daemon_reload_failed"
    systemctl --user restart "$service_name" >/dev/null 2>&1 || rollback_failure "service_restart_failed"

    old_version=$(tr -d '\r\n' <"$version_file")
    valid_token "$old_version" || rollback_failure "restored_version_invalid"
    old_sha=$(sha256sum "$cli_path" | awk '{ print $1 }')
    valid_sha256 "$old_sha" || rollback_failure "restored_hash_invalid"
    rollback_approval=$state_dir/rollback-approval.$$.json
    jq --arg version "$old_version" --arg sha "$old_sha" \
        '.cli.version = $version | .cli.sha256 = $sha' "$approval_file" >"$rollback_approval" \
        || rollback_failure "rollback_approval_failed"
    chmod 600 "$rollback_approval"
    if ! verifier_result preflight "$cli_path" "$rollback_approval" "$tmp_dir/rollback-verifier.json"; then
        rollback_failure "rollback_verification_failed"
    fi
    rm -f -- "$rollback_approval"
    if [ "$record_candidate" = true ]; then
        record_failed_fingerprint "$failure_code" || rollback_failure "fingerprint_record_failed"
    fi
    alert "candidate_rejected code=$failure_code rollback_verified=true"
    exit 3
}

snapshot_backup_directories() {
    backup_snapshot=$tmp_dir/backups.before
    : >"$backup_snapshot"
    for directory in "$app_dir"/backup-pre-"$release_tag"-*; do
        [ -d "$directory" ] || continue
        printf '%s\n' "$directory" >>"$backup_snapshot"
    done
    update_started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
}

resolve_new_backup_directory() {
    new_backups=$tmp_dir/backups.after
    : >"$new_backups"
    for directory in "$app_dir"/backup-pre-"$release_tag"-*; do
        [ -d "$directory" ] || continue
        if grep -Fqx -- "$directory" "$backup_snapshot"; then
            continue
        fi
        trusted_directory "$directory" || return 1
        for backup in "$directory/cli-proxy-api" "$directory/version.txt" "$directory/cliproxyapi.service"; do
            trusted_file "$backup" || return 1
        done
        printf '%s\n' "$directory" >>"$new_backups"
    done
    backup_count=$(awk 'END { print NR + 0 }' "$new_backups")
    [ "$backup_count" -eq 1 ] || return 1
    rollback_backup_dir=$(sed -n '1p' "$new_backups")
    [ -n "$rollback_backup_dir" ] || return 1
    log "rollback_backup_selected=$rollback_backup_dir updater_started_at=$update_started_at"
}

apply_security_override() {
    failure_code=$1
    reason=$2
    installed_sha=$(sha256sum "$cli_path" | awk '{ print $1 }')
    [ "$installed_sha" = "$cli_sha" ] || rollback_candidate "$failure_code" true

    mkdir -p -- "$quarantine_dir"
    chmod 700 "$quarantine_dir"
    trusted_directory "$quarantine_dir" || rollback_failure "quarantine_untrusted"
    quarantine_path=$quarantine_dir/$(basename -- "$plugin_path").$fingerprint
    [ ! -e "$quarantine_path" ] || rollback_failure "quarantine_collision"
    mv -- "$plugin_path" "$quarantine_path" || rollback_failure "plugin_isolation_failed"

    if ! systemctl --user restart "$service_name" >/dev/null 2>&1; then
        disable_timer
        alert "security_override_manual_intervention code=restart_failed reason=$reason; security CLI retained and plugin quarantined at $quarantine_path"
        exit 4
    fi
    if "$compat_probe" -mode core-only -candidate "$cli_path" -timeout 45s >"$tmp_dir/core-only-probe.json"; then
        alert "security_override_applied code=$failure_code reason=$reason; security CLI retained, Token Saver quarantined, raw inference verified"
        exit 0
    fi
    disable_timer
    alert "security_override_manual_intervention code=core_only_probe_failed reason=$reason; security CLI retained and plugin quarantined at $quarantine_path; run a controlled byte-identical core inference canary before re-enabling the timer"
    exit 4
}

[ "$#" -le 1 ] || die "usage: update-wrapper.sh [latest|approved-version]"
target=${1:-latest}
require_systemd_credential_support
[ "$(id -u)" = 0 ] || die "must_run_as_root"

for named_path in \
    "app_dir:$app_dir" \
    "approval_file:$approval_file" \
    "override_file:$override_file" \
    "state_dir:$state_dir" \
    "failed_file:$failed_file" \
    "lock_dir:$lock_dir" \
    "quarantine_dir:$quarantine_dir" \
    "updater:$updater" \
    "cli_path:$cli_path" \
    "version_file:$version_file" \
    "service_file:$service_file" \
    "panel_path:$panel_path" \
    "compat_probe:$compat_probe" \
    "verifier:$verifier" \
    "tmp_parent:$tmp_parent"
do
    name=${named_path%%:*}
    value=${named_path#*:}
    require_absolute_path "$name" "$value"
done

trusted_directory "$app_dir" || die "app_directory_untrusted"
trusted_file "$approval_file" || die "approval_untrusted"
if [ -e "$override_file" ]; then
    trusted_file "$override_file" || die "security_overrides_untrusted"
fi
for executable in "$updater" "$compat_probe" "$verifier"; do
    trusted_file "$executable" && [ -x "$executable" ] || die "executable_untrusted=$executable"
done
trusted_file "$panel_path" || die "artifact_untrusted=$panel_path"

approval_shape='type == "object" and (keys | sort) == ["cli","panel","plugin","schema_version","verifier_schema"] and (.cli | type == "object" and (keys | sort) == ["arch","sha256","version"]) and (.plugin | type == "object" and (keys | sort) == ["abi","rpc","sha256","version"]) and (.panel | type == "object" and (keys | sort) == ["sha256","version"])'
jq -e "$approval_shape" "$approval_file" >/dev/null 2>&1 || die "approval_invalid"
approval_values=$(jq -er '[.schema_version,.verifier_schema,.cli.version,.cli.sha256,.cli.arch,.plugin.version,.plugin.sha256,.plugin.abi,.plugin.rpc,.panel.version,.panel.sha256] | @tsv' "$approval_file") || die "approval_invalid"
old_ifs=$IFS
IFS=$(printf '\t')
read -r approval_schema verifier_schema cli_version cli_sha cli_arch plugin_version plugin_sha plugin_abi plugin_rpc panel_version panel_sha <<EOF
$approval_values
EOF
IFS=$old_ifs
[ "$approval_schema" = 1 ] && [ "$verifier_schema" = 1 ] || die "approval_schema_mismatch"
if ! valid_token "$cli_version" || ! valid_token "$plugin_version" || ! valid_token "$panel_version"; then
    die "approval_version_invalid"
fi
if ! valid_sha256 "$cli_sha" || ! valid_sha256 "$plugin_sha" || ! valid_sha256 "$panel_sha"; then
    die "approval_hash_invalid"
fi
[ "$plugin_abi" = 1 ] && [ "$plugin_rpc" = 3 ] || die "approval_plugin_contract_invalid"
case "$cli_arch" in linux-amd64|linux-arm64) ;; *) die "approval_arch_invalid" ;; esac
machine=$(uname -m)
case "$machine" in
    x86_64|amd64) host_arch=linux-amd64 ;;
    aarch64|arm64) host_arch=linux-arm64 ;;
    *) die "host_arch_unsupported=$machine" ;;
esac
[ "$host_arch" = "$cli_arch" ] || die "approval_arch_mismatch=$cli_arch host=$host_arch"
archive_arch=${cli_arch#linux-}
if [ -z "$plugin_path" ]; then
    plugin_path=/root/.cli-proxy-api/plugins/linux/$archive_arch/token-saver.so
fi
require_absolute_path plugin_path "$plugin_path"
trusted_file "$plugin_path" || die "artifact_untrusted=$plugin_path"

case "$cli_version" in
    v*) release_tag=$cli_version; release_version=${cli_version#v} ;;
    *) release_tag=v$cli_version; release_version=$cli_version ;;
esac
case "$target" in
    latest|"$cli_version"|"$release_tag") ;;
    *) die "target_not_approved=$target" ;;
esac

if [ -f "$override_file" ]; then
    override_shape='type == "object" and (keys | sort) == ["overrides","schema_version"] and .schema_version == 1 and (.overrides | type == "array") and all(.overrides[]; type == "object" and (keys | sort) == ["cli","reason"] and (.cli | type == "object" and (keys | sort) == ["arch","sha256","version"] and (.version | type == "string" and length > 0 and length <= 128) and (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and (.arch == "linux-amd64" or .arch == "linux-arm64")) and (.reason | type == "string" and length > 0 and length <= 512 and (contains("\n") | not)))'
    jq -e "$override_shape" "$override_file" >/dev/null 2>&1 || die "security_overrides_invalid"
fi

mkdir -p -- "$state_dir"
chmod 700 "$state_dir"
trusted_directory "$state_dir" || die "state_directory_untrusted"
if [ -e "$failed_file" ]; then
    trusted_file "$failed_file" || die "failed_fingerprint_untrusted"
fi
if ! mkdir -- "$lock_dir" 2>/dev/null; then
    die "update_already_running"
fi
lock_held=true

fingerprint=$(printf '%s\n%s\n%s\n%s\n%s\n' "$cli_version" "$cli_sha" "$cli_arch" "$plugin_sha" "$verifier_schema" | sha256sum | awk '{ print $1 }')
valid_sha256 "$fingerprint" || die "fingerprint_failed"
if [ -f "$failed_file" ]; then
    failed_fingerprint=$(jq -er 'select(.schema_version == 1) | .fingerprint' "$failed_file" 2>/dev/null) || die "failed_fingerprint_invalid"
    valid_sha256 "$failed_fingerprint" || die "failed_fingerprint_invalid"
    if [ "$failed_fingerprint" = "$fingerprint" ]; then
        log "candidate_skipped_same_failed_fingerprint=$fingerprint"
        exit 0
    fi
fi

tmp_dir=$(mktemp -d "$tmp_parent/cliproxyapi-token-saver-update.XXXXXX")
asset=CLIProxyAPI_${release_version}_linux_${archive_arch}.tar.gz
case "$asset" in *_no-plugin*) die "no_plugin_asset_refused" ;; esac
release_api=https://api.github.com/repos/$official_repository/releases/latest
if [ "$target" != latest ]; then
    release_api=https://api.github.com/repos/$official_repository/releases/tags/$release_tag
fi
curl --fail --silent --show-error --location --retry 3 "$release_api" -o "$tmp_dir/release.json" || die "release_metadata_download_failed"
observed_tag=$(jq -er '.tag_name' "$tmp_dir/release.json") || die "release_metadata_invalid"
[ "$observed_tag" = "$release_tag" ] || die "latest_release_not_approved=$observed_tag"
base_url=https://github.com/$official_repository/releases/download/$release_tag
curl --fail --silent --show-error --location --retry 3 "$base_url/checksums.txt" -o "$tmp_dir/checksums.txt" || die "checksums_download_failed"
curl --fail --silent --show-error --location --retry 3 "$base_url/$asset" -o "$tmp_dir/$asset" || die "candidate_download_failed"
manifest_hash=$(awk -v target="$asset" '{ name=$2; sub(/^\*/, "", name); if (name == target) print $1 }' "$tmp_dir/checksums.txt")
manifest_count=$(printf '%s\n' "$manifest_hash" | awk 'NF { count++ } END { print count + 0 }')
if [ "$manifest_count" -ne 1 ] || ! valid_sha256 "$manifest_hash"; then
    record_failed_fingerprint official_checksum_missing
    die "official_checksum_missing" 3
fi
archive_hash=$(sha256sum "$tmp_dir/$asset" | awk '{ print $1 }')
[ "$archive_hash" = "$manifest_hash" ] || { record_failed_fingerprint official_checksum_mismatch; die "official_checksum_mismatch" 3; }
tar -tzf "$tmp_dir/$asset" >"$tmp_dir/archive.entries" || { record_failed_fingerprint candidate_archive_invalid; die "candidate_archive_invalid" 3; }
awk '
    /^\// { exit 1 }
    {
        count = split($0, parts, "/")
        for (i = 1; i <= count; i++) if (parts[i] == "..") exit 1
    }
' "$tmp_dir/archive.entries" || { record_failed_fingerprint candidate_archive_unsafe_path; die "candidate_archive_unsafe_path" 3; }
tar -tvzf "$tmp_dir/$asset" | awk 'substr($0, 1, 1) != "-" && substr($0, 1, 1) != "d" { exit 1 }' \
    || { record_failed_fingerprint candidate_archive_unsafe_entry; die "candidate_archive_unsafe_entry" 3; }
mkdir -- "$tmp_dir/extracted"
tar --no-same-owner --no-same-permissions -xzf "$tmp_dir/$asset" -C "$tmp_dir/extracted" || { record_failed_fingerprint candidate_archive_invalid; die "candidate_archive_invalid" 3; }
candidate_list=$(find "$tmp_dir/extracted" -type f -name cli-proxy-api -print)
candidate_count=$(printf '%s\n' "$candidate_list" | awk 'NF { count++ } END { print count + 0 }')
[ "$candidate_count" -eq 1 ] || { record_failed_fingerprint candidate_binary_ambiguous; die "candidate_binary_ambiguous" 3; }
candidate_cli=$(printf '%s\n' "$candidate_list" | sed -n '1p')
[ -n "$candidate_cli" ] && [ ! -L "$candidate_cli" ] || { record_failed_fingerprint candidate_binary_ambiguous; die "candidate_binary_ambiguous" 3; }
candidate_sha=$(sha256sum "$candidate_cli" | awk '{ print $1 }')
[ "$candidate_sha" = "$cli_sha" ] || { record_failed_fingerprint candidate_hash_not_approved; die "candidate_hash_not_approved" 3; }
chmod 755 "$candidate_cli"

if ! verifier_result preflight "$candidate_cli" "$approval_file" "$tmp_dir/preflight-verifier.json"; then
    code=$(report_code "$tmp_dir/preflight-verifier.json")
    die "current_preflight_blocked=$code"
fi

if ! "$compat_probe" -candidate "$candidate_cli" -plugin "$plugin_path" -timeout 45s >"$tmp_dir/compat-probe.json"; then
    record_failed_fingerprint compatibility_probe_failed
    die "candidate_compatibility_probe_failed" 3
fi

snapshot_backup_directories
if ! "$updater" "$release_tag"; then
    if ! resolve_new_backup_directory; then
        disable_timer
        alert "rollback_backup_discovery_failed timer=$timer_name disabled; expected exactly one new root-owned backup-pre-$release_tag-* directory created after $update_started_at"
        exit 4
    fi
    rollback_candidate "existing_official_updater_failed" true
fi
if ! resolve_new_backup_directory; then
    disable_timer
    alert "rollback_backup_discovery_failed timer=$timer_name disabled; expected exactly one new root-owned backup-pre-$release_tag-* directory created after $update_started_at"
    exit 4
fi

if verifier_result postinstall "$cli_path" "$approval_file" "$tmp_dir/postinstall-verifier.json"; then
    log "update_accepted version=$cli_version fingerprint=$fingerprint"
    exit 0
else
    postinstall_rc=$?
fi
postinstall_code=$(report_code "$tmp_dir/postinstall-verifier.json")
if [ "$postinstall_rc" -eq 3 ] && plugin_compatibility_code "$postinstall_code"; then
    if override_reason=$(security_override_reason); then
        apply_security_override "$postinstall_code" "$override_reason"
    fi
fi
if [ "$postinstall_rc" -eq 3 ]; then
    rollback_candidate "$postinstall_code" true
fi
rollback_candidate "$postinstall_code" false
