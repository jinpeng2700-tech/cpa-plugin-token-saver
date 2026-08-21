import hashlib
import importlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

# ponytail: minimal fixture runner for unit testing build_panel logic


class TestBuildPanel(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.repo_dir = self.root / "official-repo"
        self.patch_dir = self.root / "patches"
        self.patch_dir.mkdir(parents=True, exist_ok=True)
        self.output_dir = self.root / "dist-panel"
        self.schema_path = Path(__file__).resolve().parent.parent / "panel-release.schema.json"

        self.repo_dir.mkdir(parents=True, exist_ok=True)
        self._git(self.repo_dir, "init")
        self._git(self.repo_dir, "config", "user.name", "Test Runner")
        self._git(self.repo_dir, "config", "user.email", "runner@example.com")
        self._git(self.repo_dir, "config", "commit.gpgsign", "false")

        (self.repo_dir / "index.html").write_text("<!DOCTYPE html><html><body>Base</body></html>\n", encoding="utf-8")
        (self.repo_dir / "package.json").write_text('{"name":"management-center","version":"1.22.6"}\n', encoding="utf-8")
        self._git(self.repo_dir, "add", ".")
        self._git(self.repo_dir, "commit", "-m", "Initial commit")
        self._git(self.repo_dir, "tag", "v1.22.6")

        self.commit_v1_22_6 = self._git_out(self.repo_dir, "rev-parse", "HEAD").strip()

        (self.repo_dir / "bridge.ts").write_text("export const bridge = true;\n", encoding="utf-8")
        self._git(self.repo_dir, "add", "bridge.ts")
        self._git(self.repo_dir, "commit", "-m", "feat(plugins): bridge")
        patch_text = self._git_out(self.repo_dir, "format-patch", "-1", "--stdout")
        self.patch_file = self.patch_dir / "0001-plugin-management-bridge.patch"
        self.patch_file.write_text(patch_text, encoding="utf-8")

        self._git(self.repo_dir, "reset", "--hard", "v1.22.6")

    def tearDown(self):
        self.temp_dir.cleanup()

    def _git(self, cwd: Path, *args: str):
        subprocess.run(["git", *args], cwd=str(cwd), check=True, capture_output=True)

    def _git_out(self, cwd: Path, *args: str) -> str:
        res = subprocess.run(["git", *args], cwd=str(cwd), check=True, capture_output=True, text=True)
        return res.stdout

    def test_schema_file_exists_and_valid(self):
        self.assertTrue(self.schema_path.exists(), "panel-release.schema.json must exist")
        data = json.loads(self.schema_path.read_text(encoding="utf-8"))
        self.assertEqual(data.get("properties", {}).get("schema_version", {}).get("const"), 1)
        self.assertEqual(data.get("properties", {}).get("schema_id", {}).get("const"), "cliproxyapi-patched-management-release/v1")

    def test_build_panel_resolves_exact_commit_and_emits_manifest(self):
        builder = importlib.import_module("panel.build-panel")

        fake_html = "<!DOCTYPE html><html><head><style>body{color:red}</style></head><body><h1>Panel</h1><script>console.log('ok');</script></body></html>"

        def mock_bun_runner(cwd: Path, cmd: list[str]):
            if cmd == ["bun", "install", "--frozen-lockfile"]:
                return
            if cmd == ["bun", "run", "verify"]:
                return
            if cmd == ["bun", "run", "build"]:
                dist = cwd / "dist"
                dist.mkdir(parents=True, exist_ok=True)
                (dist / "index.html").write_text(fake_html, encoding="utf-8")
                return
            raise ValueError(f"unexpected command: {cmd}")

        result = builder.build(
            upstream_tag="v1.22.6",
            output_dir=self.output_dir,
            clone_url=str(self.repo_dir),
            patch_file=self.patch_file,
            runner=mock_bun_runner,
            allow_custom_upstream=True,
        )

        self.assertEqual(result["upstream_tag"], "v1.22.6")
        self.assertEqual(result["upstream_commit"], self.commit_v1_22_6)
        self.assertEqual(len(result["upstream_commit"]), 40)

        patch_sha256 = hashlib.sha256(self.patch_file.read_bytes()).hexdigest()
        self.assertEqual(result["patch_sha256"], patch_sha256)

        expected_html_hash = hashlib.sha256(fake_html.encode("utf-8")).hexdigest()
        self.assertEqual(result["html_sha256"], expected_html_hash)

        html_file = self.output_dir / "management.html"
        sha_file = self.output_dir / "management.html.sha256"
        manifest_file = self.output_dir / "panel-manifest.json"

        self.assertTrue(html_file.exists())
        self.assertTrue(sha_file.exists())
        self.assertTrue(manifest_file.exists())

        self.assertEqual(sha_file.read_text(encoding="utf-8").strip(), f"{expected_html_hash}  management.html")

        manifest = json.loads(manifest_file.read_text(encoding="utf-8"))
        self.assertEqual(manifest["schema_version"], 1)
        self.assertEqual(manifest["schema_id"], "cliproxyapi-patched-management-release/v1")
        self.assertEqual(manifest["upstream_tag"], "v1.22.6")
        self.assertEqual(manifest["upstream_commit"], self.commit_v1_22_6)
        self.assertEqual(manifest["patch_sha256"], patch_sha256)
        self.assertEqual(manifest["asset"]["name"], "management.html")
        self.assertEqual(manifest["asset"]["sha256"], expected_html_hash)
        self.assertEqual(manifest["asset"]["size"], len(fake_html.encode("utf-8")))

    def test_patch_conflict_fails_before_dependency_install(self):
        builder = importlib.import_module("panel.build-panel")

        (self.repo_dir / "bridge.ts").write_text("export const conflict = true;\n", encoding="utf-8")
        self._git(self.repo_dir, "add", "bridge.ts")
        self._git(self.repo_dir, "commit", "-m", "conflict commit")
        self._git(self.repo_dir, "tag", "-f", "v1.22.6")

        installed = []

        def mock_bun_runner(cwd: Path, cmd: list[str]):
            if cmd == ["bun", "install", "--frozen-lockfile"]:
                installed.append(True)

        with self.assertRaises(Exception):
            builder.build(
                upstream_tag="v1.22.6",
                output_dir=self.output_dir,
                clone_url=str(self.repo_dir),
                patch_file=self.patch_file,
                runner=mock_bun_runner,
                allow_custom_upstream=True,
            )

        self.assertEqual(installed, [], "Must fail before running bun install")

    def test_patch_apply_does_not_require_global_git_identity(self):
        builder = importlib.import_module("panel.build-panel")
        empty_global_config = self.root / "empty.gitconfig"
        empty_global_config.write_text("", encoding="utf-8")

        fake_html = "<!DOCTYPE html><html><body><script>1</script></body></html>"

        def mock_bun_runner(cwd: Path, cmd: list[str]):
            if cmd == ["bun", "run", "build"]:
                dist = cwd / "dist"
                dist.mkdir(parents=True, exist_ok=True)
                (dist / "index.html").write_text(fake_html, encoding="utf-8")

        with mock.patch.dict(
            os.environ,
            {
                "GIT_CONFIG_GLOBAL": str(empty_global_config),
                "GIT_CONFIG_NOSYSTEM": "1",
            },
        ):
            builder.build(
                upstream_tag="v1.22.6",
                output_dir=self.output_dir,
                clone_url=str(self.repo_dir),
                patch_file=self.patch_file,
                runner=mock_bun_runner,
                allow_custom_upstream=True,
            )

    def test_non_reproducible_build_hashes_fail(self):
        builder = importlib.import_module("panel.build-panel")

        build_count = [0]

        def mock_bun_runner(cwd: Path, cmd: list[str]):
            if cmd == ["bun", "run", "build"]:
                build_count[0] += 1
                dist = cwd / "dist"
                dist.mkdir(parents=True, exist_ok=True)
                (dist / "index.html").write_text(f"<!DOCTYPE html><html><body>Build {build_count[0]}<script>{build_count[0]}</script></body></html>", encoding="utf-8")

        with self.assertRaises(Exception) as ctx:
            builder.build(
                upstream_tag="v1.22.6",
                output_dir=self.output_dir,
                clone_url=str(self.repo_dir),
                patch_file=self.patch_file,
                runner=mock_bun_runner,
                allow_custom_upstream=True,
            )

        self.assertIn("non-reproducible", str(ctx.exception).lower())

    def test_external_script_or_style_rejected(self):
        builder = importlib.import_module("panel.build-panel")

        bad_htmls = [
            '<!DOCTYPE html><html><head><script src="https://cdn.example.com/app.js"></script></head><body></body></html>',
            '<!DOCTYPE html><html><head><link rel="stylesheet" href="http://cdn.example.com/app.css"></head><body><script>1</script></body></html>',
            '<!DOCTYPE html><html><head><link rel="stylesheet" href="//cdn.example.com/app.css"></head><body><script>1</script></body></html>',
            '<!DOCTYPE html><html><head><script src="/assets/app.js"></script></head><body></body></html>',
        ]

        for bad_html in bad_htmls:
            def mock_bun_runner(cwd: Path, cmd: list[str]):
                if cmd == ["bun", "run", "build"]:
                    dist = cwd / "dist"
                    dist.mkdir(parents=True, exist_ok=True)
                    (dist / "index.html").write_text(bad_html, encoding="utf-8")

            with self.assertRaises(Exception) as ctx:
                builder.build(
                    upstream_tag="v1.22.6",
                    output_dir=self.output_dir,
                    clone_url=str(self.repo_dir),
                    patch_file=self.patch_file,
                    runner=mock_bun_runner,
                    allow_custom_upstream=True,
                )
            self.assertIn("external", str(ctx.exception).lower())

    def test_disallowed_upstream_url_rejected(self):
        builder = importlib.import_module("panel.build-panel")

        with self.assertRaises(Exception) as ctx:
            builder.build(
                upstream_tag="v1.22.6",
                output_dir=self.output_dir,
                clone_url="https://evil.com/fake-repo.git",
                patch_file=self.patch_file,
                runner=lambda c, m: None,
            )
        self.assertIn("disallowed", str(ctx.exception).lower())


if __name__ == "__main__":
    unittest.main()
