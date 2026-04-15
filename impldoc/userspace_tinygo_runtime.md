# Userspace TinyGo Runtime — Build Tags, Runtime Files, rt0.S Stubs

This document specifies the foundation that enables TinyGo's
`scheduler=tasks` inside Ring-3 user binaries on gooos. It
covers the build-tag split, the new runtime files we install
into the TinyGo tree, the userland-side symbol stubs, and how
`scripts/tinygo_runtime.patch` gets extended.

Blockers U1, U2, U4, U5, U6, U7 from
`userspace_goroutines_overview.md §2` are all resolved here.
Depends on nothing else (foundation); every sibling doc
references this one for the build-tag convention.

## 1. Goal

- User target `scheduler=tasks` compiles and links.
- Every symbol the tasks runtime references resolves to either
  a userspace-specific Go body (here) or an existing
  `user/rt0.S` stub.
- `runtime_unix.go` stays out of userspace's link graph so
  its libc-dependent sleepTicks / ticks don't collide with
  the gooos versions.
- Kernel build still works identically — the change is
  orthogonal.

## 2. Build-tag strategy

### 2.1 The three-axis problem

Today `scripts/tinygo_runtime.patch` installs two files gated
on `gooos && baremetal`:

- `~/.local/tinygo/src/runtime/runtime_gooos.go:1` —
  `//go:build gooos && baremetal`
- `~/.local/tinygo/src/runtime/interrupt/interrupt_gooos.go:1`
  — `//go:build gooos && baremetal`

Both files' bodies are **kernel-specific**: they reference
`main.pitTicks`, kernel asm stubs (`cli`, `sti`, `hlt`,
`outb`), and the boot-kernel's serial port. A userspace build
must not pull them in.

Meanwhile `runtime_unix.go` is gated:

```
//go:build (darwin || (linux && !baremetal && !wasip1 && !wasm_unknown && !wasip2)) && !nintendoswitch
```

i.e., it loads when `goos=linux` AND `!baremetal`. User
target today (`goos=linux`, no `baremetal` tag) loads it; its
libc bodies never actually execute because user programs only
exercise the narrow path `rt0.S` stubs cover.

Three build-tag axes:

- `gooos` — distinguishes gooos-aware builds from anything
  else.
- `baremetal` — excludes libc-dependent `runtime_unix.go`.
- **NEW**: `kernelspace` — distinguishes kernel (Ring-0) from
  userland (Ring-3) inside the `gooos && baremetal` set.

### 2.2 Target.json diffs

**Kernel** (`src/target.json:5`) — add `kernelspace`:

```diff
-  "build-tags": ["gooos", "baremetal"],
+  "build-tags": ["gooos", "baremetal", "kernelspace"],
```

**User** (`user/target.json`) — add `gooos` + `baremetal`,
switch scheduler/gc/stack:

```diff
 {
   "llvm-target": "x86_64-unknown-none-elf",
   "cpu": "x86-64",
   "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
+  "build-tags": ["gooos", "baremetal"],
   "goos": "linux",
   "goarch": "amd64",
-  "gc": "leaking",
-  "scheduler": "none",
+  "gc": "leaking",
+  "scheduler": "tasks",
   "panic-strategy": "trap",
   "linker": "ld.lld",
   "rtlib": "compiler-rt",
-  "default-stack-size": 4096
+  "default-stack-size": 8192,
+  "automatic-stack-size": true
 }
```

`gc=leaking` retained per `userspace_gc_and_stacks.md §1`.

### 2.3 Build-tag gates on runtime files

After the patch:

| File | Build tag |
|---|---|
| `runtime_gooos.go` (existing) | `gooos && baremetal && kernelspace` |
| `interrupt/interrupt_gooos.go` (existing) | `gooos && baremetal && kernelspace` |
| `runtime_gooos_user.go` (new) | `gooos && baremetal && !kernelspace` |
| `interrupt/interrupt_gooos_user.go` (new) | `gooos && baremetal && !kernelspace` |

Result matrix:

| Build | Tags | runtime_unix.go? | runtime_gooos.go? | runtime_gooos_user.go? |
|---|---|---|---|---|
| Kernel | `gooos baremetal kernelspace` | excluded (`!baremetal`) | loaded | excluded |
| User | `gooos baremetal` | excluded (`!baremetal`) | excluded | loaded |
| Standard linux | (none) | loaded | excluded | excluded |

`internal/task/task_stack.go` and `task_stack_amd64.go`
(both already patched, gated only on `scheduler.tasks`) load
in both kernel and user builds once `scheduler=tasks`, which
is the desired behavior.

## 3. New file: `runtime_gooos_user.go`

Path: `~/.local/tinygo/src/runtime/runtime_gooos_user.go`.
Replaces what `runtime_unix.go` would have provided under
`goos=linux && !baremetal`, without libc.

### 3.0 Entry-point sanity check

`user/rt0.S:17` does `call main`. Under TinyGo's
`scheduler=none` (today) `main` is the flat user
`main.main` wrapper. Under `scheduler=tasks`, TinyGo
emits a different `main` body: it initializes the task
runtime, seeds `main.main` as the first goroutine,
enters the scheduler loop, and only returns when all
goroutines are done. The exported linker symbol is
still `main` (TinyGo's runtime keeps the C-ABI entry
name stable across scheduler modes — the kernel relies
on the same invariant, which is why `_start` in our
kernel boot path also jumps through `main`).

**Verification during implementation**: after the patch
applies, run `nm user/build/hello.elf | grep '\bmain\b'`
and confirm a `T main` symbol exists. If it does not,
the `_start` stub in `user/rt0.S:17` must be adjusted
to call whatever TinyGo emits for `scheduler=tasks`
under our goos/arch combo. No redesign is required —
it's a symbol-name spot-fix in one `call` instruction.

### 3.1 Runtime bodies

```go
//go:build gooos && baremetal && !kernelspace

// gooos userspace runtime bodies. The kernel equivalent is
// runtime_gooos.go; this sibling serves Ring-3 user programs
// that link against user/rt0.S. The two files share a build-
// tag prefix (gooos && baremetal) but are disambiguated by
// the kernelspace tag on kernel target.json.
//
// Primitives here go through gooos syscalls exposed in
// user/rt0.S, not kernel asm stubs.

package runtime

import "unsafe"

type timeUnit int64

// Syscall numbers are fixed by src/userspace.go:
//   sys_exit=0, sys_write=1, sys_yield=7, sys_sleep=8
// (see the dispatch switch in sysCallDispatch at
// src/userspace.go:69-97). Existing userland constants live
// in user/gooos/syscall.go.
//
// Linkage pattern: the asm globals in user/rt0.S (syscall0,
// syscall1, syscall3) are resolved at link time. We declare
// bodyless Go functions with //go:linkname pointing at the
// asm symbol name — the same mechanism the kernel uses for
// jumpToRing3 (src/userspace.go:64-65).

//go:linkname userSyscall0 syscall0
func userSyscall0(nr uintptr) uintptr

//go:linkname userSyscall1 syscall1
func userSyscall1(nr, a1 uintptr) uintptr

//go:linkname userSyscall3 syscall3
func userSyscall3(nr, a1, a2, a3 uintptr) uintptr

// userTicks is a Go-side monotonic counter bumped whenever
// sleepTicks returns. Coarse (10 ms resolution) but
// monotonic. Good enough for time.Sleep; not a wall clock.
var userTicks uint64

// sleepTicks blocks this task on sys_sleep for d ticks, then
// bumps the local ticks counter. Called from the scheduler's
// idle path when all tasks are parked.
func sleepTicks(d timeUnit) {
    if d <= 0 {
        return
    }
    userSyscall1(8, uintptr(d)) // sys_sleep
    userTicks += uint64(d)
}

func ticks() timeUnit { return timeUnit(userTicks) }

// 100 Hz PIT tick = 10 ms = 10_000_000 ns (matches kernel).
func ticksToNanoseconds(t timeUnit) int64  { return int64(t) * 10_000_000 }
func nanosecondsToTicks(ns int64) timeUnit { return timeUnit(ns / 10_000_000) }

// putchar writes one byte via sys_write(buf, len, fd=1).
// Kernel sys_write ABI (src/userspace.go:110-113):
//   RDI=buf, RSI=len, RDX=fd.
// fd=1 is serial-only (fd=0 routes to VGA+serial but
// requires Ring-0 VGA MMIO).
// Used by runtime.printstring (panic messages).
func putchar(c byte) {
    b := c
    userSyscall3(1, uintptr(unsafe.Pointer(&b)), 1, 1)
}

// buffered: no line buffering.
func buffered() int { return 0 }

// getchar: not used; matches kernel side.
func getchar() byte { return 0 }

// exit routes to sys_exit. No halt loop — sys_exit does not
// return (processExit in kernel parks the ring3Wrapper).
func exit(code int) {
    userSyscall1(0, uintptr(code))
    for {
    }
}

func abort() { exit(1) }

// preinit / main: TinyGo's goos=linux runtime already emits
// a `main` entry that rt0.S calls (see §3.0 above and
// user/rt0.S:17). preinit is a no-op here; the tasks
// scheduler wrapper TinyGo emits for scheduler=tasks
// handles goroutine setup.
func preinit() {}

// The scheduler's idle path calls waitForEvents when
// sleepQueue / timerQueue are empty AND no task is
// runnable. In userspace that is a deadlock; TinyGo's
// built-in fallback in wait_other.go panics
// "all goroutines are asleep", which is the correct
// user-visible symptom. We rely on that path; no override
// required here.
```

Note on `putchar`: TinyGo's `printstring` (used by
runtime.panic) calls `putchar` byte-by-byte. `sys_write`
per byte is slow but only fires on panic paths. A buffered
variant is a future optimization.

## 4. New file: `interrupt_gooos_user.go`

Path:
`~/.local/tinygo/src/runtime/interrupt/interrupt_gooos_user.go`.

Trivial — userspace has no IRQ handlers, so `In()` is
unconditionally `false`, `Disable` / `Restore` are no-ops,
and `State` carries no information.

```go
//go:build gooos && baremetal && !kernelspace

package interrupt

type State uintptr

func Disable() State      { return 0 }
func Restore(state State) {}
func In() bool            { return false }
```

This file exists only so that code under the `gooos &&
baremetal` tag combo (e.g., scheduler.go's `addTimer`) finds
the `interrupt.Disable` / `Restore` symbols it expects. It
never runs meaningful work.

## 5. rt0.S stubs required

`user/rt0.S` gains two new globally-exported labels:

```asm
/* gooosOnResume — no-op hook called from internal/task.resume()
 * once scheduler=tasks is enabled. Kernel uses this to update
 * TSS.RSP0 per Ring-3 goroutine; userspace is already in
 * Ring 3 so nothing to do. */
    .global gooosOnResume
gooosOnResume:
    ret

/* gooosStackOverflow — called from internal/task.Pause() when
 * a goroutine's stack canary is corrupt. Prints a fixed
 * message via sys_write (fd=1, serial only — kernel only
 * distinguishes fd=0=VGA+serial vs other=serial) then
 * sys_exit(1). Userspace cannot recover; abort the whole
 * process. Kernel sys_write ABI (src/userspace.go:110-113):
 * RAX=1 (nr), RDI=buf, RSI=len, RDX=fd. */
    .global gooosStackOverflow
gooosStackOverflow:
    /* RDI on entry = task pointer (unused in v1). */
    leaq    .L_stack_msg(%rip), %rdi        /* buf */
    movq    $.L_stack_msg_end - .L_stack_msg, %rsi  /* len */
    movq    $1, %rdx                        /* fd = 1 (serial) */
    movq    $1, %rax                        /* sys_write */
    int     $0x80
    movq    $1, %rdi                        /* exit code */
    xorq    %rax, %rax                      /* sys_exit = 0 */
    int     $0x80
1:  jmp     1b

    .section .rodata
.L_stack_msg:
    .ascii  "gooos: user goroutine stack overflow\n"
.L_stack_msg_end:
    .section .text
```

Alternative: implement `gooosOnResume` and
`gooosStackOverflow` as Go functions in a new
`user/gooos/runtime_hooks.go` file. Two-line Go is cleaner
than assembly. Implementation choice deferred to the commit;
either approach satisfies the link dependency.

The Go alternative:

```go
// user/gooos/runtime_hooks.go
package gooos

//go:linkname gooosOnResume runtime.gooosOnResume
//go:nosplit
func gooosOnResume() {}

//go:linkname gooosStackOverflow runtime.gooosStackOverflow
//go:nosplit
func gooosStackOverflow(t uintptr) {
    // sys_write kernel ABI (src/userspace.go:110-113):
    // RDI=buf, RSI=len, RDX=fd. Existing user/gooos/io.go:11
    // uses the same (buf, len, fd) argument order on
    // syscall3. fd=1 routes to serial only; fd=0 is VGA
    // +serial, which requires Ring-0 VGA MMIO we don't
    // want from the overflow path.
    msg := []byte("gooos: user goroutine stack overflow\n")
    syscall3(sysWrite,
             uintptr(unsafe.Pointer(&msg[0])),
             uintptr(len(msg)),
             1)
    syscall1(sysExit, 1)
}
```

The Go version is recommended; the asm sketch above is
reference material for the edge case where linker won't
place a Go nosplit function at a resolvable address.

## 6. Extending `scripts/tinygo_runtime.patch`

Two additions:

1. Tighten existing file hunks' build tags by adding
   `&& kernelspace`:
   - `runtime_gooos.go:1`:
     `//go:build gooos && baremetal`
     → `//go:build gooos && baremetal && kernelspace`.
   - `interrupt_gooos.go:1`: same edit.
2. Add two new-file hunks for `runtime_gooos_user.go` and
   `interrupt_gooos_user.go`, gated
   `gooos && baremetal && !kernelspace`.

`scripts/patch_tinygo_runtime.sh`'s sentinel check
(`gooos-local runtime bodies`) still works — the new files
introduce a new sentinel line like
`gooos userspace runtime bodies`; the script should be
extended to check both sentinels (or collapse to one
directory-scan check). Implementation detail for the patch
commit.

## 7. Files to add / modify

| File | Change |
|---|---|
| `src/target.json` | add `"kernelspace"` to `build-tags` |
| `user/target.json` | `scheduler=tasks`, `default-stack-size=8192`, `automatic-stack-size=true`, `build-tags=["gooos","baremetal"]` |
| `~/.local/tinygo/src/runtime/runtime_gooos.go` | tighten build tag: add `&& kernelspace` |
| `~/.local/tinygo/src/runtime/interrupt/interrupt_gooos.go` | same tag tightening |
| `~/.local/tinygo/src/runtime/runtime_gooos_user.go` | **new** (§3) |
| `~/.local/tinygo/src/runtime/interrupt/interrupt_gooos_user.go` | **new** (§4) |
| `scripts/tinygo_runtime.patch` | extend with the four hunks above |
| `scripts/patch_tinygo_runtime.sh` | update sentinel logic if needed |
| `user/gooos/runtime_hooks.go` | **new** — `gooosOnResume`, `gooosStackOverflow` (Go version, preferred) |

## 8. Verification

1. `make build` clean after the patch reapplies. Kernel
   build uses `kernelspace` tag and picks up
   `runtime_gooos.go`; userspace build uses `gooos baremetal`
   and picks up `runtime_gooos_user.go`.
2. Kernel 10/10 `tmp/test_sendkey.sh` — confirms the tag
   tightening didn't accidentally exclude the kernel runtime
   file.
3. User build produces working ELFs: `hello.elf`, `cat.elf`,
   etc. continue to run (they don't use `go`/`chan` yet).
4. `nm user/build/hello.elf | grep -E "sleepTicks|ticks|gooos"`
   shows the expected userspace runtime symbols.
5. Patch re-apply test: `rm` the relevant file(s) under
   `~/.local/tinygo` + rerun
   `scripts/patch_tinygo_runtime.sh` — all four files land
   cleanly.

## 9. Dependencies

- None. This is the foundation. Every sibling doc in the
  set depends on this file.

## 10. Open questions

1. **Should `gooosOnResume` / `gooosStackOverflow` live in
   `user/gooos/` or in a new file inside the TinyGo tree?**
   User code is easier to edit (no patch surface growth).
   `user/gooos/runtime_hooks.go` with `//go:linkname`
   exports is the recommended path (§5 Go variant).
2. **Does adding `kernelspace` to kernel `build-tags` break
   anything that wasn't caught here?** Search `src/*.go` for
   any `//go:build … !kernelspace` or negative constraints
   that would be flipped. Unlikely (we own every `src/`
   file) but worth grepping before the patch commits.
3. **Can we drop the `baremetal` tag from the userspace
   target?** No — `runtime_unix.go`'s exclusion rule is
   `!baremetal` is what keeps it out. Dropping baremetal
   would re-enable `runtime_unix.go` and collide with our
   sleepTicks.
4. **Does any TinyGo runtime file already provide
   `gooosOnResume` or `gooosStackOverflow` symbols?** No —
   grepped. They are entirely gooos-introduced by the
   existing patch; userspace just needs its own definition
   with the same linkname target.

## 11. Risk register delta

- **Retires**: `R-userspace-no-runtime-overrides` (every
  TinyGo runtime symbol the tasks scheduler needs now has
  a userspace-specific body or a deliberate libc-free stub).
- **Adds**: `R-kernelspace-tag-miss` — forgetting
  `kernelspace` in `src/target.json` flips the kernel to
  the userspace runtime, which tries to issue `sys_exit`
  syscalls that can't work from Ring-0. Mitigation:
  `verify-globals`-style check that `_start` is defined in
  kernel.bin (trivially true today).
