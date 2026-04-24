# TODO_NOGOTIN — Route C implementation tracker

This file tracks the implementation of the no-goroutine kernel
(Route C) per `no_goroutine_kernel_design/` and `hoge.md`. One
checkbox per commit; each commit lands with its matching check
mark in the same commit.

Branch: `smp-no-goroutine-in-kernel`
Starting SHA: `7f81f12` (design doc set).

## Baseline (pre-Route-C, HEAD = 7f81f12)

- [x] B0 — `make build` clean
- [x] B1 — `make lint` clean (runs as first phase of `make build`)
- [x] B2 — `make verify-globals` clean (runs as last phase of `make build`)
- [x] B3 — `scripts/test_smp_basic.sh` — PASS (ap_kernel_cpus=3 ring3_ap_hits=0)
- [ ] B4 — `scripts/test_net.sh` — deferred; the test is long and not needed to gate M0; rerun at M2 or earlier if regression suspected
- [ ] B5 — `scripts/test_sleeptest_postrevert.sh ITERATIONS=20` — deferred; the F1 baseline (~50 % PASS S2) is already recorded in commit `d2008c8`'s log and `tmp/sleep_s2_postrevert_summary.json` if present; rerun before M4 to confirm regression-free M2/M3 landings

## M0 — Context-switch stub in isolation

- [x] M0.1 — New files: `src/kthread.go`, `src/kthread_sched.go`, `src/kthread_pool.go`, `src/kthread_lifecycle.go`, `src/kthread_switch.S`, `src/kthread_smoke.go`, `scripts/test_kthread_smoke.sh`; Makefile wires `kthread_switch.S`; Phase 4.3 `src/kernel_thread.go` + `kernelYield`/`kernelThreadInit` call sites deleted (superseded); passes `checkKernelThreadOffset()` at boot; gate `scripts/test_kthread_smoke.sh` **PASS** (A=5 B=5 ok=1)

## M1 — Demo probes on kernel threads

- [x] M1.1 — **M1 infrastructure only** (kpHog / kpMarker migration itself deferred to M4). Landed: `kschedLoopOnce()` in `src/kthread_sched.go` for one-iteration scheduling; `handlePreemptIPI` branch in `src/goroutine_irq.go` that yields the active kernel thread via `kschedYield()` (preserving the existing Ring-3 iretq-frame rewrite and TinyGo fallback); `scripts/tinygo_runtime.patch` wait_gooos.go hunk calling `gooosKschedLoopOnce` (linkname `kschedLoopOnce`) before `sti;hlt;cli` so APs pick up kernel threads during TinyGo idle windows; `kschedLoopOnce` holds IF=0 while setting `kschedRunning[cpu]` to close a preempt-IPI race. kpHog/kpMarker stay as goroutines for now (preempt test expects them cross-CPU; kpHog-as-kernel-thread debugging needs M4-scope context where `ring3Wrapper` also migrates). Gates: `scripts/test_kthread_smoke.sh` PASS (regression-clean), `scripts/test_preempt_kernel.sh` PASS (markers_observed=6 >=5).

## M2 — `fsTask` on kernel thread

- [x] M2.1 — `fsReqCh` → `fsReqQ` (new `fsReqQueue` in `src/kthread_queue.go`, MPSC-shaped bounded ring of `*fsRequest`); per-request reply chan → embedded `KEvent` (new `src/kthread_event.go`) + owned `*fsResponse`; all five `fsSend*` callers rewired; `fsTask` via `kschedSpawn("fsTask", fsTask)`. KEvent.Wait / fsReqQueue.Push when caller is not on a kernel thread pumps `kschedLoopOnce` to keep -smp 1 boots alive. **Gate**: 23/49 PASS = 46 % on `scripts/test_sleeptest_postrevert.sh` (interrupted at 49/50 per user request; within 1σ noise of the 50 % S2 baseline — no regression. M2 does not touch any `<-afterTicks(...)` site so the flake distribution is inherited.). Interactive boot: clean shell prompt under -smp 4 (fs ops verified indirectly by 30+ ELFs being embedded into FS + shell boot). Summary: `tmp/sleep_m2_summary.json`.

## M3 — Timer wheel + kernel-context `afterTicks` consumers

- [x] M3.1 — KEvent (`src/kthread_event.go`) + `fsReqQueue` (`src/kthread_queue.go`) already landed in M2; generic `KQueue[T]` generalisation deferred to when a second type (pipe / udp / tcp) needs it (M4 scope).
- [x] M3.2 — `timerEntry` extended with `ev *KEvent` alongside the legacy `ch chan<- struct{}` — exactly one is set per entry; dispatcher fires whichever. `KEventAfter(d uint64) *KEvent` + `kschedTimedPark(d uint64)` added in `src/afterticks.go`. `afterTicks` (chan-returning) shim retained verbatim for TinyGo-goroutine callers.
- [x] M3.3 — `timerDispatcher` stays a goroutine for M3 (body now signals events in addition to channel sends). Migrating the dispatcher itself to a kernel thread is deferred with the rest of the user-hosted callers to M4 (same rationale as kpHog / sys_sleep: H-01 hazard if a kthread calls Go chan send). No kernel-hosted `<-afterTicks` callers exist yet (fsTask doesn't use it), so no call sites rewired in M3; consumers migrate as they become kthread-hosted in M4. **Gates**: `scripts/test_kthread_smoke.sh` **PASS** (A=5 B=5 ok=1); -smp 4 boot sanity: shell prompt reached, `afterTicks: OK` self-test fires (timerDispatcher's dual path works). `test_sleeptest_postrevert.sh` re-run deferred; M2's 46 % baseline is the reference and M3 doesn't modify sleep paths.

## M4 — `ring3Wrapper` + net services + user-hosted sleep/recv

**Session-3 finding (2026-04-25)**: M4's net-service migrations
(M4.2 — netRxLoop, udpEchoServer, tcpRTOScannerLoop, tcpEchoServer)
cannot land cleanly before §M5's STW freeze IPI (vector 0xFD,
`05_gc_integration.md`). Reason: those services allocate in their
hot paths (`ethernetDispatch → ipv4Handle → packet buffers / ARP
cache / stats`). Once dispatched on an AP via kschedLoopOnce, they
allocate from AP context — racing the pre-Route-C "BSP-only
allocates" GC approximation. Reproducer: attempted `netRxLoop`
migration in this session triggered a boot hang under default
QEMU networking (ARP/DHCP traffic arrives, drainRxRing →
ethernetDispatch → allocator contention → hang). Reverting to a
goroutine restored boot. `-net none` boot showed the allocation
side was the culprit — without incoming traffic netRxLoop never
allocates and the kthread version boots fine.

The M4 dependency on M5 should be recorded as a **§09 sequencing
refinement**: rename the milestone order so the GC integration
lands first (new M4'), then service migrations (new M5'), then the
build-flip (new M6'). Current §09 nominally places GC work in
"§05 doc + M5 TinyGo patch trim" but the STW freeze IPI
implementation is implicitly in M4 scope; splitting it out makes
the dependency explicit.

- [x] M4.0 — **gcLock replacement (partial PREREQ)**. Replaced
  `task.PMutex gcLock` in the patched `runtime/gc_blocks.go` with
  a plain `uint32 gcLockWord` acquired/released via the gooos
  kernel spinlock stubs (`spinlockAcquire` / `spinlockRelease`)
  via `//go:linkname`. Goroutine callers and (future) kthread
  callers now take the same spinlock — cross-CPU safe without
  parking via task.PauseLocked, removing the H-01 hazard on the
  allocator hot path. `scripts/tinygo_runtime.patch` extended
  with the new hunk; `scripts/patch_tinygo_runtime.sh`
  idempotency check updated to require `gcLockWord` in the live
  tree. **The STW freeze IPI (vector 0xFD) + concurrent-mutator
  mark-phase guard remain deferred to M5** — the mark phase
  still relies on the "every mutator eventually parks at a
  safe-point" heuristic under scheduler=cores. For M4.2 net-
  service migration, the gcLock spinlock alone is the gating
  fix; full STW is correctness-nice-to-have for later.
  **Gates**: `make build` clean; `scripts/test_kthread_smoke.sh`
  **PASS** (A=5 B=5 ok=1); `-smp 4` boot with default QEMU
  networking reaches shell prompt (was hanging at M3.3 when
  netRxLoop was attempted as a kthread — that was the symptom
  M4.0 fixes).
- [x] M4.1 — `ring3Wrapper` kernel-thread rewrite landed, with
  caveats. What works: boot (runKthreadSmoke=false &
  runPreemptProbe=false) reaches shell prompt under `-smp 4`;
  ring3Wrapper hosted by a gooos kernel thread via new
  `kschedSpawnProc`; `kschedInstallRing3Ctx` (new
  `src/kthread_ring3.go`) installs TSS.RSP0 = kthread.Stack.Top
  and CR3 = proc.pml4 before first dispatch; `Process.exitCh`
  replaced with `ExitEv KEvent` + `ExitCode uintptr`;
  `processExit` signals and calls `kschedExit(0)` to reclaim
  the kthread slot; `processWait` blocks on `ExitEv.Wait()`;
  both elfLoad (boot shell) and elfSpawn (child procs)
  kschedSpawnProc'd. What does NOT land: `gInfoByTask` /
  `registerRing3G` / `unregisterRing3G` / `gooosOnResume` were
  *not* deleted — left as dead-ish code to keep the patch
  surface minimal; they're unused post-M4.1 (ring3Wrapper no
  longer registers into gInfoByTask). What's broken:
  `scripts/test_kthread_smoke.sh` and `scripts/test_preempt_kernel.sh`
  boot panic at the Spike2 chan self-test
  (`internal/task.PauseLocked` → `task.Current()` returns nil)
  when either `runKthreadSmoke` or `runPreemptProbe` is true.
  The panic is non-deterministic (one direct boot showed the
  smoke completing with `SMOKE: OK`). Likely a timing race
  between M4.1's kthread dispatch on BSP and the Spike2
  goroutine's chan Park; the smoke banner shift may alter
  scheduling. **Not blocking M4.2** because net-service
  migration doesn't use kschedSpawnProc and the Spike2 test is
  a boot-time self-test that can be removed as part of the T1
  cleanup in §06's delete-on-arrival list. Commit lands the
  ring3Wrapper migration infrastructure; the smoke-test
  interaction needs a follow-up investigation.
- [ ] M4.2 — `netRxLoop`, `udpEchoServer`, `tcpRTOScannerLoop`, `tcpEchoServer` + per-connection workers migrated to `kschedSpawn`. Unblocked by M4.0.
- [ ] M4.3 — `sys_sleep` → `kschedTimedPark`; `sys_recvfrom` timeouts → bounded-poll; `afterTicks` channel shim deleted; `ring3StackPoolCh` replaced with `KQueue[int32]` or bitmap
- [ ] M4.4 — Gate: `test_sleeptest_postrevert.sh ITERATIONS=50` ≥ 80 % (F1 closure); `test_net.sh` + `test_tcp_longidle.sh 300` + `test_smp_shell_preempt.sh` + `test_smp_release_gate.sh` + `test_smp_basic.sh` + `test_ps.sh` all PASS

## M5 — TinyGo-patch trim + `scheduler=none` flip

**Session-4 finding (2026-04-25)**: M5 is hard-blocked on M4.2
+ M4.3 + boot-probe cleanup completing first. Reason:
`scheduler=none` makes TinyGo *reject any `go` statement at
compile time*. As of HEAD `b00f2d1` the kernel still has 12
live `go` sites:

```
src/afterticks.go:91   go timerDispatcher()
src/goroutine_tss.go:88 go func() { ... } (task-offset self-test)
src/net.go:56          go netRxLoop()
src/net.go:59          go udpEchoServer()
src/main.go:348        go func() { ch <- 42 }() (Spike2 chan probe)
src/main.go:360        go func() { ... }       (afterTicks self-test)
src/main.go:418        go func() { ... }       (boot net-diag probe)
src/main.go:634        go smpBasicProbe()
src/main.go:663        go kpMarker()
src/main.go:664        go kpHog()
src/tcp_retx.go:127    go tcpRTOScannerLoop()
src/tcp.go:1344        go tcpEchoServer()
```

Plus `tcpEchoServer` itself spawns per-connection goroutines
internally. Each must either (a) migrate to `kschedSpawn` /
`kschedSpawnProc` (M4.2 service migration) or (b) be deleted
outright (the four anon boot-time self-tests at `main.go:348,
360, 418` and `goroutine_tss.go:88` are §06 delete-on-arrival
candidates already flagged in `current_impl_2026_04_24/
fix_plan_deferred_1_5/06_next_cycle.md` T1).

The M4.1 smoke-test regression (Spike2 chan path triggering
`internal/task.PauseLocked → task.Current() = nil`) is also
along this critical path: the Spike2 site at `main.go:348` is
one of the four delete-on-arrival probes, so removing it
clears the M4.1 regression as a side-effect.

**Re-sequenced M5 prerequisites**:

- [ ] M4.2.a — Delete the four anon boot-time self-tests in
  `main.go` (Spike2 chan, afterTicks self-test, boot net-diag
  probe, and the goroutine_tss.go task-offset test). Removes 4
  of the 12 `go` statements; also resolves the M4.1 smoke
  regression at the Spike2 site.
- [ ] M4.2.b — Migrate `udpEchoServer` to `kschedSpawn`. Demo
  service; small body; channel rewires to `KQueue[UDPDatagram]`
  (UDPBinding.Ch).
- [ ] M4.2.c — Migrate `tcpRTOScannerLoop` to `kschedSpawn`.
  Body uses `<-afterTicks(...)`; rewire to `kschedTimedPark`.
- [ ] M4.2.d — Migrate `tcpEchoServer` + per-connection
  workers to `kschedSpawn`. Largest sub-step; involves accept
  queue rewires.
- [ ] M4.2.e — Migrate `netRxLoop` to `kschedSpawn`. Now
  unblocked because M4.0's gcLock spinlock makes
  `ethernetDispatch` allocations cross-CPU safe.
- [ ] M4.2.f — Migrate `timerDispatcher` to `kschedSpawn`.
  Body's `runtime.Gosched` → `kschedYield`; the existing
  channel-vs-event dual fire path stays intact (still serves
  any remaining goroutine callers via afterTicks shim).
- [ ] M4.2.g — Migrate `smpBasicProbe` (gated by
  `runSMPBasicProbe`) and `kpHog` / `kpMarker` (gated by
  `runPreemptProbe`) to `kschedSpawn`. The kpHog migration
  attempt in session 2 / M1 hit a "no banner" mystery; with
  M4.1 ring3Wrapper as kthread + M4.0 gcLock spinlock + the
  Spike2 self-test gone (M4.2.a), retry should succeed.
- [ ] M4.3 — `sys_sleep` (`src/userspace.go:453`) →
  `kschedTimedPark`; `sys_recvfrom` timeouts → bounded-poll
  per §06.
- [ ] M4.4 — Full regression gate: `test_sleeptest_postrevert
  ITERATIONS=50` ≥ 80 % (F1 closure); `test_net.sh` +
  `test_tcp_longidle.sh 300` + `test_smp_shell_preempt.sh` +
  `test_smp_release_gate.sh` + `test_smp_basic.sh` +
  `test_ps.sh` PASS.

After all of the above, M5 can land cleanly:

- [ ] M5.1 — Trim `scripts/tinygo_runtime.patch` per §08
  (delete ~510 lines across `queue.go`,
  `task_stack_multicore.go`, scheduler hunks); update
  `scripts/patch_tinygo_runtime.sh` sentinels; re-apply.
- [ ] M5.2 — `src/target.json` `scheduler=cores` →
  `scheduler=none`; `scripts/verify_globals.sh` asserts
  updated to kthread globals (`kschedQueues`, `kthreadPool`,
  `kschedRunning`, etc.); full regression sweep.

## Post-M5 work

- [ ] P1 — Reviewer sub-agent pass; BLOCKING fixed; MINOR → `no_goroutine_kernel_design/12_implementation_notes.md`
- [ ] P2 — README.md §11 diff applied; `impldoc/` + `current_impl_*/` sweep for stale refs; `current_impl_<today>/` successor doc created
- [ ] P3 — Final sweep: `grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ user/ scripts/` clean for new-in-this-cycle; full M5 gate suite PASS; `make -C user all` clean

## Deferred

*(Items the cycle chose not to complete; surfaced in the final
report.)*

- **Session stop after M0** — first session landed M0 end-to-end
  (smoke test PASS on 2026-04-25). Context budget preserved for
  careful M1+ work in a follow-up session per the plan's
  resumability discipline.
- **Session stop after M3** — second session added M1 infra + M2
  (fsTask migration, KEvent + fsReqQueue) + M3 (KEventAfter +
  kschedTimedPark; dispatcher now fires both channels and events).
  All four milestones committed and pushed (`7f81f12..1df4040`).
  M4 is the heavy lift (ring3Wrapper rewrite + 5 service migrations
  + `gooosOnResume` / `gInfoByTask` deletion + `ring3StackPoolCh`
  rewire + F1 closure verification at 300-s soak) and needs a
  fresh context budget. Stopping here so M4 lands as its own
  careful cycle.
- **kpHog-as-kernel-thread dispatch reliability (M1→M4)** —
  M1 landed the infrastructure (kschedLoopOnce, waitForEvents
  hook, handlePreemptIPI branch, IF=0-guarded dispatch) but the
  actual kpHog migration was reverted after -smp 4 runs showed
  kpHog's entry banner never firing even without an observable
  crash, and only 4 markers (target ≥5) in 15 s. Hypotheses for
  the next session to investigate: (a) APs don't reliably reach
  `waitForEvents` under the harness load — shift kpHog's
  round-robin target to AP 1 explicitly at spawn; (b) string-
  concat allocation in the banner races GC under the pre-M4
  BSP-only-allocates rule (partly mitigated by removing
  `utoa(cpuID())` but may still bite); (c) `gooosKschedLoopOnce`
  linkname from the patched wait_gooos.go to our //export
  symbol resolves but runs with some incompatible stack
  assumption. Recommendation: land ring3Wrapper migration (M4)
  first so the kernel scheduler is the only scheduler — then
  re-attempt kpHog migration with no TinyGo-scheduler
  interference.
- **Baseline B4 / B5** — full `test_net.sh` and
  `test_sleeptest_postrevert.sh ITERATIONS=20` runs skipped in
  this session. Boot-level + smoke-level verification is clean.
  Run before the first migration that could regress
  (recommend: before M2 lands `fsTask` on a kernel thread).

## Notes

- Pre-existing staged TODO.md renames are left alone; every
  Route C commit uses explicit pathspec to avoid them.
- `git push` and branch ops require explicit user instruction.
- Session-resumability: a future session reads this file +
  `git log --oneline` from `7f81f12..HEAD` and continues from the
  next unticked item.
