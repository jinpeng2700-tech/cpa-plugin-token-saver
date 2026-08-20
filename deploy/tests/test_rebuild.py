import hashlib
import importlib.util
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
REBUILD = ROOT / "deploy" / "rebuild"


class RebuildDeliveryTest(unittest.TestCase):
    def test_generic_secret_assignment_is_rejected(self):
        spec = importlib.util.spec_from_file_location("rebuild_validator", REBUILD / "validate-bundle.py")
        validator = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(validator)
        with tempfile.TemporaryDirectory() as temporary:
            secret = pathlib.Path(temporary) / "settings.json"
            secret.write_text('{"client_secret":"live-secret"}\n')
            with self.assertRaisesRegex(ValueError, "secret assignment"):
                validator.scan_secrets(secret, pathlib.PurePosixPath("config/settings.json"))

    def test_required_files_and_static_security_contracts(self):
        required = [
            "assemble-bundle.py",
            "validate-bundle.py",
            "stage-release.sh",
            "activate-release.sh",
            "rollback-release.sh",
            "config/config.template.yaml",
            "systemd/cliproxyapi.service",
            "nginx/cpa.ai2c.asia.conf",
            "firewall/cpa-network-guard.nft",
            "firewall/cpa-network-guard.service",
            "firewall/cpa-network-guard.env.template",
        ]
        for relative in required:
            self.assertTrue((REBUILD / relative).is_file(), relative)

        config = (REBUILD / "config/config.template.yaml").read_text()
        for expected in [
            "host: 127.0.0.1",
            "dir: /root/cliproxyapi/plugins",
            "rtk_enabled: false",
            "headroom_enabled: false",
            "caveman_enabled: false",
            "ponytail_enabled: false",
            "REPLACE_WITH_ONE_EXACT_MODEL_ID",
        ]:
            self.assertIn(expected, config)

        nginx = (REBUILD / "nginx/cpa.ai2c.asia.conf").read_text()
        for expected in [
            "return 301 https://$host$request_uri",
            "location ^~ /.well-known/acme-challenge/",
            "proxy_buffering off",
            "proxy_request_buffering off",
            "proxy_set_header Upgrade $http_upgrade",
            "limit_req zone=management_api",
            "proxy_set_header X-Forwarded-For $remote_addr",
        ]:
            self.assertIn(expected, nginx)

        nft = (REBUILD / "firewall/cpa-network-guard.nft").read_text()
        self.assertNotIn("flush ruleset", nft)
        for expected in ["table inet cpa_network_guard", "@podman_subnets", "8317", "8787", "18317", "1455", "54545", "51121"]:
            self.assertIn(expected, nft)

        service = (REBUILD / "systemd/cliproxyapi.service").read_text()
        for expected in ["WRITABLE_PATH", "UMask=0077", "/root/cliproxyapi/state/config.yaml"]:
            self.assertIn(expected, service)

        firewall_service = (REBUILD / "firewall/cpa-network-guard.service").read_text()
        for expected in ["PODMAN_SUBNET", "delete table inet cpa_network_guard", "add element inet cpa_network_guard"]:
                self.assertIn(expected, firewall_service)
        self.assertEqual(
            "PODMAN_SUBNET=REPLACE_WITH_PODMAN_SUBNET",
            (REBUILD / "firewall/cpa-network-guard.env.template").read_text().strip(),
        )

        for name in ["stage-release.sh", "activate-release.sh", "rollback-release.sh"]:
            script = (REBUILD / name).read_text()
            for expected in ["--apply", "DRY RUN", "id -u", "root"]:
                self.assertIn(expected, script)
        stage = (REBUILD / "stage-release.sh").read_text()
        for expected in [
            "$temporary/deploy/stage-release.sh",
            "$temporary/deploy/activate-release.sh",
            "$temporary/deploy/rollback-release.sh",
            "$temporary/deploy/validate-bundle.py",
        ]:
            self.assertIn(expected, stage)
        for name in ["activate-release.sh", "rollback-release.sh"]:
            script = (REBUILD / name).read_text()
            for expected in [
                "ln -s",
                "mv -T",
                "cliproxyapi.prev",
                "non-root-only path",
                "must equal $value exactly once",
                "model_allowlist must appear exactly once",
            ]:
                self.assertIn(expected, script)
        self.assertIn("model_allowlist must contain an exact model id", (REBUILD / "activate-release.sh").read_text())
        self.assertIn("legacy-artifacts.json", (REBUILD / "rollback-release.sh").read_text())

        shell = "sh"
        if pathlib.Path("/bin/sh").is_file() or sys.platform == "win32":
            subprocess.run(
                [shell, "-n", *(f"deploy/rebuild/{name}" for name in [
                    "stage-release.sh", "activate-release.sh", "rollback-release.sh"
                ])],
                check=True,
                cwd=ROOT,
            )

    def test_bundle_round_trip_and_secret_rejection(self):
        with tempfile.TemporaryDirectory() as temporary:
            temporary = pathlib.Path(temporary)
            inputs = temporary / "inputs"
            inputs.mkdir()
            for name, content in {
                "cli-proxy-api": b"\x7fELF\x00sk-false-positive-binary-string\n",
                "token-saver-v1.0.1.so": b"\x7fELF\x00fake plugin\n",
                "compat-probe": b"\x7fELF\x00fake probe\n",
                "update-verifier": b"\x7fELF\x00fake verifier\n",
            }.items():
                (inputs / name).write_bytes(content)

            output = temporary / "bundle"
            command = [
                sys.executable,
                str(REBUILD / "assemble-bundle.py"),
                "--input-dir",
                str(inputs),
                "--output-dir",
                str(output),
                "--deployment-id",
                "test-7.2.136-1.0.1",
                "--plugin-source-commit",
                "7be5a808",
                "--plugin-builder-digest",
                "sha256:" + "1" * 64,
                "--glibc-max",
                "2.3.2",
                "--write",
            ]
            dry_output = temporary / "missing-parent" / "dry-bundle"
            dry_command = [str(dry_output) if value == str(output) else value for value in command[:-1]]
            dry_run = subprocess.run(dry_command, capture_output=True, text=True)
            self.assertEqual(0, dry_run.returncode, dry_run.stdout + dry_run.stderr)
            self.assertIn("DRY RUN", dry_run.stdout)
            self.assertFalse(dry_output.exists())
            self.assertFalse(dry_output.parent.exists())

            assembled = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(0, assembled.returncode, assembled.stdout + assembled.stderr)
            subprocess.run(
                [sys.executable, str(REBUILD / "validate-bundle.py"), str(output)],
                check=True,
                capture_output=True,
                text=True,
            )

            manifest = json.loads((output / "approved-artifacts.json").read_text())
            self.assertEqual(2, manifest["schema_version"])
            self.assertEqual(1, manifest["verifier_schema"])
            self.assertEqual("v7.2.136", manifest["cli"]["tag"])
            self.assertEqual(
                "8f9160982bc2f26142f7b76a73fcc50f954c453470d5a6aefa81324ad18da288",
                manifest["cli"]["archive_sha256"],
            )
            self.assertEqual(
                {
                    "version": "1.0.1",
                    "abi": 1,
                    "rpc_schema": 3,
                    "source_commit": "7be5a808",
                    "builder_digest": "sha256:" + "1" * 64,
                    "glibc_max": "2.3.2",
                },
                {key: manifest["plugin"][key] for key in [
                    "version", "abi", "rpc_schema", "source_commit", "builder_digest", "glibc_max"
                ]},
            )
            self.assertGreaterEqual(len(manifest["files"]), 10)
            for required in [
                "deploy/stage-release.sh",
                "deploy/activate-release.sh",
                "deploy/rollback-release.sh",
                "deploy/validate-bundle.py",
            ]:
                entry = next(item for item in manifest["files"] if item["path"] == required)
                self.assertEqual("0700", entry["mode"])
            for entry in manifest["files"]:
                self.assertEqual(64, len(entry["sha256"]))
                bytes.fromhex(entry["sha256"])
                self.assertRegex(entry["mode"], r"^0[0-7]{3}$")
                self.assertEqual(
                    entry["sha256"],
                    hashlib.sha256((output / entry["path"]).read_bytes()).hexdigest(),
                )
            actual = sorted(
                path.relative_to(output).as_posix()
                for path in output.rglob("*")
                if path.is_file()
            )
            self.assertEqual(
                actual,
                sorted([entry["path"] for entry in manifest["files"]] + [
                    "approved-artifacts.json", "SHA256SUMS"
                ]),
            )

            tampered = temporary / "tampered-bundle"
            shutil.copytree(output, tampered)
            tampered_manifest_path = tampered / "approved-artifacts.json"
            tampered_manifest = json.loads(tampered_manifest_path.read_text())
            tampered_manifest["plugin"]["sha256"] = "0" * 64
            tampered_manifest_path.write_text(json.dumps(tampered_manifest, indent=2, sort_keys=True) + "\n")
            sums_path = tampered / "SHA256SUMS"
            sums = []
            for line in sums_path.read_text().splitlines():
                _, separator, relative = line.partition("  ")
                digest = hashlib.sha256((tampered / relative).read_bytes()).hexdigest()
                sums.append(f"{digest}{separator}{relative}")
            sums_path.write_text("\n".join(sums) + "\n")
            rejected = subprocess.run(
                [sys.executable, str(REBUILD / "validate-bundle.py"), str(tampered)],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(0, rejected.returncode)
            self.assertIn("identity hash mismatch", (rejected.stdout + rejected.stderr).lower())



if __name__ == "__main__":
    unittest.main()
