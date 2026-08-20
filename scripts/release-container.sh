#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: release-container.sh REPOSITORY SOURCE_REF DIST_DIR" >&2
	exit 2
fi

repository=$1
reference=$2
dist=$3
docker_tool=${DOCKER:-docker}

test ! -e "$dist" || { echo "release destination already exists: $dist" >&2; exit 1; }
commit=$(git -c core.autocrlf=false -C "$repository" rev-parse --verify "$reference^{commit}")
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

git -c core.autocrlf=false -C "$repository" show "$commit:scripts/archive-source.sh" >"$temporary/archive-source.sh"
sh "$temporary/archive-source.sh" "$repository" "$commit" >"$temporary/source.tar"
mkdir "$temporary/source"
tar -xf "$temporary/source.tar" -C "$temporary/source"

"$docker_tool" build \
	--build-arg "SOURCE_COMMIT=$commit" \
	--file "$temporary/source/build/release.Dockerfile" \
	--output "type=local,dest=$dist" \
	"$temporary/source"
