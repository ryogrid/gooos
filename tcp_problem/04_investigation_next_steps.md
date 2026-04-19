# 04 — Next Steps

This is a recommended starting sequence for the fix session. It
is NOT prescriptive — if you spot a better path after reading
the evidence, take it. The aim here is to give a cold-start
agent a concrete first move.

## Step 1 — Confirm the hypothesis before fixing

Add a single `serialPrintln` inside the TinyGo runtime's task
allocation / dispatch path that reports the current task-slot
occupancy. The runtime lives under
`~/.local/tinygo/src/runtime/scheduler_any.go` and related
files (already patched by `scripts/tinygo_runtime.patch`). Wire
the occupancy number into the existing `netDiag` snapshot so it
prints alongside `pitTicks` and `e1000IRQCount`.

Expected outcome:

- If occupancy grows monotonically and plateaus at a cap when
  the heartbeat stops firing: **hypothesis confirmed**,
  proceed to step 2 with fix (a) or (b) from
  `02_evidence_and_hypotheses.md`.
- If occupancy stays low but kernel goroutines still stop:
  hypothesis is wrong. Pivot — the Ring-3 starvation theory
  is the next candidate. Look at how `ring3Wrapper` interacts
  with the runqueue (`current_impl_doc/scheduler.md` has the
  CR3-swap story).

Run `scripts/test_tcp_latetiming.sh` after adding the
instrumentation; the FAIL output includes 60 lines of serial
tail, which should include a netDiag snapshot with the new
counter.

Do not start implementing the fix without this confirmation —
the existing session already burned time on hypotheses that
looked right on paper and turned out to be wrong.

## Step 2 — If the leak is in `afterTicks`, fix (a)

Replace the per-call `go func()` spawn in `src/afterticks.go`
with a single long-lived timer-wheel goroutine. Sketch:

- One goroutine, spawned at init time, owns a sorted list of
  `(deadline, chan)` pairs.
- `afterTicks(d)` allocates a buffered channel, computes the
  deadline, locks the list, inserts, returns the channel.
- The goroutine wakes via `runtime.Gosched` in a tight loop,
  fires channels whose deadline has passed, sleeps (by yielding)
  until the next one.
- The existing `afterTicks(d) <-chan struct{}` signature stays
  identical so every call site keeps working.

This removes per-call goroutine spawns entirely. It also benefits
`sys_sleep` (the primary userspace afterTicks consumer) and the
kernel service goroutines that use afterTicks for their cadences.

Verify:

- `scripts/test_tcp_latetiming.sh` PASSes.
- `scripts/test_tcp_phase{1..5}.sh` all PASS.
- The netDiag task-slot counter from step 1 now stays flat over
  the whole run.

## Step 3 — If the leak is not in `afterTicks`

Possible fix (b): bump the task-slot cap. This is a band-aid and
might be acceptable as a short-term measure, but only if (a) is
infeasible for some reason. Document the rationale in the commit
if you go this route.

Possible pivot to Ring-3 starvation: re-read
`current_impl_doc/scheduler.md` section on `gooosOnResume` and
the CR3 swap. If the issue is that Ring-3 wrappers never yield
back to kernel goroutines, the fix might live in
`src/goroutine_tss.go` or in the patched TinyGo runtime's
runqueue dispatch order.

## Step 4 — Clean up WIP instrumentation

Only **after** the latetiming script PASSes. The investigation
commit `2abec07` already removed the noisiest prints (the
first-20-IRQs "e1000 IRQ fired" print, the 2-second heartbeat
goroutine, the self-rescheduling periodic netDiag). What remains
as WIP-ish in HEAD:

- The piggybacked netDiag cadence inside `netRxLoop`
  (`src/net.go:70-85`, `netRxDiagPeriodIterations` constant + the
  loop body).
  Either remove it once a separate periodic netDiag can be
  re-introduced safely, or at least dial its period up by 10x so
  it doesn't spam the serial log.
- The expanded `netDiag` body (`src/net.go:145-231`) — specifically
  the IMS / RCTL / RDBAL / RDBAH / RDLEN readbacks and the
  DD[0..7] per-descriptor snapshot. These were added for
  diagnosis; you can keep them or trim them, but they should no
  longer be needed after the fix.

Keep these — they are permanent diagnostic fields, not WIP:

- `e1000IRQCount`, `rxReadyFlag`, `lastICR` in `src/e1000_irq.go`
  (file header documents their purpose).
- `NetRxLoopWakes`, `NetRxFrames` in `netStats`. Useful for
  diagnosing any future RX issue.

## Step 5 — Document the fix

Add an entry to `current_impl_doc/known_issues.md` under
"Active Workarounds" (or "Resolved issues" if that section gets
created), naming the bug, the root cause, the fix, and the
regression test (`scripts/test_tcp_latetiming.sh`).

Also update `pasttodos/TODO_NET3.md`: strike the "Known issue —
late-timing RX stall" block, reference the commit that fixes it.

## Watch-outs

### Do not use unbounded background Bash loops in verification

Previous sessions burned time on orphaned Bash processes running
`until <condition>; sleep 1; done` forever. When polling for
the guest to reach a state, always bound the loop (see
`scripts/test_tcp_latetiming.sh` line 37: `for i in $(seq 1 300)`).

### Do not `run_in_background: true` a QEMU that you don't reliably kill

Stuck QEMU processes will hold port 10080 and make subsequent
test runs fail with confusing "bind: address already in use".
Check with `ss -tln | grep 10080` before starting a fresh QEMU;
`pkill -9 qemu-system-x86_64` if needed.

### Respect the gooos shell guidance

Per `CLAUDE.md`: no compound Bash commands, no `/tmp` (use
`tmp/` in the repo), no commits without explicit order, no merge
to master. These are project-wide rules, not just for this bug.

### Keep `fe627b5` intact until the fix lands

The WIP diagnostic commit is load-bearing for verifying the fix.
Don't rebase it away, don't squash it out. Stack the fix on top,
then clean up in the commit after success.

### Regression gates are your friend

`scripts/test_tcp_phase{1..5}.sh` PASS on HEAD. If they stop
passing at any point during your work, you broke something
unrelated to the late-timing stall. Stop, diagnose, don't keep
layering changes.

## Success exit checklist

- [ ] `bash scripts/test_tcp_latetiming.sh` → exit 0
- [ ] `bash scripts/test_tcp_phase1.sh` → exit 0
- [ ] `bash scripts/test_tcp_phase2.sh` → exit 0
- [ ] `bash scripts/test_tcp_phase3.sh` → exit 0
- [ ] `bash scripts/test_tcp_phase4.sh` → exit 0
- [ ] `bash scripts/test_tcp_phase5.sh` → exit 0
- [ ] `make build` clean
- [ ] `make lint` clean (ISR-safety AST check)
- [ ] `make verify-globals` clean
- [ ] `current_impl_doc/known_issues.md` entry added
- [ ] `pasttodos/TODO_NET3.md` late-timing-RX block struck
- [ ] WIP instrumentation from `fe627b5` reverted or toned down
