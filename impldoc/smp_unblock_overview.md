# SMP Unblock — Overview

**Read me first.** This doc set **extends** `impldoc/smp_migration_overview.md`; it does **not** supersede it. The 0.33.0 → 0.40.1 migration (branch `smp-take3`, commits `1de2050..2a1a13d`, pushed) landed M0 + M1 and deferred M2 / M3 / M4 / M5 with rationale in `TODO_SMP3.md §"Deferred further"`. This batch unblocks **M2, M3, M4** — enough to drive goroutine execution onto APs. M5 (GC stop-the-world via `gcPauseCore` IPI) remains deferred and is sized for a follow-on batch.

An implementation agent picking this up should read this file first, then follow the milestone chain in `impldoc/smp_unblock_milestones_and_verification.md`.

---

## 1. Purpose

The user reported on `smp-take3` (post-M1) that running `make run-smp` → `smpprobe` shows every worker goroutine on `cpuID=0`. That is the documented Wave 1 behaviour, not a bug: `stealWork()` is intentionally dormant (commit `d0cba8e`), APs idle in `waitForEvents`, and Ring-3 triple-faults on AP steal. The three milestones in this batch together make goroutines actually distribute across APs.

- **M2** — fix the AP LAPIC timer global-counter race so every CPU can run a 100 Hz periodic timer without `blocked inside interrupt` panics.
- **M3** — promote `src/target.json` from `scheduler=tasks` to `scheduler=cores` on the patched TinyGo 0.40.1 tree and wire the existing `stealWork()` helper into the scheduler's pop site.
- **M4** — QEMU + GDB investigation and fix for the AP Ring-3 `iretq` triple-fault that today blocks stealWork from being wired at all.

**Expected post-batch state.** `smpprobe` reports worker goroutines on ≥ 2 distinct cpuIDs. Full regression matrix green under `-smp 4`.

---

## 2. Document Set

| # | Document | Purpose |
|---|---|---|
| 1 | `impldoc/smp_unblock_overview.md` | **This file** — entry point, milestone map, risk register, reviewer findings. |
| 2 | `impldoc/smp_m4_ring3_fault.md` | M4 design: QEMU + GDB investigation playbook for the AP Ring-3 `iretq` triple-fault. |
| 3 | `impldoc/smp_m2_ap_lapic_timer.md` | M2 design: AP LAPIC timer race fix; three candidate strategies with Strategy A recommended. |
| 4 | `impldoc/smp_m3_cores_promotion.md` | M3 design: `scheduler=cores` promotion + `stealWork()` wire-up; commit-per-edit plan. |
| 5 | `impldoc/smp_unblock_milestones_and_verification.md` | Unified Entry/Exit gates, QEMU invocation matrix, harness extension list. |
| 6 | `impldoc/smp_unblock_readme_update_plan.md` | Grep-replace rules for `README.md`, `current_impl_doc/scheduler.md`, `impldoc/smp_deferred_and_known_issues.md`, `TODO_SMP3.md`. |

Suggested read order for someone executing: **2 (M4 background) → 3 (M2 background) → 4 (M3 design) → 5 (schedule) → 6 (closing edits) → 1 (reviewer findings)**. Read order for someone reviewing the design: **1 → 4 → 5**, then others as needed.

---

## 3. Milestone Dependency Map

```
   M2 (AP LAPIC timer)    M4 (Ring-3 iretq fault)
       │                        │
       │  (M2 ∥ M4 independent) │
       │                        ▼
       │                 M3 (scheduler=cores + stealWork wire-up)
       │                        │
       └────────────────────────┤
                                ▼
                      README + docs closing step
                      (impldoc/smp_unblock_readme_update_plan.md)
```

- **M2 ∥ M4.** Independent and can be worked in parallel. Either can land first.
- **M3 depends on M4** — primary path. Alternative: a kernel-only-affinity fallback (described in `impldoc/smp_m3_cores_promotion.md §2`) that lets M3 land before M4 with Ring-3 pinned to BSP. **Decision at M3 entry** based on M4 status; do not silently switch paths.
- **README + docs update** closes the batch; conditional rules in `impldoc/smp_unblock_readme_update_plan.md` select the right wording based on which of M2 / M3 / M4 actually landed.

---

## 4. Milestone Summary

| Milestone | Exit criterion (tightly summarized) | Owner doc |
|---|---|---|
| **M2** | Per-CPU ISR-depth counter (+ syscall-depth flag under Strategy A); global `gooos_in_interrupt_depth` retired; AP LAPIC timer at 100 Hz; no `blocked inside interrupt`; full regression matrix green under `-smp 4` | `smp_m2_ap_lapic_timer.md §6` |
| **M4** | `scripts/test_smp_ring3.sh` PASS (≥ 2 distinct cpuIDs); Ring-3 harnesses (`test_sendkey.sh 1`, `test_pipe_matrix.sh`) PASS under `-smp 4`; `impldoc/smp_deferred_and_known_issues.md §2.1` marked Resolved | `smp_m4_ring3_fault.md §6` |
| **M3** | `scripts/test_smp_basic.sh` PASS (≥ 2 distinct cpuIDs in probe); `smpprobe` reports workers on ≥ 2 cpuIDs; regression matrix green under `-smp 4`; `scheduler_cores.go` live with `stealWork()` wired | `smp_m3_cores_promotion.md §6` |

---

## 5. Critical Files to Read (not modify by this task)

gooos-side anchors referenced across the docs:

- `/home/ryo/work/gooos/TODO_SMP3.md` — deferred-item charter.
- `/home/ryo/work/gooos/CLAUDE.md` — workflow discipline.
- `/home/ryo/work/gooos/impldoc/smp_deferred_and_known_issues.md` — §2.1 (Ring-3 fault), §2.2 (LAPIC timer race), §5 (work-stealing dormancy row).
- `/home/ryo/work/gooos/impldoc/smp_migration_overview.md` + siblings — the 0.33.0 → 0.40.1 migration set this extends.
- `/home/ryo/work/gooos/impldoc/runtime_patches.md` — §3.3 / §3.8 / §3.10 (patch-hunk anchors).
- `/home/ryo/work/gooos/impldoc/tinygo_0_40_1_assessment.md` — §4.2 / §4.3 / §5.1 (upstream evidence the M3 patch must agree with).
- Kernel code: `src/percpu.go`, `src/smp.go`, `src/gdt.go`, `src/goroutine_tss.go`, `src/process.go`, `src/isr.S`, `src/userspace.go`, `src/goroutine_irq.go`, `src/main.go`, `src/stubs.S`.

TinyGo-side anchors (all under `~/.local/tinygo0.40.1/src/` unless otherwise noted):

- `runtime/scheduler_cooperative.go:176` `stealWork()` function (Wave 1 dormant); `:248-254` "intentionally not called" comment block.
- `runtime/scheduler_cores.go:37` `scheduleTask`; `:87` `Gosched`; `:253-258` `currentTask` / `setCurrentTask`; `:260-266` `lockScheduler/unlockScheduler`; `:268-277` `lockFutex/unlockFutex`; `:281-290` `lockAtomics/unlockAtomics`; `:292` `systemStack [numCPU]uintptr`; `:298-300` `systemStackPtr()`; `:306-327` `printlock/printunlock`.
- `runtime/runtime_rp2.go:295-298` `printLock / schedulerLock / atomicsLock / futexLock = spinLock{id: 20..23}` — the RP2 pattern gooos mirrors.
- `internal/task/task_stack_amd64.go:1` build tag (to widen); `:12-13` `runtime_systemStackPtr` linkname pattern (mirror of `task_stack_tinygoriscv.go:12-13`).
- `internal/task/queue.go` — Wave 1 already has gooos spinlock, reused unchanged.

---

## 6. Risk Register

Consolidated across the three milestone docs. Primary mitigations are in the per-milestone design; this table is the one-page at-a-glance.

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R-m4-cannot-localise | M4 QEMU+GDB session cannot localise the fault in budget | Medium | High | Two-day budget; fall back to kernel-only-affinity M3 with user approval (`smp_m3_cores_promotion.md §2`). |
| R-m2-trap-gate-audit | Strategy B (trap-gate syscall) requires auditing every syscall handler for interrupt-safety | Medium | High | Default to Strategy A (per-CPU counter + syscall-depth flag). |
| R-m2-strategy-c-tech-debt | Strategy C leaves `interrupt.In()` returning false, a footgun for future callers | Low | Medium | Only land C as an emergency escape; document in `impldoc/smp_deferred_and_known_issues.md §2.2`. |
| R-m3-systemstackptr-tasks | Upstream `scheduler_tasks.go` may not expose `systemStackPtr()` under tasks mode, breaking the `task_stack_amd64.go` widening | Medium | High | Verify at M3 entry via grep; pivot to dual-mode per-CPU array if absent. |
| R-m3-scheduler-cores-other-push | `scheduler_cores.go` has push sites beyond `:37`/`:87` that gooos misses | Low | High | M3 entry grep: `grep -n 'runqueue.Push' scheduler_cores.go`. |
| R-m3-cores-chan-direct | `chan.go` under cores mode has direct `runqueue.Push` calls bypassing `scheduleTask` | Low | Medium | M3 entry grep of chan.go. |
| R-atomicsLock-recursion | `atomicsLock` spinlock variable declarations re-trigger recursion | Low (design-eliminated) | High | M1 atomics smoke probe reused at M3 entry. |
| R-numcpu-ceiling | `numCPU = 17` hard-limits to 16 APs | Accepted | Low | Matches existing gooos ceiling. |

---

## 7. Design Decisions

Consolidated from the three milestone docs. Rationale lives in the referenced doc sections.

| # | Decision | Rationale | Source |
|---|---|---|---|
| D1 | M2 primary fix = Strategy A (per-CPU counter + syscall-depth flag) | Preserves `interrupt.In()` semantic; aligns with `smp_percpu_and_sync.md §6.2` | `smp_m2_ap_lapic_timer.md §3.4` |
| D2 | M3 depends on M4; fallback is kernel-only `stealWork` affinity | Enabling stealWork before M4 = guaranteed Ring-3 triple-fault | `smp_m3_cores_promotion.md §2` |
| D3 | M3 uses upstream `systemStackPtr()` via linkname import (mirrors `task_stack_tinygoriscv.go:12-13`) | Retires gooos's per-CPU systemStacks array in favour of upstream; less patch surface | `smp_m3_cores_promotion.md §4.2`, `tinygo_0_40_1_assessment.md §5.1` |
| D4 | `gcPauseCore` shipped as a stub at M3 (empty body); full IPI impl deferred to M5 | M5 orthogonal to M3 Exit; a partial-mark race during GC is tolerable short-term under single-probe testing | `smp_m3_cores_promotion.md §4.1` |
| D5 | Do NOT redefine upstream `lockFutex`/`unlockFutex`/`lockAtomics`/`unlockAtomics`/`lockScheduler`/`unlockScheduler` | Upstream scheduler_cores.go:260-290 defines them; gooos supplies only the variable bindings | `tinygo_0_40_1_assessment.md §4.3`, `smp_m3_cores_promotion.md §4.1` |
| D6 | README edits use grep-replace rules over line-number edits | Prior review flagged off-by-one citations from line-based edits | `smp_unblock_readme_update_plan.md §1` |
| D7 | Rollback = `git revert`, never `git reset --hard` without user approval | `CLAUDE.md` discipline | (all rollback sections) |

---

## 8. Open Questions (carried forward for implementation session)

1. **Does `scheduler_tasks.go` expose `systemStackPtr()` under tasks mode?** If not, commit #2 of M3 needs the fallback per R-m3-systemStackPtr-tasks. Verify at M3 entry: `grep -n 'func systemStackPtr' ~/.local/tinygo0.40.1/src/runtime/scheduler_tasks.go`.
2. **Are there `runqueue.Push` sites in scheduler_cores.go beyond `:37` and `:87`?** `grep -n 'runqueue.Push' scheduler_cores.go` at M3 entry.
3. **Is there a `runqueue.Push` direct call in chan.go?** `grep -n 'runqueue' chan.go` at M3 entry.
4. **What is the precise M4 root cause?** Unknown until the GDB session runs; hypothesis table in `smp_m4_ring3_fault.md §4` drives the investigation.
5. **Which M2 strategy if Strategy A has an unexpected regression?** Fallback order: A → B → C. Document whichever lands.

---

## 9. Constraints Inherited from gooos Workflow

Applied throughout (from `CLAUDE.md`, memory, and prior SMP practice):

- **No code changes in this planning task.** Documents only.
- **Plan first; no `git push`; no branch ops; no `master` merge without user order.** Commits of design docs are permitted (one `docs(smp): …` commit per doc, or one grouped commit).
- **One Bash command per invocation** (no `&&` / `;` / pipe chains in Bash tool calls).
- **Scratch under `tmp/`, never `/tmp`.**
- **Citation discipline**: verify each `path:line` with Grep before committing it.
- **Task tools** track progress of the documentation authoring, not the future implementation.

---

## Reviewer findings

`general-purpose` reviewer subagent ran against the first-cut of this 6-doc set on 2026-04-20. Classification: **0 CRITICAL-remaining, 2 CRITICAL-fixed, 5 MAJOR-fixed, 7 MINOR (mix of fixed + accepted)**. Summary:

### CRITICAL — resolved inline

1. **M4 "presumed fix" duplicated existing code.** The draft's pseudo-diff in `smp_m4_ring3_fault.md §5` instructed the agent to synthesise a fresh TSS descriptor with `type=0x9` — but `src/gdt.go:166-178` **already** does exactly that (`low |= uint64(0x89) << 40`). Fixed by deleting the pseudo-diff, downgrading hypothesis (b) to "unlikely — already built fresh", and promoting hypothesis (a) — `lgdtReload` / `ltr` sequencing at `src/gdt.go:197-201` — as the leading candidate.
2. **M4 GDB breakpoint on `.bss` data symbol.** The draft had `break *gooos_in_interrupt_depth` which is syntactically wrong for a data symbol (it would trap on the data byte's address). Replaced with a read-watchpoint `rwatch *(unsigned int*)gooos_in_interrupt_depth` plus a note that `break ring3Wrapper` alone is usually sufficient.

### MAJOR — resolved inline

1. **`gdtInitPerCPU` line-range off by ~25 lines.** Draft cited `src/gdt.go:148-177`; actual extent is `151-202`, with descriptor construction at `166-178` and `ltr(selectorTSS)` at `:201`. Fixed via `replace_all`.
2. **Strategy A's asm snippet was hand-wavy.** Spelled out the exact `cmpq $0x80, 120(%rsp); jne .Lnosys_enter; incl %gs:12; .Lnosys_enter:` pattern and the mirrored epilogue. §6.3 M2 probe promoted from Optional to **Required**.
3. **M3 overloaded the `cpuID` linkname binding.** Fixed by reusing the existing `gooosCpuID` import pattern from `task_stack_amd64.go:17-18` instead of declaring a second `cpuID` extern in `runtime_gooos.go`.
4. **M3's `scheduler_tasks.go systemStackPtr` open question resolved at write-time.** `grep -n 'func systemStackPtr' ~/.local/tinygo0.40.1/src/runtime/scheduler_tasks.go` confirms the accessor exists at `:11`. Risk R-systemStackPtr-tasks downgraded to "(resolved)" in the risk register; §4.2 and the milestones-and-verification Entry row updated accordingly.
5. **README-plan SMP-row variants incomplete.** Added two missing combinations: "M3 + M4 land, M2 deferred" (most likely) and "M3 lands with kernel-only-affinity fallback; M4 + M2 both deferred".

### MINOR — resolved or accepted

- Line-range narrowing in M3 / overview citations: `:260-290` → split into `:260-266` / `:268-277` / `:281-290` / `:306-327` per function.
- `src/isr.S:150-170` → `:152-168` in M2 §4 table (actual extent of the `.bss` block + comment).
- M4 §3.3 monitor-flag framing rewritten (gdbstub's `monitor` prefix already gives monitor access; telnet only needed for separate-shell queries).
- M4 §6.1 `test_smp_ring3.sh` PID race fixed: drop the outer `bash -c '… &'` wrapper so `$!` captures the QEMU PID directly; add `kill -0 "$QEMU_PID"` short-circuit to the poll loop.
- README-plan §5 `TODO_SMP3.md` tick rule now explicitly shows `~~` strike-through removal per bullet, not just the checkbox flip.
- "M1 atomicsLock-recursion smoke probe" reference clarified as a manual boot-log observation, not a dedicated harness.
- Doc-set reading-order slight circularity (overview §2 references future docs by filename) — accepted; cross-links are intentional and markdown doesn't enforce linear reads.
