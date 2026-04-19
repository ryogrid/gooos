# Toolchain Switch Plan — `~/.local/tinygo` → `~/.local/tinygo0.40.1`

**Scope.** Exact edits required to switch the gooos build from the current patched TinyGo 0.33.0 at `$HOME/.local/tinygo` to patched TinyGo 0.40.1 at `$HOME/.local/tinygo0.40.1`. No code is modified in this document — only the implementation plan. Commit-per-file guidance is included so an implementation agent can land each edit as a self-contained change.

**Cross-links.**
- Verdict that justifies the switch: `impldoc/tinygo_0_40_1_assessment.md`
- Patch rebase that must land concurrently: `impldoc/runtime_patches.md`
- Milestone gates gating each edit: `impldoc/smp_milestones_and_verification.md`
- README changes driven by this switch: `impldoc/readme_update_plan.md`
- Rollback procedure: `impldoc/rollback_plan.md`
- Top-level index: `impldoc/smp_migration_overview.md`

---

## 1. Files to Edit

One table. Every entry carries the exact file, the exact line(s) today, and the target state after Wave 1 and (where relevant) Wave 2.

| # | File | Line(s) today | Wave 1 change | Wave 2 change |
|---|---|---|---|---|
| 1 | `Makefile` | 13 | `TINYGOROOT ?= $(HOME)/.local/tinygo` → `TINYGOROOT ?= $(HOME)/.local/tinygo0.40.1` | — |
| 2 | `Makefile` | 8–12 (comment) | Update comment block: mention 0.40.1 tree, drop "TODO.md Deferred" reference if no longer accurate | — |
| 3 | `scripts/patch_tinygo_runtime.sh` | 31 | `TINYGO_SRC="${TINYGO_SRC:-$HOME/.local/tinygo/src}"` → `"${TINYGO_SRC:-$HOME/.local/tinygo0.40.1/src}"` | — |
| 4 | `scripts/patch_tinygo_runtime.sh` | 57–69 | Update idempotency greps per Wave 1 post-conditions (`impldoc/runtime_patches.md §7`) — notably `SCHED=$TINYGO_SRC/runtime/scheduler_cooperative.go` | Add probes for cores-mode symbols (`numCPU = 17`, `atomicsLock`, `schedulerLock`, `futexLock`, `gcPauseCore`, `scheduler_cores.go` `runqueues`, `task_stack_amd64.go` build-tag widening) |
| 5 | `scripts/patch_tinygo_runtime.sh` | 96–100, 141–143 | Update file-list comments to reflect 0.40.1 paths (scheduler_cooperative.go in place of scheduler.go) | Add `wait_other.go` path verification branch per `impldoc/runtime_patches.md §3.12` |
| 6 | `scripts/patch_tinygo_runtime.sh` | 148–176 (trailing heredoc) | Update the "patch installs" list to reflect Wave 1 file set | Update for Wave 2 additions |
| 7 | `scripts/tinygo_runtime.patch` | entire file | Regenerate against 0.40.1 + `scheduler=tasks` per `impldoc/runtime_patches.md §6 Wave 1` | Regenerate against 0.40.1 + `scheduler=cores` per Wave 2 |
| 8 | `src/target.json` | 9 | `"scheduler": "tasks"` **unchanged** | Flip to `"scheduler": "cores"` |
| 9 | `src/target.json` | 5 | `"build-tags": ["gooos", "baremetal", "kernelspace"]` **unchanged** | Possibly add `"cores-smp"` or similar **only if** needed to disambiguate gooos-local code paths — decision deferred to M3 implementation |
| 10 | `user/target.json` | (current scheduler=tasks) | Leave unchanged | **Do not promote** userspace to cores mode until after M5; user binaries stay on `scheduler=tasks` |
| 11 | `README.md` | 173, 190–241, 252–259, 387, 400–403, 21 (progress row) | See `impldoc/readme_update_plan.md` for the dedicated plan | Further README updates per milestone progress |
| 12 | (optional) `scripts/setup_tinygo0_40_1.sh` NEW | — | Optional helper that clones `/usr/local/lib/tinygo0.40.1/` → `$HOME/.local/tinygo0.40.1/` if the 0.40.1 `.deb` ships elsewhere — decision deferred; today 0.40.1 is already at `$HOME/.local/tinygo0.40.1` per `impldoc/tinygo_0_40_1_assessment.md §7` | — |

---

## 2. Decision Points

### 2.1 Staged scheduler promotion (`tasks` → `cores`)

Captured in `impldoc/smp_scheduler_design.md §1.1` and the milestone doc. The toolchain switch itself (Makefile + patch script + patch file) lands at Wave 1 with **no change** to `"scheduler": "tasks"`. The scheduler flip happens later at M3, as a separate commit targeting `src/target.json:9`. **Do not combine the two in one PR** — they bisect separately.

### 2.2 Dual-version support during transition

`scripts/patch_tinygo_runtime.sh` must gracefully handle a developer who has both `$HOME/.local/tinygo` (0.33.0) and `$HOME/.local/tinygo0.40.1` installed. Logic:

```bash
if [[ -z "${TINYGO_SRC:-}" ]]; then
    if [[ -d "$HOME/.local/tinygo0.40.1/src" ]]; then
        TINYGO_SRC="$HOME/.local/tinygo0.40.1/src"
    elif [[ -d "$HOME/.local/tinygo/src" ]]; then
        TINYGO_SRC="$HOME/.local/tinygo/src"
        echo "warning: using deprecated 0.33.0 tree at $TINYGO_SRC" >&2
        echo "         upgrade to 0.40.1 per README.md (path changed)" >&2
    else
        echo "error: neither 0.40.1 nor legacy 0.33.0 TinyGo tree found" >&2
        exit 1
    fi
fi
```

**Makefile stays simple — no auto-fallback.** `TINYGOROOT ?= $(HOME)/.local/tinygo0.40.1` as a single default. A fallback via `$(shell test -d ... && echo ... || echo ...)` would tie build determinism to filesystem state and is rejected for reproducibility reasons. Contributors on the old tree either export `TINYGOROOT=$HOME/.local/tinygo` explicitly (legacy path) or install 0.40.1 at the new path. The `CLAUDE.md` "no compound shell commands" rule concerns Bash tool invocations, not Makefile recipe bodies; but applying it here anyway keeps the Makefile readable.

The patch script carries the dual-version detection (§2.2 above is the authoritative location) so developers running `bash scripts/patch_tinygo_runtime.sh` get the sensible default + deprecation warning. Drop the patch-script fallback branch after a transition period (e.g., after M3 lands).

### 2.3 `TINYGOROOT` override

The env var `TINYGOROOT` remains the canonical override. Contributors with custom installs export it; the default changes; behaviour for explicit-override users is unchanged.

### 2.4 Should the tree live at a version-neutral path?

E.g., `$HOME/.local/tinygo-patched` with a symlink from the versioned install. **Rejected** for this migration — adds cognitive overhead during a staged switch where we explicitly want to see both trees side-by-side while rebasing. Revisit after M5.

---

## 3. Commit-per-Edit Plan

Each of these is one git commit, landed in order.

1. `build(toolchain): point TINYGOROOT at ~/.local/tinygo0.40.1` — Makefile line 13 + comment refresh (lines 8–12).
2. `build(toolchain): patch script targets 0.40.1 tree` — `scripts/patch_tinygo_runtime.sh` line 31 + comment refresh + dual-version fallback per §2.2.
3. `build(toolchain): regenerate tinygo_runtime.patch for 0.40.1 (tasks mode)` — rerun patch-diff pipeline per `impldoc/runtime_patches.md §6`. Expected diff: similar shape, relocated scheduler.go → scheduler_cooperative.go hunks.
4. `build(toolchain): patch script post-conditions for 0.40.1` — update `SCHED` path and grep expectations in `scripts/patch_tinygo_runtime.sh` lines 57–69. Keep idempotency.
5. `docs(README): TinyGo 0.40.1 toolchain setup` — README.md edits per `impldoc/readme_update_plan.md §Wave 1` (only the toolchain paragraphs; SMP status row waits for M3).
6. **M0/M1 milestone gates run between here and next commit** — if gates fail, stop and re-plan per `impldoc/smp_milestones_and_verification.md §Gate failure`.
7. `build(target): flip scheduler to cores` — `src/target.json` line 9. This is the Wave 2 trigger.
8. `build(toolchain): Wave 2 patch additions (futex, gcPauseCore, numCPU)` — regenerate patch file; update post-conditions.
9. `docs(README): SMP multi-core scheduling status` — README.md progress row + known-limitations update per `impldoc/readme_update_plan.md §Wave 2`.

Commit subject style matches existing `git log --oneline` conventions (scope prefix in parens). All commits are local; no push without explicit user instruction.

---

## 4. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Mechanical patch rebase fails on `scheduler.go` → `scheduler_cooperative.go` relocation | Medium | High | Manual hunk-by-hunk apply; regenerate patch from clean `git diff` rather than porting unified diff text |
| Idempotency grep misses new symbol → patch re-applies and fails | Low | Medium | M0 smoke: apply patch twice in a row on a fresh tree; second run must say `already-applied:` |
| Contributor has neither tree installed | Low | High | Clear error message in patch script + README install section rewrite |
| `wait_other.go` relocated/removed in 0.40.1 | Medium | Low | M0 Entry check: verify file existence; drop hunk if gone (detail in `impldoc/runtime_patches.md §3.12`) |
| Build tag semantics changed in 0.40.1 (e.g., `baremetal`) | Low | High | Spot-check tags during Wave 1 rebase; align `src/target.json` accordingly |
| Dual-version fallback masks a legit "stale 0.33.0 tree" problem | Low | Medium | Deprecation warning emits to stderr on every 0.33.0 use; grep for it in logs |

---

## 5. Manual Verification (at each Wave)

Wave 1 verification (after step 5 above, before step 7):

```
make clean
bash scripts/patch_tinygo_runtime.sh    # expect success or already-applied
make build                              # expect clean
make verify-globals                     # expect clean
bash scripts/test_net.sh                # expect PASS (regression)
bash scripts/test_tcp_phase5.sh         # expect PASS (regression)
make run-smp                            # confirm boot to shell under -smp 4 (APs idle)
```

Wave 2 verification (after step 9):

All of Wave 1 plus:

```
bash scripts/test_smp_basic.sh          # NEW harness per impldoc/smp_milestones_and_verification.md
                                        # verifies goroutines run on >1 CPU under -smp 4
```

---

## 6. What This Document Does NOT Cover

- Detailed per-hunk rebase mechanics — `impldoc/runtime_patches.md`.
- Scheduler-design rationale for `tasks` → `cores` promotion — `impldoc/smp_scheduler_design.md §1`.
- README.md copy edits — `impldoc/readme_update_plan.md`.
- Rollback if Wave 1 or Wave 2 fails — `impldoc/rollback_plan.md`.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
