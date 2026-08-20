# Task 1 Report — U1

## Scope

- Module, import, Makefile, and runtime metadata now use `github.com/jinpeng2700-tech/cpa-plugin-token-saver`.
- Generic Plugins UI schema remains nine fields; Caveman and Ponytail enum choices remain unchanged.
- Status and self-test routes remain unchanged and covered by existing management tests.
- Deleted `deploy/update-management-panel.sh`.
- Removed panel identity, hash checks, manifest fields, bundle input, verifier flag/input, wrapper input, release/update test fixtures, and runbook steps.
- Did not add, install, replace, or roll back `management.html`.
- Did not modify CLIProxyAPI core, Management Center, network, firewall, or remote state.

## TDD Evidence

1. Added `TestPluginRegistrationUsesUserRepositoryAndGenericUIFields`.
   - RED: failed because metadata still reported `https://github.com/router-for-me/cpa-plugin-token-saver`.
   - GREEN: passed after identity normalization.
2. Added `TestRebuildBundleOmitsDedicatedPanelArtifact`.
   - RED: `assemble-bundle.py` required `--panel-source-commit` and `--panel-builder-digest`.
   - GREEN: panel-free bundle assembles, validates, omits the manifest panel field, and contains no `static/management.html`.

## Verification

- `docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go test ./...` — pass.
- `docker run --rm -v "${PWD}:/src" -w /src golang:1.26.5-bookworm go vet ./...` — pass.
- `sh deploy/tests/test-deploy.sh` — pass.
- `sh deploy/tests/test-update-scripts.sh` — pass.
- `python deploy/tests/test_rebuild.py` — pass.
- Active source/delivery search found no old repository identity, panel manifest/hash, updater input, or `management.html` artifact dependency.

## Notes

- Local host has no `go` executable. Verification used existing Docker `golang:1.26.5-bookworm`.
- Test commands generated untracked `deploy/rebuild/__pycache__/` and `deploy/tests/__pycache__/`; they are not staged.
