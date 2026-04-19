# Rollback Plan — TinyGo 0.40.1 Migration

**Scope.** Revert procedure for each migration wave. Two independent rollback paths: Wave 1 (toolchain swap + tasks-mode rebase) and Wave 2 (cores-mode promotion). Either wave can be rolled back without disturbing the other.

**Cross-links.**
- Wave structure: `impldoc/toolchain_switch_plan.md §2.1`
- Commits to revert: `impldoc/toolchain_switch_plan.md §3`
- Milestone Exit criteria that trigger rollback: `impldoc/smp_milestones_and_verification.md §Gate Failure Protocol`
- Top-level index: `impldoc/smp_migration_overview.md`

---

## 1. When to Roll Back

Per `impldoc/smp_milestones_and_verification.md`, each milestone's Exit criteria specifies the trigger. Summary:

| Milestone | Rollback when |
|---|---|
| M0 | Any of `test_tcp_phase{1..5}.sh`, `test_net.sh` regress vs. pre-migration baseline; or patch apply fails and cannot be rebased in reasonable time |
| M1 | `-smp 4` boot hang / triple-fault / atomicsLock recursion |
| M2 | AP LAPIC timer migration introduces "blocked inside interrupt" panics that cannot be fixed in session |
| M3 | Kernel-goroutine distribution probe fails; regression matrix breaks under `-smp 4` |
| M4 | (not a migration rollback — orthogonal kernel bug work; leave at M3 state if M4 cannot be resolved) |
| M5 | GC stress causes corruption; revert `gcPauseCore` enablement only — earlier milestones remain |

---

## 2. Pre-Rollback Hygiene

Before executing any rollback:

1. Capture the failing serial log to `tmp/rollback_M{N}_{timestamp}.log`.
2. `git log --oneline master..HEAD` — record the range being reverted.
3. `git diff master HEAD -- scripts/tinygo_runtime.patch` — record the patch delta for forensic analysis.
4. Note the exact failure signature (panic string, triple-fault RIP, hang location) in the corresponding milestone section of `impldoc/smp_milestones_and_verification.md` under a new "Observed failure" subsection.

**Do not `git reset --hard`** — destructive; hides work. Use `git revert` for surgical backouts or create a rollback branch.

---

## 3. Wave 1 Rollback (toolchain swap + tasks-mode rebase)

Scope: the commits from `impldoc/toolchain_switch_plan.md §3 commits 1–5`.

### 3.1 Git-side rollback

```
git log --oneline                               # identify the Wave 1 commit range
git revert <commit1> <commit2> ... <commit5>    # one revert commit per original
                                                # OR:
git revert --no-commit <commit1>..<commit5>
git commit -m "revert(toolchain): drop TinyGo 0.40.1 migration Wave 1"
```

Chose individual reverts when bisecting is still desired. Choose squashed revert when the wave is being fully abandoned.

### 3.2 Filesystem-side rollback

Restore the 0.33.0 tree. Two scenarios:

**Scenario A: `$HOME/.local/tinygo` was never deleted.** Nothing to do filesystem-side — the post-revert Makefile points back at `$HOME/.local/tinygo`. The patch script operates on the 0.33.0 tree as it did pre-migration. Re-run:

```
bash scripts/patch_tinygo_runtime.sh            # against .local/tinygo, 0.33.0 patch
make clean
make build                                       # expect: clean
```

**Scenario B: `$HOME/.local/tinygo` was deleted/corrupted during migration.** Recreate it:

```
rm -rf $HOME/.local/tinygo
mkdir -p $HOME/.local/tinygo
cp -a /usr/local/lib/tinygo/. $HOME/.local/tinygo/   # system TinyGo 0.33.0
bash scripts/patch_tinygo_runtime.sh                  # apply 0.33.0 patch
make clean
make build
```

The 0.40.1 tree at `$HOME/.local/tinygo0.40.1/` can be left in place — it is harmless if `TINYGOROOT` points elsewhere.

### 3.3 Verification post-rollback

```
make build                                       # clean
make verify-globals                              # clean
bash scripts/test_net.sh                         # PASS
bash scripts/test_tcp_phase5.sh                  # PASS
make run-smp                                     # boot to shell, APs idle (pre-migration behaviour)
```

---

## 4. Wave 2 Rollback (cores-mode promotion)

Scope: the commits from `impldoc/toolchain_switch_plan.md §3 commits 7–9`.

### 4.1 Git-side rollback

```
git revert <commit7> <commit8> <commit9>
```

Or squashed:

```
git revert --no-commit <commit7>..<commit9>
git commit -m "revert(toolchain): drop scheduler=cores promotion (Wave 2)"
```

Wave 1 remains in place — the project stays on 0.40.1 + `scheduler=tasks`. Only the cores promotion backs out.

### 4.2 Filesystem-side rollback

`scripts/tinygo_runtime.patch` reverts to its Wave-1 state (the revert commit handles this). Re-apply:

```
bash scripts/patch_tinygo_runtime.sh            # apply Wave-1 patch to .local/tinygo0.40.1
```

The idempotency check should detect cores-mode remnants as "not applied" and re-apply cleanly. If `.rej` files appear, clean tree and re-mirror from `/usr/local/lib/tinygo0.40.1/`:

```
rm -rf $HOME/.local/tinygo0.40.1
cp -a /usr/local/lib/tinygo0.40.1/. $HOME/.local/tinygo0.40.1/   # (if system install exists)
bash scripts/patch_tinygo_runtime.sh
```

If no system-wide 0.40.1 install exists, restore from the TinyGo 0.40.1 `.deb` or source build per current installation guidance (path to be added to README.md when migration lands).

### 4.3 Verification post-rollback

Same as M1 Exit gate:

```
make build                                       # clean
make run-smp                                     # boot to shell under -smp 4, APs idle
bash scripts/test_tcp_phase5.sh                  # PASS
```

---

## 5. Full Rollback (both waves)

Execute §3 (Wave 1 rollback). That implicitly covers Wave 2 because the Wave 2 commits depend on Wave 1; reverting Wave 1 alone would leave the tree in an inconsistent state.

**Explicit sequence** when abandoning the entire migration:

```
# 1. Identify both ranges
git log --oneline master..HEAD

# 2. Revert in reverse order (Wave 2 first, then Wave 1)
git revert --no-commit <wave2 range>
git revert --no-commit <wave1 range>
git commit -m "revert(toolchain): abandon TinyGo 0.40.1 migration"

# 3. Ensure 0.33.0 tree is the active toolchain
bash scripts/patch_tinygo_runtime.sh            # applies 0.33.0 patch to .local/tinygo

# 4. Clean build + regression
make clean
make build
bash scripts/test_tcp_phase5.sh
```

---

## 6. Branch Strategy (when in doubt)

Prefer `git revert` commits on the same branch for traceability. If a rollback is exploratory (trying something else next) and you want to preserve the migration work visibly:

```
git branch save/tinygo-0-40-1-attempt-YYYYMMDD
git reset --hard <pre-migration-commit>          # only with explicit user approval
```

**Never reset without user approval** (per `CLAUDE.md`). The branch-save pattern is also acceptable as: land the reverts, then `git log` still shows the history.

---

## 7. README.md Rollback

If `impldoc/readme_update_plan.md` Wave-1 edits already landed, the revert commit should include their reversal. Keep README truth-consistent with actual build behaviour; never leave README.md claiming 0.40.1 while the Makefile points at 0.33.0.

---

## 8. What Does NOT Need Rolling Back

- `hoge.md` (prompt file) — unrelated, not checked into build pipeline.
- `impldoc/` documents authored for this migration — they are historical record; leave them in place with a prominent "ABANDONED" / "SUPERSEDED" banner at the top of `impldoc/smp_migration_overview.md`.
- Existing SMP v2 `impldoc/smp_*.md` — unchanged by the migration; unaffected by rollback.

---

## 9. Post-Rollback Analysis

After any rollback, add a short post-mortem to `pasttodos/` naming:
- What failed.
- Why (root cause if known; open questions if not).
- What would need to change in the migration plan for a second attempt.

File suggestion: `pasttodos/TINYGO_0_40_1_ATTEMPT_{YYYYMMDD}.md`.

This builds the institutional memory the next attempt needs.

---

## 10. Emergency Kernel-Only Rollback

If the migration is mid-wave and the tree is in an inconsistent state (e.g., patch regen failed partway) — recovery without git revert:

```
# Discard all migration-related in-progress changes
git stash                                        # preserve in-progress edits
git checkout -- Makefile scripts/ src/target.json

# Restore 0.33.0 toolchain
bash scripts/patch_tinygo_runtime.sh            # applies 0.33.0 patch

# Verify
make build
```

This preserves the in-progress `git stash` for later inspection. User approval required before `git reset --hard` on anything.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
