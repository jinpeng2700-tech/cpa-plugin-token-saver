#!/bin/sh
set -eu

if [ "$#" -ne 5 ]; then
	echo "usage: verify-release-identity.sh EVENT_COMMIT BUILD_COMMIT METADATA REMOTE TAG" >&2
	exit 2
fi

event_commit=$1
build_commit=$2
metadata=$3
remote=$4
tag=$5

printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
	{ echo "invalid stable release tag" >&2; exit 1; }
test -f "$metadata" || { echo "release metadata is missing" >&2; exit 1; }

metadata_commit=$(awk -F'"' '$2 == "source_commit" { print $4 }' "$metadata")
test "$(printf '%s\n' "$metadata_commit" | sed '/^$/d' | wc -l)" -eq 1 ||
	{ echo "release metadata source commit is ambiguous" >&2; exit 1; }

tag_ref="refs/tags/$tag"
remote_refs=$(git ls-remote "$remote" "$tag_ref" "$tag_ref^{}")
direct=$(printf '%s\n' "$remote_refs" | awk -v ref="$tag_ref" '$2 == ref { print $1 }')
peeled=$(printf '%s\n' "$remote_refs" | awk -v ref="$tag_ref^{}" '$2 == ref { print $1 }')
test "$(printf '%s\n' "$direct" | sed '/^$/d' | wc -l)" -eq 1 ||
	{ echo "remote release tag is missing or ambiguous" >&2; exit 1; }
test "$(printf '%s\n' "$peeled" | sed '/^$/d' | wc -l)" -le 1 ||
	{ echo "peeled remote release tag is ambiguous" >&2; exit 1; }
remote_commit=${peeled:-$direct}

for commit in "$event_commit" "$build_commit" "$metadata_commit" "$remote_commit"; do
	printf '%s\n' "$commit" | grep -Eq '^[0-9a-f]{40}$' ||
		{ echo "release commit identity is invalid" >&2; exit 1; }
done
if [ "$event_commit" != "$build_commit" ] ||
	[ "$event_commit" != "$metadata_commit" ] ||
	[ "$event_commit" != "$remote_commit" ]; then
	echo "release commit identity mismatch" >&2
	exit 1
fi

printf '%s\n' "$event_commit"
