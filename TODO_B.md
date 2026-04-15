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

### Design flaw discovered during B4 attempt (2026-04-15)

**B4 cannot land incrementally without B5+B7+B9**.

The design in `phase_b_channel_migrations.md §3.6` assumed that
native `chan *fsRequest` sends/recvs could land first, with
`createTask(fsTaskEntryAddr())` still wrapping `fsTask` until B7
landed. This does not work.

Concrete symptom: replacing `chanRecv(fsRequestChannel)` with
`for req := range fsReqCh` and flipping the reply channel to
`make(chan *fsResponse, 1)` caused a boot-time panic
(`runtime error: deadlocked: no event source` followed by
`nil pointer dereference`). Every sendkey trial returned
`pf=0 exit=0 cat=0` — the shell never ran.

Root cause: TinyGo's native `chan` operations park blocked
goroutines via `task.Pause()`, which reads `task.Current()` to
identify which goroutine to save state for. When a callers
runs inside a custom-scheduler `Task` (from `createTask`), it
does **not** have a valid goroutine identity — `task.Current()`
returns whatever goroutine was last active in TinyGo's
scheduler (typically the boot goroutine `main`). `task.Pause()`
then writes save state to `main`'s goroutine struct,
corrupting it. The TinyGo scheduler's runqueue is left with no
runnable goroutines → `deadlock()` fires from
`runtime_gooos.go`.

Callers that reach `fsSendRead` from a custom-scheduler task
stack:

- `src/process.go:89` (`elfExec`) — runs on the parent's
  custom Task stack.
- `src/userspace.go` (`sysFsReadHandler`, `sysFsWriteHandler`,
  `sysFsListHandler`) — runs on the child's kernel stack set
  by TSS.RSP0 on `int 0x80` entry; the child itself is a
  custom Task.

For B4 to work, **both** service-task hosts and user-process
hosts must be goroutines. That means B4 is transitively
blocked on B7 (service task spawn) **and** B9 (Ring-3 process
spawn). B8 and B10 follow. Essentially the entire remaining
Phase B must land as one atomic change.

**Decision**: this session halts Phase B at B3. The remaining
nine items (B4, B5, B6, B7, B9, B11, B8, B10, plus README)
move to **Deferred with user guidance required**. The next
attempt needs:

1. An atomic big-bang commit that converts scheduler
   ownership in one go, accepting the loss of
   per-item-commit rollback granularity.
2. Or a mid-state with a shim: a function that synchronously
   drives `fsTask` inside the caller's context (bypassing
   `task.Pause()`). This shim would cost correctness
   guarantees and be a significant design deviation.

Both routes warrant user sign-off before proceeding.
