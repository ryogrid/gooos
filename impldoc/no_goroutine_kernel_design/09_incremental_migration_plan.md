# 09 — Incremental migration plan (M0..M5)

This is the execution order. Each milestone is a single coherent
commit (or a tight 2–3 commit cluster), with a gating test that
must pass before the next milestone starts. "Landed iff `<gating
test>` passes" is the acceptance rule. No milestone depends on a
future milestone's symbol; milestones can be bisected independently.

## Milestone summary

| M | Name | Outcome | Gate | Revert unit |
|---|------|---------|------|-------------|
| M0 | Context-switch stub in isolation | `kschedSwitch` + `KernelThread` round-trip, no services migrated | New test `scripts/test_kthread_smoke.sh` boots, spawns two demo kernel threads, swaps between them, halts cleanly | Single commit |
| M1 | Migrate the demo probes (`kpHog`/`kpMarker`) | §06 services #11, #12 on kernel threads under gate | `scripts/test_preempt_kernel.sh` PASS with `runPreemptProbe=true` | Single commit |
| M2 | Migrate `fsTask` | §06 service #9 on kernel thread | `scripts/test_sleeptest_postrevert.sh` at S2 parity (≥ 50% PASS); `make run` interactive shell reads/writes files via the new KQueue-based FS | Single commit |
| M3 | Migrate timer wheel + kernel-context `afterTicks` consumers | `timerDispatcher` kernel thread, `KEventAfter` primitive live; `<-afterTicks(...)` sites whose caller is *already a kernel thread* rewired; `sys_sleep` and other user-hosted sites defer to M4 | `scripts/test_sleeptest_postrevert.sh` S2 parity (no regression); `scripts/test_net.sh` PASS | 2–3 commits (core wheel + per-callsite batch) |
| M4 | Migrate `ring3Wrapper` + net services + user-hosted `afterTicks` consumers | `ring3Wrapper`, `netRxLoop`, `udpEchoServer`, `tcpRTOScannerLoop`, `tcpEchoServer` all kernel threads; `sys_sleep` and `sys_recvfrom` rewired (now safe because callers are kernel-thread-hosted) | `scripts/test_sleeptest_postrevert.sh` ≥ 80% PASS (F1 mechanically closed; stretch ≥ 95%); `scripts/test_net.sh` PASS; `scripts/test_tcp_longidle.sh` PASS at 300 s; `scripts/test_smp_shell_preempt.sh` PASS | 2–3 commits |
| M5 | TinyGo-patch trim + build flip | `scheduler=none` on `src/target.json`; patch hunk deletions per §08 | Full regression sweep: `make build` + `make lint` + `make verify-globals` clean; every test harness that passed at M4 still passes; `scripts/test_tcp_longidle.sh` at 300 s | 2–3 commits |

Time-budget-wise M0 → M4 is the bulk of the work; M5 is mechanical
once the migration is complete.

## M0 — Context-switch stub in isolation

### Scope

- Land `src/kthread.go`, `src/kthread_sched.go`, `src/kthread_pool.go`,
  `src/kthread_switch.S`, `src/kthread_lifecycle.go`.
- Do NOT migrate any existing service. The TinyGo scheduler still
  runs.
- Implement `kschedSpawn`, `kschedYield`, `kschedSwitch`, idle
  threads, per-CPU queues.
- Add BSP-only initialization (AP bring-up postponed to M1).

### Deliverable

A boot that:

1. Goes through normal `main()` init (all existing services spawn
   as TinyGo goroutines, unchanged).
2. At the very end of init, calls new `kschedSmokeTest()`:
   - Spawn thread A with body `for { print("A"); kschedYield() }`.
   - Spawn thread B with body `for { print("B"); kschedYield() }`.
   - Briefly enter `kschedLoop` on BSP, let A and B round-robin a
     few iterations, then `kschedExit` both, then *return back to
     TinyGo* (so the rest of the system continues normally).
3. Serial log shows `ABABABAB...` for a handful of iterations, then
   the normal shell boot.

**Note on "return back to TinyGo"**: this is the only milestone
where kschedLoop is entered *and exits*. M0's kschedLoop sees
`kschedAllExited` and returns to its caller. Later milestones replace
the TinyGo scheduler entirely, so kschedLoop becomes non-returning.

### New test

`scripts/test_kthread_smoke.sh`:

- Boot with a sentinel env var flipping `runKthreadSmoke = true` in
  a new `src/kthread_smoke.go` (gated flag same pattern as
  `runSleeputestTest`).
- Expect output `ABABABABAB` (10+ alternations) then `SMOKE: OK`.
- Timeout 30 s.

### Risks

- Asm stub offset mismatches between `KernelThread` Go layout and
  `kthread_switch.S` hard-coded offsets. Mitigation: a
  `checkKernelThreadOffset()` safety check analogous to
  `checkTaskOffset()` at `src/goroutine_tss.go:86`, halting on
  mismatch.
- Stack-canary drift. Mitigation: the canary word at
  `KernelStack.Canary` (offset 0) is a fixed sentinel; `kschedExit`
  checks it.

### Reversion

Single commit. `git revert` removes the five new files and the
smoke-test gate. No impact on existing services.

## M1 — Demo probes on kernel threads

### Scope

- Move `kpHog` / `kpMarker` (§06 services #11, #12) from
  `go name()` to `kschedSpawn("name", name)` in `src/main.go`.
- Replace the one `runtime.Gosched()` call in each probe body (if
  any; grep) with `kschedYield()`.
- Keep TinyGo scheduler in place for every other service.
- Wire AP bring-up: each AP calls `kschedLoop` after its existing
  entry path (`apEntry`).
- **Caveat**: at M1 the system has *both* TinyGo goroutines AND
  kernel threads alive. They coexist. The TinyGo scheduler runs
  normally; the kernel threads run between TinyGo's scheduler
  invocations (when TinyGo yields via hlt/wait_for_events, the per-
  CPU kschedLoop gets a slice; §10 parks the exact co-existence
  mechanism as "to be designed, M1 internal detail").

Co-existence mechanism (M1 only): `waitForEvents` at
`scripts/tinygo_runtime.patch:1120..1139` becomes:

```
waitForEvents() {
    // If any kernel thread runnable on this CPU, run them first.
    for kschedQueues[cpuID()].hasWork() {
        kschedLoopOnce() // one iteration: pop + switch; return on park
    }
    sti; hlt; cli  // original body
}
```

This is a temporary co-hosted mode. M4 removes TinyGo's `waitForEvents`
entirely because there's no TinyGo scheduler to return to.

### Deliverable

Demo probes run under kschedSpawn instead of `go`. Their observed
behaviour (preempt-IPI-driven yields) matches pre-M1.

### Gate

`scripts/test_preempt_kernel.sh` with `runPreemptProbe=true` — the
existing harness that exercises kpHog. PASS rate unchanged vs.
HEAD.

### Risks

- Co-existence of TinyGo scheduler and kschedLoop on the same CPU.
  Mitigation: mutual exclusion via the `waitForEvents` hook above —
  kschedLoop only runs when TinyGo is idle. No concurrent
  TinyGo-task + kernel-thread execution on the same CPU.
- Priority inversion: TinyGo tasks might starve kernel threads if
  the system is busy. Mitigation: the preempt IPI still fires every
  10 ms; kernel threads get scheduled at worst-case 10 ms latency.

### Reversion

Single commit. Revert switches `kschedSpawn` back to `go` and
removes the `waitForEvents` hook. TinyGo reclaims full scheduling.

## M2 — `fsTask` on a kernel thread

### Scope

- Migrate `fsTask` (§06 service #9) from `go fsTask()` to
  `kschedSpawn("fsTask", fsTask)`.
- Rewrite `fsReqCh` → `fsReqQ KQueue[*fsRequest]` per §06 service 4.
- Rewrite per-request reply channel → embedded `KEvent`.
- Rewrite callers `fsSendCreate` / `fsSendWrite` / `fsSendRead` /
  `fsSendList` / `fsSendDelete` to use `req.ev.Wait()`.

### Deliverable

The kernel thread `fsTask` is the sole writer / reader of the
filesystem. Every shell `ls`, `cat`, user-binary `sys_fs_read`,
user-binary `sys_fs_write` lands on this thread via `fsReqQ`.

### Gate

- `scripts/test_sleeptest_postrevert.sh` S2-baseline parity (≥ 50%
  PASS). This verifies M2 did not regress the existing F1 flake;
  F1 itself is not fixed until M3.
- `make run` interactive: `ls`, `cat hello.txt`, `echo hi > out.txt`,
  `cat out.txt` all succeed.

### Risks

- Caller callers are scattered (every `fs*` function at
  `src/fs.go:218..250` + shell + user binaries). The user binaries
  call via `sys_fs_*` syscalls whose handlers are kernel-side —
  the handlers call `fsSend*`, so the rewrite is kernel-local.
  Mitigation: §06 lists all the callers.
- Timing: `fsTask` is on the hot path for boot (`elfSpawn` reads
  each user ELF via `fsSendRead`). If `kschedSpawn` isn't robust
  enough yet, boot hangs. Mitigation: M0 smoke-test + M1 gated
  co-existence give us two boot-level tests before M2 touches the
  hot path.

### Reversion

Single commit. Revert restores `go fsTask()` and the channel-based
fs* helpers.

## M3 — Timer wheel + kernel-context `afterTicks` consumers

### Scope

Steps:

1. Land `KEvent`, `KQueue` primitives (§03) as standalone types (no
   callers yet). M0 may have already partially landed them; M3
   completes.
2. Land `kschedTimedPark(d)` and `KEventAfter(d)` (§03) using the
   existing timerList in `src/afterticks.go`.
3. Migrate `timerDispatcher` from goroutine to kernel thread.
4. Migrate `<-afterTicks(d)` consumers **whose caller is already a
   kernel thread** (after M2's `fsTask` migration: `fsTask` callers;
   after M1: `kpHog` / `kpMarker` if they use afterTicks). Consumers
   still hosted in TinyGo goroutines — specifically **`sys_sleep`
   (`src/userspace.go:453`, row L) and `sys_recvfrom` timeouts (rows
   G/H/I) — stay on the `afterTicks` channel shim and are migrated
   in M4** once their host `ring3Wrapper` is itself a kernel thread.
   Rationale (reviewer finding, Check 4): rewiring `sys_sleep` to
   `kschedTimedPark` while the caller is a TinyGo goroutine stack
   walks directly into the H-01 hazard (§01) — `kschedTimedPark`
   would write the TinyGo task's stack pointer into a
   `KernelThread` struct, corrupting both.
5. Keep the `afterTicks` channel shim alive through M3; it's
   removed in M4 after the last caller migrates.

### Deliverable

`timerDispatcher` runs as a kernel thread, signalling `KEvent`s
directly; kernel-thread-hosted services (fsTask, tcpRTOScannerLoop
after M2's migration) park via `kschedTimedPark` / `KEventAfter`.
User-hosted sleep / recv paths still route through the shim, giving
identical semantics to today. **F1 is not yet closed at M3** —
that closure requires M4's `ring3Wrapper` migration so that the
sleeptest user program's host becomes a kernel thread and the full
wake chain stays inside the kthread scheduler.

### Gate

- `scripts/test_sleeptest_postrevert.sh` **S2 parity** — ≥ 50% PASS
  with no regression below the current baseline. F1's actual
  closure is M4's gate.
- `scripts/test_net.sh` PASS — UDP echo round-trip, DHCP DORA,
  other Phase 5 harness bits. `sys_recvfrom` still routes through
  the unchanged shim path at M3.
- `scripts/test_tcp_longidle.sh` at 60 s PASS (full 300 s is M4's
  gate).

### Risks

- Many callers, many site rewrites. Mitigation: one commit per
  call-site cluster (e.g. all `netsock.go` sites in one commit, all
  `main.go` sites in another, `tcp_retx.go` alone, `userspace.go`
  alone). The `afterTicks` shim survives until the final cleanup
  commit.
- Semantic drift: a bounded-poll replacement for select-on-N is
  subtly different in wakeup latency (bounded to the poll step).
  Mitigation: pick the same poll interval the existing code uses
  (5 ticks = 50 ms for recvfrom; 1 tick for keyboard fallback;
  unchanged).
- `tcpEchoServer` per-connection spawns. Mitigation: each connection
  gets its own kschedSpawn, exiting on connection close via
  `kschedExit`.

### Reversion

M3 splits into 3–4 commits; each is independently revertable. The
shim strategy means revert can stop at any point and the remaining
call sites still use the `afterTicks` channel.

## M4 — `ring3Wrapper` + net services + user-hosted sleep/recv

### Scope

- Migrate `ring3Wrapper` (§06 service #7; §07) from goroutine to
  kernel thread. **This is the enabling step for the remaining
  items.**
- Migrate `netRxLoop` (§06 service #2).
- Migrate `udpEchoServer` (§06 service #3).
- Migrate `tcpRTOScannerLoop` (§06 service #5).
- Migrate `tcpEchoServer` (§06 service #6) plus per-connection
  spawned workers.
- **Rewire the user-hosted `afterTicks` consumers deferred from
  M3**: `sys_sleep` (row L, `src/userspace.go:453`) to
  `kschedTimedPark`; `sys_recvfrom` timeouts (rows G/H/I) to the
  bounded-poll pattern. Safe now because the host is a kernel
  thread.
- Delete the `afterTicks` channel shim (last caller is gone after
  the rewires above).
- Delete `ring3StackPoolCh` channel; repurpose the slot pool
  (§06 service #8).

### Deliverable

Every long-lived kernel service runs as a kernel thread. Ring-3
processes are hosted by kernel threads. **F1 is mechanically
closed**: the sleeptest wake path is now `timerDispatcher
(kthread) → KEvent.Signal → kschedWake → kschedQueues[sleeptest's
OwnerCPU].Push` — no channel, no TinyGo scheduler, no cross-CPU
steal race. The H1 pathology from
`current_impl_2026_04_24/fix_plan_deferred_1_5/03_sleep_cross_cpu_channel_wakeup_audit.md`
cannot occur. The TinyGo scheduler on the kernel side is effectively
idle; `waitForEvents` still runs (as idle-thread body inside
kschedLoop's idle slot) but TinyGo's scheduler loop has no
goroutines left to schedule.

### Gate

- `scripts/test_sleeptest_postrevert.sh` ≥ 80% PASS (target;
  stretch ≥ 95%). **F1 closure measured here.**
- `scripts/test_net.sh` PASS.
- `scripts/test_tcp_longidle.sh` 300 s PASS.
- `scripts/test_smp_shell_preempt.sh` PASS — verifies Ring-3
  SIGALRM delivery still works with kernel-thread hosts.
- `scripts/test_smp_release_gate.sh` PASS.
- `scripts/test_smp_basic.sh` PASS — SMP distribution observable.
- `scripts/test_ps.sh` PASS — feature 2.5 ps listing still works.

### Risks

- TSS.RSP0 / CR3 sequencing on Ring 3 entry/return (§07). High risk
  for subtle faults. Mitigation: §07 spells out the `tssSetRSP0ForKernelThread`
  call site exactly; reviewer gate in §04 + §07 should catch errors
  before landing.
- Per-connection kernel-thread spawn in `tcpEchoServer` may
  exhaust the 32-slot stack pool under high connection load.
  Mitigation: add a "connection-accept drops if pool full" path
  (already the TCB table's behaviour — no new failure mode).

### Reversion

M4 is the hardest to revert cleanly because ring3Wrapper migration
intertwines with the Process/KernelThread lifecycle. The commit set
is split per service: migrate one service at a time, each commit
independently revertable up to ring3Wrapper (which is the central
one and must stay under revert after the supporting services are
reverted).

## M5 — TinyGo-patch trim + `scheduler=none`

### Scope

- Flip `src/target.json` `scheduler=cores → scheduler=none`.
- Trim `scripts/tinygo_runtime.patch` per §08 (delete ~510 lines
  across the scheduler / queue / multicore / gc_blocks hunks).
- Update `scripts/patch_tinygo_runtime.sh` checksums.
- Re-apply the new patch in `~/.local/tinygo0.40.1/`.
- Update `scripts/verify_globals.sh` to assert the new kthread
  globals instead of TinyGo queues.
- Confirm `make lint` + `make verify-globals` clean.

### Deliverable

Kernel builds with `scheduler=none`. No `go` / `chan` / `select` in
kernel source. TinyGo runtime binary footprint shrinks.

### Gate

Full regression sweep — every harness that passed at M4 must still
pass at M5:

- `scripts/test_smp_basic.sh`
- `scripts/test_net.sh`
- `scripts/test_tcp_longidle.sh 300`
- `scripts/test_smp_shell_preempt.sh`
- `scripts/test_smp_release_gate.sh`
- `scripts/test_preempt_kernel.sh`
- `scripts/test_sleeptest_postrevert.sh ITERATIONS=50` (target
  ≥ 95% PASS).
- `scripts/test_ps.sh`

Plus `make build` + `make lint` + `make verify-globals` clean.

### Risks

- Build break if any non-obvious kernel caller still uses a
  TinyGo-scheduler-linked symbol. Mitigation: the `scheduler=none`
  TinyGo tag produces a linker error naming the missing symbol, so
  breakage is caught at build time with a clear message.
- Patched TinyGo tree mismatch: forgetting to re-run
  `scripts/patch_tinygo_runtime.sh` after the patch-file edit
  leaves the live tree stale. Mitigation: the wrapper's
  idempotency check rejects stale state.

### Reversion

Single commit pair (patch-file edit + `target.json` flip). Revert
restores `scheduler=cores` and the full patch; `make build`
confirms the pre-M5 world is back.

**Note on README update**: the README edit lands as the *M4 tail*
or *M5 first commit* per §11. It is not a separate milestone.

## Post-M5 tidy-up (parked in §10)

Not required for Route C closure, but natural cleanup that follows:

- Delete `runSleepAudit` gate + ISR-side audit dump + counters.
- Delete the one-shot boot demos (§06 classification).
- Consolidate `impldoc/smp_*.md` and `impldoc/goroutine_design_*.md`
  into a single Route-C-successor design doc, deprecating the
  legacy set.
- Consider deleting `src/task_stack_amd64.S` kernel-side copy
  (user-side copy stays); §02 deferred this.

## Milestone independence argument

Reviewer gate from §hoge Phase B check 4: "Each milestone is
testable with current harnesses without forward-references to
future milestone symbols."

| M | Needs M-n symbol? | Test uses future symbol? |
|---|-------------------|--------------------------|
| M0 | No (pure new surface) | `kschedSmokeTest` is local to M0 |
| M1 | M0's `kschedSpawn` | `test_preempt_kernel.sh` tests existing behaviour |
| M2 | M0+M1 | `test_sleeptest_postrevert.sh` is the S2 baseline; existing |
| M3 | M0..M2 + §03 primitives | `test_sleeptest_postrevert.sh` + `test_net.sh` existing |
| M4 | M0..M3 + `KEvent` | `test_net.sh`, `test_tcp_longidle.sh`, `test_smp_shell_preempt.sh` all existing |
| M5 | M0..M4 + patch edits | Full sweep existing |

Every gate is an existing harness (or a trivial new one for M0).
No milestone references a symbol that only exists in a later one.

## Reviewer-pass re-sequencing note

The initial draft of §09 placed F1 closure at M3 on the premise
that migrating `timerDispatcher` and the `afterTicks` wheel alone
was enough. The Phase-B reviewer (§10 "Applied findings, Check 4")
caught that the wake chain from `timerDispatcher` into a
TinyGo-goroutine-hosted sleeptest process would re-enter the
TinyGo scheduler (since `ring3Wrapper` is still a goroutine at
M3), reintroducing the H1 cross-CPU steal race. The re-sequenced
plan (this version of §09) keeps F1 closure at M4 where
`ring3Wrapper` migration has landed; `sys_sleep` and `sys_recvfrom`
migrations move with it. M3's gate downgrades to "no regression
below S2 baseline" accordingly.
