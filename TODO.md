# TODO - flaky_kbdproblem_fix implementation

Policy notes:

1. Follow `flaky_kbdproblem_fix/` documents as implementation source of truth.
2. Commit once per completed TODO item.
3. Do not run `git push` or branch operations without explicit user instruction.

- [x] Phase 0: Bootstrap implementation tracking in this root `TODO.md` and lock git workflow policy.
- [x] Phase 1: Implement startup/preempt phase-state gating (`src/preempt_phase.go`, `src/main.go`, `src/smp.go`, `src/lapic_timer.go`).
- [ ] Phase 2: Implement deterministic preempt target snapshot routing (`src/ipi.go`) while preserving ISR safe-point policy.
- [ ] Phase 3: Implement deterministic shell `smpprobe` path (probe gate + autorun path + foreground ownership diagnostics).
- [ ] Phase 4: Add deterministic harness and execute Tier-0/Tier-1 verification matrix.
- [ ] Phase 5: Run reviewer subagent, fix findings, update `README.md` and linked docs, and reconcile unresolved TODO/FIXME markers.
