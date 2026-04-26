# TODO_M7 — Userspace SMP on APs milestone

Tracker for the execution cycle defined by
`no_goroutine_kernel_design/15_userspace_smp_on_aps.md`,
`16_m7_execution_plan.md`, `17_m7_test_strategy.md`, and
driven by `hoge.md`. One TODO item per commit + push; tick
`[x]` only after the corresponding commit lands and pushes.

Branch: `uni-proc-kernel-but-usrprog-smp`. Starting HEAD:
`314ceb3 chore: moved files related ralph to pasttdos dir`
(parent `8ecbdac no-goroutine kernel/M6.fix-1: ...`).

## Steps

- [x] Bootstrap — create this tracker, commit M7 design docs, add `00_index.md` TOC entry
- [x] Baseline — run smoke + keyboard + post-exec harnesses, record pre-M7 numbers
- [x] Step 0 — add `scripts/test_ring3_distribution.sh` per `17_*.md` §1
- [x] Step 1 — add `const userspaceSMP = false` to `src/preempt_config.go`
- [x] Step 2 — Ring-3 tier scaffolding (`kschedQueuesRing3` + helpers)
- [x] Step 3 — APs dispatch Ring-3 tier under flag + BSP combined pump
- [ ] Step 4 — exec'd children land on AP queues (`kschedSpawnRing3Wrapper`)
- [ ] Step 5 — re-purpose 5 SMP-distribution harnesses (SKIP gate flip)
- [ ] Step 6 — flip `userspaceSMP=true` default + lock-rank doc + RR cleanup
- [ ] Step 7 — README + `docs/` refresh
- [ ] Reviewer sub-agent pass (`hoge.md` §5, 9-item checklist)
- [ ] Final sweep — grep TODO/FIXME/XXX/HACK + TODO ↔ codebase ↔ R1..R13 cross-check + report

## Baseline (HEAD `80a9fae`, pre-M7 code)

- `scripts/test_kthread_smoke.sh`: PASS (A=5 B=5 ok=1)
- `scripts/test_ps.sh`: PASS (header=1 row=1)
- `scripts/test_run_smp_keyboard.sh`: 10/10 helpRan,
  10/10 M8, 10/10 M9, 0/10 PF — **PASS** (M6 invariant intact)
- `scripts/test_shell_post_exec_prompt.sh`: 10/10 helloPrinted,
  0/10 panics — **PASS** (M6.fix-1 invariant intact)
- `scripts/test_ring3_distribution.sh` (new harness, not yet
  added; pre-M7 baseline expectation: 1 distinct cpu only,
  cpuhog runs on BSP under M6).

## Per-step measurements

- **Step 0** (HEAD `337977c`, no §M7 code yet): the new
  harness ran cleanly, made an ISO with
  `runSMPShellPreemptProbe=true`, captured 15 s of serial
  output, and reported FAIL with `distinct_cpus=0..1`
  (cpuhog + markerprint run entirely on BSP under M6 —
  the very condition M7 fixes).
- **Step 3** (HEAD `9360477` + Step 3 edits, default
  `userspaceSMP=false`): regression-only run because Step 3
  alone changes no behaviour (the `if userspaceSMP` branch
  in apSchedulerEntry stays inert; the new
  `kschedLoopRing3OnlyOnce(0)` in the BSP combined pump is
  a no-op until Step 4 routes the boot shell into the
  Ring-3 tier). Measured: keyboard 9/10 helpRan
  10/10 M9 0/10 PF (PASS); post-exec 10/10 helloPrinted
  0/10 panics (PASS).

## Deferred

(items punted from this cycle; surface in final report)
