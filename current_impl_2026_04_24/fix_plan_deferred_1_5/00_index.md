# Fix Plan — DEFERRED items 1–5 (Master Index)

**Source**: `current_impl_2026_04_24/FINAL_REPORT.md §Deferred`.
**Scope**: design + work plan only — no code edits, no commits beyond
the plan files themselves.
**Audience**: a Claude Code agent that will open this directory cold
and pick up any single item for implementation.
**Authored at**: HEAD `0a840d4` (2026-04-24 cycle). All
`file:line` citations are verified against this commit; an
implementer working from a later commit should re-grep symbols
before relying on exact line numbers.

## DEFERRED item → plan file map

| # | FINAL_REPORT DEFERRED item (verbatim head) | Plan file | Suggested landing order |
|---|---|---|---|
| 1 | **C1 / C3** — Phase 4.4 kernel-thread context switch + migrate long-lived kernel services (`timerDispatcher`, `netRxLoop`, `tcpRTOScannerLoop`, `fsTask`). | `01_phase4_4_context_switch_and_service_migration.md` | 1st |
| 2 | **B1** — `elfSpawn` round-robin distribution for `ring3Wrapper` goroutines (the smpprobe-workers-all-on-cpuID=0 symptom). | `02_ring3wrapper_round_robin_distribution.md` | 2nd |
| 3 | **F1 follow-up** — Sleep-3 intermittent hang under `-smp 4` (suspected TinyGo channel-wakeup cross-CPU race). | `03_sleep_cross_cpu_channel_wakeup_audit.md` | 3rd |
| 4 | **A1** — move heavy work inside `bootActivatePostShellReady` out of first-`int 0x80` ISR context into a dedicated boot-finalize kernel thread. | `04_boot_finalize_kernel_thread.md` | 4th |
| 5 | **G1 / G2** — re-gate `test_smp_shell_preempt.sh` and `test_sleeptest_shell.sh` from "diagnostic / reproducer" to release-blocking regression harnesses. | `05_harness_regating.md` | 5th |
|   | Integration, README + doc updates, traceability sweep | `99_integration_and_readme_update.md` | alongside each item |

## Dependency graph

```
       +----------------+
       | 01 Phase 4.4   |            (C1 + C3: kernel-thread context
       |   C1 (switch)  |             switch + service migration)
       |   C3 (migrate) |
       +----+-----------+
            |
            v
   +--------+---------+       +-----------------------+
   | 04 boot-finalize |       | 02 ring3 round-robin  |  (B1: spawn
   | kernel thread    |       |                       |   distribution)
   | (A1)             |       +----+------------------+
   +------------------+            |
                                   |
                                   v
                          +--------+--------+
                          | 03 Sleep-3 flake |  (F1 follow-up: audit,
                          |    audit         |   not a direct fix yet)
                          +--------+--------+
                                   |
                                   v
                          +--------+--------+
                          | 05 harness       |  (G1 + G2)
                          |    re-gating     |
                          +-----------------+
```

Item 04 depends on 01 (needs Phase 4.4 context switch).
Item 05 depends on 02 landing (for `test_smp_shell_preempt.sh` to
become deterministic) and on 03 closing F1 follow-up (for
`test_sleeptest_shell.sh` to become deterministic).
Item 03 is an **audit** plan; it produces diagnostics, not a direct
fix. A separate follow-up session converts audit findings to a fix.

## Prior reading performed (confirming hoge.md §Required Prior Reading)

Fully read:

- `current_impl_2026_04_24/FINAL_REPORT.md`
- `current_impl_2026_04_24/TODO_FIX.md`
- `current_impl_2026_04_24/00_index.md`
- `current_impl_2026_04_24/01_boot_and_init_delta.md`
- `current_impl_2026_04_24/03_smp_preempt_phase_gating.md`
- `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`
- `current_impl_2026_04_24/05_syscalls_and_shell_ready.md`
- `current_impl_2026_04_24/07_keyboard_irq_ring.md`
- `current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md`
- `current_impl_2026_04_24/10_test_harnesses_delta.md`
- `current_impl_2026_04_24/11_traceability_delta.md`
- `current_impl_0421_night/00_index.md` through `11_traceability_matrix.md` (all 12)
- `smp_preempt_problem/README.md`
- `flaky_kbdproblem_fix/00_index.md`, `01_problem_and_evidence.md`, `02_design_revision_startup_scheduling.md`, `03_smpprobe_and_shell_liveness_design.md`, `04_implementation_workplan.md`, `05_verification_matrix.md`, `06_risks_and_rollback.md`, `07_review_log.md` (all 8)
- `README.md` and `CLAUDE.md`
- `tasks/TODO.md` and `tasks/lessons.md`
- `scripts/tinygo_runtime.patch` (in full, via Explore-agent survey)

Spot-read:

- `src/kernel_thread.go`, `src/goroutine_tss.go`,
  `src/afterticks.go`, `src/net.go`, `src/fs.go`,
  `src/tcp_retx.go`, `src/tcp.go`, `src/udp.go`
- `src/process.go`, `src/elf.go`
- `src/userspace.go`, `src/lapic_timer.go`, `src/ipi.go`,
  `src/smp.go`, `src/main.go`
- `scripts/test_sleeptest_shell.sh`,
  `scripts/test_smp_shell_preempt.sh`,
  `scripts/test_smp_stability_sample.sh`,
  `scripts/harness_lib.sh`
- `src/task_stack_amd64.S` (for the `tinygo_swapTask` reference)

No file was missing, empty, or unreadable.

## Conventions mirrored from baseline

All per-item files use the baseline conventions observed in
`current_impl_0421_night/` and `current_impl_2026_04_24/`:

- Repo-relative paths (`src/...`, `scripts/...`, `user/...`).
- Code-font for function / type / const names.
- `%gs:N` offsets for per-CPU ABI references.
- Numbered invariants, explicit **Risk** and **Open Questions**
  call-outs.
- Commit SHAs as short (7-char) hex.

## Commit cadence for the plan files themselves

**Chosen: single bundled commit** of all seven plan files.

Rationale: the files are tightly coupled (00_index and 99_integration
reference every other file; landing them separately produces
transiently inconsistent cross-references). One commit keeps
`current_impl_2026_04_24/fix_plan_deferred_1_5/` internally
consistent at every point in git history.

Commit subject: `current_impl_2026_04_24: add fix_plan_deferred_1_5 design + workplan`.

## How to start implementing (for the reader)

1. Read `99_integration_and_readme_update.md` first — it has the
   concrete landing order and the doc-update checklist.
2. Pick the DEFERRED item whose dependencies are satisfied.
3. Open the corresponding per-item file and follow its §File /
   symbol touch-points + §Verification plan sections.
4. Update `current_impl_2026_04_24/FINAL_REPORT.md §Deferred` and
   the relevant delta doc's §Open Questions section when the item
   closes (skeleton in `99_integration_and_readme_update.md`).
