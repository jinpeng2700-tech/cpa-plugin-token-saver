# VPS rollout and rollback runbook

## Stop: current VPS is not deployable

Two verified facts block production rollout today:

1. The VPS runs **systemd 239**, below the `LoadCredential=` minimum of systemd 247. Upgrade the OS/systemd through a supported path, then re-check `systemd --version`. Do not substitute an environment variable, CLI argument, shell file read, or helper process for the credential channel.
2. Headroom listens on **`0.0.0.0:8787`**. Bind it to loopback, deny public egress by default, and disable telemetry and raw-prompt persistence before enabling the Headroom stage.

The steps below are a runbook for after both blockers and all offline release gates are cleared. They are not authorization to mutate the current VPS.

## 1. Install immutable, root-owned inputs

Place the approved artifacts at explicit absolute paths. The stable plugin install path is architecture-specific:

- amd64: `/root/.cli-proxy-api/plugins/linux/amd64/token-saver.so`
- arm64: `/root/.cli-proxy-api/plugins/linux/arm64/token-saver.so`

The wrapper compares `uname` with `cli.arch` and refuses a mismatch. Install `compat-probe`, `update-verifier`, `update-wrapper.sh`, and the existing `update.sh` as root-owned, non-group/other-writable executable files. Keep the official `update.sh`; do not replace it with wrapper business logic.

Copy `deploy/approved-artifacts.example.json`, replace every placeholder with locally reviewed exact values, save it as `/root/cliproxyapi/approved-artifacts.json`, then set root ownership and mode `0600` or `0644`. The CLI SHA is the extracted executable SHA used by the verifier; upstream `checksums.txt` separately authenticates transport. An optional `security-overrides.json` must be root-owned mode `0600`, exactly bind CLI version/SHA/architecture, and state the review reason.

Create `/root/.config/cliproxyapi/credentials/management-key` as a root-owned regular file mode `0600`. Do not print it while installing or testing.

## 2. Close Headroom and ordering blockers

Before any production stage is on, record evidence that:

- Headroom listens only on `127.0.0.1:8787` and/or `[::1]:8787`;
- the host firewall/service sandbox denies public egress by default, with any exception reduced to a reviewed minimal allowlist;
- environment proxies, redirects, response compression, telemetry, and raw-prompt logs are disabled for this path;
- an external host cannot reach port 8787 and a network/log sentinel sees no prompt egress;
- `plugins.configs.token-saver.priority` is exactly `-100`;
- a manual audit finds zero enabled normalizers with a lower priority, because the current API cannot expose this capability ordering;
- `model_allowlist` is non-empty and contains only selected low-risk models;
- the plugin host is enabled while RTK, Headroom, Caveman, and Ponytail are all off.

## 3. Integrate the wrapper without changing source or schedule

The actual service is the root user unit `cliproxyapi-update.service`; its timer is `/root/.config/systemd/user/cliproxyapi-update.timer`, scheduled daily at 00:00. Preserve that timer and the official GitHub release/checksum source.

The repository keeps the plan-requested template path `deploy/systemd/cliproxyapi-updater.service.d/credentials.conf`. Install that file under the real unit name:

```text
/root/.config/systemd/user/cliproxyapi-update.service.d/credentials.conf
```

It clears the existing `ExecStart`, changes it to `/root/cliproxyapi/update-wrapper.sh latest`, and adds the absolute `LoadCredential=` source. The base unit must retain `WorkingDirectory=/root/cliproxyapi`. After installation, run daemon-reload and inspect the merged unit; the final update command must be the wrapper and the timer must still activate `cliproxyapi-update.service` daily. Do not enable the drop-in unless `systemd --version` is at least 247.

The wrapper resolves `latest` once, proves that exact approved tag, and passes the exact tag to the existing `/root/cliproxyapi/update.sh`. This prevents a later release from entering between probe and install.

## 4. Required update rehearsals

Perform these with sanitized fixtures and a sentinel management credential in an isolated maintenance window:

1. **Normal official update:** preflight, real candidate dispatch, existing updater, unique new backup directory, and postinstall verifier all pass. Headroom `degraded` is accepted without rollback.
2. **Ordinary incompatibility:** make postinstall return a plugin candidate failure. Confirm binary, `version.txt`, and `cliproxyapi.service` come only from the one new `backup-pre-<tag>-<timestamp>/` directory; restart and old-state verifier pass; the exact failed fingerprint is recorded.
3. **Next-day bad fingerprint:** rerun unchanged inputs. Confirm no network fetch, candidate probe, or updater invocation occurs.
4. **Security override:** use an exact root-approved security entry. Confirm the new CLI remains, the plugin moves to the root-only quarantine, restart succeeds, and `compat-probe -mode core-only` proves mock-provider inference with zero plugin markers. A failed core-only proof must keep the security CLI, keep the plugin isolated, disable the timer, and emit a high-priority manual-intervention alert.
5. **Rollback failure:** break the restore or old-state verifier. Confirm `cliproxyapi-update.timer` is disabled, backups are preserved, and a priority-alert message is present.
6. **Credential sentinel:** inspect wrapper/verifier `/proc/<pid>/cmdline`, `/proc/<pid>/environ`, journal, verifier reports, failure fingerprint, and backups. The sentinel value must occur nowhere. Only the verifier may receive `CREDENTIALS_DIRECTORY`; downloads, compat probes, updater, and logger must not receive it.

## 5. Minimum pilot

Copy `testdata/pilot/pilot-report.example.json` and validate it against `pilot-report.schema.json`. Never paste a production prompt, request/response, API key, or management credential into the report.

Run in a low-traffic window with an administrator continuously present:

1. all four stages off, 10–20 controlled requests, byte-identical output, zero Headroom calls;
2. RTK only, 10–20 fixed tasks;
3. RTK + Headroom, 10–20 fixed tasks;
4. add Caveman **lite**, 10–20 fixed tasks;
5. add Ponytail **lite**, 10–20 fixed tasks.

Advance only after the new config generation/digest is visible. Record build fingerprints, per-stage window and sample size, provider-reported input tokens separately from byte measurements, RSS/headroom concurrency/swap, and the fixed-task quality results.

Stop immediately on plugin 5xx, provider 400/422, structure/tool/SSE corruption, duplicate prompt markers, critical task failure, Headroom timeout/saturation, OOM/service restart, or new persistent swap-in. Disable the most recent stage; if attribution is unclear, disable all four. Wait for the new generation and old in-flight count to reach zero, then prove `pipeline=all_bypassed`, byte-identical fixture output, zero new Headroom calls, and complete confirmation within 30 seconds.

Full/Ultra levels, 24-hour observation, 50–100 requests, and p95/p99 are later confidence expansion. They do not replace or retroactively improve this minimum gate.
