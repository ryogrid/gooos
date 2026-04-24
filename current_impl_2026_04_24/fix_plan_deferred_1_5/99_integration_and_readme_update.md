# Integration, README + doc updates, traceability sweep

## Purpose

This file is the single point that ties the five per-item plans
together: the recommended landing order, the documents each item
must update, and a `TODO_FIX.md`-style checklist skeleton the
implementer can drop in.

## Landing order (with dependency graph)

Strict dependency order:

```
DEFERRED 1  (Phase 4.4 + service migration)
   |
   +--> DEFERRED 4  (boot-finalize kernel thread)        # requires kernelThreadSpawn to be safe
   |
   +--> DEFERRED 2  (elfSpawn round-robin)               # independent of 1, but co-needed by 5
                |
                +--> DEFERRED 3  (sleep audit)           # independent, but easier on top of 2
                              |
                              +--> DEFERRED 3a (sleep fix; produced by audit session)
                                            |
                                            +--> DEFERRED 5 (harness re-gating)
```

Suggested concrete sequence by session count:

| Session | Item(s) landed | Reason |
|---|---|---|
| 1 | DEFERRED 1 core (context switch + asm stub, no service migration yet) | Foundation; nothing else can rely on it until verified. |
| 2 | DEFERRED 1 service migration (timerDispatcher, fsTask first; netRxLoop last) | Validates the swap under realistic load. |
| 3 | DEFERRED 2 (round-robin) | Independent and small; quick win. |
| 4 | DEFERRED 4 (boot-finalize thread) | Cheap follow-up to DEFERRED 1. |
| 5 | DEFERRED 3 audit instrumentation + first 50-run sampler | Produces the diagnosis. |
| 6 | DEFERRED 3a sleep fix (plan written by session 5) | Closes Sleep flake. |
| 7 | DEFERRED 5 (harness re-gating) | Final cleanup once sampler at ≥ 95 %. |

DEFERRED 2 can move up to session 1 or 2 if a quick demonstrable
win is desired.

## Doc-update matrix

For each item, the implementer must update **all** of the
following files when the item closes (or partially closes).
Every cell lists the file path + a short instruction.

### DEFERRED 1 (Phase 4.4 + service migration)

- `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`
  - **§Open Questions**: replace the "Phase 4.4 not landed"
    bullet (currently last in the file) with a *Closed (C1, C3)*
    note + commit SHA.
  - **§Current Implementation Details**: rewrite the "no
    scheduling fairness" subsection to reflect the real swap.
  - **§2026-04-24 correction block** (the in-line correction
    inserted by the previous cycle): mark superseded by the
    real Phase 4.4 implementation.
- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred**: remove item 1.
  - **§Verification state**: update the "make iso" / "stability
    sampler" pass-rate row.
- `current_impl_2026_04_24/11_traceability_delta.md`
  - Add `kernelThreadSwap`, `kernelThreadTrampoline`,
    `primeKernelThreadStack`, `bootReadyCh` to the symbol list
    for the §04 doc row.
- `README.md`
  - Update the **Scheduler** progress-table row to note that
    long-lived kernel services run on the gooos-owned kernel-
    thread runtime (Phase 4.4) on top of TinyGo's substrate.
  - Update the **Where assembly is used** subsection to add
    `kernel_thread_swap.S`.
  - Update the **Architecture** ASCII diagram to show the
    kernel-thread layer alongside TinyGo goroutines.
- `docs/repo_layout.md`
  - Add `src/kernel_thread_swap.S` to the file inventory.

### DEFERRED 2 (B1)

- `current_impl_2026_04_24/03_smp_preempt_phase_gating.md`
  - **§Open Questions**: close the "smpprobe workers all on
    cpuID=0" bullet (currently the second non-Closed bullet).
- `current_impl_2026_04_24/05_syscalls_and_shell_ready.md`
  - Add a one-paragraph note in **§Current Implementation
    Details** describing the new `scheduleRing3Wrapper` helper
    in `src/process.go`.
- `current_impl_2026_04_24/11_traceability_delta.md`
  - Add `runqueuePushTo`, `schedulerWake`, `ring3SpawnCounter`,
    `scheduleRing3Wrapper` to the §05 row.
- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred**: remove item 2.
- `README.md`
  - Update the **SMP** progress-table row to note that
    user-process spawn is round-robin distributed.
- `docs/user_programs.md` (if it discusses smpprobe behaviour)
  - Update the expected-output snippet for `smpprobe` to show
    workers actually distributed.

### DEFERRED 3 (audit) and 3a (fix)

- `current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md`
  - **§Open Questions**: close the F1 follow-up bullet and the
    F2 bullet once the fix lands.
- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred**: remove item 3 once 3a lands.
- `README.md`
  - Update the **Known limitations** section if the Sleep
    behaviour was previously listed there.
- `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
  - **New** — produced by the audit session, documents the
    winning hypothesis + chosen fix + verification.

### DEFERRED 4 (A1)

- `current_impl_2026_04_24/01_boot_and_init_delta.md`
  - **§Open Questions**: close the A1 bullet ("heavy work in
    first-int 0x80 ISR").
  - **§Current Design**: insert the new boot-finalize thread
    into the boot-sequence-tail subsection.
- `current_impl_2026_04_24/05_syscalls_and_shell_ready.md`
  - Update the §Syscall #38 description to reflect the
    handler's new tiny body (non-blocking channel send).
- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred**: remove item 4.

### DEFERRED 5 (G1, G2)

- `current_impl_2026_04_24/10_test_harnesses_delta.md`
  - **§Open Questions**: close G1 and G2 bullets.
  - **§Stability Fixes Applied**: add new entries for the round-
    robin distribution fix and the Sleep-3 fix.
- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred**: remove item 5.
- `README.md`
  - Update the **Run in QEMU** or **Tests** section if it
    references the sampler.

### After all five close

- `current_impl_2026_04_24/FINAL_REPORT.md`
  - **§Deferred** should be empty (or contain only newly-found
    follow-ups).
  - **§Final Verification** checklist all checked.
  - Add a top-of-file note: "All DEFERRED 1–5 items landed in
    the 2026-MM-DD cycle; see commit range
    `<first>..<last>`."
- `current_impl_2026_04_24/00_index.md`
  - **§Non-Goals (Delta)**: remove or update the bullets that
    described the residuals (sys_sleep, smpprobe distribution,
    AP LAPIC timer).
- `current_impl_2026_04_24/fix_plan_deferred_1_5/`
  - This directory becomes historical; do **not** delete it.
    Leave it in place as a record of the planning work.

## TODO_FIX-style checklist skeleton (drop into a fresh `TODO_FIX_v2.md` at session start)

```
# TODO_FIX_v2 — DEFERRED 1–5 implementation cycle (started YYYY-MM-DD)

Source: current_impl_2026_04_24/fix_plan_deferred_1_5/00_index.md

## Group A — Foundation

- [ ] **D1.core** (per 01_phase4_4_*.md)
      Add src/kernel_thread_swap.S; rewrite kernelYield;
      add primeKernelThreadStack + trampoline; pass build/lint/verify-globals.
- [ ] **D1.svc-A** (per 01_phase4_4_*.md §Service migration)
      Migrate timerDispatcher + fsTask to kernelThreadSpawn;
      run smp_basic + sleeptest + net regressions.
- [ ] **D1.svc-B** Migrate tcpRTOScannerLoop + tcpEchoServer + udpEchoServer.
- [ ] **D1.svc-C** Re-add kernelThreadSpawn(0, netRxLoop); soak via test_net.

## Group B — Distribution

- [ ] **D2** (per 02_ring3wrapper_*.md)
      Add runqueuePushTo + schedulerWake linknames in src/goroutine_tss.go;
      add ring3SpawnCounter + scheduleRing3Wrapper in src/process.go;
      replace go ring3Wrapper(child) at every elfSpawn / elfLoad call site;
      verify smpprobe distribution.

## Group C — Boot finalize

- [ ] **D4** (per 04_boot_finalize_*.md)
      Add bootReadyCh + bootFinalizeThread in src/main.go;
      shrink sysShellReadyHandler to a non-blocking signal.

## Group D — Sleep audit + fix

- [ ] **D3.audit** (per 03_sleep_*.md)
      Land gated audit counters under runSleepAudit;
      add scripts/test_sleeptest_longrun.sh;
      run 50-iteration sampler; write 03a_sleep_fix.md.
- [ ] **D3a.fix** (per 03a_sleep_fix.md, written by D3.audit session)
      Implement the winning hypothesis fix.

## Group E — Harness re-gating

- [ ] **D5.gate-G1** (per 05_harness_regating.md)
      Update test_smp_shell_preempt.sh header to RELEASE-BLOCKING;
      add to scripts/test_smp_stability_sample.sh matrix.
- [ ] **D5.gate-G2** Same for test_sleeptest_shell.sh.
- [ ] **D5.sampler** Run 50-iteration sampler; verify ≥ 95 % per harness.

## Group F — Final close-out

- [ ] **DOC** Apply doc updates per 99_integration_and_readme_update.md.
- [ ] **README** Update progress table + arch diagram per 99_integration_*.md.
- [ ] **FINAL_REPORT** Empty §Deferred; add cycle-summary header.
- [ ] **REVIEW** Reviewer subagent pass; apply blockers; record declines.

## Commit cadence

One commit per checkbox where feasible; bundle small doc-only
edits. Commit subject prefix `TODO_FIX_v2/<id>:`.

## Final verification

- [ ] All `current_impl_2026_04_24/*.md §Open Questions` empty
      or marked Closed.
- [ ] `grep -rnE 'TODO|FIXME|XXX' src/` returns zero real markers.
- [ ] `scripts/test_smp_stability_sample.sh` PASS at ≥ 95 % per harness.
- [ ] FINAL_REPORT.md updated.
```

## Acceptance criteria for "all five DEFERRED items landed"

1. Every per-item file's **§Acceptance criteria** is met.
2. `current_impl_2026_04_24/FINAL_REPORT.md §Deferred` is empty
   or contains only newly-discovered follow-ups (each annotated
   with a justification).
3. Every `current_impl_2026_04_24/*.md §Open Questions` section
   is empty or contains only **Closed** annotations.
4. `scripts/test_smp_stability_sample.sh` exits 0 with all
   harnesses ≥ 95 % pass rate.
5. README progress table reflects the new state (kernel-thread
   runtime + round-robin distribution + closed sys_sleep
   bullet).
6. Reviewer subagent (round 2 of this implementation cycle)
   returns no blocking findings.

## Where a *new* DEFERRED list would live

If the cycle surfaces new issues that genuinely warrant
deferral, the implementer creates a fresh
`current_impl_2026_MM_DD/FINAL_REPORT.md §Deferred` section in
that cycle's delta doc-set rather than re-opening the
2026-04-24 list. The 2026-04-24 doc-set is then frozen as a
historical reference, similar to how `current_impl_0421_night/`
was treated by the 2026-04-24 cycle.
