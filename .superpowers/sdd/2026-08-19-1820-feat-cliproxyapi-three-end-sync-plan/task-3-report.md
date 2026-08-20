# Task 3 Report — U3 Automatic official-release promotion

Date: 2026-08-20
Status: COMPLETE

## Delivered

- Added scheduled/manual serialized promotion workflow.
- Enumerates releases; never uses `/releases/latest`.
- Selects highest valid stable SemVer official/plugin releases.
- Locks official/plugin asset tag, release ID, asset ID, name, size, API digest, calculated SHA-256, official checksum, plugin metadata source commit, and attestation identity before compatibility execution.
- Runs exact plugin dispatch/config generation+digest/status/self-test matrix plus core-only proof.
- Generates strict approved manifest with canonical fingerprint, monotonic channel generation, predecessor fingerprint, official/plugin identity, and attestation identity.
- Duplicate candidate fingerprint converges without new generation or duplicate release.
- Keeps write permissions in final publish job only; actions use full commit SHAs.
- Added strict JSON Schema, trust policy, channel genesis, parser/generator fixtures, and workflow contract tests.

## TDD Record

### RED 1 — control-plane files and permission structure

Command:

```text
go test ./deploy/tests -run 'TestPromotion(ControlPlaneFilesExist|WorkflowIsScheduledManualSerializedAndReadOnlyBeforePublish)' -count=1
```

Observed: failed because `promote-cliproxyapi.yml`, `channel.json`, schema, and trust policy did not exist.

### GREEN 1

Same command passed after minimal trigger/concurrency/permission skeleton and files were added.

### RED 2 — release selection

Command:

```text
go test ./deploy/tests -run 'TestPromotionSelection' -count=1
```

Observed: failed with `not implemented`.

### GREEN 2

Same command passed after strict stable SemVer ordering, exact asset selection, digest requirements, unstable input rejection, and downgrade rejection were added.

### RED 3 — strict manifest contract

Command:

```text
go test ./deploy/tests -run 'TestApprovedManifest' -count=1
```

Observed: failed with unimplemented parser and empty schema.

### GREEN 3

Same command passed after duplicate-key detection, unknown-field rejection, trailing-data rejection, identity/range/name validation, canonical fingerprinting, JSON Schema, trust policy, and channel genesis were added.

### RED 4 — byte locking, lineage, compatibility failure, workflow chain

Command:

```text
go test ./deploy/tests -run 'TestPromotion(LocksBytesBeforeCompatibility|GenerationIsMonotonicAndDuplicateConverges|CompatibilityFailureProducesNoManifest|WorkflowLocksExactAssetsRunsGateAndPublishesImmutably)' -count=1
```

Observed: failed with unimplemented locking/generation and missing `discover` job.

### GREEN 4

Focused tests passed after shared Go promotion helper and complete workflow were added. One ordering assertion initially checked a step label instead of executable command and failed; assertion was corrected, then suite passed.

## Final Focused Verification

```text
go test ./deploy/tests -count=1
ok github.com/jinpeng2700-tech/cpa-plugin-token-saver/deploy/tests

go test ./test/compat -count=1
ok github.com/jinpeng2700-tech/cpa-plugin-token-saver/test/compat
```

Existing `test/compat/compat_test.go` real-candidate tests were reused unchanged.

## Scope / Safety

- Changed only Task 3 files and this report.
- No push, tag, release, workflow dispatch, VPS/UI/network operation, or subagent use.
- Live GitHub workflow execution and live attestation lookup were intentionally not performed.
