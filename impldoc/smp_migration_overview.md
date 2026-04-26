# gooos SMP Migration — TinyGo 0.33.0 → 0.40.1

**Read me first.** This is the entry point to the migration design set. An implementation agent picking up this work should read this file first, then follow the milestone chain in `impldoc/smp_milestones_and_verification.md`. The design **extends** the existing SMP v2 docs (`impldoc/smp_overview.md`, `impldoc/smp_kernel_scheduler.md`, `impldoc/smp_ap_safety_overview.md`, etc.) — it does **not** replace them. Existing blockers tracked in `pasttodos/TODO_SMP2.md` and `impldoc/smp_deferred_and_known_issues.md` remain authoritative for root-cause diagnosis.

---

## 1. Purpose

Move gooos off patched TinyGo 0.33.0 and onto patched TinyGo 0.40.1 so that:

1. Kernel goroutines can be scheduled across every CPU (the gate-stopper from `pasttodos/TODO_SMP2.md`).
2. The gooos-side TinyGo patch shrinks by ~35 % as upstream's `scheduler.cores` mode absorbs functionality gooos currently carries locally.
3. Future upstream improvements (GC stop-the-world via `gcPauseCore`, multi-core futex / mutex) land in gooos by routine resync instead of as bespoke work.

**Not in this migration:** fixing the Ring-3 `iretq` triple-fault on APs or the AP LAPIC timer global-counter race. Both are kernel-side bugs orthogonal to TinyGo; they have milestone slots (M2, M4) but do not block the toolchain switch itself.

---

## 2. Verdict

**SUITABLE-WITH-PATCHES** per `impldoc/tinygo_0_40_1_assessment.md §0`.

Upstream 0.40.1 ships `scheduler.cores` (per-CPU task storage, `NumCPU`, cores-mode `lockAtomics`, multi-core GC stack scanning). It does **not** ship an x86_64 bare-metal target — gooos creates one (the project already does; this migration just reshapes it). gooos's existing 853-line patch rebase carries forward, shedding hunks that duplicate upstream functionality and adding small linkname bodies (`lockFutex`, `gcPauseCore`, `currentCPU`, `numCPU = 17`) to connect upstream hooks to gooos's per-CPU kernel infrastructure.

---

## 3. Document Set

| # | Document | Purpose |
|---|---|---|
| 1 | `impldoc/smp_migration_overview.md` | This file — entry point, verdict, milestone map, risk register |
| 2 | `impldoc/tinygo_0_40_1_assessment.md` | Evidence-based verdict with file:line citations across `../tinygo` and `~/.local/tinygo0.40.1` |
| 3 | `impldoc/toolchain_switch_plan.md` | Makefile / patch script / target.json edits with commit-per-edit plan |
| 4 | `impldoc/runtime_patches.md` | Per-file rebase plan for `scripts/tinygo_runtime.patch` (Wave 1 tasks-mode + Wave 2 cores-mode) |
| 5 | `impldoc/smp_scheduler_design.md` | How 0.40.1 `scheduler.cores` composes with gooos's existing SMP v2 per-CPU infra |
| 6 | `impldoc/smp_milestones_and_verification.md` | M0–M5 milestones with Entry/Exit gates, affected files, verification commands |
| 7 | `impldoc/rollback_plan.md` | Revert procedure for each wave |
| 8 | `impldoc/readme_update_plan.md` | `README.md` edits (Wave 1 toolchain + Wave 2 scheduler row) |

Read order for someone executing: **2 → 3 → 4 → 5 → 6 → 7 → 8**. Read order for someone reviewing the design: **1 → 2 → 5 → 6**, then others as needed.

---

## 4. Staged Promotion at a Glance

```
Commit 1  [build] TINYGOROOT → 0.40.1            ┐
Commit 2  [build] patch script targets 0.40.1     │ Wave 1
Commit 3  [build] regenerate patch (tasks mode)   │ (M0 → M1)
Commit 4  [build] patch script post-conditions    │
Commit 5  [docs]  README toolchain section        ┘
  ─ M0 Exit gate: single-core parity ────────────────────
  ─ M1 Exit gate: -smp 4 boots, APs idle, atomics probe OK ─
  (M2 parallel branch: fix AP LAPIC timer race)
Commit 7  [build] src/target.json: scheduler=cores ┐
Commit 8  [build] Wave 2 patch additions           │ Wave 2
Commit 9  [docs]  README scheduler + SMP row       ┘ (M3)
  ─ M3 Exit gate: kernel goroutines on multiple CPUs ─
  (M4: fix Ring-3 iretq triple-fault on AP)
  (M5: gcPauseCore IPI + GC stop-the-world)
```

Details: `impldoc/toolchain_switch_plan.md §3` for commits; `impldoc/smp_milestones_and_verification.md` for gates.

---

## 5. Milestone Summary

| Milestone | Exit criterion (tightly summarized) | Owner doc |
|---|---|---|
| **M0** | `test_tcp_phase{1..5}.sh` + `test_net.sh` PASS on 0.40.1 + `scheduler=tasks`, `-smp 1` | `smp_milestones_and_verification.md §M0` |
| **M1** | `-smp 4` boots to shell; APs idle; atomicsLock recursion probe prints OK | `§M1` |
| **M2** | AP LAPIC timer at 100 Hz; no "blocked inside interrupt" panics | `§M2` |
| **M3** | `scripts/test_smp_basic.sh` PASS on ≥2 distinct cpuIDs under `scheduler=cores` | `§M3` |
| **M4** | Existing Ring-3 harnesses PASS under `-smp 4`; `smpprobe.elf` shows Ring-3 on APs | `§M4` |
| **M5** | `scripts/test_smp_gc_stress.sh` PASS; no heap corruption under allocation stress | `§M5` |

---

## 6. Critical Files (read on each milestone)

gooos-side (do not modify during documentation task; implementation session will):

- `/home/ryo/work/gooos/Makefile` — `TINYGOROOT` at line 13.
- `/home/ryo/work/gooos/src/target.json` — `"scheduler"` at line 9.
- `/home/ryo/work/gooos/scripts/tinygo_runtime.patch` — 853 lines, 12 file regions.
- `/home/ryo/work/gooos/scripts/patch_tinygo_runtime.sh` — idempotent apply script.
- `/home/ryo/work/gooos/README.md` — canonical touch-point list lives in `impldoc/readme_update_plan.md`; do not re-enumerate line numbers here.
- `/home/ryo/work/gooos/pasttodos/TODO_SMP2.md` — blocker list.
- `/home/ryo/work/gooos/impldoc/smp_overview.md` and siblings — existing SMP v2 design.

TinyGo-side (read-only reference during design):

- `/home/ryo/work/tinygo/src/runtime/scheduler_cores.go` — upstream cores scheduler.
- `/home/ryo/work/tinygo/src/runtime/scheduler_cooperative.go` — upstream tasks-mode scheduler (relocation target for Wave 1).
- `/home/ryo/work/tinygo/src/runtime/gc_stack_cores.go` — multi-core GC scan.
- `/home/ryo/work/tinygo/src/runtime/atomics_critical.go` — bare-metal atomic ops.
- `/home/ryo/work/tinygo/src/internal/task/futex-cores.go`, `mutex-preemptive.go`, `queue.go`, `task_stack_amd64.go`.
- `/home/ryo/.local/tinygo0.40.1/` — installed toolchain (same files; spot-check only).

---

## 7. Risk Register

Consolidates risks called out across the document set.

| ID | Risk | Likelihood | Impact | Owner mitigation |
|---|---|---|---|---|
| R-fork-divergence | Upstream 0.40.x evolves; gooos patch drifts | Medium | High | Staged promotion keeps tasks-mode bisect point; regenerate patch from clean `git diff` |
| R-atomicslock-recursion | `atomicsLock.Lock()` re-enters `lockAtomics` → BSP hang | Eliminated (by design) | — | Declare runtime locks as gooos-local `spinLock` type, not `task.Mutex` — mirrors `runtime_rp2.go:293-299` pattern. See `smp_scheduler_design.md §4.4` |
| R-scheduler-file-relocation | `scheduler.go` hunk rebase onto `scheduler_cooperative.go`/`scheduler_cores.go` non-trivial | Medium | High | Manual hunk-by-hunk apply; regenerate patch rather than porting diff text |
| R-wait-other-removed | `wait_other.go` may not exist in 0.40.1 | Medium | Low | M0 Entry check; drop hunk if gone |
| R-ap-gc-concurrent-mutation | AP runs goroutines during GC mark phase | Medium | High | M5: `gcPauseCore` IPI + per-CPU pause ack |
| R-ap-ring3-triple-fault | AP `iretq` triple-faults on Ring-3 entry (orthogonal kernel bug) | High (unchanged from today) | High | M4: QEMU+GDB debug; migration does not introduce or fix |
| R-ap-lapic-timer-race | Global `gooos_in_interrupt_depth` races under SMP (orthogonal kernel bug) | High (unchanged) | Medium | M2: migrate `interrupt.In()` to per-CPU only |
| R-numcpu-ceiling | `const numCPU = 17` rules out >16 APs | Low | Low | Matches existing gooos ceiling; bump requires coordinated patch |
| R-dual-version-confusion | Developers with both 0.33.0 and 0.40.1 trees installed | Medium | Medium | Patch script fallback with deprecation warning (`toolchain_switch_plan.md §2.2`) |
| R-user-mode-unpromoted | `user/target.json` stays on `scheduler=tasks`; inconsistent with kernel | Accepted | Low | Deliberate: user-mode cores promotion post-M5 |

---

## 8. Design Decisions

Consolidated; detailed rationale in the referenced docs.

| # | Decision | Rationale | Source |
|---|---|---|---|
| D1 | Two waves (tasks-mode rebase → cores promotion) | Bisectability; each wave isolates one failure class | `runtime_patches.md §2`, `smp_scheduler_design.md §1.1` |
| D2 | `numCPU = 17` | Aligns with existing `maxCPUs` across gooos per-CPU arrays | `smp_scheduler_design.md §3` |
| D3 | Keep gooos `runqueues[numCPU]task.Queue` + `stealWork` as local patch on `scheduler_cores.go` | Upstream cores has `cpuTasks` (single pointer) but no per-CPU Queue; work-stealing is gooos-specific | `smp_scheduler_design.md §2`, `runtime_patches.md §3.10` |
| D4 | Retain explicit Queue spinlock under `scheduler=tasks` | Upstream `lockAtomics` in tasks mode is per-CPU only, insufficient for SMP | `smp_scheduler_design.md §4.3`, `runtime_patches.md §3.1` |
| D5 | Declare runtime-lock variables (`atomicsLock`, `schedulerLock`, `futexLock`, `printLock`) in `runtime_gooos.go` using gooos-local `spinLock` type. Supply only the `gcPauseCore` and `currentCPU` bodies. **Do not** redefine `lockFutex` / `unlockFutex` / `lockAtomics` — upstream defines them in `scheduler_cores.go:260-290` | Mirror of `runtime_rp2.go` pattern; avoids duplicate-symbol link error | `runtime_patches.md §3.8` |
| D6 | Dual-version fallback in `patch_tinygo_runtime.sh` | Smooth transition for in-flight branches | `toolchain_switch_plan.md §2.2` |
| D7 | Userspace stays on `scheduler=tasks` through M5 | Kernel-side cores is enough to unblock SMP goroutines; userspace promotion is future work | `runtime_patches.md §3.9` |
| D8 | `chan.go` per-CPU routing hunk retained through Wave 2 | gooos uses its own `runqueues` array, not upstream's `cpuTasks` slot | `smp_scheduler_design.md §8`, `runtime_patches.md §3.4` |
| D9 | README Wave 1 edits land with toolchain commit (not deferred) | Keep README truth-consistent with build behaviour at every commit | `readme_update_plan.md §Wave 1` |
| D10 | Rollback via `git revert`, never `git reset --hard` without user approval | Preserves history; `CLAUDE.md` discipline | `rollback_plan.md §2, §6` |

---

## 9. Open Questions (carried forward for implementation session)

1. **Exact LLVM version shipped with 0.40.1 `.deb`** — need for README edit 1 at `README.md:173`. Capture at M0 install.
2. **Path of the system-wide 0.40.1 install** (is it `/usr/local/lib/tinygo0.40.1/` or elsewhere?) — needed for README edit 2. Capture at M0 install.
3. **`wait_other.go` existence in 0.40.1** — M0 Entry gate per `runtime_patches.md §3.12`.
4. **atomicsLock recursion** — validate at M1 probe per `smp_scheduler_design.md §4.4`.
5. **Whether to retire gooos Queue spinlock in Wave 2** — opt-in simplification, not required for correctness. Decide after M3 Exit.
6. **Whether to promote user-mode to `scheduler=cores`** — explicitly deferred post-M5.
7. **IPI vector for `vectorGCPause`** — suggest `0xFB`, confirm no collision with existing `vectorWakeup = 0xFC` and lint-scan vector table at M5 start.

---

## 10. Constraints Inherited from Gooos Workflow

Applied throughout the migration session (from `CLAUDE.md`, memory, and pre-existing conventions):

- **Plan-first; no code until user approves the plan.** This doc set *is* the plan for the next session.
- **No merge to `master` / `main` without explicit user instruction.** No `git push`. No branch creation.
- **Scratch files under `tmp/`, not `/tmp`.**
- **One Bash command per invocation** — no `&&` / `;` / pipe chains in implementation scripts.
- **Never skip hooks (`--no-verify`, `--no-gpg-sign`).**
- **After ANY user correction, update `tasks/lessons.md`.**
- **Task tools track progress of the work** (not this documentation — this is the plan, not the execution).

---

## 11. If Verdict Had Been UNSUITABLE

(Historical note.) If the assessment had found 0.40.1 unworkable for x86_64 bare-metal SMP, the deliverable would instead be `impldoc/smp_alternatives.md` covering:
- Stay on 0.33.0 indefinitely; accept maintenance burden.
- Back-port only `scheduler_cores.go` and `gc_stack_cores.go` onto the 0.33.0 patch tree without upgrading the toolchain.
- Skip to TinyGo 0.41+ if upstream x86_64 cores support lands.

The actual verdict is SUITABLE-WITH-PATCHES, so the alternatives doc is not produced. If future evidence flips the verdict (per the falsifiability conditions in `impldoc/tinygo_0_40_1_assessment.md §0`), the alternatives doc becomes the next design deliverable.

---

## Reviewer findings

Reviewer pass completed by `general-purpose` subagent. Classification and resolution:

### CRITICAL — resolved inline

1. **`lockFutex`/`unlockFutex` already defined upstream.** Docs initially instructed gooos to redefine these as new function bodies. Upstream `scheduler_cores.go:268-277` already defines them; gooos must supply only the variable `futexLock` (and siblings `atomicsLock`, `schedulerLock`, `printLock`). Fixed in `tinygo_0_40_1_assessment.md §4.3`, `smp_scheduler_design.md §2` table + `§10`, `runtime_patches.md §3.8`, `smp_migration_overview.md §8 D5`.
2. **Per-CPU `systemStack` exists upstream under cores mode.** `scheduler_cores.go:292` declares `var systemStack [numCPU]uintptr` with accessor `systemStackPtr()`. Docs initially said "no per-CPU systemStacks in upstream". Corrected: Wave 1 keeps gooos patch; Wave 2 retires it in favour of upstream `systemStackPtr()` consumed via linkname (mirror of `task_stack_tinygoriscv.go:12-13`). Fixed in `tinygo_0_40_1_assessment.md §5.1`, `runtime_patches.md §3.3`, `smp_scheduler_design.md §2` table.
3. **Cores mode has single global `runqueue`, not per-CPU.** Docs implied upstream already routes per-CPU. Reality: `scheduler_cores.go:26` has one global `runqueue`; `scheduleTask` (line 43) and `Gosched` (line 89) push to it under `schedulerLock`. Gooos's per-CPU routing patch must retarget those push sites. Fixed in `smp_scheduler_design.md §8.1`, `runtime_patches.md §3.10` (enumerated push-site table added).

### MAJOR — resolved inline

1. **`atomicsLock` design vs. probe.** Upstream RP2 declares the runtime locks as `spinLock`, not `task.Mutex` — the recursion concern is design-eliminated, not runtime-validated. `smp_scheduler_design.md §4.4` rewritten to recommend the spinlock declaration as the primary design. M1 probe kept as smoke test only.
2. **README line drift after first edit.** `readme_update_plan.md` header now states line numbers are pre-migration and recommends grep-replace over absolute addressing for subsequent edits.
3. **Wave 1 commit-count reconciliation.** `smp_milestones_and_verification.md §M0 Entry` updated from "commits 1–4" to "commits 1–5" to match `toolchain_switch_plan.md §3`.
4. **Makefile `$(shell ...)` fallback rejected.** `toolchain_switch_plan.md §2.2` now explicitly keeps the Makefile single-default. Dual-version detection lives only in the patch script.

### MINOR — resolved or accepted

- README touch-point enumeration consolidated — this doc's §6 no longer duplicates `readme_update_plan.md`'s list.
- "Line numbers approximate" hedging removed from `tinygo_0_40_1_assessment.md §4.1` (exact lines confirmed).
- D10 reference in `smp_scheduler_design.md §3.2` / overview §8 — `smp_overview.md` decision D10 confirmed to exist (`smp_overview.md §3`); no change.
- Placeholder "Reviewer MINOR notes" sections in individual docs — left in place as future-pass slots; populated in this overview for the current pass.
