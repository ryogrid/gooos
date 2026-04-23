# Test Harnesses and Instability Map — Delta

**Scope:** supersedes `current_impl_0421_night/10_test_harnesses_and_instability_map.md` **§Harness Inventory** (enumerates all current `scripts/test_*.sh`) and incorporates the baseline's §Stability Fixes Applied into the bigger post-baseline picture. Other baseline sections (§Harness Patterns, §Observed Instability Surfaces 2, 3, 4, §Suggested Static Review Focus, §Invariants for Test Reliability) remain authoritative.

## Summary of Changes Since `a384b1a`

Seven new harness scripts; one harness's marker grep hardening; one new external "brief" document on the ongoing SMP preempt/input instability.

1. New autorun-style harnesses that bypass HMP `sendkey` by toggling a `src/preempt_config.go` flag that injects an `.autorun.sh` for the shell:
   - `scripts/test_goprobe_shell.sh` (commit `39ed4e0`).
   - `scripts/test_smp_shell_smpprobe.sh` (commit `6eefda5`; markers hardened `ee64fb9`).
   - `scripts/test_sleeptest_shell.sh` (commit `af9cb8f`).
2. New reliability sampling harnesses (no `sendkey`, multi-run sampling):
   - `scripts/test_keyboard_reliability.sh` (commit `de90018`).
   - `scripts/test_smp_multi_boot.sh` (commit `d7cb673`).
   - `scripts/test_smp_stability_sample.sh` (commit `4c80037`, timing refined `d2b164d`).
3. Legacy HMP-backed reproducer kept for archival: `scripts/test_goprobe_hmp.sh` (commit `589b0f2`).
4. New brief: `smp_preempt_problem/README.md` — current mitigations + working hypotheses + near-term repair direction. Commit `d00171a`.

## Current Harness Inventory

Supersedes baseline §Harness Inventory. List reflects `ls scripts/test_*.sh` at HEAD.

### Kernel/user preempt-focused

| Script | Flag it toggles | Pass criterion |
|---|---|---|
| `test_preempt_kernel.sh` | `runPreemptProbe` | ≥5 `preempt_probe_marker=` lines |
| `test_preempt_user.sh` | `runUserPreemptProbe` | ≥5 `userpreempt_marker=` lines |
| `test_smp_shell_preempt.sh` | `runSMPShellPreemptProbe` | ≥5 `^marker [0-9]+ cpu=` lines (historically flaky — see §Instability 1) |

### Shell-autorun user-program harnesses (no HMP sendkey)

| Script | Flag it toggles | Target binary | Purpose |
|---|---|---|---|
| `test_goprobe_shell.sh` | `runGoprobeTest` | `goprobe.elf` | userspace goroutine+chan+select+yield probe |
| `test_smp_shell_smpprobe.sh` | `runSMPProbeShellTest` | `smpprobe.elf` | multi-worker SMP distribution probe |
| `test_sleeptest_shell.sh` | `runSleeputestTest` | `sleeptest.elf` | reproducer for the Ring-3 `sys_sleep` hang (expected-fail indicator) |

`runSMPProbeShellTest` additionally inhibits `handleLAPICTimer` preempt fanout (`src/lapic_timer.go:91`) so preempt-driven migrations don't perturb the grep markers.

### Reliability sampling

| Script | Purpose |
|---|---|
| `test_keyboard_reliability.sh` | Boot N times, count successful first-keystroke-to-shell-echo events. |
| `test_smp_multi_boot.sh` | Boot N times, count successful shell-reach events without page fault. |
| `test_smp_stability_sample.sh` | Multi-run sampler across `gochan`, `smpprobe`: per-run pass/fail + aggregate rate. Human-cadence delays; no HMP. |

### Legacy / archival

| Script | Purpose |
|---|---|
| `test_goprobe_hmp.sh` | HMP-`sendkey` `goprobe` runner — kept as fallback for manual triage. |

### Core regression (unchanged from baseline)

`test_net.sh`, `test_net_tap.sh`, `test_smp_basic.sh`, `test_smp_shell_distribution.sh`, `test_shell_background.sh`, `test_ps.sh`, `test_tcp_phase1.sh` … `test_tcp_phase5.sh`, `test_tcp_longidle.sh`, `test_tcp_latetiming.sh`.

## Full `preempt_config.go` flag matrix (replaces baseline's 4-flag listing)

See the matrix in `04_scheduler_and_kernel_thread.md` **§Preemption Configuration Gates**. Reproduced here for harness-author convenience: flags are `preemptEnabled`, `runPreemptProbe`, `runUserPreemptProbe`, `runSMPShellPreemptProbe`, `runSMPBasicProbe`, `runSMPProbeShellTest`, `runGoprobeTest`, `runSleeputestTest`, `runYieldtestTest` (9 flags total).

Every autorun-style harness follows the same pattern:

```
1. sed -i 's/const runXxxTest = false/const runXxxTest = true/' src/preempt_config.go
2. make iso
3. qemu ... -serial file:tmp/serial.log
4. grep ... tmp/serial.log   (pass/fail)
5. sed -i back to false  (trap EXIT)
```

The flag must revert in all exit paths — see baseline §Invariants for Test Reliability.

## Stability Fixes Applied (chronological, superseding baseline §Stability Fixes Applied)

### goprobe/gochan select-hang (April 2026) — evolving workaround

Baseline documented the fix as "add 1 ms `time.Sleep` before `select`" (commits `61b89d0`, `de0ab96`). Current reality has **diverged**:

- `gochan` still uses a `time.Sleep` pre-select delay (user/cmd/gochan/main.go, commit `de0ab96`).
- `goprobe` **replaced** the `time.Sleep` with a `Yield`-loop: `for j := 0; j < 10; j++ { gooos.Yield() }` at `user/cmd/goprobe/main.go:39`. Commits `f4bf75e`, rationale in `09_user_programs_sleep_vs_yield.md`.

See also `impldoc/userspace_scheduler_integration.md §9.5` (unchanged baseline reference).

### Keyboard IRQ race (April 22-23, 2026)

Race: reader observed empty ring → `sti()` → IRQ1 arrived and wrote to ring → `hlt()` → CPU halts forever.

Fix landed in `50cc6ce` (after the earlier revert pair `838c044`/`12d1b4d`): insert `sti() → re-check gooosKbdHead vs. gooosKbdTail → hlt()` atomic sequence at `src/keyboard_irq.go:98–107`. See `07_keyboard_irq_ring.md` for the x86-TSO argument.

### SMP worker `processExit` contention fix (April 22, 2026)

Concurrent worker exits contended on `pageAllocLock`. Fix: `procLock` serializes `processExit`'s `freePage` loop (`src/process.go:501–523`, commit `9cbe862`). Sibling worker commit `c063a61` removed the staggered-exit sleeps that were a band-aid for the same symptom.

### Shell foreground restore before wait cleanup (April 23, 2026)

`setForegroundProc(prevForeground)` now happens immediately after `<-proc.exitCh` in `processWait`, before the PID-map teardown block. Commit `f758f9b`. See `05_syscalls_and_shell_ready.md` §`processWait` foreground restore.

### Preempt startup-phase gating (April 22-23, 2026)

New `src/preempt_phase.go` prevents preempt IPI fanout until `bspBootDone` + all APs have reached `apSchedulerEntry`. Commits `8b75550` `1c99a72` `74d8eed` `74d0377`. See `03_smp_preempt_phase_gating.md`.

## Observed Instability Surfaces — Current Picture

Baseline §Observed Instability Surfaces identified four. Current status:

1. **Shell preempt sub-gate (2.3)** — still flaky. The `252a96b` investigation snapshot added `APIDSTAT`/`PRESTAT` diagnostics but did not resolve AP-targeted preempt delivery. `smpprobe` workers can still all report `cpuID=0`. Treat `test_smp_shell_preempt.sh` as *non-blocking* for regressions. Full handoff: `smp_preempt_problem/README.md`.
2. **AP LAPIC timer deferred** — unchanged. Comment in `src/smp.go:apEntry` still disables the AP timer init call.
3. **Preempt feature interaction sensitivity** — partially mitigated by phase-gating (Stability Fix §Preempt startup-phase gating), still not deterministic under `-smp > 1`.
4. **Serial-output interleaving** — partially mitigated by `427e9a0 Reduce SMP serial log noise` (consolidated split `serialPrintln` calls). Still enough fragmentation that `smp_preempt_problem/README.md §E` calls console cleanliness "not just cosmetic — directly affects diagnosis quality".

### New surface (post-baseline): Ring-3 `sys_sleep` hang under SMP

User processes reliably hang on the second or third `time.Sleep`/`gooos.Sleep` call under `-smp 4`. Reproducer `sleeptest.elf` via `test_sleeptest_shell.sh`. Root cause unknown; see `09_user_programs_sleep_vs_yield.md` and `smp_preempt_problem/README.md`.

## Diff-from-Baseline Notes

- Baseline §Harness Inventory listed 3 preempt harnesses + 10 core regression scripts (5 named + `test_tcp_phase1..5.sh`). Current tree has 3 preempt + 3 autorun + 3 reliability + 1 legacy + 13 core regression (the extra three are `test_net_tap.sh`, `test_tcp_longidle.sh`, `test_tcp_latetiming.sh`).
- Baseline §Stability Fixes Applied was a single section. Current list is 5 fixes; the goprobe/gochan entry itself has evolved since baseline.
- Baseline did not mention `smp_preempt_problem/` — that doc was added post-baseline by `d00171a` and is the canonical open-handoff corpus.

## Open Questions / Known Gaps

- **Deferred (G1)**: `test_smp_shell_preempt.sh` still flaky
  (`markers_observed=0..5` across runs). Improvements from B2
  (AP LAPIC timer) + F1 (netRxLoop kernel-thread removal)
  should help; a re-gating as a release-blocking harness is
  deferred until B1 (ring3Wrapper distribution) lands.
- **Deferred (G2)**: `test_sleeptest_shell.sh` is partially
  passing (Sleep 1 + 2 reliably, Sleep 3 intermittently hangs).
  Re-gating to "regression" is deferred until the F1
  follow-up (channel-wakeup cross-CPU audit) closes.
- **Closed (G3)**: `scripts/harness_lib.sh` now provides
  `harness_recover_stale_backup`, sourced from all eight
  autorun-style harnesses. A leaked `kill -9` that left a
  backup in `tmp/` is restored automatically at the next run.
- **Closed (G4)**: `test_net_tap.sh` — out of scope (not in
  the delta doc-set's Open Questions list originally; noted
  as "check header before using").
