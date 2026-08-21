import hashlib
import importlib.util
import io
import json
import os
import pathlib
import stat
import sys
import tarfile
import tempfile
import unittest
from typing import Callable, Optional

FILE_DIR = pathlib.Path(__file__).resolve().parent
ROOT = FILE_DIR.parents[1]
RECONCILER_PATH = ROOT / 'deploy' / 'vps-reconciler.py'


def load_reconciler():
    if not RECONCILER_PATH.is_file():
        raise FileNotFoundError(f'Reconciler script missing: {RECONCILER_PATH}')
    spec = importlib.util.spec_from_file_location('vps_reconciler', RECONCILER_PATH)
    if spec is None or spec.loader is None:
        raise ImportError(f'Cannot load spec from {RECONCILER_PATH}')
    mod = importlib.util.module_from_spec(spec)
    sys.modules['vps_reconciler'] = mod
    spec.loader.exec_module(mod)
    return mod

def make_tar_gz(files: dict[str, bytes]) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w:gz') as tar:
        for name, data in files.items():
            info = tarfile.TarInfo(name=name)
            info.size = len(data)
            info.mode = 0o755
            info.mtime = 1700000000
            tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()

class MockDownloader:
    def __init__(self, responses: dict[str, bytes]):
        self.responses = responses
        self.download_log: list[str] = []

    def __call__(self, url: str, target_path: pathlib.Path, expected_sha256: str, expected_size: Optional[int] = None) -> None:
        self.download_log.append(url)
        if url not in self.responses:
            raise RuntimeError(f'Mock 404 for URL: {url}')
        content = self.responses[url]
        actual_sha = hashlib.sha256(content).hexdigest()
        if actual_sha != expected_sha256:
            raise ValueError(f'Download SHA-256 mismatch for {url}: got {actual_sha}, want {expected_sha256}')
        if expected_size is not None and len(content) != expected_size:
            raise ValueError(f'Download size mismatch for {url}: got {len(content)}, want {expected_size}')
        target_path.parent.mkdir(parents=True, exist_ok=True)
        target_path.write_bytes(content)

class MockServiceRunner:
    def __init__(self, smoke_succeeds: bool = True):
        self.smoke_succeeds = smoke_succeeds
        self.actions: list[tuple[str, str]] = []

    def __call__(self, action: str, deployment_path: pathlib.Path) -> bool:
        self.actions.append((action, str(deployment_path)))
        if action == 'smoke':
            return self.smoke_succeeds
        return True

class MockLockProvider:
    def __init__(self):
        self.locked_paths = set()
    def __call__(self, lock_path: pathlib.Path):
        return MockLock(str(lock_path.resolve()), self.locked_paths)

class MockLock:
    def __init__(self, key: str, locked_set: set):
        self.key = key
        self.locked_set = locked_set
        self.acquired = False
    def __enter__(self):
        if self.key in self.locked_set:
            raise BlockingIOError('reconcile_already_running')
        self.locked_set.add(self.key)
        self.acquired = True
        return self
    def __exit__(self, exc_type, exc_val, exc_tb):
        if self.acquired:
            self.locked_set.discard(self.key)
            self.acquired = False

class TestVPSReconciler(unittest.TestCase):
    def setUp(self):
        self.reconciler = load_reconciler()
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp_dir.name)
        self.deploy_root = self.root / "deployments"
        self.deploy_root.mkdir(parents=True, mode=0o700)
        self.active_link = self.root / "cliproxyapi"
        self.prev_link = self.root / "cliproxyapi.prev"
        self.state_dir = self.root / "state"
        self.state_dir.mkdir(parents=True, mode=0o700)
        (self.state_dir / "config.yaml").write_text("host: 127.0.0.1\n")
        (self.state_dir / "auth").mkdir(parents=True, mode=0o700)
        (self.state_dir / "auth" / "session.json").write_text("{\"token\":\"secret\"}\n")
        (self.state_dir / "logs").mkdir(parents=True, mode=0o700)
        (self.state_dir / "logs" / "cliproxyapi.log").write_text("initial log\n")
        self.cli_binary_bytes = b"\x7fELF\x02\x01\x01\x00cli-proxy-api-binary"
        self.cli_binary_sha256 = hashlib.sha256(self.cli_binary_bytes).hexdigest()
        self.tar_gz_bytes = make_tar_gz({"cli-proxy-api": self.cli_binary_bytes})
        self.tar_gz_sha256 = hashlib.sha256(self.tar_gz_bytes).hexdigest()
        self.plugin_bytes = b"\x7fELF\x02\x01\x01\x00token-saver-plugin"
        self.plugin_sha256 = hashlib.sha256(self.plugin_bytes).hexdigest()
        self.probe_bytes = b"\x7fELF\x02\x01\x01\x00compat-probe"
        self.probe_sha256 = hashlib.sha256(self.probe_bytes).hexdigest()
        self.panel_bytes = b"<!DOCTYPE html><html><head><title>Management Center</title></head><body>Single file</body></html>"
        self.panel_sha256 = hashlib.sha256(self.panel_bytes).hexdigest()
        self.panel_manifest_bytes = json.dumps({"schema_version": 1, "schema_id": "cliproxyapi-patched-management-release/v1", "upstream_repository": "https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git", "upstream_tag": "v1.22.6", "upstream_commit": "1234567890123456789012345678901234567890", "patch_file": "0001-plugin-management-bridge.patch", "patch_sha256": "a" * 64, "asset": {"name": "management.html", "size": len(self.panel_bytes), "sha256": self.panel_sha256}}).encode("utf-8")
        self.panel_manifest_sha256 = hashlib.sha256(self.panel_manifest_bytes).hexdigest()
        self.manifest_v2_data = {"schema_version": 2, "verifier_schema": 1, "channel": "stable", "channel_generation": 4, "prior_fingerprint": "sha256:" + "0" * 64, "fingerprint": "sha256:" + "f" * 64, "official": {"repository": "router-for-me/CLIProxyAPI", "release_id": 101, "tag": "v7.2.137", "version": "7.2.137", "asset": {"name": "CLIProxyAPI_7.2.137_linux_amd64.tar.gz", "id": 201, "size": len(self.tar_gz_bytes), "sha256": self.tar_gz_sha256}, "checksums": {"name": "checksums.txt", "id": 202, "size": 100, "sha256": "c" * 64}, "binary_sha256": self.cli_binary_sha256, "provenance": "official-checksum-only"}, "plugin": {"repository": "jinpeng2700-tech/cpa-plugin-token-saver", "release_id": 301, "tag": "v1.1.0", "version": "1.1.0", "source_commit": "e" * 40, "asset": {"name": "token-saver-v1.1.0-linux-amd64.so", "id": 401, "size": len(self.plugin_bytes), "sha256": self.plugin_sha256}, "probe_asset": {"name": "compat-probe-v1.1.0-linux-amd64", "id": 402, "size": len(self.probe_bytes), "sha256": self.probe_sha256}, "attestation": {"repository": "jinpeng2700-tech/cpa-plugin-token-saver", "workflow": ".github/workflows/release.yml", "ref": "refs/tags/v1.1.0", "source_commit": "e" * 40, "issuer": "https://token.actions.githubusercontent.com"}}, "panel": {"repository": "jinpeng2700-tech/cpa-plugin-token-saver", "release_id": 501, "tag": "panel-v1.22.6-bridge.1", "upstream_tag": "v1.22.6", "upstream_commit": "1234567890123456789012345678901234567890", "patch_sha256": "a" * 64, "asset": {"name": "management.html", "id": 601, "size": len(self.panel_bytes), "sha256": self.panel_sha256}, "manifest": {"name": "panel-manifest.json", "id": 602, "size": len(self.panel_manifest_bytes), "sha256": self.panel_manifest_sha256}, "attestation": {"repository": "jinpeng2700-tech/cpa-plugin-token-saver", "workflow": ".github/workflows/release-panel.yml", "ref": "refs/tags/panel-v1.22.6-bridge.1", "source_commit": "d" * 40, "issuer": "https://token.actions.githubusercontent.com"}}, "compatibility": {"schema_version": 1, "plugin": True, "core_only": True, "config_generation": 4, "config_digest": "b" * 64, "scenarios": ["all-off", "rtk", "headroom-rewrite", "headroom-timeout", "caveman", "ponytail", "fixed-order"]}, "approved_attestation": {"repository": "jinpeng2700-tech/cpa-plugin-token-saver", "workflow": ".github/workflows/promote-cliproxyapi.yml", "ref": "refs/heads/main", "source_commit": "c" * 40, "issuer": "https://token.actions.githubusercontent.com"}}
    def tearDown(self):
        self.temp_dir.cleanup()
    def create_initial_deployment(self, name="initial-dep", schema_version=2):
        dep_dir = self.deploy_root / name
        dep_dir.mkdir(parents=True, mode=0o700)
        (dep_dir / "cli-proxy-api").write_bytes(self.cli_binary_bytes)
        (dep_dir / "plugins" / "linux" / "amd64").mkdir(parents=True, mode=0o700)
        (dep_dir / "plugins" / "linux" / "amd64" / "token-saver.so").write_bytes(self.plugin_bytes)
        (dep_dir / "management.html").write_bytes(self.panel_bytes)
        (dep_dir / "version.txt").write_text("7.2.136\n")
        manifest = dict(self.manifest_v2_data)
        manifest["fingerprint"] = "sha256:" + "0" * 64
        if schema_version == 1:
            manifest["schema_version"] = 1
            manifest.pop("panel", None)
        (dep_dir / "approved-manifest.json").write_text(json.dumps(manifest, indent=2))
        if self.active_link.is_symlink() or self.active_link.exists():
            self.active_link.unlink()
        self.active_link.symlink_to(dep_dir)
        return dep_dir
    def test_schema_v2_exact_download_and_stage(self):
        self.create_initial_deployment("dep-initial")
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=True)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        result = reconciler.reconcile(self.manifest_v2_data)
        self.assertTrue(result["success"])
        self.assertIn("panel-v1.22.6-bridge.1", result["deployment_id"])
        new_active = self.active_link.resolve()
        self.assertTrue(new_active.exists())
        self.assertIn("panel-v1.22.6-bridge.1", new_active.name)
        panel_file = new_active / "static" / "management.html"
        self.assertTrue(panel_file.is_file())
        self.assertEqual(panel_file.read_bytes(), self.panel_bytes)
        if os.name != "nt":
            self.assertEqual(stat.S_IMODE(panel_file.stat().st_mode), 0o600)
        cli_file = new_active / "cli-proxy-api"
        self.assertTrue(cli_file.is_file())
        self.assertEqual(cli_file.read_bytes(), self.cli_binary_bytes)
        prev_target = self.prev_link.resolve()
        self.assertEqual(prev_target.name, "dep-initial")
    def test_panel_sha_mismatch_leaves_active_unchanged(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": b"TAMPERED PANEL CONTENT",
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=True)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(Exception):
            reconciler.reconcile(self.manifest_v2_data)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
    def test_missing_panel_in_schema_v2_rejected(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        bad_manifest = dict(self.manifest_v2_data)
        bad_manifest.pop("panel", None)
        downloader = MockDownloader({})
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(ValueError):
            reconciler.reconcile(bad_manifest)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
    def test_failed_service_smoke_restores_previous_symlink(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=False)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(Exception):
            reconciler.reconcile(self.manifest_v2_data)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
        self.assertIn(("restart", str(initial_dir.resolve())), [(act, str(pathlib.Path(p).resolve())) for act, p in service_runner.actions])
    def test_existing_schema_v1_deployment_recognized_during_transition(self):
        v1_dir = self.create_initial_deployment("dep-v1", schema_version=1)
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=True)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        current = reconciler.inspect_current_deployment()
        self.assertIsNotNone(current)
        self.assertEqual(current.get("schema_version"), 1)
        result = reconciler.reconcile(self.manifest_v2_data)
        self.assertTrue(result["success"])
        self.assertEqual(self.prev_link.resolve(), v1_dir.resolve())
    def test_new_schema_v1_target_rejected_as_target(self):
        self.create_initial_deployment("dep-initial")
        manifest_v1 = dict(self.manifest_v2_data)
        manifest_v1["schema_version"] = 1
        manifest_v1.pop("panel", None)
        downloader = MockDownloader({})
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(ValueError):
            reconciler.reconcile(manifest_v1)
    def test_state_directory_is_linked_without_copying(self):
        self.create_initial_deployment("dep-initial")
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=True)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        reconciler.reconcile(self.manifest_v2_data)
        self.assertTrue((self.state_dir / "config.yaml").is_file())
        self.assertEqual((self.state_dir / "config.yaml").read_text(), "host: 127.0.0.1\n")
        self.assertTrue((self.state_dir / "auth" / "session.json").is_file())
        self.assertTrue((self.state_dir / "logs" / "cliproxyapi.log").is_file())
        new_active = self.active_link.resolve()
        state_link = new_active / "state"
        self.assertTrue(state_link.is_symlink())
        self.assertEqual(state_link.resolve(), self.state_dir.resolve())
        self.assertFalse((new_active / "auth").exists())
        self.assertFalse((new_active / "logs").exists())
    def test_active_and_previous_deployments_never_deleted(self):
        dep0 = self.create_initial_deployment("dep-0")
        dep1 = self.create_initial_deployment("dep-1")
        self.prev_link.symlink_to(dep0)
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner(smoke_succeeds=True)
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
            keep_deployments=1,
        )
        reconciler.reconcile(self.manifest_v2_data)
        new_active = self.active_link.resolve()
        new_prev = self.prev_link.resolve()
        self.assertTrue(new_active.exists())
        self.assertTrue(new_prev.exists())
        self.assertEqual(new_prev.name, "dep-1")
    def test_logs_omit_url_credentials_query_tokens_and_config_secrets(self):
        log_messages = [
            "Connecting to https://user:secretpassword123@api.github.com/repos/owner/repo/releases",
            "Fetched manifest from https://raw.githubusercontent.com/channel.json?token=mysecrettoken&key=secretkey456",
            "Config content: client_secret: " + chr(34) + "supersecretvalue" + chr(34) + " and management_key: " + chr(34) + "admin-key-789" + chr(34),
            "Secret JSON: {" + chr(34) + "api_key" + chr(34) + ": " + chr(34) + "my-live-api-key" + chr(34) + ", " + chr(34) + "authorization" + chr(34) + ": " + chr(34) + "Bearer token_secret" + chr(34) + "}",
        ]
        sanitized = [self.reconciler.sanitize_log(msg) for msg in log_messages]
        for s in sanitized:
            self.assertNotIn("secretpassword123", s)
            self.assertNotIn("mysecrettoken", s)
            self.assertNotIn("secretkey456", s)
            self.assertNotIn("supersecretvalue", s)
            self.assertNotIn("admin-key-789", s)
            self.assertNotIn("my-live-api-key", s)
            self.assertNotIn("token_secret", s)
    def test_path_traversal_in_tar_archive_rejected(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        bad_tar = make_tar_gz({
            "../../etc/cron.d/bad": b"malicious",
            "cli-proxy-api": self.cli_binary_bytes,
        })
        bad_tar_sha = hashlib.sha256(bad_tar).hexdigest()
        manifest = dict(self.manifest_v2_data)
        manifest["fingerprint"] = "sha256:" + "8" * 64
        manifest["official"]["asset"]["sha256"] = bad_tar_sha
        manifest["official"]["asset"]["size"] = len(bad_tar)
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": bad_tar,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": self.plugin_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(ValueError):
            reconciler.reconcile(manifest)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
    def test_invalid_or_malicious_tags_rejected(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        manifest = dict(self.manifest_v2_data)
        manifest["fingerprint"] = "sha256:" + "0" * 64
        manifest["panel"]["tag"] = "panel-v1.22.6-bridge.1/../../escape"
        downloader = MockDownloader({})
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(ValueError):
            reconciler.reconcile(manifest)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
    def test_interrupted_or_truncated_download_rejected(self):
        initial_dir = self.create_initial_deployment("dep-initial")
        truncated_plugin = self.plugin_bytes[:10]
        urls = {
            "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz": self.tar_gz_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so": truncated_plugin,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64": self.probe_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html": self.panel_bytes,
            "https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json": self.panel_manifest_bytes,
        }
        downloader = MockDownloader(urls)
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        with self.assertRaises(ValueError):
            reconciler.reconcile(self.manifest_v2_data)
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
    def test_already_latest_fingerprint_is_noop(self):
        initial_dir = self.create_initial_deployment("dep-current-f")
        manifest_file = initial_dir / "approved-manifest.json"
        manifest_data = json.loads(manifest_file.read_text())
        manifest_data["fingerprint"] = self.manifest_v2_data["fingerprint"]
        manifest_file.write_text(json.dumps(manifest_data))
        downloader = MockDownloader({})
        service_runner = MockServiceRunner()
        reconciler = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=downloader,
            service_runner=service_runner,
            lock_provider=MockLockProvider(),
        )
        result = reconciler.reconcile(self.manifest_v2_data)
        self.assertTrue(result["success"])
        self.assertEqual(result.get("action"), "noop")
        self.assertEqual(self.active_link.resolve(), initial_dir.resolve())
        self.assertEqual(len(downloader.download_log), 0)


    def test_concurrency_lock_blocks_concurrent_instance_and_releases(self):
        initial_dir = self.create_initial_deployment('dep-initial')
        urls = {
            'https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.2.137/CLIProxyAPI_7.2.137_linux_amd64.tar.gz': self.tar_gz_bytes,
            'https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/token-saver-v1.1.0-linux-amd64.so': self.plugin_bytes,
            'https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/v1.1.0/compat-probe-v1.1.0-linux-amd64': self.probe_bytes,
            'https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/management.html': self.panel_bytes,
            'https://github.com/jinpeng2700-tech/cpa-plugin-token-saver/releases/download/panel-v1.22.6-bridge.1/panel-manifest.json': self.panel_manifest_bytes,
        }
        lock_provider = MockLockProvider()
        lock_path = self.state_dir / 'reconciler.lock'

        reconciler2 = None
        rec2_attempted = False
        rec2_blocked = False

        def concurrent_downloader(url, target_path, expected_sha256, expected_size=None):
            nonlocal rec2_attempted, rec2_blocked
            rec2_attempted = True
            try:
                reconciler2.reconcile(self.manifest_v2_data)
            except BlockingIOError:
                rec2_blocked = True
            MockDownloader(urls)(url, target_path, expected_sha256, expected_size)

        reconciler1 = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=concurrent_downloader,
            service_runner=MockServiceRunner(smoke_succeeds=True),
            lock_path=lock_path,
            lock_provider=lock_provider,
        )

        reconciler2 = self.reconciler.Reconciler(
            deploy_root=self.deploy_root,
            active_link=self.active_link,
            prev_link=self.prev_link,
            state_dir=self.state_dir,
            downloader=MockDownloader(urls),
            service_runner=MockServiceRunner(smoke_succeeds=True),
            lock_path=lock_path,
            lock_provider=lock_provider,
        )

        res1 = reconciler1.reconcile(self.manifest_v2_data)
        self.assertTrue(res1['success'])
        self.assertTrue(rec2_attempted)
        self.assertTrue(rec2_blocked)

        res2 = reconciler2.reconcile(self.manifest_v2_data)
        self.assertTrue(res2['success'])
        self.assertEqual(res2.get('action'), 'noop')

    def test_default_file_lock_fails_closed_on_missing_fcntl(self):
        lock_path = self.state_dir / 'reconciler.lock'
        file_lock = self.reconciler.FileLock(lock_path)
        if 'fcntl' not in sys.modules and sys.platform == 'win32':
            with self.assertRaises((RuntimeError, OSError)):
                with file_lock:
                    pass

if __name__ == "__main__":
    unittest.main()
