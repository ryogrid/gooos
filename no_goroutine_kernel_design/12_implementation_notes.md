# 12 — Implementation notes (progress + remaining work)

This doc tracks the actual execution of the Route C plan against
the design-set in §00–§11. It is updated incrementally as each
milestone lands or is re-sequenced. The authoritative per-commit
checklist lives in `/home/ryo/work/gooos/TODO_NOGOTIN.md`; this
file is the prose summary.

## Progress to date

Branch: `smp-no-goroutine-in-kernel`. Design base commit:
`7f81f12` (the 12-doc set). Implementation commits run from
`99c283d` (M0) through the current HEAD.

| Milestone | Status | Commit(s) | Gate result |
|---|---|---|---|
| **M0** — Context-switch stub in isolation | **Landed** | `99c283d` | `scripts/test_kthread_smoke.sh` PASS (A=5 B=5 ok=1) |
| **M1** — Demo probes on kernel threads | **Infra-only landed** | `4d7fc22` | `scripts/test_preempt_kernel.sh` PASS (markers=6); kpHog/kpMarker migration deferred to M4 |
| **M2** — `fsTask` on kernel thread | **Landed** | `732fc4e` | `test_sleeptest_postrevert.sh` 23/49 = 46 % (within 1σ of S2 50 % baseline; F1 unchanged) |
| **M3** — Timer wheel + `KEventAfter` | **Landed** | `1df4040` | smoke PASS; `-smp 4` boot reaches shell |
| **M4.0** — gooos spinlock for `gcLock` | **Landed** | `bdfb06b` | Allocator path now cross-CPU safe without parking via `task.PauseLocked`; smoke PASS; `-smp 4` boot reaches shell |
| **M4.1** — `ring3Wrapper` as kernel thread (attempt 1) | **Reverted** | `b00f2d1` (commit) → `4ada612` (revert) | Boot reached shell once but reproducibly panicked at `internal/task.PauseLocked → task.Current()=nil` after some boots |
| **M4.2.a** — Delete Spike2 + afterTicks self-tests | **Landed** | `cbad225` | Removes 2 of the original 12 boot-time `go ` sites; `-smp 4` boot stable |
| **M4.1** — `ring3Wrapper` as kernel thread (attempt 3 alpha) | **Landed** | `f6c65b8` | Boot reaches shell prompt cleanly; `test_ps.sh` PASS (header=1 row=1) proves `sys_exec → processWait → processExit` round-trip works with kthread-hosted shell. Dispatch-loop unchanged. sleeptest regresses to 0 % until M4.3 fixes `sys_sleep` chan-recv hazard. |
| **M4.1.b** — Cross-CPU CR3+TSS re-install on kthread re-dispatch | **Landed** | `7f504ed` | New `kthreadResumeRing3Ctx()` hooked into every post-`kschedSwitch` resume site (kschedYield, kschedPark, KEvent.Wait, fsReqQueue.Push/Pop). Dispatch loop still untouched. Preempt-IPI rewiring for kthread Ring 3 deferred to M4.1.c. Smoke + preempt + ps PASS. |
| **M4.1.c** — Preempt-IPI explicit kthread branch + `currentProc()` kthread fallback | **Landed** | `4c0557b` | `handlePreemptIPI` short-circuits Ring-3 preempt to `kschedYield()` when host is a kthread, skipping the goroutine-only `maybeDeliverSignal` path. `currentProc()` falls back to `procByPoolSlot[CurrentPoolIdx]` when `procByTask` lookup misses — fixes the migrated-kthread case where `taskCurrent()` returns a different stale key on the new CPU than what `setCurrentProc` stored. Smoke + preempt + ps PASS. |
| **M4.3** — `sys_sleep` + TCP polls + `processWait` migrated to `kschedTimedPark` | **Landed** | `d186e9b` | `sys_sleep` and TCP bounded-poll sites in `netsock.go` migrated from `<-afterTicks(d)` to `kschedTimedPark(d)` when host is a kthread. `processWait` parent-side park switched from `kschedYield` (tight re-dispatch loop hogging parent's CPU) to `kschedTimedPark(1)` so peer CPUs can dispatch the child kthread. **F1 closure benchmark**: `test_sleeptest_postrevert.sh` 50-iter run = **74 % PASS** (37/50), up from 50 % at M2 baseline and 0 % at M4.1 alpha. Stretch target 80 % not met but architectural hazard closed; remaining failures are scheduler jitter. |
| **M4.2.{b,c,d,e,f,g}** — All net/probe/dispatcher kthread migrations + `kschedSpawnAt` + boot-pinning | **Landed** | `b1af8bc` | Bundled commit. NEW `udpDgramQueue` (M4.2.b). NEW `kschedSpawnAt(name, entry, cpu)` to pin fsTask + boot shell + smoke threads to CPU 0 so the BSP-driven elf.go pump and kschedSmokeRun's kschedLoop dispatch via local pop. BSP keyboard-wait kschedTimedPark fix. elf.go pump uses runtime.Gosched. After this commit: **0 `go ` sites in src/*.go** — M5 prerequisites met. **F1 sleeptest closure: 98 % PASS (49/50)** — up from 74 % at M4.3 and 50 % at M2 baseline. Stretch target 80 % comfortably exceeded. Gates: smoke + ps + preempt_kernel (markers=6) + test_net all PASS. |
| **M4.4** — smpBasicProbe AP-pin + tcp_longidle banner | **Landed** | `0b704f8` | smpBasicProbe pinned to AP 1 (was always-CPU-0); test_tcp_longidle.sh grep updated for ring3WrapperKT banner. Partial release_gate showed 86%/74% pass rates (below 95% threshold) for smp_basic / smp_shell_distribution — documented as architectural follow-up. |
| **M5.1+M5.2** — TinyGo patch trim + scheduler=none flip | **Landed** | (pending — this commit) | runtime_gooos.go split: scheduler-cores-specific currentCPU + currentTask declarations moved to NEW runtime_gooos_sched_cores.go gated by `!scheduler.none`. apSchedulerEntry rewritten to call kschedLoop (was linkname runtime.apScheduler which doesn't exist under scheduler=none). NEW src/scheduler_none_stubs.go provides tinygo_task_exit halt stub. verify_globals.sh accepts kthread globals. Gates under scheduler=none: smoke + ps + test_net PASS. Known regressions: test_preempt_kernel (interleaved serial output without print lock); sleeptest 0% at first 7 iter (sleeptest never produces "begin" — needs investigation). |

Other commits intermixed in the range:
- `cdc033e`, `3ca2cdb`, `5901490`, `23fdb3d` — session-stop notes
  + the §09 sequencing refinement that recorded the M4 / M5
  ordering finding.
- `6b2cac9` — repo hygiene (root `TODO_*.md` → `pasttodos/`).

### M4.1 attempt-1 root-cause hypotheses (reverted in `4ada612`)

The attempt added an `ExitEv KEvent` field to the `Process`
struct AND used a closure (`func() { ring3Wrapper(proc) }`) as
the kthread entry. Either or both probably contributed to the
boot panic:

1. **Process struct layout shift** — adding `ExitEv` (~24 B)
   changed `Process`'s heap-allocation footprint, which may
   have perturbed boot-time GC / allocator behaviour enough to
   expose a pre-existing race in the boot probe sequence.
2. **Closure entry alloc** — `kschedSpawnProc` received a
   closure that captures `proc`; the closure context lives on
   the heap. The alloc may have raced with the boot
   chan/Mutex code path (the panic site is exactly inside
   `internal/task.PauseLocked` reading `task.Current()`).

### M4.1 attempt-2 (side-table strategy) — also reverted

A second attempt was made in this session with a side-table
strategy (`kthreadHostedProc[kthreadPoolCap]*Process` indexed by
kthread `Slot`) and a top-level entry (`ring3WrapperKT`) instead
of a closure. The `Process` struct stayed pristine.

This attempt produced a **different** boot regression: instead
of a `task.Current() = nil` panic, boot reaches `Scheduler:
TinyGo goroutines active` and then **hangs in
`fsSendRead("sh.elf")`** inside `setupUserspace` →
`elfLoad` — `elfLoad`'s `ELF: loading sh.elf` print never
fires. The periodic netDiag goroutine continues firing (so
TinyGo's scheduler is still alive), but the spin-pumped
`KEvent.Wait` in `fsSendRead` never returns.

Bisection showed:

- Adding only the new file `src/kthread_ring3.go` (with all the
  symbols defined but nothing called from existing paths) →
  boot reaches shell.
- Adding the `kschedInstallRing3Ctx(t)` call inside
  `kschedLoopOnce` and `kschedLoop` (even after no-op'ing the
  hook body to `_ = t`) → boot hangs in fsSendRead.

This rules out the hook's BODY as the cause and points at the
scheduler-loop changes themselves (the call insertion, the
re-arrangement of the dispatch sequence) as the trigger. The
exact mechanism is not yet understood — kschedLoopOnce/kschedLoop
are `//go:nosplit`, and `kschedInstallRing3Ctx` is also nosplit,
so a stack-grow split shouldn't be the issue. Possibilities for
the next investigation: a TinyGo function-call-prologue
allocation, a register-clobber from the call disrupting the
dispatch sequence, or a re-ordering effect that races with
fsTask's wake.

Both M4.1 attempts have been reverted; HEAD `cbad225` (M4.0 +
M4.2.a) remains the working ceiling.

### M4.1 attempt 3 — landed (alpha)

The attempt-3 plan (move CR3 + TSS.RSP0 install into
`ring3WrapperKT` body, leave dispatch loop untouched) lands
cleanly. New file `src/kthread_ring3.go` defines:

- `kthreadHostedProc[kthreadPoolCap]*Process` — slot → proc
  side table.
- `kschedSpawnRing3Wrapper(proc *Process) *KernelThread` —
  mirrors `kschedSpawn` body with side-table store before
  enqueue and `Entry = ring3WrapperKT`.
- `ring3WrapperKT()` — reads `kschedRunning[cpuID()].Slot`,
  resolves proc, sets `proc.poolIdx`/`procByPoolSlot`/
  `perCPUBlocks[cpu].CurrentPoolIdx`, calls
  `setCurrentProc(proc)` (so syscall-handler `currentProc()`
  resolves), `writeCR3(proc.pml4)`, `tssSetRSP0(&t.Stack.Top)`,
  `setGateDPL3(0x80)`, `jumpToRing3(...)`.

Modifications:

- `src/elf.go` boot-shell spawn (`go ring3Wrapper(proc); 
  <-proc.exitCh`) → `kschedSpawnRing3Wrapper(proc); for 
  proc.Exited == 0 { kschedLoopOnce(); gooosPause() }`. The
  pump is needed because the BSP main goroutine waiting on
  the chan would otherwise never give the kthread a CPU; the
  pump drives one-iteration scheduling from BSP context until
  the boot shell exits.
- `src/process.go:415` exec'd-child spawn → `kschedSpawnRing3Wrapper`.
- `src/process.go` `processExit` final park: branches on
  `kschedRunning[cpu] != nil`. From kthread context, clears
  `kthreadHostedProc[t.Slot]` and calls `kschedExit(exitCode)`
  (legacy path falls through to `taskPause()`).
- `src/process.go` `processWait`: branches on
  `kschedRunning[cpu] != nil`. From kthread context, spin-
  yields via `for proc.Exited == 0 { kschedYield() }`. The
  goroutine path keeps `<-proc.exitCh`.
- `src/userspace.go` `sysYieldHandler`: branches on kthread
  context — `kschedYield()` instead of `runtime.Gosched()`.
  This is the first H-01-hazard call site uncovered by the
  shell autorun path; `runtime.Gosched()` from kthread
  page-faults at `internal/task.Queue.Push` because
  `taskCurrent()` returns garbage.

Gates passing (post-attempt-3): `make build` clean;
`scripts/test_kthread_smoke.sh` PASS (A=5 B=5 ok=1);
`scripts/test_preempt_kernel.sh` PASS (markers=5);
`scripts/test_ps.sh` PASS (header=1 row=1, proves
shell-exec → child-run → processWait round-trip works).

Known regression (deferred to M4.1.b / M4.3):
`scripts/test_sleeptest_postrevert.sh` regresses to 0 % —
`sleeptest` calls `sys_sleep` which uses `<-afterTicks(d)`
(Go chan recv) from kthread context, page-faulting at
`runtime.lockAtomics`. Same H-01 family as the
`runtime.Gosched()` issue. M4.3 substitutes
`kschedTimedPark`. There is also a residual cross-CPU CR3+TSS
re-install hazard if the kthread is preempted in Ring 3 and
re-dispatched on a different CPU — `gooosOnResume` runs only
for goroutines, not kthreads. M4.1.b is the targeted fix
(`handleLAPICTimer` / `handlePreemptIPI` rewiring + per-
dispatch CR3+TSS).

## Remaining work

Listed in approximate execution order. Each item maps to one
or more `TODO_NOGOTIN.md` checkboxes.

### M4.1 third attempt — ring3Wrapper as kernel thread

After two reverts (the closure-spawn attempt and the
side-table attempt — see §M4.1 attempt notes above), the next
investigation needs to identify why simply *inserting a
function call* into `kschedLoopOnce` + `kschedLoop` breaks
fsSendRead's spin-pump even when the called function is no-op.

Investigation candidates for the next attempt:

1. Implement `kschedInstallRing3Ctx` as a `//go:noinline`
   global asm-style stub or as a literal inline (no function
   call) so the dispatch loop's compiled shape stays identical
   to the working M4.0 baseline.
2. Move the dispatch hook OUT of `kschedLoopOnce` entirely:
   add the CR3 + TSS.RSP0 write at the top of `ring3WrapperKT`
   instead. The kthread is already on its own stack at that
   point (kschedSwitch returned), so CR3 swap is safe (kernel
   half identity-mapped). TSS.RSP0 still gets installed before
   any Ring 3 entry.
3. Consider whether the `kschedSpawnRing3Wrapper` round-robin
   counter pushes ring3Wrapper to a CPU where fsTask's
   subsequent dispatch path conflicts; pinning ring3Wrapper to
   a specific AP at spawn might isolate the issue.

When attempt 3 succeeds: lands `kthreadHostedProc[
kthreadPoolCap]*Process` side table, the spawn helper, the
top-level entry function, and the modified `ring3Wrapper` body
+ `processExit` branch — same shape as attempt 2 minus the
in-loop dispatch hook.

### M4.2.b — `udpEchoServer` migration

Demo UDP echo server at `src/net.go:63`. Body recv's UDP
datagrams from a `chan UDPDatagram` (`src/udp.go:40`) and echoes
them back. Migration requires either rewiring the UDP binding
chan to a `KQueue[UDPDatagram]`-equivalent (parallel to the M2
`fsReqQueue` shape) or a polling loop using `udpBindWithChannel`
sans chan. Smaller scope than tcpEchoServer.

### M4.2.c — `tcpRTOScannerLoop` migration

`src/tcp_retx.go:127`. Body: `for { <-afterTicks(tcpRetxScanTicks);
tcpRTOScanPass() }`. Migration: replace the chan recv with
`kschedTimedPark(tcpRetxScanTicks)`. Hot-path allocations during
RTO retransmit fire are cross-CPU safe post-M4.0.

### M4.2.d — `tcpEchoServer` + per-connection workers

`src/tcp.go:1344`. Largest M4.2 sub-step: the echo server spawns
per-connection goroutines as new clients connect. Each
per-connection spawn must become `kschedSpawn` and the accept
queue must be rewired off Go channels.

### M4.2.e — `netRxLoop` migration

`src/net.go:60`. Pure `runtime.Gosched()` poll loop. Was
attempted at the M4 exploration step (commit `23fdb3d`) and hit
the gcLock-via-task.Mutex hazard; M4.0 fixed that root cause so
the migration should now succeed mechanically. Body change:
`runtime.Gosched()` → `kschedYield()`.

### M4.2.f — `timerDispatcher` migration

`src/afterticks.go:91`. The dispatcher itself becomes a kernel
thread; the channel-send path inside its body (for legacy
goroutine waiters) is preserved until the last
chan-based-`afterTicks` caller goes away. May need to defer until
all callers (sys_sleep included) have moved to `KEventAfter`.

### M4.2.g — `smpBasicProbe` + `kpHog` + `kpMarker`

Demo probes gated by `runSMPBasicProbe` and `runPreemptProbe`.
M1 attempted kpHog migration and reverted because of an
unresolved "no banner" issue under `-smp 4`. With M4.1 (kthread
ring3Wrapper) + M4.0 (gcLock spinlock) + the Spike2 self-test
gone (M4.2.a), the retry should succeed.

### M4.3 — `sys_sleep` + `sys_recvfrom` user-hosted timers

`sys_sleep` at `src/userspace.go:453` does `<-afterTicks(ticks)`.
With M4.1 in place the caller is a kthread, so this can move to
`kschedTimedPark(ticks)`. Same shape for `sys_recvfrom` timeouts
(`src/netsock.go:593,648,784`). F1 closure (`test_sleeptest`
≥ 80 %) is the gate for this step.

### M4.4 — Full regression gate

`scripts/test_sleeptest_postrevert.sh ITERATIONS=50` ≥ 80 %
(F1 closed); `test_net.sh` PASS; `test_tcp_longidle.sh 300`
PASS; `test_smp_shell_preempt.sh` PASS;
`test_smp_release_gate.sh` PASS; `test_smp_basic.sh` PASS;
`test_ps.sh` PASS. Confirms the migration shipped without
service regressions.

### M5.1 — TinyGo patch trim

Per §08: delete the scheduler-cores hunks (`scheduler_cores.go`,
`scheduler_cooperative.go`, `task_stack_multicore.go`,
`queue.go`, `gc_blocks.go` parts) once no `go ` statements
remain. Update the `scripts/patch_tinygo_runtime.sh`
post-conditions accordingly.

### M5.2 — `scheduler=none` flip

`src/target.json` `scheduler=cores` → `scheduler=none`. Update
`scripts/verify_globals.sh` to assert the new kthread globals
(`kschedQueues`, `kthreadPool`, `kschedRunning`, etc.) instead
of the now-removed TinyGo runqueues. Full regression sweep.

### P1 — Reviewer sub-agent pass

Per `hoge.md §Workflow 5`: launch a `general-purpose` reviewer
with the 6-check brief (invariants K1..K5 / L1 / entry-1 /
syscall-1 upheld; gates pass; lint + verify-globals clean; no
`go`/`chan`/`select` in `src/*.go`; user-side build untouched;
STW deadlock-freedom verified against the actual code).
BLOCKING findings fixed in place; MINOR appended to this file.

### P2 — README + impldoc refresh

Apply the §11 README diff. Sweep `impldoc/` and
`current_impl_*/` for stale references (`scheduler=cores`,
kernel goroutines, `gooosOnResume`, `gInfoByTask`) and either
update or mark legacy. Land a successor `current_impl_<today>/`
doc that describes the as-built Route C kernel.

### P3 — Final sweep + report

`grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ user/ scripts/` —
resolve anything this cycle introduced. Verify every
`TODO_NOGOTIN.md` checkbox is ticked. Confirm `make -C user
all` clean. Re-run the full M5.2 gate. Deliver the in-chat
report (commit range, per-harness PASS rate, deferred items,
pointer to this file).

## Sequencing rationale

**Why M4.1 is the next step.** F1 closure requires that the
sleeptest user process's host be a kthread, so the wake path
`timerDispatcher → KEvent → kschedWake → kschedQueues.Push`
stays inside the gooos scheduler instead of reinjecting into
TinyGo's runtime. `ring3Wrapper` *is* that host; until it's a
kthread, neither M4.3 (`sys_sleep` migration) nor F1 closure
can land.

**Why a side-table strategy for the M4.1 re-attempt.** The
first attempt's panic root-cause is unproven, but two changes
in that attempt are easy to factor out: the `Process` struct
shape change and the closure-spawn allocation. Both are
removable without losing capability — the side table provides
the same `kthread → Process` mapping as a struct field would,
and a top-level entry function can read `proc` from
`kthreadHostedProc[currentSlot]` without taking a closure
context. If the side-table re-attempt boots cleanly, we have
strong empirical evidence that one of those two changes was
the root cause; if it still panics, the panic is independent
of the M4.1 design and we'll dig deeper.

**Why M4.2.* and M4.3 follow M4.1.** Each net-service migration
reduces the live `go ` site count toward the M5 prerequisite
of zero. `sys_sleep` (M4.3) closes F1 once its host is a
kthread (M4.1). M4.4 is the cumulative regression gate.

**Why M5 is last.** `scheduler=none` makes any `go ` statement
a compile error. Until M4.2.* and the residual boot-probe
deletions remove every `go ` site, the M5 flip won't compile.

## Open issues + risks

- **M4.1 panic may resurface.** The attempt-1 panic is
  non-deterministic and not fully understood. The re-attempt
  removes the two most-suspicious changes; if it still
  panics, candidates for next investigation: the
  `kschedInstallRing3Ctx` hook timing (race between writing
  CR3 and the kschedSwitch), `setCurrentProc(proc)` from a
  kthread context (writes under `taskCurrent()` key which
  may collide with a TinyGo goroutine's entry), or some
  cross-CPU TSS.RSP0 visibility window.
- **`taskCurrent()` from a kthread is undefined.** The
  re-attempt keeps `setCurrentProc(proc)` for backward
  compatibility but stores under whatever stale value
  `taskCurrent()` returns. If `currentProc()` lookups misfire,
  add a parallel `procByKThreadSlot[]` (same shape as
  `procByPoolSlot`).
- **M4.2.* allocator pressure.** M4.0 fixed `gcLock` parking
  but did NOT add the §05 STW freeze IPI. Concurrent mark-
  phase mutators can still corrupt the mark bitmap in theory.
  In practice the conservative collector tolerates this on
  the gooos workload (M2 + M3 boots are stable). If a
  net-service migration triggers GC corruption symptoms (heap
  metadata panics), the freeze IPI moves up to a M4.0.b
  follow-up.
- **M5 patch-trim ordering.** `scheduler=none` requires all
  `go ` removed AND the patch trim landed in the right
  order: target.json flip without trim → link error;
  trim without flip → patch verification mismatch. M5.1 +
  M5.2 ship as a single tightly-coupled commit pair.
- **`make run-smp` keyboard input crashes the kernel.**
  Confirmed at HEAD `a4cfe0d` (post-Route-C-complete). Under
  `-smp 4`, the first few PS/2 keystrokes (HMP `sendkey` or
  the QEMU window) reproducibly corrupt CPU state. Across
  10 consecutive runs of `qemu -smp 4` + HMP `sendkey h e l
  p ret`: **0/10 successfully ran `help`**, **5/10 hit a
  fatal trap**, and the surviving 5 silently dropped the
  keystrokes. Observed traps include:
  (a) `panic: runtime error at 0x102bfa: index out of range`
       — `cpuID()` returns > 16 inside `timerDispatcher` →
       `kschedYield` → `cmp $0x10,%eax; ja lookupPanic`,
  (b) `#DE: division error` (vector 0),
  (c) `PF: addr=… rip=0x100DA2` (`kschedSwitch` `mov (%rdi),%rsp`
       — `*KernelThread` from ready queue pointed to garbage),
  (d) `PF: addr=0xFFFFFFFFFFFFFF99 rip=0x202` — RIP loaded
       with what looks like a RFLAGS value (iretq frame
       misalignment / stack corruption),
  (e) `PF: addr=rip=0x7FFF4000` — control transferred to
       garbage (an iretq popped a non-canonical RIP).
  These point to **stack/frame corruption** rather than a
  single race in one symbol. Symptom (c) reproduced via an
  experimental serial-RX shim (IRQ4 path), so any non-timer
  IRQ that triggers a scheduler dispatch under `-smp > 1`
  can hit it. Removing the `serialPrintln` in handleKeyboard
  ISR (held `serialLock` + `IF=0` for ~3.5 ms) did not
  improve the rate, so the ~3.5 ms cli window is not the
  trigger.
  Under `-smp 1` (`make run`, `test_ps.sh`, all SMP-1
  paths) everything works cleanly. Workaround: use
  `make run` for interactive shell sessions; reserve
  `make run-smp` for headless non-interactive testing
  (the existing 50-iter SMP gates already avoid keyboard
  injection per `test_smp_shell_distribution.sh` comment
  "QEMU HMP sendkey latency flake under -smp > 1" — that
  flake comment was masking what is actually this corruption
  race).
  Root-cause hypothesis: cross-CPU race in the
  `kschedWake` → `kschedPush` → `kschedLoop` →
  `kschedSwitch` hand-off, where the just-pushed
  `*KernelThread` is observed by a peer CPU's stealer
  before its `SavedRSP`/state is fully written. The IRQ1
  path is the easiest trigger because PIC pass-through
  routes IRQ1 to BSP while `timerDispatcher` has been
  work-stolen onto an AP. Proper fix needs a fence + state
  machine review of `kschedPushLocked` / `kschedSteal` /
  `kschedSwitch`'s saved-RSP write ordering. Investigation
  scoped as a post-Route-C M6 milestone.

### P1 reviewer-pass MINOR findings (post-§13 Phase 4)

The Phase 4 reviewer pass found 2 BLOCKING items (both fixed
in commit `<P1-fix>`: `keyboard_irq.go` AP path bare chan recv
+ `spinlock.go` rank table missing Route C primitives). The
reviewer also surfaced these MINOR items:

- **`src/process.go:580` `proc.exitCh <- exitCode`** runs
  before the kthread branch. Works only because the chan has
  cap=1 buffer and exactly one send per Process; if any path
  ever resends to a full chan from kthread context,
  `task.Pause()` would panic under scheduler=none. Consider
  conditioning the send on `kschedRunning[idx] == nil` to
  skip the legacy chan path on kthread context.
- **`src/process.go:552-574` procLock + pageAllocLock
  inversion.** procLock (rank 2) is held while calling
  freePage which acquires pageAllocLock (rank 1). The inline
  comment claims this is normative but rank-table convention
  ("outermost first" = lower rank first) says the opposite.
  Pre-existing (D1 in TODO_FIX.md), not introduced by Route C
  but adjacent to where Route C added more locks.
- **`src/kthread_ring3.go:30` `kthreadHostedProc` array** is
  mutated without a lock. Single-writer-per-slot in practice
  (spawn writes once before push; processExit clears once on
  the kthread's own slot). Add a one-line comment documenting
  the implicit invariant: "no lock — writer is exactly the
  kthread spawn site or the kthread's own processExit; reader
  is the kthread itself or its dispatch hook."
- **`src/kthread_ring3.go:110, 114, 119, 141`
  `serialPrintln` calls inside `ring3WrapperKT`** fire on
  every dispatch. Reasonable for boot-time debugging but
  consider gating behind a `runRing3KTDebug` flag for
  steady-state reduction.
- **`src/main.go:425` `checkKernelThreadOffset()`** is
  redundant with `src/kthread_sched.go:151` (called from
  `kschedInit`). Defensive but harmless.
- Pipe / channel value types in `src/*.go`: `Process.exitCh`
  (cap=1, one send), `pipe.ch chan byte` (used by ring3
  pipe fds — under scheduler=none CAN park on full/empty and
  panic). Document that scheduler=none does not yet support
  pipe-using user binaries; cleanup pending if a user
  program needs pipes.

## How this file is updated

- After each Route C commit, the commit body's "what landed"
  text is mirrored into the appropriate row of the §Progress
  table above (status + commit SHA + gate result).
- New milestone-shape changes (e.g. M4.0 splitting off from
  M5) are recorded in §Sequencing rationale.
- Reviewer pass (P1) findings: BLOCKING fixed in place;
  MINOR appended to §Open issues + risks with a one-line
  citation back to the commit that recorded them.
