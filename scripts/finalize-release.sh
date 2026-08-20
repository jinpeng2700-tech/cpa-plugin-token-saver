#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
	echo "usage: finalize-release.sh DIST_DIR VERSION SOURCE_COMMIT" >&2
	exit 2
fi

dist=$1
version=$2
source_commit=$3
readelf_tool=${READELF:-readelf}
objdump_tool=${OBJDUMP:-objdump}

printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' ||
	{ echo "release version must be stable SemVer" >&2; exit 1; }
printf '%s\n' "$source_commit" | grep -Eq '^[0-9a-f]{40}$' ||
	{ echo "source commit must be a full lowercase commit SHA" >&2; exit 1; }
test -d "$dist" || { echo "release directory is missing: $dist" >&2; exit 1; }

plugin="token-saver-v${version}-linux-amd64.so"
compat_probe="compat-probe-v${version}-linux-amd64"
update_verifier="update-verifier-v${version}-linux-amd64"

actual=$(find "$dist" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort)
expected=$(printf '%s\n' "$compat_probe" "$plugin" "$update_verifier" | LC_ALL=C sort)
test "$actual" = "$expected" ||
	{ echo "release directory contains missing or unexpected build outputs" >&2; exit 1; }

for file in "$plugin" "$compat_probe" "$update_verifier"; do
	test -s "$dist/$file" || { echo "release output is missing: $file" >&2; exit 1; }
done
for helper in "$compat_probe" "$update_verifier"; do
	test -x "$dist/$helper" || { echo "release helper is not executable: $helper" >&2; exit 1; }
done

"$readelf_tool" -h "$dist/$plugin" | grep -F 'Class:' | grep -Fq 'ELF64'
"$readelf_tool" -h "$dist/$plugin" | grep -F 'Machine:' | grep -Fq 'X86-64'
"$readelf_tool" -h "$dist/$plugin" | grep -F 'Type:' | grep -Fq 'DYN'

for helper in "$compat_probe" "$update_verifier"; do
	"$readelf_tool" -h "$dist/$helper" | grep -F 'Class:' | grep -Fq 'ELF64'
	"$readelf_tool" -h "$dist/$helper" | grep -F 'Machine:' | grep -Fq 'X86-64'
	"$readelf_tool" -h "$dist/$helper" | grep -F 'Type:' | grep -Fq 'EXEC'
	if "$objdump_tool" -p "$dist/$helper" | grep -q 'NEEDED'; then
		echo "dynamically linked helper rejected: $helper" >&2
		exit 1
	fi
done

glibc_versions=$("$objdump_tool" -T "$dist/$plugin" |
	sed -n 's/.*GLIBC_\([0-9][0-9.]*\).*/\1/p' |
	LC_ALL=C sort -Vu)
test -n "$glibc_versions" || { echo "plugin GLIBC evidence is missing" >&2; exit 1; }
max_glibc=$(printf '%s\n' "$glibc_versions" | tail -n 1)
if [ "$(printf '%s\n' "$max_glibc" 2.32 | sort -V | tail -n 1)" != "2.32" ]; then
	echo "plugin exceeds GLIBC ceiling 2.32: $max_glibc" >&2
	exit 1
fi

printf 'GLIBC_%s\n' $glibc_versions >"$dist/GLIBC_REQUIREMENTS.txt"
cat >"$dist/release-metadata.json" <<EOF
{
  "version": "$version",
  "tag": "v$version",
  "source_commit": "$source_commit",
  "platform": "linux-amd64",
  "abi": 1,
  "rpc": 3,
  "glibc_max": "$max_glibc"
}
EOF

(
	cd "$dist"
	sha256sum \
		"$plugin" \
		"$compat_probe" \
		"$update_verifier" \
		GLIBC_REQUIREMENTS.txt \
		release-metadata.json |
		LC_ALL=C sort -k2 >SHA256SUMS
	sha256sum -c SHA256SUMS
)
