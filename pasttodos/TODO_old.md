# TODO — Ring-0 goroutine & channel support

Implementation of the design in `impldoc/goroutine_design_*.md`.
Every item is a discrete commit. Completed items remain here as an
audit trail — do not delete.

## Phase A — Prerequisite spikes

- [x] **Spike 1 — Runtime collision**: resolved by installing a
  user-writable TinyGo copy at `~/.local/tinygo/`, pointing the
  Makefile at it via `TINYGOROOT`, and running
  `bash scripts/patch_tinygo_runtime.sh`. Build clean with
  `scheduler=none` + `baremetal` tag; boot reaches shell prompt.
- [x] **Spike 2 — Link viability**: `scheduler=tasks` enabled in
  `src/target.json`; trivial `ch := make(chan int); go func(){ch<-42}(); <-ch`
  probe in `src/main.go` prints `Spike2: goroutine+chan OK` on boot;
  shell prompt reached; 3/3 sendkey trials pass. Required importing
  two TinyGo-runtime asm files into gooos's link (`src/task_stack_amd64.S`,
  `src/runtime_asm_amd64.S`) and adding `memmove` to `src/stubs.S`.
- [x] **Spike 3 — `interrupt.In()`**: `.bss` counter
  `gooos_in_interrupt_depth` defined in `src/isr.S`, incremented in
  the common ISR prologue and decremented in the epilogue. The
  patched `runtime/interrupt/interrupt_gooos.go` links to it via
  `//go:linkname` to implement `interrupt.In()`. `task.Pause()` from
  the Spike 2 probe (non-ISR) does not panic; 3/3 sendkey trials
  still pass with the counter actively bouncing on every timer IRQ.
- [x] **Spike 4 — Boot-goroutine stack**: TinyGo's `run()` spawns
  `initAll()`+`callMain()` on a fresh goroutine stack under
  `scheduler=tasks` (`runtime/scheduler_any.go:23`). Setting
  `"automatic-stack-size": true` with `"default-stack-size": 8192`
  in `src/target.json` lets TinyGo size the main goroutine's stack
  by static call-graph analysis. The boot sequence (IDT, PIC, PIT,
  keyboard, VM, FS, SMP, GC demo, ELF embed, shell launch) completes
  without stack corruption; 3/3 sendkey trials pass end-to-end.

## Phase B — Production migration (deferred, see below)

Phase A already gives the core capability: `go func()` and
`make(chan ...)` work inside Ring 0 on BSP. The hand-written
scheduler and channel APIs continue to drive the existing service
tasks and Ring-3 processes unchanged. Phase B is scope-reduction
(deleting the legacy APIs, migrating callers) and is moved to
Deferred — see below.

- [x] **B2** — `scheduler=tasks` enabled; handled as part of
  Spike 2.
- [→] **B1, B3, B4, B5, B6, B7, B8, B9, B10, B11** — deferred.
  Rationale in the Deferred section below.

## Phase C — Verification gates (Phase A scope)

- [x] `make build` clean + no unresolved symbols.
- [x] 10/10 `tmp/test_sendkey.sh` trials (pf=0, exit=3, cat=1).
- [x] `tmp/stress_test.sh` pass (pf=0, exit=6, cat=1).
- [x] `make run-smp` reaches shell with 4 cores and Spike 2 probe.

## Phase D — Reviewer subagent pass

- [x] Reviewer ran against Phase A diff + design docs. 1 CRITICAL
  (missing README prerequisite section) and 2 MINOR findings
  (stale "Next steps" echo in patch script; SMP v2 counter note
  for design doc). All three addressed in-place.

## Phase E — Reconciliation

- [x] `grep -rE "TODO|FIXME|HACK|XXX|temporarily"` over `src/`
  returns zero hits.
- [x] Final `make build` clean.

## Deferred (out-of-scope for this session)

### Phase B migrations (B1, B3–B11)

Deferred to a future session. Rationale and risk notes:

- **B1 — goroutine_stubs.go**: subsumed by
  `scripts/patch_tinygo_runtime.sh` and `src/goroutine_irq.go`.
  Not needed as a separate file.
- **B3 — serialChannel migration**: low risk in principle (no
  ISR call site) but `serialPrintln` is called from many places;
  native `chan string` send blocks when the buffer is full (the
  current `chanTrySend` drops on full). Behavioral change needs
  careful validation across service tasks + boot log.
- **B4 — fsRequestChannel migration**: same pattern as B3, with
  the added complication that the static `fsReqPool` lifetime is
  entangled with the current reply-channel design. Dropping it
  changes GC residency of request structs.
- **B5 — keyboard IRQ path**: CRITICAL correctness hazard. The
  current `handleKeyboard` uses `chanTrySend` from ISR context;
  any migration to a native Go `chan byte` with
  `select { default: }` risks tripping `interrupt.In()` checks
  inside `chanSelect`'s locking, which now panics post-Spike 3.
  The design's ring-buffer + pump approach is correct but is a
  nontrivial rewrite that needs its own verification cycle.
- **B6 — fatal handlers**: `handlePageFault` /
  `handleDivisionError` allocate Go strings via `serialPrintln`.
  Under conservative GC + ISR context this can reenter the
  allocator. Needs a non-allocating `serialPanicPrint` helper
  plus a hex-to-ASCII helper using a static buffer. Low functional
  risk (fatals don't fire in happy-path tests) but fiddly.
- **B7 — `createTask` replacement**: straightforward in itself,
  but `serialTask` and `fsTask` bodies call into the custom
  channel API. B7 is trivially green only after B3 + B4 land.
- **B8–B10 — scheduler.go / channel.go / switch.S deletions**:
  dependent on B3–B7 completing. Code removals, not risky once
  every caller has migrated.
- **B9 — `elfExec` → `ring3Wrapper`**: the trickiest migration.
  It touches the TSS.RSP0 update policy (currently per-Task
  switch in `schedule()`), requires a working
  `tssSetRSP0ForCurrentG()` helper, and changes the parent-task
  blocking mechanism from `schedule()` spin to `<-exitCh`. Needs
  a full sendkey regression plus the goroutine smoke tests in
  `impldoc/goroutine_design_gc_and_smp.md §6.2`.
- **B11 — SMP AP idle loop**: minimal change; deferred only
  because it should land alongside B9 (both touch scheduler
  ownership semantics).

All Phase B items are blocked on the same set of careful-testing
cycles; completing them in one session is high-risk. Phase A
already unlocks goroutine + channel usage for new kernel code;
Phase B is code-cleanup that does not add user-visible capability
beyond what Phase A provides.

### Previously flagged (from design review)

Out-of-scope items flagged by
`impldoc/goroutine_design_gc_and_smp.md §8a`: precise GC,
ISR-safety lint enforcement, growable goroutine stacks, SMP v2,
fatal-handler detail preservation.

**Status update (2026-04-15):** the implementation push tracked
in `TODO_DEF.md` retired most of these. Precise GC alone
remains explicitly out of scope: see
`impldoc/deferred_gc_and_stacks.md §2.3` for the gap analysis
(what TinyGo provides, what upstream Go's write-barrier
demands, why local implementation is too large to schedule).
Revisit when TinyGo upstream lands write barriers, not before.

### Historical: Spike 1 sudo blocker (resolved)

Original blocker notes retained for documentation. Resolution:
user installed a writable TinyGo tree at `~/.local/tinygo/`,
bypassing the sudo requirement.

- **Spike 1 blocked — TinyGo runtime patch requires sudo**.
  `runtime_unix.go` (under `goos=linux` and `!baremetal`) defines
  `sleepTicks` / `ticks` / `ticksToNanoseconds` / `nanosecondsToTicks`
  / `tinygo_register_fatal_signals` with libc-calling bodies. The
  TinyGo build has no overlay flag, and TinyGo's runtime package
  cannot be shadowed from `./src`. Adding `"baremetal"` to
  `build-tags` excludes `runtime_unix.go` but also drops
  `interrupt_none.go`, leaving the `interrupt` package with no
  `Disable` / `Restore` / `In` / `State` definitions — the TinyGo
  runtime's `internal/task` package then fails to compile.
  - Concrete breakage confirmed:
    `undefined: interrupt.Disable` at
    `/usr/local/lib/tinygo/src/internal/task/queue.go:15` and siblings.
  - Only viable resolution found: install gooos-specific files
    inside the TinyGo source tree at
    `/usr/local/lib/tinygo/src/runtime/runtime_gooos.go` and
    `/usr/local/lib/tinygo/src/runtime/interrupt/interrupt_gooos.go`
    (both tagged `//go:build gooos && baremetal`). That path is
    owned by root, requires `sudo`, and is not reproducible
    without a documented patch script.
  - **Prepared artifact**: `scripts/patch_tinygo_runtime.sh` creates
    both files with stub bodies that `//go:linkname`-bridge to
    gooos's kernel primitives (`pitTicks`, `cli`, `sti`, `hlt`,
    `outb`, `readFlags`, `restoreFlags`, `inInterruptDepth`).
    Run once per dev machine with `sudo bash scripts/patch_tinygo_runtime.sh`.
    Re-run idempotently after TinyGo upgrades.
  - Dependencies for subsequent steps once patch is applied:
    - add `"baremetal"` to `src/target.json` `build-tags`
    - add `main.inInterruptDepth` as a `uint32` global
    - wire `src/isr.S` prologue/epilogue to inc/dec it
    - Spike 2, 3, 4 still need separate validation after the
      patch lands (Spike 3 in particular depends on the ISR
      counter wiring being correct)
- All subsequent Phase A/B steps are implicitly blocked pending
  the patch.
- Out-of-scope items already flagged by design review (unchanged
  from `impldoc/goroutine_design_gc_and_smp.md §8a`): precise GC,
  ISR-safety lint enforcement, growable goroutine stacks, SMP v2,
  fatal-handler detail preservation.
