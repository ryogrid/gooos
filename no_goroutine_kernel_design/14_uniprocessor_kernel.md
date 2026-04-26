# 14 — Uniprocessor kernel on BSP, SMP only for userspace

## 1. Context and motivation

Route C as landed in `13_post_m5_completion.md` (HEAD `a4cfe0d`)
spreads kernel kthreads across every online CPU via round-robin
spawn + work-stealing. The post-Route-C M6 bisection on branch
`smp-no-goroutine-in-kernel` (commits `e3561e6`, `6a5d0cb`,
`193e205`) measured this design failing under `qemu -smp 4` +
HMP `sendkey h e l p ret`:

| Config | helpRan | M8 fired | PF |
|---|---|---|---|
| Baseline (full kthreads, round-robin)        | 0/10 | 2/5  | **5/10** |
| `runMinimalKthreads` (no net/tcp services)   | 0/10 | 3/10 | 2/10 |
| Minimal + `timerDispatcher` BSP-pinned       | 0/10 | 7/10 | **0/10** |
| + no-steal hint (reverted)                   | 0/10 | 5/10 | 0/10 |
| + `preemptEnabled=false` (reverted)          | 0/10 | 5/10 | 5/10 |

Two distinct bugs were isolated:
- **Bug A** — cross-CPU `kschedSwitch` PF: `rip=0x100DA2`
  (`mov (%rdi),%rsp`), `addr=0x092BAA4ED8`. The `*KernelThread`
  popped from a ready queue by a peer-CPU stealer points to
  garbage. Disappears when `timerDispatcher` is pinned to BSP
  via `kschedSpawnAt(.., 0)`.
- **Bug B** — `kschedTimedPark`/`KEvent.Wait` resume failure:
  M8 fires but M9 (drain) = 0/10. The parked shell kthread is
  woken (Signal returns) but never returns to drain
  `gooosKbdRing`. Survives every M6 sub-step that was tried.

Both bugs originate in cross-CPU hand-offs:
`kschedWake → kschedPush → kschedLoop → kschedSwitch` when the
waker and the wakee live on different CPUs. Eliminating
cross-CPU kthread dispatch by construction is the cheapest
fix that addresses both bugs simultaneously.

The user has decided on the design shift:

> "Route C の SMP は user-space 並列性のためで、kernel 内 SMP は
> 要らない." — the kernel itself runs as a uniprocessor on BSP;
> APs exist only to host Ring-3 user processes.

Architectural precedent: Linux kernels pre-`CONFIG_PREEMPT`
(through ~2003) ran as a uniprocessor in kernel mode while
permitting userspace SMP; TinyGo `scheduler=tasks` is similarly
cooperative. The shift is conservative.

## 2. Invariants after the shift

- **U1** — every gooos kthread (`timerDispatcher`, `fsTask`,
  the boot shell `ring3WrapperKT`, `netRxLoop`, `udpEchoServer`,
  `tcpRTOScannerLoop`, `tcpEchoServer`, `netDiagLoop`, exec'd
  child `ring3WrapperKT`, `kpHog`, `kpMarker`, `smpBasicProbe`)
  is enqueued on `kschedQueues[0]` only. APs never have a
  ready-queue entry.
- **U2** — `kschedLoop` runs only on BSP. APs run a static
  idle loop (`for { sti(); hlt(); }`) inside `apSchedulerEntry`.
- **U3** — `kschedSteal` returns `nil` unconditionally. The
  steal block in `kschedLoop`'s body becomes dead code (kept,
  comment-gated, to ease M7 revert).
- **U4** — `kschedSpawn` and `kschedSpawnAt(.., n)` push to
  `kschedQueues[0]` regardless of `n`. The round-robin
  counter `kschedSpawnRRCounter` is no longer read.
- **U5** — `KEvent.Signal → kschedWake → kschedPush(t, 0)` is
  always a same-CPU push from BSP. `gooosWakeupCPU` is never
  called from the wake path (cross-CPU branch in `kschedPush`
  at `src/kthread_sched.go:109..111` becomes unreachable).
- **U6** — Ring-3 user processes still run on APs. The
  M6 milestone leaves the AP Ring-3 dispatch path unwired
  (APs idle in kernel mode); wiring is **M7 future work**.
- **U7** — `runMinimalKthreads` (introduced in `6a5d0cb`)
  becomes irrelevant for keyboard correctness. Default flips
  back to `false`; the `if !runMinimalKthreads { ... }` gates
  in `src/main.go:447..454` and `src/net.go:50..58` are
  removed (or comment-gated for revert).
- **U8** — preempt-IPI broadcast (`broadcastPreemptIPI` at
  `src/ipi.go:132`) is **not** removed — it still fires every
  100 Hz on BSP, but with empty AP queues it is functionally
  a no-op until M7 lands. APs receive vector 0xFB but
  `handlePreemptIPI` (`src/goroutine_irq.go:130..200`) sees
  `kschedRunning[c] == nil` and short-circuits.
- **U9** — `pitWakeAPs` (`src/pit.go:83`) becomes redundant
  (no AP work to wake). Comment-gated off; AP per-CPU LAPIC
  timers (initialised at `src/smp.go:309`) still fire and
  keep `pitTicks`-equivalent local progress for any future
  M7 logic.
- **U10** — every Route C sync primitive (`KEvent`, `KQueue`,
  `kschedTimedPark`, `kschedYield`, `kschedExit`,
  `fsReqQueue`, `udpDgramQueue`) keeps its existing API.
  Their cross-CPU edge cases simply stop firing because no
  caller crosses a CPU boundary.

## 3. Code-level inventory of changes

### 3.1 AP entry path

`src/smp.go:344` `apSchedulerEntry`:

```go
func apSchedulerEntry() {
    kschedLoop()
}
```

becomes

```go
func apSchedulerEntry() {
    // M6 / §14: APs idle in kernel mode. The kernel runs as a
    // uniprocessor on BSP; APs exist only to host Ring-3 user
    // processes (M7 future work). The previous `kschedLoop()`
    // call is preserved here as dead code via the comment so a
    // future `git revert` of the §14 commit restores the SMP
    // kernel scheduler in one diff.
    //   kschedLoop()
    for {
        sti()
        hlt()
    }
}
```

The comment-out form keeps the `kschedLoop` symbol referenced
exactly once (from `src/main.go`'s BSP entry); no dead-code lint
fires.

### 3.2 Kthread spawn sites

All sites listed below. The boot shell, `fsTask`,
`timerDispatcher`, and the smoke-test threads are already
BSP-pinned. The remaining sites change from round-robin to
explicit `kschedSpawnAt(.., 0)`:

| file:line | entry | current | change |
|---|---|---|---|
| `src/elf.go:257`        | boot shell    | `kschedSpawnRing3WrapperOnBSP` | unchanged |
| `src/main.go:444`       | fsTask        | `kschedSpawnAt(.., 0)`         | unchanged |
| `src/main.go:452`       | netDiagLoop   | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/main.go:637`       | smpBasicProbe | `kschedSpawnAt(.., apTarget)`   | → `kschedSpawnAt(.., 0)` (probe re-purposed; see §6) |
| `src/main.go:659`       | kpMarker      | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/main.go:660`       | kpHog         | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/afterticks.go:102` | timerDispatcher | `kschedSpawnAt(.., 0)`        | unchanged (commit `6a5d0cb`) |
| `src/net.go:71`         | netRxLoop     | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/net.go:73`         | udpEchoServer | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/tcp.go:1347`       | tcpEchoServer | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/tcp_retx.go:131`   | tcpRTOScanner | `kschedSpawn`                   | → `kschedSpawnAt(.., 0)` |
| `src/process.go:439`    | exec'd child  | `kschedSpawnRing3Wrapper` (RR) | → see §3.5 |
| `src/kthread_smoke.go:66..67` | smokeA/B | `kschedSpawnAt(.., 0)` | unchanged |

### 3.3 `kschedSpawn` / `kschedSpawnAt` body

`src/kthread_lifecycle.go` `kschedSpawn` and `kschedSpawnAt`
both end in `kschedPush(t, target)`. Two equivalent paths exist
to enforce the BSP-only invariant:

- **Option A (lightest)**: change every call site (§3.2) and
  leave `kschedSpawn`/`kschedSpawnAt` untouched. Restoring SMP
  later requires reverting only those call-site commits.
- **Option B (defence-in-depth)**: also force `target = 0`
  inside `kschedSpawnAt` body, gated by a new `const
  uniprocessorKernel = true` in `src/preempt_config.go`:

  ```go
  if uniprocessorKernel {
      target = 0
  }
  ```

This doc recommends **Option A** for revertability (one
`git revert` per commit) plus a single short-circuit in
`kschedSpawnAt` to catch any caller this design missed:

```go
func kschedSpawnAt(name string, entry func(), targetCPU uint32) *KernelThread {
    // §14 invariant U4: kernel runs uniprocessor on BSP. Any
    // caller that asks for an AP slot is silently routed to BSP.
    if uniprocessorKernel {
        targetCPU = 0
    }
    ...
}
```

### 3.4 `kschedLoop` steal block

`src/kthread_sched.go:184..193` (the `if t == nil { for ... =
kschedSteal(...) }` block). Under U3, `kschedSteal` returns
`nil`, so the loop is harmless but wasted work. Wrap the block
in `if !uniprocessorKernel { ... }` so revert is one diff.

### 3.5 `kschedSteal`

`src/kthread_sched.go:132..141`. Under U3:

```go
func kschedSteal(from, to uint32) *KernelThread {
    if uniprocessorKernel {
        return nil
    }
    ...
}
```

### 3.6 `kschedSpawnRing3Wrapper` (round-robin)

`src/kthread_ring3.go:40..58` is used by `processExec`
(`src/process.go:439`) for exec'd children. Under U6 the
intended placement (M7) is "round-robin across APs to give
parallelism", but until the AP Ring-3 dispatch path is wired,
exec'd children must also land on BSP and run there
sequentially with the boot shell.

Edit shape:

```go
target := uint32(0)
if !uniprocessorKernel {
    target = kschedSpawnRRCounter
    kschedSpawnRRCounter++
    if numCoresOnline == 0 { target = 0 }
    else { target = target % numCoresOnline }
}
kschedPush(t, target)
```

### 3.7 IPI fanout

- `src/ipi.go:109..123` `gooosWakeupCPU`: callable but never
  called from the wake path under U5. Leave as-is; document it
  becomes M7-only.
- `src/ipi.go:132..157` `broadcastPreemptIPI`: still fires per
  U8. Leave unchanged.
- `src/lapic_timer.go:119` `broadcastPreemptIPI()` call: leave
  unchanged.
- `src/pit.go:67` call to `pitWakeAPs()`, `src/pit.go:83..100`
  body: comment-gate per U9:

  ```go
  if !uniprocessorKernel {
      pitWakeAPs()
  }
  ```

  Reason: with empty AP kthread queues there is no remote
  work to wake, and the IPI cost is wasted. The local AP LAPIC
  timer (initialised at `src/smp.go:309`) still drives any
  M7 future logic.

### 3.8 `runMinimalKthreads` and net spawn gates

Per U7:

- `src/preempt_config.go:108` `const runMinimalKthreads`:
  flip default `true → false`; doc comment notes it is now
  superseded by `uniprocessorKernel`.
- `src/main.go:447..454` (boot-time `if !runMinimalKthreads
  { netSpawnServices(); if e1000Found { kschedSpawn(...) } }`):
  drop the gate; `netSpawnServices()` is called
  unconditionally. The contained spawns themselves became
  BSP-pinned per §3.2.
- `src/net.go:50..58` `if !runMinimalKthreads { tcpInit() }`:
  drop the gate; `tcpInit()` always runs.

### 3.9 New flag

`src/preempt_config.go` adds:

```go
// uniprocessorKernel runs the gooos kthread scheduler on BSP only.
// APs are kernel-mode idle (sti; hlt) and reserved for Ring-3 user
// processes (dispatch path is M7 future work). When true:
//   - kschedSpawn / kschedSpawnAt always target CPU 0
//   - kschedSteal returns nil
//   - kschedLoop's steal block is bypassed
//   - apSchedulerEntry idles instead of calling kschedLoop
//   - pitWakeAPs is bypassed
//   - runMinimalKthreads has no effect (kept for revert ergonomics)
// See no_goroutine_kernel_design/14_uniprocessor_kernel.md.
const uniprocessorKernel = true
```

## 4. Lock-rank table review

`src/spinlock.go:7..37` ranks 1..17. Under U1..U5:

- Rank 13 (`fsReqQueue.lock`, `udpDgramQueue.lock`): producers
  may still be ISRs (cross-context) but never cross-CPU.
  Same-CPU ISR ↔ kthread is already correctly handled by
  `Spinlock.Acquire`'s `cli`.
- Rank 14 (`KEvent.lock`): same-CPU only. The "Signal drops
  e.lock before kschedWake" rule still matters only because
  `kschedPush` takes rank 15.
- Rank 15 (`kschedQueues[cpu].lock`): only `kschedQueues[0]`
  is ever contended. Other entries never see a write under
  U1.
- Rank 16 (`kthreadPoolLock`): all alloc/free calls happen
  on BSP under U2.
- Rank 17 (`serialLock`): unchanged behaviour.

The ranked table itself stays intact; the doc comment in
`src/spinlock.go` should add a paragraph noting that ranks
13..16 lose cross-CPU contention under U1.

## 5. Removed / dormant cross-CPU IPI paths

- `gooosWakeupCPU` (`src/ipi.go:109`): unreachable from the
  kthread wake path under U5. Kept verbatim — it is the only
  primitive M7 will reuse for AP Ring-3 dispatch wake.
- `handleWakeupIPI` (`src/ipi.go:92`, registered at
  `src/main.go:378`): receives the vector; with no `kschedPush`
  cross-CPU caller, it never fires from the kthread path. Boot
  retains it because `pitWakeAPs` may still call into it (when
  `uniprocessorKernel = false`). Leave as-is.
- `pitWakeAPs` (`src/pit.go:83`): comment-gated per U9.

No code is `rm`'d. The user's standing rule is `削除は避けて`
(avoid deletion).

## 6. Test impact

29 test scripts in `scripts/test_*.sh`. Categorisation under U1..U10:

### 6.1 Unaffected — must continue to PASS

`test_kthread_smoke.sh`, `test_ps.sh`, `test_net.sh`,
`test_tcp_phase[1-5].sh`, `test_tcp_longidle.sh`,
`test_tcp_latetiming.sh`, `test_keyboard_reliability.sh`,
`test_shell_background.sh`, `test_sleeptest_*.sh` (4),
`test_goprobe_*.sh` (3), `test_preempt_kernel.sh`,
`test_preempt_user.sh`, `test_smp_multi_boot.sh`,
`test_net_tap.sh`, `test_smp_stability_sample.sh`.
These do not assert kernel-goroutine distribution across CPUs.

### 6.2 Re-purposed for Ring-3 distribution (M7) or marked deferred

- `scripts/test_smp_basic.sh` — currently asserts kernel
  goroutines run on `cpuID > 0` via `smpBasicProbe`. Under U1
  no kthread runs on AP, so this assertion is structurally
  false. Re-purpose: assert *Ring-3 process* distribution via
  `cpuhog` / `smpprobe.elf`, OR mark deferred to M7 with a
  `result: SKIP — pending M7 user-Ring-3-on-AP dispatch`
  output line.
- `scripts/test_smp_shell_distribution.sh` — same as above.
- `scripts/test_smp_shell_preempt.sh` — `cpuhog` +
  `markerprint` under `-smp 4`. The current test injects via
  the autorun gate (no HMP `sendkey`), so it should still
  PASS once Ring-3 distribution lands; under M6 it is
  expected to FAIL (autorun runs on BSP only). Mark deferred.
- `scripts/test_smp_release_gate.sh` — 50×8 sampler that
  includes the three above. Mark deferred until those return.
- `scripts/test_smp_shell_smpprobe.sh` — relies on Ring-3
  distribution. Same as above.

### 6.3 Newly relevant — interactive keyboard

A new harness `scripts/test_run_smp_keyboard.sh` (10-iter
HMP `sendkey h e l p ret` under `qemu -smp 4`) becomes the
gating test for §14. Spec:

```
PASS: helpRan ≥ 9/10, PF = 0/10, M9 fired ≥ 9/10
```

This harness does not yet exist; one of the §7 commits adds it.

## 7. Step-by-step execution plan

Each step is one commit; each ends with a 10-iter measurement
on the new `test_run_smp_keyboard.sh` harness (added in step
0). The measurement decision rule for each step:

- **PASS**: ≥ 9/10 helpRan, 0/10 PF, ≥ 9/10 M9. Keep the
  commit, advance.
- **REGRESSION** (worse than baseline 0/10 success / 5/10 PF):
  revert via `git revert HEAD` and re-think.
- **PARTIAL** (improvement but below PASS bar): keep commit,
  continue (some steps are expected to be partial until the
  whole shift lands).

### Step 0 — add the keyboard-reliability harness

Add `scripts/test_run_smp_keyboard.sh` mirroring the M6
`bcgqc0hfi`-style script (10 QEMU runs, HMP sendkey via
python3 over UNIX socket). Commit subject:
`no-goroutine kernel/M6: add test_run_smp_keyboard.sh harness`.
No code changes. Baseline measurement: 0/10 PASS, 5/10 PF.

### Step 1 — introduce `uniprocessorKernel` flag (default true)

Add `const uniprocessorKernel = true` to
`src/preempt_config.go`. No other change. Build + run the
existing regression gates. Measurement: unchanged from
baseline (the flag has no consumer yet). Subject:
`no-goroutine kernel/M6: add uniprocessorKernel flag`.

### Step 2 — pin every kthread spawn to BSP

Edit the 6 round-robin spawn sites in §3.2:
`src/main.go:452,659,660`, `src/net.go:71,73`,
`src/tcp.go:1347`, `src/tcp_retx.go:131`. Also gate
`kschedSpawnAt` body to clamp `targetCPU = 0` under
`uniprocessorKernel`. Also gate
`kschedSpawnRing3Wrapper`'s round-robin block per §3.6.
Measurement: PF expected to stay at 0/10 (timerDispatcher
was already pinned by `6a5d0cb`); M9 expected to stay 0/10
(Bug B is unrelated to spawn placement). Subject:
`no-goroutine kernel/M6: pin all kthread spawns to BSP`.

### Step 3 — APs idle, no `kschedLoop`

Edit `src/smp.go:344` per §3.1. Also wrap
`src/kthread_sched.go:184..193` steal block per §3.4 and
`kschedSteal` per §3.5. Measurement: this is the structural
fix for Bug B. Expected ≥ 9/10 helpRan, 0/10 PF, ≥ 9/10 M9.
Subject: `no-goroutine kernel/M6: APs idle in kernel mode`.

### Step 4 — disable `pitWakeAPs`, drop `runMinimalKthreads` gates

Per §3.7 and §3.8: comment-gate `pitWakeAPs` call,
flip `runMinimalKthreads` default to `false`, drop the two
`if !runMinimalKthreads { ... }` gates in `src/main.go` and
`src/net.go`. Net services come back online (still BSP-pinned
per step 2). Measurement: net regression suite
(`test_net.sh`, `test_tcp_*.sh`, `test_tcp_longidle.sh`)
must PASS. Keyboard harness must remain at ≥ 9/10. Subject:
`no-goroutine kernel/M6: re-enable net services on BSP`.

### Step 5 — re-purpose / defer SMP distribution tests

Per §6.2: edit the 5 affected harnesses to either re-target
Ring-3 distribution or print `result: SKIP — pending M7`.
Subject: `no-goroutine kernel/M6: SMP distribution tests
deferred to M7`.

### Step 6 — cleanup + lock-rank doc update

Remove `kschedSpawnRRCounter` reads (the variable is now
write-only — keep declaration with a "M7 reserved" comment).
Update `src/spinlock.go:7..37` doc-comment with the §4
clarification. Subject:
`no-goroutine kernel/M6: lock-rank doc + RR counter cleanup`.

## 8. Verification matrix

End-state must show all of:

- `scripts/test_run_smp_keyboard.sh` (Step 0): **≥ 9/10**
  helpRan, **0/10** PF, **≥ 9/10** M9-drained.
- `scripts/test_kthread_smoke.sh` PASS.
- `scripts/test_ps.sh` PASS (`-smp 1` keyboard via HMP).
- `scripts/test_net.sh` PASS (services back on BSP, U7).
- `scripts/test_tcp_longidle.sh 15` PASS.
- `scripts/test_tcp_phase[1-5].sh` PASS.
- `scripts/test_smp_basic.sh`, `test_smp_shell_distribution.sh`,
  `test_smp_shell_preempt.sh`, `test_smp_release_gate.sh`,
  `test_smp_shell_smpprobe.sh`: SKIP per §6.2 (re-purpose
  in M7).
- `make build`, `make lint`, `make verify-globals` clean.
- Manual smoke: `make run-smp` accepts terminal keyboard
  input interactively (assuming the user-side build still
  uses `-display` for PS/2). The bug B "M9 = 0" disappears
  once Step 3 lands.

## 9. Rollback plan

Each step is a single commit. Revert order is the reverse of
the §7 plan. Each revert restores the prior measurement.

| Revert | Restores |
|---|---|
| Step 6 | doc-only |
| Step 5 | SMP-distribution test scripts |
| Step 4 | `runMinimalKthreads = true` + pitWakeAPs |
| Step 3 | AP `kschedLoop`, steal block, `kschedSteal` |
| Step 2 | round-robin spawn placement |
| Step 1 | the `uniprocessorKernel` flag (becomes a no-op) |
| Step 0 | removes the harness only |

Full rollback re-creates the pre-§14 SMP kernel scheduler.
Because every §14 change is comment-gated or flag-gated rather
than deleted, no code archaeology is needed — `git revert`
of the relevant range suffices.

## 10. Out of scope (deferred to M7)

- **Ring-3 dispatch on APs.** The mechanism for taking a user
  process's kthread off BSP and running it on an AP. Requires:
  - per-AP "current Ring-3 host" state machine,
  - cross-CPU CR3 + TSS.RSP0 install (already implemented at
    `src/kthread_ring3.go:82..100` `kthreadResumeRing3Ctx` —
    bring it back online when M7 lands),
  - a wake protocol from BSP (where `processExec` runs) to the
    target AP (`gooosWakeupCPU` is the right primitive).
- **`kschedSpawnRing3Wrapper` round-robin re-design** for M7.
- **User-side TinyGo runtime changes**. The user-side build
  remains `scheduler=tasks` (cooperative) — INVARIANT K5 from
  §01 is preserved.
- **Re-running the M5 release-gate**. Done at HEAD `a4cfe0d`;
  §14 lands on top.

### 10.1 Sequencing relative to existing design docs

- §02 (`kernel_thread_runtime.md`): adds an addendum noting
  that under §14, `kschedQueues[i>0]` are unused but kept.
- §04 (`preemption_and_isr.md`): preempt-IPI broadcast still
  fires; `handlePreemptIPI` on AP short-circuits because
  `kschedRunning[c] == nil`.
- §06 (`service_migration.md`): every service migrated to a
  kthread is now BSP-pinned. The service-migration table is
  still valid; placement is the only delta.
- §09 (`incremental_migration_plan.md`): §14 is the new
  M6 milestone; M7 = AP Ring-3 dispatch is logged here as a
  successor.
- §13 (`post_m5_completion.md`): closes; §14 supersedes for
  any keyboard/SMP-correctness conflict.

`00_index.md` gains a new entry; that TOC update is the
first commit of the execution cycle, not part of this design.
