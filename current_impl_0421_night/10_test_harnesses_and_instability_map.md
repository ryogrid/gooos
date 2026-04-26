# Test Harnesses and Instability Map

## Harness Inventory (scripts)

## Kernel/user preempt-focused

- `scripts/test_preempt_kernel.sh`
  - toggles `runPreemptProbe` to true
  - boots `-smp 4`
  - pass criterion: at least 5 `preempt_probe_marker=` lines
- `scripts/test_preempt_user.sh`
  - toggles `runUserPreemptProbe` to true
  - boots `-smp 4`
  - pass criterion: at least 5 `userpreempt_marker=` lines
- `scripts/test_smp_shell_preempt.sh`
  - toggles `runSMPShellPreemptProbe` to true
  - boots `-smp 4`
  - pass criterion: at least 5 lines matching `^marker [0-9]+ cpu=`

## Core regression harnesses (frequently used in this repository)

- `scripts/test_net.sh`
- `scripts/test_smp_basic.sh`
- `scripts/test_smp_shell_distribution.sh`
- `scripts/test_shell_background.sh`
- `scripts/test_ps.sh`
- `scripts/test_tcp_phase1.sh` through `scripts/test_tcp_phase5.sh`

## Harness Patterns

Common pattern across scripts:

1. mutate config constants in `src/preempt_config.go` when needed
2. `make iso`
3. run QEMU in non-graphical mode with serial log file under `tmp/`
4. grep serial output for expected marker lines
5. restore config on exit

## Observed Instability Surfaces

### 1. Shell preempt sub-gate path (2.3)

`test_smp_shell_preempt.sh` has historically encountered intermittent fail behavior (`markers_observed` low or zero). Current code includes launch-path and preempt-path diagnostics for this surface.

### 2. AP timer deferred path

In `apEntry`, AP LAPIC timer init is intentionally not enabled; comments indicate unresolved hang behavior in AP timer-dispatch path.

### 3. Preempt feature interaction sensitivity

When preempt probes are enabled, expected behavior depends on scheduling/migration timing. Regression outcomes can flap across runs without deterministic ordering guarantees.

### 4. Serial-output interleaving

Many probes rely on serial line grep markers. Under interrupt-heavy runs, output interleaving can affect line-level matching sensitivity.

## Suggested Static Review Focus

1. Confirm each harness edits/restores exactly one config axis.
2. Validate grep patterns against current marker output formats in code.
3. Ensure all scripts clean up QEMU process and temporary files.
4. Cross-check pass criteria against current behavior of gated probe goroutines (`kpMarker`, `userpreempt`, `markerprint`).

## Invariants for Test Reliability

- Harness-generated config flips must be reverted in all exit paths.
- Marker format literals in scripts must match current code output strings exactly.
- `tmp/` log path ownership and cleanup must remain stable to avoid stale-read false positives.

## Stability Fixes Applied

### goprobe/gochan select hang (April 2026)

**Issue**: Both `goprobe` and `gochan` would intermittently hang with truncated output like `goprobe: select O` (output cut mid-word). Cause: TinyGo user-space scheduler does not execute queued goroutines on demand when main goroutine immediately enters `select` block.

**Fix**: Added 1ms `time.Sleep()` before `select` statements in both programs to provide scheduling window for goroutines to execute and push values to buffered channels before main goroutine blocks.

**Commits**: 
- `61b89d0` Fix goprobe select hang with pre-select sleep
- `de0ab96` Fix gochan select hang with pre-select sleep

**Testing**: Both probes now pass all tests when run sequentially via shell autorun (`scripts/test_smp_shell_smpprobe.sh` variant pattern).

**Implication**: User programs that spawn goroutines and immediately block on channels should include a small sleep/yield to guarantee goroutines execute. See `impldoc/userspace_scheduler_integration.md §9.5` for details.
