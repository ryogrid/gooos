# 15 — Userspace SMP on APs (M7 design)

## 1. Context and motivation

`14_uniprocessor_kernel.md` § 10 deferred Ring-3 dispatch on
APs to a future M7 milestone. M6 ("uniprocessor kernel on
BSP") shipped on branch `uni-proc-kernel-but-usrprog-smp`,
commits `aad1a04..8ecbdac` (8ecbdac = M6.fix-1: chan→spinlock
substitution for `ring3StackPoolCh`). Today the kernel runs
as a uniprocessor on BSP and APs idle in `sti; hlt;` — there
is no concurrency benefit from `-smp N > 1` because no Ring-3
process can use an AP either.

**M7 deliverable**: exec'd Ring-3 children spawn onto AP
queues (round-robin), the AP's `apSchedulerEntry` runs a
Ring-3-only dispatch loop (`kschedLoopRing3Only`), and the
existing `kthreadResumeRing3Ctx` per-resume install
(`src/kthread_ring3.go:74-104`) handles cross-CPU CR3 +
TSS.RSP0 + CurrentPoolIdx without further change. The boot
shell stays BSP-pinned per
`src/elf.go:257`'s `kschedSpawnRing3WrapperOnBSP` call.
Service kthreads (`fsTask`, `timerDispatcher`,
`netRxLoop`, `udpEchoServer`, `tcpRTOScannerLoop`,
`tcpEchoServer`, `netDiagLoop`, smoke threads, kpHog,
kpMarker) stay on BSP per M6 invariants (U1 preserved).

**Outcome metric**: the 5 SKIP-gated SMP harnesses
(`scripts/test_smp_basic.sh`,
`scripts/test_smp_shell_distribution.sh`,
`scripts/test_smp_shell_preempt.sh`,
`scripts/test_smp_shell_smpprobe.sh`,
`scripts/test_smp_release_gate.sh`) re-gate against
**Ring-3** distribution (cpuhog + markerprint observed on
≥ 2 distinct cpuIDs within 15 s) instead of kernel-goroutine
distribution. A new harness
`scripts/test_ring3_distribution.sh` becomes the gating
test for M7. M6 invariants stay intact:
`scripts/test_run_smp_keyboard.sh` still ≥ 9/10,
`scripts/test_shell_post_exec_prompt.sh` still ≥ 8/10 with
0 panics.

This doc is the M7 contract for a future Claude Code
execution cycle. It is **plan only** — no code edits land
here. Implementation cycle uses `16_m7_execution_plan.md`
as the work order and `17_m7_test_strategy.md` as the
harness rewrite spec.

## 2. Invariants `R1..R12` (preserve / relax / supersede `U1..U10`)

| Tag | Statement | M6 status |
|---|---|---|
| R1  | Service kthreads (`fsTask`, `timerDispatcher`, net/tcp services, kpHog/kpMarker, smoke threads, smpBasicProbe) stay BSP-pinned. | preserves U1 |
| R2  | `kschedQueues[0]` is the only service-kthread queue ever populated. | preserves U1 |
| R3  | Only `ring3WrapperKT` instances may be enqueued on AP queues (`kschedQueuesRing3[ap]`). | new |
| R3a | Service-kthread spawn helpers `kschedSpawn` / `kschedSpawnAt` keep their `uniprocessorKernel` clamp (`src/kthread_lifecycle.go:25-28, 45-50`); `userspaceSMP` does not affect them. | preserves U4 |
| R4  | Boot shell (`src/elf.go:257`'s `kschedSpawnRing3WrapperOnBSP`) stays BSP-pinned (foreground-keyboard owner at boot). | supersedes U6 (boot-shell case preserved; broader Ring-3-on-AP enabled) |
| R5  | `gooosWakeupCPU` (`src/ipi.go:100-123`) is the AP wake primitive: BSP issues IPI 0xFC after pushing a Ring-3 host onto an AP queue. | restores U5 (cross-CPU push branch is hot again, but only for Ring-3 hosts) |
| R6  | `kschedSteal` (`src/kthread_sched.go:128-149`) returns nil for service kthreads always; for Ring-3 hosts, AP↔AP steal is allowed under M7. BSP never steals from AP, AP never steals from BSP. | partial-revert U3 |
| R7  | Foreground-process transitions (`src/process.go:160-167` `setForegroundProc`) remain serialised by `procLock` (rank 2). Cross-CPU foreground hand-off is safe via existing spinlock semantics. | new (cross-CPU edge case re-emerges) |
| R8  | `apSchedulerEntry` (`src/smp.go:344`) runs a Ring-3-only dispatch loop (`kschedLoopRing3Only`), not the full `kschedLoop`. The M6 `for { sti(); hlt(); }` idle is preserved as a `userspaceSMP=false` fallback for one-revert rollback. | supersedes U2 |
| R9  | `kthreadResumeRing3Ctx` (`src/kthread_ring3.go:74-104`) is reused as-is. Its per-resume install (CR3 + TSS.RSP0 + `CurrentPoolIdx`) is already cross-CPU safe by design (M4.1.b). | preserves the M4.1.b invariant |
| R10 | `runMinimalKthreads` semantics from M6 are unchanged (default `false`). The flag was a bisection facility; M7 doesn't rely on it. | preserves U7 |
| R11 | `broadcastPreemptIPI` (`src/ipi.go:132-157`) broadcast every 100 Hz becomes meaningful again on APs (Ring-3 preemption). `handlePreemptIPI` no longer short-circuits on APs that host a Ring-3 thread. | supersedes U8 |
| R12 | `pitWakeAPs` (`src/pit.go:83-100`) stays gated. Wake-on-push (R5) covers the Ring-3-host wake path; periodic 100 Hz broadcast for empty AP queues is wasted work. The gate flips from `!uniprocessorKernel` to a pure `false` (or stays as `!uniprocessorKernel`; the doc picks the simpler form below). | preserves U9 (with a clarification) |
| R13 | All Route C sync primitives (`KEvent`, `KQueue`, `kschedTimedPark`, `kschedYield`, `kschedExit`, `fsReqQueue`, `udpDgramQueue`, the post-M6.fix-1 spinlock-based `ring3StackPool`) keep their existing API. M7 reuses them as-is across CPU boundaries. | preserves U10 |

K5 (user-side TinyGo runtime untouched, `scheduler=tasks`)
is reaffirmed. M7 changes nothing under `user/`.

## 3. Architecture

The M7 architecture is a **two-tier ready queue**:

- **Service tier** (BSP only): `kschedQueues[0]`. Holds
  every long-lived kthread. Dispatched by `kschedLoop` on
  BSP from `apSchedulerEntry`/`main`'s BSP entry.
- **Ring-3 tier** (per AP): `kschedQueuesRing3[1..numCoresOnline-1]`.
  Holds only `ring3WrapperKT` instances. Dispatched by a
  new `kschedLoopRing3Only(cpu)` from `apSchedulerEntry`.

A `KernelThread` is identified as a Ring-3 host iff its
`kthreadHostedProc[t.Slot] != nil`
(`src/kthread_ring3.go:30`). The existing side table is the
M7 type tag; no new field on `KernelThread` is required.

### 3.1 Spawn path

`elfSpawn` (`src/process.go:319-441`) → `kschedSpawnRing3Wrapper(child)`
(`src/process.go:439`). Today
`kschedSpawnRing3Wrapper` (`src/kthread_ring3.go:40-68`)
is round-robin block-gated under `uniprocessorKernel` and
falls back to BSP. M7 re-enables the round-robin block but
**targets only AP queues** (1..numCoresOnline-1; never
queue 0). When `numCoresOnline == 1` (e.g. `make run`),
the spawn falls back to BSP queue 0 and the M7 dispatch
behaves exactly like M6.

### 3.2 Dispatch path

`apSchedulerEntry` (`src/smp.go:344`) currently:

```go
func apSchedulerEntry() {
    if uniprocessorKernel {
        for { sti(); hlt() }
    }
    kschedLoop()
}
```

M7 adds a third branch under a new flag `userspaceSMP`:

```go
func apSchedulerEntry() {
    if userspaceSMP {
        kschedLoopRing3Only(cpuID())
    } else if uniprocessorKernel {
        for { sti(); hlt() }
    } else {
        kschedLoop()
    }
}
```

`kschedLoopRing3Only(cpu)` is a near-clone of `kschedLoop`
(`src/kthread_sched.go:170-225`) with two differences:

- pops from `kschedQueuesRing3[cpu]` (not
  `kschedQueues[cpu]`);
- the steal block (`src/kthread_sched.go:184-193`) calls
  a new `kschedStealRing3(from, to)` that scans only
  `kschedQueuesRing3[*]` (never service-tier).

The dispatched `KernelThread` runs `ring3WrapperKT`
(`src/kthread_ring3.go:106-148`); the existing
`kthreadResumeRing3Ctx` install at line 143 + 147's
`jumpToRing3` covers the cross-CPU dispatch contract. No
new asm.

### 3.3 Wake protocol

BSP `processExec` → `elfSpawn` → `kschedSpawnRing3Wrapper`
→ `kschedPush(t, target_ap)`. `kschedPush`
(`src/kthread_sched.go:99-117`) already calls
`gooosWakeupCPU(target_ap)` when `target_ap != cpuID()`.
The AP wakes from `sti; hlt;`, pops the host from
`kschedQueuesRing3[ap]`, dispatches.

### 3.4 What stays the same

- `kschedSpawnRing3WrapperOnBSP`
  (`src/kthread_ring3.go:64-72`) — boot shell only.
- `kschedSpawn` and `kschedSpawnAt(.., n)` for service
  kthreads — under M7 still clamp to BSP via the
  `uniprocessorKernel` gate inside the function bodies
  (`src/kthread_lifecycle.go:25-28` and `45-50`). The flag
  stays `true` under M7; only the `userspaceSMP` flag
  toggles AP usage.
- `gooosWakeupCPU`, `handleWakeupIPI`,
  `broadcastPreemptIPI`, `handlePreemptIPI`. No changes
  to IPI vectors.
- All sync primitives: `KEvent`, `KQueue`,
  `kschedTimedPark`, `kschedYield`, `kschedExit`,
  `fsReqQueue`, `udpDgramQueue`, the post-M6.fix-1
  spinlock-based `ring3StackPool`.

## 4. Code-level inventory of changes

| file:line | current | M7 change | invariant |
|---|---|---|---|
| `src/preempt_config.go:128` | `const uniprocessorKernel = true` | unchanged (kept true) | R1, R10 |
| `src/preempt_config.go` (NEW after `uniprocessorKernel`) | — | `const userspaceSMP = false` (default off; flipped to `true` in M7's final commit) | R8 |
| `src/smp.go:344` | `apSchedulerEntry` calls idle loop under `uniprocessorKernel`, else `kschedLoop` | add `if userspaceSMP { kschedLoopRing3Only(cpuID()) }` branch *before* the M6 idle branch | R8 |
| `src/kthread_sched.go:23` | `var kschedQueues [maxCPUs]kschedReadyQueue` | add `var kschedQueuesRing3 [maxCPUs]kschedReadyQueue` (sibling) | R3 |
| `src/kthread_sched.go:170-225` | `kschedLoop()` (BSP service-tier) | unchanged | R1 |
| `src/kthread_sched.go` (NEW after `kschedLoop`) | — | `func kschedLoopRing3Only(cpu uint32)` — pops from `kschedQueuesRing3[cpu]`, steal via `kschedStealRing3` | R8 |
| `src/kthread_sched.go:128-149` | `kschedSteal` returns nil under flag | unchanged (still service-tier; still nil under flag) | R6 |
| `src/kthread_sched.go` (NEW after `kschedSteal`) | — | `func kschedStealRing3(from, to uint32) *KernelThread` — AP↔AP only, never touches `kschedQueues` | R6 |
| `src/kthread_sched.go:99-117` | `kschedPush(t, cpu)` writes `kschedQueues[cpu]` | extend: when caller is `kschedSpawnRing3Wrapper`, write `kschedQueuesRing3[cpu]` instead. Cleanest implementation: a new `kschedPushRing3(t, cpu)` that mirrors `kschedPush` against the Ring-3 queue. The existing `kschedPush` is unchanged for service kthreads. | R3, R5 |
| `src/kthread_ring3.go:40-68` | `kschedSpawnRing3Wrapper` round-robin block flag-gated to BSP | re-enable round-robin onto APs only when `userspaceSMP`. Target: `1 + (counter % (numCoresOnline-1))` when `numCoresOnline > 1`, else 0. Push via the new `kschedPushRing3`. | R3, R5 |
| `src/kthread_ring3.go:64-72` | `kschedSpawnRing3WrapperOnBSP` pins boot shell to BSP via `kschedPush(t, 0)` | switch to `kschedPushRing3(t, 0)` so the boot shell joins the Ring-3 tier on BSP. Without this, BSP's `kschedLoop` would pop the boot shell and dispatch it — but BSP doesn't run the Ring-3-only loop, so boot shell would never run. **CRITICAL for boot.** | R4 |
| `src/process.go:439` | `kschedSpawnRing3Wrapper(child)` for exec'd children | unchanged (the fix is inside `kschedSpawnRing3Wrapper`) | R3 |
| `src/elf.go:257` | `kschedSpawnRing3WrapperOnBSP(proc)` for boot shell | unchanged (the fix is inside `kschedSpawnRing3WrapperOnBSP`) | R4 |
| `src/main.go` (BSP entry) | runs `kschedLoopOnce` from `bspBootDone` onward via `elf.go`'s pump | also pump `kschedLoopRing3Only(0)` so the boot shell runs on BSP. The simplest form: extend the existing pump in `src/elf.go:258-266` to alternate between `kschedLoopOnce()` (service tier) and `kschedLoopRing3OnlyOnce(0)` (Ring-3 tier on BSP). A single combined `kschedLoopOnceAny()` is preferred. | R4 |
| `src/pit.go:67-72` | `pitWakeAPs()` gated under `!uniprocessorKernel` | unchanged. Wake-on-push (R5) is sufficient for the Ring-3 tier; the periodic 100 Hz broadcast adds no value when AP queues are empty. | R12 |
| `src/ipi.go:132-157` | `broadcastPreemptIPI()` fires every 100 Hz from BSP | unchanged. AP `handlePreemptIPI` already short-circuits when `kschedRunning[ap] == nil`; under M7 with a Ring-3 host running, the IPI now triggers a real reschedule. | R11 |
| `src/spinlock.go:7-90` | lock-rank table 1..17 | doc-comment addendum: ranks 13..16 regain cross-CPU contention for Ring-3 tier. New rank 15a for `kschedQueuesRing3[cpu].lock` (same rank as `kschedQueues[cpu].lock`; never nested with each other). | R7 |
| `scripts/test_smp_basic.sh` etc. (5 harnesses) | SKIP under `^const uniprocessorKernel = true` | change SKIP gate to `^const userspaceSMP = true && grep -q ring3DistributionAssertion`. Re-purpose assertion to Ring-3 distribution. Detail in `17_m7_test_strategy.md`. | R8 outcome |
| `scripts/test_ring3_distribution.sh` (NEW) | — | M7 gating harness: cpuhog + markerprint via autorun, ≥ 2 distinct cpuIDs in markerprint output within 15 s | R8 outcome |

## 5. Per-CPU state for Ring-3 on AP

The existing per-CPU machinery is sufficient. Confirm:

- `src/percpu.go:22-34` `PerCPU` struct fields:
  - `CurrentPoolIdx int32` (offset 40) — set by
    `kthreadResumeRing3Ctx` at line 99
    (`src/kthread_ring3.go`); read by `currentProc()`
    (`src/process.go:202-217`).
  - `CurrentPML4 uintptr` (offset 32) — set by
    `kthreadResumeRing3Ctx` at line 101 via `writeCR3`.
  - `TSSPtr uintptr` (offset 16) — set once per AP by
    `gdtInitPerCPU` (`src/gdt.go:146-202`); the per-AP TSS
    already exists.
  - `WantReschedule uint32` (offset 28) — set by
    `handleLAPICTimer` (preempt-IPI path); consumed by
    `kschedLoopRing3Only`'s post-dispatch return path
    (the same `kschedLoop` shape that already exists).
  - `APICID uint32` (offset 24) — latched at AP boot
    (`src/smp.go:291,321`).
- `tssSetRSP0` (`src/gdt.go:135-144`) writes to the
  current CPU's TSS (offset 4 / RSP0). Per-resume install
  in `kthreadResumeRing3Ctx` (line 103) calls this — works
  on any CPU because it indexes via `cpuID()`.

**Nothing new is required at the per-CPU layer.** The
M4.1.b cross-CPU re-install machinery, written
defensively for "any CPU might dispatch this kthread next
time", is exactly what M7 needs.

## 6. Lock-rank review (`src/spinlock.go:7-90`)

Re-introduce cross-CPU contention on:

- **Rank 2** (`procLock`) — `setForegroundProc` /
  `getForegroundProc` may now be called from BSP (boot
  shell) and from any AP (exec'd child takes / releases
  foreground). Already spinlock-protected; no rank change.
- **Rank 14** (`KEvent.lock`) — kthread waiting on Ring-3
  process exit (`processWait` poll loop, M6.fix-1's
  `proc.Exited` path) crosses CPUs when the parent runs
  on BSP and the child on AP. Spinlock semantics already
  cover this.
- **Rank 15** (`kschedQueues[cpu].lock`) — service tier
  is BSP-only; no change.
- **Rank 15a** (NEW: `kschedQueuesRing3[cpu].lock`) —
  same rank as 15; the two queues are never nested with
  each other. Push from BSP to AP queue: holds 15a only.
  AP↔AP steal: holds 15a once (peer queue), then 15a
  again (own queue) only via the unchanged
  push-after-steal pattern; existing `kschedSteal`
  comments cover the pattern (`src/kthread_sched.go:128-149`).
- **Rank 16** (`kthreadPoolLock`) — alloc/free now
  happens on any CPU (Ring-3 host alloc on BSP via
  `processExec`; free on AP via `kthreadPoolFree` inside
  `kschedLoopRing3Only`). Spinlock; no rank change.

Doc-comment update belongs to the M7 execution cycle's
Step 6 (cleanup), not this design.

## 7. Restored cross-CPU IPI paths

- `gooosWakeupCPU` (`src/ipi.go:100-123`) — hot. Every
  Ring-3 spawn from BSP onto an AP queue triggers it. AP
  wakes from `sti; hlt;` and pops.
- `handleWakeupIPI` (`src/ipi.go:86-98`) — hot on APs.
- `broadcastPreemptIPI` (`src/ipi.go:132-157`) — already
  fires every 100 Hz on BSP. Under M7, AP `handlePreemptIPI`
  no longer short-circuits when the AP is dispatching a
  Ring-3 host: the existing `kschedRunning[c] != nil` check
  evaluates true, and the existing safe-point logic
  (`src/goroutine_irq.go:130-200`) requests reschedule.
  Effect: cpuhog on AP1 cannot starve markerprint also on
  AP1; the preempt IPI forces a `kschedYield`-equivalent
  via `WantReschedule`.
- `pitWakeAPs` (`src/pit.go:83-100`) — stays gated. Per
  R12, the wake-on-push protocol covers Ring-3 dispatch;
  periodic broadcast is wasted work for empty AP queues.

The `wakeFirstSeen[ap]` instrumentation in `src/ipi.go:66`
becomes useful again — the `wake:NNNN` line in `netDiag`
output should show non-zero bits for APs that handled at
least one wake IPI under M7.

## 8. Test impact

5 SKIP-gated harnesses (currently skip on
`^const uniprocessorKernel = true`):

| Script | Pre-M6 assertion | M7 re-purpose |
|---|---|---|
| `scripts/test_smp_basic.sh` | `smp_basic_cpu=N` (N>0) from kernel-goroutine `smpBasicProbe` | run a Ring-3 binary (e.g. cpuhog) and assert its `cpuID=N` line appears with N>0 |
| `scripts/test_smp_shell_distribution.sh` | kernel-goroutine stealWork proof | autorun cpuhog + markerprint; assert ≥ 2 distinct cpuIDs across markerprint output |
| `scripts/test_smp_shell_preempt.sh` | cpuhog + markerprint preempt fairness on shared CPU | unchanged behaviour; the M7 build allows this to actually pass (Ring-3 preempt-IPI path becomes live) |
| `scripts/test_smp_shell_smpprobe.sh` | smpprobe.elf workers cpuID distribution | unchanged behaviour; passes once Ring-3 spawn distributes |
| `scripts/test_smp_release_gate.sh` | 50× sampler over the above | re-enable; threshold ≥ 95 % per Plan-05 |

SKIP gate: change from `^const uniprocessorKernel = true`
to `^const userspaceSMP = false` (gate on absence of M7).
Once M7's final commit flips `userspaceSMP = true`, the
harnesses run.

NEW harness:
`scripts/test_ring3_distribution.sh` — boot `-smp 4`,
autorun cpuhog + markerprint via the existing
`runSMPShellPreemptProbe` mechanism, capture serial log
for 15 s, assert `markerprint cpu=N` lines show ≥ 2
distinct N values. Mirrors
`scripts/test_smp_shell_preempt.sh`'s shape.

M6 invariants must remain green:
- `scripts/test_run_smp_keyboard.sh` ≥ 9/10 helpRan,
  0/10 PF, ≥ 9/10 M9.
- `scripts/test_shell_post_exec_prompt.sh` ≥ 8/10
  helloPrinted, 0/10 panics.

Detailed harness rewrites + new fixture spec in
`17_m7_test_strategy.md`.

## 9. Step-by-step execution plan (cross-reference)

The full step-by-step (Step 0 .. Step 7) lives in
`16_m7_execution_plan.md`. Summary:

- **Step 0** — add `scripts/test_ring3_distribution.sh`.
- **Step 1** — add `const userspaceSMP = false` to
  `src/preempt_config.go`.
- **Step 2** — add `kschedQueuesRing3` +
  `kschedPushRing3` + `kschedLoopRing3Only` +
  `kschedStealRing3` (no consumer yet).
- **Step 3** — wire `apSchedulerEntry` to
  `kschedLoopRing3Only` under flag.
- **Step 4** — re-enable Ring-3 round-robin spawn for
  exec'd children; switch `kschedSpawnRing3WrapperOnBSP`
  to `kschedPushRing3`.
- **Step 5** — re-purpose / un-SKIP the 5 deferred
  harnesses; update SKIP gate to `^const userspaceSMP = false`.
- **Step 6** — flip `userspaceSMP = true` default;
  lock-rank doc update.
- **Step 7** — README + `docs/` refresh (per `hoge.md`'s
  doc-update planning requirement).

Each step: one commit, decision rule (PASS / PARTIAL /
REGRESSION), expected measurement delta.

## 10. Verification matrix

End-state must show:

- `scripts/test_ring3_distribution.sh` (NEW): ≥ 9/10 PASS
  (≥ 2 distinct cpuIDs observed within 15 s).
- `scripts/test_run_smp_keyboard.sh`: ≥ 9/10
  (M6 invariant preserved).
- `scripts/test_shell_post_exec_prompt.sh`: ≥ 8/10
  helloPrinted, 0/10 panics (M6.fix-1 invariant preserved).
- `scripts/test_kthread_smoke.sh`, `test_ps.sh`,
  `test_net.sh`, `test_tcp_phase[1-5].sh`,
  `test_tcp_longidle.sh 15`: PASS unchanged.
- 5 SKIP-gated harnesses: PASS with M7 assertions
  (`scripts/test_smp_basic.sh`,
  `scripts/test_smp_shell_distribution.sh`,
  `scripts/test_smp_shell_preempt.sh`,
  `scripts/test_smp_shell_smpprobe.sh`,
  `scripts/test_smp_release_gate.sh`).
- `make build` / `make lint` / `make verify-globals`
  clean. `make -C user all` clean (K5 preserved).
- Reviewer sub-agent (M7 execution cycle): zero BLOCKING.
- `TODO_M7.md` (created by the M7 execution cycle's first
  commit): every box `[x]`.

## 11. Rollback plan

Each Step in `16_m7_execution_plan.md` is one commit;
revert order is the reverse of Step 0..7. The strongest
rollback is **flip `userspaceSMP = false`**:

- `apSchedulerEntry` falls through to the M6 idle loop.
- `kschedSpawnRing3Wrapper`'s round-robin block is
  bypassed (round-robin gated under `userspaceSMP`).
- `kschedSpawnRing3WrapperOnBSP` keeps the boot shell on
  BSP via `kschedPushRing3(t, 0)` — this still works
  because the BSP pump's combined
  `kschedLoopOnceAny()` polls the Ring-3 tier on BSP too.
- All M7-added types/functions stay defined (zero callers
  under flag); no symbol-removal needed.

Per-step `git revert` is also valid for granular rollback.

## 12. Out of scope (M8+)

- **Process migration after spawn.** Ring-3 hosts under
  M7 stay on the AP they were dispatched to. Migration
  (steal-driven) is part of `kschedStealRing3`'s design
  but is bounded — no live-migration of an in-flight
  syscall. M8 may add hot-running migration; M7 only
  steals when an AP queue empties.
- **User-side TinyGo runtime changes.** K5 invariant —
  `user/target.json:9` stays `"scheduler": "tasks"`. M7
  delivers kernel-mediated SMP only; intra-process
  parallelism is M9+ territory.
- **Cross-AP GC stop-the-world.** Service kthreads still
  run on BSP; the M5 STW protocol (`05_gc_integration.md`)
  is BSP-only. Ring-3 hosts on APs need the freeze IPI
  (vector 0xFD) wired to `handlePreemptIPI`-equivalent on
  APs so the conservative scanner can sweep their
  TSS.RSP0-rooted kernel stacks. M8.
- **e1000 IRQ steering to APs.** IRQ 11 stays BSP-only;
  net-RX kthread stays BSP-only (R1). M7 doesn't change
  this.

## 12.1 Reviewer findings — MINOR (recorded for M7 execution cycle)

Recorded by the M7 design-doc reviewer pass run at the close
of this design cycle. BLOCKING items (none) were fixed in place;
MINOR items captured here for the M7 execution cycle to address
opportunistically:

- **MINOR-1**: `file:line` ranges in §4 Code-level inventory
  drifted by 5-30 lines from current `8ecbdac` HEAD. Examples:
  `src/smp.go:344` `apSchedulerEntry` (function actually at
  line 351); `src/kthread_sched.go:170-225` `kschedLoop` (actual
  body 183-243); `src/kthread_sched.go:128-149` `kschedSteal`
  (actual 133-154); `src/pit.go:67-72` `pitWakeAPs` call site
  (actual 66-72). All cited symbols exist and resolve unambiguously
  via grep — only the line numbers drift. **Fix during the M7
  execution cycle**: as each Step touches a cited file, tighten
  the line range in the same commit; do not block Step
  progression on this.

## 13. Sequencing relative to existing design docs

- `02_kernel_thread_runtime.md` — addendum: M7 introduces
  `kschedQueuesRing3[]` as a sibling tier to
  `kschedQueues[]`. The single-queue design in §02 stays
  authoritative for service kthreads.
- `04_preemption_and_isr.md` — the preempt-IPI path
  becomes meaningful again on APs. No code change to the
  ISR; only the consumer (Ring-3 host present) changes.
- `06_service_migration.md` — service kthread placement
  table stays valid. M7 adds Ring-3 hosts to APs but does
  not migrate any service kthread.
- `09_incremental_migration_plan.md` — M7 is the
  successor to the M6 entry there. The §09 milestone
  table gains an M7 row in the M7 execution cycle's
  first commit (TOC update).
- `14_uniprocessor_kernel.md` — supersedes for any
  AP-dispatch question; M7 explicitly inverts U6.
- `00_index.md` — TOC entry for `15_*`, `16_*`, `17_*`
  added in the M7 execution cycle's first commit, not in
  this design cycle.
