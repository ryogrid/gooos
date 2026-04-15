# Deferred Items — Overview and Execution Map

This document is the entry point to the design set for every item
currently marked "Deferred" across `impldoc/phase_b_*.md`,
`impldoc/goroutine_design_*.md`, `TODO.md`, and `TODO_B.md`. Phase A
and Phase B delivered a working TinyGo-goroutine kernel
(commits `3cc2132..df6a6ce`); this design set covers what was
intentionally left for later.

Each topic-specific file under `impldoc/deferred_*.md` answers one
or more items from the inventory below. This document is the
coverage map and the dependency DAG.

## 1. Inventory

17 entries enumerated by Phase-1 exploration. Sixteen are truly
outstanding; one is already landed and recorded here for
bookkeeping so nobody re-designs phantom work.

| # | Title | Location | State | Home doc |
|---|---|---|---|---|
| 1 | SMP v2: per-CPU runqueues + work stealing | `goroutine_design_gc_and_smp.md §3`, `phase_b_overview.md §4` | todo | `deferred_smp_v2.md §2` |
| 2 | SMP v2: APIC timer preemption on APs | `goroutine_design_gc_and_smp.md §3` | todo | `deferred_smp_v2.md §3` |
| 3 | SMP v2: per-CPU TSS/GDT | `goroutine_design_gc_and_smp.md §3`, `phase_b_ring3_and_exec.md §4.3` | todo | `deferred_smp_v2.md §4` |
| 4 | SMP v2: `atomic.StoreUint32`/`LoadUint32` retrofit (`R-b5-smp-atomics`) | `phase_b_keyboard_irq.md §3.4` | todo | `deferred_smp_v2.md §5` |
| 5 | SMP v2: LAPIC IPI support | `goroutine_design_gc_and_smp.md §3` | todo | `deferred_smp_v2.md §6` |
| 6 | Precise (write-barrier) GC | `goroutine_design_overview.md §2.2`, `goroutine_design_gc_and_smp.md §1.3` | todo | `deferred_gc_and_stacks.md §2` |
| 7 | Growable per-goroutine stacks | `goroutine_design_overview.md §2.2`, `goroutine_design_scheduler.md §3.1` | todo | `deferred_gc_and_stacks.md §3` |
| 8 | B6 fatal-handler CR2/RIP detail preservation | `phase_b_teardown.md §2`, `TODO_B.md` | partial | `deferred_fatal_handlers.md §2` |
| 9 | Orphan Ring-3 goroutine stack leak per exec | `phase_b_ring3_and_exec.md §11`, `TODO_B.md` | partial | `deferred_stack_reclaim.md §2` |
| 10 | ISR-safety lint / CI enforcement | `goroutine_design_channels_and_isr.md §3.4`, `TODO.md` | todo | `deferred_hygiene.md §2` |
| 11 | `R-sleep-granularity`: 10 ms PIT floor | `goroutine_design_scheduler.md §5.2`, `goroutine_design_gc_and_smp.md §8` | todo | `deferred_hygiene.md §6` |
| 12 | `time.After` / full `time` package verification | `goroutine_design_channels_and_isr.md §4` | partial | `deferred_hygiene.md §5` |
| 13 | `R-goroutine-stack-size` audit + bounds | `goroutine_design_gc_and_smp.md §8` | partial | `deferred_gc_and_stacks.md §4` |
| 14 | `R-runtime-alloc-reentry` enforcement | `goroutine_design_scheduler.md §5.5`, `goroutine_design_gc_and_smp.md §8` | partial | `deferred_hygiene.md §3` |
| 15 | `R-global-layout` re-verification on every build | `goroutine_design_gc_and_smp.md §8` | partial | `deferred_hygiene.md §4` |
| 16 | `R-keyboard-latency` measurement + optimization | `goroutine_design_channels_and_isr.md §3.6`, `goroutine_design_gc_and_smp.md §8` | partial | `deferred_hygiene.md §7` |
| 17 | TSS.RSP0 per-Ring-3 goroutine (`R-task-stack-top-unknown`) | `goroutine_design_gc_and_smp.md §8` | **landed (7a5ef02)** | close-out §5 below |

## 2. The six documents

1. **`deferred_overview.md`** — this file. Inventory, DAG,
   ordering, coverage table.
2. **`deferred_fatal_handlers.md`** — item 8.
3. **`deferred_stack_reclaim.md`** — item 9.
4. **`deferred_smp_v2.md`** — items 1–5.
5. **`deferred_gc_and_stacks.md`** — items 6, 7, 13.
6. **`deferred_hygiene.md`** — items 10, 11, 12, 14, 15, 16.

Every `impldoc/deferred_*.md` file cites `file:line` for every
source reference, has `Dependencies` / `Verification` /
`Open questions` subsections, and ends with a
`Risk register delta`.

## 3. Dependency DAG

```
   (TinyGo runtime fork)                        (lint infra)
          │                                          │
          v                                  ┌───────┴───────┐
   per-CPU storage (§7 of deferred_smp_v2)   │               │
          │                                  v               v
   ┌──────┼──────┬──────┬──────┐         item 10         item 14
   v      v      v      v      v             │
 item 3  item 1  item 4  item 5  item 2      └── item 15 (global layout)
 (TSS)  (rq)   (atomics)(IPI) (APIC tmr)

 item 6 (precise GC) ──┐
                       │
 item 7 (grow stacks)──┘  ← shares stack-map substrate with 6

 item 9 (stack reclaim) — design (a) shares the TinyGo fork;
                           design (b) is independent (recommended for v1)

 Independent: item 8 (fatal detail), item 11 (sleep granularity),
              item 12 (time.After), item 13 (stack-size audit),
              item 16 (kbd latency)
```

Notes:

- **Item 9** (stack reclaim) is drawn twice because it has **two**
  candidate designs: (a) TinyGo runtime fork — shares the fork with
  SMP v2 items 1–3; (b) kernel-side pool — independent.
  `deferred_stack_reclaim.md` recommends (b) for v1 because it ships
  without a runtime fork; the fork-based design is kept in reserve
  for when SMP v2 lands.
- **SMP v2 items** (1–5) must all land together; partial SMP-v2 is
  incoherent. The dependency chain inside is: runqueue → APIC
  timer → per-CPU TSS → atomics → IPI. Per-CPU storage (via
  `fs`/`gs` wrmsr) underpins every SMP-v2 item.
- **Precise GC (6)** is a gap analysis, not an implementation spec.
  Growable stacks (7) depend on a precise-GC substrate to avoid the
  conservative scanner falsely pinning freed stack slots; they are
  deferred together.
- **Hygiene items (10, 14, 15)** share infrastructure (the same
  `make lint` / `make verify-globals` targets).

## 4. Recommended implementation ordering

Most items are independent. Suggested execution sequence, smallest
first:

1. **Item 8 (fatal detail)** — isolated edit to `src/vm.go` and
   `src/main.go`; ~60 LOC; one sendkey regression + a manual #PF
   trigger. ~1 hour.
2. **Item 13 (stack-size audit)** — instrumentation-only; add a
   boot-time sweep that prints per-goroutine high-water marks.
   Informs item 7 later. ~1 hour.
3. **Item 15 (global-layout verify)** — new `make verify-globals`
   target wired into `make build`. Catches TinyGo upgrades that
   break `findGlobals` coverage. ~30 min.
4. **Item 10 + 14 (lint)** — `make lint` target; AST walker (or
   careful grep) that flags forbidden calls inside ISR handlers.
   ~3 hours including false-positive suppression.
5. **Item 12 (time.After)** — verification spike first; a failing
   spike unlocks a small local `afterTicks` implementation.
6. **Item 11 (sleep granularity)** — LAPIC one-shot timer for sub-
   10-ms sleeps. Ship only if a kernel goroutine actually needs it;
   otherwise close as "documented limitation".
7. **Item 16 (keyboard latency)** — measurement extension first;
   optimize only if measurement shows ≥20 ms/keystroke.
8. **Item 9 (stack reclaim)** — kernel-side pool variant (design
   (b) in `deferred_stack_reclaim.md §3`). Unlocks long-running
   shell sessions without reboot.
9. **Item 6 / 7 (precise GC + growable stacks)** — gap analysis and
   preliminary growth mitigation. Full precise GC requires either a
   TinyGo fork or upstream contribution; document the path and stop.
10. **Items 1–5 (SMP v2)** — the largest block. Land as one cohesive
    milestone. Requires the TinyGo runtime fork to expose per-CPU
    scheduler hooks. Expected to be several weeks of work.

The ordering is a recommendation, not a mandate. Items 1–5 can
usefully overlap with 6–7 once per-CPU storage infrastructure
exists. 6 and 7 are joint because they share GC invariants.

## 5. Close-out: item 17 has landed

`phase_b_ring3_and_exec.md §4` originally presented two
alternative approaches to track each Ring-3 goroutine's kernel
stack top for `TSS.RSP0`: option 1 ("local-variable-address
trick") and option 2 (side table keyed on `*task.Task`). The
design doc recommended option 1 with option 2 as fallback.

**Phase B commit `7a5ef02` shipped option 2.** The implementation
is in `src/goroutine_tss.go`:

- `gInfoByTask map[uintptr]*gInfo` — side table.
- `registerRing3G()` / `unregisterRing3G()` populate / clear.
- `tssSetRSP0ForCurrentG()` and `gooosOnResume()` look up and
  install `TSS.RSP0` per goroutine switch.
- `taskStackTop(t)` reads `state.stackTop` at offset 40 of the
  TinyGo Task struct (verified at boot by `checkTaskOffset()` in
  `src/main.go`).

The review-time risk `R-task-stack-top-unknown` is retired. No
design action needed.

## 6. Risk-register delta summary

The per-doc `Risk register delta` sections provide detail; below
is the aggregate.

**Retired (after each item lands):**

- `R-task-stack-top-unknown` — already retired in Phase B.
- `R-fatal-detail-loss` — retired by item 8.
- `R-orphan-goroutine-stack` (implicit) — retired by item 9.
- `R-isr-safety-enforcement` — retired by item 10.
- `R-runtime-alloc-reentry` — retired by item 14.
- `R-global-layout` — retired by item 15.
- `R-b5-smp-atomics`, `R-smp-runqueue-race`, `R-smp-ipi-missing`
  — retired when the full SMP v2 block (items 1–5) lands.
- `R-goroutine-stack-size` — retired by item 13 instrumentation
  plus item 7 overflow handling.
- `R-sleep-granularity`, `R-keyboard-latency` — retired when the
  corresponding hygiene items land (11, 16) or documented as
  accepted.

**Added (implementation-time risks per design):**

- `R-tinygo-fork-divergence` (items 1–5, and option (a) of item 9)
  — ongoing cost of maintaining a TinyGo fork.
- `R-percpu-storage-overhead` (SMP v2) — per-CPU data segment
  allocation budget.
- `R-precise-gc-scope` (item 6) — documented as too-large-to-
  schedule until a project-level decision.

## 7. Out-of-scope

- Writing code for any item.
- Re-opening design decisions already settled in Phase A/B.
- Cross-editing `phase_b_*.md` or `goroutine_design_*.md` — those
  are historical records of decisions at the time they were made.
  This design set points at them, not into them.

## 8. Open questions (overview level)

1. **TinyGo fork commitment.** Items 1–5 and option (a) of item 9
   require a gooos-owned fork of TinyGo's runtime. The project has
   so far survived with four small in-place patches
   (`scripts/tinygo_runtime.patch`). A fork is a larger commitment;
   the user should sign off before SMP v2 work begins.
2. **SMP v2 target audience.** Is gooos intended for
   multi-workload production use (justifying SMP v2 now) or is it a
   teaching / research artifact (deferring SMP v2 indefinitely)?
   The answer shapes whether items 1–5 go to "todo" or "paused".
3. **Precise GC priority.** Item 6's cost dwarfs every other
   deferred item. A realistic path is to wait for TinyGo upstream
   to add write barriers, not to implement them locally. Confirm.
