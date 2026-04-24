# 00 — Index, reading order, cross-refs

This directory captures the full detailed design for Route C, the
"no-goroutine kernel" redesign: gooos's Ring 0 drops TinyGo goroutines
and Go channels entirely and replaces them with a custom
kernel-thread scheduler, per-CPU ready queues, spinlocks, and bounded
lock-based IPC primitives. The design is written for a future Claude
Code session to execute — every claim about the current tree is cited
by `file:line` + symbol so the reviewer can `grep` each reference.

**No code changes land in this cycle.** The cycle produces design
docs only. Implementation is gated on milestone ordering in
[`09_incremental_migration_plan.md`](09_incremental_migration_plan.md).

## Scope tags

Throughout the set we use a small set of tags:

- **STATUS-QUO**: a factual claim about the current tree, cited by
  `file:line` or symbol name. Reviewers must be able to grep each.
- **PROPOSED**: a new symbol / file / invariant created by Route C.
  Each is given a concrete name + target file.
- **INVARIANT**: a rule the design depends on (e.g. "kernel threads
  never change CR3"). Violation is a bug; rules are audit-checkable.
- **MINOR** / **BLOCKING**: reviewer findings (added under §10 once
  Phase B of hoge.md completes).

## Reading order

The docs are numbered for linear read. A short read (the "what and
why") covers 01 + 06 + 11. The deep read for implementers is
02 → 03 → 04 → 05 → 07 → 08, in that order. 09 → 10 wrap up execution
planning.

| # | File | Role | Required for |
|---|------|------|-------------|
| 01 | [`01_overview_and_motivation.md`](01_overview_and_motivation.md) | Why Route C over A/B; flakes closed; capabilities gained | Everyone |
| 02 | [`02_kernel_thread_runtime.md`](02_kernel_thread_runtime.md) | Core design: stack allocator, asm context switch, `KernelThread` struct, per-CPU ready queues | Implementers |
| 03 | [`03_sync_primitives.md`](03_sync_primitives.md) | Replacements for `chan`: semaphore, bounded SPSC/MPMC queues, park-until-tick | Implementers |
| 04 | [`04_preemption_and_isr.md`](04_preemption_and_isr.md) | Timer ISR integration, quantum accounting, STW + spinlock invariants | Implementers |
| 05 | [`05_gc_integration.md`](05_gc_integration.md) | Conservative mark-sweep under a custom scheduler; STW broadcast; stack-bound discovery | Implementers |
| 06 | [`06_service_migration.md`](06_service_migration.md) | 1:1 mapping of every current goroutine / channel to a kernel-thread equivalent | Everyone |
| 07 | [`07_userspace_boundary.md`](07_userspace_boundary.md) | Ring-3 exec path, `ring3Wrapper`-as-kernel-thread, user-side TinyGo stays untouched | Implementers |
| 08 | [`08_build_config_and_tinygo_patch.md`](08_build_config_and_tinygo_patch.md) | Concrete build flip + TinyGo patch hunk audit | Implementers |
| 09 | [`09_incremental_migration_plan.md`](09_incremental_migration_plan.md) | M0 → M5 milestone plan with gating tests | Everyone |
| 10 | [`10_risks_rollback_and_open_questions.md`](10_risks_rollback_and_open_questions.md) | What goes wrong, how to back out, reviewer MINOR parking lot | Everyone |
| 11 | [`11_readme_update_plan.md`](11_readme_update_plan.md) | README diff shape (no README edit this cycle) | Implementers of M4/M5 |

## Cross-reference map

- **F1 flake background** → `current_impl_2026_04_24/fix_plan_deferred_1_5/03_sleep_cross_cpu_channel_wakeup_audit.md`
  (H1..H7 hypotheses, H1 @ 65 %). Cited in §01, §04, §10.
- **H-01 hazard** → `pasttodos/TODO_SCHED.md:347` (the "kernel thread
  on own stack cannot host services that call runtime.Gosched /
  channel ops" hazard). Cited in §01, §02, §06.
- **F1 audit infrastructure** (dormant under `runSleepAudit=false`) →
  `src/preempt_config.go:71`, `src/percpu.go:54`,
  `src/pit.go:73` (ISR-side dump). Cited in §04, §10.
- **TinyGo runtime patch** → `scripts/tinygo_runtime.patch` (1168
  lines; 15 runtime files split kernel-side + user-side). Catalog of
  hunks lives in §08.
- **Current goroutine inventory** → 15 spawn sites across `src/*.go`.
  Full table in §06; the kernel-side demonstrators (`kpHog`,
  `kpMarker`, `smpBasicProbe`) plus anonymous boot-probe goroutines in
  `src/main.go` are called out as dead-on-arrival once Route C lands.
- **Current channel inventory** → 8 channel-typed struct fields + 21
  `make(chan ...)` sites + 12 `<-afterTicks(...)` consumers. Table in
  §06; mapping rules in §03.

## Conventions used in the set

- File paths always include `src/` or the other top-level directory.
- Line numbers refer to the tree at the start of this design cycle
  (branch `smp-no-goroutine-in-kernel`, HEAD as of 2026-04-24). Stale
  references are a reviewer finding.
- New symbols follow the existing gooos naming style:
  `camelCaseForLowerLevelInternals`, `PascalCase` for exported types.
  Run-queue / scheduler primitives all share the `ksched` prefix
  (e.g. `kschedLock`, `kschedRunQueues`, `kschedYield`).
- Type layouts are shown as Go struct declarations in fenced blocks,
  but no runnable code — the docs describe shapes, not implementations.

## Out of scope for the design cycle

Restated here for single-point reference; §10 expands on why:

- No `.go` / `.S` / `.patch` / `.json` edits.
- No `README.md` edit (plan in §11; commit lands with M4 or M5).
- No intra-user-process preemption (the user-side TinyGo
  `scheduler=tasks` stays cooperative — gooos preempts the *process*
  via SIGALRM delivery on the iretq frame; that machinery survives
  untouched, see §07).
- No H-01 / H-03 / H-04 re-gating this cycle.
- No `git push`.

## Commit plan

One commit on branch `smp-no-goroutine-in-kernel`:

- Subject: `no-goroutine kernel: design doc set (Route C)`
- Body: lists files, applied-vs-deferred reviewer findings count,
  pointer to §11.
- Touches only `no_goroutine_kernel_design/*` (and optionally `hoge.md`
  as a pointer, if chosen at commit time).

After the commit, the TL;DR in chat is ≤ 5 lines: commit SHA, file
count, BLOCKING applied, MINOR deferred, §11 pointer. Nothing more.
