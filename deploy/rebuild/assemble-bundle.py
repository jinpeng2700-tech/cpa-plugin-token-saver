#!/usr/bin/env python3
import argparse
import importlib.util
import json
import os
import pathlib
import re
import shutil
import sys
import tempfile


sys.dont_write_bytecode = True

CLI_TAG = "v7.2.136"
CLI_ARCHIVE_SHA256 = "8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288"
PLUGIN_VERSION = "1.2.3"
INPUTS = {
    "cli-proxy-api": "cli-proxy-api",
    "token-saver-v1.2.3.so": "plugins/linux/amd64/token-saver-v1.2.3.so",
    "compat-probe": "tools/compat-probe",
    "update-verifier": "tools/update-verifier",
}
TEMPLATES = {
    "config/config.template.yaml": "config/config.template.yaml",
    "systemd/cliproxyapi.service": "systemd/cliproxyapi.service",
    "nginx/cpa.ai2c.asia.conf": "nginx/cpa.ai2c.asia.conf",
    "firewall/cpa-network-guard.nft": "firewall/cpa-network-guard.nft",
    "firewall/cpa-network-guard.service": "firewall/cpa-network-guard.service",
    "firewall/cpa-network-guard.env.template": "firewall/cpa-network-guard.env.template",
    "stage-release.sh": "deploy/stage-release.sh",
    "activate-release.sh": "deploy/activate-release.sh",
    "rollback-release.sh": "deploy/rollback-release.sh",
    "validate-bundle.py": "deploy/validate-bundle.py",
}
def load_validator(script_dir):
    spec = importlib.util.spec_from_file_location("rebuild_validate_bundle", script_dir / "validate-bundle.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def copy_file(source, destination, executable=False):
    if not source.is_file() or source.is_symlink():
        raise ValueError(f"required regular input missing: {source}")
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    shutil.copyfile(source, destination)
    os.chmod(destination, 0o700 if executable else 0o600)


def validate_argument(value, pattern, name):
    if not re.fullmatch(pattern, value):
        raise ValueError(f"invalid {name}")
    return value


def build(staging, args, script_dir, repo_root, validator):
    input_dir = pathlib.Path(args.input_dir).resolve()
    if not input_dir.is_dir():
        raise ValueError(f"input directory missing: {input_dir}")

    for source_name, relative in INPUTS.items():
        copy_file(input_dir / source_name, staging / relative, relative in validator.EXECUTABLES)
    for source_name, relative in TEMPLATES.items():
        copy_file(script_dir / source_name, staging / relative, relative in validator.EXECUTABLES)

    license_dir = staging / "licenses"
    for source in sorted((repo_root / "licenses").glob("*")):
        if source.is_file():
            copy_file(source, license_dir / source.name)
    copy_file(repo_root / "LICENSE", license_dir / "token-saver-LICENSE.txt")
    copy_file(repo_root / "THIRD_PARTY_NOTICES.md", license_dir / "THIRD_PARTY_NOTICES.md")

    files = []
    for path in sorted(staging.rglob("*"), key=lambda item: item.relative_to(staging).as_posix()):
        if not path.is_file():
            continue
        relative = path.relative_to(staging).as_posix()
        files.append(
            {
                "path": relative,
                "sha256": validator.sha256(path),
                "mode": "0700" if relative in validator.EXECUTABLES else "0600",
            }
        )

    manifest = {
        "schema_version": 2,
        "verifier_schema": 1,
        "bundle": {
            "deployment_id": args.deployment_id,
            "source_commit": args.plugin_source_commit,
            "builder_digest": args.plugin_builder_digest,
        },
        "cli": {
            "tag": CLI_TAG,
            "archive_sha256": CLI_ARCHIVE_SHA256,
            "binary_sha256": validator.sha256(staging / "cli-proxy-api"),
            "arch": "linux-amd64",
        },
        "plugin": {
            "id": "token-saver",
            "version": PLUGIN_VERSION,
            "abi": 1,
            "rpc_schema": 3,
            "source_commit": args.plugin_source_commit,
            "builder_digest": args.plugin_builder_digest,
            "glibc_max": args.glibc_max,
            "sha256": validator.sha256(staging / "plugins/linux/amd64/token-saver-v1.2.3.so"),
        },
        "files": files,
        "manifest_exclusions": ["approved-artifacts.json", "SHA256SUMS"],
    }
    manifest_path = staging / "approved-artifacts.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(manifest_path, 0o600)

    summed = sorted(
        (path for path in staging.rglob("*") if path.is_file()),
        key=lambda item: item.relative_to(staging).as_posix(),
    )
    sums = "".join(f"{validator.sha256(path)}  {path.relative_to(staging).as_posix()}\n" for path in summed)
    sums_path = staging / "SHA256SUMS"
    sums_path.write_text(sums, encoding="utf-8")
    os.chmod(sums_path, 0o600)
    validator.validate_bundle(staging)


def main():
    parser = argparse.ArgumentParser(description="Assemble secret-free immutable CLIProxyAPI rebuild bundle.")
    parser.add_argument("--input-dir", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--deployment-id", required=True)
    parser.add_argument("--plugin-source-commit", required=True)
    parser.add_argument("--plugin-builder-digest", required=True)
    parser.add_argument("--glibc-max", required=True)
    parser.add_argument("--write", action="store_true", help="Write output; default validates in a disposable directory.")
    args = parser.parse_args()

    try:
        validate_argument(args.deployment_id, r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", "deployment id")
        validate_argument(args.plugin_source_commit, r"[0-9a-f]{7,40}", "plugin source commit")
        validate_argument(args.plugin_builder_digest, r"sha256:[0-9a-f]{64}", "plugin builder digest")
        validate_argument(args.glibc_max, r"[0-9]+(?:\.[0-9]+)+", "glibc max")

        output = pathlib.Path(args.output_dir).resolve()
        if output.exists() or output.is_symlink():
            raise ValueError(f"output already exists: {output}")

        script_dir = pathlib.Path(__file__).resolve().parent
        repo_root = script_dir.parents[1]
        validator = load_validator(script_dir)
        if args.write:
            output.parent.mkdir(parents=True, exist_ok=True)
            staging = pathlib.Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))
        else:
            staging = pathlib.Path(tempfile.mkdtemp(prefix="cliproxyapi-rebuild-dry-run-"))
        try:
            os.chmod(staging, 0o700)
            build(staging, args, script_dir, repo_root, validator)
            if args.write:
                os.replace(staging, output)
                staging = None
                print(f"bundle written: {output}")
            else:
                print(f"DRY RUN: bundle valid; would write {output}")
        finally:
            if staging is not None:
                shutil.rmtree(staging)
    except (OSError, ValueError) as error:
        print(f"bundle assembly failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
