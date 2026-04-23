# gooos Internal Implementation Documentation — Delta Set (2026-04-24)

## Scope

This directory is a code-grounded delta/extension of
`current_impl_0421_night/`. It captures design and implementation
changes since baseline commit **`a384b1a`** (2026-04-21 14:34,
`diag(smp): add M8 kbdIRQ-seen + M9 per-CPU pump-drain flags`)
through HEAD at authoring time (**`f4bf75e`**, 2026-04-24).

The 2026-04-21 snapshot under `current_impl_0421_night/` is treated
as **frozen baseline**: it is not modified. Each file in this
directory begins with a scope header declaring which baseline
document it *supersedes* (replace partial/whole sections) or
*extends* (append new subsystem content).

Same conventions as the baseline: source-of-truth is
`src/` / `user/` / `scripts/` / `Makefile`; no external design-note
dependency; invariant-focused; terse.

## Source-of-Truth Rule

Same as baseline. In addition, every new/changed symbol, file, or
syscall is cited with a repo-relative path and, where useful, a
short-SHA reference to the commit that introduced it. Commits in
the documented range `a384b1a..HEAD` are inline-referenced freely.

## Reading Order

Read the baseline `00_index.md` first if unfamiliar with the repo,
then this set in the order below.

1. `00_index.md` — this file.
2. `01_boot_and_init_delta.md` — boot-sequencing additions.
3. `03_smp_preempt_phase_gating.md` — startup phase gate for preempt fanout.
4. `04_scheduler_and_kernel_thread.md` — updated preempt flag matrix + kernel-thread abstraction.
5. `05_syscalls_and_shell_ready.md` — syscall #38 + `processExit` serialization + foreground restore.
6. `07_keyboard_irq_ring.md` — lock-free IRQ ring + fallback polling + virtual-wire restore.
7. `09_user_programs_sleep_vs_yield.md` — `sys_sleep` Ring-3 hang, `Yield`-loop workaround, diagnostic programs.
8. `10_test_harnesses_delta.md` — new shell-autorun harnesses + full `preempt_config` flag matrix.
9. `11_traceability_delta.md` — updated Docs → Files → Symbols matrix.

## Baseline → Delta Map

| Baseline file | Delta file | Relation |
|---|---|---|
| `00_index.md` | `00_index.md` (this) | supersedes (new master map) |
| `01_boot_and_kernel_init.md` | `01_boot_and_init_delta.md` | extends |
| `02_cpu_descriptors_traps_interrupts.md` | *(no delta)* | unchanged — use baseline |
| `03_smp_lapic_timer_ipi.md` | `03_smp_preempt_phase_gating.md` | supersedes §LAPIC Timer Flow + §IPI Paths (preempt fanout only); other sections still baseline-authoritative |
| `04_scheduler_runtime_preemption.md` | `04_scheduler_and_kernel_thread.md` | supersedes §Preemption Configuration Gates; adds §Kernel-Thread Abstraction |
| `05_process_elf_ring3_syscalls_signals.md` | `05_syscalls_and_shell_ready.md` | extends (syscall #38, `processExit` lock, foreground restore) |
| `06_memory_vm_allocator_gc.md` | *(no delta)* | unchanged — use baseline |
| `07_filesystem_fd_shell_io.md` | `07_keyboard_irq_ring.md` | extends §Keyboard Ownership (narrative only — user-visible contract unchanged) |
| `08_network_stack_driver_to_socket.md` | *(no delta)* | unchanged — use baseline |
| `09_userland_abi_and_embedded_elves.md` | `09_user_programs_sleep_vs_yield.md` | extends (new diagnostic programs, Sleep-vs-Yield status, sys #38 row) |
| `10_test_harnesses_and_instability_map.md` | `10_test_harnesses_delta.md` | supersedes §Harness Inventory; incorporates new stability fixes and the `smp_preempt_problem/` pointer |
| `11_traceability_matrix.md` | `11_traceability_delta.md` | extends (new files + symbols) |

Baseline files without a delta file are still authoritative for their
subsystem as of 2026-04-24: nothing meaningful changed under `src/`
for descriptors/traps (02), memory/VM/GC (06), or network
stack/socket ABI (08) in the range `a384b1a..HEAD`. Verified by
`git log --stat a384b1a..HEAD -- <baseline-scope-files>`.

## Commit-Range Summary

52 commits, themed:

1. Preempt startup-phase gating (new `src/preempt_phase.go`) — commits `7826548 8b75550 1c99a72 74d8eed 74d0377 fb17102 f758f9b`.
2. Kernel-thread abstraction Phases 4.1–4.3 (new `src/kernel_thread.go`) — `69029f2 e31b2bc 961cb90 3489340 f094316 9fe86e5 051cef1`.
3. Keyboard wake-path refactor + race fix (new `src/keyboard_irq.go` + `pollKeyboardFallback`) — `dfcd404 50cc6ce 9b71867 838c044 12d1b4d`.
4. User `time.Sleep` hang, `Yield`-loop workaround + diagnostics — `af9cb8f 4a0337c e6b79d3 f4bf75e 61b89d0 de0ab96 cb71a5b`.
5. SMP worker distribution + `processExit` serialization + shell foreground restore — `1be16c1 9cbe862 c063a61 45e3f2a 873410c 7f22b5c`.
6. Investigation checkpoint (preempt IPI delivery still unreliable) — `252a96b 604be0d`.
7. New shell-autorun test harnesses — `39ed4e0 6eefda5 7128c4e ee64fb9 4c80037 d2b164d d7cb673 589b0f2 de90018`.
8. Serial-log noise reduction — `427e9a0`.
9. Docs/tracking in-range — `7b11f09` (the baseline itself), `fa96bd8`, `751b4bb`, `3b933c1`, `d00171a`, `eacf8f8`, `6eefda5`.

The reverted commit pair `332a7a1` → `a3cc9c8` (failed `sys_sleep` TSS/CR3 sync) is **excluded from current-reality claims** throughout this set. The investigation snapshot `252a96b` introduced diagnostics but did **not** solve AP-targeted preempt delivery; see `smp_preempt_problem/README.md` for the open handoff.

## Global Implementation Invariants (Delta)

The baseline's eight invariants (see `current_impl_0421_night/00_index.md`) still hold. Two additions:

6. `preemptPhase` is monotonic. Readers on the ISR path
   (`handleLAPICTimer`) call `preemptPhaseIsOperational()` lock-free;
   writers go through `preemptPhaseAdvance()` under `preemptPhaseLock`.
7. `procLock` (rank 2) now serializes `processExit` page-free and
   PID bookkeeping, ensuring at most one `processExit` frees pages
   at a time and no concurrent `freePage` contention across CPUs.

## Non-Goals (Delta)

- **Updated 2026-04-24** (see `TODO_FIX.md`): several of the
  bullets below have moved from "non-goal" to "partially
  closed" in the 2026-04-24 fix cycle:
- The user-space `time.Sleep` hang at Ring 3 under SMP is
  **partially fixed**: the dominant root cause (a Phase 4.3
  `kernelThreadSpawn(0, netRxLoop)` that hijacked the timer
  dispatcher) was resolved; `sleeptest.elf` now typically
  completes 2 of 3 Sleep(10) calls. A residual Sleep-3
  intermittent hang remains open — see
  `09_user_programs_sleep_vs_yield.md §Open Questions`.
- AP-targeted preempt IPI delivery improvements: the AP LAPIC
  timer is now enabled (B2 — see
  `03_smp_preempt_phase_gating.md §Open Questions`). The
  residual `smpprobe` cpuID=0 distribution issue (ring3Wrapper
  round-robin) remains deferred to a future session.
- Phase 4.4 of the kernel-thread abstraction (real context
  switching) is **not** landed in this cycle; it is the
  prerequisite for migrating long-lived kernel services off
  TinyGo's goroutine scheduler. See `TODO_FIX.md §Deferred`.
