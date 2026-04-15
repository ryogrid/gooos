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
- [ ] **Spike 4 — Boot-goroutine stack**: identify a mechanism to
  give `main()`'s goroutine ≥16 KiB, or install a manual early-in-
  `main` stack swap. Pass when a canary at the stack boundary
  survives the full boot sequence.

## Phase B — Production migration

- [ ] **B1** — Add `src/goroutine_stubs.go` (stub bodies for the
  six runtime hooks; compiles under `scheduler=none` as inert).
- [ ] **B2** — Flip `src/target.json` to `scheduler=tasks`. 10-trial
  sendkey regression.
- [ ] **B3** — Migrate `serialChannel` → native `chan string`.
  Sendkey regression.
- [ ] **B4** — Migrate `fsRequestChannel` + per-request replies
  → native channels. Sendkey regression.
- [ ] **B5** — Replace keyboard IRQ path with ring-buffer + pump.
  Sendkey regression.
- [ ] **B6** — Fatal handlers (`handlePageFault`,
  `handleDivisionError`) use non-allocating `serialPanicPrint` +
  hex helper.
- [ ] **B7** — Replace `createTask` calls in `src/main.go` with
  `go serialTask()` / `go fsTask()`.
- [ ] **B8** — Delete `src/scheduler.go` + dead `*TaskAddr`
  `//go:linkname` declarations.
- [ ] **B9** — Convert `elfExec` to `ring3Wrapper` + `exitCh`
  channel.
- [ ] **B10** — Delete `src/channel.go`. Strip dead stubs from
  `src/switch.S`.
- [ ] **B11** — `src/smp.go` AP trampoline becomes bare
  `sti; hlt`.

## Phase C — Verification gates (after B11)

- [ ] `make build` clean + no unresolved symbols.
- [ ] 10/10 `tmp/test_sendkey.sh` trials (pf=0, exit=3, cat=1).
- [ ] `tmp/stress_test.sh` pass (pf=0, exit=6, cat=1).
- [ ] `make run-smp` reaches shell with 4 cores.

## Phase D — Reviewer subagent pass

- [ ] Reviewer subagent run against the full diff + design docs.
  CRITICAL/MAJOR findings addressed.

## Phase E — Reconciliation

- [ ] `grep -rE "TODO|FIXME|HACK|XXX|temporarily"` over `src/`
  returns nothing new.
- [ ] Final `make build` clean; `git status` shows only expected
  untracked paths.

## Deferred (out-of-scope for this session)

Out-of-scope items already flagged by design review
(`impldoc/goroutine_design_gc_and_smp.md §8a`): precise GC,
ISR-safety lint enforcement, growable goroutine stacks, SMP v2,
fatal-handler detail preservation.

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
