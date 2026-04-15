# Userspace Goroutines & Channels — Overview and Execution Map

This document is the entry point to the design set for making
native Go `go func()`, `chan`, `select`, and `time.Sleep`
available inside Ring-3 user programs on gooos. The kernel
already runs on TinyGo's `scheduler=tasks` runtime (Phase A/B);
this design set closes the gap in userspace.

Produces **design documents only** — implementation is a future
Claude Code session's job. See
`impldoc/userspace_tinygo_runtime.md`,
`impldoc/userspace_scheduler_integration.md`,
`impldoc/userspace_gc_and_stacks.md`,
`impldoc/userspace_verification.md` for topic-specific detail.

## 1. Problem statement

User programs today compile against a minimal TinyGo runtime
with `scheduler=none`, `gc=leaking`, `default-stack-size=4096`
(`user/target.json:7-12`). That gives them Go syntax + heap
allocation via `sys_sbrk`, but `go func()` produces a
"scheduler=none disallows goroutines" compile-time error and
`chan` operations fall through to the stub impls in
`~/.local/tinygo/src/internal/task/task_none.go`.

Concrete user-observable gaps:

- No concurrent I/O vs CPU work within a single user process.
- No in-process producer/consumer pipelines backed by `chan`.
- No `time.Sleep`-driven timers without blocking the whole
  program.

Kernel-side this was solved by four edits to the TinyGo tree
(`scripts/tinygo_runtime.patch`). The same mechanism can
serve userspace; the design work below decides the build-tag
split, names the new runtime files, and spells out which
existing TinyGo primitives need overriding.

## 2. Inventory of blockers

Every blocker is mapped to the topic doc that resolves it:

| # | Blocker | Location | Resolved in |
|---|---|---|---|
| U1 | `user/target.json` has `scheduler=none`; `go func()` won't compile | `user/target.json:8` | `userspace_tinygo_runtime.md §2` |
| U2 | `user/rt0.S` has no `gooosOnResume` symbol; patched `internal/task/task_stack_amd64.go` calls it unconditionally once `scheduler.tasks` is set | `~/.local/tinygo/src/internal/task/task_stack_amd64.go:47-53`, `user/rt0.S` | `userspace_tinygo_runtime.md §4` |
| U3 | `user/rt0.S` has no `gooosStackOverflow` symbol; patched `internal/task/task_stack.go` calls it from `Pause()` on canary mismatch | `~/.local/tinygo/src/internal/task/task_stack.go:48-67`, `user/rt0.S` | `userspace_gc_and_stacks.md §3` |
| U4 | No userspace `sleepTicks` / `ticks` / `nanosecondsToTicks` / `ticksToNanoseconds` that speak to gooos syscalls | `~/.local/tinygo/src/runtime/runtime_unix.go:1` (currently loaded; calls libc) | `userspace_tinygo_runtime.md §3` |
| U5 | No userspace `interrupt.In()` that is a no-op (userspace has no IRQs) | `~/.local/tinygo/src/runtime/interrupt/` | `userspace_tinygo_runtime.md §5` |
| U6 | `runtime_unix.go` is loaded by default under `goos=linux && !baremetal`; its libc-dependent bodies fail at link or call time | `~/.local/tinygo/src/runtime/runtime_unix.go:1` | `userspace_tinygo_runtime.md §2.2` |
| U7 | TinyGo tree is shared between kernel and userspace builds; existing `runtime_gooos.go` / `interrupt_gooos.go` gated on `gooos && baremetal` would be pulled into a userspace build that sets both tags | `~/.local/tinygo/src/runtime/runtime_gooos.go:1` | `userspace_tinygo_runtime.md §2.3` (build-tag split via a new `kernelspace` tag) |
| U8 | `default-stack-size=4096` is too small for TinyGo's scheduler-driven runtime (Spike-4 era kernel work sized at 8192) | `user/target.json:12` | `userspace_gc_and_stacks.md §2` |
| U9 | A user goroutine that issues a kernel-blocking syscall (`sys_read`, `sys_wait`) freezes the whole user process because the kernel-side `ring3Wrapper` goroutine parks | `src/userspace.go` handlers | `userspace_scheduler_integration.md §4` (documented as accepted limitation) |
| U10 | `gc=leaking` never reclaims memory; a long-lived user program with goroutine churn will grow monotonically | `user/target.json:7` | `userspace_gc_and_stacks.md §1` |

Items already in our favor:

- `sys_yield=7` and `sys_sleep=8` already exist (see the
  dispatch table in `src/userspace.go:69-97` and handler
  bodies at `src/userspace.go:328-344`). No new kernel
  syscall needed.
- `scripts/tinygo_runtime.patch` already handles the
  install-new-files mechanism; extending it for two
  more files is straightforward.
- Per-process PML4 isolation (Phase 4, shell-IO)
  guarantees each user's goroutine stacks stay private.

## 3. The five documents

| File | Lines | Scope |
|---|---|---|
| `userspace_goroutines_overview.md` (this file) | ~200 | inventory, DAG, phasing, decisions, risk delta |
| `userspace_tinygo_runtime.md` | ~300 | target.json diff, `runtime_gooos_user.go` / `interrupt_gooos_user.go`, `rt0.S` stubs, patch extension |
| `userspace_scheduler_integration.md` | ~200 | `sys_yield`/`sys_sleep` plumbing, deadlock, blocking-syscall pattern |
| `userspace_gc_and_stacks.md` | ~200 | GC choice, stack sizing, `gooosStackOverflow` userspace impl |
| `userspace_verification.md` | ~150 | `goprobe` ELF, harness, regression matrix, size audit |

Every doc cites `file:line`, carries explicit
`Dependencies` / `Verification` / `Open questions`
subsections, and ends with a `Risk register delta`. The
build-tag strategy is described **exactly once** in
`userspace_tinygo_runtime.md §2`; siblings cite by
reference.

## 4. Dependency DAG

```
userspace_tinygo_runtime  (foundation — everything else needs it)
   ├──► userspace_scheduler_integration
   ├──► userspace_gc_and_stacks
   └──► userspace_verification
```

- `userspace_tinygo_runtime.md` is the root: target.json +
  new runtime files + rt0.S stubs + patch extension.
- Scheduler integration (`sys_yield` / `sys_sleep`
  wiring) depends on the runtime files existing.
- GC + stacks depends on the runtime files and dovetails
  with the scheduler doc (canary check in `Pause`).
- Verification depends on all three.

## 5. Recommended implementation phasing

Single landing phase — the scheduler surface is too
entangled to split:

1. `user/target.json` → `scheduler=tasks`,
   `default-stack-size=8192`, `automatic-stack-size=true`,
   build-tags `["gooos", "baremetal"]`. Kernel
   target.json gets an additional `kernelspace` tag (see
   decision 1 below).
2. Two new TinyGo-tree files installed via the patch:
   `runtime_gooos_user.go`,
   `interrupt_gooos_user.go`.
3. Existing `runtime_gooos.go` / `interrupt_gooos.go`
   build tags tightened to require `kernelspace`.
4. `user/rt0.S` gains `gooosOnResume` + `gooosStackOverflow`
   stubs (or an equivalent Go file under `user/gooos/`
   linking to them).
5. `make embed-user` rebuilds every user ELF against the
   new runtime.
6. `user/cmd/goprobe/main.go` + `tmp/test_goprobe.sh`
   prove the feature end-to-end.

Single commit per step, per the prompt's git policy (when
implementation runs; **this round ships no commits**).

## 6. Decisions resolved before design

| # | Decision | Resolution |
|---|---|---|
| D1 | Build-tag split between kernel and userspace TinyGo-runtime files | **Add a new `kernelspace` tag to kernel `src/target.json`**. Existing `runtime_gooos.go` / `interrupt_gooos.go` move to `gooos && baremetal && kernelspace`. New `runtime_gooos_user.go` / `interrupt_gooos_user.go` use `gooos && baremetal && !kernelspace`. User target gets `build-tags=["gooos","baremetal"]` which excludes `runtime_unix.go` cleanly. Detailed in `userspace_tinygo_runtime.md §2`. |
| D2 | Introduce `sys_ticks` syscall for userspace `runtime.ticks()`? | **No**. Maintain a userspace-side Go counter incremented by each `sys_sleep` return. Coarse 10-ms resolution is fine for `time.Sleep`; no wall clock. |
| D3 | User goroutine default stack size | **8 KiB** (matches kernel). Re-audit via a stack-audit probe after landing. |
| D4 | `gooosOnResume` body in userspace | **Trivial `//go:nosplit` Go no-op**. There's no TSS for user code (we're already Ring-3); the symbol exists only to satisfy the patched `internal/task/task_stack_amd64.go:resume()` call. |
| D5 | New userland API surface in `user/gooos/` | **None**. Transparency is the point: user code writes `go func()` / `chan` / `select` directly. |

## 7. Out of scope

Explicitly deferred to future rounds:

- Preemptive scheduling / time-slicing inside a user
  process.
- Non-blocking syscalls (`sys_poll`, async I/O).
- Real OS threads (there's one kernel goroutine per user
  process, always; multi-thread per user process would
  need cross-CPU support — see
  `impldoc/deferred_smp_v2.md`).
- `runtime.LockOSThread`, `syscall.Rusage`, etc.
- Changes to the kernel scheduler, GC, or SMP design.
- A TinyGo runtime fork. The design stays inside the
  existing in-place patch flow.

## 8. Risk register delta summary

**Retires (when this round's implementation lands):**

- `R-userspace-no-goroutines` — resolved by runtime
  foundation.
- `R-userspace-no-chan` — resolved transparently through
  the same foundation.

**Adds:**

- `R-userspace-blocking-syscall-freeze` — if any user
  goroutine issues a syscall that parks in kernel, all
  goroutines in that process freeze. Documented in
  `userspace_scheduler_integration.md §4`; accepted
  (mitigation: dedicated blocking-I/O goroutine at the
  pipeline tail).
- `R-kernelspace-tag-miss` — if `kernelspace` is ever
  forgotten from kernel `src/target.json`, the
  userspace runtime files load into the kernel build and
  break it. Mitigated by `make verify-globals`-style
  guard; see `userspace_tinygo_runtime.md §6`.
- `R-userspace-elf-size-creep` — the TinyGo scheduler
  surface inflates user ELFs. Current cap is
  `maxFileData = 40960` (40 KiB, `src/fs.go:12`); at
  head `sh.elf` is ~37 KiB, so headroom is under 3 KiB.
  `userspace_verification.md §5` mandates bumping
  `maxFileData` to **96 KiB** as part of this round
  (FS memory: 32 × 96 KiB = 3 MiB, well within heap)
  and tracking a future doubling to 128 KiB if growth
  exceeds forecast.
- `R-user-gc-leak` — user heap growth if the GC choice
  stays `leaking`. Documented in
  `userspace_gc_and_stacks.md §1`.

## 9. Open questions (overview level)

1. **Does a `gc=leaking` user heap survive long-running
   goroutine workloads?** Small test apps (shell,
   cat, wc) exit quickly so leaks don't matter; a
   long-lived pipeline orchestrator would eventually
   exhaust `sys_sbrk`. Recommended default: keep
   `leaking` for now; switch to `conservative` when a
   user program's runtime profile demands it. See
   `userspace_gc_and_stacks.md §1`.
2. **Should `automatic-stack-size` be enabled?** Matches
   the kernel. Pros: per-goroutine sizing; cons:
   slightly larger binary (TinyGo emits stack-size
   metadata). Recommended: yes, enable.
3. **Post-landing audit cadence.** After the first
   goroutine-using user program ships, run the stack
   audit probe to confirm 8 KiB default suffices.
   Covered by `userspace_verification.md §3`.
