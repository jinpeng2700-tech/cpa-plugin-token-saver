# Headroom Authenticated Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add authenticated Headroom connectivity checks and four-stage metrics while automatically rebuilding one minimal bridge patch over official Management Center releases and deploying the approved CLI/plugin/panel set atomically.

**Architecture:** Token Saver exposes passive authenticated dashboard data and one isolated active Headroom check. Official Management Center remains upstream-owned; a generic iframe capability bridge is maintained as one patch, rebuilt in GitHub Actions, and bound into the composite approval manifest. VPS updater downloads only exact approved panel bytes and preserves current deployment on any verification failure.

**Tech Stack:** Go 1.26.5, Bun 1.3.14, React 19, TypeScript 6, GitHub Actions, Python 3 stdlib, systemd user units.

**Spec:** `docs/superpowers/specs/2026-08-21-headroom-authenticated-dashboard-design.md`

## Global Constraints

- Plugin resource iframe must never receive, read, store, log, or transmit the management key.
- Public `/v0/resource/plugins/token-saver/...` routes remain unauthenticated and passive.
- Active Headroom check uses `/v1/compress`, one attempt, configured timeout bounded to 100-1500 ms, and no production metrics or circuit mutation.
- Existing `/v0/management/plugins/token-saver/self-test` contract remains unchanged.
- Four-stage counters are process-lifetime only and reset after CLIProxyAPI restart.
- Management Center patch applies to exact official source and fails closed on conflict.
- Approved identity binds CLIProxyAPI, Token Saver, and `management.html` bytes.
- No new dependency unless already present in repository lock files.
- No firewall or management-key changes.
- Preserve unrelated `D:\Project\60.CLIProxyAPI\cpa-plugin-token-saver\test_write.txt`.

---

## File map

### Token Saver repository

- Modify `internal/headroom/client.go`: closeable client support used by isolated checks.
- Create `internal/headroom/check.go`: isolated functional check and stable result.
- Create `internal/headroom/check_test.go`: success, timeout, connection, and cleanup tests.
- Modify `internal/metrics/metrics.go`: safe saved-byte projection.
- Modify `internal/metrics/metrics_test.go`: underflow and fixed-stage tests.
- Modify `internal/management/dto.go`: dashboard/check routes and DTOs.
- Modify `internal/management/handler.go`: passive dashboard and active check handlers.
- Modify `internal/management/handler_test.go`: routing, passivity, cooldown, and metric-isolation tests.
- Modify `internal/management/headroom_page.html`: bridge client, fallback, cards, and metrics.
- Modify `test/abi-host/abi_test.go` and `test/abi-host/main.go`: route count and ABI contract.
- Modify `tools/compat-probe/probe/probe.go` and tests: authenticated dashboard/check host probe.
- Modify `internal/abi/envelope.go`, `Makefile`, release/rebuild contracts: version `1.1.0`.
- Create `panel/patches/0001-plugin-management-bridge.patch`: exact official panel delta.
- Create `panel/build-panel.py`: deterministic upstream checkout, patch, test, and build driver.
- Create `panel/panel-release.schema.json`: immutable panel manifest contract.
- Create `panel/tests/test_build_panel.py`: source locking and patch/build failure tests.
- Create `.github/workflows/release-panel.yml`: scheduled/manual panel tracking release.
- Modify `deploy/approved-release.schema.json`: schema v2 panel identity.
- Modify `deploy/tests/release_workflow_test.go`: panel selection, lock, fingerprint, and tag.
- Modify `.github/workflows/promote-cliproxyapi.yml`: panel discovery/download/attestation.
- Create `deploy/vps-reconciler.py`: repository-owned composite updater.
- Create `deploy/tests/test_vps_reconciler.py`: exact download, hash, rollback, and v1 transition.
- Modify `docs/compatibility.md` and `docs/vps-rollout.md`: panel and bridge contracts.

### Management Center working repository

Base exact official tag `v1.22.6` in a separate worktree.

- Create `src/features/plugins/pluginManagementBridge.ts`: validation and parent proxy.
- Create `tests/pluginManagementBridge.test.ts`: pure contract tests.
- Modify `src/features/plugins/PluginResourcePage.tsx`: iframe ref and bridge lifecycle.

Only these three paths enter the stored patch.

---

### Task 1: Saved-byte projection

**Files:**
- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`

**Interfaces:**
- Produces: `func (snapshot StageSnapshot) SavedBytes() uint64`
- Consumed by: management dashboard DTO projection.

- [ ] **Step 1: Write failing saved-byte tests**

```go
func TestStageSnapshotSavedBytesNeverUnderflows(t *testing.T) {
	tests := []struct {
		input, output, want uint64
	}{
		{100, 40, 60},
		{40, 40, 0},
		{40, 100, 0},
	}
	for _, test := range tests {
		got := (StageSnapshot{InputBytes: test.input, OutputBytes: test.output}).SavedBytes()
		if got != test.want {
			t.Fatalf("SavedBytes(%d,%d) = %d, want %d", test.input, test.output, got, test.want)
		}
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./internal/metrics
```

Expected: compile failure because `SavedBytes` is undefined.

- [ ] **Step 3: Add minimal projection**

```go
func (snapshot StageSnapshot) SavedBytes() uint64 {
	if snapshot.OutputBytes >= snapshot.InputBytes {
		return 0
	}
	return snapshot.InputBytes - snapshot.OutputBytes
}
```

- [ ] **Step 4: Verify GREEN**

Run the Task 1 test command. Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/metrics/metrics.go internal/metrics/metrics_test.go
git commit -m "feat(metrics): project saved bytes safely"
```

---

### Task 2: Isolated Headroom functional checker

**Files:**
- Modify: `internal/headroom/client.go`
- Create: `internal/headroom/check.go`
- Create: `internal/headroom/check_test.go`

**Interfaces:**
- Produces:

```go
type CheckResult struct {
	Reachable bool
	Outcome   Outcome
	Latency   time.Duration
}

func Check(ctx context.Context, baseURL string, timeout time.Duration) CheckResult
func (client *Client) Close()
```

- Consumed by: `management.Options.HeadroomCheck`.

- [ ] **Step 1: Write failing checker tests**

Use `httptest.Server` bound through the existing loopback guard and assert:

```go
func TestCheckReportsFunctionalSuccess(t *testing.T) {
	server := validCompressServer(t)
	start := time.Now()
	result := Check(context.Background(), server.URL, 500*time.Millisecond)
	if !result.Reachable || result.Outcome != OutcomeApplied || result.Latency <= 0 ||
		result.Latency > time.Since(start) {
		t.Fatalf("result = %#v", result)
	}
}

func TestCheckReportsTimeoutWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := slowCompressServer(t, &calls)
	result := Check(context.Background(), server.URL, 100*time.Millisecond)
	if result.Reachable || result.Outcome != OutcomeTimeout || calls.Load() != 1 {
		t.Fatalf("result=%#v calls=%d", result, calls.Load())
	}
}
```

Add connection-refused and canceled-context cases.

- [ ] **Step 2: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./internal/headroom
```

Expected: compile failure for `Check`.

- [ ] **Step 3: Implement isolated check**

`Check` constructs a fresh `Client`, measures only `Probe`, calls `Close`, and
maps reachable outcomes exactly:

```go
reachable := outcome == OutcomeApplied || outcome == OutcomeNoChange
```

`Close` calls `CloseIdleConnections` on the owned transport through
`httpClient.CloseIdleConnections()`.

- [ ] **Step 4: Verify GREEN and race safety**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./internal/headroom
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -race -count=1 ./internal/headroom
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/headroom/client.go internal/headroom/check.go internal/headroom/check_test.go
git commit -m "feat(headroom): add isolated connectivity check"
```

---

### Task 3: Authenticated dashboard and check routes

**Files:**
- Modify: `internal/management/dto.go`
- Modify: `internal/management/handler.go`
- Modify: `internal/management/handler_test.go`

**Interfaces:**
- Produces:

```go
const DashboardRoute = "/plugins/token-saver/dashboard"
const HeadroomCheckRoute = "/plugins/token-saver/headroom/check"

type HeadroomCheckFunc func(context.Context, string, time.Duration) headroom.CheckResult

type DashboardStageDTO struct {
	Executed, Bypassed, FailOpen, Timeout, Saturated uint64
	InputBytes, OutputBytes, SavedBytes, DurationNano uint64
}
```

Dashboard Headroom sample fields are:

```go
LastCheckedAt *time.Time `json:"last_checked_at"`
LastLatencyMS *uint64    `json:"last_latency_ms"`
LastOutcome   string     `json:"last_outcome"`
```

Before first check, pointers are `nil` and `LastOutcome` is `"unknown"`.
A completed unreachable check returns HTTP `200` with `reachable:false`;
disabled or invalid configuration returns `409`; in-flight or cooldown
rejection returns `429`. Cooldown is exactly two seconds.

- Consumes: `metrics.StageSnapshot.SavedBytes`, `headroom.Check`.

- [ ] **Step 1: Write failing registration and dashboard tests**

Assert four authenticated routes and two resources:

```go
wantRoutes := []Route{
	{Method: http.MethodGet, Path: StatusRoute},
	{Method: http.MethodPost, Path: SelfTestRoute},
	{Method: http.MethodGet, Path: DashboardRoute},
	{Method: http.MethodPost, Path: HeadroomCheckRoute},
}
```

Record a stage metric, call dashboard twice, and assert:

```go
if checkerCalls.Load() != 0 {
	t.Fatal("passive dashboard called Headroom")
}
if dashboard.Stages.RTK.SavedBytes != 40 {
	t.Fatalf("RTK = %#v", dashboard.Stages.RTK)
}
```

- [ ] **Step 2: Write failing active-check tests**

Inject:

```go
HeadroomCheck: func(context.Context, string, time.Duration) headroom.CheckResult {
	return headroom.CheckResult{
		Reachable: true,
		Outcome: headroom.OutcomeApplied,
		Latency: 12 * time.Millisecond,
	}
}
```

Assert URL and timeout passed to the checker, response latency `12`, and
dashboard remembers the result. Snapshot metrics and circuit before/after and
assert equality.

Add disabled, timeout, connection, one-in-flight, and cooldown cases.

- [ ] **Step 3: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./internal/management
```

Expected: missing route/DTO failures.

- [ ] **Step 4: Implement DTOs and route dispatch**

Add `Options.HeadroomCheck`, default it to `headroom.Check`, store last check
under the existing management mutex boundary, and use a capacity-one channel
for non-blocking exclusivity.

Return stable outcomes only. Do not serialize raw errors.

- [ ] **Step 5: Verify GREEN**

Run Task 3 test command. Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/management/dto.go internal/management/handler.go internal/management/handler_test.go
git commit -m "feat(management): add Headroom dashboard APIs"
```

---

### Task 4: Embedded page bridge client and fallback UI

**Files:**
- Modify: `internal/management/headroom_page.html`
- Modify: `internal/management/handler_test.go`

**Interfaces:**
- Consumes parent messages from `cpa.plugin.management.response` version `1`.
- Sends only dashboard GET and Headroom check POST capability requests.
- Falls back to `/v0/resource/plugins/token-saver/headroom/status`.

- [ ] **Step 1: Write failing static security tests**

Require:

```go
for _, required := range []string{
	"cpa.plugin.management.request",
	"/plugins/token-saver/dashboard",
	"/plugins/token-saver/headroom/check",
	"需要新版管理面板",
	"saved_bytes",
} {
	if !strings.Contains(body, required) { t.Errorf("missing %q", required) }
}
```

Forbid:

```go
for _, forbidden := range []string{
	"fetch('/v0/management",
	"managementKey",
	"Authorization",
	"localStorage",
	"sessionStorage",
	"URLSearchParams",
	"document.cookie",
} {
	if strings.Contains(body, forbidden) { t.Errorf("contains %q", forbidden) }
}
```

- [ ] **Step 2: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./internal/management
```

Expected: required bridge/UI strings absent.

- [ ] **Step 3: Implement minimal page**

Use one `Map` of pending request IDs, a 10-second timeout, exact response type,
and `window.parent.postMessage(request, expectedParentOrigin)`.

Start with a credential-free handshake:

```js
window.parent.postMessage({
  type: 'cpa.plugin.management.hello',
  version: 1,
  pluginId: 'token-saver'
}, '*');
```

Accept `cpa.plugin.management.ready` only when
`event.source === window.parent`; record `event.origin` as
`expectedParentOrigin`, then send every request to that exact origin. If no
valid ready message arrives within two seconds, use public fallback.

Poll dashboard every 5 seconds only after one successful bridge response.
Button performs one check and then one dashboard refresh.

Render:

- URL
- `🟢 运行中` or `🔴 无法连接`
- latency in milliseconds
- circuit and checked time
- four stage cards: executed and saved bytes
- process-restart reset notice

- [ ] **Step 4: Validate embedded JavaScript**

Extract script content and compile it with Node:

```powershell
node -e "const fs=require('fs'),vm=require('vm');const h=fs.readFileSync('internal/management/headroom_page.html','utf8');const s=h.match(/<script>([\s\S]*)<\/script>/)[1];new vm.Script(s)"
```

Expected: exit `0`.

- [ ] **Step 5: Verify GREEN**

Run Task 4 Go tests and `git diff --check`. Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/management/headroom_page.html internal/management/handler_test.go
git commit -m "feat(ui): add authenticated Headroom dashboard"
```

---

### Task 5: Real-host ABI and compatibility contracts

**Files:**
- Modify: `test/abi-host/abi_test.go`
- Modify: `test/abi-host/main.go`
- Modify: `tools/compat-probe/probe/probe.go`
- Modify: `tools/compat-probe/probe/probe_test.go`

**Interfaces:**
- Verifies four management routes, two resources, passive dashboard, and one
  authenticated active check.

- [ ] **Step 1: Update expected registration tests**

Change route count from `2` to `4`; preserve resource count `2`. Require exact
paths for dashboard and check.

- [ ] **Step 2: Add real-host dashboard/check assertions**

In compat probe:

1. GET dashboard with management key;
2. snapshot stage metrics;
3. POST Headroom check once;
4. assert stable check DTO;
5. GET dashboard and confirm remembered check;
6. assert check did not increment four-stage metrics;
7. repeat public resource GET without auth and require zero 401/403.

- [ ] **Step 3: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./test/abi-host ./tools/compat-probe/probe
```

Expected: old counts/routes fail.

- [ ] **Step 4: Make fixture decoding match new DTOs**

Add exact JSON structs; reject unknown or missing fields where contracts are
security-sensitive.

- [ ] **Step 5: Verify GREEN**

Run Task 5 tests. Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add test/abi-host tools/compat-probe
git commit -m "test(compat): gate authenticated dashboard routes"
```

---

### Task 6: Generic Management Center iframe bridge

**Files in Management Center worktree based on official `v1.22.6`:**
- Create: `src/features/plugins/pluginManagementBridge.ts`
- Create: `tests/pluginManagementBridge.test.ts`
- Modify: `src/features/plugins/PluginResourcePage.tsx`

**Interfaces:**
- Produces `createPluginManagementBridge(options): () => void`.
- Calls existing `apiClient.get` or `apiClient.post`; never accepts headers.

- [ ] **Step 1: Create official worktree**

```powershell
git fetch origin --tags
git worktree add .worktrees/plugin-management-bridge -b feat/plugin-management-bridge v1.22.6
```

Verify:

```powershell
git describe --tags --exact-match
```

Expected: `v1.22.6`.

- [ ] **Step 2: Write failing pure validation tests**

Test `validatePluginManagementRequest` with:

- valid and invalid `hello` handshakes;
- exact valid GET and POST;
- wrong `event.source`;
- wrong origin;
- wrong plugin ID;
- `PUT`;
- `/v0/management/config`;
- sibling plugin namespace;
- query string;
- body over 16 KiB;
- credentials/header fields;
- duplicate in-flight request ID.

Expected valid output:

```ts
{
  requestId: 'r1',
  method: 'GET',
  path: '/v0/management/plugins/token-saver/dashboard',
  body: undefined,
}
```

- [ ] **Step 3: Verify RED**

```powershell
bun test tests/pluginManagementBridge.test.ts
```

Expected: module not found.

- [ ] **Step 4: Implement bridge module**

Keep validation pure. Runtime handler must:

```ts
if (request.method === 'GET') {
  data = await apiClient.get(request.path);
} else {
  data = await apiClient.post(request.path, request.body);
}
```

Normalize errors to `{ ok:false, status, error:{code,message} }` without
headers, stack, Axios config, or credentials.

The parent validates `hello` against the exact iframe source and resolved
iframe origin, then replies with `cpa.plugin.management.ready` to that child
origin. No request is accepted before this handshake.

- [ ] **Step 5: Wire `PluginResourcePage`**

Add `useRef<HTMLIFrameElement>(null)`, resolve expected iframe origin, install
the bridge only while connected and resource exists, and clean it up on route
change/unmount.

- [ ] **Step 6: Verify Management Center**

```powershell
bun install --frozen-lockfile
bun test tests/pluginManagementBridge.test.ts
bun run type-check
bun run lint
bun run build
```

Expected: PASS and one `dist/index.html`.

- [ ] **Step 7: Commit and export one patch**

```powershell
git add src/features/plugins/pluginManagementBridge.ts src/features/plugins/PluginResourcePage.tsx tests/pluginManagementBridge.test.ts
git commit -m "feat(plugins): proxy authenticated iframe capabilities"
New-Item -ItemType Directory -Force D:\Project\60.CLIProxyAPI\cpa-plugin-token-saver\.worktrees\headroom-dashboard-bridge\panel\patches | Out-Null
git format-patch -1 --stdout > D:\Project\60.CLIProxyAPI\cpa-plugin-token-saver\.worktrees\headroom-dashboard-bridge\panel\patches\0001-plugin-management-bridge.patch
```

Do not push to official repository.

---

### Task 7: Deterministic patched-panel builder

**Files:**
- Create: `panel/patches/0001-plugin-management-bridge.patch`
- Create: `panel/build-panel.py`
- Create: `panel/panel-release.schema.json`
- Create: `panel/tests/test_build_panel.py`

**Interfaces:**
- Command:

```text
python panel/build-panel.py --upstream-tag v1.22.6 --output dist-panel
```

- Produces:
  - `management.html`
  - `management.html.sha256`
  - `panel-manifest.json`

`panel-manifest.json` uses schema
`cliproxyapi-patched-management-release/v1`.

- [ ] **Step 1: Write failing builder tests**

Use temporary local Git fixtures; inject command runner and downloader.
Assert:

- exact tag resolves to one 40-character commit;
- patch hash enters manifest;
- patch conflict fails before dependency install;
- two different build hashes fail;
- output contains one HTML file and no external script/style references.

- [ ] **Step 2: Verify RED**

```powershell
python -m unittest panel.tests.test_build_panel -v
```

Expected: module missing.

- [ ] **Step 3: Implement stdlib builder**

Use `subprocess.run(..., check=True)` and temporary directories. Do not invoke
shell strings. Clone/fetch only:

```text
https://github.com/router-for-me/Cli-Proxy-API-Management-Center.git
```

Checkout exact tag, record `git rev-parse HEAD`, apply patch with
`git am --3way`, run frozen Bun verification/build twice, and emit sorted JSON.

- [ ] **Step 4: Verify GREEN**

Run Task 7 unit tests, then one real build for `v1.22.6`. Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add panel
git commit -m "build(panel): rebuild bridge over official releases"
```

---

### Task 8: Automated panel release workflow

**Files:**
- Create: `.github/workflows/release-panel.yml`
- Modify: `deploy/tests/release_workflow_test.go`
- Modify: `docs/compatibility.md`

**Interfaces:**
- Release tag: `panel-v1.22.6-bridge.1`
- Workflow identity:
  `.github/workflows/release-panel.yml@refs/heads/main`

- [ ] **Step 1: Write failing workflow contract tests**

Require:

- schedule `43 */6 * * *`;
- manual dispatch;
- pinned checkout, setup-python, setup-bun, upload/download, and attestation
  actions;
- latest non-draft/non-prerelease official panel selection;
- `panel/build-panel.py`;
- two-stage build/publish jobs;
- immutable duplicate refusal;
- no `--clobber`;
- attestation before release creation.

- [ ] **Step 2: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 -run Panel ./deploy/tests
```

Expected: workflow file missing.

- [ ] **Step 3: Implement workflow**

Build job has read-only permissions. Publish job alone gets:

```yaml
permissions:
  actions: read
  attestations: write
  contents: write
  id-token: write
```

Release exactly:

- `management.html`
- `management.html.sha256`
- `panel-manifest.json`

- [ ] **Step 4: Verify GREEN**

Run panel workflow contract tests and parse YAML in existing Go workflow test
helpers. Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add .github/workflows/release-panel.yml deploy/tests/release_workflow_test.go docs/compatibility.md
git commit -m "ci(panel): track official management releases"
```

---

### Task 9: Composite approval manifest v2

**Files:**
- Modify: `deploy/approved-release.schema.json`
- Modify: `deploy/trust-policy.json`
- Modify: `deploy/tests/release_workflow_test.go`
- Modify: `.github/workflows/promote-cliproxyapi.yml`

**Interfaces:**
- Produces approved schema `2` with required `panel`.
- Fingerprint input includes official, plugin, compatibility, and panel.

- [ ] **Step 1: Add failing panel selection fixtures**

Define panel release fixtures containing:

```go
type approvedPanel struct {
	Repository     string              `json:"repository"`
	ReleaseID     uint64              `json:"release_id"`
	Tag           string              `json:"tag"`
	UpstreamTag   string              `json:"upstream_tag"`
	UpstreamCommit string             `json:"upstream_commit"`
	PatchSHA256   string              `json:"patch_sha256"`
	Asset         approvedAsset       `json:"asset"`
	Manifest      approvedAsset       `json:"manifest"`
	Attestation   approvedAttestation `json:"attestation"`
}
```

Test panel-only change increments generation and changes fingerprint.

- [ ] **Step 2: Add failing schema tests**

Require top-level `panel`, schema version `2`, exact repository
`jinpeng2700-tech/cpa-plugin-token-saver`, tag pattern
`^panel-v[0-9]+\.[0-9]+\.[0-9]+-bridge\.[1-9][0-9]*$`, and no unknown fields.

- [ ] **Step 3: Verify RED**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./deploy/tests
```

Expected: panel/schema tests fail.

- [ ] **Step 4: Extend promotion selection and lock**

Enumerate panel releases from `PLUGIN_REPOSITORY`, select highest valid stable
panel tag, download exact asset IDs, verify panel attestation, and compare
`panel-manifest.json` identity to selected release.

Accept a schema-v1 previous manifest only for transition lineage; emit schema
v2 exclusively.

- [ ] **Step 5: Update approved tag**

Generate:

```text
approved-cli-v7.2.137-plugin-v1.1.0-panel-v1.22.6-bridge.1-g4
```

Use actual selected versions and monotonic generation at runtime.

- [ ] **Step 6: Verify GREEN**

Run all deploy tests. Expected: PASS.

- [ ] **Step 7: Commit**

```powershell
git add deploy/approved-release.schema.json deploy/trust-policy.json deploy/tests/release_workflow_test.go .github/workflows/promote-cliproxyapi.yml
git commit -m "feat(release): approve CLI plugin and panel together"
```

---

### Task 10: Repository-owned VPS reconciler

**Files:**
- Create: `deploy/vps-reconciler.py`
- Create: `deploy/tests/test_vps_reconciler.py`
- Modify: `docs/vps-rollout.md`

**Interfaces:**
- Command: `python3 /root/cliproxyapi-updater.py`
- Accepts schema v1 only when current deployment already uses it.
- Requires panel identity for every schema-v2 target.

- [ ] **Step 1: Write failing updater tests**

Inject filesystem, downloader, and service runner. Cover:

- schema-v2 exact CLI/plugin/panel download;
- panel SHA mismatch leaves active symlink unchanged;
- missing panel rejected;
- failed service smoke restores previous symlink;
- existing schema-v1 deployment recognized during transition;
- state directory never removed or copied;
- logs omit URLs containing credentials and omit all config secrets.

- [ ] **Step 2: Verify RED**

```powershell
python -m unittest deploy.tests.test_vps_reconciler -v
```

Expected: module missing.

- [ ] **Step 3: Implement reconciler**

Use only Python stdlib. Download panel from manifest repository/tag/asset,
verify SHA-256, stage mode `0600`, and include panel tag in deployment ID.
Never delete the currently active or previous deployment.

- [ ] **Step 4: Verify GREEN**

Run Task 10 tests and:

```powershell
python -m py_compile deploy/vps-reconciler.py
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add deploy/vps-reconciler.py deploy/tests/test_vps_reconciler.py docs/vps-rollout.md
git commit -m "feat(deploy): reconcile approved panel bytes"
```

---

### Task 11: Version, full verification, and review

**Files:**
- Modify version-bearing release/rebuild files from `1.0.2` to `1.1.0`.
- Modify docs contracts.

**Interfaces:**
- Plugin release: `v1.1.0`.
- Existing ABI `1` and RPC schema `3` remain unchanged.

- [ ] **Step 1: Update version contracts**

Replace only semantic plugin version fields and expected artifact names. Do not
change ABI or RPC schema.

Touch only:

- `.github/workflows/release.yml`
- `Makefile`
- `deploy/rebuild/assemble-bundle.py`
- `deploy/rebuild/stage-release.sh`
- `deploy/rebuild/validate-bundle.py`
- `deploy/tests/build_contract_test.go`
- `deploy/tests/rebuild_contract_test.go`
- `deploy/tests/release_workflow_test.go`
- `deploy/tests/test_rebuild.py`
- `docs/compatibility.md`
- `internal/abi/envelope.go`
- `internal/abi/version_test.go`
- `test/abi-host/abi_test.go`
- `test/abi-host/main.go`
- `test/compat/compat_test.go`
- `tools/compat-probe/probe/types.go`
- `tools/update-verifier/verifier/approval.go`
- `tools/update-verifier/verifier/approval_test.go`

- [ ] **Step 2: Run full plugin verification**

```powershell
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe test -count=1 ./...
D:\Project\60.CLIProxyAPI\.tools\go\bin\go.exe vet ./...
python -m unittest discover -s panel/tests -v
python -m unittest deploy.tests.test_vps_reconciler -v
git diff --check
```

Expected: PASS.

- [ ] **Step 3: Run official panel build verification**

```powershell
python panel/build-panel.py --upstream-tag v1.22.6 --output dist-panel-v1.22.6
```

Verify checksum and no external assets.

- [ ] **Step 4: Run real CLI compatibility matrix**

Build `v1.1.0` artifacts and run compat probe against exact official
CLIProxyAPI `v7.2.133` and selected stable `v7.2.137`.

Expected reports:

```json
{"compatible":true,"code":"ok"}
```

- [ ] **Step 5: Request two-stage review**

Review separately:

1. spec/contract compliance;
2. code quality and security, especially bridge origin checks, no secret
   transfer, active-check isolation, manifest identity, and rollback.

- [ ] **Step 6: Commit final contract updates**

```powershell
git add .github/workflows/release.yml Makefile deploy/rebuild deploy/tests docs/compatibility.md internal/abi test tools
git commit -m "release: prepare Token Saver v1.1.0"
```

---

### Task 12: GitHub publication and VPS deployment

**Files:**
- No new source files after review.

**Interfaces:**
- Immutable releases and approved channel only.

- [ ] **Step 1: Merge reviewed branch**

Push branch, open PR, wait for CI, merge without bypassing required checks.

- [ ] **Step 2: Publish plugin `v1.1.0`**

Create annotated tag on merged commit, push tag, and wait for release workflow.
Verify artifact attestations and checksums.

- [ ] **Step 3: Publish patched panel**

Run `release-panel.yml`, confirm immutable
`panel-v1.22.6-bridge.1`, manifest identity, checksum, and attestation.

- [ ] **Step 4: Publish composite approval**

Run promotion workflow. Verify approved manifest contains exact CLI, plugin,
and panel SHA-256 values and a new fingerprint.

- [ ] **Step 5: Bootstrap reconciler safely**

Copy `deploy/vps-reconciler.py` to
`/root/cliproxyapi-updater.py.next`, run `python3 -m py_compile`, preserve the
old updater as `/root/cliproxyapi-updater.py.prev`, then atomically rename the
new file. Do not print configuration or credentials.

- [ ] **Step 6: Trigger updater**

```text
systemctl --user start cliproxyapi-updater.service
```

Verify updater result, active deployment path, service state, plugin SHA, panel
SHA, and timer state.

- [ ] **Step 7: Domain smoke**

Verify:

- management login succeeds with one manual attempt;
- plugin page dashboard bridge returns authenticated data;
- configured URL is correct;
- one button click performs one active check and reports real latency;
- four stage metrics refresh without the check changing counters;
- public fallback contains no URL or metrics;
- Nginx management 401/403 count does not increase during page polling;
- Headroom health remains reachable;
- no firewall changes.

- [ ] **Step 8: Future-update drill**

Run panel workflow against the same upstream tag and confirm duplicate
convergence. Run updater again and confirm “already latest approved”. Simulate
a panel hash mismatch in unit tests only; do not tamper with production.
