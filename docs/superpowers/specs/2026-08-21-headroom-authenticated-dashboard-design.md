# Headroom authenticated dashboard and patched panel release design

Date: 2026-08-21

## Goal

Add three dashboard capabilities without exposing the CLIProxyAPI management
key or restoring the failed-login loop:

1. show configured Headroom URL, current connectivity, circuit state, last
   checked time, and server-measured latency;
2. run one explicit Headroom connectivity check from a button;
3. show RTK, Headroom, Caveman, and Ponytail execution counts and saved bytes.

Official Management Center updates must remain automatic. The bridge patch
must be rebuilt over each exact official release, tested, approved, and
deployed atomically instead of being edited on the VPS.

## Current constraints

- Plugin menu resources are served below `/v0/resource/plugins/...` without
  management authentication.
- Management Center renders each plugin resource in an iframe. Its Axios
  client owns the management key and adds `Authorization`; iframe requests do
  not inherit that client.
- The iframe must never read local storage, receive the management key, or
  issue direct `/v0/management/...` requests.
- Existing `POST /v0/management/plugins/token-saver/self-test` validates the
  fail-open pipeline. Headroom failure can still produce `result: passed`, and
  the test increments pipeline metrics. It is not a connectivity endpoint.
- Existing stage metrics already contain fixed, process-lifetime counters for
  executions, bypasses, failures, input bytes, output bytes, and duration.
- VPS updater currently hard-codes official panel `v1.22.6` and does not bind
  panel bytes into the approved manifest.

## Chosen architecture

Use a parent-mediated capability bridge plus dedicated authenticated plugin
routes.

The iframe sends a versioned request message. `PluginResourcePage` validates
the source window, exact iframe origin, current plugin ID, method, route
namespace, body size, and request ID. The parent calls the existing
authenticated `apiClient` and returns only the response body or a normalized
error. The management key never leaves the parent process and is never placed
in a message.

Because Management Center loads the iframe with
`referrerPolicy="no-referrer"`, the bridge starts with a credential-free
handshake. The child sends a `hello` message with no request data. The parent
validates the child source and origin, replies to that exact child origin, and
the child records the parent origin from the reply. Only then can management
requests begin. The initial `hello` is the only message allowed to use `*` as
its target because it contains no capability, credential, route, or payload.

The bridge is a small generic patch built over an exact official Management
Center release. It is not a Token Saver page fork and does not add a second
configuration UI.

## Message contract

Handshake:

```json
{
  "type": "cpa.plugin.management.hello",
  "version": 1,
  "pluginId": "token-saver"
}
```

```json
{
  "type": "cpa.plugin.management.ready",
  "version": 1,
  "pluginId": "token-saver"
}
```

Request:

```json
{
  "type": "cpa.plugin.management.request",
  "version": 1,
  "requestId": "opaque-random-id",
  "pluginId": "token-saver",
  "method": "GET",
  "path": "/v0/management/plugins/token-saver/dashboard",
  "body": null
}
```

Response:

```json
{
  "type": "cpa.plugin.management.response",
  "version": 1,
  "requestId": "opaque-random-id",
  "ok": true,
  "status": 200,
  "data": {}
}
```

Bridge rules:

- accept messages only from `iframe.contentWindow`;
- require `event.origin` to equal the resolved iframe URL origin;
- require a successful `hello` / `ready` handshake before accepting requests;
- require `pluginId` to equal the resource page plugin ID;
- allow only `GET` and `POST`;
- require path prefix `/v0/management/plugins/<pluginId>/`;
- reject credentials, custom headers, query forwarding, and bodies over 16 KiB;
- permit one in-flight request per iframe and cap each request with the normal
  Management Center API timeout;
- reply to the exact validated origin, never `*`;
- never serialize the management key, Axios configuration, or response
  headers.

Opening the resource URL outside Management Center therefore provides only
the existing public passive fallback.

## Plugin management API

### Passive dashboard

`GET /v0/management/plugins/token-saver/dashboard`

Returns authenticated, read-only data:

```json
{
  "started_at": "2026-08-21T00:00:00Z",
  "headroom": {
    "enabled": true,
    "url": "http://127.0.0.1:8787",
    "status": "ready",
    "circuit": "closed",
    "last_checked_at": "2026-08-21T00:00:00Z",
    "last_latency_ms": 12,
    "last_outcome": "applied"
  },
  "stages": {
    "rtk": {},
    "headroom": {},
    "caveman": {},
    "ponytail": {}
  }
}
```

Each stage contains:

- `executed`
- `bypassed`
- `fail_open`
- `timeout`
- `saturated`
- `input_bytes`
- `output_bytes`
- `saved_bytes`, calculated as `max(input_bytes - output_bytes, 0)`
- `duration_ns`

This route is passive. Polling it never calls Headroom or changes metrics.
Counters reset when the CLIProxyAPI process restarts; the UI labels this
clearly.

### Active connectivity check

`POST /v0/management/plugins/token-saver/headroom/check`

Behavior:

- reject disabled or invalid Headroom configuration with a stable result;
- perform one fresh functional request against the configured
  `/v1/compress` path;
- use the configured timeout, bounded to 100-1500 ms;
- make no retry;
- use an isolated health client so the check does not change production
  circuit state, last production outcome, or four-stage metrics;
- allow only one check in flight and impose a short server-side cooldown;
- record only low-cardinality outcome, timestamp, and latency in management
  memory.

The response distinguishes connectivity from pipeline fail-open behavior:

```json
{
  "reachable": true,
  "status": "ready",
  "outcome": "applied",
  "latency_ms": 12,
  "tested_at": "2026-08-21T00:00:00Z"
}
```

Existing `/self-test` behavior remains unchanged for updater and compatibility
contracts.

## Embedded page behavior

- On load, request authenticated dashboard data through the parent bridge.
- Poll passive dashboard data every 5 seconds.
- The button performs exactly one active check, then refreshes dashboard data.
- Show Headroom URL, green/red status, latency, last checked time, and circuit.
- Show four stage cards with execution count and human-readable saved bytes.
- If the bridge is absent or rejected, fall back to the current public passive
  endpoint, show “需要新版管理面板”, and disable active check and metrics.
- Never call `/v0/management/...` directly.
- Never read storage, URL credentials, parent variables, cookies, or headers.

## Patched official panel pipeline

No separate Management Center fork is required. The control-plane repository
`jinpeng2700-tech/cpa-plugin-token-saver` stores:

- one bridge patch against official Management Center source;
- a deterministic build script;
- bridge unit/static tests;
- a scheduled and manually runnable panel tracking workflow.

For each new stable official Management Center release:

1. resolve and lock exact upstream tag and commit;
2. fetch clean upstream source;
3. apply the single bridge patch;
4. run frozen Bun install, tests, lint, type check, and two clean single-file
   builds;
5. reject patch conflicts, non-reproducible output, external assets, or failed
   bridge security contracts;
6. attest and publish immutable tag
   `panel-v<upstream>-bridge.<revision>`.

Failure stops panel promotion. The current approved panel remains active.
CLIProxyAPI and Token Saver updates are not allowed to silently replace it
with an unpatched panel.

## Composite approval channel

Upgrade approved manifest schema to include a `panel` identity:

- upstream repository, release tag, and source commit;
- bridge patch revision and SHA-256;
- panel release repository and tag;
- `management.html` asset ID, size, SHA-256, and attestation identity.

The approved fingerprint includes CLI, plugin, and panel identities. A
panel-only release creates a new approved generation even when CLI and plugin
versions do not change.

Expected tag form:

`approved-cli-v<cli>-plugin-v<plugin>-panel-v<panel>-bridge.<revision>-g<n>`

## VPS reconciliation

Replace the hard-coded panel URL in `/root/cliproxyapi-updater.py`.

The reconciler:

1. accepts old schema during one transition and requires panel identity in the
   new schema;
2. downloads the exact approved panel asset;
3. verifies its SHA-256 before staging;
4. records the composite manifest in the deployment directory;
5. atomically switches the deployment symlink and restarts the service;
6. rolls back the complete CLI/plugin/panel set if service or smoke checks
   fail.

The existing persistent state directory remains untouched. No firewall or
management-key changes are part of this work.

## Tests and release gates

### Management Center

- reject wrong origin, wrong window, wrong plugin ID, unsafe method, unsafe
  path, oversized body, malformed request, and duplicate in-flight request;
- prove no response or message contains the management key;
- prove only the current plugin namespace reaches `apiClient`;
- verify clean upstream patch application and reproducible single-file build.

### Token Saver

- dashboard route is authenticated and passive;
- active check reports success, timeout, connection failure, disabled state,
  and cooldown;
- active check does not mutate stage metrics or production circuit;
- saved-byte projections never underflow;
- iframe page contains no direct management request or credential access;
- old panel fallback remains functional.

### Promotion and VPS

- approved schema rejects missing or mismatched panel identity;
- panel attestation and digest are verified before publication;
- updater rejects panel hash mismatch and leaves active deployment unchanged;
- official CLI compatibility probes continue for the fixed baseline and
  selected latest stable host;
- domain smoke verifies panel load, bridge dashboard GET, one active check,
  metrics refresh, no repeated 401/403, and rollback.

## Rollout order

1. Land plugin endpoints and fallback-capable page.
2. Publish patched panel artifact.
3. Extend promotion manifest and VPS reconciler.
4. Publish one composite approved generation.
5. Trigger updater manually once, verify, then leave existing timer enabled.

Because the page has a passive fallback, either plugin or panel may arrive
first without causing authentication retries or login bans.

## Non-goals

- no new Token Saver configuration panel;
- no management key inside iframe;
- no public active probe;
- no public metrics or configured URL;
- no persistent metrics database;
- no WebSocket/SSE metrics stream;
- no firewall changes.
