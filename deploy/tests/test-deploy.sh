#!/bin/sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/../.." && pwd)
failures=0

fail() {
    printf 'FAIL: %s\n' "$1" >&2
    failures=$((failures + 1))
}

assert_file() {
    [ -f "$repo_root/$1" ] || fail "missing $1"
}

for path in \
    deploy/update-wrapper.sh \
    deploy/approved-artifacts.example.json \
    deploy/security-overrides.example.json \
    deploy/systemd/cliproxyapi-updater.service.d/credentials.conf
do
    assert_file "$path"
done

drop_in="$repo_root/deploy/systemd/cliproxyapi-updater.service.d/credentials.conf"
if [ -f "$drop_in" ]; then
    grep -F 'LoadCredential=cliproxyapi-management-key:/root/' "$drop_in" >/dev/null || fail 'drop-in lacks absolute LoadCredential source'
    grep -F 'ExecStart=/root/cliproxyapi/update-wrapper.sh latest' "$drop_in" >/dev/null || fail 'drop-in does not replace ExecStart with wrapper'
    grep -F 'Requires systemd >= 247' "$drop_in" >/dev/null || fail 'drop-in lacks systemd 247 guard notice'
    if grep -F 'Environment=CLIPROXYAPI_MANAGEMENT_KEY' "$drop_in" >/dev/null; then
        fail 'drop-in exposes the management credential as an environment variable'
    fi
fi

wrapper="$repo_root/deploy/update-wrapper.sh"
if [ -f "$wrapper" ]; then
    grep -F 'compat-probe' "$wrapper" >/dev/null || fail 'wrapper lacks compat-probe gate'
    grep -F 'update-verifier' "$wrapper" >/dev/null || fail 'wrapper lacks verifier gate'
    grep -F 'CREDENTIALS_DIRECTORY' "$wrapper" >/dev/null || fail 'wrapper lacks credential-directory handoff'
    grep -F '"$updater" "$release_tag"' "$wrapper" >/dev/null || fail 'wrapper does not pin the existing updater to the pre-probed exact tag'
    grep -F 'plugin_path=/root/.cli-proxy-api/plugins/linux/$archive_arch/token-saver.so' "$wrapper" >/dev/null || fail 'wrapper lacks the host OS/arch plugin path default'
    if grep -F "IFS='\\t'" "$wrapper" >/dev/null || grep -F "IFS='\\n'" "$wrapper" >/dev/null; then
        fail 'wrapper uses literal backslash IFS delimiters'
    fi
    if grep -F '/v0/management' "$wrapper" >/dev/null; then
        fail 'wrapper calls the Management API'
    fi
fi

if [ "$failures" -ne 0 ]; then
    exit 1
fi
printf 'deploy static contracts: PASS\n'
