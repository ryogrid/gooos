# TODO_SCHED — DEFERRED 1–5 implementation cycle

Authoritative specs: `current_impl_2026_04_24/fix_plan_deferred_1_5/`.
Start state: HEAD `c4ddb7d`. Plan commit cadence: one commit per
item, subject prefix `TODO_SCHED/<id>:`.

## Execution order (user-approved "Full scope")

```
P02 → P01 core → P04 → P01 services → P03 audit → P03a fix → P05
```

Doc updates land alongside each item per
`99_integration_and_readme_update.md`.

## Items

### P02 — `elfSpawn` round-robin distribution (plan file: `02_ring3wrapper_round_robin_distribution.md`)

- [ ] **P02.patch** — extend `scripts/tinygo_runtime.patch`
  `scheduler_cores.go` hunk with `runqueuePushTo(t, cpuIdx)`
  and `migrateAndPause(targetCpu)` (Gosched lock-discipline
  pattern).
- [ ] **P02.linkname** — add
  `//go:linkname migrateAndPause runtime.migrateAndPause`
  declaration in `src/goroutine_tss.go`.
- [ ] **P02.spawn** — add `ring3SpawnCounter` and
  `scheduleRing3Wrapper(proc *Process)` in `src/process.go`.
- [ ] **P02.callsites** — replace `go ring3Wrapper(child)` at
  `src/process.go:415` and `go ring3Wrapper(proc)` at
  `src/elf.go:250` with `scheduleRing3Wrapper`.
- [ ] **P02.verify** — `make build`/`lint`/`verify-globals`
  clean; `bash scripts/test_smp_shell_smpprobe.sh` shows ≥ 2
  distinct `cpuID=` in worker output.
- [ ] **P02.docs** — close
  `current_impl_2026_04_24/03_smp_preempt_phase_gating.md`
  §Open Questions smpprobe-distribution bullet; update
  `FINAL_REPORT.md §Deferred` item 2.

### P01 core — Phase 4.4 context switch (plan file: `01_phase4_4_context_switch_and_service_migration.md`)

- [ ] **P01c.asm** — new `src/kernel_thread_swap.S`
  (`kernelThreadSwap` + global `kernelThreadTrampolinePC`
  qword set by the linker for safe PC capture).
- [ ] **P01c.make** — extend `Makefile` with
  `tmp/kernel_thread_swap.o` rule.
- [ ] **P01c.struct** — extend `KernelThread` in
  `src/kernel_thread.go` (`hostCtx`, `started`, `returnAddr`,
  stack pointers).
- [ ] **P01c.yield** — rewrite `kernelYield()` for host/thread
  bidirectional swap; add `primeKernelThreadStack`; add
  `kernelThreadTrampoline` Go body.
- [ ] **P01c.verify** — `make build`/`lint`/`verify-globals`
  clean; `bash scripts/test_smp_basic.sh`,
  `test_smp_shell_distribution.sh`, `test_preempt_kernel.sh`,
  `test_net.sh` all PASS.
- [ ] **P01c.docs** — mark C1 closed in
  `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`
  §Open Questions; note the correction in
  `04`'s Phase-4.3-benign-claim block.

### P04 — boot-finalize kernel thread (plan file: `04_boot_finalize_kernel_thread.md`)

- [ ] **P04.chan** — add `bootReadyCh = make(chan struct{}, 1)`
  in `src/main.go`; spawn `kernelThreadSpawn(0, bootFinalizeThread)`
  after `kernelThreadInit()`.
- [ ] **P04.handler** — shrink `sysShellReadyHandler`
  (`src/userspace.go:623`) to foreground-check + non-blocking
  select send + `frame.RAX = 0`.
- [ ] **P04.verify** — boot under `-smp 4` reaches shell prompt
  within one PIT tick of ShellReady; all regression harnesses
  still PASS.
- [ ] **P04.docs** — close
  `current_impl_2026_04_24/01_boot_and_init_delta.md` §Open
  Questions A1 bullet; `FINAL_REPORT §Deferred` item 4.

### P01 services — migrate long-lived kernel services (plan file: `01_phase4_4_*.md §Service migration`)

- [ ] **P01s.A** — Group A: migrate `timerDispatcher`
  (`src/afterticks.go`) + `fsTask` (`src/fs.go`) via
  `kernelThreadSpawn(0, <svc>)` (keep `go <svc>()` during soak).
- [ ] **P01s.B** — Group B: migrate `tcpRTOScannerLoop`
  (`src/tcp_retx.go`), `tcpEchoServer` (`src/tcp.go`),
  `udpEchoServer` (`src/udp.go`).
- [ ] **P01s.C** — Group C: re-enable
  `kernelThreadSpawn(0, netRxLoop)` in `src/net.go` (undoes
  the F1 removal).
- [ ] **P01s.soak** — full regression sweep after each group;
  remove the `go <svc>()` spawn after soak if stable.
- [ ] **P01s.docs** — close C3 in
  `current_impl_2026_04_24/04_scheduler_and_kernel_thread.md`.

### P03 audit — sleep cross-CPU channel-wake audit (plan file: `03_sleep_cross_cpu_channel_wakeup_audit.md`)

- [ ] **P03.flag** — add `const runSleepAudit = false` in
  `src/preempt_config.go`; add per-CPU `SchedTasksPushed`,
  `SchedPopNil`, `SchedPopOk` in `src/percpu.go` (gated).
- [ ] **P03.patch** — extend
  `scripts/tinygo_runtime.patch` scheduler hunks with
  `gooosNotePush` / `gooosNotePop` linkname hooks.
- [ ] **P03.icr** — add `lapicICRTimeouts` bump in
  `src/smp.go:lapicWaitICR`.
- [ ] **P03.dump** — add `sleepAuditDump()` in `src/net.go`,
  called from `netDiag` when gate is on.
- [ ] **P03.harness** — create
  `scripts/test_sleeptest_longrun.sh` (50-run sampler, per-run
  audit dump, harness_recover_stale_backup sourced).
- [ ] **P03.run** — run sampler; collect logs; analyse against
  H1–H7 signal rules.
- [ ] **P03.doc** — write
  `current_impl_2026_04_24/fix_plan_deferred_1_5/03a_sleep_fix.md`
  with the winning hypothesis + proposed fix.

### P03a fix — implement winning hypothesis (plan file: `03a_sleep_fix.md`, written by P03 session)

- [ ] **P03a** — implement the fix per `03a_sleep_fix.md`;
  verify `scripts/test_sleeptest_shell.sh` ≥ 95 % over the
  50-run sampler.
- [ ] **P03a.docs** — close F1-follow-up in
  `current_impl_2026_04_24/09_user_programs_sleep_vs_yield.md`
  §Open Questions; `FINAL_REPORT §Deferred` item 3.

### P05 — harness re-gating (plan file: `05_harness_regating.md`)

- [ ] **P05.gate** — new `scripts/test_smp_release_gate.sh`
  (50-iteration outer loop over 8 harnesses; JSON summary;
  exit-non-zero on any < 95 %).
- [ ] **P05.headers** — flip `scripts/test_smp_shell_preempt.sh`
  and `scripts/test_sleeptest_shell.sh` headers to
  RELEASE-BLOCKING.
- [ ] **P05.verify** — sampler runs; every harness ≥ 95 %.
- [ ] **P05.docs** — close G1 + G2 in
  `current_impl_2026_04_24/10_test_harnesses_delta.md`;
  `FINAL_REPORT §Deferred` item 5.

### Final close-out

- [ ] **FINAL.docs** — apply doc sweep per
  `99_integration_and_readme_update.md` §Doc-update matrix:
  `README.md`, every affected
  `current_impl_2026_04_24/*.md §Open Questions`, relevant
  `docs/*.md`, `impldoc/*.md`.
- [ ] **FINAL.report** — empty out
  `current_impl_2026_04_24/FINAL_REPORT.md §Deferred` (or
  reduce to newly-found follow-ups with written justification);
  add a top-of-file commit-range note for this cycle.
- [ ] **FINAL.review** — general-purpose reviewer subagent
  pass across the whole commit range; apply blockers; record
  declines here.
- [ ] **FINAL.verify** — complete the Final Verification
  section below.

## Final Verification

*(To be filled in when all items are checked or explicitly
deferred.)*

- [ ] Every checklist item above is `[x]` or annotated
  `DEFERRED — see Deferred section`.
- [ ] `grep -nE 'TODO|FIXME|XXX'` in `src/`, `user/`,
  `scripts/`, and edited docs shows no incomplete-work
  markers introduced by this cycle; pre-existing markers
  listed below.
- [ ] `TODO_SCHED.md` itself has no unchecked items outside
  the Deferred sub-section.

### Pre-existing TODO/FIXME/XXX markers (surveyed at start)

*(Filled at end.)*

### Declined reviewer findings

*(Filled at end.)*

## Deferred

*(Filled at end. Anything intentionally punted, with
justification. Used verbatim in the final user report.)*
