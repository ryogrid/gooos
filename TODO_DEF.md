# TODO_DEF — Deferred-Item Implementation

Tracks every concrete work item from `impldoc/deferred_*.md`.
Order follows `deferred_overview.md §4` (smallest / most
independent first; SMP v2 last).

Mark `- [x]` only after the implementation **and** its
verification step pass. One commit per top-level item.

---

## Phase A — Bootstrap

- [x] Bootstrap commit lands TODO_DEF.md + the six
  `impldoc/deferred_*.md` design docs. (commit `b7dc849`)

## Phase B — Implementation

### `deferred_fatal_handlers.md` (item 8)

- [x] **Item 8** — fatal-handler detail preservation.
  - [x] Add `src/panic.go`: `panicHexBuf [96]byte`,
    `appendHex`, `appendStr`, `bytesToString`.
  - [x] Add `serialPrintBytes` to `src/serial.go`.
  - [x] Rewrite `handlePageFault` in `src/vm.go:280` no-alloc
    + `//go:nosplit`.
  - [x] Rewrite `handleDivisionError` in `src/main.go` no-alloc
    + `//go:nosplit`. Add `//go:nosplit` to `vgaWriteLine`.
  - [x] Verify: `make build` clean.
  - [x] Verify: dev `#PF` trigger emits
    `PF: addr=0xFFFF800000001000 err=0x... rip=0x...`. Trigger removed.
  - [x] Verify: `objdump -d <main.handlePageFault>` reaches
    only `appendStr`/`appendHex`/`vgaWriteLine`/`serialPrintBytes`/
    `serialPutChar`/`hlt`; no `alloc` symbol.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh` (all pf=0).

### `deferred_gc_and_stacks.md` (items 6, 7, 13)

- [x] **Item 13** — boot-time stack-size audit.
  - [x] Add `src/stack_audit.go` with `stackSizeAudit()` over
    captured task handles.
  - [x] Capture `fsTaskHandle`, `keyboardPumpHandle`,
    `ring3WrapperHandle` package-scope vars in their
    goroutines.
  - [x] Wire into `src/main.go` after `checkTaskOffset()` and
    re-fire after the first `elfExec` returns, guarded by
    `const runStackAudit`.
  - [x] Ran audit. Results: main 6%, fsTask 3%,
    keyboardPump 1%, ring3Wrapper 7% (recorded in
    `impldoc/deferred_gc_and_stacks.md §4.5`).
  - [x] All goroutines < 75%; no `default-stack-size` bump.
  - [x] `runStackAudit` flipped back to `false` before commit.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.

### `deferred_hygiene.md` (items 10, 11, 12, 14, 15, 16)

- [x] **Item 15** — `make verify-globals`.
  - [x] Add `scripts/verify_globals.sh` (asserts
    `runtime.sleepQueue|timerQueue|runqueue` lie in
    `[_globals_start, _globals_end)`).
  - [x] Add `verify-globals` Makefile target; wire as `build`
    prereq.
  - [x] Verify: passes on current binary (2 symbols
    `runqueue` and `sleepQueue`; `timerQueue` DCE'd because
    no caller uses timers yet — accepted).
  - [x] Verify: fake-nm wrapper test triggers a clean
    failure with `runtime.runqueue @ 0x... outside [...)`.
  - [x] Verify: `make build` still green and runs the new
    target as part of the normal build.

- [ ] **Items 10 + 14** — ISR-safety lint.
  - [ ] Add `scripts/lint_isr.go` (AST walker; flags string
    concat + `make(chan)` + `go` + runtime-alloc reentry
    inside ISR-reachable functions).
  - [ ] Add `lint` Makefile target; wire as `build` prereq.
  - [ ] Verify: passes on clean tree.
  - [ ] Verify: deliberately inserted violation
    (`serialPrintln("x" + utoa(1))` in `handleTimer`) fails
    the lint. Revert violation.
  - [ ] Verify: 10/10 `bash tmp/test_sendkey.sh`.

- [ ] **Item 12** — `time.After` spike.
  - [ ] Add scratch spike to `src/main.go` behind
    `const timeAfterSpike`.
  - [ ] If link succeeds and spike prints "time.After: OK",
    remove spike and close item.
  - [ ] If link fails, add `src/afterticks.go`
    (`afterTicks(ticks uint64) <-chan struct{}` using
    `sleepTicks` via `//go:linkname`).
  - [ ] Verify: 10/10 `bash tmp/test_sendkey.sh`.

- [ ] **Item 16** — keyboard-latency measurement.
  - [ ] Add `tmp/test_kbd_latency.sh` (burst 100 keys,
    measure wall-clock to last echo).
  - [ ] Run measurement; record result in
    `impldoc/deferred_hygiene.md §7.3`.
  - [ ] If < 20 ms/key, close. If ≥ 20 ms/key, open
    follow-up design (do not implement here).

### `deferred_gc_and_stacks.md §3` (item 7 mitigation)

- [ ] **Item 7 (mitigation)** — stack-overflow diagnosis.
  - [ ] Extend `src/panic.go` with
    `serialPanicStackOverflow(t *task.Task)`.
  - [ ] Patch
    `~/.local/tinygo/src/internal/task/task_stack.go` —
    call diagnostic from `Pause()` on canary mismatch.
  - [ ] Extend `scripts/tinygo_runtime.patch` so the change
    re-applies cleanly.
  - [ ] Verify: deliberate 12 KiB recursive overflow emits
    diagnostic. Revert trigger.
  - [ ] Verify: 10/10 `bash tmp/test_sendkey.sh`.

### `deferred_stack_reclaim.md` (item 9)

- [ ] **Item 9** — Ring-3 stack pool (option 2b).
  - [ ] Add `src/ring3_pool.go` (`ring3StackPoolInit`,
    `ring3StackAcquire`, `ring3StackRelease`).
  - [ ] Modify `src/main.go` to call `ring3StackPoolInit()`
    after `vmInit()`.
  - [ ] Modify `src/process.go`: `ring3Wrapper` acquires;
    `processExit` releases; add `Process.poolIdx`.
  - [ ] Add `registerRing3GWithStack(stackTop)` to
    `src/goroutine_tss.go`.
  - [ ] Conditionally lower `target.json`
    `default-stack-size` only if item 13 audit confirms
    headroom.
  - [ ] Verify: 10/10 `bash tmp/test_sendkey.sh`.
  - [ ] Verify: extended stress-exec script (≥ 500 execs)
    shows `HeapInuse` plateaus.

### `deferred_gc_and_stacks.md §2` (item 6, doc-only)

- [ ] **Item 6** — precise-GC gap analysis lands as
  documentation only.
  - [ ] Update `TODO.md` to point at
    `impldoc/deferred_gc_and_stacks.md §2.3` as the gap
    analysis.

### `deferred_hygiene.md §6` (item 11, doc-only)

- [ ] **Item 11** — 10 ms PIT sleep floor accepted as a
  documented limitation.
  - [ ] Note the floor in `README.md`.

### `deferred_smp_v2.md` — SMP v2 (items 1–5)

- [ ] **SMP v2 §7** — per-CPU storage foundation.
  - [ ] Add `src/percpu.go` (per-CPU block layout;
    `CPU_INTR_DEPTH` byte offset; `cpuID()` helper).
  - [ ] Modify `src/smp.go` `apEntry` to set
    `IA32_GS_BASE` (`wrmsr`) per AP.
  - [ ] Modify `src/isr.S` prologue to write
    `incl %gs:CPU_INTR_DEPTH` instead of the global
    `gooos_in_interrupt_depth`. Update `interruptIn()`
    accessor.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.

- [ ] **Item 3** — per-CPU TSS + GDT.
  - [ ] Modify `src/gdt.go` for `perCPUGDT[maxCPUs]` +
    `perCPUTSS[maxCPUs]`.
  - [ ] Each AP `apEntry` builds and `lgdt`/`ltr` per-CPU
    GDT/TSS.
  - [ ] `src/goroutine_tss.go` `tssSetRSP0` writes
    `perCPUTSS[cpuID()]`.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.

- [ ] **Item 1** — per-CPU runqueues + work stealing.
  - [ ] Extend `scripts/tinygo_runtime.patch`:
    `runtime/scheduler.go` `runqueue` →
    `runqueues[maxCPUs]`; `runtime/chan.go` `resumeRX`/
    `resumeTX` route via `cpuID()` (or target-CPU); new
    `task.Queue.PopTail()`.
  - [ ] Add `src/spinlock.go` (`xchg`-based
    `Acquire`/`Release`).
  - [ ] Add `xchg` helper in `src/stubs.S` only if
    `sync/atomic` is insufficient.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.
  - [ ] Verify: counter-balance smoke (4 goroutines, tight
    counter loop 1 s; counters within ±20%).

- [ ] **Item 4** — `atomic.StoreUint32` / `LoadUint32`
  retrofit.
  - [ ] `src/keyboard_irq.go`: head/tail use atomics.
  - [ ] `src/goroutine_tss.go`: spinlock around
    `gInfoByTask` map access (new `gInfoLock`).
  - [ ] `src/process.go`: spinlock around `procByTask`
    (new `procLock`).
  - [ ] Verify: `objdump -d` shows `lock`-prefixed
    instructions.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.

- [ ] **Item 5** — LAPIC IPI support.
  - [ ] Add `src/lapic_ipi.go` (`lapicSendIPI`).
  - [ ] Register IPI vectors in `src/idt.go` /
    `src/main.go`.
  - [ ] Smoke handler that flips a per-CPU flag.
  - [ ] Verify: BSP→AP IPI smoke succeeds.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.

- [ ] **Item 2** — APIC timer preemption on APs.
  - [ ] Calibrate `lapicTimerCount` against PIT once at
    boot.
  - [ ] Each AP programs LAPIC timer @ 100 Hz periodic.
  - [ ] Per-CPU `handleAPTimer(vector)` registered.
  - [ ] Verify: 10/10 `make run-smp` sendkey green.
  - [ ] Verify: counter-balance smoke (cross-CPU work
    distribution observable).

## Phase C — Reviewer pass

- [ ] Launch `general-purpose` reviewer subagent.
  - [ ] CRITICAL findings addressed inline.
  - [ ] MAJOR findings addressed inline.
  - [ ] MINOR findings: fixed or recorded in
    `## Reviewer follow-ups (MINOR)` below with rationale.
  - [ ] Second pass if first surfaced > 3 design-level
    issues.

## Phase D — Final reconciliation

- [ ] All items in this file are `- [x]`.
- [ ] `git log` shows one commit per implemented item.
- [ ] Repo-wide `Grep` for `TODO|FIXME|XXX|HACK` returns
  no new hits.
- [ ] Final sendkey: 10/10 `make run` and 10/10
  `make run-smp`.
- [ ] `README.md` updated:
  - [ ] Retired risks listed.
  - [ ] New `make` targets (`lint`, `verify-globals`)
    documented.
  - [ ] 10 ms PIT sleep floor documented.
  - [ ] SMP status: BSP-only → SMP v2 working.
- [ ] Final report to user.

## Reviewer follow-ups (MINOR)

(empty — populated by Phase C if any minor issues are
deferred rather than fixed)

## Further deferred

(empty — populated if a deferred item must slip out of
this task's scope; include reason + unlock condition)
