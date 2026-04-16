# Userspace GC and Goroutine Stacks

This document specifies the garbage-collector choice, per-
goroutine stack sizing, and the userspace implementation of
`gooosStackOverflow` — the hook that fires when a user
goroutine's canary check detects stack corruption.

Blockers U3, U8, U10 from
`userspace_goroutines_overview.md §2` are resolved here.
Depends on `userspace_tinygo_runtime.md` (build-tag split and
the new runtime file that hosts the GC-related primitives).

## 1. GC choice for user binaries

### 1.1 Current state

`user/target.json:7` sets `"gc": "leaking"` — TinyGo's
no-free-ever allocator. Every allocation grows `_heap_start`
forever via `sys_sbrk`. No mark/sweep, no reclamation.

### 1.2 Options

| Option | Pros | Cons |
|---|---|---|
| **Keep `leaking`** | Zero overhead, simplest possible. User programs today don't leak meaningfully — they exit quickly. | A long-running user program with goroutine churn (each `go func()` allocates a stack) runs out of heap. |
| **Switch to `conservative`** | Same GC the kernel uses. Reclaims dead allocations including goroutine stacks. | Every pointer store in user Go code gains no-op overhead (conservative scans, no write barriers — so the overhead is scan-time not runtime). User heap must be large enough for the GC's free-block metadata. |

### 1.3 Recommendation

**Keep `leaking` in v1.** User programs shipped today
(`sh.elf`, `cat.elf`, `wc.elf`, `ls.elf`, `hello.elf`,
`fdprobe.elf`) all exit quickly. The new `goprobe.elf` used
for verification will also exit. The `leaking` heap never
reclaims but never has time to exhaust its `sys_sbrk` budget
(1 MiB cap per `user/rt0.S:mmap` stub) either.

Switching to `conservative` is a one-line target.json change
any future session can flip when a long-running user
program appears. The switch has no kernel-side dependency;
it's purely a userspace decision. Recording as:

- **v1 default**: `leaking`.
- **Escalation condition**: any user program whose
  runtime is bounded by `sys_sbrk` exhaustion.

### 1.4 Heap layout under `leaking`

- `_heap_start` at the end of `.bss` (per
  `user/linker_user.ld:34-35`).
- `sys_sbrk` grows via kernel-side `HeapBreak` in the
  `Process` struct (`src/process.go:34`).
- Maximum heap: whatever `mmap`'s 1 MiB cap
  (`user/rt0.S:110-113`) allows per single request; TinyGo
  halves until `mmap` succeeds. Real ceiling is the
  process's per-process PML4 user-address space — each
  user process has its own PML4 allocated in `elfSpawn`
  (`src/process.go`); see `src/process.go` for the
  per-process `PML4` field and its mapping in
  `src/elf.go`.

## 2. Per-goroutine stack sizing

### 2.1 `default-stack-size`

Kernel uses 8192 bytes (`src/target.json:10`). User today
uses 4096 bytes (`user/target.json:12`) — too small once
`scheduler=tasks` adds goroutine-context-switch machinery.

**Recommendation**: bump to **8192 bytes** to match the
kernel. Per `impldoc/deferred_gc_and_stacks.md §4.5`, kernel
goroutines use 3–7% of 8 KiB in practice; user programs
should be similar or smaller.

### 2.2 `automatic-stack-size`

`user/target.json` currently omits this field (defaults to
`false`). The kernel uses `true` (`src/target.json:11`), so
TinyGo's compile-time call-graph analysis sizes each
goroutine's stack individually and `default-stack-size`
only kicks in when the estimator can't decide (e.g.,
through-interface calls).

**Recommendation**: set `automatic-stack-size: true` for
user builds too. One-line target.json addition (see
`userspace_tinygo_runtime.md §2.2` for the full diff).

### 2.3 Canary mechanism

TinyGo places a random canary at the bottom of each
goroutine's stack (`~/.local/tinygo/src/internal/task/
task_stack.go:16` const `stackCanary`). On every
`task.Pause()`, the canary is checked
(`task_stack.go:53-62`). If corrupted, `gooosStackOverflow`
fires.

This mechanism is **already shared** between kernel and
userspace via the patched `internal/task/task_stack.go`
(gated only on `scheduler.tasks`). Once userspace enables
tasks, the canary check runs automatically in every user
goroutine's Pause.

## 3. `gooosStackOverflow` in userspace

### 3.1 Kernel behavior (unchanged)

In the kernel, `gooosStackOverflow` is
`src/panic.go:gooosStackOverflow` which formats
`panicHexBuf` and prints via `serialPrintBytes` + `hlt` in
a loop. That's Ring-0 specific — Ring-3 code can't use
`serialPutChar` (no I/O port access) and can't `hlt`
(privileged).

### 3.2 Userspace implementation

Per `userspace_tinygo_runtime.md §5` (Go variant),
userspace provides a `gooosStackOverflow` stub that:

1. Writes a fixed message to serial (fd=1) via
   `sys_write`. gooos's kernel `sysWriteHandler`
   (`src/userspace.go:110-124`) routes fd=0 to VGA+serial
   and any other fd to serial only; there is no dedicated
   stderr channel, and the overflow path avoids VGA MMIO
   to stay safe under a corrupted stack.
2. Calls `sys_exit(1)` to terminate the process.

Code:

```go
// user/gooos/runtime_hooks.go (new file)
package gooos

import "unsafe"

// Kernel sys_write ABI (src/userspace.go:110-113):
//   RAX=1 (nr), RDI=buf, RSI=len, RDX=fd.
// user/gooos/io.go:11 already calls syscall3 in
// (buf, len, fd) order; match that here. fd=1 is
// serial-only (fd=0 writes to VGA + serial but requires
// Ring-0 VGA MMIO we do not touch from the overflow path).
//go:linkname gooosStackOverflow runtime.gooosStackOverflow
//go:nosplit
func gooosStackOverflow(t uintptr) {
    msg := "gooos: user goroutine stack overflow\n"
    p := unsafe.Pointer(unsafe.StringData(msg))
    syscall3(sysWrite, uintptr(p), uintptr(len(msg)), 1)
    syscall1(sysExit, 1)
    // sys_exit doesn't return, but defensively:
    for {
    }
}
```

**Why `//go:nosplit`?** The canary corruption scenario
means the goroutine's stack is already in trouble. We
cannot afford a stack-growth check that might itself
recurse. Matches the kernel-side nosplit on its
equivalent in `src/panic.go`.

**Why no per-task diagnostic (task pointer, stackTop,
canaryPtr)?** User programs have no safe way to print
hex-formatted pointers without allocations — Go's
`strconv.FormatUint` allocates. The kernel uses
`appendHex` + `panicHexBuf` (`src/panic.go`) to stay
allocation-free; replicating that in userspace is
possible but low value for v1 (the message alone is
enough to tell the user "your goroutine overflowed").
Future polish, not a v1 blocker.

### 3.3 `gooosOnResume` in userspace (cross-reference)

Per `userspace_tinygo_runtime.md §5`, `gooosOnResume` is
an inline no-op:

```go
//go:linkname gooosOnResume runtime.gooosOnResume
//go:nosplit
func gooosOnResume() {}
```

Userspace has no TSS to update — the CPU is already in
Ring 3. No CR3 swap either: per-process PML4 is set by
the kernel-side `gooosOnResume` when the kernel switches
TO the user process; within one process, all user
goroutines share the process's PML4 so no CR3 change is
needed between user-goroutine switches.

### 3.4 Interaction with per-process PML4

All user goroutines inside one process share the same
PML4 (allocated by `elfSpawn` in `src/process.go:201`).
Per-goroutine stacks allocated by TinyGo's
`internal/task/task_stack.go:initialize` come from the
user heap (sbrk-managed, mapped in the process's PML4).
Nothing escapes the process's address space.

Consequence: **a user goroutine's stack overflow cannot
corrupt another user process's memory** — PML4 isolation
is the guard. It can only corrupt other user goroutines
within the same process (which is the point of the canary
check).

## 4. Files referenced

- `user/target.json:7,11,12` — GC, scheduler,
  default-stack-size settings.
- `~/.local/tinygo/src/internal/task/task_stack.go:16`
  (canary const), `:48-49` (gooosStackOverflow linkname),
  `:53-67` (Pause check).
- `~/.local/tinygo/src/internal/task/task_stack.go:91-97`
  — `initialize` method that installs the canary.
- `src/panic.go:gooosStackOverflow` — kernel equivalent
  (unchanged).
- `impldoc/deferred_gc_and_stacks.md §4` — prior
  stack-audit results establishing the 75%-threshold
  convention.

## 5. Files to add / modify

| File | Change |
|---|---|
| `user/target.json` | (already covered in `userspace_tinygo_runtime.md §2.2`) `default-stack-size: 8192`, `automatic-stack-size: true` |
| `user/gooos/runtime_hooks.go` | **new** — `gooosOnResume`, `gooosStackOverflow` (covered in `userspace_tinygo_runtime.md §5`) |

No separate changes from what
`userspace_tinygo_runtime.md` prescribes.

## 6. Verification

1. `user/cmd/goprobe/main.go` spawns a goroutine that
   uses ~1 KiB of stack. Confirm canary unmodified
   (no overflow diagnostic on serial).
2. **Overflow trigger**: a follow-up test in
   `tmp/test_goprobe_overflow.sh` (optional) spawns a
   deliberately-recursive goroutine that consumes ~12 KiB
   of stack. Confirm serial shows
   `gooos: user goroutine stack overflow` and the
   process exits with code 1.
3. **Heap growth**: run the `goprobe.elf` with 1000
   `go func()` spawns (each allocating a few KiB). Under
   `gc=leaking`, observe `sys_sbrk` requests on serial
   and confirm they stay under 1 MiB. If they don't, flip
   to `gc=conservative`.
4. Stack audit probe (adapted from
   `impldoc/deferred_gc_and_stacks.md §4.2`): a special
   build with a `stackSizeAudit` pass over the user
   process's known-goroutine handles. Optional v1.

## 7. Dependencies

- `userspace_tinygo_runtime.md` for the runtime-hooks
  Go file placement and build-tag split.

## 8. Open questions

1. **Should the userspace `gooosStackOverflow` capture
   per-task detail (task pointer hex, stackTop hex)
   like the kernel does?** Low value for v1 — the fixed
   message is enough to diagnose. Future polish: port
   `src/panic.go`'s `panicHexBuf` + `appendHex` +
   `appendStr` into the userland `user/gooos/`
   package. Not in scope.
2. **Do we need `tinygo_scanCurrentStack` symbol in
   userspace?** Only if `gc=conservative` is ever
   enabled. The kernel's asm stub
   (`src/stubs.S:tinygo_scanCurrentStack`) walks the
   current stack for the GC mark phase. For v1
   `gc=leaking` it's not called; defer until a user
   program switches to `conservative`.
3. **Automatic-stack-size audit**. After shipping,
   enable a runtime audit that logs each goroutine's
   high-water mark once. Target: no goroutine exceeds
   75% of its allocated stack. If one does, bump
   `default-stack-size` or refactor the offending
   function.

## 8.5 Reviewer follow-ups (MINOR, left as-is)

- **automatic-stack-size × gc=leaking interaction**:
  enabling `automatic-stack-size=true` under `gc=leaking`
  means every `go func()` stack is permanent sbrk growth;
  the 1 MiB mmap cap in `user/rt0.S:112` gives a hard
  ceiling of ~128 goroutines at 8 KiB each. The
  verification probe (§6 item 3, 1000 spawns) must
  therefore be capped in practice or switched to
  `gc=conservative` before running. Recorded here
  because the probe is optional; v1 `goprobe.elf` only
  spawns a handful of goroutines.
- **`runtime.alloc` stack allocation chain**: patched
  `internal/task/task_stack.go:120` allocates goroutine
  stacks via `runtime_alloc`, which under `gc=leaking`
  routes to the bump allocator → `growHeap` → `mmap` (=
  `sys_sbrk` in our `user/rt0.S` stub). Listed for
  completeness; no action required.

## 9. Risk register delta

- **Retires**: `R-userspace-no-canary-hook` (now that
  `gooosStackOverflow` has a userspace-safe impl that
  prints to the serial console via `sys_write(fd=1)`
  and cleanly exits via `sys_exit(1)`).
- **Adds**: `R-user-gc-leak` — `gc=leaking` accumulates
  indefinitely; mitigation is a switch to
  `conservative` once user programs run long enough
  to matter.
- **Adds**: `R-user-stack-overflow-silent-history` —
  the fixed overflow message doesn't include the
  task's own stack-pointer or function-start detail,
  so debugging user overflows requires recompiling
  with audits enabled. Acceptable for v1.
