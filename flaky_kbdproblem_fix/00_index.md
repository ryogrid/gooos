# Flaky keyboard/SMP startup fix plan (coding-agent oriented)

## Objective

Design and execute a robust fix for:

1. startup scheduling instability,
2. correct `smpprobe` command behavior, and
3. shell continuity after `smpprobe`.

This plan is grounded in `tasks/TODO.md`, `current_impl_0421_night/*`, and current source code (`src/*`, `user/*`, `scripts/*`).

## Scope

In scope:

1. Boot/SMP/preempt stabilization around `src/main.go`, `src/smp.go`, `src/lapic_timer.go`, `src/ipi.go`, `src/goroutine_irq.go`.
2. Deterministic `smpprobe` command-path verification through actual shell execution (`user/cmd/sh/*`, `user/cmd/smpprobe/main.go`).
3. Shell liveness and foreground/stdio integrity (`src/process.go`, `src/fd.go`, `src/userspace.go`).
4. Harness updates for reproducible validation (`scripts/*`).

Out of scope:

1. Large scheduler/runtime redesign beyond what is needed for deterministic startup/preempt behavior.
2. AP-local LAPIC timer enablement as a primary fix path (kept deferred unless proven safe).

## Document map

1. `01_problem_and_evidence.md` — failure model and evidence targets.
2. `02_design_revision_startup_scheduling.md` — startup/preempt architecture revision.
3. `03_smpprobe_and_shell_liveness_design.md` — command-path and shell-liveness design.
4. `04_implementation_workplan.md` — implementation sequence with exact touch points.
5. `05_verification_matrix.md` — acceptance checks and measurable pass criteria.
6. `06_risks_and_rollback.md` — risk and rollback strategy.
7. `07_review_log.md` — reviewer findings and resolved actions.
