# TODO_NET4 — Fix late-timing RX stall

Implementation of the fix planned against `tcp_problem_review2/`
review findings. Root cause: `afterTicks()` in `src/afterticks.go`
spawns a fresh goroutine per call; the patched TinyGo runtime has
no task-reclaim path, so repeated hot-loop callers (tcp_retx RTO
scanner, kernel echo server idle poll, netsock wait loops) leak
goroutine state until kernel-goroutine scheduling stalls ~12–16 s
post-Ring-3. Symptom: `scripts/test_tcp_latetiming.sh` FAILs on
HEAD with `echoed=''`; tight-timing paths (`test_tcp_phase{1..5}.sh`)
continue to PASS.

Approach: single-dispatcher timer wheel (one long-lived goroutine
drains a sorted deadline list and fires waiter channels), mirrors
`netRxLoop`'s survival pattern. Preserves public signature of
`afterTicks(uint64) <-chan struct{}` so no call-site edits.

One commit per checklist item; tick `- [x]` when the commit lands
and its listed verification passes.

## Phase 1 — Instrumentation (confirm the hypothesis)

- [x] `diag(net): afterTicks call counter` — add `afterTicksCalls`
      uint64 in `src/afterticks.go` incremented on every call.
      Verify: `make build` clean.
- [x] `diag(net): expose afterTicks counter in netDiag` — add row
      in `src/net.go` netDiag output: `Sched: afterTicksCalls=N`.
      Verify: `make run-net` serial shows a nonzero counter.
- [x] `diag(net): capture pre-fix latetiming evidence` — run
      `scripts/test_tcp_latetiming.sh`, archive the serial log to
      `tmp/serial_pre_fix.log` (not committed), confirm the
      counter grows across the netDiag piggyback dumps. Verify:
      counter value in the last dump is substantially larger
      than in the first (monotonic growth). **Confirmed**:
      172 → 180 → 344 across three piggyback dumps, ~20-30
      calls/s matching the hot-loop cadence.

## Phase 2 — Timer-wheel fix

- [x] `feat(spinlock): add lock rank 12 (timerListLock)` — extend
      `src/spinlock.go` lock-ordering comment with rank 12 for
      the afterTicks timer wheel. Verify: `make build` clean.
- [x] `feat(net): single-dispatcher timer wheel in afterTicks` —
      rewrite `src/afterticks.go`: add `timerEntry` array,
      `timerListLock`, `timerDispatcher` goroutine that walks the
      list on every Gosched cycle and fires matured channels,
      `afterTicksInit` spawn hook. `afterTicks(d) <-chan struct{}`
      signature unchanged. Verify: `make build && make lint &&
      make verify-globals` clean.
- [x] `feat(main): spawn timer dispatcher at boot` — call
      `afterTicksInit()` in `src/main.go` after `pitInit`, before
      `netInit`/`ring3Start`. Verify: `make build` clean.
- [x] `test(net): latetiming PASSes + phase1..5 regression green` —
      run all six scripts. Verify:
      `scripts/test_tcp_latetiming.sh` exits 0;
      `scripts/test_tcp_phase{1..5}.sh` each exit 0;
      the Phase-1 counter from the instrumentation still rises
      (afterTicks is still being called), but the stall is gone.
      **Confirmed**: latetiming PASS 3x in a row; phase1..5 all
      PASS; post-fix serial log shows 4 netDiag dumps (vs 3
      pre-fix before the stall froze the loop) with
      afterTicksCalls climbing 196 → 586 while `netRxLoop`
      kept advancing iter=1000 → 3000. Evidence archived at
      `tmp/serial_post_fix.log`.

## Phase 3 — Cleanup of WIP diagnostics

- [ ] `chore(net): restore proper periodic netDiag` — remove the
      `netRxLoop`-piggyback diag at `src/net.go:70-85`, replace
      with a dedicated `afterTicks`-based periodic goroutine now
      that the timer wheel survives. Tone down the expanded
      `netDiag` body added in commit `fe627b5`/`2abec07` to the
      essentials. Verify: serial log shows a steady cadence of
      netDiag dumps over 30+ s without piggybacking on netRxLoop.
- [ ] `chore(net): finalize scheduler counter decision` — either
      keep `afterTicksCalls` as a permanent diagnostic in
      `netDiag` (recommended) or revert. Document the decision
      in the commit body.

## Phase 4 — Docs + reviewer pass

- [ ] `docs+review(net): record fix, reviewer pass, close the bug` —
      Add "Late-timing RX stall" entry to
      `current_impl_doc/known_issues.md` under "Active Workarounds"
      or "Resolved issues". Strike the
      `pasttodos/TODO_NET3.md` late-timing-RX block (lines 472-574)
      with a pointer to the fix commit. Run reviewer subagent,
      classify findings CRITICAL / MAJOR / MINOR; fix
      CRITICAL+MAJOR inline, MINOR entries into "Reviewer findings"
      tail below. Update `README.md` only if user-visible behaviour
      changed (not expected). Verify: all checkboxes in this file
      `- [x]`; `grep -rnE 'TODO|FIXME|XXX' src/ user/` shows no
      new markers vs baseline; `git status --porcelain` clean.

## Deferred further (not in this TODO)

- None yet; populated if anything is encountered mid-task that is
  legitimately out of scope for the stall fix.

## Reviewer findings

CRITICAL:
- (none yet)

MAJOR:
- (none yet)

MINOR:
- (none yet)
