# Task 2 Report — U2

## Status

Implemented U2 from base commit `17c5fa9`.

- No push, tag, GitHub release, VPS change, network change, or UI change was performed.
- Release version remains the next stable identity `v1.0.1`; local and remote tag lookup returned no existing `v1.0.1`.
- U2 does not add provenance attestation. This avoids annotated-tag commit ambiguity and leaves attestation policy to the later approval/promotion unit.

## Delivered behavior

### Committed-source-only build

- `scripts/archive-source.sh` resolves a commit and emits only `git archive` content with `core.autocrlf=false`, so archive bytes do not depend on the developer checkout.
- `make release-container` extracts that archive into a disposable directory and uses the archived Dockerfile and archived source as the complete Docker build context.
- Dirty tracked files and untracked `compat-probe`, `update-verifier`, `token-saver.so`, `token-saver.h`, `source.tar.gz`, and Python `__pycache__` cannot enter the build context.
- `.gitignore` covers those local outputs; release upload lists exact files rather than a broad directory.

### Portable Linux amd64 artifacts

- Pinned builder combines `golang:1.26.5` toolchain with Debian Bullseye glibc `2.31`.
- Plugin builds with `CGO_ENABLED=1`, `GOOS=linux`, `GOARCH=amd64`, and `-buildmode=c-shared`.
- `compat-probe` and `update-verifier` build with `CGO_ENABLED=0`.
- `scripts/finalize-release.sh` rejects non-ELF64/non-amd64/wrong ELF types, helper `NEEDED` entries, missing GLIBC evidence, and plugin GLIBC requirements above `2.32`.

### Release artifact contract

Exact artifact set:

1. `token-saver-v1.0.1-linux-amd64.so`
2. `compat-probe-v1.0.1-linux-amd64`
3. `update-verifier-v1.0.1-linux-amd64`
4. `GLIBC_REQUIREMENTS.txt`
5. `release-metadata.json`
6. `SHA256SUMS`

`release-metadata.json` binds:

- version `1.0.1`
- tag `v1.0.1`
- full source commit
- platform `linux-amd64`
- ABI `1`
- RPC `3`
- observed maximum GLIBC requirement

`SHA256SUMS` covers the other five files.

### CI and release permissions

- All `uses:` entries are full 40-character commit SHAs.
- Existing reviewed pins remain:
  - `actions/checkout@11d5960a326750d5838078e36cf38b85af677262`
  - `actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff`
  - `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`
- CI and release build/compatibility jobs are read-only.
- Only final `publish` job has `contents: write`; it runs after compatibility, downloads the exact run-attempt artifact, verifies checksums/metadata, refuses an existing release, and uses `gh release create --verify-tag`.
- No workflow uses `gh release upload`, `--clobber`, `source.tar.gz`, broad `dist/` upload, or arm64 artifacts.

## TDD evidence

### RED 1 — missing clean release and publisher behavior

Command:

```text
docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test -count=1 ./deploy/tests
```

Expected failures:

- Makefile lacked `SOURCE_COMMIT`, committed-source archive, and release finalizer.
- `scripts/archive-source.sh` and `scripts/finalize-release.sh` did not exist.
- GLIBC `2.34` and dynamic-helper rejection behavior did not exist.
- Release workflow lacked final `publish` job.

### GREEN 1

Added minimum archive/finalizer/metadata/workflow behavior. `go test -count=1 ./deploy/tests` passed.

### RED 2 — compatibility accidentally placed after publication

New step-ownership assertion failed:

```text
job is missing "Prove real host dispatch on baseline and fixed v7.2.136" step
```

The test exposed YAML placement that put real dispatch under `publish`.

### GREEN 2

Moved real dispatch back into read-only `compatibility`; `publish` now contains no compatibility execution. Deploy tests passed.

### RED 3 — Dockerfile could come from working tree

New committed-Dockerfile contract failed:

```text
Makefile missing "tar -xf - -C"
Makefile missing "--file \"$$temporary/build/release.Dockerfile\""
release container must use Dockerfile extracted from the committed source archive
```

### GREEN 3

`release-container` now extracts the committed archive and builds with its Dockerfile and context. Deploy tests passed.

### RED 4 — Windows archive line endings broke the committed build

The first post-commit clean archive build failed inside Bullseye:

```text
make: scripts/finalize-release.sh: No such file or directory
```

Inspection proved Windows `core.autocrlf=true` had converted the archived shell script to CRLF. A new archive-content test configured `core.autocrlf=true` and failed with:

```text
committed source archive depends on local core.autocrlf
```

### GREEN 4

`scripts/archive-source.sh` now forces `core.autocrlf=false` for commit resolution and archive generation. The archive test passes with repository `core.autocrlf=true`.

## Verification evidence

### Baseline

- `docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./...` — pass before Task 2 changes.

### Full Go/ABI matrix

`make ci` passed in `golang:1.26.5-bookworm`, including:

- `gofmt`
- `go vet ./...`
- `go test ./...`
- `CGO_ENABLED=0 go test ./...`
- `go test -race ./...`
- both 10-second fuzz targets
- `GOEXPERIMENT=cgocheck2 CGO_ENABLED=1 go test -count=1 ./test/abi-host`
- dynamic ABI host stress, buffer ownership, shutdown, and race-sensitive tests

### Deployment fixtures

- `sh deploy/tests/test-deploy.sh` — pass.
- `sh deploy/tests/test-update-scripts.sh` — pass.
- `python deploy/tests/test_rebuild.py` — 3 tests pass.
- `python deploy/tests/test_pilot_fixtures.py` — pass.

### Real release build

Pinned Bullseye container build passed with:

- two consecutive builds from the same committed archive produced byte-identical six-file artifact sets
- `release-metadata.json` source commit matched the archived commit
- six exact artifacts
- checksum verification
- plugin ELF64/X86-64/DYN
- helper ELF64/X86-64/EXEC
- no helper `NEEDED` entries
- observed plugin GLIBC evidence `GLIBC_2.2.5`, `GLIBC_2.3.2`
- observed maximum GLIBC `2.3.2`, below ceiling `2.32`

### Real host dispatch

Current release artifacts passed plugin and core-only dispatch against:

- CLIProxyAPI `v7.2.133`
- CLIProxyAPI `v7.2.136`

Both hosts returned `compatible: true`; plugin dispatch reported version `1.0.1`, marker count `1`, non-zero config generation, config digest, and all seven required scenarios. `TestRealCandidateDispatch` and `TestRealCandidateCoreOnlyDispatch` also passed for both hosts.

## Files changed

- `.dockerignore`
- `.github/workflows/release.yml`
- `.gitignore`
- `Makefile`
- `build/release.Dockerfile`
- `scripts/archive-source.sh`
- `scripts/finalize-release.sh`
- `deploy/tests/build_contract_test.go`
- `deploy/tests/release_workflow_test.go`
- `docs/compatibility.md`
- this report

## Remaining publication work

Controller must create/push tag `v1.0.1` when authorized. This task intentionally did not push, tag, publish, or attest.
