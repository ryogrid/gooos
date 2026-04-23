# TODO_FIX — Resolve Open Issues in `current_impl_2026_04_24/`

Authored: 2026-04-24. Tracks the implementation work requested by
`hoge.md` to close every "Open Questions / Known Gaps" bullet in
the delta doc-set. Update each checkbox as work progresses.

## Baseline references

- Spec: `current_impl_2026_04_24/` (delta doc-set; Open Questions
  sections list the bullets this file tracks).
- Prior-art handoffs: `smp_preempt_problem/README.md`,
  `flaky_kbdproblem_fix/` (all 7 files).
- Current-state overview: `README.md`, `tasks/TODO.md`.
- Review baseline: 9-file delta set at commit `c167cef`.

## Scope decisions

Recorded up-front so later work can be audited against them.

### Userland scheduling (hoge.md requirement 2)

**Decision: KEEP TinyGo userland scheduler (`scheduler=tasks`).**

Justification: userland already runs `scheduler=tasks` under the
gooos runtime patch; migrating to "one kernel thread per
goroutine via syscall" would require adding `clone`-like syscalls,
a full userland thread runtime, and reworking every userspace
program (19 binaries + 2 new diagnostics). The Ring-3 `sys_sleep`
hang — the concrete symptom that motivated the question — is
actually addressable within the current model (see item B below)
without a userland-runtime rewrite. The performance cost argument
for native threads does not apply here; there is no contention
case in the current binaries that demands it.

### Kernel-side scheduling (hoge.md requirement 1)

**Approach: incremental Phase 4.4 — gooos owns the
kernel-thread scheduler loop for long-lived kernel services;
TinyGo goroutines remain the substrate for `go`/`chan` language
features but their dispatch policy (which CPU, when to preempt)
is driven by gooos primitives already in the tree
(`gooosOnResume`, `gooosWakeupCPU`, preempt IPIs, phase gate).**

Justification: the TinyGo runtime patch at
`scripts/tinygo_runtime.patch` already exposes every hook needed
for gooos to own policy without replacing the whole runtime
(per the Phase-1 scheduler-integration survey). A ground-up
scheduler rewrite would require re-implementing task
allocation / queue / stack management / channel wakeup — weeks
of work with high regression risk — without changing the
observable behavior we need. Instead we finish Phase 4.4
(context-switching kernel-thread runtime for deterministic
per-CPU services) and complete the migration of the long-lived
services listed below. Items we cannot reach in this batch are
explicitly deferred at the end of this file.

## Open-Question matrix → plan

One section per delta-doc `§Open Questions / Known Gaps` entry.
Each item is tagged **FIX** (in scope), **DEFER** (moved to the
Deferred section at end of this file), or **CLOSE-AS-WONTFIX**
(won't change code; doc updated to reflect reality).

### Group A — `01_boot_and_init_delta.md §Open Questions`

- [ ] **A1 FIX**: `bootActivatePostShellReady` runs heavy work
  (serial prints, goroutine spawn, phase-lock acquire) in first
  `int 0x80` ISR context. Factor heavy work out of ISR — handler
  sets a latch + wakes a dedicated boot-finalize kernel thread
  that does the rest on its own stack.
- [x] **A2 CLOSE-AS-WONTFIX**: IOAPIC-path virtual-wire restore
  untested. IOAPIC path is not used in the supported QEMU run
  (`make run-smp` uses the non-IOAPIC path). Document the
  symmetric restore sketch under `07_keyboard_irq_ring.md §Open
  Questions` but do not implement until hardware demands it.
  *Already documented in `01_boot_and_init_delta.md §Open
  Questions`; no code change.*

### Group B — `03_smp_preempt_phase_gating.md §Open Questions`

- [ ] **B1 FIX**: `smpprobe` workers all report `cpuID=0`. Root
  cause is that Ring-3 `ring3Wrapper` goroutines do not migrate
  to APs under the current cooperative policy because
  `stealWork()` is "wired live" but the per-CPU runqueues are
  fed by BSP only until a goroutine yields. Make `elfSpawn`
  schedule new Ring-3 wrappers round-robin across online CPUs
  by pushing onto the target CPU's runqueue directly (analogous
  to `kernelThreadSpawn`'s per-CPU push), bypassing
  `scheduleTask(current-CPU)`.
- [ ] **B2 FIX**: AP LAPIC timer enablement still deferred.
  Land it: the prior hang was diagnosed to a non-nosplit / lock
  issue in `handleLAPICTimer` (per `smp_preempt_problem`
  hypotheses). Audit `handleLAPICTimer` for AP-safety:
  `maybeSignalUserPreempt`, `broadcastPreemptIPI` already gated
  by `idx == 0`; the AP path today only sets
  `perCPUBlocks[idx].WantReschedule = 1` and sends EOI. That is
  safe. Enable `lapicTimerInit()` in `apEntry` and add an
  AP-side yield site (`runtime.Gosched()` on `WantReschedule`).
- [x] **B3 CLOSE-AS-WONTFIX**: `preemptTargetSnapshotN` racy
  read is diagnostic-only. Add a comment; no code change.
  *Comment added in `src/ipi.go`.*
- [ ] **B4 DEFER**: investigation-checkpoint `252a96b`
  diagnostics (`APIDSTAT`, `PRESTAT`) remain in tree behind
  `runSMPShellPreemptProbe`. Keep as-is; not in the delta-doc
  Open-Questions list as a code gap.

### Group C — `04_scheduler_and_kernel_thread.md §Open Questions`

- [ ] **C1 FIX**: Phase 4.4 not landed — `kernelYield` is a
  one-shot drain. Implement real context switching for
  KernelThreads using `SavedContext` + a `kernel_thread_swap.S`
  stub modeled after `tinygo_swapTask`. Per-CPU stack pool
  (lazy-allocated on first yield).
- [x] **C2 FIX**: `kernelThreadSpawn` allocates from ISR path
  without lint coverage. Replace the `&KernelThread{}`
  allocation with a bounded static pool (`[maxCPUs*8]KernelThread`)
  and convert `kernelThreadSpawn` to an allocation-free
  slot-pop — ISR-safe. Add a `//go:nosplit` annotation.
  *Landed in commit `e346305`; pool size 128; ISR-safe drop counter
  `kernelThreadSpawnDrops`.*
- [ ] **C3 FIX**: migrate long-lived kernel services from
  TinyGo goroutines to KernelThreads once C1 lands. Concrete
  targets: `timerDispatcher`, `netRxLoop`, `tcpRTOScannerLoop`,
  `fsTask`. Keep `ring3Wrapper` and other per-process
  goroutines on TinyGo's task runtime (they *are* the goroutine
  abstraction users see).

### Group D — `05_syscalls_and_shell_ready.md §Open Questions`

- [x] **D1 CLOSE-AS-WONTFIX**: `procLock` rank-2 holding across
  `freePage` (rank 1) — correct rank-order (higher holding
  lower is OK); add an explicit assertion comment above the
  critical section.
  *Comment added above the `procLock.Acquire` in `processExit`.*
- [x] **D2 FIX**: `sys_shell_ready` has no caller
  authentication. Gate it to callers with `proc == foregroundProc`
  at the time of the syscall — this narrows the attack surface
  to the interactive shell without breaking the current flow.
  *Landed in commit `74afce5`.*
- [x] **D3 CLOSE-AS-WONTFIX**: `processExit` diagnostic dump
  inside critical section is `runSMPShellPreemptProbe`-gated
  and off by default — no change needed.
  *Recorded as-is; no code change.*

### Group E — `07_keyboard_irq_ring.md §Open Questions`

- [x] **E1 FIX**: Ring drops on full without warning. Add a
  diagnostic counter (`kbdRingDrops uint32`) incremented on the
  drop branch; reported by `netDiag`.
  *Landed in commit `e346305`.*
- [ ] **E2 CLOSE-AS-WONTFIX (pending B2/C3)**: keyboard
  reliability not 100% — covered by B2 (AP LAPIC timer) and
  C3 (kernel service migration); this bullet closes when those
  land.
- [x] **E3 CLOSE-AS-WONTFIX**: `pump:NNNN` netDiag name is
  historical — rename cost isn't worth the break in test-harness
  grep patterns that match this string; document in
  `07_keyboard_irq_ring.md`.
  *Existing doc already notes this; no code change.*
- [x] **E4 FIX**: AP-hosted blocking keyboard reader burns 100%
  CPU yielding against an empty ring. Fix: use `afterTicks(1)`
  (10 ms) as a bounded-sleep fallback on the AP path instead of
  raw `gooosSchedulerYield()`. That path is unreachable in
  practice today (readers are BSP-originated) but the fix is
  one line and closes the open question.
  *Landed in commit `74afce5`.*

### Group F — `09_user_programs_sleep_vs_yield.md §Open Questions`

- [ ] **F1 FIX**: Ring-3 `sys_sleep` hang under SMP. Root cause
  per the Phase-1 survey: a ring3Wrapper that parks on
  `<-afterTicks(d)` may be woken on any CPU, and while
  `gooosOnResume` updates TSS.RSP0 / CR3 correctly, the
  scheduler can resume the wrapper on a CPU where the previous
  syscall ISR frame on the ring3Wrapper's kernel stack is
  *stale* (the wakeup replays the old iretq-frame which points
  at a different user context). Fix: bind Ring-3 wrappers to
  their origin CPU for the duration of a blocking syscall —
  set `proc.PinnedCPU = cpuID()` before `<-afterTicks` in
  `sysSleepHandler`, clear after return; extend the
  scheduler-integration hook (`gooosOnResume`) to re-target the
  runqueue if the current CPU doesn't match `PinnedCPU`.
- [ ] **F2 DEFER**: "is Yield-loop a sustainable workaround" —
  moot once F1 lands. Updates userland contract doc once F1
  passes verification.

### Group G — `10_test_harnesses_delta.md §Open Questions`

- [ ] **G1 FIX**: `test_smp_shell_preempt.sh` flaky. Once B1
  (`smpprobe` distribution) and B2 (AP LAPIC timer) land, the
  harness should become deterministic. Re-run after B1/B2;
  if still flaky, mark as "diagnostic-only, expected-fail" in
  the script header.
- [ ] **G2 FIX**: `test_sleeptest_shell.sh` is a deliberate
  reproducer. After F1 lands, flip its header to
  "regression — expected PASS" and add to the stability sampler.
- [x] **G3 FIX**: harness sed-leak on kill -9. Add a stronger
  trap — write the original flag value to `tmp/.<script>.flag`
  at start, restore from it on exit. Covers all autorun-style
  harnesses.
  *Landed in commit `74afce5` via `scripts/harness_lib.sh` +
  `harness_recover_stale_backup` sourced from all eight
  flag-flipping harnesses.*
- [x] **G4 CLOSE-AS-WONTFIX**: `test_net_tap.sh` status unclear.
  Not an Open Question from the delta docs — already out of
  scope (mentions only "unclear if production-ready; check
  header").
  *Recorded as-is; no code change.*

## Implementation sequence

1. **C2** (`kernelThreadSpawn` to pool allocation) — small,
   enables later items and eliminates a lint/ISR concern.
2. **C1** (Phase 4.4 context switch) — foundation for C3, F1.
3. **C3** (migrate 2–4 kernel services) — validates C1 under
   realistic load.
4. **B1** (`elfSpawn` round-robin distribution) — directly
   addresses smpprobe cpuID=0.
5. **B2** (AP LAPIC timer) — unblocks per-AP preemption.
6. **F1** (Ring-3 `sys_sleep` hang) — depends on B2 + C1.
7. **D2** (`sys_shell_ready` authentication) — one-liner.
8. **A1** (boot-finalize kernel thread) — requires C1.
9. **E1, E4** — diagnostic hardening.
10. **G3** — harness hygiene.
11. **G1, G2** — re-run and re-gate.
12. **D1, D3, B3, E2, E3, G4** — doc-only close-out.

## Verification plan

Run after each major item (per `CLAUDE.md §Verification Before Done`):

- `make build` — must pass.
- `make lint` — no new ISR-safety violations.
- `make verify-globals` — no new globals outside `[_globals_start,
  _globals_end)`.
- Single-shot kernel harnesses: `scripts/test_preempt_kernel.sh`,
  `scripts/test_preempt_user.sh`, `scripts/test_smp_basic.sh`.
- Per-subsystem: `scripts/test_smp_shell_distribution.sh` (B1),
  `scripts/test_smp_shell_smpprobe.sh` (B1),
  `scripts/test_sleeptest_shell.sh` (F1),
  `scripts/test_goprobe_shell.sh` (C3 sanity).
- Final pass: `scripts/test_smp_stability_sample.sh`
  (multi-boot rate ≥ 95 %).

For items I cannot fully verify through these scripts in a single
session, record the residual risk in the Final Verification
section.

## Subagent usage plan

- Use `Explore` for source-reading subtasks that touch many files
  (already done once in Phase 1).
- Use `general-purpose` for the review loop and for focused
  implementation delegation (e.g., Phase 4.4 context-switch
  assembly stub).
- Use an isolated-worktree `general-purpose` agent only if an
  item demands large, hard-to-revert mass edits; otherwise
  foreground edits with per-item commits are preferred.

## Progress log

Commits made against this TODO are tagged `TODO_FIX/<id>:` in the
commit message subject (e.g., `TODO_FIX/C2: ...`).

---

## Final Verification

*(To be filled in when all tasks are complete or explicitly
deferred.)*

- [ ] All checklist items above are resolved (checked or marked
  DEFER / CLOSE-AS-WONTFIX).
- [ ] Repo-wide `grep -nE 'TODO|FIXME|XXX'` run; all occurrences
  introduced by this change set are removed; pre-existing
  occurrences are listed below.
- [ ] Every delta doc's §Open Questions section is updated (or
  removed for resolved bullets) to match current reality.
- [ ] `README.md` progress table reflects landed changes.
- [ ] `scripts/test_smp_stability_sample.sh` completes with
  ≥ 95 % pass rate and zero hangs.

### Pre-existing `TODO`/`FIXME`/`XXX` tags (surveyed at start)

*(Filled in by final-verification step.)*

### Declined reviewer findings

*(Filled in after the reviewer pass; each declined finding with
written reason.)*

## Deferred

*(Anything intentionally left out of scope, with justification.
Used verbatim in the final user-report.)*
