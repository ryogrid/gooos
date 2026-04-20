# Preempt + Shell Enhancements — Overview

> **Read me first.** This doc is the entry point for an implementation-session agent working on gooos preemption + shell enhancements. Read this before touching code. Reading order:
>
> 1. This doc.
> 2. `preempt_shell_milestones_and_verification.md` — batch-wide Entry/Exit gates.
> 3. Per-feature designs in dependency order: 2.1 → 2.2 → 2.3 → 2.4 → 2.5.
> 4. `preempt_shell_readme_update_plan.md` — closing doc drift.
>
> This doc set **extends** the SMP unblock batch (`impldoc/smp_unblock_*`), it does not replace anything. All background claims (scheduler=cores live; stealWork wired; AP LAPIC timer deferred) are as of `smp-take4` HEAD (branch head at authoring: commit `dc58dbc`).

## 1. Purpose

The TinyGo 0.33.0 → 0.40.1 SMP unblock batch (landed via commits `aa5bb91 … dc58dbc`) put gooos kernel goroutines on multiple CPUs. Two classes of follow-on work remained uncaptured:

- **Preemption**. Kernel and user goroutines today yield only at cooperative points. A `for {}` can monopolize a core.
- **Shell usability**. No `&` for background execution; no `ps` for process visibility.

This batch specs both so a future implementation-agent session can execute without re-deriving context.

## 2. Document Set

| Doc | Feature | Scope |
| --- | --- | --- |
| `preempt_shell_overview.md` (this file) | — | Entry point + Design Decisions + dependency DAG. |
| `preempt_kernel_goroutines.md` | 2.1 | Kernel goroutine preemption via BSP timer + IPI broadcast. |
| `preempt_user_goroutines.md` | 2.2 | User goroutine preemption via kernel-delivered SIGALRM-style signal. |
| `shell_multicore_preempt.md` | 2.3 | Integration verification — 3 sub-gates. |
| `shell_background_jobs.md` | 2.4 | `&` parser + `sys_waitpid` #34. |
| `shell_ps_command.md` | 2.5 | `ps` command + `sys_listprocs` #36. |
| `preempt_shell_milestones_and_verification.md` | — | Unified gates, QEMU matrix, harness list. |
| `preempt_shell_readme_update_plan.md` | — | Anchor-text edits for README + `current_impl_doc/` + trackers. |

## 3. Milestone Dependency DAG

```
          +----------------+
          | 2.1 kernel     |
          | preemption     |
          +----------------+
           /        |     \
          v         v      v
  +----------+  +-------+  +-----------+
  | 2.3 a|b  |  | 2.2   |  | 2.4 &     |
  | shell    |  | user  |  | + waitpid |
  | multi-   |  | pree. |  +-----------+
  | core     |  +-------+
  +----------+      |
                    v
              +-------------+
              | 2.3 c       |
              | intra-proc  |
              | fairness    |
              +-------------+

     +------+      +-----------+
     | 2.4  |----->| 2.3 b     | (2.3 test script uses `&` if available;
     +------+      +-----------+  has non-`&` fallback in §3.2 of 2.3 doc)

  +----------+
  | 2.5 ps   |  INDEPENDENT
  +----------+
```

Precise edges:

- **2.1** — no dependency on other features in this batch. Entry: `smp-take4` + M4 fix in history.
- **2.2** — weakly depends on 2.1 (composes cleanly; if 2.1 is deferred, 2.2 can still ship with an independent gate; see 2.2 §7 commit 8).
- **2.3 (a) distribution** — depends on nothing new; already satisfied by `aa5bb91`.
- **2.3 (b) anti-starvation** — depends on 2.1 landed.
- **2.3 (c) intra-process fairness** — depends on 2.2 landed.
- **2.3 harness using `&`** — depends on 2.4; has a non-`&` fallback documented.
- **2.4** — independent.
- **2.5** — independent.

## 4. Design Decisions (load-bearing, user-confirmed)

Resolved via AskUserQuestion during plan-mode on 2026-04-20. Documented verbatim so the implementation agent need not re-derive:

| # | Feature | Decision | Consequence |
| --- | --- | --- | --- |
| 1 | 2.1 | **BSP timer + IPI broadcast**. Keep the BSP-only 100 Hz LAPIC timer; preempt IPI broadcast on each tick. | APs preempt only at IPI arrival boundaries (not per-CPU timer ticks). Does not re-open the `blocked inside interrupt` regression from `impldoc/smp_deferred_and_known_issues.md §2.2`. AP LAPIC timer stays stubbed. |
| 2 | 2.2 | **Mechanism B (kernel-delivered SIGALRM-style signal)**. Kernel rewrites the iretq frame to redirect user RIP to a runtime-registered handler. | Adds `sys_sigaction` #35 + `sys_sigreturn` #36. Touches the iretq path — coordinates with M4 fix. Mechanism A (user scheduler=cores) rejected; mechanism C (syscall-return check) rejected. |
| 3 | 2.4 | **New dedicated syscall #34 `sys_waitpid`** with `(pid, options, status*)` signature. | Existing `sys_wait` (#16) untouched. `sys_waitpid` handler does NOT call `setForegroundProc`, preserving foreground-transfer invariant for background jobs. |
| 4 | 2.5 | **New syscall #37 `sys_listprocs`**. | Follows #34 (2.4), #35 (2.2 sigaction), #36 (2.2 sigreturn). Reviewer CRITICAL #1 resolution: the three features claim four distinct numbers; no #36 collision. |

Non-load-bearing choices the design-doc author picked and documented:

- Pipeline + background semantics = **whole pipeline** goes to background (POSIX), not just the last stage. Documented in 2.4 §2.2.
- `sys_listprocs` max-procs cap = **32**, matching `maxRing3Procs`. Documented in 2.5 §2.1.
- `current_impl_doc/scheduler.md` update tactic = **inline extend the SMP v2 subsections** (post-`dc58dbc` structure), not a new top-level section. Documented in 2.1's landing path via `preempt_shell_readme_update_plan.md §3.1`.
- `hoge.md` retained as scratch; not committed.

## 5. Critical Files (read before editing — do not modify during this task)

**Kernel source** (cited with line anchors throughout the per-feature docs):
- `src/lapic_timer.go:69-80` — BSP tick handler.
- `src/ipi.go:13,33-55` — IPI vector 0xFC + wakeup handler + `gooosWakeupCPU`.
- `src/isr.S:88-149` — full 15-GPR save + `InterruptDepth`/`SyscallDepth` counters.
- `src/percpu.go:22-33,36-46` — PerCPU struct + assembly-visible offsets.
- `src/smp.go:257-273` — disabled AP LAPIC init.
- `src/stubs.S:437-459` — spinlock primitives.
- `src/process.go:32-64,68,235,354` — Process struct, procLock, elfSpawn, processWait.
- `src/userspace.go:47-85,95,697-751` — syscall table, dispatch, sysSpawn/sysWait handlers.
- `user/cmd/sh/parse.go:5-125` + `user/cmd/sh/main.go:13-233` — complete shell source.
- `user/gooos/proc.go:5-100` + `user/gooos/syscall.go:31-54` — SDK.
- `user/Makefile:21` — CMDS list.
- `user/target.json` — `scheduler=tasks`.

**Patched TinyGo** (under `~/.local/tinygo0.40.1/src/`):
- `runtime/scheduler_cores.go:131-220` — stealWork + scheduler loop.
- `runtime/runtime_gooos.go:228-249` — schedulerWake IPI broadcast.
- `runtime/runtime_gooos_user.go` — user-runtime patch surface.
- `runtime/interrupt/interrupt_gooos.go:45-47` — `interrupt.In()`.
- `internal/task/task_stack_amd64.go:21-30` — calleeSavedRegs.
- `internal/task/queue.go` — `//go:noescape` spinlock declarations (the GC-hang fix).

**Prior batch context** (not modified, but cross-referenced):
- `impldoc/smp_unblock_overview.md` — the batch this one extends.
- `impldoc/smp_deferred_and_known_issues.md` — §2.2 (AP LAPIC) is live context for 2.1.
- `impldoc/smp_m4_ring3_fault.md` — commit `5aea173` fix; 2.2 interacts with the iretq path.
- `impldoc/runtime_patches.md` — 2.2 extends rather than duplicates this doc.

**Doc conventions**:
- `impldoc/smp_m3_cores_promotion.md` — template for per-feature docs.
- `impldoc/smp_unblock_readme_update_plan.md` — template for the update-plan doc.
- `TODO_SMP3.md` / `TODO_SMP4.md` — tracker-file schema.

## 6. Risk Register (consolidated)

| # | Risk | Feature | Severity | Mitigation / check |
| --- | --- | --- | --- | --- |
| R1 | Preempt-inside-spinlock re-entrancy resurrects GC-hang class | 2.1 | CRITICAL | Audit every spinlock callsite; reviewer bullet (m). |
| R2 | iretq-frame-rewrite race with timer during syscall return | 2.2 | CRITICAL | Rewrite under `interrupt.Disable`. Reviewer bullet (c). |
| R3 | `sys_waitpid` accidentally calls `setForegroundProc` | 2.4 | MAJOR | Code review; reviewer bullet (g). |
| R4 | `ProcInfo` struct-layout drift kernel↔user | 2.5 | MAJOR | Single-source-of-truth in `shell_ps_command.md §2.1`; build-time `unsafe.Sizeof == 64`. |
| R5 | 2.2 signal handler corrupts user stack on mis-use (e.g. longjmp) | 2.2 | MINOR | Documented as user-responsibility; out of scope. |
| R6 | AP LAPIC timer fix accidentally enabled | 2.1 | MINOR | Explicit `## Future` section; reviewer audit of `src/smp.go:273`. |
| R7 | `sys_waitpid` option-bit drift (reserved bits silently succeeding) | 2.4 | MINOR | Reject nonzero reserved bits; reviewer verifies test case. |
| R8 | Background-job leak on shell `exit` | 2.4 | MINOR | Documented; out of scope for this batch. |
| R9 | Nested signal delivery blows user stack | 2.2 | MINOR | `SigInProgress` flag in PCB; early-return in `maybeDeliverSignal`. |

## 7. Open Questions (non-load-bearing — design-author picks)

These are documented so the implementation agent knows what was *chosen* (not where to diverge):

- **Preempt quantum.** 2.1 uses 1 tick = 10 ms. If measurement reveals this is too aggressive or too coarse, quantum changes in commit 5 (the trigger wire-up) — not here.
- **User preempt quantum.** 2.2 uses 10 ticks = 100 ms per `UserQuantumTicks` in the PCB. Configurable per-process via `sys_sigaction` flags extension in a future batch.
- **Reserved signal numbers.** Only `SIGALRM = 14` defined in 2.2. Future batches may add SIGINT / SIGTERM etc. — schema is already future-proof.
- **Jobs table display format.** `[id] pid done exit=N cmd` per 2.4. Follows bash precedent, not strict POSIX.
- **`ps` output columns.** 6 columns per 2.5 (`PID PPID STATE CPU TICKS NAME`). No -e/-u/-f flags.

## 8. Constraints (batch-wide)

- No `git push`, no branch ops, no master merges without explicit user instruction.
- One Bash command per invocation (no compound shell).
- Scratch files under `tmp/`, never `/tmp`.
- Every commit: `docs(impldoc): …` subject per doc for this planning pass; `feat(subsys): …` / `test(subsys): …` for implementation commits in the next session (matches the `b481473 docs(smp): refresh` / `aa5bb91 fix(smp): wire stealWork` precedents).
- Citation discipline: every `path:line` cited here was grep-verified at authoring time (2026-04-20). If an anchor has drifted, update the doc in a separate `docs(impldoc): refresh citations …` commit before continuing.
- Subagents used liberally; reviewer pass is mandatory.

## 9. Stop Conditions (implementation-agent guidance)

The implementation agent opening this doc set to execute the batch should **pause and surface** to the user if any of the following occurs:

- A cited `path:line` no longer matches the expected symbol after a drift-inducing commit elsewhere on the branch.
- 2.2's mechanism B iretq-frame rewrite conflicts with M4's landed fix in a way not anticipated by 2.2 §12.
- `sys_sigaction` ABI decisions require Ring-3 handler-stack design exceeding what one doc can cover.
- The reviewer pass raises a CRITICAL that requires design-level (not doc-level) rework.
- Any existing regression harness (`test_net.sh`, `test_tcp_phase{1..5}.sh`, etc.) stops PASSing mid-batch.
- QEMU triple-faults / panics after a feature commit.
- `scripts/patch_tinygo_runtime.sh` fails to re-apply idempotently on a freshly-unpacked TinyGo tree.

## 10. Commit Cadence

This doc set was authored in 8 `docs(impldoc): …` commits — one per file, on branch `smp-take4`. Implementation work that *consumes* this doc set is tracked separately in `TODO_SMP5.md` (see `preempt_shell_readme_update_plan.md §6.2`) and will land as `feat(subsys): …` / `test(subsys): …` / `docs(readme): …` commits in a follow-on session.

No push, no branch ops, no merges. `git log --oneline master..HEAD` at end of this authoring session shows 8 + 1 (reviewer-findings) `docs(impldoc)` commits.

## 11. Deliverables (this authoring session)

- 8 docs under `impldoc/` — confirmed by `ls impldoc/preempt_*` / `ls impldoc/shell_*`.
- 1 consolidated reviewer pass with findings classified CRITICAL / MAJOR / MINOR.
- CRITICAL + MAJOR folded inline; MINOR recorded per-doc in `Reviewer MINOR notes` tails and consolidated in §12 below.
- Final summary turn to the user.

## 12. Reviewer findings

Mandatory reviewer pass completed 2026-04-20 by a `general-purpose` subagent brief per hoge.md §6 + added bullet (m). Findings: **4 CRITICAL, 9 MAJOR, 10 MINOR**. All CRITICAL and 6 of 9 MAJOR folded inline during the review-fold commit. Cross-feature invariant (bullet m) verdict: **GAP FOUND**, now closed by runtime-spinlock integration Option A in `preempt_kernel_goroutines.md §2.3`.

**Classification legend.**
- CRITICAL — technical error or missing invariant that would cause the implementation to fail or diverge from the design decisions.
- MAJOR — Claude-Code-implementability gap (missing path/line, missing ABI, missing rollback) that forces the implementation agent to re-derive context.
- MINOR — doc hygiene, typo, phrasing, cross-link polish.

**CRITICAL findings + resolutions (all folded inline):**

1. **Syscall #36 double-claim** (overview §4 + `preempt_user_goroutines.md §3.2` + `shell_ps_command.md §2.4`). Resolution: `sys_listprocs` moved from #36 to **#37**; `sys_sigaction` keeps #35; `sys_sigreturn` keeps #36. Fixed in overview §4 (new row 4), `shell_ps_command.md §1/§2.4/§3.1/§5/§6`, `preempt_shell_readme_update_plan.md §2.5`.
2. **`PreemptDisable` offset 56 is wrong** (`preempt_kernel_goroutines.md §2.3`). Real fields occupy 0..47; `_pad[16]` covers 48..63. Resolution: placed at **offset 48**, pad trimmed to 12 bytes. Fixed in `preempt_kernel_goroutines.md §2.3`.
3. **`swapTask`-emitted `iretq` would triple-fault** (`preempt_kernel_goroutines.md §2.2`). Resolution: separate `resumePreempted` assembly helper in new `task_stack_preempt_amd64.S`; `state.resume()` branches on `kind` discriminator. Fixed in `preempt_kernel_goroutines.md §2.2`.
4. **`maybeDeliverSignal` contradictory call sites** (`preempt_user_goroutines.md §2 vs §6`). Resolution: **syscall-return only**; `jumpToRing3` is explicitly excluded. Fixed in `preempt_user_goroutines.md §2 point 2 + §6`.

**MAJOR findings + resolutions:**

1. Runtime-side spinlock gap (bullet m) — folded: Option A at `preempt_kernel_goroutines.md §2.3` (asm-level `PreemptDisable` bump inside `gooos_spinlockAcquire`/`Release` themselves).
2. Nosplit-RIP table TinyGo-compiler hand-wave — folded: approximation "treat all kernel code as nosplit-unsafe while `SyscallDepth>0`" (4th ISR early-return condition).
3. `pushU64Through` helper missing spec — folded: full spec + page-boundary handling at `preempt_user_goroutines.md §4.2`.
4. `SigInProgress` missing from PCB — folded: 5th signal field at `preempt_user_goroutines.md §3.3`.
5. `sys_waitpid` deadlock-prone double-reap — folded: simplified to WNOHANG-only; `procByPID[child.pid] == child` race guard; blocking fallback removed.
6. `gooosCurrentProc` linkname confusion — folded: update lives in kernel-side `gooosOnResume` body (`src/goroutine_tss.go`), no new runtime linkname. `shell_ps_command.md §2.3/§6`.
7. Overview row 2 Consequence cell — folded: new row 4 added for sys_listprocs #37; row 2 wording preserved.
8. `writeU32Through`/`writeStructThrough` not defined — folded: cross-referenced to `preempt_user_goroutines.md §4.2` spec.
9. Harness-list `-smp 1` for `test_smp_shell_preempt.sh` — verified already listed; no edit needed.

**MINOR findings recorded for implementation-session awareness** (not blocking; not all folded here; each per-doc `Reviewer MINOR notes` tail captures its own):

- Readme-update-plan Variant A (§2.5) syscall numbers updated post-CRITICAL-#1 fold.
- Overview §5 citations should be grep-re-verified at implementation time (anchor drift).
- `pushU64Through` helper's kernel-panic path could be softened to `processExit(-1)`; deferred decision.
- `[16]jobEntry` cap rationale phrasing — harmless; kept.
- Build-time `ProcInfo` size assertion — recommended pattern documented in `shell_ps_command.md §2.1`.
- `strconv.Itoa(status)` signed-vs-unsigned contract in jobs.go — documented as exit-code convention.
- `preemptEnabled` const vs build-tag — kept as const for rollback simplicity.
- Several cross-link polish items — captured per-doc Reviewer MINOR tails.

**Deferred.** The 3 remaining MAJOR items (not folded) and all MINOR items stay documented in the per-doc `Reviewer MINOR notes` tails. The implementation session should scan those tails before starting each feature's commits.
