#!/usr/bin/env python3
# ponytail: VPS approved release reconciler and updater
import argparse, hashlib, io, json, os, pathlib, re, shutil, stat, subprocess, sys, tarfile, tempfile, urllib.parse, urllib.request
from typing import Any, Callable, Dict, List, Optional, Tuple


HEX64 = re.compile(r"^[0-9a-f]{64}$")
COMMIT = re.compile(r"^[0-9a-f]{40}$")
TAG_PATTERN = re.compile(r"^[A-Za-z0-9._-]+$")
REPOSITORY_PATTERN = re.compile(r"^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$")
DEFAULT_APPROVED_REPOSITORY = "jinpeng2700-tech/cpa-plugin-token-saver"

SECRET_PATTERNS = [
    (re.compile(r"https?://([^:]+):([^@]+)@", re.IGNORECASE), r"https://\1:***@"),
    (re.compile(r"([?&](?:token|key|secret|password|auth|access_token|credential)=)[^&\s]+", re.IGNORECASE), r"\1***"),
    (re.compile(r"([\"\']?(?:client[-_ ]?secret|api[-_ ]?key|management[-_ ]?key|password|authorization|secret[-_ ]?key|bearer[-_ ]?token)[\"\']?\s*[:=]\s*[\"\']?(?:Bearer\s+)?)(?:[^\"\'\s,;}]+|[\"\'][^\"\']*[\"\'])", re.IGNORECASE), r"\1***"),
]

def sanitize_log(message: str) -> str:
    sanitized = str(message)
    for pattern, repl in SECRET_PATTERNS:
        sanitized = pattern.sub(repl, sanitized)
    return sanitized

def log(msg: str) -> None:
    print(f"[reconciler] {sanitize_log(msg)}", file=sys.stderr)

def read_json_url(url: str) -> Any:
    req = urllib.request.Request(url, headers={"User-Agent": "cliproxyapi-updater/1.0"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))

def discover_latest_approved_manifest(repository: str, json_loader: Callable[[str], Any] = read_json_url) -> Dict[str, Any]:
    if not REPOSITORY_PATTERN.fullmatch(repository):
        raise ValueError(f"Invalid approved repository: {repository}")
    releases = json_loader(f"https://api.github.com/repos/{repository}/releases?per_page=100")
    if not isinstance(releases, list):
        raise ValueError("GitHub releases response must be an array")
    candidates = []
    for release in releases:
        if not isinstance(release, dict) or release.get("draft") or release.get("prerelease"):
            continue
        if not str(release.get("tag_name", "")).startswith("approved-cli-"):
            continue
        for asset in release.get("assets", []):
            if isinstance(asset, dict) and asset.get("name") == "approved-release.json":
                manifest = json_loader(str(asset.get("browser_download_url", "")))
                generation = manifest.get("channel_generation") if isinstance(manifest, dict) else None
                if manifest.get("schema_version") == 2 and isinstance(generation, int) and not isinstance(generation, bool) and generation > 0:
                    candidates.append((generation, manifest))
                break
    if not candidates:
        raise ValueError("No valid approved schema v2 release found")
    candidates.sort(key=lambda item: item[0], reverse=True)
    if len(candidates) > 1 and candidates[0][0] == candidates[1][0] and candidates[0][1].get("fingerprint") != candidates[1][1].get("fingerprint"):
        raise ValueError(f"Conflicting approved releases at channel_generation {candidates[0][0]}")
    return candidates[0][1]

def sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        while chunk_data := f.read(65536):
            h.update(chunk_data)
    return h.hexdigest()

def default_downloader(url: str, target_path: pathlib.Path, expected_sha256: str, expected_size: Optional[int] = None) -> None:
    sanitized_url = sanitize_log(url)
    log(f"Downloading {sanitized_url}")
    target_path.parent.mkdir(parents=True, exist_ok=True)
    temp_file = target_path.parent / f".tmp.{target_path.name}.{os.getpid()}"
    h = hashlib.sha256()
    size = 0
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "cliproxyapi-updater/1.0"})
        with urllib.request.urlopen(req, timeout=60) as resp, temp_file.open("wb") as out:
            while chunk_bytes := resp.read(65536):
                size += len(chunk_bytes)
                h.update(chunk_bytes)
                out.write(chunk_bytes)
        actual_sha = h.hexdigest()
        if actual_sha != expected_sha256:
            raise ValueError(f"Checksum mismatch for {sanitized_url}: got {actual_sha}, want {expected_sha256}")
        if expected_size is not None and size != expected_size:
            raise ValueError(f"Size mismatch for {sanitized_url}: got {size}, want {expected_size}")
        temp_file.replace(target_path)
    finally:
        if temp_file.exists():
            try:
                temp_file.unlink()
            except OSError:
                pass

def default_service_runner(action: str, deployment_path: pathlib.Path) -> bool:
    service_name = os.environ.get("CLIPROXYAPI_SERVICE_NAME", "cliproxyapi.service")
    if action == "restart":
        log(f"Restarting service {service_name}")
        subprocess.run(["systemctl", "--user", "daemon-reload"], check=False)
        res = subprocess.run(["systemctl", "--user", "restart", service_name], capture_output=True)
        return res.returncode == 0
    elif action == "smoke":
        log("Running service smoke check")
        import time
        for _ in range(15):
            try:
                req = urllib.request.Request("http://127.0.0.1:8317/v0/resource/plugins/token-saver/headroom/status")
                with urllib.request.urlopen(req, timeout=2) as resp:
                    if resp.status == 200:
                        return True
            except Exception:
                time.sleep(1)
        return False
    return True


try:
    import fcntl
except ImportError:
    fcntl = None

class FileLock:
    def __init__(self, lock_path: pathlib.Path):
        self.lock_path = lock_path
        self.fd = None

    def __enter__(self):
        if fcntl is None:
            if sys.platform != "win32":
                raise RuntimeError("fcntl module required on POSIX systems")
            # Fail-closed if fcntl unavailable
            raise RuntimeError("fcntl_unavailable_cannot_acquire_lock")
        self.lock_path.parent.mkdir(parents=True, exist_ok=True)
        self.fd = open(self.lock_path, "w")
        try:
            fcntl.flock(self.fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except (BlockingIOError, OSError) as e:
            self.fd.close()
            self.fd = None
            log("Another reconcile process is currently running; exiting safely without mutating active deployment")
            raise BlockingIOError("reconcile_already_running") from e
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        if self.fd is not None:
            try:
                if fcntl is not None:
                    fcntl.flock(self.fd.fileno(), fcntl.LOCK_UN)
            except OSError:
                pass
            try:
                self.fd.close()
            except OSError:
                pass
            self.fd = None


class Reconciler:
    def __init__(
        self,
        deploy_root: pathlib.Path,
        active_link: pathlib.Path,
        prev_link: pathlib.Path,
        state_dir: pathlib.Path,
        downloader: Optional[Callable[[str, pathlib.Path, str, Optional[int]], None]] = None,
        service_runner: Optional[Callable[[str, pathlib.Path], bool]] = None,
        keep_deployments: int = 5,
        lock_path: Optional[pathlib.Path] = None,
        lock_provider: Optional[Callable[[pathlib.Path], Any]] = None,
    ):
        self.deploy_root = deploy_root.resolve()
        self.active_link = active_link
        self.prev_link = prev_link
        self.state_dir = state_dir.resolve()
        self.downloader = downloader or default_downloader
        self.service_runner = service_runner or default_service_runner
        self.keep_deployments = keep_deployments
        self.lock_path = lock_path or (self.state_dir / "reconciler.lock")
        self.lock_provider = lock_provider or FileLock

    def inspect_current_deployment(self) -> Optional[Dict[str, Any]]:
        if not self.active_link.is_symlink() and not self.active_link.exists():
            return None
        target = self.active_link.resolve()
        if not target.exists() or not target.is_dir():
            return None
        for name in ("approved-manifest.json", "approved-release.json"):
            manifest_file = target / name
            if manifest_file.is_file():
                try:
                    return json.loads(manifest_file.read_text(encoding="utf-8"))
                except Exception:
                    return None
        return None

    def validate_manifest(self, manifest: Dict[str, Any]) -> None:
        schema_version = manifest.get("schema_version")
        if schema_version != 2:
            raise ValueError(f"Unsupported target manifest schema_version: {schema_version}; target requires schema v2")
        if manifest.get("verifier_schema") != 1:
            raise ValueError("Unsupported verifier_schema")
        generation = manifest.get("channel_generation")
        if not isinstance(generation, int) or isinstance(generation, bool) or generation < 1:
            raise ValueError("Invalid channel_generation")

        panel = manifest.get("panel")
        if not isinstance(panel, dict):
            raise ValueError("Target manifest schema v2 requires panel object")

        panel_tag = str(panel.get("tag", ""))
        if not panel_tag or not TAG_PATTERN.match(panel_tag) or ".." in panel_tag or "/" in panel_tag or "\\" in panel_tag:
            raise ValueError(f"Invalid panel tag: {panel_tag}")

        official = manifest.get("official")
        if not isinstance(official, dict):
            raise ValueError("Target manifest requires official object")
        cli_tag = str(official.get("tag", ""))
        if not cli_tag or not TAG_PATTERN.match(cli_tag) or ".." in cli_tag or "/" in cli_tag or "\\" in cli_tag:
            raise ValueError(f"Invalid official tag: {cli_tag}")

        plugin = manifest.get("plugin")
        if not isinstance(plugin, dict):
            raise ValueError("Target manifest requires plugin object")
        plugin_tag = str(plugin.get("tag", ""))
        if not plugin_tag or not TAG_PATTERN.match(plugin_tag) or ".." in plugin_tag or "/" in plugin_tag or "\\" in plugin_tag:
            raise ValueError(f"Invalid plugin tag: {plugin_tag}")

    def unpack_cli_archive(self, archive_path: pathlib.Path, target_bin: pathlib.Path, expected_binary_sha256: str) -> None:
        with tempfile.TemporaryDirectory() as extract_dir_str:
            extract_dir = pathlib.Path(extract_dir_str)
            with tarfile.open(archive_path, "r:*") as tar:
                for member in tar.getmembers():
                    if member.name.startswith("/") or ".." in pathlib.PurePosixPath(member.name).parts:
                        raise ValueError(f"Unsafe path in tar archive: {member.name}")
                    if not (member.isfile() or member.isdir()):
                        raise ValueError(f"Unsafe entry type in tar archive: {member.name}")
                tar.extractall(extract_dir)

            candidates = list(extract_dir.rglob("cli-proxy-api"))
            if not candidates:
                candidates = [p for p in extract_dir.rglob("*") if p.is_file() and p.name == "cli-proxy-api"]
            if len(candidates) != 1:
                raise ValueError(f"Expected exactly 1 cli-proxy-api binary in archive, found {len(candidates)}")
            found_binary = candidates[0]
            bin_sha = sha256_file(found_binary)
            if bin_sha != expected_binary_sha256:
                raise ValueError(f"CLI binary sha256 mismatch: got {bin_sha}, want {expected_binary_sha256}")

            target_bin.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(found_binary, target_bin)
            try:
                os.chmod(target_bin, 0o755)
            except OSError:
                pass

    def atomic_symlink(self, target: pathlib.Path, link: pathlib.Path) -> None:
        link_dir = link.parent
        temp_link = link_dir / f".tmp-link.{link.name}.{os.getpid()}"
        if temp_link.is_symlink() or temp_link.exists():
            try:
                temp_link.unlink()
            except OSError:
                pass
        temp_link.symlink_to(target)
        try:
            temp_link.replace(link)
        except OSError:
            if link.is_symlink() or link.exists():
                try:
                    link.unlink()
                except OSError:
                    pass
            temp_link.replace(link)

    def reconcile(self, manifest: Dict[str, Any]) -> Dict[str, Any]:
        with self.lock_provider(self.lock_path):
            return self._reconcile_locked(manifest)

    def _reconcile_locked(self, manifest: Dict[str, Any]) -> Dict[str, Any]:
        self.validate_manifest(manifest)

        current_manifest = self.inspect_current_deployment()
        current_fingerprint = current_manifest.get("fingerprint") if current_manifest else None
        target_fingerprint = manifest.get("fingerprint")

        if current_fingerprint and current_fingerprint == target_fingerprint:
            log(f"Deployment is already up-to-date with fingerprint {target_fingerprint}")
            return {"success": True, "action": "noop", "fingerprint": target_fingerprint}
        if current_manifest:
            current_generation = current_manifest.get("channel_generation")
            target_generation = manifest.get("channel_generation")
            if isinstance(current_generation, int) and not isinstance(current_generation, bool):
                if target_generation < current_generation:
                    raise ValueError(f"Refusing channel generation downgrade: current {current_generation}, target {target_generation}")
                if target_generation == current_generation:
                    raise ValueError(f"Conflicting fingerprints at channel_generation {target_generation}")

        official = manifest["official"]
        plugin = manifest["plugin"]
        panel = manifest["panel"]

        cli_ver = official.get("version", official.get("tag"))
        plugin_ver = plugin.get("version", plugin.get("tag"))
        panel_tag = panel.get("tag")

        deployment_id = f"dep-cli-{cli_ver}-plugin-{plugin_ver}-{panel_tag}-g{manifest.get('channel_generation', 0)}"
        staged_dir = self.deploy_root / deployment_id

        temp_stage = self.deploy_root / f".tmp-stage.{deployment_id}.{os.getpid()}"
        if temp_stage.exists():
            shutil.rmtree(temp_stage)
        temp_stage.mkdir(parents=True, mode=0o700)

        initial_active_target = self.active_link.resolve() if (self.active_link.is_symlink() or self.active_link.exists()) else None

        try:
            # 1. Download official CLI archive
            cli_asset = official["asset"]
            cli_url = f"https://github.com/{official['repository']}/releases/download/{official['tag']}/{cli_asset['name']}"
            cli_tar_path = temp_stage / "downloads" / cli_asset["name"]
            self.downloader(cli_url, cli_tar_path, cli_asset["sha256"], cli_asset.get("size"))

            # Unpack CLI
            target_cli = temp_stage / "cli-proxy-api"
            self.unpack_cli_archive(cli_tar_path, target_cli, official["binary_sha256"])

            # 2. Download Token Saver plugin
            plugin_asset = plugin["asset"]
            plugin_url = f"https://github.com/{plugin['repository']}/releases/download/{plugin['tag']}/{plugin_asset['name']}"
            target_plugin = temp_stage / "plugins" / "linux" / "amd64" / "token-saver.so"
            self.downloader(plugin_url, target_plugin, plugin_asset["sha256"], plugin_asset.get("size"))
            if os.name != "nt":
                os.chmod(target_plugin, 0o755)

            # 3. Download compat-probe
            if "probe_asset" in plugin:
                probe_asset = plugin["probe_asset"]
                probe_url = f"https://github.com/{plugin['repository']}/releases/download/{plugin['tag']}/{probe_asset['name']}"
                target_probe = temp_stage / "compat-probe"
                self.downloader(probe_url, target_probe, probe_asset["sha256"], probe_asset.get("size"))
                if os.name != "nt":
                    os.chmod(target_probe, 0o755)

            # 4. Download Panel management.html
            panel_asset = panel["asset"]
            panel_url = f"https://github.com/{panel['repository']}/releases/download/{panel['tag']}/{panel_asset['name']}"
            target_panel = temp_stage / "static" / "management.html"
            self.downloader(panel_url, target_panel, panel_asset["sha256"], panel_asset.get("size"))
            if os.name != "nt":
                os.chmod(target_panel, 0o600)

            # 5. Download Panel Manifest
            panel_manifest = panel["manifest"]
            panel_manifest_url = f"https://github.com/{panel['repository']}/releases/download/{panel['tag']}/{panel_manifest['name']}"
            target_panel_manifest = temp_stage / "panel-manifest.json"
            self.downloader(panel_manifest_url, target_panel_manifest, panel_manifest["sha256"], panel_manifest.get("size"))
            if os.name != "nt":
                os.chmod(target_panel_manifest, 0o600)

            # Clean download cache inside temp_stage
            shutil.rmtree(temp_stage / "downloads", ignore_errors=True)

            # Write manifest & version
            (temp_stage / "approved-manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            (temp_stage / "version.txt").write_text(f"{official.get('version', official.get('tag'))}\n", encoding="utf-8")
            (temp_stage / "state").symlink_to(self.state_dir, target_is_directory=True)

            # Move temp_stage to final staged_dir
            if staged_dir.exists():
                shutil.rmtree(staged_dir)
            temp_stage.replace(staged_dir)

            # Switch symlinks: active -> staged_dir, prev -> initial_active_target
            if initial_active_target and initial_active_target.exists():
                self.atomic_symlink(initial_active_target, self.prev_link)

            self.atomic_symlink(staged_dir, self.active_link)

            # Service restart & smoke check
            self.service_runner("restart", staged_dir)
            smoke_ok = self.service_runner("smoke", staged_dir)

            if not smoke_ok:
                log("Service smoke check failed! Rolling back symlink to previous deployment...")
                if initial_active_target and initial_active_target.exists():
                    self.atomic_symlink(initial_active_target, self.active_link)
                    self.service_runner("restart", initial_active_target)
                raise RuntimeError(f"Service smoke check failed after activating {deployment_id}")

            self.cleanup_old_deployments(active=staged_dir, previous=initial_active_target)
            return {"success": True, "action": "deployed", "deployment_id": deployment_id, "fingerprint": target_fingerprint}

        finally:
            if temp_stage.exists():
                shutil.rmtree(temp_stage, ignore_errors=True)

    def cleanup_old_deployments(self, active: pathlib.Path, previous: Optional[pathlib.Path]) -> None:
        protected = {active.resolve()}
        if previous and previous.exists():
            protected.add(previous.resolve())

        all_deps = [p for p in self.deploy_root.iterdir() if p.is_dir() and not p.name.startswith(".")]
        all_deps.sort(key=lambda p: p.stat().st_mtime, reverse=True)

        retained = 0
        for dep in all_deps:
            if dep.resolve() in protected:
                continue
            retained += 1
            if retained > self.keep_deployments:
                try:
                    shutil.rmtree(dep)
                except OSError:
                    pass

def main() -> None:
    parser = argparse.ArgumentParser(description="Reconcile CLIProxyAPI approved release")
    parser.add_argument("--manifest", help="Path or URL to approved-release.json; omitted discovers highest approved generation", default=os.environ.get("APPROVED_MANIFEST", ""))
    parser.add_argument("--repository", default=os.environ.get("APPROVED_REPOSITORY", DEFAULT_APPROVED_REPOSITORY))
    parser.add_argument("--deploy-root", default=os.environ.get("DEPLOY_ROOT", "/root/cliproxyapi.deployments"))
    parser.add_argument("--active-link", default=os.environ.get("ACTIVE_LINK", "/root/cliproxyapi"))
    parser.add_argument("--prev-link", default=os.environ.get("PREV_LINK", "/root/cliproxyapi.prev"))
    parser.add_argument("--state-dir", default=os.environ.get("STATE_DIR", "/root/cliproxyapi/state"))
    args = parser.parse_args()

    manifest_source = args.manifest
    if not manifest_source:
        manifest_data = discover_latest_approved_manifest(args.repository)
    elif manifest_source.startswith("http://") or manifest_source.startswith("https://"):
        manifest_data = read_json_url(manifest_source)
    else:
        manifest_data = json.loads(pathlib.Path(manifest_source).read_text(encoding="utf-8"))

    reconciler = Reconciler(
        deploy_root=pathlib.Path(args.deploy_root),
        active_link=pathlib.Path(args.active_link),
        prev_link=pathlib.Path(args.prev_link),
        state_dir=pathlib.Path(args.state_dir),
    )
    result = reconciler.reconcile(manifest_data)
    print(json.dumps(result, indent=2))

if __name__ == "__main__":
    main()
