# TCP Late-Timing RX Stall: Review Summary (Round 2)

## Scope

This review used:

- `tcp_problem/` handoff docs (`01..04`)
- linked docs (especially `current_impl_doc/scheduler.md`, `current_impl_doc/known_issues.md`, `current_impl_doc/ipc.md`)
- `README.md` networking section
- kernel/runtime source under `src/`
- patched TinyGo runtime sources under `~/.local/tinygo/src/runtime/` and `~/.local/tinygo/src/internal/task/`

The goal here is bug detection only (cause candidates and suspicious code paths).

## Main finding

The strongest current suspect remains a scheduler/runtime-side issue centered on `afterTicks()`:

1. `afterTicks()` spawns a new goroutine per call (`src/afterticks.go:26-35`).
2. Multiple long-lived loops call it repeatedly (`src/tcp_retx.go:138-142`, `src/tcp.go:1351-1357`, `src/userspace.go:433-437`, `src/netsock.go:593/648/784`).
3. Each goroutine creation allocates task+stack state in TinyGo runtime (`~/.local/tinygo/src/internal/task/task_stack.go:92-135`).
4. gooos already documents and compensates for lack of goroutine reaping in `scheduler=tasks` for Ring-3 wrappers (`src/ring3_pool.go:4-14`, `src/process.go:428-452`).
5. The RX path depends on `netRxLoop()` continuously running (`src/net.go:72-99`). If scheduling progress is lost, frames are not drained into `ethernetDispatch`.

This chain still best matches the handoff symptom profile.

## Important nuance

I did **not** find a clear hard-coded fixed task-slot cap in the current patched TinyGo runtime sources.  
So the stronger statement is:

- likely runtime pressure / lifecycle / scheduling-progress failure tied to repeated timer goroutine creation,
- not yet a proven literal “small fixed task table” cap.

## Secondary suspect

Ring-3 interaction remains a secondary possibility:

- `gooosOnResume` runs on every goroutine resume and does Ring-3-specific TSS/CR3 work (`src/goroutine_tss.go:162-214`).
- If instrumentation disproves timer-goroutine accumulation, post-Ring-3 scheduling bias/starvation is the next suspect class.

## Reproduction signal observed in this round

- `bash scripts/test_tcp_latetiming.sh` failed (expected for current HEAD).
- `bash scripts/test_tcp_phase1.sh` passed.

This again supports a timing-dependent failure rather than a universal TCP bring-up failure.

One discrepancy to note: in this local rerun, the retained serial tail showed `e1000IRQs=0` in `netDiag`, which differs from the handoff’s prior captured “ISR keeps firing post-stall” observation. The external symptom (late TCP no-echo) is still reproduced.

## Most suspicious files

- `src/afterticks.go`
- `src/net.go`
- `src/tcp_retx.go`
- `src/tcp.go`
- `src/userspace.go`
- `src/netsock.go`
- `src/ring3_pool.go`
- `src/process.go`
- `~/.local/tinygo/src/internal/task/task_stack.go`
- `~/.local/tinygo/src/runtime/scheduler.go`
- `~/.local/tinygo/src/runtime/runtime_gooos.go`

