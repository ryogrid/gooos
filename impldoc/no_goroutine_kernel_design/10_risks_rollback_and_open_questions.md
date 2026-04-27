# 10 — Risks, rollback, and open questions

## Top-level risks

### R1 — Asm context-switch subtle bugs

**Scope**: `src/kthread_switch.S` (§02). Classic failure mode: a
register lost across the switch, an RFLAGS IF mismatch at resume,
or a TSS.RSP0 stale after a missed update.

**Detection**:

- M0 smoke test (two-thread round-robin) exercises the happy path.
- `scripts/test_smp_shell_preempt.sh` exercises Ring-3 preemption
  through the switch on every LAPIC tick.
- Stack-canary check at `KernelStack.Canary` exits wrong on
  corruption.

**Mitigation**:

- A `checkKernelThreadOffset()` safety check (analogous to
  `src/goroutine_tss.go:86`) verifies struct layout matches the asm
  hard-coded offsets at boot; halt-on-mismatch.
- §02's register save-set table is explicit; reviewer gate verifies
  all callee-saved regs + RFLAGS are saved.

**Rollback**: revert the M0 commit. No service depends on M0
symbols yet (M1 is the first consumer).

### R2 — SMP wake-and-steal regressions

**Scope**: the sticky+steal policy in §02 may not replicate the
observed distribution the current `stealWork` produces. The
existing `current_impl_2026_04_24/fix_plan_deferred_1_5/02_ring3wrapper_round_robin_distribution.md`
documents per-process round-robin on spawn; if our `OwnerCPU`
preference keeps a service too sticky, an AP may sit idle while
another AP is overloaded.

**Detection**:

- `scripts/test_smp_basic.sh` observes cross-CPU distribution.
- `scripts/test_sleeptest_postrevert.sh` — if sticky policy
  regresses F1 in a new way (thread stranded on its OwnerCPU with
  no steal), PASS rate drops.

**Mitigation**:

- M3 gating test is ≥ 80% PASS; if we observe < 80%, the pathology
  is visible and we re-tune (e.g. broadcast wake on idle APs more
  aggressively, or reduce stickiness to "first choice, fall back
  to round-robin after N failed wakeups").
- Preserve `stealWork` behaviour under Route C: §02's `kschedSteal`
  scans `(me + i) % n` identically to the existing
  `scripts/tinygo_runtime.patch` line 1088 pattern.

**Rollback**: M3 splits into sub-commits; revert the wake-policy
change independently of the broader migration.

### R3 — GC STW deadlock

**Scope**: §05 broadcast freeze IPI (new vector 0xFD). The freeze
relies on every thread reaching `Spinlock.Release` (or an already-
safe point) within bounded time. If a kernel thread is in an
uncooperative loop (e.g. a hot spin without any spinlock op and
without `kschedYield`), the freeze never completes.

**Detection**:

- Qualitative: GC dumps wall-clock tokens; a stuck freeze shows as
  unbounded "GC pause" latency.
- `scripts/test_tcp_longidle.sh` at 300 s stresses long idle periods
  under which GC will certainly fire.

**Mitigation**:

- Every long-running loop in §06's service list is already yielding
  (`runtime.Gosched()` → `kschedYield()`); an uncooperative new
  thread would be an explicit regression, caught by reviewer.
- Add a "freeze timeout" in the GC: if stwFrozenCount doesn't reach
  the target within N ticks, panic with the per-CPU state dumped
  (same pattern as `panicHexBuf` / `sleepAuditISRDump` — see
  `src/panic.go:13..113`).

**Rollback**: M5 back-out restores the old "BSP-only allocates"
approximation. The freeze IPI would be landed in an earlier
milestone (earliest sensible: late M3 after the thread table is
populated; more likely M4 once ring3Wrapper is a thread too). Roll
back that specific commit; the old BSP-only approximation resumes.

### R4 — TinyGo-version drift during migration

**Scope**: if a TinyGo upgrade lands mid-migration (e.g. between M2
and M3), the patch hunks still in flight need to rebase on top of
the upstream changes. The `scripts/tinygo_runtime.patch` file is
~1168 lines, most of which we are *deleting* — but during the
migration window the deletions haven't happened yet.

**Detection**: `bash scripts/patch_tinygo_runtime.sh` output shows
conflicts.

**Mitigation**:

- Do not upgrade TinyGo during the Route C cycle. Pin to 0.40.1 for
  the full duration.
- If a critical CVE / blocker forces an upgrade, pause Route C at
  the current milestone, upgrade TinyGo, re-apply the patch as-is,
  then resume.

**Rollback**: pinning TinyGo to 0.40.1 is a Makefile-level
`TINYGOROOT` path. No actual rollback needed.

### R5 — Debugger / tracing impact

**Scope**: today, a debugger with Go-runtime awareness can
enumerate TinyGo goroutines; under Route C it would see only
kernel threads. Same for stack-traces in panic paths.

**Detection**: qualitative — `src/panic.go`'s `gooosStackOverflow`
dumps a `task=...` pointer today; under Route C the pointer type
changes to `*KernelThread`.

**Mitigation**:

- `gooosStackOverflow` hook at `src/panic.go:94..113` accepts a
  `uintptr` — the address of a TinyGo `task.Task`. Rename to
  `kschedStackOverflow(t *KernelThread)` at M4 and keep it printing
  `top = t.Stack.Top`, `canary = t.Stack.Canary`. Symbol-level
  change only.
- A new `kps` debug-dump (PROPOSED helper) that iterates
  `kthreadAll` and prints `[cpu=... state=... name=... rsp=... top=...]`
  per thread. Trivial addition in M4.

**Rollback**: N/A — purely diagnostic.

### R6 — Boot-log format changes

**Scope**: the serial boot log is consumed by several harnesses
(`scripts/test_*.sh`). The transition from `go fsTask()` /
`go netRxLoop()` prints to `kschedSpawn` prints changes a handful
of message lines.

**Detection**: harness grep failures.

**Mitigation**:

- Preserve the existing `serialPrintln("ring3Wrapper: cpuID=...")`,
  `serialPrintln("NET: RX dispatch goroutine started")`, etc.
  verbatim (those are in §06's touched files).
- Where a log line references a TinyGo concept (e.g. "RX dispatch
  goroutine started"), keep it — a forgiving consumer just greps
  for the substring. A stricter rewrite can happen post-M5.

**Rollback**: N/A — each milestone's gate includes the harness
scan, so broken log lines stop the milestone.

### R7 — Build of user binaries across the migration

**Scope**: `make build` compiles user binaries (`user/cmd/*`) with
a different TinyGo target (`scheduler=tasks`). If the user-side
half of `scripts/tinygo_runtime.patch` is accidentally touched
during M5 trimming, user binaries break.

**Detection**: `make -C user all` fails or user programs halt on
entry.

**Mitigation**: §08's per-hunk verdict table is explicit about
which hunks are user-only. M5 commit should be reviewable by
running `git diff scripts/tinygo_runtime.patch` and confirming every
change is to a kernel-tagged hunk.

**Rollback**: revert the M5 patch edit; user binaries recover.

### R8 — `procByPoolSlot` semantics on thread-pool-reused slots

**Scope**: feature 2.2's ISR-safe signal delivery uses
`CurrentPoolIdx` at `src/percpu.go:30` to find the owning Process
during preempt/SIGALRM. §07 repoints the slot to the kthread stack
pool. If a thread slot is reused after a process exit (kthread
stack released, new kthread on same slot), the SIGALRM delivery
path could briefly race.

**Detection**: `scripts/test_smp_shell_preempt.sh` on rapid
process spawn/exit.

**Mitigation**:

- `ring3Wrapper` sets `setProcByPoolSlot(idx, proc)` on entry,
  clears on exit. The clear happens inside `processExit` before
  `kschedExit`, so the slot → proc mapping is nil by the time the
  slot is released.
- Make `setProcByPoolSlot(idx, nil)` explicit in `processExit` as
  part of the M4 cleanup.

**Rollback**: part of M4 commit; revert restores the pre-M4
`gInfoByTask` path which has the same race via a different
mechanism.

## Open questions

These are **not** blocking for design; they're surfaced so the
implementer knows to decide at landing time.

### O1 — Quantum duration

§04's flat 10 ms quantum mirrors the existing 100 Hz LAPIC preempt.
Is this right for I/O-bound services (netRxLoop, fsTask)? They
probably want *shorter* quanta to avoid head-of-line blocking.
CPU-bound services (kpHog, TCP retx scanner) are fine at 10 ms.

Deferred: land with flat 10 ms in M4; add per-thread-class tuning
in a post-M5 cycle if observed necessary.

### O2 — Priority inheritance / deadline inheritance

Not currently needed (no user-facing priority). If Route C wants to
expose deadlines to user programs via a new syscall (e.g. EDF-ish
real-time), §03 would need PI on spinlocks and deadline inheritance
on waits.

Deferred: out of scope.

### O3 — Select-on-N-events primitive

§03 identifies only two `select` uses in the kernel today, both
trivial. If a future service needs real select-of-N, §03's
bounded-poll pattern scales poorly (wake latency = poll interval).
Options then: a multi-event wait (thread can park on multiple
primitives, whichever signals first wakes it, others remove the
thread from their wait list on wake).

Deferred: reconsider if a consumer lands.

### O4 — Lock-free SPSC pipe optimization

§03 keeps `pipe.ch`-replacement as a locked `KQueue[byte]`. A
lock-free SPSC ring (single producer, single consumer, seqlock
cursor) would improve pipe throughput if profiling shows lock
contention.

Deferred to post-M5.

### O5 — Delete `runSleepAudit` and its counters

Once Route C has soaked and the F1 flake is observed dead (say,
30 days of clean CI), `runSleepAudit`, `SchedTasksPushed`,
`SchedPopOk`, `SchedPopNil`, `sleepAuditISRBuf`,
`sleepAuditISRDump`, `migrateTrace*` can all go. Currently landed
in `src/percpu.go:54..199` + `src/pit.go:73..75` +
`src/net.go:237..247`.

Deferred: propose a follow-up `/schedule` agent to revisit in ~30
days after M5 lands.

### O6 — Delete the kernel-side `src/task_stack_amd64.S` copy

§02 defers this. Once M5 has soaked and the user-side is confirmed
to need only `user/task_stack_amd64.S`, the kernel-side copy can
go. Small cosmetic cleanup.

### O7 — Should `kschedSpawn` return a handle?

Currently §02 has `kschedSpawn(name, fn) *KernelThread`. Callers
that don't need the handle discard it. Callers that want to send a
`KEvent` to a specific thread (e.g. `processWait` on the
ring3Wrapper thread) use the handle. This is fine.

No decision needed; flagging for review.

### O8 — Stack size: 16 KiB vs 8 KiB

§02 specifies 16 KiB kernel-thread stack. Current Ring-3 kernel
stacks (at `src/ring3_pool.go`) are 8 KiB. The doubling is
defensive — kernel threads may go deeper (e.g. TCP scanner calling
into multiple TCB helpers).

Sanity check at M4: run `stackSizeAudit()` (gated by
`const runStackAudit`, mentioned at README:35) on live services;
if watermark ≤ 4 KiB across all threads, reduce to 8 KiB.

Deferred tuning.

## Rollback matrix

| Milestone | Revert command | Residual state |
|-----------|----------------|----------------|
| M0 | `git revert <M0 SHA>` | None — new files only |
| M1 | `git revert <M1 SHA>` | Demo probes back on goroutines |
| M2 | `git revert <M2 SHA>` | fsTask back on goroutine + channels; user binaries unaffected |
| M3 | `git revert <M3 commits>` | Timer wheel back to channel-based; afterTicks shim restored |
| M4 | `git revert <M4 commits>` | ring3Wrapper + net services back on goroutines; gInfoByTask restored |
| M5 | `git revert <M5 commits>` | TinyGo patch restored; scheduler=cores restored |

A full Route C rollback is `git revert` in reverse milestone order.
No milestone commit irreversibly destroys data or configuration
outside the repo.

## Reviewer pass — applied findings

The Phase-B general-purpose reviewer subagent (hoge.md §Workflow
step 3) returned **0 BLOCKING** findings and **15 MINOR** findings
across six checks. All BLOCKING applied before commit (n/a —
none). Two MINOR findings from Check 4 were substantive enough to
the plan's integrity that they are applied rather than parked:

### Applied (two of fifteen MINOR)

- **Check 4 #1** — M3 F1-closure gate was optimistic because the
  sleeptest user program's host is still a TinyGo goroutine at
  M3. Applied: downgraded `09_incremental_migration_plan.md` M3
  gate to "S2 parity, no regression"; moved F1 closure to M4 where
  `ring3Wrapper` migration lands. Added an explicit "reviewer-pass
  re-sequencing note" paragraph at the end of §09.
- **Check 4 #2** — M3's `sys_sleep` migration (§06 row L) walks
  into the H-01 hazard because the caller is on a TinyGo goroutine
  stack at M3. Applied: reclassified row L to M4; §06's `afterTicks`
  consumer table row L now states the M4 sequencing constraint;
  §09 M3 scope excludes user-hosted afterTicks consumers; M4
  scope explicitly includes `sys_sleep` and `sys_recvfrom`
  migration alongside `ring3Wrapper`.

### Deferred (thirteen of fifteen MINOR) — citation drift and small rank gaps

All the following are small textual drifts that do not affect the
design's correctness; leaving them as tracked items lets the
landing commit apply them or ignore them at low cost.

- **Check 1 — `src/percpu.go:68` → should be `:88`** for
  `sleepAuditISRDump`; off by ~20 lines in 00 and 01.
- **Check 1 — `src/userspace.go:454` → `:453`** for `sys_sleep`
  afterTicks call; off-by-one in 06 and 07. (Partly corrected in
  §06 Applied row above; cite in §07 still says 454.)
- **Check 1 — `scripts/tinygo_runtime.patch:1004` → `:1025`** for
  the F1 `runqueues[cpuID()].Push(t)` line; line 1004 is the
  `migrateTracePush` audit hook.
- **Check 1 — `scripts/tinygo_runtime.patch:1088` → `:1054`** for
  `stealWork`'s function header.
- **Check 1 — `src/elf.go:250` cited as "round-robin site"** but
  that line is just `go ring3Wrapper(proc)`; round-robin logic
  lives in `elfSpawn`'s counter.
- **Check 1 — `ipiWakeupVector` defined in `src/ipi.go:13`**, not
  `src/pit.go:68..99`. `pit.go:83..100` only sends it.
- **Check 1 — `README:258` idempotency citation** → line 259.
- **Check 3 — `kschedQueues[].lock` rank is implicit** in the
  deadlock-freedom argument; should be explicitly ranked
  (proposed: rank 15) in §03's lock-order extension.
- **Check 3 — `Spinlock.Release` STW-hook insertion point** is
  underspecified; should be "modify `spinlockRelease` asm at
  `src/stubs.S:466..472` to read `WantSTWFreeze` before `decl
  %gs:48`".
- **Check 3 — busy-loop-without-spinlock case** deserves a line in
  §05's deadlock-freedom argument: "case b: thread not holding
  any lock is frozen directly by the IPI, not via Release".
- **Check 5 — `09_incremental_migration_plan` README anchor line
  drift** — §11's diff-shape table uses approximate line ranges
  (e.g. "lines 123..145" for the diagram actually at 122..146);
  mechanical apply will need to re-find anchors.
- **Check 5 — "SMP" row (README.md:21) rewrite enumeration
  incomplete** — §11 says "rewrite that paragraph to cite Route C
  equivalents" but doesn't list every symbol (`runtime.apScheduler`,
  `gooosWakeupCPU`, `gooosOnResume`, `runtime.systemStackPtr`) that
  the landing commit must replace.
- **Check 5 — "Multi-process" row (README.md:29) references
  `gooosOnResume`** which Route C deletes; §11 should list the row
  for update.
- **Check 5 — "Global-layout verification" row (README.md:31)
  references `runqueue`/`sleepQueue`/`timerQueue`** which §08 plans
  to remove; §11 should list the row for update.
- **Check 6 — "netDiag goroutine" phrasing ambiguity** in §01 and
  §03 is inherited from existing codebase nomenclature; clarify to
  "the anonymous netDiag-caller goroutine at `src/main.go:417`".

These deferred findings are captured here in one place so a
follow-up doc-polish commit (or the landing commit for M4/M5) can
sweep them.

## Acceptance reminder

Route C is "done" when:

1. `ls no_goroutine_kernel_design/` shows 12 files (this set).
2. Reviewer subagent zero BLOCKING findings.
3. One commit on `smp-no-goroutine-in-kernel` contains only the
   doc-set diff.
4. TL;DR in chat ≤ 5 lines.

Implementation of M0..M5 is a separate, later cycle.
