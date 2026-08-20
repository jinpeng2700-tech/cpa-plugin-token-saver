#!/bin/sh
set -eu

repository=${1:-.}
reference=${2:-HEAD}
commit=$(git -c core.autocrlf=false -C "$repository" rev-parse --verify "$reference^{commit}")

git -c core.autocrlf=false -C "$repository" archive --format=tar "$commit"
