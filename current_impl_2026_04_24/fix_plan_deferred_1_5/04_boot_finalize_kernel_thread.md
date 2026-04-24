# DEFERRED 4 — Boot-finalize kernel thread (A1)

## Scope & goal

**Scope (verbatim from `FINAL_REPORT.md §Deferred` item 4)**:
*Move the heavy work inside `bootActivatePostShellReady` out of
the first-`int 0x80` ISR context into a dedicated boot-finalize
kernel thread.*

**Goal**: `sys_shell_ready` (`#38`) becomes a tiny, deterministic
ISR handler. All heavy work — phase-lock acquires, optional
serial prints, probe-goroutine spawns, virtual-wire restore —
runs on a dedicated kernel thread spawned at boot, parked on a
signal channel until the shell-ready event fires.

## Root-cause analysis

Current `bootActivatePostShellReady` at `src/main.go:604–639`:

- Is called from `sysShellReadyHandler`
  (`src/userspace.go:623`).
- Takes `preemptPhaseLock` (rank 4), emits multiple serial
  prints (each a potential contention point), optionally starts
  long-lived probe goroutines (`kpHog`, `kpMarker`,
  `smpBasicProbe`) via `go` statements, and in the non-IOAPIC
  path calls `restoreBSPVirtualWire` which does four MMIO
  writes.
- All of this runs in the very narrow and narrow-consequence
  context of the shell's **first-ever `int 0x80` ISR**, with
  interrupts re-enabled part-way through and the shell's
  ring3Wrapper's kernel stack depth already one syscall deep.

Observed-safe today — no faults — but the combination
(`late-boot + first-ever Ring-3 ISR + phase-lock + goroutine
spawn`) sits in the same hazard space as the keyboard-IRQ race
that was mitigated in the 2026-04-24 delta cycle (see
`current_impl_2026_04_24/07_keyboard_irq_ring.md`). Moving the
heavy work out of the ISR keeps the narrow path narrow.

## Design approach

### Dedicated kernel thread, parked on a ready channel

Add a new boot-finalize kernel thread at boot time:

- Spawn via `kernelThreadSpawn(0, bootFinalizeThread)` in
  `main()` **after** `kernelThreadInit()` and before
  `setupUserspace()`. The thread immediately parks on
  `<-bootReadyCh`.
- `bootReadyCh` is a buffered-cap-1 `chan struct{}` declared in
  `src/main.go`.
- `sysShellReadyHandler` replaces the `bootActivatePostShellReady()`
  call with:
  ```go
  func sysShellReadyHandler(frame *SyscallFrame) {
      proc := currentProc()
      if proc == nil || proc != getForegroundProc() {
          frame.RAX = sysFail(fdErrBad)
          return
      }
      // Non-blocking signal; the handler returns immediately.
      select {
      case bootReadyCh <- struct{}{}:
      default:
      }
      frame.RAX = 0
  }
  ```
- `bootFinalizeThread` body:
  ```go
  func bootFinalizeThread() {
      <-bootReadyCh
      bootActivatePostShellReady()
      // Thread exits; pool slot returns.
  }
  ```

The existing idempotency guard `bootPostShellReadyDone` in
`bootActivatePostShellReady` still protects against concurrent /
repeat invocations.

### Dependency on DEFERRED 1

This plan **requires a working Phase 4.4 kernel-thread context
switch**. Without it, `kernelThreadSpawn(0, bootFinalizeThread)`
is dangerous: as established by the F1 fix (commit `6a45e74`),
Phase 4.3 direct-invocation turns a long-parked function into a
stack-hijacker. Phase 4.4 fixes that so a thread that blocks on
`<-bootReadyCh` correctly yields back to the host.

If DEFERRED 1 is **not** landed yet, the implementer has a
fallback: spawn a regular TinyGo goroutine instead of a kernel
thread. This loses the "gooos owns scheduling for this service"
property from the `hoge.md` spirit but is correct and side-effect-
free.

### ISR-safety and lock-rank

- `sysShellReadyHandler` no longer acquires `preemptPhaseLock`
  at ISR level. `preemptPhaseLock` (declared in
  `src/preempt_phase.go`; ranked under "the post-IPI lock
  class" by the existing `current_impl_2026_04_24` delta docs —
  the rank table does not assign a number to it explicitly) is
  still acquired inside `bootActivatePostShellReady` — but that
  now runs on the boot-finalize kernel thread, which is not in
  an ISR frame.
- Non-blocking `select{case ch<-:default:}` is ISR-safe
  because the channel is buffered-cap-1: either the slot is
  empty (send succeeds synchronously, no park) or full (the
  `default` branch fires — no park). No lock acquisition that
  can block.
- The boot-finalize thread runs cooperatively with the existing
  service threads on BSP; lock-order unchanged.

### Interaction with existing gooos hooks

- `bootActivatePostShellReady` already calls
  `preemptPhaseAdvance(preemptPhaseSchedReady)`
  (`src/main.go:638`). Because the thread runs **after** the
  ISR returns to Ring 3, the phase transition now lags the
  syscall by a scheduler quantum — acceptable because the next
  LAPIC tick still fires the preempt IPI fanout once
  `preemptPhaseIsOperational()` becomes true.
- `runSMPShellPreemptProbe` / `runPreemptProbe` /
  `runSMPBasicProbe` branches inside the function continue to
  work — they just run on the kernel thread now.

## File / symbol touch-points

| File | Status | Purpose |
|---|---|---|
| `src/main.go` | Modify | Declare `var bootReadyCh = make(chan struct{}, 1)`. Add `bootFinalizeThread` func. `main()` calls `kernelThreadSpawn(0, bootFinalizeThread)` immediately after `kernelThreadInit()`. No other body changes. |
| `src/userspace.go` | Modify | Replace `bootActivatePostShellReady()` call in `sysShellReadyHandler` with the non-blocking channel send. |
| `current_impl_2026_04_24/01_boot_and_init_delta.md` | Doc update | Close the A1 Open Question bullet; describe the new boot-finalize thread. |
| `current_impl_2026_04_24/FINAL_REPORT.md` | Doc update | Remove DEFERRED 4. |

## TinyGo runtime patch changes

**None.** `kernelThreadSpawn` + `kernelYield` exist after
DEFERRED 1 lands; this plan just adds one more spawn call-site.

## Acceptance criteria

1. `make build` + `make lint` + `make verify-globals` pass.
2. Boot under `-smp 4` reaches the shell prompt and responds
   to the shell's `ShellReady()` call within one PIT tick
   (~10 ms).
3. `preemptPhase` transitions to `SchedReady` then `Operational`
   with the same timing profile as pre-change (verified by
   `preempt_probe: apicid ...` markers in the serial log).
4. `sysShellReadyHandler` body, when inspected, does no lock
   acquisition and no allocation.
5. No regression in any existing harness.

## Verification plan

```
make build
make lint
make verify-globals
make iso
bash scripts/test_smp_basic.sh
bash scripts/test_smp_shell_distribution.sh
bash scripts/test_preempt_kernel.sh
bash scripts/test_preempt_user.sh
bash scripts/test_smp_shell_preempt.sh
bash scripts/test_smp_shell_smpprobe.sh
bash scripts/test_sleeptest_shell.sh
bash scripts/test_goprobe_shell.sh
bash scripts/test_shell_background.sh
bash scripts/test_ps.sh
```

All must PASS (with `test_sleeptest_shell.sh` and
`test_smp_shell_preempt.sh` allowed to remain at their pre-fix
flake profile — they close under DEFERRED 3 and 5).

Additional manual inspection:

```
grep -A20 "func sysShellReadyHandler" src/userspace.go
```

Handler body should be under ~10 lines.

## Risk & rollback

| Risk | Impact | Mitigation |
|---|---|---|
| boot-finalize thread is never scheduled because kernelYield path is broken | Shell appears to start but preempt phase stuck in `BootInit`; no preempt fanout | Depends on DEFERRED 1 being verified; a soak of `test_smp_basic.sh` catches this immediately. |
| Non-blocking send drops the signal if somehow two ShellReady calls race | `bootFinalizeThread` never runs | `bootPostShellReadyDone` is already idempotent; additionally, buffered-cap-1 channel stores the first send which the thread consumes. A dropped duplicate is benign. |
| Thread consumes pool slot even though `bootActivatePostShellReady` is instant | Pool accounting burn | Post-return the `ktPool[i].inUse` flag is cleared by Phase 4.4's termination path; pool has 128 slots, 1 consumed ever. |

**Rollback**: revert the call-site change in
`sysShellReadyHandler`; the thread spawn + channel declaration
can remain as dead code without effect.

## Dependencies

- **Depends on DEFERRED 1** (Phase 4.4 context switch + service
  migration). Fallback: land with a plain `go bootFinalizeThread()`
  spawn instead of `kernelThreadSpawn` if DEFERRED 1 isn't
  ready. Convert the fallback back to `kernelThreadSpawn` in
  the same commit/PR that lands DEFERRED 1, so the two changes
  ship together and the boot-finalize path is on the new
  runtime as soon as it exists.
- No other DEFERRED dependency.

## Estimated effort

**Small.** ~15 LOC net in `src/main.go` + `src/userspace.go`.
Single focused session including full regression sweep.
