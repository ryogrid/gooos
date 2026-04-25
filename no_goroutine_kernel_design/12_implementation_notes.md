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

Other commits intermixed in the range:
- `cdc033e`, `3ca2cdb`, `5901490`, `23fdb3d` — session-stop notes
  + the §09 sequencing refinement that recorded the M4 / M5
  ordering finding.
- `6b2cac9` — repo hygiene (root `TODO_*.md` → `pasttodos/`).

### M4.1 attempt-1 root-cause hypotheses

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

The re-attempt strategy avoids both: keep `Process` shape
unchanged and use a top-level entry function that reads `proc`
from a slot-indexed side table.

## Remaining work

Listed in approximate execution order. Each item maps to one
or more `TODO_NOGOTIN.md` checkboxes.

### M4.1 re-attempt — ring3Wrapper as kernel thread (side-table)

The next step. Adds `kthreadHostedProc[kthreadPoolCap]*Process`
side table indexed by kthread `Slot`. Spawn helper
`kschedSpawnRing3Wrapper(proc)` records the entry; top-level
`ring3WrapperKT()` reads it; dispatch hook
`kschedInstallRing3Ctx(t)` writes CR3 + TSS.RSP0 from
`kthreadHostedProc[t.Slot]`. `Process` struct stays untouched;
`exitCh` channel notification path stays as-is (parent is still
a goroutine in this milestone).

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

## How this file is updated

- After each Route C commit, the commit body's "what landed"
  text is mirrored into the appropriate row of the §Progress
  table above (status + commit SHA + gate result).
- New milestone-shape changes (e.g. M4.0 splitting off from
  M5) are recorded in §Sequencing rationale.
- Reviewer pass (P1) findings: BLOCKING fixed in place;
  MINOR appended to §Open issues + risks with a one-line
  citation back to the commit that recorded them.
