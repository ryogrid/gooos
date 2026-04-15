# TODO_B — Phase B migrations

Execution of the design in `impldoc/phase_b_*.md`. One commit per item.
Completed items remain here as audit trail — do not delete rows.

## Items

- [x] B1 — close-out note (no code change; subsumed by Phase A)
- [x] B3 — retire `serialChannel` / `serialSend` / `serialTaskEntry`
  (dead code per `phase_b_channel_migrations.md §1.1`)
- [ ] B4 — `fsRequestChannel` → native `chan *fsRequest`; drop
  `fsReqPool` / `fsRespPool` static pools
- [ ] B6 — fatal handlers use `serialPanicPrint` + non-allocating
  hex helper (`phase_b_teardown.md §2`)
- [ ] B5 — keyboard IRQ ring buffer + `keyboardPump` goroutine
  (`phase_b_keyboard_irq.md`)
- [ ] B7 — replace `createTask` in `main.go` with `go fsTask()` /
  `go keyboardPump()`
- [ ] B9 — `elfExec` → `ring3Wrapper` + `exitCh`; two TinyGo
  runtime patches (`state.stackTop` field, `gooosOnResume`
  hook) added to `scripts/patch_tinygo_runtime.sh`
- [ ] B11 — SMP AP idle loop: `sti + hlt` in `apEntry`
- [ ] B8 — delete `src/scheduler.go`, strip `*TaskAddr` stubs in
  `src/switch.S`, update `handleTimer` to drop `schedule()`
- [ ] B10 — delete `src/channel.go`; consider removing
  `src/switch.S` entirely

## Verification gates (after all items)

- [ ] `make build` clean; `nm tmp/kernel.bin | grep " U "` empty
- [ ] 10/10 `tmp/test_sendkey.sh` trials (pf=0, exit=3, cat=1)
- [ ] `tmp/stress_test.sh` (pf=0, exit=6, cat=1)
- [ ] `make run-smp` reaches shell with 4 cores
- [ ] `grep -rE "TODO|FIXME|HACK|XXX|temporarily" src/ user/`
  returns no new hits

## Reviewer pass + README.md update

- [ ] Reviewer subagent run; CRITICAL/MAJOR addressed inline
- [ ] `README.md` updated to reflect post-Phase-B state
  (progress table, assembly section, architecture diagram,
  `src/` layout, prerequisites)

## Deferred (populated during execution)

Out of scope per design (`impldoc/goroutine_design_gc_and_smp.md §8a`
and `impldoc/phase_b_overview.md §4`):

- SMP v2 (per-CPU runqueues, work stealing, AP goroutine
  scheduling)
- Precise (write-barrier) GC
- Growable goroutine stacks
- ISR-safety lint / CI enforcement
- Fatal-handler detail preservation beyond what `serialPanicPrint`
  provides (deferred from B6 if the allocation-free hex helper is
  too cumbersome)

Items discovered during execution will be added below with a
one-line summary and a `phase_b_*.md §N` reference.
