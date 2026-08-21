#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Callable, Optional

# ponytail: build patched official panel into self-contained single-file management.html

ALLOWED_UPSTREAM_URL = 'https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git'
HEX64_PATTERN = re.compile(r"^[0-9a-f]{64}$")
COMMIT_PATTERN = re.compile(r'^[0-9a-f]{40}$')


def default_cmd_runner(cwd: Path, cmd: list[str]) -> None:
    subprocess.run(cmd, cwd=str(cwd), check=True)


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open('rb') as f:
        while chunk := f.read(65536):
            h.update(chunk)
    return h.hexdigest()


def check_single_file_html(html_path: Path) -> None:
    text = html_path.read_text(encoding='utf-8')
    if '<script' not in text:
        raise ValueError('Built management.html does not contain inline scripts')
    for tag in re.finditer(r'<(?:script|link)([^>]*)>', text, re.IGNORECASE):
        attrs = tag.group(1)
        src_match = re.search(r'(?:src|href)=(["\'])(.*?)\1', attrs, re.IGNORECASE)
        if src_match:
            target = src_match.group(2)
            if target.startswith('data:') or target.startswith('blob:') or target.startswith('#'):
                continue
            if target.startswith('http:') or target.startswith('https:') or target.startswith('//') or target.endswith('.js') or target.endswith('.css'):
                raise ValueError(f'External script or style reference detected in management.html: {target}')


def build(
    upstream_tag: str,
    output_dir: Path,
    clone_url: str = ALLOWED_UPSTREAM_URL,
    patch_file: Optional[Path] = None,
    runner: Optional[Callable[[Path, list[str]], None]] = None,
    allow_custom_upstream: bool = False,
) -> dict:
    if not allow_custom_upstream and clone_url != ALLOWED_UPSTREAM_URL:
        raise ValueError(f'Disallowed upstream URL: {clone_url}')

    if patch_file is None:
        patch_file = Path(__file__).resolve().parent / 'patches' / '0001-plugin-management-bridge.patch'
    if not patch_file.exists():
        raise FileNotFoundError(f'Patch file not found: {patch_file}')


    patch_sha256 = sha256_file(patch_file)

    if runner is None:
        cmd_runner = default_cmd_runner
    else:
        cmd_runner = runner

    with tempfile.TemporaryDirectory() as temp_dir_str:
        work_dir = Path(temp_dir_str)
        repo_dir = work_dir / 'management-center'

        # 1. Clone upstream repository
        subprocess.run(
            ['git', 'clone', '--depth', '1', '--branch', upstream_tag, clone_url, str(repo_dir)],
            check=True,
            capture_output=True,
        )

        # 2. Resolves 40-character commit hash
        commit_res = subprocess.run(
            ['git', 'rev-parse', 'HEAD'],
            cwd=str(repo_dir),
            check=True,
            capture_output=True,
            text=True,
        )
        upstream_commit = commit_res.stdout.strip()
        if not COMMIT_PATTERN.match(upstream_commit):
            raise ValueError(f'Invalid upstream commit resolved: {upstream_commit}')


        # 3. Apply patch with git am --3way
        patch_abs = patch_file.resolve()
        subprocess.run(
            ['git', 'am', '--3way', str(patch_abs)],
            cwd=str(repo_dir),
            check=True,
            capture_output=True,
        )

        # 4. Run frozen Bun install and verify
        cmd_runner(repo_dir, ['bun', 'install', '--frozen-lockfile'])
        cmd_runner(repo_dir, ['bun', 'run', 'verify'])


        # 5. First build
        cmd_runner(repo_dir, ['bun', 'run', 'build'])
        first_dist_html = repo_dir / 'dist' / 'index.html'
        if not first_dist_html.exists():
            raise FileNotFoundError('dist/index.html missing after first build')
        first_hash = sha256_file(first_dist_html)
        check_single_file_html(first_dist_html)
        first_html_bytes = first_dist_html.read_bytes()

        # 6. Second build to verify deterministic reproducible output
        shutil.rmtree(repo_dir / 'dist', ignore_errors=True)
        cmd_runner(repo_dir, ['bun', 'run', 'build'])
        second_dist_html = repo_dir / 'dist' / 'index.html'
        if not second_dist_html.exists():
            raise FileNotFoundError('dist/index.html missing after second build')
        second_hash = sha256_file(second_dist_html)

        if first_hash != second_hash:
            raise ValueError(f'Non-reproducible build: first hash {first_hash} != second hash {second_hash}')


        # 7. Write outputs to output_dir
        output_dir.mkdir(parents=True, exist_ok=True)
        out_html = output_dir / 'management.html'
        out_sha = output_dir / 'management.html.sha256'
        out_manifest = output_dir / 'panel-manifest.json'


        out_html.write_bytes(first_html_bytes)
        out_sha.write_text(f'{first_hash}  management.html\n', encoding='utf-8')

        manifest_data = {
            'schema_version': 1,
            'schema_id': 'cliproxyapi-patched-management-release/v1',
            'upstream_repository': ALLOWED_UPSTREAM_URL,
            'upstream_tag': upstream_tag,
            'upstream_commit': upstream_commit,
            'patch_file': patch_file.name,
            'patch_sha256': patch_sha256,
            'asset': {
                'name': 'management.html',
                'size': len(first_html_bytes),
                'sha256': first_hash,
            },
        }

        out_manifest.write_text(json.dumps(manifest_data, indent=2, sort_keys=True) + '\n', encoding='utf-8')

        return {
            'upstream_tag': upstream_tag,
            'upstream_commit': upstream_commit,
            'patch_sha256': patch_sha256,
            'html_sha256': first_hash,
            'html_size': len(first_html_bytes),
            'manifest': manifest_data,
        }


def main() -> None:
    parser = argparse.ArgumentParser(description='Build patched official Management Center single-file HTML')
    parser.add_argument('--upstream-tag', required=True, help='Exact official upstream tag (e.g. v1.22.6)')
    parser.add_argument('--output', required=True, help='Output directory for release artifacts')
    args = parser.parse_args()


    out_path = Path(args.output)
    result = build(upstream_tag=args.upstream_tag, output_dir=out_path)
    print(f'Successfully built patched management panel for {result["upstream_tag"]} ({result["upstream_commit"][:12]})')
    print(f'HTML SHA-256: {result["html_sha256"]}')
    print(f'HTML Size: {result["html_size"]} bytes')


if __name__ == '__main__':
    main()
