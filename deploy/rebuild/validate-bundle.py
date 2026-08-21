#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import pathlib
import re
import stat
import sys


MANIFEST = "approved-artifacts.json"
SUMS = "SHA256SUMS"
HEX64 = re.compile(r"^[0-9a-f]{64}$")
COMMIT = re.compile(r"^[0-9a-f]{7,40}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
SENSITIVE_PATH = re.compile(r"(^|/)(auth|credentials?|oauth|secrets?)(/|$)|(^|/)\.env$|\.(key|pem|p12)$", re.I)
SENSITIVE_ASSIGNMENT = re.compile(
    rb"(?im)^\s*[\"']?(api[-_ ]?keys?|secret[-_ ]?key|client[-_ ]?secret|access[-_ ]?token|"
    rb"refresh[-_ ]?token|password|authorization)[\"']?[ \t]*[:=][ \t]*[\"']?([^\s#\"']+)"
)
JSON_SENSITIVE_ASSIGNMENT = re.compile(
    rb"(?i)[\"'](api[-_ ]?keys?|secret[-_ ]?key|client[-_ ]?secret|access[-_ ]?token|"
    rb"refresh[-_ ]?token|password|authorization)[\"'][ \t]*:[ \t]*[\"']([^\"']+)"
)
SECRET_MARKERS = (
    re.compile(rb"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----"),
    re.compile(rb"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(rb"\bgh[pousr]_[A-Za-z0-9_]{20,}\b"),
    re.compile(rb"\bsk-[A-Za-z0-9_-]{8,}\b"),
)
PLACEHOLDERS = (b"REPLACE_", b"CHANGEME", b"REQUIRED", b"${", b"<")
ELF_EXECUTABLES = {
    "cli-proxy-api",
    "plugins/linux/amd64/token-saver-v1.0.2.so",
    "tools/compat-probe",
    "tools/update-verifier",
}
EXECUTABLES = ELF_EXECUTABLES | {
    "deploy/stage-release.sh",
    "deploy/activate-release.sh",
    "deploy/rollback-release.sh",
    "deploy/validate-bundle.py",
}


def fail(message):
    raise ValueError(message)


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def mode(path):
    return f"0{stat.S_IMODE(path.stat().st_mode):03o}"


def scan_secrets(path, relative):
    normalized = relative.as_posix()
    if SENSITIVE_PATH.search(normalized):
        fail(f"secret path rejected: {normalized}")
    raw = path.read_bytes()
    if normalized in ELF_EXECUTABLES:
        if not raw.startswith(b"\x7fELF"):
            fail(f"executable is not ELF: {normalized}")
        return
    for marker in SECRET_MARKERS:
        if marker.search(raw):
            fail(f"secret content rejected: {normalized}")
    for assignment in (SENSITIVE_ASSIGNMENT, JSON_SENSITIVE_ASSIGNMENT):
        for match in assignment.finditer(raw):
            value = match.group(2).upper()
            if not any(placeholder in value for placeholder in PLACEHOLDERS):
                fail(f"secret assignment rejected: {normalized}")


def require_object(parent, name):
    value = parent.get(name)
    if not isinstance(value, dict):
        fail(f"{name} must be an object")
    return value


def require_keys(value, expected, name):
    if set(value) != set(expected):
        fail(f"{name} has unexpected or missing fields")


def validate_identity(manifest):
    require_keys(
        manifest,
        {"schema_version", "verifier_schema", "bundle", "cli", "plugin", "files", "manifest_exclusions"},
        "manifest",
    )
    if manifest.get("schema_version") != 2:
        fail("schema_version must be 2")
    if manifest.get("verifier_schema") != 1:
        fail("verifier_schema must be 1")
    if manifest.get("manifest_exclusions") != [MANIFEST, SUMS]:
        fail("manifest_exclusions must identify self-referential files")

    bundle = require_object(manifest, "bundle")
    require_keys(bundle, {"deployment_id", "source_commit", "builder_digest"}, "bundle")
    if (
        not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", str(bundle.get("deployment_id", "")))
        or not COMMIT.fullmatch(str(bundle.get("source_commit", "")))
        or not DIGEST.fullmatch(str(bundle.get("builder_digest", "")))
    ):
        fail("invalid bundle identity")

    cli = require_object(manifest, "cli")
    require_keys(cli, {"tag", "archive_sha256", "binary_sha256", "arch"}, "cli")
    if cli.get("tag") != "v7.2.136":
        fail("cli.tag must be v7.2.136")
    if cli.get("archive_sha256") != "8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288":
        fail("cli.archive_sha256 mismatch")
    if cli.get("arch") != "linux-amd64" or not HEX64.fullmatch(str(cli.get("binary_sha256", ""))):
        fail("invalid CLI identity")

    plugin = require_object(manifest, "plugin")
    require_keys(
        plugin,
        {"id", "version", "abi", "rpc_schema", "source_commit", "builder_digest", "glibc_max", "sha256"},
        "plugin",
    )
    if (
        plugin.get("id") != "token-saver"
        or plugin.get("version") != "1.0.2"
        or plugin.get("abi") != 1
        or plugin.get("rpc_schema") != 3
        or not COMMIT.fullmatch(str(plugin.get("source_commit", "")))
        or not DIGEST.fullmatch(str(plugin.get("builder_digest", "")))
        or not HEX64.fullmatch(str(plugin.get("sha256", "")))
    ):
        fail("invalid plugin identity")
    try:
        glibc = tuple(int(part) for part in str(plugin.get("glibc_max", "")).split("."))
    except ValueError as error:
        raise ValueError("invalid plugin glibc_max") from error
    if len(glibc) < 2 or glibc > (2, 32):
        fail("plugin glibc_max exceeds 2.32")


def validate_bundle(root):
    root = pathlib.Path(root)
    if root.is_symlink():
        fail(f"bundle directory must not be a symlink: {root}")
    root = root.resolve()
    if not root.is_dir():
        fail(f"bundle directory missing: {root}")
    for path in root.rglob("*"):
        if path.is_symlink():
            fail(f"symlink rejected: {path.relative_to(root).as_posix()}")

    manifest_path = root / MANIFEST
    sums_path = root / SUMS
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"invalid {MANIFEST}: {error}") from error
    validate_identity(manifest)

    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        fail("files must be a non-empty array")
    listed = []
    listed_hashes = {}
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != {"path", "sha256", "mode"}:
            fail("each files entry must contain only path, sha256, mode")
        relative = pathlib.PurePosixPath(str(entry["path"]))
        if relative.is_absolute() or ".." in relative.parts or relative.as_posix() in (MANIFEST, SUMS):
            fail(f"invalid manifest path: {relative}")
        if relative.as_posix() in listed:
            fail(f"duplicate manifest path: {relative}")
        listed.append(relative.as_posix())
        path = root.joinpath(*relative.parts)
        if not path.is_file():
            fail(f"manifest file missing: {relative}")
        if not HEX64.fullmatch(str(entry["sha256"])) or sha256(path) != entry["sha256"]:
            fail(f"hash mismatch: {relative}")
        if not re.fullmatch(r"0[0-7]{3}", str(entry["mode"])):
            fail(f"mode mismatch: {relative}")
        expected_mode = "0700" if relative.as_posix() in EXECUTABLES else "0600"
        if entry["mode"] != expected_mode:
            fail(f"non-root-only mode: {relative}")
        if os.name != "nt" and mode(path) != entry["mode"]:
            fail(f"mode mismatch: {relative}")
        listed_hashes[relative.as_posix()] = entry["sha256"]
        scan_secrets(path, relative)
    if listed != sorted(listed):
        fail("manifest files must be sorted")

    actual = sorted(path.relative_to(root).as_posix() for path in root.rglob("*") if path.is_file())
    if actual != sorted(listed + [MANIFEST, SUMS]):
        fail("manifest does not cover complete bundle file set")
    for relative, digest in {
        "cli-proxy-api": manifest["cli"]["binary_sha256"],
        "plugins/linux/amd64/token-saver-v1.0.2.so": manifest["plugin"]["sha256"],
    }.items():
        if listed_hashes.get(relative) != digest:
            fail(f"identity hash mismatch: {relative}")

    expected_sums = {}
    try:
        for line in sums_path.read_text(encoding="utf-8").splitlines():
            digest, separator, relative = line.partition("  ")
            if not separator or not HEX64.fullmatch(digest) or relative in expected_sums:
                fail(f"invalid {SUMS} line")
            expected_sums[relative] = digest
    except OSError as error:
        raise ValueError(f"invalid {SUMS}: {error}") from error
    summed = sorted(path for path in actual if path != SUMS)
    if list(expected_sums) != summed:
        fail(f"{SUMS} must list every file except itself in sorted order")
    for relative, digest in expected_sums.items():
        if sha256(root / relative) != digest:
            fail(f"{SUMS} mismatch: {relative}")

    scan_secrets(manifest_path, pathlib.PurePosixPath(MANIFEST))
    scan_secrets(sums_path, pathlib.PurePosixPath(SUMS))
    return manifest


def main():
    parser = argparse.ArgumentParser(description="Validate immutable CLIProxyAPI rebuild bundle.")
    parser.add_argument("bundle")
    args = parser.parse_args()
    try:
        manifest = validate_bundle(pathlib.Path(args.bundle))
    except ValueError as error:
        print(f"bundle validation failed: {error}", file=sys.stderr)
        return 1
    print(f"bundle valid: {manifest['bundle']['deployment_id']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
