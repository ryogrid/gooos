# TODO — Ring-0 goroutine & channel support

Implementation of the design in `impldoc/goroutine_design_*.md`.
Every item is a discrete commit. Completed items remain here as an
audit trail — do not delete.

## Phase A — Prerequisite spikes

- [ ] **Spike 1 — Runtime collision**: decide how to replace
  `runtime_unix.go`'s `sleepTicks`/`ticks`/`ticksToNanoseconds`/
  `nanosecondsToTicks`/`deadlock`/`tinygo_register_fatal_signals`
  with gooos-local bodies. Pass when a build succeeds without
  duplicate-symbol errors.
- [ ] **Spike 2 — Link viability**: trivial `ch := make(chan int); go func(){ ch<-1 }(); <-ch`
  links and boots to the shell banner under QEMU.
- [ ] **Spike 3 — `interrupt.In()`**: `in_interrupt_depth` counter
  in `src/isr.S`, `interruptIn()` exposed via `//go:linkname`.
  Pass when `task.Pause()` outside ISR does not panic.
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

(empty at start — items populated when encountered)
