# 16 — M7 execution plan (Step 0..7)

Companion to `15_userspace_smp_on_aps.md` (the design
contract). This file is the work order for the future M7
execution cycle: one commit per Step, each ending with a
gating measurement on
`scripts/test_ring3_distribution.sh` (added in Step 0)
and the regression matrix from §10 of `15_*.md`.

Branch: `uni-proc-kernel-but-usrprog-smp` (continues M6).
Top of branch: `8ecbdac` (M6.fix-1). M7 commits land on
top.

Decision rule for each Step (mirrors `14_*.md` §7):

- **PASS**: gating measurement at or above its bar. Keep
  the commit, advance.
- **REGRESSION**: any §10 regression harness drops below
  threshold. Revert via `git revert HEAD` and re-think.
- **PARTIAL**: improvement but below the PASS bar. Keep
  the commit, continue (some Steps are expected to be
  partial until the whole shift lands; e.g. Step 2 adds
  unused symbols, Step 3 wires them).

## Bootstrap (workflow §1, before Step 0)

Create `TODO_M7.md` at the repository root with one
checkable line per Step (0..7) plus baseline +
reviewer-pass + final-sweep items, mirroring `TODO_M6.md`.
Initial commit subject: `no-goroutine kernel/M7: bootstrap
TODO_M7.md tracker`.

## Baseline (workflow §2)

Run on `8ecbdac`:
- `scripts/test_run_smp_keyboard.sh` (M6 invariant).
- `scripts/test_shell_post_exec_prompt.sh` (M6.fix-1).
- `scripts/test_kthread_smoke.sh`, `test_ps.sh`,
  `test_net.sh`.

Record results in `TODO_M7.md ## Baseline`. Any
pre-existing failure must be understood — Route C or M6
should not be blamed on M7.

## Step 0 — add `scripts/test_ring3_distribution.sh`

Mirror `scripts/test_smp_shell_preempt.sh`'s shape:

- enable `runSMPShellPreemptProbe = true` via sed
  (existing pattern from `harness_lib.sh`),
- boot `qemu -smp 4` with serial-file capture,
- wait 15 s,
- count distinct cpuIDs from `marker <iter> cpu=<N>` lines
  in the markerprint output,
- PASS if ≥ 2 distinct cpuIDs observed,
- restore the config flag on exit (`harness_lib.sh`'s
  `restore_config` pattern).

No code changes. **Baseline measurement on M6 build:
0/N distinct (cpuhog runs entirely on BSP).**

Commit: `no-goroutine kernel/M7: add test_ring3_distribution.sh harness`.

## Step 1 — add `userspaceSMP` flag

Edit `src/preempt_config.go` after `uniprocessorKernel`:

```go
// userspaceSMP enables Ring-3 process dispatch on APs while
// the kernel itself stays uniprocessor on BSP. When false,
// M6 semantics apply: APs idle in `sti; hlt;` and exec'd
// children land on BSP. When true, exec'd children
// round-robin onto AP queues; APs run kschedLoopRing3Only.
//
// See no_goroutine_kernel_design/15_userspace_smp_on_aps.md.
const userspaceSMP = false
```

No consumer yet. Build + regression unchanged.
Commit: `no-goroutine kernel/M7: add userspaceSMP flag`.

## Step 2 — Ring-3 tier scaffolding (no consumer)

Add to `src/kthread_sched.go` (after the existing service-
tier declarations and helpers):

- `var kschedQueuesRing3 [maxCPUs]kschedReadyQueue` — sibling
  to `kschedQueues`.
- `func kschedPushRing3(t *KernelThread, cpu uint32)` —
  near-clone of `kschedPush` (`src/kthread_sched.go:99-117`)
  that writes `kschedQueuesRing3[cpu]` instead of
  `kschedQueues[cpu]`. Same `gooosWakeupCPU(cpu)` cross-CPU
  wake on `cpu != cpuID()`.
- `func kschedPopRing3(cpu uint32) *KernelThread` —
  near-clone of `kschedPop`.
- `func kschedStealRing3(from, to uint32) *KernelThread` —
  near-clone of `kschedSteal` (`src/kthread_sched.go:128-149`)
  but **not flag-gated to nil**; it scans Ring-3 queues
  only. Per R6: BSP is never a steal source for the Ring-3
  tier (BSP's Ring-3 queue holds the boot shell only and
  must not be stolen — boot shell stays foreground-keyboard
  owner on BSP).
- `func kschedLoopRing3Only(cpu uint32)` — near-clone of
  `kschedLoop` (`src/kthread_sched.go:170-225`):
  ```go
  for {
      t := kschedPopRing3(cpu)
      if t == nil {
          for i := uint32(1); i < numCoresOnline; i++ {
              src := (cpu + i) % numCoresOnline
              if src == 0 { continue } // never steal from BSP
              t = kschedStealRing3(src, cpu)
              if t != nil { break }
          }
      }
      if t == nil {
          sti(); hlt(); cli(); continue
      }
      if KState(t.State) == KStateExiting {
          kthreadPoolFree(t); continue
      }
      kschedRunning[cpu] = t
      t.State = uint32(KStateRunning)
      t.OwnerCPU = cpu
      t.Quantum = kschedDefaultQuantum
      kschedSwitch(t, &kschedBootstrap[cpu])
      kschedRunning[cpu] = nil
  }
  ```

All four new symbols are unreachable until Step 3 wires
them. Build + regression unchanged.

Commit: `no-goroutine kernel/M7: Ring-3 tier scaffolding (kschedQueuesRing3 + helpers)`.

## Step 3 — APs dispatch Ring-3 tier under flag

Edit `src/smp.go:344` `apSchedulerEntry`:

```go
func apSchedulerEntry() {
    if userspaceSMP {
        kschedLoopRing3Only(cpuID())
        return
    }
    if uniprocessorKernel {
        for { sti(); hlt() }
    }
    kschedLoop()
}
```

Also extend the BSP pump in `src/elf.go:258-266` to drive
the Ring-3 tier on BSP as well — without this, the boot
shell (R4) never gets dispatched. Cleanest form: add a
sibling `kschedLoopRing3OnlyOnce(0)` (a `kschedLoopOnce`-
shaped one-shot for the Ring-3 tier on BSP) and call both
from the existing pump:

```go
for proc.Exited == 0 {
    kschedLoopOnce()           // service tier on BSP
    kschedLoopRing3OnlyOnce(0) // Ring-3 tier on BSP
    runtime.Gosched()
}
```

Add `func kschedLoopRing3OnlyOnce(cpu uint32)` to
`src/kthread_sched.go` mirroring `kschedLoopOnce`
(`src/kthread_sched.go:227-308`).

`userspaceSMP` is still `false` so behavior is unchanged
in the default build. Toggle locally (`sed -i 's/= false/= true/'`)
to exercise.

**Measurement** (toggled `true`):
`scripts/test_run_smp_keyboard.sh` must remain ≥ 9/10
(boot shell still works);
`scripts/test_shell_post_exec_prompt.sh` must remain ≥ 8/10
(exec round-trip still works — child runs on BSP via the
combined pump because `kschedSpawnRing3WrapperOnBSP` still
targets BSP).

Commit: `no-goroutine kernel/M7: APs dispatch Ring-3 tier under userspaceSMP`.

## Step 4 — exec'd children land on AP queues

Edit `src/kthread_ring3.go:40-68` `kschedSpawnRing3Wrapper`:

```go
func kschedSpawnRing3Wrapper(proc *Process) *KernelThread {
    t := kschedSpawnInternal("ring3", ring3WrapperKT)
    kthreadHostedProc[t.Slot] = proc
    target := uint32(0)
    if userspaceSMP && numCoresOnline > 1 {
        target = 1 + (kschedSpawnRRCounter % (numCoresOnline - 1))
        kschedSpawnRRCounter++
    } else if !uniprocessorKernel {
        target = kschedSpawnRRCounter
        kschedSpawnRRCounter++
        if numCoresOnline == 0 {
            target = 0
        } else {
            target = target % numCoresOnline
        }
    }
    kschedPushRing3(t, target)
    return t
}
```

Edit `src/kthread_ring3.go:64-72`
`kschedSpawnRing3WrapperOnBSP`:

```go
func kschedSpawnRing3WrapperOnBSP(proc *Process) *KernelThread {
    t := kschedSpawnInternal("ring3", ring3WrapperKT)
    kthreadHostedProc[t.Slot] = proc
    kschedPushRing3(t, 0)  // boot shell into Ring-3 tier on BSP
    return t
}
```

**Critical**: switching from `kschedPush(t, 0)` to
`kschedPushRing3(t, 0)` is what makes the combined BSP
pump from Step 3 actually dispatch the boot shell. Without
this change, the boot shell would land on
`kschedQueues[0]` (service tier) which is fine — but the
service-tier `kschedLoop` doesn't exist on BSP under
`userspaceSMP=true` either (BSP runs its own combined
pump, not `kschedLoop`).

**Measurement** (toggled `true`): the M7 PASS bar.
- `scripts/test_ring3_distribution.sh`: ≥ 9/10 (was 0/N).
- `scripts/test_run_smp_keyboard.sh`: ≥ 9/10 unchanged.
- `scripts/test_shell_post_exec_prompt.sh`: ≥ 8/10 unchanged.

Commit: `no-goroutine kernel/M7: exec'd children round-robin onto AP queues`.

## Step 5 — re-purpose the 5 deferred SMP harnesses

Edit each of:
- `scripts/test_smp_basic.sh:20-24`
- `scripts/test_smp_shell_distribution.sh:26-30`
- `scripts/test_smp_shell_preempt.sh:24-28`
- `scripts/test_smp_shell_smpprobe.sh:16-20`
- `scripts/test_smp_release_gate.sh:25-29`

Change SKIP gate from:
```bash
if grep -q '^const uniprocessorKernel = true' src/preempt_config.go 2>/dev/null; then
    echo "result: SKIP — pending M7 ..."
    exit 0
fi
```
to:
```bash
if grep -q '^const userspaceSMP = false' src/preempt_config.go 2>/dev/null; then
    echo "result: SKIP — userspaceSMP off; flip in src/preempt_config.go to enable M7 SMP dispatch"
    exit 0
fi
```

For `test_smp_basic.sh` and `test_smp_shell_distribution.sh`,
also re-purpose the assertion: count distinct `cpu=N`
values from a Ring-3 binary's output (cpuhog or smpprobe.elf
markers) instead of `smp_basic_cpu=N` from
`smpBasicProbe`. Detail in `17_m7_test_strategy.md`.

`scripts/test_smp_shell_preempt.sh` and
`scripts/test_smp_shell_smpprobe.sh` keep their existing
assertions; under M7 they simply pass for the first time.

`scripts/test_smp_release_gate.sh` re-runs the matrix 50×.

**Measurement**: each of the 5 SKIP-gated harnesses passes
when `userspaceSMP=true` is built into the ISO.

Commit: `no-goroutine kernel/M7: re-purpose 5 SMP-distribution harnesses`.

## Step 6 — flip default + lock-rank doc + cleanup

Edit `src/preempt_config.go`: flip `userspaceSMP = true`
(default on).

Edit `src/spinlock.go:7-90` lock-rank doc-comment to add
the rank 15a (`kschedQueuesRing3[cpu].lock`) clarification
from `15_*.md` § 6:

```
//   --- M7 (Userspace SMP on APs) primitives ---
//  15a. kschedQueuesRing3[cpu].lock — per-CPU Ring-3
//       host ready queue (src/kthread_sched.go). Same rank
//       as 15; never nested with kschedQueues[cpu].lock
//       (different tier; service vs. Ring-3 host). AP↔AP
//       steal: holds 15a once, drops, then 15a again.
```

Edit `src/kthread_sched.go:44-55`'s
`kschedSpawnRRCounter` doc comment: drop the M7-reserved
note (now actively read).

**Measurement**: full §10 verification matrix (`15_*.md`).
All gates green at default build.

Commit: `no-goroutine kernel/M7: enable userspaceSMP by default + lock-rank doc cleanup`.

## Step 7 — README + `docs/` refresh

Per `hoge.md`'s § "Documentation update planning":

### `README.md`

Find the `make run-smp` section (currently updated for M6
to read "kernel runs as a uniprocessor on the BSP regardless
of `-smp N`. APs are kernel-mode idle ... Userspace SMP
... is the M7 follow-up"). Replace with M7 reality:

> Under M7, the gooos kernel runs as a uniprocessor on
> the BSP for all kernel-side work (services, interrupts,
> I/O); user processes run in parallel on APs. Exec'd
> children round-robin onto APs, the boot shell stays on
> BSP. `make run-smp` (`-smp 4`) gives ~3× userspace
> throughput on a 4-core host while keeping kernel-side
> SMP race surface zero. Toggle off via
> `userspaceSMP = false` in `src/preempt_config.go`.

### `docs/networking_demos.md`, `docs/repo_layout.md`, `docs/user_programs.md`

Sweep for any stale "uniprocessor kernel" / "BSP-only"
wording. Update or add a top-of-file pointer to
`no_goroutine_kernel_design/15_userspace_smp_on_aps.md`.
**Do not delete** any doc; superseding follows the project
convention (top-of-file pointer + create new file under
`current_impl_<today>/` if needed).

### `current_impl_2026_04_26/route_c_kernel.md`

Append an M7 note under "Known follow-ups":

> **M7 LANDED** (commit `<TBD>`): Ring-3 dispatch on APs
> per `no_goroutine_kernel_design/15_userspace_smp_on_aps.md`.
> Userspace SMP enabled by default
> (`userspaceSMP = true`). M6 keyboard / post-exec
> regressions stay green. New harness
> `scripts/test_ring3_distribution.sh` is the M7 gate.

### `00_index.md`

TOC entries for `15_*.md`, `16_*.md`, `17_*.md` belong to
the M7 execution cycle's **first** commit (per `hoge.md`)
— **not** Step 7. Step 7 only updates README + `docs/` +
`current_impl_*/`.

### Step 7 PASS gate

Doc-only step; the measurable PASS criteria are:

1. `grep -lE 'uniprocessor kernel|BSP-only|kernel runs as a uniprocessor'
   docs/*.md | wc -l` returns **0** (no stale wording in the docs
   set referenced from README).
2. `grep -c "M7" README.md` returns **≥ 1** (the M7 update is
   present).
3. `grep -c "M7" current_impl_2026_04_26/route_c_kernel.md` returns
   **≥ 1**.
4. All §10 verification-matrix harnesses still PASS at the same
   thresholds (no doc edit accidentally broke a `make` target or
   a script).
5. `make build` clean (the doc edits touch no code, but the build
   re-runs as a tripwire).

REGRESSION = any §10 harness drops below threshold; revert the
Step 7 commit. The doc edits themselves are non-regressive — Step
7's only failure mode is incomplete sweep coverage.

Commit: `no-goroutine kernel/M7: README + docs refresh`.

## Reviewer sub-agent pass (after Step 7)

Launch a `general-purpose` sub-agent with the brief:

- Read `no_goroutine_kernel_design/15_userspace_smp_on_aps.md`,
  `16_m7_execution_plan.md` (this file),
  `17_m7_test_strategy.md`, plus
  `14_uniprocessor_kernel.md` (the M6 contract).
- Read the implementation commits (range from M6.fix-1
  `8ecbdac` to the M7 head).
- Verify:
  1. Every M6 invariant `U1..U10` is preserved/relaxed/
     superseded as `15_*.md` §2 specifies.
  2. Every `file:line` in the inventories is real and
     correct in the implemented commits.
  3. `make build` / `make lint` / `make verify-globals`
     clean.
  4. No code path was deleted; every removed surface is
     flag-gated for one-revert rollback.
  5. The Step subjects + rollback plan match git history.
  6. K5 (user-side `scheduler=tasks`) preserved.
  7. The M6.fix-1 chan→spinlock pattern is referenced by
     any M7 producer/consumer primitive (none introduced
     under the current design — primitives reuse existing
     spinlock + queue shapes).
- Findings graded **BLOCKING** or **MINOR**. BLOCKING
  fixed in place; MINOR recorded in
  `12_implementation_notes.md` appendix.

## Final sweep + report

- `grep -rIn 'TODO\|FIXME\|XXX\|HACK' src/ user/ scripts/`
  — every result added by M7 resolved or tracked.
- Re-read `TODO_M7.md`; every `[x]`.
- Run the §10 matrix from `15_*.md` one final time.
- Confirm `make -C user all` clean.

Deliver report in chat:
- commit range (`<M7 first SHA>..<M7 last SHA>`),
- per-harness PASS rate from §10,
- deferred items (BLOCKING fixed in place vs. punted),
- pointer to `12_implementation_notes.md` appendix if
  MINOR findings landed there.
