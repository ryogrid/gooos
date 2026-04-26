# SMP Migration — Milestones and Verification

**Scope.** Six staged milestones (M0–M5) that progress gooos from "patched TinyGo 0.33.0, APs idle" to "TinyGo 0.40.1 `scheduler=cores`, goroutines running across multiple CPUs with safe GC". Each milestone has explicit Entry criteria, Exit criteria, affected files, verification commands, and QEMU invocation. This document **extends** `impldoc/smp_verification.md` — existing verification harnesses are reused; new harnesses are added.

**Cross-links.**
- Toolchain switch that gates M0/M1: `impldoc/toolchain_switch_plan.md`
- Patch rebase work that happens at M0 and M3: `impldoc/runtime_patches.md`
- Scheduler design reference: `impldoc/smp_scheduler_design.md`
- Top-level index + risk register: `impldoc/smp_migration_overview.md`
- Rollback procedure: `impldoc/rollback_plan.md`

---

## Milestone Map (one-liner each)

| # | Milestone | What it proves |
|---|---|---|
| M0 | **Single-core parity on 0.40.1.** Build + boot + full regression suite with `scheduler=tasks` on 0.40.1, `-smp 1`. | Patch rebase is mechanically correct; no behaviour regression. |
| M1 | **APs boot and idle on 0.40.1.** `-smp 4` boots; BSP runs all goroutines; APs park in `waitForEvents`. | Per-CPU init + gate still work after toolchain swap. |
| M2 | **Fix AP LAPIC timer race.** Per-CPU ISR counter migration; enable AP 100 Hz timer. | Orthogonal bug (`impldoc/smp_deferred_and_known_issues.md §2.2`) resolved. |
| M3 | **Kernel goroutines run on APs.** Flip `scheduler=cores`; AP scheduler entry wired; `stealWork` active. | Upstream cores mode composes with gooos per-CPU infra. |
| M4 | **Ring-3 user goroutines run on APs.** Fix Ring-3 `iretq` triple-fault on AP. | Orthogonal bug (`§2.1`) resolved. |
| M5 | **SMP-safe GC.** `gcPauseCore` IPI + mark-phase stop-the-world. | No concurrent mutation during GC mark. |

**Dependency chain.** M0 → M1 → (M2 ∥ M3) → M4 → M5. M2 and M3 can be worked in parallel but neither depends on the other for its Exit gate; they converge at M4.

---

## M0 — Single-Core Parity on 0.40.1

### Entry criteria

- `impldoc/toolchain_switch_plan.md §3 commits 1–5` landed (Makefile + patch script + patch file targeting 0.40.1 tasks-mode + Wave 1 README edits).
- `~/.local/tinygo0.40.1/` exists and matches the clone at `../tinygo`.
- `src/target.json` still has `"scheduler": "tasks"`.
- `git status` clean except for the toolchain-switch edits.

### Work

Mechanical rebase only. Execute the commit list in `impldoc/toolchain_switch_plan.md §3 commits 1–4`. Resolve any hunk-apply rejection per `impldoc/runtime_patches.md §3`.

### Affected files

- `Makefile:13`
- `scripts/patch_tinygo_runtime.sh:31, 57–69, 96–100, 141–176`
- `scripts/tinygo_runtime.patch` (full regenerate)
- `src/target.json` — **unchanged** at this milestone

### Verification

```
bash scripts/patch_tinygo_runtime.sh          # expect: success OR already-applied
make clean
make build                                     # expect: clean
make lint                                      # expect: clean
make verify-globals                            # expect: clean
bash scripts/test_net.sh                       # expect: PASS
bash scripts/test_tcp_phase1.sh                # expect: PASS
bash scripts/test_tcp_phase2.sh                # expect: PASS
bash scripts/test_tcp_phase3.sh                # expect: PASS
bash scripts/test_tcp_phase4.sh                # expect: PASS
bash scripts/test_tcp_phase5.sh                # expect: PASS
```

QEMU invocation for the above scripts is unchanged (`-smp 1` default).

Also run existing SMP smoke manually (not a script today):

```
make run-smp                                   # expect: boot to shell, -smp 4, APs idle
```

Serial log grep: `"BSP cpuID=0"`, `"AP 0 cpuID=1"`, `"AP 1 cpuID=2"`, `"AP 2 cpuID=3"` all present.

### Exit criteria

- Every command above exits 0 / serial matches expectations.
- `scripts/tinygo_runtime.patch` applies cleanly to a fresh 0.40.1 tree (second-apply prints `already-applied:`).
- No new `TODO`/`FIXME`/`XXX` markers in product code vs. pre-M0 baseline.

### Rollback trigger

Any regression in `test_tcp_phase{1..5}.sh` that was passing pre-migration. Drop to `impldoc/rollback_plan.md §Wave 1 rollback`.

---

## M1 — APs Boot and Idle on 0.40.1

### Entry criteria

- M0 Exit passed.
- No code changes required for M1 beyond what M0 already landed — M1 is a verification-only stage.

### Work

None. M1 exists to separate "did the toolchain swap regress single-core?" (M0) from "did it regress SMP bring-up behaviour?" (M1).

### Affected files

None.

### Verification

```
make run-smp                                   # -smp 4
```

Serial log checks:
- `"BSP boot"` then `"AP 0 online"`, `"AP 1 online"`, `"AP 2 online"`.
- `"bspBootDone"` gate released before shell prompts.
- No triple-fault, no stuck spin, no panic.
- Shell responds to keystrokes.

Smoke test (reuse existing harness):

```
bash scripts/test_tcp_phase5.sh                # expect: PASS under default single-CPU
```

Then the same harness manually under SMP (no automated gate yet):

```
# Temporary patch: sed -i 's/qemu-system-x86_64/qemu-system-x86_64 -smp 4/' tmp/run.sh
# or invoke QEMU directly with -smp 4 and the same serial-log plumbing.
```

Expected: PASS; no new pfault, no hang, no deadlock.

**Atomics smoke probe** (smoke-test, not a recursion-detector — recursion is design-eliminated per `impldoc/smp_scheduler_design.md §4.4`):

Add a temporary boot-time probe in `src/main.go` (revert after M1):

```go
import "sync/atomic"
// After percpuInit but before spawning goroutines:
var probe uint32
atomic.StoreUint32(&probe, 0)
atomic.AddUint32(&probe, 1)
atomic.CompareAndSwapUint32(&probe, 1, 2)
serialPrintln("atomics probe: ok")
```

Expected: `"atomics probe: ok"` on serial before the shell prompt. At M1 under `scheduler=tasks`, this exercises the Wave 1 `lockAtomics` path (which in tasks mode is only `interrupt.Disable`) and confirms the patched atomics flow through without linker errors. At M3 under `scheduler=cores`, the same probe exercises the new spinlock declarations (`atomicsLock`) and proves they're wired. If BSP hangs before printing, root-cause is likely a missing linkname / spin implementation — **not** recursion.

### Exit criteria

- All of M0's Exit plus:
- `-smp 4` boot completes.
- atomics probe prints the expected line.
- No new Ring-3 triple-fault signatures in serial log.

### Rollback trigger

Hang during boot, triple-fault in kernel-space, atomics probe hang. Drop to `impldoc/rollback_plan.md §Wave 1 rollback`.

---

## M2 — Fix AP LAPIC Timer Race

### Entry criteria

- M1 Exit passed.
- Can be worked in parallel with M3 (independent).

### Work

Per `impldoc/smp_deferred_and_known_issues.md §2.2`:

1. Migrate `interrupt.In()` to read per-CPU `%gs:4` counter only (remove dual-counter dependency on `gooos_in_interrupt_depth`).
2. Remove global `gooos_in_interrupt_depth` once the migration lands.
3. Re-enable AP `lapicTimerInit()` in `src/main.go`.
4. Validate that syscall handlers calling `task.Pause()` with per-CPU depth=1 no longer panic with "blocked inside interrupt". This may require revisiting gooos's ISR-hosted syscall design — depends on whether syscall entry decrements the per-CPU counter before calling `Pause()`.

### Affected files

- `src/isr.S` — remove `incl gooos_in_interrupt_depth(%rip)` (prologue + epilogue).
- Patched TinyGo `interrupt_gooos.go` (or `runtime_gooos.go`) — `interrupt.In()` body reads per-CPU counter only.
- `src/main.go` — uncomment `lapicTimerInitAP()` call sites.
- `src/goroutine_irq.go` — update any consumers of `gooos_in_interrupt_depth`.

### Verification

```
make build                                     # clean
make run-smp
```

Serial log:
- `"LAPIC timer: N ticks/10ms"` during BSP calibration.
- Per-AP heartbeat line (if instrumented): `"AP 1 tick"` at 100 Hz.
- No "blocked inside interrupt" panics under any test harness.

Regression matrix under `-smp 4`:

```
bash scripts/test_tcp_phase5.sh SMP=4          # extended harness, SMP flag
bash scripts/test_net.sh SMP=4
```

Where the harnesses grow an `SMP=N` env knob (wrapper script `tmp/run_smp.sh`); or create `scripts/test_smp_matrix.sh` that wraps existing harnesses with `-smp 4`.

### Exit criteria

- AP LAPIC timer enabled and firing at 100 Hz on every AP.
- No new panics.
- Existing harnesses pass under `-smp 4`.

### Rollback trigger

Regression in any existing harness, or panic involving `interrupt.In()`. Revert the commit; AP LAPIC remains disabled.

---

## M3 — Kernel Goroutines Run on APs

### Entry criteria

- M1 Exit passed. (M2 is recommended but not required — M3 can land with APs still lacking their own timers; `stealWork` triggered by external IPIs still progresses.)

### Work

Per `impldoc/runtime_patches.md §6 Wave 2`:

1. Flip `src/target.json:9` `"scheduler": "tasks"` → `"cores"`.
2. Add Wave 2 hunks to `scripts/tinygo_runtime.patch`: `const numCPU = 17`; runtime-lock **variable declarations** `atomicsLock`, `schedulerLock`, `futexLock`, `printLock` (gooos-local `spinLock` type — do NOT redefine the `lockFutex`/`unlockFutex`/`lockAtomics`/`unlockAtomics`/`lockScheduler`/`unlockScheduler` functions, upstream already defines them); `gcPauseCore` stub body (returns immediately at M3; full impl at M5); `currentCPU` linkname body.
3. Retarget gooos per-CPU runqueue hunks from `scheduler_cooperative.go` → `scheduler_cores.go`.
4. Optional cleanup: retire the gooos-added Queue spinlock in favour of upstream `lockAtomics()` (see `impldoc/runtime_patches.md §3.1`). Defer to post-M4 if time-constrained.

### Affected files

- `src/target.json:9`
- `scripts/tinygo_runtime.patch` (regenerate)
- `scripts/patch_tinygo_runtime.sh` (Wave 2 post-conditions)

### Verification

Add a new harness `scripts/test_smp_basic.sh`:

```
# Spawn N kernel-side goroutines that each print their cpuID and
# increment a shared counter. After 500 ms, verify:
#   - at least 2 distinct cpuIDs observed (goroutines ran on multiple CPUs)
#   - final counter equals N (all goroutines completed)
#   - no panic, no triple-fault
```

Wired as a boot-time probe in `src/main.go` gated by a build tag (off in release). QEMU: `make run-smp`. Serial grep for the probe's PASS/FAIL line.

Existing harness matrix under `-smp 4`:

```
bash scripts/test_tcp_phase5.sh SMP=4
bash scripts/test_net.sh SMP=4
bash scripts/test_gochan.sh SMP=4
bash scripts/test_goprobe.sh SMP=4
```

All PASS.

### Exit criteria

- `scripts/test_smp_basic.sh` reports PASS on at least 2 distinct cpuIDs.
- `smpprobe.elf` under the shell shows workers on ≥2 cpuIDs.
- Full regression matrix PASS under `-smp 4`.
- `grep -rn 'TODO\|FIXME\|XXX' src/` no new markers vs. M0 baseline.

### Rollback trigger

Kernel panic, triple-fault, AP hang under `-smp 4`, or any regression in harnesses. Drop to `impldoc/rollback_plan.md §Wave 2 rollback`.

---

## M4 — Ring-3 User Goroutines Run on APs

### Entry criteria

- M3 Exit passed.
- AP LAPIC timer (M2) is preferred but not required.

### Work

Per `impldoc/smp_deferred_and_known_issues.md §2.1`:

Debug the AP Ring-3 `iretq` triple-fault with QEMU + GDB. Likely causes (from the existing known-issues writeup):
- AP's TR does not point at the correct per-CPU TSS.
- TSS type byte stale (0xB busy instead of 0x9 available) after `ltr`.
- RSP0 in AP TSS mismatched with `ring3StackPool` kernel stack.
- CR3 in AP doesn't match `proc.pml4`.
- AP per-CPU GDT selectors for user CS/SS (0x1B/0x23) not resolvable.

Fix whichever root cause the debugger exposes.

### Affected files

Unknown until debugger session identifies the fault. Likely candidates: `src/gdt.go`, `src/percpu.go`, `src/process.go`, `src/goroutine_tss.go`.

### Verification

```
bash scripts/test_sendkey.sh 1                 # under -smp 4
bash scripts/test_pipe_matrix.sh               # under -smp 4
```

All PASS. `smpprobe.elf` exercises Ring-3 goroutines on APs.

### Exit criteria

- Existing Ring-3 harnesses PASS under `-smp 4`.
- `smpprobe.elf` reports workers on ≥2 distinct cpuIDs, each running a Ring-3 goroutine.
- No AP triple-fault signatures in serial log.

### Rollback trigger

Unable to diagnose the fault in a reasonable session (e.g., >2 engineering days). Leave APs running kernel goroutines only (M3 state); defer M4 to a future session.

---

## M5 — SMP-Safe GC

### Entry criteria

- M3 Exit passed (not strictly M4; GC correctness concerns BSP even with APs only running kernel goroutines).

### Work

Implement `gcPauseCore(cpu)` body in `runtime_gooos.go` per `impldoc/smp_scheduler_design.md §7.2`:
1. Send `vectorGCPause` IPI to target CPU.
2. Spin on `perCPUBlocks[cpu].gcPaused == 1` (set by handler).
3. Mark phase runs while peers are paused.
4. Clear state; paused CPUs resume.

Implement the kernel-side IPI handler in `src/smp.go` or new `src/gc_ipi.go`.

Reserve new IPI vector (e.g., `0xFB`).

### Affected files

- Patched TinyGo `runtime_gooos.go` — `gcPauseCore` body.
- `src/ipi.go` — vector definition, send path.
- `src/smp.go` or new `src/gc_ipi.go` — handler.
- `src/percpu.go` — add `gcPaused` field to PerCPU struct (per `impldoc/smp_percpu_and_sync.md §1.3`).

### Verification

Stress test: spawn 16 goroutines that each allocate heavily (`make([]byte, 4096)` in tight loops), run for 5 seconds under `-smp 4`. Expected: no corruption, no missed collections, no panic.

New harness `scripts/test_smp_gc_stress.sh`.

Existing regression matrix under `-smp 4` all green.

### Exit criteria

- `scripts/test_smp_gc_stress.sh` PASS.
- `grep "allocPage: out of memory"` absent after 30 seconds of stress.
- No live-root loss (manually validated by `objdump -t tmp/kernel.bin | grep -E "sleepQueue|timerQueue|runqueue"` still falls in `_globals_start..end`).

### Rollback trigger

Heap corruption, GC-related panic, or stress-test failure. Revert `gcPauseCore` enablement; GC continues in single-CPU mode (BSP only allocates).

---

## QEMU Invocation Reference

Standard:
```
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 4
```

Wrapper: `make run-smp` (Makefile line 124–125, exists today).

Higher core counts for stress:
```
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 8
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 16
```

Networking + SMP:
```
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 4 \
  -device e1000,netdev=n0 \
  -netdev user,id=n0,hostfwd=udp::9999-:7,hostfwd=tcp::10080-:8080
```

(Extension of existing `make run-net` invocation, Makefile lines 134–137.)

---

## Harness Extension Plan

| Harness | Today | Post-migration |
|---|---|---|
| `scripts/test_net.sh` | single-CPU UDP echo smoke | Add `SMP=4` env var support at M3 |
| `scripts/test_tcp_phase{1..5}.sh` | single-CPU TCP gates | Add `SMP=4` env var support at M3 |
| `scripts/test_net_tap.sh` | TAP integration (root) | unchanged |
| (NEW) `scripts/test_smp_basic.sh` | — | Added at M3: kernel-goroutine distribution probe |
| (NEW) `scripts/test_smp_ring3.sh` | — | Added at M4: Ring-3 distribution probe via `smpprobe.elf` |
| (NEW) `scripts/test_smp_gc_stress.sh` | — | Added at M5: allocation stress under GC |

Existing harnesses are **not modified to require** `SMP=4`; the env var is opt-in so default behaviour remains single-CPU bisectable.

---

## Gate Failure Protocol

If any Exit criterion fails:

1. Pause. Do not patch forward.
2. Read serial log; capture `dmesg`-equivalent output into `tmp/failure_M{N}_{timestamp}.log`.
3. Consult relevant section of `impldoc/smp_deferred_and_known_issues.md`, `impldoc/smp_overview.md §Risk Register`.
4. Decide: fix-forward (small patch) vs. rollback (drop the milestone's commits).
5. Rollback procedure per `impldoc/rollback_plan.md`.
6. Update this document's milestone section with the observed failure mode and the resolution.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
