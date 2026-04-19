# 03 — gooos Design-Doc Map

gooos carries two design-doc roots:

- `current_impl_doc/` — 8 authoritative as-built docs, dated
  around 2026-04-14 through 2026-04-17. **Prefer these.**
- `impldoc/` — ~50 historical design docs, many of which
  describe intermediate states of the system. A few are
  materially stale and will mislead you if you take them at
  face value.

This file names what to read for this bug and what to skip.

**Terminology**: "Phase B" appears frequently in the older
`impldoc/` docs. It refers to the migration from gooos's
original hand-written scheduler (`src/scheduler.go`, now deleted)
to the TinyGo `scheduler=tasks` runtime that runs today. It is
step 5 in the evolution order at the bottom of this file. When
you see a doc that predates Phase B (gc docs are the most common
offenders), read it for *motivation* only, not as a description
of what currently runs.

## Read first (in order)

### `current_impl_doc/scheduler.md`

The single most important doc for this bug. It states:

> gooos **does not have a custom scheduler**. Every running
> thing in the kernel — service loops, Ring-3 wrappers,
> `afterTicks` timers, per-process exit watchers — is a TinyGo
> goroutine. TinyGo's `scheduler=tasks` runtime (loaded from
> `~/.local/tinygo/src/runtime/scheduler_any.go` + a few gooos
> patches) does all the context-switching.

It also documents why ISR context cannot touch the runqueue,
why `sleepTicks` is a busy `sti; hlt` loop (not a parking
primitive), and the `gooosOnResume` hook that performs the
Ring-3 CR3 swap. All of this context is load-bearing.

### `current_impl_doc/known_issues.md`

The authoritative list of known limitations and workarounds.
Notes:

- Contains "No preemption — cpu-bound goroutine starves the
  scheduler" as a known limitation.
- Contains `sys_sleep` uses `afterTicks` not `time.Sleep`
  (workaround entry).
- **Does NOT currently list the late-timing RX stall.** Your
  fix session should add an entry here (see
  `04_investigation_next_steps.md`).

### `impldoc/smp_kernel_scheduler.md`

Describes how the scheduler is supposed to behave on SMP.
gooos currently runs BSP-only (APs halt after boot; see
known_issues.md), so most SMP-specific details are deferred.
But the runqueue / wake semantics described here apply to the
single-core path in use today.

### `impldoc/goroutine_design_scheduler.md`

Describes the **transition plan** from the old hand-written
scheduler (`src/scheduler.go`, deleted) to the TinyGo runtime.
The "old scheduler" section references a fixed `maxTasks = 32`
slot cap in the removed code — an interesting historical signal
that gooos has precedent for a 32-slot cap, though the TinyGo
runtime's cap (if any) is independent and must be verified by
reading `~/.local/tinygo/src/runtime/`.

Treat this doc as **partly historical**. Section 1 describes
deleted code; sections 2+ are the design for what currently
runs. If you see references to `src/scheduler.go` lines —
those are the OLD file which no longer exists.

### `src/afterticks.go` (source, not docs)

Whole file is 36 lines. The relevant function is 12 lines. Read
it directly; it's the primary suspect in the leading hypothesis
and the comment header explicitly warns about the sleepTicks
deadlock that motivated this design.

## Skip — stale enough to confuse

### `impldoc/conservative_gc_design.md` and `impldoc/conservetive_gc_desing_guide.md`

Both pre-date Phase B and reference the deleted hand-written
scheduler. They describe a conservative-GC-plus-custom-scheduler
world that no longer exists. For the current GC story, the
`current_impl_doc/memory.md` / `known_issues.md` pair is
authoritative. The typo ("conservetive ... desing") in the
second filename is in the repo, not in this doc.

### `impldoc/phase_b_overview.md` and `impldoc/goroutine_design_overview.md`

Both describe the Phase B migration plan — the transition FROM
hand-written scheduler TO TinyGo runtime. Useful for history /
motivation but superseded by `current_impl_doc/scheduler.md`
for the as-built state. Skip them unless you are specifically
interested in why the migration happened.

## Not immediately relevant but nice to know

- `current_impl_doc/ipc.md` — channel semantics and `afterTicks`
  usage patterns. Confirms afterTicks is the standard idiom for
  delayed wakes.
- `current_impl_doc/syscalls.md` — lists kernel syscalls. The
  TCP syscalls (28-33) are not directly involved in this bug,
  but the syscall boundary is where Ring-3 transitions through
  `ring3Wrapper` and possibly contributes to the Ring-3-starves-
  kernel hypothesis.
- `impldoc/deferred_hygiene.md` — referenced by afterticks.go
  §5 for the sleepTicks deadlock rationale.

## Evolution order (for decoding older docs)

When you hit a doc that doesn't match the current source, place
it on this timeline:

1. **Boot** — Multiboot2 entry, long-mode setup, IDT / GDT / TSS.
2. **GC** — conservative GC with root-scan and page-table pointer
   false positives.
3. **Ring-3** — userspace via iretq, per-process PML4 with shared
   kernel PDP[0], kernel PDP[3] for BAR0 MMIO and device pages.
4. **Phase A goroutines** — hand-written scheduler
   (`src/scheduler.go`, fixed 32-task table, removed in Phase B)
   with WaitQueue channels. Most old `impldoc/` docs sit here.
5. **Phase B** — migration to TinyGo `scheduler=tasks`.
   `src/scheduler.go` deleted. `current_impl_doc/` written.
   `afterTicks` became the standard delayed-wake primitive.
6. **Shell I/O** — Ring-3 shell, cooperative stdin parking on
   keyboard ring buffer.
7. **SMP v1** — APs detected and brought up but immediately
   halted; BSP runs everything.
8. **Userspace GC** — per-process `gc=leaking` with a 256 KiB
   heap ceiling; conservative GC option exists behind a revert.
9. **Networking** — e1000 driver, ARP / IPv4 / ICMP / UDP, RX
   polling via `netRxLoop`.
10. **TCP** — Phases TCP-1..TCP-5, the most recent work.
    `pasttodos/TODO_NET3.md` tracks this. This is the layer
    where the bug **manifests** but not where it **lives**.

The bug manifests at step 10 but its cause is at step 5 (Phase B
scheduler behaviour) or step 6 (Ring-3 yield interaction). When
you read old Phase A docs, remember they describe the *predecessor*
of what's running — useful for understanding what changed and why.

## Source files to read alongside the docs

Short list, all on branch `tcp-take2`:

- `src/afterticks.go` — 36 lines, primary suspect.
- `src/net.go` — `netInit`, `netRxLoop`, `drainRxRing`,
  `netDiag`. Lines 72-231 are the relevant window.
- `src/e1000_irq.go` — 73 lines, the ISR and its diagnostic
  fields (`e1000IRQCount`, `rxReadyFlag`, `lastICR`).
- `src/pit.go` — 48 lines. Whole file.
- `src/main.go:netInit` call site — confirms what goroutines
  the kernel spawns at boot.
- `pasttodos/TODO_NET3.md` lines 472-574 — original investigation
  writeup.

For the TinyGo runtime internals (task-slot pool, runqueue
dispatch), read directly under `~/.local/tinygo/src/runtime/`
— those files are patched by `scripts/tinygo_runtime.patch`
and are where a scheduler-instrumentation change would land.
