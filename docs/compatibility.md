# Compatibility and update contract

> **Production blocker:** the verified VPS runs systemd 239. Token Saver rollout and the CLI update wrapper must not be enabled there. `LoadCredential=` was added in systemd 247; the wrapper rejects every version below 247 and there is no environment-variable, command-line, script, or child-process fallback for the management credential.

## Token Saver release contract

Stable plugin release `v1.0.1` is built from the tagged commit archive, never from the working tree. The pinned release container combines Go `1.26.5` with Debian Bullseye glibc `2.31`; release validation rejects a plugin whose highest required GLIBC symbol exceeds `2.32` and rejects either helper when its ELF dynamic section contains `NEEDED`.

The immutable Linux amd64 release contains exactly:

- `token-saver-v1.0.1-linux-amd64.so`
- `compat-probe-v1.0.1-linux-amd64`
- `update-verifier-v1.0.1-linux-amd64`
- `GLIBC_REQUIREMENTS.txt`
- `release-metadata.json`
- `SHA256SUMS`

`release-metadata.json` binds version `1.0.1`, tag `v1.0.1`, full source commit, platform `linux-amd64`, ABI `1`, RPC `3`, and observed maximum GLIBC requirement. `SHA256SUMS` covers every release file except itself. The release workflow grants write permission only to the final job after the read-only compatibility job passes.

## Host matrix

Every plugin release must run the real `compat-probe` against both hosts below with the plugin-capable Linux asset (`CLIProxyAPI_<version>_linux_<arch>.tar.gz`, never `_no-plugin`):

| Host | Required evidence |
|---|---|
| CLIProxyAPI `v7.2.133` | Candidate starts on a temporary loopback port, loads ABI 1/RPC 3, applies config, dispatches a mock-provider request, and emits exactly one Caveman marker. |
| Exact approved latest stable | The same real dispatch plus config GET/PATCH/status and self-test. At the time this document was prepared, the upstream latest was `v7.2.134`; release automation must resolve and pin the current exact tag rather than trust this sentence. |

The Management API does not expose normalizer capabilities or their complete ordering. Production therefore also requires a manual config audit proving Token Saver has `priority: -100` and no enabled normalizer has a lower numeric priority. A plugin self-test alone is not host-dispatch evidence.

`update-verifier` exits with these stable classifications:

| Exit | Classification | Wrapper action |
|---:|---|---|
| 0 | `compatible` | Continue or accept. `dependency=degraded` with Headroom desired but ineffective is compatible and must not roll back the CLI. |
| 2 | `blocked` | Stop without blaming or fingerprinting the candidate. If installation already happened, restore the unique new updater backup but do not create a candidate-failure fingerprint. |
| 3 | `candidate_failure` | For an ordinary release, restore and verify the previous CLI, then record the exact failed fingerprint. |

The failed fingerprint is SHA-256 over the approved CLI version, CLI binary SHA-256, architecture, plugin SHA-256, and verifier schema. An identical fingerprint is skipped on later timer runs until an administrator removes the root-owned record after investigation.

## Credential boundary

The only supported path is systemd `LoadCredential=cliproxyapi-management-key:/absolute/root-only/source`. systemd supplies `CREDENTIALS_DIRECTORY` to the wrapper; the wrapper immediately unexports it and gives it only to the Go verifier. The wrapper never opens the credential, never calls the Management API with `curl`, and never forwards the credential directory to downloads, the compatibility probe, the official updater, or logging commands.

The source credential must be a root-owned regular file with mode `0600`. The approval, optional security override, and failed-fingerprint files must be root-owned regular files and must not be group- or other-writable. The official systemd credential documentation is [systemd.exec — Credentials](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#Credentials).

## Update boundaries

The wrapper is a gate around the existing official updater; it does not replace its download, replacement, restart, retention, or internal rollback logic. It:

1. resolves an exact root-approved official tag and plugin-capable asset;
2. verifies the upstream `checksums.txt`, extracts the candidate, and matches the candidate binary to the independently approved SHA;
3. runs current-state verifier preflight and real candidate dispatch;
4. invokes the existing `update.sh` with the already-probed exact tag, never `latest`;
5. accepts exactly one new root-owned `backup-pre-<tag>-<timestamp>/` directory containing `cli-proxy-api`, `version.txt`, and `cliproxyapi.service`;
6. runs postinstall verifier and either accepts, restores that backup, or applies the security override contract.

Missing, untrusted, or multiple new backup directories are rollback-safety failures: the timer is disabled and a priority-alert message is emitted.

For a root-approved security override that exactly matches CLI version/SHA/architecture and has a non-empty reason, only a plugin-compatibility failure is eligible. The new security CLI is retained, Token Saver is moved to the root-only quarantine, the service is restarted, and `compat-probe -mode core-only` must prove raw core inference through a local mock with plugins disabled. If that proof fails, the security CLI remains installed, the plugin remains isolated, the timer is disabled, and manual intervention is required. The wrapper never rolls a security-approved CLI back merely to preserve plugin compatibility.

## External dependency blockers

Headroom currently listens on `0.0.0.0:8787`; this is a separate production blocker. Before the Headroom stage is enabled it must listen only on literal `127.0.0.1` and/or `::1`, have public egress denied by default, and have telemetry and raw-prompt logging disabled. A Headroom outage after those controls are in place is a degraded stage, not a CLI compatibility failure.
