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

- [x] **Items 10 + 14** — ISR-safety lint.
  - [x] Add `scripts/lint_isr.go` (AST walker, stdlib-only;
    flags string concat / `make(chan)` / send / receive /
    `go` / slice-or-map literals / interface boxing inside
    every ISR-reachable function chain, depth ≤ 4, with
    safelist of 22 reviewed-safe helpers).
  - [x] Add `lint` Makefile target; wire as `build` prereq
    (runs before TinyGo compile, with `LINT_BIN` cached).
  - [x] Verify: lint exit 0 on clean tree.
  - [x] Verify: deliberate
    `serialPrintln("debug: " + utoa(pitTicks))` inside
    `handleTimer` triggered
    `ISR-LINT: src/pit.go:43:16: string concat in
    handleTimer (root=handleTimer)` and exit 1. Reverted.
  - [x] Verify: `make build` runs lint first, exit 0.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.

- [x] **Item 12** — `time.After` spike.
  - [x] Spike with `import "time"` failed to link
    (`reflect.Value.Complex` wants SSE; gooos build has SSE
    disabled). Took the design's fallback path.
  - [x] Added `src/afterticks.go` —
    `afterTicks(d uint64) <-chan struct{}`. Uses
    `runtime.Gosched` between `pitTicks` checks (NOT
    `sleepTicks`, which deadlocks; rationale recorded in
    `impldoc/deferred_hygiene.md §5.2`).
  - [x] Boot-time self-test (background goroutine) prints
    `afterTicks: OK` ~20 ms after spawn; observed in serial
    log.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.

- [x] **Item 16** — keyboard-latency measurement.
  - [x] `tmp/test_kbd_latency.sh` bursts 100 keys via QEMU
    monitor with no inter-key delay and waits for 100
    'a' echoes on serial after a snapshot baseline.
  - [x] Recorded measurement in
    `impldoc/deferred_hygiene.md §7.3` and §11
    (R-keyboard-latency retired, dated 2026-04-15).
  - [x] Result: 19.929 ms/key (single trial), reproduced
    at 19.888 ms/key on a re-run. Both < 20 ms threshold
    → PASS, item closed without optimization.
  - [x] Margin is tight (~0.4% headroom); harness left in
    place as a one-shot regression check.

### `deferred_gc_and_stacks.md §3` (item 7 mitigation)

- [x] **Item 7 (mitigation)** — stack-overflow diagnosis.
  - [x] Extend `src/panic.go` with
    `gooosStackOverflow(t uintptr)` (no-alloc, `//go:nosplit`).
    Prints `STACK OVERFLOW: task=... top=... canaryPtr=...`
    on serial + VGA, then halts.
  - [x] Patch `~/.local/tinygo/src/internal/task/task_stack.go`
    `Pause()` to call the gooos hook (instead of falling
    straight into `runtimePanic`) on canary mismatch.
  - [x] Extend `scripts/tinygo_runtime.patch` with the new
    hunk (state struct comment, linkname declaration,
    Pause() body change). Verified by reverting +
    re-applying cleanly via `scripts/patch_tinygo_runtime.sh`.
  - [x] Verify: dev trigger that corrupts the canary
    directly (more deterministic than recursion-based
    overflow, which the auto-stack-size estimator absorbs)
    fires the diagnostic on next yield. Trigger removed.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.

### `deferred_stack_reclaim.md` (item 9)

- [x] **Item 9** — Ring-3 stack pool (option 2b).
  - [x] Add `src/ring3_pool.go` (`ring3StackPoolInit`,
    `ring3StackAcquire`, `ring3StackRelease`,
    `maxRing3Procs = 32`).
  - [x] Modify `src/main.go` to call `ring3StackPoolInit()`
    after `vmInit()`.
  - [x] Modify `src/process.go`: `ring3Wrapper` acquires
    on entry; `processExit` releases before
    `taskPause()`; add `Process.poolIdx`.
  - [x] Initialize `poolIdx = -1` in `elfExec` child and in
    `elfLoad`'s boot-shell `Process` struct.
  - [x] Add `registerRing3GWithStack(stackTop uintptr)` to
    `src/goroutine_tss.go` so ring3Wrapper can install the
    pool-owned stack into TSS.RSP0 instead of the
    goroutine's own stack.
  - [x] No `target.json` change — item 13 audit showed
    ring3Wrapper at 7%, headroom plenty already.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.
  - [x] Verify: `bash tmp/stress_test.sh` (5 sequential
    execs in one session) passes — no heap growth, no
    pool exhaustion.

### `deferred_gc_and_stacks.md §2` (item 6, doc-only)

- [x] **Item 6** — precise-GC gap analysis lands as
  documentation only.
  - [x] `TODO.md` "Previously flagged" section updated with
    a 2026-04-15 status note pointing at
    `impldoc/deferred_gc_and_stacks.md §2.3`.

### `deferred_hygiene.md §6` (item 11, doc-only)

- [x] **Item 11** — 10 ms PIT sleep floor accepted as a
  documented limitation.
  - [x] `README.md` "Known limitations" section now records
    the 10 ms floor and points at
    `impldoc/deferred_hygiene.md §6` for the LAPIC-one-shot
    follow-up if a sub-10-ms caller ever appears.

### `deferred_smp_v2.md` — SMP v2 (items 1–5)

**Deferred from this round** — see `## Further deferred`
below.

## Phase C — Reviewer pass

- [ ] Launch `general-purpose` reviewer subagent.
  - [ ] CRITICAL findings addressed inline.
  - [ ] MAJOR findings addressed inline.
  - [ ] MINOR findings: fixed or recorded in
    `## Reviewer follow-ups (MINOR)` below with rationale.
  - [ ] Second pass if first surfaced > 3 design-level
    issues.

## Phase D — Final reconciliation

- [x] All non-SMP-v2 items are `- [x]`; SMP v2 items 1–5
  recorded under `## Further deferred`.
- [x] `git log` shows one commit per implemented item
  (b7dc849..a24b1f8 = 12 commits including bootstrap and
  Phase C reviewer).
- [x] Repo-wide `Grep` for `TODO|FIXME|XXX|HACK` over `src/`
  and `scripts/`: zero hits. Single `Makefile` hit is a
  doc reference to `TODO.md` (the filename), not a marker.
- [x] Final sendkey: 10/10 `make run`. SMP smoke under
  `-smp 4` reaches the shell prompt with all 4 cores
  online (`SMP: 4 cores online` on serial); APs idle as
  expected since SMP v2 is deferred.
- [x] `README.md` updated:
  - [x] Progress table extended with new rows for ISR
    lint, verify-globals, ring-3 stack pool, allocation-
    free fatal handlers, stack-overflow diagnostic, boot
    stack-size audit, `time.After` replacement.
  - [x] SMP row updated to "Done (v1)" with a pointer to
    the SMP v2 deferral.
  - [x] `make lint` and `make verify-globals` documented
    in the Build section, alongside the expanded
    five-phase build pipeline.
  - [x] 10 ms PIT sleep floor documented under "Known
    limitations" (landed as part of item 11's commit).
- [ ] Final report to user (next action).

## Reviewer follow-ups (MINOR)

Reviewer pass on commits `b7dc849..b71805b` returned PASS
with no CRITICAL or MAJOR findings. The six MINOR items:

1. `src/stack_audit.go:29-33` — irregular column alignment
   in the var block. **Status:** left as-is. Not gofmt'd
   because the kernel package is not a target for `gofmt`
   today (TinyGo accepts the spacing); applying gofmt
   would create churn unrelated to deferred items. Revisit
   if the project ever adds a project-wide `make fmt`.
2. `src/process.go:237-238` — `firstExecAudited` declared
   *after* its first reference. **Status:** left as-is.
   Legal Go, package-scope vars may be declared anywhere;
   moving it would touch unrelated git blame.
3. `src/process.go:229-232` — `firstExecAudited` flip runs
   even when `runStackAudit=false`. **Status:** left as-is.
   One bool write per exec is negligible; the audit
   short-circuit inside `stackSizeAudit` already keeps the
   serial-print cost zero. A guard at the call site would
   leak the audit constant to a wider scope.
4. `scripts/lint_isr.go:65-68` — struct field alignment
   non-gofmt. **Status:** same rationale as item 1.
5. `src/main.go:321-333` — Spike 2 probe comment claims
   "Removed once the full migration lands". The full
   migration is landed (Phase B done). **Status:** retained
   as a passive smoke test; the comment is stale but the
   probe still has signal value (catches scheduler/chan
   regressions on every boot before the shell launches).
   The stale-comment fix would itself add no signal.
6. Self-referential nit: this `## Reviewer follow-ups`
   section was empty when the reviewer ran. **Status:**
   addressed by populating it now.

## Further deferred

**SMP v2 items 1–5** — original design in
`impldoc/deferred_smp_v2.md`. The user-approved
implementation strategy was "extend the in-place patch
(`scripts/tinygo_runtime.patch`)". Item 1 (per-CPU runqueues
+ work stealing) needs more than a unified-diff patch can
comfortably express:

- Replace `runqueue` (single `task.Queue` global) with
  `runqueues[maxCPUs]` and rewrite every `Push/Pop` call
  site in `runtime/scheduler.go` (5+ sites) and
  `runtime/chan.go` (`resumeRX`, `resumeTX`).
- Add a way for APs to enter `scheduler()` — currently only
  `main()` calls `run()` which calls `scheduler()` once on
  the BSP. APs would need their own scheduler instance.
- Decide per-CPU vs shared sleepQueue / timerQueue (and add
  locking either way).
- Add work-stealing (`PopTail`), cross-CPU IPI nudges, and
  `cpuID()` plumbing inside the runtime.

These changes amount to a small fork of TinyGo's runtime —
the ergonomic break-point the plan flagged as a stop
condition. Items 2–5 (APIC timer, per-CPU TSS, atomics
retrofit, LAPIC IPI) are mostly useless without item 1
(APs would still idle), so deferring all five together
keeps the codebase coherent.

**Reason**: blocked on a TinyGo fork commitment (declined
this round in favour of the in-place patch flow that has
already proven its limits here).

**Unlock condition**: user signs off on a gooos-owned
TinyGo fork (per `impldoc/deferred_overview.md §8 Q1`),
hosted as a submodule, vendored copy, or maintained branch.
Once the fork exists, `impldoc/deferred_smp_v2.md`'s
implementation order remains valid: §7 → item 3 → item 1 →
item 4 → item 5 → item 2.
