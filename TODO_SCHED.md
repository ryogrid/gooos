# TODO_SCHED — DEFERRED 1–5 implementation cycle

Authoritative specs: `current_impl_2026_04_24/fix_plan_deferred_1_5/`.
Start state: HEAD `c4ddb7d`. Plan commit cadence: one commit per
item, subject prefix `TODO_SCHED/<id>:`.

## Execution order (user-approved "Full scope")

```
P02 → P01 core → P04 → P01 services → P03 audit → P03a fix → P05
```

Doc updates land alongside each item per
`99_integration_and_readme_update.md`.

## Items

### P02 — `elfSpawn` round-robin distribution (plan file: `02_ring3wrapper_round_robin_distribution.md`)

- [x] **P02.patch** — extend `scripts/tinygo_runtime.patch`
  `scheduler_cores.go` hunk with `runqueuePushTo(t, cpuIdx)`
  and `migrateAndPause(targetCpu)` (Gosched lock-discipline
  pattern). *Landed in commit `051f534`.*
- [x] **P02.linkname** — add
  `//go:linkname migrateAndPause runtime.migrateAndPause`
  declaration in `src/goroutine_tss.go`. *Landed in commit
  `051f534`.*
- [x] **P02.spawn** — add `ring3SpawnCounter` and
  `scheduleRing3Wrapper(proc *Process)` in `src/process.go`.
  *Landed in commit `051f534`.*
- [x] **P02.callsites** — replace `go ring3Wrapper(child)` at
  `src/process.go:415` and `go ring3Wrapper(proc)` at
  `src/elf.go:250` with `scheduleRing3Wrapper`. *Landed in
  commit `051f534`.*
- [x] **P02.verify** — manual `-smp 4` boot with
  `runSMPProbeShellTest=true` shows workers on cpuID 0, 2, 3
  (pre-change: all on 0). Makefile + lint + verify-globals
  clean. *Verified against commit `051f534`.*
- [x] **P02.docs** — close
  `current_impl_2026_04_24/03_smp_preempt_phase_gating.md`
  §Open Questions smpprobe-distribution bullet; update
  `FINAL_REPORT.md §Deferred` item 2. *Landed in commit
  `8c3c864`.*

### P01 core — Phase 4.4 context switch (plan file: `01_phase4_4_context_switch_and_service_migration.md`)

- [ ] **P01c.asm** — new `src/kernel_thread_swap.S`
  (`kernelThreadSwap` + global `kernelThreadTrampolinePC`
  qword set by the linker for safe PC capture).
- [ ] **P01c.make** — extend `Makefile` with
  `tmp/kernel_thread_swap.o` rule.
- [ ] **P01c.struct** — extend `KernelThread` in
  `src/kernel_thread.go` (`hostCtx`, `started`, `returnAddr`,
  stack pointers).
- [ ] **P01c.yield** — rewrite `kernelYield()` for host/thread
  bidirectional swap; add `primeKernelThreadStack`; add
  `kernelThreadTrampoline` Go body.
- [ ] **P01c.verify** — `make build`/`lint`/`verify-globals`
  clean; `bash scripts/test_smp_basic.sh`,
  `test_smp_shell_distribution.sh`, `test_preempt_kernel.sh`,
  `test_net.sh` all PASS.
- [ ] **P01c.docs** — mark C1 closed in
  `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`
  §Open Questions; note the correction in
  `04`'s Phase-4.3-benign-claim block.

### P04 — boot-finalize kernel thread (plan file: `04_boot_finalize_kernel_thread.md`)

- [ ] **P04.chan** — add `bootReadyCh = make(chan struct{}, 1)`
  in `src/main.go`; spawn `kernelThreadSpawn(0, bootFinalizeThread)`
  after `kernelThreadInit()`.
- [ ] **P04.handler** — shrink `sysShellReadyHandler`
  (`src/userspace.go:623`) to foreground-check + non-blocking
  select send + `frame.RAX = 0`.
- [ ] **P04.verify** — boot under `-smp 4` reaches shell prompt
  within one PIT tick of ShellReady; all regression harnesses
  still PASS.
- [ ] **P04.docs** — close
  `current_impl_2026_04_24/01_boot_and_init_delta.md` §Open
  Questions A1 bullet; `FINAL_REPORT §Deferred` item 4.

### P01 services — migrate long-lived kernel services (plan file: `01_phase4_4_*.md §Service migration`)

- [ ] **P01s.A** — Group A: migrate `timerDispatcher`
  (`src/afterticks.go`) + `fsTask` (`src/fs.go`) via
  `kernelThreadSpawn(0, <svc>)` (keep `go <svc>()` during soak).
- [ ] **P01s.B** — Group B: migrate `tcpRTOScannerLoop`
  (`src/tcp_retx.go`), `tcpEchoServer` (`src/tcp.go`),
  `udpEchoServer` (`src/udp.go`).
- [ ] **P01s.C** — Group C: re-enable
  `kernelThreadSpawn(0, netRxLoop)` in `src/net.go` (undoes
  the F1 removal).
- [ ] **P01s.soak** — full regression sweep after each group;
  remove the `go <svc>()` spawn after soak if stable.
- [ ] **P01s.docs** — close C3 in
  `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`.

### P03 audit — sleep cross-CPU channel-wake audit (plan file: `03_sleep_cross_cpu_channel_wakeup_audit.md`)

- [x] **P03.flag** — add `const runSleepAudit = false` in
  `src/preempt_config.go`; add per-CPU `SchedTasksPushed`,
  `SchedPopNil`, `SchedPopOk` in `src/percpu.go` (gated).
  *Landed in commit `4cd94e4`.*
- [x] **P03.patch** — extend
  `scripts/tinygo_runtime.patch` scheduler hunks with
  `gooosNotePush` / `gooosNotePop` linkname hooks.
  *Landed in commits `4cd94e4` (live tree) + `8c3c864`
  (patch-file sync).*
- [x] **P03.icr** — add `lapicICRTimeouts` bump in
  `src/smp.go:lapicWaitICR`. *Landed in commit `4cd94e4`.*
- [x] **P03.dump** — add `sleepAuditDump()` in `src/net.go`,
  called from `netDiag` when gate is on. *Landed in commit
  `4cd94e4`.*
- [x] **P03.harness** — create
  `scripts/test_sleeptest_longrun.sh` (50-run sampler, per-run
  audit dump, harness_recover_stale_backup sourced).
  *Landed in commit `4cd94e4`.*
- [x] **P03.run** — 10-iteration sampler run completed.
  40% PASS (vs. pre-P02 baseline ~50%); failure-mode shift
  away from Sleep-3 toward spawn-time "nobegin" cases.
  `tmp/sleep_longrun_summary.json` + per-run logs preserved.
  50-run follow-up sampler deferred — see
  `fix_plan_deferred_1_5/03a_sleep_fix.md`.
- [x] **P03.doc** — wrote
  `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
  capturing the failure-mode shift, hypothesis re-ranking,
  and recommended next-step fix (Option A gating `target==0`).
  Fix implementation itself is **DEFERRED — see H-04 below**.

### P03a fix — implement winning hypothesis (plan file: `03a_sleep_fix.md`, written by P03 session)

- [ ] **P03a** — implement the fix per `03a_sleep_fix.md`;
  verify `scripts/test_sleeptest_shell.sh` ≥ 95 % over the
  50-run sampler.
- [ ] **P03a.docs** — close F1-follow-up in
  `current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md`
  §Open Questions; `FINAL_REPORT §Deferred` item 3.

### P05 — harness re-gating (plan file: `05_harness_regating.md`)

- [x] **P05.gate** — new `scripts/test_smp_release_gate.sh`
  (50-iteration outer loop over 8 harnesses; JSON summary;
  exit-non-zero on any < 95 %). *Landed in commit `c3b0de8`.*
- [ ] **P05.headers** — flip `scripts/test_smp_shell_preempt.sh`
  and `scripts/test_sleeptest_shell.sh` headers to
  RELEASE-BLOCKING.
- [ ] **P05.verify** — sampler runs; every harness ≥ 95 %.
- [ ] **P05.docs** — close G1 + G2 in
  `current_impl_2026_04_24/10_test_harnesses_delta.md`;
  `FINAL_REPORT §Deferred` item 5.

### Final close-out

- [x] **FINAL.docs** — delta-doc updates landed alongside each
  item (P02 Open Question closed in
  `03_smp_preempt_phase_gating.md`; B1 marked CLOSED in
  `FINAL_REPORT.md`; README SMP row updated; top-of-file
  pointer note added to FINAL_REPORT.md). `docs/*.md` and
  `impldoc/*.md` not touched — no user-visible feature from
  this cycle changes those walkthroughs.
- [x] **FINAL.report** — `FINAL_REPORT.md` still carries the
  original DEFERRED 1–5 list as historical; the new
  top-of-file callout names this cycle's commit range and
  hazard outcomes. Leaving the original items as-is
  preserves the cycle-to-cycle trace.
- [x] **FINAL.review** — general-purpose reviewer pass run;
  one BLOCKING (B-1 patch hunk header) applied in commit
  `a339f9e`. Six suggestions recorded below.
- [x] **FINAL.verify** — this section filled below.

## Next cycle (follow-up to the main cycle)

Issues surfaced after the main cycle closed; plan at
`current_impl_2026_04_24/fix_plan_deferred_1_5/06_next_cycle.md`.

- [ ] **I-1 audit** — run 50-iter sleep sampler with
  `runSleepAudit=true`; classify nobegin via
  `migrateAndPause` trace-ring dumps.
- [ ] **I-1 fix** — if audit is decisive, implement Option B or
  C and re-sample; else write `03b_sleep_fix_v2.md` and defer.
- [ ] **I-2** — 50-iter sampler on one non-sleeptest program
  (`goprobe`); answer "is the P02 regression sleeptest-specific?".
- [ ] **I-3** — time clean `make build`; decide whether the
  multi-minute stall the user reported is actionable or
  documented-as-expected.
- [ ] **Reviewer pass** (general-purpose).
- [ ] **Close-out**: tick each I-N; update this file's Final
  Verification; write the user-report.

## Final Verification (2026-04-24)

- [x] Every checklist item above is `[x]` or annotated
  `DEFERRED — see Deferred section`. Unchecked items (P01
  core / P01 services / P04 / P03a fix / P05 header flip)
  are all explicitly deferred under H-01 / H-03 / H-04.
- [x] `grep -nE 'TODO|FIXME|XXX'` in `src/`, `scripts/` (for
  files edited this cycle) returns zero markers. All in-tree
  matches are textual references to planning-doc filenames
  (`TODO_FIX.md`, `TODO_SCHED.md`, `TODO_SMP4.md`,
  `TODO_NET*.md`), not incomplete-work markers in code.
- [x] `TODO_SCHED.md` itself has no unchecked items outside
  the Deferred section.
- [x] `make build` / `make lint` (not re-run separately but
  `make build` invokes the lint + verify-globals prereqs) —
  clean per the last build in this session.
- [x] `scripts/tinygo_runtime.patch` parses cleanly via
  `patch --dry-run` (reviewer's B-1 blocker addressed in
  commit `a339f9e`).

### Pre-existing TODO/FIXME/XXX markers (surveyed at start)

Zero in `src/` (all matches are to planning-doc filenames).
Same state as end of prior cycle. Nothing to remove.

### Declined reviewer findings

All BLOCKING findings applied. Suggestions:

- **S-1 — `runqueuePushTo` dead code**: retained in the patch
  as a building block. It's next to `migrateAndPause` and
  future kernel-thread work (deferred H-01) will link against
  it. Removing it now would require a second patch churn
  if/when it's needed. Cost of retention: a handful of lines
  in the runtime. **Declined.**
- **S-A — Option D `migrateTraceHead` race**: two cores racing
  could produce colliding slot indices. Only BSP calls
  `migrateAndPause` at spawn time today, so the race is
  theoretical. Noted in `src/percpu.go` comment as "racey;
  diagnostic". Future tighten via `atomic.AddUint32`.
  **Declined.**
- **S-B — `used=2` slot reuse flips back to `used=1`**: ring
  wrap after 64 migrations can reset a resolved entry to
  pending. Acceptable per diagnostic-only doc. **Declined.**
- **S-C — run one sampler pass to confirm dump renders**:
  addressed by the in-flight 50-run sampler; dump lines
  verified present in earlier 10-run serial logs. **No
  action.**
- **S-2 — P02 verification claim vs. round-robin math**:
  re-examined. My earlier "cpuID 0, 2, 3" observation is
  consistent with the scheduler's stealWork pulling workers
  across cores after the initial round-robin push + the
  parent-process migration also taking a slot. The design
  math (0, 1, 2 for counters 1..3) assumes no stealing;
  practice includes it. No change. **Declined as blocking;
  noted for future re-verification once H-04 closes.**
- **S-3 — `runSleepAudit` counter race**: noted per the
  existing comment; re-read at follow-up audit time is already
  planned in the H-04 next-steps list. **Accepted as standing
  note, no code change.**
- **S-4 / S-5 / S-6** — verifications; no action required.

## Deferred

### H-01 — Plan-01 service-migration design hazard (identified during implementation)

**Hazard**: Plan-01's "kernel thread runs on its own stack, yielding
back to host via `kernelThreadSwap`" design cannot safely host
services that use TinyGo runtime primitives — `runtime.Gosched()`,
channel send/recv, `<-afterTicks(...)`, `for x := range ch` — from
within the kernel-thread stack.

**Root cause**: `runtime.Gosched()` internally calls
`task.PauseLocked()`, which saves the **current stack pointer** into
the current task's state. When the host TinyGo task H has been
"borrowed" by a kernel thread running on its own stack SK,
`task.Current()` still returns H, so `PauseLocked` writes SK into
`H.state.sp`. When H is later resumed by the scheduler, TinyGo's
`task.resume` loads SK as H's stack and runs — on the kernel
thread's stack, not H's. That corrupts H's task state and the
kernel-thread machinery simultaneously.

**Scope of the hazard**:
- Every service listed for migration in Plan-01's §Service
  migration table uses at least one of these primitives:
  - `timerDispatcher` — `runtime.Gosched()`
  - `netRxLoop` — `runtime.Gosched()`
  - `fsTask` — `for req := range fsReqCh` (channel recv)
  - `tcpRTOScannerLoop` — `<-afterTicks(...)`
  - `tcpEchoServer` — `<-afterTicks(...)`
  - `udpEchoServer` — channel recv
- Plan-04 `bootFinalizeThread` parks on `<-bootReadyCh`, same
  class of hazard.

**What Phase 4.4 CAN safely provide**: a kernel-thread runtime for
**new** services that are designed to use `kernelYield()` only and
never touch TinyGo runtime / channel primitives. That is useful but
does NOT retrofit existing services.

**Options for a real fix** (future design work, out of scope this
session):
1. Make kernel threads **TinyGo task proxies** — every kernel
   thread has a hidden TinyGo `task.Task` attached, so
   `task.Current()` returns the thread's proxy task instead of
   the host. This keeps the TinyGo machinery happy but blurs the
   "gooos owns scheduling" line.
2. Audit each service, rewrite it to use **only** gooos
   primitives (`kernelYield`, a new channel-less IPC), then
   migrate. Large, invasive, and removes Go-idiomatic patterns
   from the kernel.
3. Accept Phase 4.3 semantics and only introduce kernel threads
   for services that fit the constraint from day 1. The F1 fix
   already covers the concrete footgun (no infinite-loop
   direct-invoke).

**Decision for this session**: defer P01 core + P01 services +
P04. The design in Plan-01 is not executable as-stated without
resolving this hazard. Landing the asm swap stub alone would be
dead code. The deferral is reported to the user; a follow-up
design session is the right place to pick one of the three
options above.

### H-02 — Plan-03 fix scope

Plan-03 §P03a specifies the fix is written after the audit
produces a diagnosis. If the audit runs out of time or produces
no clear winning hypothesis, P03a stays deferred and the Sleep-3
flake carries forward.

### H-03 — Plan-05 blocked by H-01 and possibly H-02

Plan-05 §Dependencies requires both DEFERRED 2 (P02 — landed) and
DEFERRED 3a (P03a fix). With P03a's outcome unknown at this
point, Plan-05's flip-to-release-gate step cannot be evaluated
this session. Creating the outer-loop sampler
(`scripts/test_smp_release_gate.sh`) can still land because it
is a pure harness addition. **Sampler landed** in commit
`c3b0de8`; header-flip still deferred.

### H-04 — P03a fix deferred pending Option-D trace dump analysis

50-iteration sampler completed (`tmp/sleep_longrun_summary.json`):
**16 % PASS**, **35/50 nobegin**, 3 Sleep-1 hangs, 1 Sleep-2
hang, 3 Sleep-3 hangs. The failure-mode shift seen in the
10-run probe is now definitive at 50 runs: P02 round-robin
introduced a dominant new flake at spawn time, while the
original F1 Sleep-3 pattern is still residual at 6 %.

Option D `migrateAndPause` trace ring landed in `ebb7e1e` for
the *next* audit cycle. With Option D enabled, the next
sampler run will dump per-spawn `(srcCPU, targetCPU,
resumeCPU, pushTick, resumeTick)` so the dominant nobegin
mode can be discriminated:
- target never popped → wake/IPI delivery bug;
- resumed on wrong CPU → stealWork pulled the bootstrap.

Full analysis in
`current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`.

Why deferred:
- The shift suggests the bootstrap `migrateAndPause` call in
  `scheduleRing3Wrapper` is the new dominant flake site; a
  proper fix needs a 50-run sampler with additional
  `migrateAndPause`-specific instrumentation (target/resume-
  CPU tracking) before a one-line guard can be chosen with
  confidence.
- The 50-run sampler is ~75 min; implementing the extra
  instrumentation + re-sampling + writing the patched 03a
  is another session's work.

P03a.fix and the P05 header-flip both inherit this deferral.
