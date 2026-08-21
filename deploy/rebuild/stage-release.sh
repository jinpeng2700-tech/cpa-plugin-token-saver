#!/bin/sh
set -eu

apply=false
bundle=
deployment_id=
deploy_root=${DEPLOY_ROOT:-/root/cliproxyapi.deployments}
next_link=${NEXT_LINK:-/root/cliproxyapi.next}

usage() {
    echo "usage: $0 --bundle DIR --deployment-id ID [--apply]" >&2
    exit 2
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --bundle) [ "$#" -ge 2 ] || usage; bundle=$2; shift 2 ;;
        --deployment-id) [ "$#" -ge 2 ] || usage; deployment_id=$2; shift 2 ;;
        --apply) apply=true; shift ;;
        *) usage ;;
    esac
done

[ -n "$bundle" ] && [ -n "$deployment_id" ] || usage
case "$deployment_id" in *[!A-Za-z0-9._-]*|'') echo "invalid deployment id" >&2; exit 2 ;; esac
case "$deploy_root" in /*) ;; *) echo "deploy root must be absolute" >&2; exit 2 ;; esac
case "$next_link" in /*) ;; *) echo "next link must be absolute" >&2; exit 2 ;; esac
case "$deploy_root:$next_link" in *"/../"*|*/..|/:*) echo "unsafe deployment path" >&2; exit 2 ;; esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 "$script_dir/validate-bundle.py" "$bundle"
target=$deploy_root/$deployment_id

if [ "$apply" != true ]; then
    echo "DRY RUN: would stage root-only deployment $target and atomically point $next_link to it"
    exit 0
fi
[ "$(id -u)" -eq 0 ] || { echo "root required for --apply" >&2; exit 1; }
[ ! -e "$target" ] && [ ! -L "$target" ] || { echo "deployment already exists: $target" >&2; exit 1; }
[ ! -L "$deploy_root" ] || { echo "deployment root must not be a symlink" >&2; exit 1; }

mkdir -p -- "$deploy_root"
chmod 0700 "$deploy_root"
temporary=$deploy_root/.$deployment_id.tmp.$$
temporary_link=$next_link.tmp.$$
cleanup() {
    rm -rf -- "$temporary"
    rm -f -- "$temporary_link"
}
trap cleanup EXIT HUP INT TERM

mkdir -m 0700 -- "$temporary"
cp -a -- "$bundle"/. "$temporary"/
mkdir -m 0700 -- "$temporary/state" "$temporary/state/auth" "$temporary/state/logs"
cp -- "$temporary/config/config.template.yaml" "$temporary/state/config.yaml"
chmod 0600 "$temporary/state/config.yaml"
chown -R 0:0 -- "$temporary"
find "$temporary" -type d -exec chmod 0700 {} +
find "$temporary" -type f -exec chmod 0600 {} +
chmod 0700 \
    "$temporary/cli-proxy-api" \
    "$temporary/plugins/linux/amd64/token-saver-v1.0.2.so" \
    "$temporary/tools/compat-probe" \
    "$temporary/tools/update-verifier" \
    "$temporary/deploy/stage-release.sh" \
    "$temporary/deploy/activate-release.sh" \
    "$temporary/deploy/rollback-release.sh" \
    "$temporary/deploy/validate-bundle.py"

mv -T -- "$temporary" "$target"
ln -s -- "$target" "$temporary_link"
mv -Tf -- "$temporary_link" "$next_link"
trap - EXIT HUP INT TERM
echo "staged: $target"
