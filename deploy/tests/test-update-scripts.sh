#!/bin/sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/../.." && pwd)
wrapper=$repo_root/deploy/update-wrapper.sh
panel_installer=$repo_root/deploy/update-management-panel.sh
suite_root=$(mktemp -d "${TMPDIR:-/tmp}/token-saver-deploy-tests.XXXXXX")
trap 'case "$suite_root" in */token-saver-deploy-tests.*) rm -rf -- "$suite_root" ;; esac' EXIT HUP INT TERM

CLI_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
PLUGIN_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
PANEL_SHA=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
ARCHIVE_SHA=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
OLD_CLI_SHA=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
FINGERPRINT=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
OLD_PANEL_SHA=1111111111111111111111111111111111111111111111111111111111111111
TAMPERED_SHA=9999999999999999999999999999999999999999999999999999999999999999
SENTINEL=sentinel-management-key-never-log
export CLI_SHA PLUGIN_SHA PANEL_SHA ARCHIVE_SHA OLD_CLI_SHA FINGERPRINT OLD_PANEL_SHA TAMPERED_SHA SENTINEL

failures=0

fail() {
    printf 'FAIL: %s\n' "$1" >&2
    failures=$((failures + 1))
}

assert_eq() {
    [ "$1" = "$2" ] || fail "$3: got '$1', want '$2'"
}

assert_contains() {
    grep -F "$2" "$1" >/dev/null 2>&1 || fail "$3: missing '$2'"
}

assert_absent() {
    if [ -e "$1" ]; then fail "$2: unexpected $1"; fi
}

make_fakes() {
    root=$1
    bin=$root/bin
    mkdir -p "$bin"

    cat >"$bin/systemd" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'systemd\n' >>"$TEST_ROOT/credential-leaks"
printf 'systemd %s (test)\n' "${FAKE_SYSTEMD_VERSION:-247}"
EOF
    cat >"$bin/id" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'id\n' >>"$TEST_ROOT/credential-leaks"
printf '0\n'
EOF
    cat >"$bin/stat" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'stat\n' >>"$TEST_ROOT/credential-leaks"
printf '0 600\n'
EOF
    cat >"$bin/jq" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'jq\n' >>"$TEST_ROOT/credential-leaks"
all=$*
last=
for value do last=$value; done
case "$all" in
    *'@tsv'*)
        printf '1\t1\t1.2.3\t%s\tlinux-amd64\t0.1.0\t%s\t1\t3\tv0.1.0\t%s\n' "$CLI_SHA" "$PLUGIN_SHA" "$PANEL_SHA"
        ;;
    *'all(.overrides[]'*) exit 0 ;;
    *'[.overrides[]'*)
        [ "${FAKE_OVERRIDE_MATCH:-0}" = 1 ] || exit 1
        printf 'CVE-test-approved\n'
        ;;
    *'.tag_name'*) printf 'v1.2.3\n' ;;
    *'.panel.version'*) printf 'v0.1.0\n' ;;
    *'.panel.sha256'*) printf '%s\n' "$PANEL_SHA" ;;
    *'.code | select'*)
        sed -n 's/.*"code":"\([^"]*\)".*/\1/p' "$last"
        ;;
    *'.fingerprint'*)
        sed -n 's/.*"fingerprint":"\([^"]*\)".*/\1/p' "$last"
        ;;
    *'.cli.version = $version'*) printf '{}\n' ;;
    *) exit 0 ;;
esac
EOF
    cat >"$bin/curl" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'curl\n' >>"$TEST_ROOT/credential-leaks"
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -o) output=$2; shift 2 ;;
        --*) shift ;;
        *) url=$1; shift ;;
    esac
done
printf 'curl %s\n' "$url" >>"$TEST_ROOT/events"
case "$url" in
    */releases/latest|*/releases/tags/*) printf '{"tag_name":"v1.2.3"}\n' >"$output" ;;
    */checksums.txt) printf '%s  CLIProxyAPI_1.2.3_linux_amd64.tar.gz\n' "$ARCHIVE_SHA" >"$output" ;;
    */CLIProxyAPI_1.2.3_linux_amd64.tar.gz) printf 'archive\n' >"$output" ;;
    */management.html)
        if [ "${FAKE_PANEL_TAMPER:-0}" = 1 ]; then printf 'panel-tampered\n' >"$output"; else printf 'panel-new\n' >"$output"; fi
        ;;
    *) exit 1 ;;
esac
EOF
    cat >"$bin/sha256sum" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'sha256sum\n' >>"$TEST_ROOT/credential-leaks"
if [ "$#" -eq 0 ]; then
    while IFS= read -r ignored; do :; done
    printf '%s  -\n' "$FINGERPRINT"
    exit 0
fi
content=$(tr -d '\r\n' <"$1")
case "$content" in
    archive) hash=$ARCHIVE_SHA ;;
    candidate-cli) hash=$CLI_SHA ;;
    old-cli) hash=$OLD_CLI_SHA ;;
    panel-new) hash=$PANEL_SHA ;;
    panel-old) hash=$OLD_PANEL_SHA ;;
    panel-tampered) hash=$TAMPERED_SHA ;;
    *) hash=$TAMPERED_SHA ;;
esac
printf '%s  %s\n' "$hash" "$1"
EOF
    cat >"$bin/tar" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'tar\n' >>"$TEST_ROOT/credential-leaks"
case " $* " in
    *' -tzf '*) printf 'cli-proxy-api\n'; exit 0 ;;
    *' -tvzf '*) printf '%s\n' '-rwxr-xr-x root/root 14 2026-08-17 00:00 cli-proxy-api'; exit 0 ;;
esac
destination=
while [ "$#" -gt 0 ]; do
    case "$1" in -C) destination=$2; shift 2 ;; *) shift ;; esac
done
printf 'candidate-cli\n' >"$destination/cli-proxy-api"
EOF
    cat >"$bin/systemctl" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'systemctl\n' >>"$TEST_ROOT/credential-leaks"
printf 'systemctl %s\n' "$*" >>"$TEST_ROOT/events"
case "$*" in
    *'restart cliproxyapi.service'*) [ "${FAKE_RESTART_FAIL:-0}" = 1 ] && exit 1 ;;
esac
exit 0
EOF
    cat >"$bin/systemd-cat" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'systemd-cat\n' >>"$TEST_ROOT/credential-leaks"
message=$(cat)
printf 'alert %s\n' "$message" >>"$TEST_ROOT/events"
EOF
    cat >"$bin/logger" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'logger\n' >>"$TEST_ROOT/credential-leaks"
exit 0
EOF
    chmod +x "$bin"/*
}

make_case() {
    name=$1
    root=$suite_root/$name
    app=$root/app
    mkdir -p "$app/static" "$root/tmp" "$root/credentials"
    : >"$root/events"
    printf 'old-cli\n' >"$app/cli-proxy-api"
    printf '1.0.0\n' >"$app/version.txt"
    printf 'old-service\n' >"$app/cliproxyapi.service"
    printf 'plugin\n' >"$app/token-saver.so"
    printf 'panel-old\n' >"$app/static/management.html"
    printf '{}\n' >"$app/approved-artifacts.json"
    printf '%s\n' "$SENTINEL" >"$root/credentials/cliproxyapi-management-key"
    cat >"$app/config.yaml" <<'EOF'
remote-management:
  disable-auto-update-panel: true
  panel-github-repository: "https://github.com/example/panel-fork"
EOF
    make_fakes "$root"

    cat >"$app/update.sh" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'updater\n' >>"$TEST_ROOT/credential-leaks"
printf 'updater %s\n' "$*" >>"$TEST_ROOT/events"
[ "${FAKE_UPDATER_FAIL:-0}" = 0 ] || exit 1
if [ "${FAKE_NO_BACKUP:-0}" = 0 ]; then
    backup="$CLIPROXYAPI_HOME/backup-pre-v1.2.3-20260817-000000"
    mkdir -p "$backup"
    cp "$CLIPROXYAPI_BINARY" "$backup/cli-proxy-api"
    cp "$CLIPROXYAPI_VERSION_FILE" "$backup/version.txt"
    cp "$CLIPROXYAPI_SERVICE_FILE" "$backup/cliproxyapi.service"
    if [ "${FAKE_MULTIPLE_BACKUPS:-0}" = 1 ]; then
        cp -R "$backup" "$CLIPROXYAPI_HOME/backup-pre-v1.2.3-20260817-000001"
    fi
fi
printf 'candidate-cli\n' >"$CLIPROXYAPI_BINARY"
[ "${FAKE_UPDATER_PARTIAL_FAIL:-0}" = 0 ] || exit 1
printf '1.2.3\n' >"$CLIPROXYAPI_VERSION_FILE"
EOF
    cat >"$app/compat-probe" <<'EOF'
#!/bin/sh
[ -z "${CREDENTIALS_DIRECTORY-}" ] || printf 'compat-probe\n' >>"$TEST_ROOT/credential-leaks"
case " $* " in
    *' -mode core-only '*)
        printf 'compat core-only\n' >>"$TEST_ROOT/events"
        [ "${FAKE_CORE_PROBE_FAIL:-0}" = 0 ]
        ;;
    *)
        printf 'compat normal\n' >>"$TEST_ROOT/events"
        [ "${FAKE_COMPAT_FAIL:-0}" = 0 ]
        ;;
esac
EOF
    cat >"$app/update-verifier" <<'EOF'
#!/bin/sh
phase=
approval=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -phase) phase=$2; shift 2 ;;
        -approval) approval=$2; shift 2 ;;
        *) shift ;;
    esac
done
[ -n "${CREDENTIALS_DIRECTORY-}" ] || { printf '{"classification":"blocked","code":"credential_directory_missing"}\n'; exit 2; }
credential=$(tr -d '\r\n' <"$CREDENTIALS_DIRECTORY/cliproxyapi-management-key")
[ "$credential" = "$SENTINEL" ] || exit 2
printf 'verifier-credential-seen\n' >>"$TEST_ROOT/verifier-credential-seen"
case "$approval" in
    *rollback-approval*)
        printf 'verifier rollback\n' >>"$TEST_ROOT/events"
        if [ "${FAKE_ROLLBACK_VERIFY_FAIL:-0}" = 1 ]; then
            printf '{"classification":"blocked","code":"runtime_unhealthy"}\n'
            exit 2
        fi
        printf '{"classification":"compatible","code":"ok"}\n'
        exit 0
        ;;
esac
case "$phase" in
    preflight)
        printf 'verifier preflight\n' >>"$TEST_ROOT/events"
        if [ "${FAKE_PREFLIGHT_BLOCKED:-0}" = 1 ]; then
            printf '{"classification":"blocked","code":"management_auth_failed"}\n'
            exit 2
        fi
        printf '{"classification":"compatible","code":"ok"}\n'
        ;;
    postinstall)
        printf 'verifier postinstall\n' >>"$TEST_ROOT/events"
        case "${FAKE_POSTINSTALL:-ok}" in
            ok) printf '{"classification":"compatible","code":"ok"}\n' ;;
            blocked) printf '{"classification":"blocked","code":"config_race"}\n'; exit 2 ;;
            candidate) printf '{"classification":"candidate_failure","code":"%s"}\n' "${FAKE_POSTINSTALL_CODE:-abi_mismatch}"; exit 3 ;;
        esac
        ;;
esac
EOF
    chmod +x "$app/update.sh" "$app/compat-probe" "$app/update-verifier"
    printf '%s\n' "$root"
}

run_wrapper() {
    root=$1
    shift
    set +e
    (
        export TEST_ROOT=$root
        export PATH=$root/bin:$PATH
        export TMPDIR=$root/tmp
        export CLIPROXYAPI_HOME=$root/app
        export APPROVED_ARTIFACTS_FILE=$root/app/approved-artifacts.json
        export SECURITY_OVERRIDES_FILE=$root/app/security-overrides.json
        export TOKEN_SAVER_UPDATE_STATE_DIR=$root/app/.token-saver-update
        export TOKEN_SAVER_QUARANTINE_DIR=$root/app/.token-saver-update/quarantine
        export OFFICIAL_UPDATER=$root/app/update.sh
        export CLIPROXYAPI_BINARY=$root/app/cli-proxy-api
        export CLIPROXYAPI_VERSION_FILE=$root/app/version.txt
        export CLIPROXYAPI_SERVICE_FILE=$root/app/cliproxyapi.service
        export TOKEN_SAVER_PLUGIN_FILE=$root/app/token-saver.so
        export MANAGEMENT_PANEL_FILE=$root/app/static/management.html
        export COMPAT_PROBE_FILE=$root/app/compat-probe
        export UPDATE_VERIFIER_FILE=$root/app/update-verifier
        export CREDENTIALS_DIRECTORY=$root/credentials
        sh "$wrapper" latest
    ) >"$root/stdout" 2>"$root/stderr"
    RUN_RC=$?
    set -e
}

test_systemd_239_refusal() {
    root=$(make_case systemd239)
    export FAKE_SYSTEMD_VERSION=239
    run_wrapper "$root"
    unset FAKE_SYSTEMD_VERSION
    assert_eq "$RUN_RC" 2 'systemd 239 refusal exit'
    assert_contains "$root/stderr" 'systemd_version_unsupported=239' 'systemd 239 refusal'
    assert_absent "$root/app/.token-saver-update/failed-candidate.json" 'systemd 239 must not blame candidate'
    if grep -F 'updater ' "$root/events" >/dev/null; then fail 'systemd 239 called updater'; fi
}

test_normal_update_and_credential_boundary() {
    root=$(make_case normal)
    run_wrapper "$root"
    if [ "$RUN_RC" -ne 0 ]; then
        sed 's/^/normal stderr: /' "$root/stderr" >&2
    fi
    assert_eq "$RUN_RC" 0 'normal update exit'
expected='verifier preflight
compat normal
updater v1.2.3
verifier postinstall'
    actual=$(grep -E '^(verifier|compat|updater)' "$root/events" || true)
    assert_eq "$actual" "$expected" 'normal gate ordering'
    assert_absent "$root/credential-leaks" 'credential directory leaked to a non-verifier child'
    assert_contains "$root/verifier-credential-seen" 'verifier-credential-seen' 'verifier credential handoff'
}

test_preflight_blocked_not_fingerprinted() {
    root=$(make_case blocked)
    export FAKE_PREFLIGHT_BLOCKED=1
    run_wrapper "$root"
    unset FAKE_PREFLIGHT_BLOCKED
    assert_eq "$RUN_RC" 2 'blocked preflight exit'
    assert_absent "$root/app/.token-saver-update/failed-candidate.json" 'blocked preflight fingerprint'
    if grep -F 'updater ' "$root/events" >/dev/null; then fail 'blocked preflight called updater'; fi
}

test_same_failed_fingerprint_skips() {
    root=$(make_case fingerprint)
    mkdir -p "$root/app/.token-saver-update"
    printf '{"schema_version":1,"fingerprint":"%s"}\n' "$FINGERPRINT" >"$root/app/.token-saver-update/failed-candidate.json"
    run_wrapper "$root"
    assert_eq "$RUN_RC" 0 'same fingerprint skip exit'
    assert_contains "$root/stderr" 'candidate_skipped_same_failed_fingerprint' 'same fingerprint skip'
    if [ -s "$root/events" ]; then fail 'same fingerprint performed external update work'; fi
}

test_candidate_failure_rolls_back() {
    root=$(make_case rollback)
    export FAKE_POSTINSTALL=candidate
    run_wrapper "$root"
    unset FAKE_POSTINSTALL
    assert_eq "$RUN_RC" 3 'candidate rollback exit'
    assert_eq "$(tr -d '\r\n' <"$root/app/cli-proxy-api")" old-cli 'candidate rollback binary'
    assert_contains "$root/events" 'verifier rollback' 'candidate rollback verification'
    [ -f "$root/app/.token-saver-update/failed-candidate.json" ] || fail 'candidate rollback did not record fingerprint'
}

test_partial_updater_failure_rolls_back() {
    root=$(make_case updater-partial-failure)
    export FAKE_UPDATER_PARTIAL_FAIL=1
    run_wrapper "$root"
    unset FAKE_UPDATER_PARTIAL_FAIL
    assert_eq "$RUN_RC" 3 'partial updater failure rollback exit'
    assert_eq "$(tr -d '\r\n' <"$root/app/cli-proxy-api")" old-cli 'partial updater failure rollback binary'
    assert_eq "$(tr -d '\r\n' <"$root/app/version.txt")" 1.0.0 'partial updater failure rollback version'
    assert_contains "$root/events" 'verifier rollback' 'partial updater failure rollback verification'
    [ -f "$root/app/.token-saver-update/failed-candidate.json" ] || fail 'partial updater failure did not record fingerprint'
}

test_security_override_keeps_cli_and_isolates_plugin() {
    root=$(make_case override)
    printf '{"schema_version":1,"overrides":[]}\n' >"$root/app/security-overrides.json"
    export FAKE_POSTINSTALL=candidate FAKE_OVERRIDE_MATCH=1
    run_wrapper "$root"
    unset FAKE_POSTINSTALL FAKE_OVERRIDE_MATCH
    assert_eq "$RUN_RC" 0 'security override exit'
    assert_eq "$(tr -d '\r\n' <"$root/app/cli-proxy-api")" candidate-cli 'security override retained CLI'
    assert_absent "$root/app/token-saver.so" 'security override plugin isolation'
    assert_contains "$root/events" 'compat core-only' 'security override raw inference proof'
    assert_contains "$root/events" 'security_override_applied' 'security override alert'
}

test_rollback_failure_disables_timer() {
    root=$(make_case rollback-failure)
    export FAKE_POSTINSTALL=candidate FAKE_RESTART_FAIL=1
    run_wrapper "$root"
    unset FAKE_POSTINSTALL FAKE_RESTART_FAIL
    assert_eq "$RUN_RC" 4 'rollback failure exit'
    assert_contains "$root/events" 'systemctl --user disable --now cliproxyapi-update.timer' 'rollback failure timer disable'
    assert_contains "$root/events" 'rollback_failed' 'rollback failure alert'
}

test_backup_discovery_requires_one_new_directory() {
    root=$(make_case no-new-backup)
    export FAKE_NO_BACKUP=1
    run_wrapper "$root"
    unset FAKE_NO_BACKUP
    assert_eq "$RUN_RC" 4 'missing new backup exit'
    assert_contains "$root/events" 'systemctl --user disable --now cliproxyapi-update.timer' 'missing new backup timer disable'
    assert_contains "$root/events" 'rollback_backup_discovery_failed' 'missing new backup alert'

    root=$(make_case ambiguous-backup)
    export FAKE_MULTIPLE_BACKUPS=1
    run_wrapper "$root"
    unset FAKE_MULTIPLE_BACKUPS
    assert_eq "$RUN_RC" 4 'ambiguous new backup exit'
    assert_contains "$root/events" 'systemctl --user disable --now cliproxyapi-update.timer' 'ambiguous backup timer disable'
    assert_contains "$root/events" 'rollback_backup_discovery_failed' 'ambiguous backup alert'
}

run_panel() {
    root=$1
    tag=$2
    set +e
    (
        export TEST_ROOT=$root
        export PATH=$root/bin:$PATH
        export TMPDIR=$root/tmp
        export CLIPROXYAPI_HOME=$root/app
        export APPROVED_ARTIFACTS_FILE=$root/app/approved-artifacts.json
        export CLIPROXYAPI_CONFIG_FILE=$root/app/config.yaml
        export MANAGEMENT_PANEL_FILE=$root/app/static/management.html
        export PANEL_UPDATE_STATE_DIR=$root/app/.panel-update
        export PANEL_RELEASE_REPOSITORY=example/panel-fork
        export CREDENTIALS_DIRECTORY=$root/credentials
        sh "$panel_installer" "$tag"
    ) >"$root/panel-stdout" 2>"$root/panel-stderr"
    PANEL_RC=$?
    set -e
}

test_panel_install_and_tamper_rejection() {
    root=$(make_case panel-normal)
    run_panel "$root" v0.1.0
    assert_eq "$PANEL_RC" 0 'panel normal exit'
    assert_eq "$(tr -d '\r\n' <"$root/app/static/management.html")" panel-new 'panel atomic install'
    [ -f "$root/app/.panel-update/management.html.lkg" ] || fail 'panel LKG missing'
    assert_absent "$root/credential-leaks" 'panel inherited credential directory'

    root=$(make_case panel-tamper)
    export FAKE_PANEL_TAMPER=1
    run_panel "$root" v0.1.0
    unset FAKE_PANEL_TAMPER
    assert_eq "$PANEL_RC" 3 'panel tamper exit'
    assert_eq "$(tr -d '\r\n' <"$root/app/static/management.html")" panel-old 'panel tamper preserved installed panel'
}

test_systemd_239_refusal
test_normal_update_and_credential_boundary
test_preflight_blocked_not_fingerprinted
test_same_failed_fingerprint_skips
test_partial_updater_failure_rolls_back
test_candidate_failure_rolls_back
test_security_override_keeps_cli_and_isolates_plugin
test_rollback_failure_disables_timer
test_backup_discovery_requires_one_new_directory
test_panel_install_and_tamper_rejection

if [ "$failures" -ne 0 ]; then
    exit 1
fi
printf 'deploy behavior tests: PASS\n'
