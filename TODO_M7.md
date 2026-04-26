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
- [ ] Baseline — run smoke + keyboard + post-exec harnesses, record pre-M7 numbers
- [ ] Step 0 — add `scripts/test_ring3_distribution.sh` per `17_*.md` §1
- [ ] Step 1 — add `const userspaceSMP = false` to `src/preempt_config.go`
- [ ] Step 2 — Ring-3 tier scaffolding (`kschedQueuesRing3` + helpers)
- [ ] Step 3 — APs dispatch Ring-3 tier under flag + BSP combined pump
- [ ] Step 4 — exec'd children land on AP queues (`kschedSpawnRing3Wrapper`)
- [ ] Step 5 — re-purpose 5 SMP-distribution harnesses (SKIP gate flip)
- [ ] Step 6 — flip `userspaceSMP=true` default + lock-rank doc + RR cleanup
- [ ] Step 7 — README + `docs/` refresh
- [ ] Reviewer sub-agent pass (`hoge.md` §5, 9-item checklist)
- [ ] Final sweep — grep TODO/FIXME/XXX/HACK + TODO ↔ codebase ↔ R1..R13 cross-check + report

## Baseline

(populated by Baseline step)

## Per-step measurements

(populated as each step lands)

## Deferred

(items punted from this cycle; surface in final report)
