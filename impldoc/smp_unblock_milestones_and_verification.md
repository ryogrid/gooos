# SMP Unblock — Milestones and Verification

**Scope.** Unified milestone schedule for the M2 / M3 / M4 work batch that unblocks gooos goroutine distribution across APs. Entry / Exit gates per milestone, QEMU invocation matrix, and the harness extension list. **Extends** `impldoc/smp_milestones_and_verification.md`, does not replace it.

**Cross-links.**
- Overview index: `impldoc/smp_unblock_overview.md`.
- Per-milestone designs: `impldoc/smp_m4_ring3_fault.md`, `impldoc/smp_m2_ap_lapic_timer.md`, `impldoc/smp_m3_cores_promotion.md`.
- README / doc drift plan: `impldoc/smp_unblock_readme_update_plan.md`.
- Base schedule this extends: `impldoc/smp_milestones_and_verification.md`.
- Wave 1 baseline (the starting state): `impldoc/smp_migration_overview.md` (M0 + M1 complete, M2/M3/M4/M5 deferred).

---

## Milestone Map (this batch)

```
    ┌────────────────┐     ┌────────────────┐
    │      M2        │     │      M4        │    (M2 ∥ M4: independent)
    │ AP LAPIC timer │     │ AP Ring-3 iretq│
    │  race fix      │     │ triple-fault   │
    └────────┬───────┘     └────────┬───────┘
             │                      │
             │          ┌───────────┘
             │          │
             ▼          ▼
          ┌────────────────┐
          │      M3        │
          │ scheduler=cores│
          │ + stealWork    │
          │ wire-up        │
          └────────┬───────┘
                   │
                   ▼
          ┌────────────────┐
          │ README + docs  │    (closing step,
          │   update       │     per impldoc/
          │                │     smp_unblock_
          │                │     readme_update_
          │                │     plan.md)
          └────────────────┘
```

M2 and M4 are independent and can be worked in parallel. M3 depends on M4 resolving (or on the documented kernel-only-affinity fallback per `impldoc/smp_m3_cores_promotion.md §2`). README / doc updates close the batch once at least M3 lands.

---

## M2 — AP LAPIC Timer Race Fix

**Owner doc:** `impldoc/smp_m2_ap_lapic_timer.md`.

### Entry
- Wave 1 baseline (`smp-take3` tip, `2a1a13d`) clean; `make build && make lint && make verify-globals` all green.
- No unrelated regressions in `scripts/test_tcp_phase{1..5}.sh` or `scripts/test_net.sh` under `-smp 1`.
- `smp_m2_ap_lapic_timer.md` Strategy A (or explicit fallback) chosen.

### Work
Per `impldoc/smp_m2_ap_lapic_timer.md §4-5`:
1. `src/stubs.S` — `readInterruptDepth` + `readSyscallDepth` asm helpers.
2. `src/isr.S` — drop global counter; add vector-0x80 branch for syscallDepth.
3. `src/percpu.go` — `syscallDepth` field + offset constant.
4. `src/goroutine_irq.go` — migrate any reader.
5. Patched `interrupt_gooos.go` — new `In()` body.
6. `scripts/tinygo_runtime.patch` — regenerated.
7. `scripts/patch_tinygo_runtime.sh` — post-condition grep update.
8. `src/main.go` — un-gate `lapicTimerInitAP()`.

### Exit
All of the following PASS:

```
make build                                    # clean
make lint                                     # clean
make verify-globals                           # clean
bash scripts/patch_tinygo_runtime.sh          # "already-applied:"
bash scripts/test_net.sh                      # PASS (default -smp 1)
bash scripts/test_tcp_phase1.sh               # PASS
bash scripts/test_tcp_phase2.sh               # PASS
bash scripts/test_tcp_phase3.sh               # PASS
bash scripts/test_tcp_phase4.sh               # PASS
bash scripts/test_tcp_phase5.sh               # PASS

# -smp 4 regression:
SMP=4 bash scripts/test_smp_matrix.sh         # PASS (wraps existing harnesses)
```

Serial-log assertions under `-smp 4`:
- `"LAPIC timer: N ticks/10ms"` present (BSP calibration).
- At least one per-AP tick marker visible per AP over a 1-second capture window (instrumentation gated by `const apTimerTrace` if absent today — add under commit #5 of M2).
- **No** `"blocked inside interrupt"` panics.
- **No** boot hang; shell prompt reached.

### Rollback
`git revert` the M2 commit series; AP LAPIC timer returns to disabled; Wave 1 safe state intact. Per `impldoc/smp_m2_ap_lapic_timer.md §7`.

---

## M4 — AP Ring-3 `iretq` Triple-Fault Fix

**Owner doc:** `impldoc/smp_m4_ring3_fault.md`.

### Entry
- Wave 1 baseline clean.
- QEMU has GDB server support (`qemu-system-x86_64 --version` ≥ 8.0 is sufficient; WSL2 Ubuntu 24.04 ships 8.2+).
- Developer has GDB ≥ 12.0 and can attach to `localhost:1234`.

### Work
Per `impldoc/smp_m4_ring3_fault.md §2-5`:
1. Temporarily enable `stealWork()` call in scheduler_cooperative.go's pop site (the line commit `d0cba8e` disabled).
2. Boot under QEMU with `-s -S -d int,cpu_reset,guest_errors`.
3. Attach GDB; breakpoints on `ring3Wrapper`, `jumpToRing3`, `iretq`.
4. Dump TR / TSS / GDT / CR3 / user selectors at fault-time.
5. Work the hypothesis table (a-e) until one confirms.
6. Apply the fix at the identified site (`src/gdt.go` / `src/percpu.go` / `src/smp.go`).
7. Revert the repro-enable edit (stealWork call stays disabled at M3 time).

### Exit
- New harness `scripts/test_smp_ring3.sh` PASS (worker goroutines report ≥ 2 distinct cpuIDs under `-smp 4`).
- Existing Ring-3 harnesses under `-smp 4`: `test_sendkey.sh 1`, `test_pipe_matrix.sh` — PASS.
- Full regression matrix (same commands as M2 Exit) — PASS.
- `impldoc/smp_deferred_and_known_issues.md §2.1` marked Resolved with commit hash.
- No `ring3Wrapper: jumping to Ring 3` → silence patterns in any serial log under `-smp 4`.

### Rollback
If the investigation session stalls without localising root cause (two engineering days budget): revert the stealWork repro-enable edit; Wave 1 safe state intact; document session findings per `impldoc/smp_m4_ring3_fault.md §8`. Per `impldoc/smp_m4_ring3_fault.md §8`.

---

## M3 — `scheduler=cores` Promotion + `stealWork()` Wire-up

**Owner doc:** `impldoc/smp_m3_cores_promotion.md`.

### Entry
- M4 resolved (primary path) OR kernel-only-affinity fallback chosen (`impldoc/smp_m3_cores_promotion.md §2`).
- M1 Wave 1 atomicsLock-recursion smoke probe still PASSes under the fresh cores-mode build.
- `scheduler_cores.go` push-site anchors verified (`scheduler_cores.go:37` scheduleTask, `:87` Gosched — not the `:43` / `:89` figures in the 0.33→0.40 migration plan which were off-by-a-few; confirm before committing).
- (Pre-verified, not a runtime entry gate): `scheduler_tasks.go:11` exposes `systemStackPtr()`; the commit #2 build-tag widening is safe under both `scheduler.tasks` and `scheduler.cores`. See `impldoc/smp_m3_cores_promotion.md §4.2`.

### Work
Per `impldoc/smp_m3_cores_promotion.md §3` commit sequence 1-8:
1. runtime_gooos.go Wave 2 declarations.
2. task_stack_amd64.go widening + linkname consume.
3. scheduler_cores.go push-site retargeting + stealWork + apScheduler.
4. `src/target.json:9` flip tasks → cores.
5. scripts/patch_tinygo_runtime.sh post-condition update.
6. Wire stealWork call into scheduler pop site.
7. Add `scripts/test_smp_basic.sh` + boot-time probe.
8. Update `impldoc/smp_deferred_and_known_issues.md` + `TODO_SMP3.md`.

### Exit
- `scripts/test_smp_basic.sh` PASS (≥ 2 distinct cpuIDs observed among the boot-time probe goroutines).
- `smpprobe` from the interactive shell reports workers on ≥ 2 distinct cpuIDs.
- Full regression matrix under `-smp 4`:
  ```
  SMP=4 bash scripts/test_net.sh               # PASS
  SMP=4 bash scripts/test_tcp_phase1.sh        # PASS
  SMP=4 bash scripts/test_tcp_phase2.sh        # PASS
  SMP=4 bash scripts/test_tcp_phase3.sh        # PASS
  SMP=4 bash scripts/test_tcp_phase4.sh        # PASS
  SMP=4 bash scripts/test_tcp_phase5.sh        # PASS
  SMP=4 bash scripts/test_gochan.sh            # PASS
  SMP=4 bash scripts/test_goprobe.sh           # PASS
  SMP=4 bash scripts/test_sendkey.sh 1         # PASS (requires M4)
  ```
- `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/` — zero new markers vs. pre-M3.
- `make build && make lint && make verify-globals` clean.
- `bash scripts/patch_tinygo_runtime.sh` idempotent.

### Rollback
Per `impldoc/rollback_plan.md §4` (Wave 2 rollback): `git revert` the M3 commit series in reverse. The tree falls back to Wave 1 (scheduler=tasks, APs idle in waitForEvents). If only commit #6 (stealWork wire-up) regresses, revert just that commit — the tree stays on cores mode with dormant stealWork, which is a valid intermediate state for M5.

---

## QEMU Invocation Matrix

All commands assume CWD = `/home/ryo/work/gooos` and `tmp/kernel.iso` fresh from `make iso`.

| Purpose | CPUs | Invocation |
|---|---|---|
| Single-core parity (M2 exit gate) | 1 | `make run` (or `make run-kernel`) — standard Wave 1 behaviour |
| SMP baseline (M1 parity post-M2) | 4 | `make run-smp` — `-smp 4` wrapper |
| Stress distribution (M3) | 8 | `qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -display none -smp 8` |
| Max cores (M3 regression) | 16 | `qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -display none -smp 16` |
| M4 GDB session | 4 | `-smp 4 -s -S -d int,cpu_reset,guest_errors -D tmp/m4_qemu.log` (per `impldoc/smp_m4_ring3_fault.md §3`) |
| Networking + SMP (TCP regression) | 4 | Extend `make run-net` with `-smp 4`: `qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown -smp 4 -device e1000,netdev=n0 -netdev user,id=n0,hostfwd=...` — or call `SMP=4 scripts/test_net.sh` if the harness gains an env var knob. |

`-smp 16` maxes out the gooos `maxCPUs = 17` constant (1 BSP + 16 APs). Do not exceed; boot will misbehave if `cpuID()` returns an index past the array.

---

## Harness Extension Plan

| Harness | Status | Landed as |
|---|---|---|
| `scripts/test_net.sh` | existing (Wave 1) | extend with optional `SMP=N` env var at M2 |
| `scripts/test_tcp_phase{1..5}.sh` | existing (Wave 1) | extend with optional `SMP=N` env var at M2 |
| `scripts/test_sendkey.sh` | existing (Wave 1) | used under `-smp 4` at M4 / M3 exit |
| `scripts/test_pipe_matrix.sh` | existing (Wave 1) | used under `-smp 4` at M4 exit |
| `scripts/test_gochan.sh` | existing (Wave 1) | used at M3 exit |
| `scripts/test_goprobe.sh` | existing (Wave 1) | used at M3 exit |
| **NEW** `scripts/test_smp_ring3.sh` | new at M4 | wraps `smpprobe.elf` under `-smp 4`; expects ≥ 2 distinct cpuIDs |
| **NEW** `scripts/test_smp_basic.sh` | new at M3 | boot-time probe (kernel goroutines); expects ≥ 2 distinct cpuIDs in probe output |
| **NEW** `scripts/test_smp_matrix.sh` | new at M2 (optional) | wrapper that reruns existing harnesses with `SMP=4` |

All new harnesses must respect `feedback_background_bash.md` memory: any background poll loop is bounded (`for i in $(seq 1 N)` or `timeout N` or explicit PID-check).

---

## Cross-Milestone Acceptance

The batch is complete when:

- M2 Exit gate green, OR M2 explicitly deferred again with rationale added to `impldoc/smp_deferred_and_known_issues.md §2.2`.
- M4 Exit gate green, OR M4 explicitly deferred again with rationale added to §2.1.
- M3 Exit gate green (requires M4 OR fallback-affinity).
- `TODO_SMP3.md` — milestone ticks + "Deferred further" tail trimmed accordingly.
- `impldoc/smp_unblock_readme_update_plan.md` rules applied to README, `current_impl_doc/scheduler.md`, `impldoc/smp_deferred_and_known_issues.md`, `TODO_SMP3.md`.
- Reviewer subagent pass classified findings CRITICAL / MAJOR / MINOR; CRITICAL + MAJOR folded in; MINOR recorded.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
