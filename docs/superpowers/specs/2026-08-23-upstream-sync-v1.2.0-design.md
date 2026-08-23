# Upstream sync v1.2.0 design

Date: 2026-08-23

## Goal

Make the plugin's RTK, Caveman, and Ponytail provenance auditable, selectively
adopt the useful Ponytail v4.9.0 behavior, and prepare Token Saver v1.2.0.

## Scope

- Record exact repositories, tags, commits, source paths, and Git object IDs.
- Keep the existing 12-filter Go RTK port unless RTK v0.45.0 exposes a directly
  applicable correctness fix.
- Keep the current Caveman prompt faces; record the v2.2.0 comparison.
- Add the Ponytail v4.9.0 rules that prevent duplicate code and shallow fixes:
  reuse existing code, understand and trace the real flow first, and fix root
  causes across callers.
- Keep all three Ponytail levels and the current CPA injection markers.
- Keep Ponytail prompts below 2,048 UTF-8 bytes per level.
- Bump plugin release identity from 1.1.1 to 1.2.0 without changing ABI 1 or
  RPC schema 3.

## Headroom boundary

Headroom is deployed as an unmodified external service. The plugin only calls
its API. This change does not modify Headroom code, client behavior, tests, or
deployment version; the snapshot document states that its version is managed
by the deployment.

## Upstream decisions

- Integration base: 9router v0.5.55 at
  `699edac3273e13d4744bc46f6082618f08560702`.
- RTK review target: v0.45.0 at
  `b34be37caf3796b69a50952a28e60e32b5daad43`.
- Caveman review target: v2.2.0 at
  `9aa63945a349bef17206540650db48c30fafbdf2`.
- Ponytail sync target: v4.9.0 at
  `0a4dd63ad4541f4f655c4108a295916f3c1d8fda`.

RTK v0.45.0 is a much larger executable with command-specific filters. Its
overlap with this plugin is limited to conservative pipe filtering. The plugin
already includes UTF-8-safe detection and Windows path handling, so no RTK
code change is required by the reviewed release.

Caveman v2.2.0 primarily expands the complete product and skill surface. The
plugin intentionally keeps its six compact 9router prompt faces; blindly
replacing them would raise prompt cost and change response policy.

## Verification

- A failing semantic test proves the Ponytail v4.9.0 rules are absent before
  implementation and present afterward.
- Existing exact prompt snapshots are updated only after the semantic test is
  green.
- Targeted prompt tests and the full Go suite pass in Go 1.26.
- Python rebuild tests, `git diff --check`, and CodeGraph sync pass.
- A final reviewer checks provenance accuracy, scope, and release identity.

## Non-goals

- No Headroom compatibility or deployment work.
- No RTK executable embedding or new filters.
- No Caveman engine/runtime integration.
- No GitHub push, release, tag, or VPS deployment.
