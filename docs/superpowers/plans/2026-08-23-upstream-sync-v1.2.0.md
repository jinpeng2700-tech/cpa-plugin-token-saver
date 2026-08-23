# Upstream Sync v1.2.0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add auditable upstream snapshots, selectively sync Ponytail v4.9.0 behavior, and prepare Token Saver v1.2.0.

**Architecture:** Keep the current 9router-derived RTK and Caveman behavior, document exact upstream identities, and make a narrow Ponytail prompt update protected by semantic and size tests. Release identity changes remain mechanical and preserve ABI/RPC contracts.

**Tech Stack:** Go 1.26, Python 3 stdlib, Git, CodeGraph, Docker.

**Spec:** `docs/superpowers/specs/2026-08-23-upstream-sync-v1.2.0-design.md`

## Global Constraints

- Headroom code, client behavior, tests, and deployment version are untouched.
- RTK remains the same 12-filter Go port unless a reviewed v0.45.0 correctness fix directly applies.
- Caveman prompt behavior remains unchanged.
- Ponytail retains `lite`, `full`, and `ultra`, CPA markers, and a 2,048-byte maximum per prompt.
- Plugin version becomes `1.2.0`; ABI remains `1`; RPC schema remains `3`.
- No GitHub push, release, tag, or deployment.

---

### Task 1: Auditable upstream snapshot

**Files:**
- Create: `UPSTREAM_SNAPSHOTS.md`
- Modify: `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: one human-verifiable source-of-truth for future targeted syncs.

- [ ] **Step 1: Record exact snapshot identities**

Create `UPSTREAM_SNAPSHOTS.md` with the four reviewed repositories and these
exact commits:

```text
9router v0.5.55 699edac3273e13d4744bc46f6082618f08560702
RTK v0.45.0 b34be37caf3796b69a50952a28e60e32b5daad43
Caveman v2.2.0 9aa63945a349bef17206540650db48c30fafbdf2
Ponytail v4.9.0 0a4dd63ad4541f4f655c4108a295916f3c1d8fda
```

Record these exact Git objects and state the integration form and whether
code was synced, audited only, or externally deployed:

```text
9router open-sse/rtk/filters tree d65145390c8d4491c6536adf3c240f36157f8ec1
9router open-sse/rtk/autodetect.js blob 81992034350f6f95c1a08708073dd2077903f77d
9router open-sse/rtk/registry.js blob 5378aabd1f633c1c7416c0b433152d9343043235
9router open-sse/rtk/applyFilter.js blob 9de34ac8aae08fe1bd584bad3729cd8500a67ab7
9router open-sse/rtk/ponytailPrompt.js blob 1de20663e1b4e13816f5084236e1ad92d959e194
9router open-sse/rtk/cavemanPrompts.js blob 7b533d8254f5354a947271b417c9423fba509984
RTK src/cmds/system/pipe_cmd.rs blob 563d54a10f109c735d67a7a749d11bf3dd40ebdd
RTK src/core/filter.rs blob 4c712814e8d51e4213cd2c66da7869bc40233c49
RTK src/cmds/git/git.rs blob 9936a061d886d6eeba0be5faef22efddf8694f32
Caveman skills/caveman/SKILL.md blob bd22d86b32e4a99e09ff7482a35509faac7a6f65
Ponytail skills/ponytail/SKILL.md blob 02c0712c86277d49d18a77da3a2b825657bf02d1
Ponytail AGENTS.md blob bc3595d1a07cff2b135139cadfa4c17ec412667b
```

- [ ] **Step 2: Update notices narrowly**

Update RTK, Caveman, and Ponytail entries with reviewed versions and the
verification date `2026-08-23`. State that Headroom is an external unmodified
service whose deployed version is managed separately.

- [ ] **Step 3: Verify provenance objects**

Run `git cat-file -e <object>` in each local upstream clone for every recorded
object ID. Run `git diff --check`.

- [ ] **Step 4: Commit**

```text
docs: record upstream snapshots
```

---

### Task 2: Ponytail v4.9.0 behavior sync

**Files:**
- Modify: `internal/prompt/inject_test.go`
- Modify: `internal/prompt/ponytail.go`

**Interfaces:**
- Preserves: `func Ponytail(level string) (string, bool)`.
- Adds no new configuration level or injection marker.

- [ ] **Step 1: Write the failing semantic test**

Add this behavior test for every Ponytail level:

```go
func TestPonytailTracksOfficial490Rules(t *testing.T) {
	for _, level := range []string{"lite", "full", "ultra"} {
		prompt, ok := Ponytail(level)
		if !ok {
			t.Fatalf("Ponytail(%q) is missing", level)
		}
		for _, want := range []string{
			"Already in this codebase?",
			"trace the real flow end to end",
			"Bug fix = root cause, not symptom",
			"Ponytail governs what you build, not how you talk",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("Ponytail(%q) missing %q", level, want)
			}
		}
		if len([]byte(prompt)) > 2048 {
			t.Errorf("Ponytail(%q) = %d bytes, want <= 2048", level, len([]byte(prompt)))
		}
	}
}
```

- [ ] **Step 2: Verify RED**

Run in Go 1.26 Docker:

```text
go test -count=1 ./internal/prompt
```

Expected: failure for the four missing v4.9.0 rules.

- [ ] **Step 3: Implement the smallest prompt change**

Update the shared Ponytail ladder/boundary text once so all levels inherit the
four rules. Do not copy the full upstream skill and do not change Caveman.

- [ ] **Step 4: Update exact local prompt snapshots**

Rename the old 9router-only snapshot test to reflect mixed provenance and
update only the three Ponytail byte lengths and SHA-256 values. Preserve all
six Caveman values unchanged.

- [ ] **Step 5: Verify GREEN**

Run `go test -count=1 ./internal/prompt ./internal/saver` in Go 1.26 Docker.
Expected: PASS.

- [ ] **Step 6: Commit**

```text
feat(prompt): sync Ponytail v4.9.0 rules
```

---

### Task 3: Token Saver v1.2.0 release identity

**Files:**
- Modify every tracked plugin-version occurrence found by
  `rg -n --hidden "1\\.1\\.1|v1\\.1\\.1"`, excluding the dependency
  `github.com/tidwall/match v1.1.1` in `go.mod` and `go.sum`.

**Interfaces:**
- Plugin release identity: `1.2.0` / `v1.2.0`.
- ABI: `1` unchanged.
- RPC schema: `3` unchanged.

- [ ] **Step 1: Update release contracts mechanically**

Replace plugin version fields, artifact names, release tag checks, test
fixtures, and compatibility documentation. Do not alter dependency versions,
CLIProxyAPI pins, panel pins, ABI, or RPC schema.

- [ ] **Step 2: Verify no stale plugin identity remains**

Run the same `rg` command and require only the tidwall dependency matches.

- [ ] **Step 3: Run full verification**

In a temporary Linux copy with `.sh` files normalized to LF, run:

```text
go test -count=1 ./...
go vet ./...
```

Then run:

```text
python -m unittest discover -s deploy/tests -p "test_*.py" -v
git diff --check
codegraph sync .
codegraph status .
```

- [ ] **Step 4: Commit**

```text
release: prepare Token Saver v1.2.0
```
