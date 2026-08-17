#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

require_nonempty() {
	if [ ! -s "$repo_root/$1" ]; then
		echo "required release input is missing: $1" >&2
		exit 1
	fi
}

for required in \
	LICENSE \
	THIRD_PARTY_NOTICES.md \
	licenses/9router-MIT.txt \
	licenses/rtk-Apache-2.0.txt \
	licenses/caveman-MIT.txt \
	licenses/ponytail-MIT.txt \
	go.mod \
	go.sum
do
	require_nonempty "$required"
done

for notice in \
	"9router 0.5.55" \
	"RTK (Rust Token Killer)" \
	"Caveman prompt face" \
	"Ponytail prompt face" \
	"Business Source License 1.1"
do
	if ! grep -Fq "$notice" "$repo_root/THIRD_PARTY_NOTICES.md" "$repo_root/licenses/caveman-MIT.txt"; then
		echo "required notice text is missing: $notice" >&2
		exit 1
	fi
done

workflow_files=$(find "$repo_root/.github/workflows" -type f \( -name '*.yml' -o -name '*.yaml' \) -print)
if [ -z "$workflow_files" ]; then
	echo "release workflows are missing" >&2
	exit 1
fi
invalid_actions=$(grep -hE '^[[:space:]]*-[[:space:]]*uses:' $workflow_files | grep -Ev 'uses:[[:space:]]*[^@[:space:]]+@[0-9a-f]{40}([[:space:]]*#.*)?$' || true)
if [ -n "$invalid_actions" ]; then
	echo "workflow actions must use immutable 40-character SHAs" >&2
	exit 1
fi

(
	cd "$repo_root"
	go mod verify
	go list -mod=readonly -m all >/dev/null
)
