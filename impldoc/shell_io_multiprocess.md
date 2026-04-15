# Shell IO — Multi-Process Execution

This document specifies the per-process address-space refactor
that lets two or more Ring-3 processes run concurrently on
gooos. It is the heaviest piece of the shell-IO design set —
the main payoff is that concurrent pipes become possible.

Resolves blockers **B1** (synchronous `elfExec`), **B2**
(`savedParent` global), **B3** (single PML4), **B7** (stdin
contention) from `shell_io_overview.md §1`. Depends on
`shell_io_fd_table.md` for the descriptor model that the
syscalls return.

Out of scope (per `shell_io_overview.md §7`): job control
(`&`, `jobs`, `fg`, `bg`), signals, process groups. The
foreground/background model here is "most recently spawned,
non-pipe-driven process owns the keyboard" — minimal but
sufficient for shell-driven pipes.

## 1. Problem statement

The kernel today runs exactly one Ring-3 process at a time.
Concrete evidence:

- `elfExec` (`src/process.go:138`) blocks on
  `<-child.exitCh` at line 222. A shell calling `sys_exec`
  cannot continue until the child has exited.
- `savedParent` (`src/process.go:55`) is a single global
  `SavedMapping`. Per the comment at `src/process.go:8-10`:
  "Single-CPU v1 invariant: only one exec is in flight at
  a time."
- `sysExecHandler` (`src/userspace.go:199-204`) explicitly
  rejects nested exec ("Reject nested exec: only one level
  of exec nesting is supported").
- `mapPage` / `unmapPage` (`src/vm.go:156`, `:176`) walk the
  PML4 read from CR3 (line 157, 177). There is one CR3 in
  the kernel — one PML4 — shared across all execs. Two
  processes mapping the same vaddr (`userStackBase =
  0x7FFF0000` at `src/process.go:63`,
  `argPageVaddr = 0x40300000` at `src/process.go:60`, plus
  PT_LOAD vaddr `0x40100000` from `user/linker_user.ld`)
  would corrupt each other.

These invariants are the single biggest blocker to
concurrent pipes, background jobs, or any shell composition
that runs multiple programs simultaneously.

## 2. Approach: per-process PML4 + CR3 swap on resume

### 2.1 Decision

Each `Process` owns its own PML4. The kernel's 0..1 GiB
identity-mapped region is **shared by reference** across
every PML4 — every process's PML4 page has its own first
entry, but that entry points at the same kernel PDP
physical page. CR3 is swapped on every goroutine resume
to a Ring-3 task, piggy-backed on the existing
`gooosOnResume` hook.

User binaries already link at `0x40100000`
(`user/linker_user.ld:12`), well above the kernel's
identity-mapped 0..0x3FFFFFFF range. **No relocation,
no PIE, no per-binary link-address munging needed** —
each process gets its own PT entries for the
`0x40100000+` and `0x7FFF0000+` regions; the kernel's
identity map (covering 0..1 GiB) is untouched.

### 2.2 Why not vaddr-sliding (rejected)

The alternative considered was: keep one PML4, give each
process a non-overlapping vaddr range. Stack at
`0x7FFF0000 - N * 0x10000` for process N, etc.

**Rejected because:** every user ELF in `user/cmd/*` is
linked at the same `0x40100000` PT_LOAD vaddr
(`user/linker_user.ld:12`). Two simultaneous processes
would map their `.text` segments to overlapping vaddrs
in a single PML4 — impossible. The only fix would be
re-linking each user binary at a unique address (not
viable: number of slots = number of distinct link
addresses) or making user binaries position-independent
(PIE) and adjusting the loader.

Confirmed empirically: `objdump -d user/build/hello.elf`
shows TinyGo emits 32-bit-immediate absolute references
(e.g., `mov $0x40100342, %edi`) for string-literal
addresses — NOT `%rip`-relative throughout. PIE-style
relocation would be required to remap user code to a
different vaddr. Per-process PML4 sidesteps the issue
entirely.

### 2.3 Per-process PML4 layout

Page-table levels recap (4-level, x86_64):

- PML4 entry covers 512 GiB of vaddr.
- PDP entry covers 1 GiB.
- PD entry covers 2 MiB (or maps a 2 MiB huge page).
- PT entry covers 4 KiB (or maps a 4 KiB page).

Boot PML4 today (`src/boot.S:71-95`):

```
PML4[0]  → PDP (one entry at PDP[0])
              └── PDP[0]  → PD (entries 0..511)
                              └── PD[i] = i*2MiB | HUGE | PRESENT | WRITE
                                  (covers 0..1 GiB identity)
PML4[1..511] = 0
```

Per-process PML4 layout:

```
PML4[0]  → per-process PDP (allocated fresh per Process)
              ├── PDP[0]   = SHARED — points at the boot PD physical page,
              │              so vaddr 0..1 GiB stays identity-mapped to
              │              the same physical pages the kernel runs on
              └── PDP[1+]  = per-process — fresh PDs/PTs for user vaddrs
                              (0x40100000 lives in PDP[1] which covers
                              1..2 GiB; 0x7FFF0000 lives in PDP[1] too;
                              future sbrk past 2 GiB allocates PDP[2+])
```

Each process's PML4 is created by:

1. Allocate a fresh page (`allocPage`) for the PML4
   table itself.
2. Allocate a fresh page for the per-process PDP under
   PML4[0].
3. Set `procPML4[0]` to point at the per-process PDP.
4. Copy `bootPDP[0]` into `procPDP[0]` by value (it's
   a pointer to the boot PD; both PDPs end up pointing
   at the same kernel PD physical page, sharing the
   identity map).
5. Leave `procPDP[1..511]` empty; user mappings populate
   them via the existing `walkOrCreate` machinery in
   `src/vm.go:205`.

A clear mental model: **PDP[0] is shared, everything
else is per-process.**

When a process exits:

1. Walk the per-process PDP[1..511]: for each present
   entry, free its PD, then walk that PD for present
   PT entries and free them too. Free the user
   physical pages (existing behavior, recorded in
   `Process.UserPaddrs`).
2. Free the per-process PDP page itself.
3. Free the PML4 page.
4. **Do NOT** touch `procPDP[0]` past clearing —
   that's the boot PD pointer; the boot PD lives
   forever.

### 2.4 User vaddr layout (UNCHANGED from today)

| What | Vaddr | Source |
|---|---|---|
| ELF PT_LOAD | `0x40100000+` | `user/linker_user.ld:12` |
| Arg page | `0x40300000` | `src/process.go:60` |
| User stack (top) | `0x7FFF0000 + 0x2000` | `src/process.go:63, 210` |
| sbrk heap | `lastPh.Vaddr + memsz` aligned up | `src/process.go:214` |

All user vaddrs are ≥ 1 GiB, outside the boot identity
map. No vaddr changes; no entry-point patching;
`child.EntryPoint = entry` (unchanged from today).

### 2.5 `writeCR3` helper

```asm
// src/stubs.S (added)
    .global writeCR3
writeCR3:
    movq    %rdi, %cr3       /* Go SysV: RDI = pml4 phys addr */
    ret
```

Justified because Go cannot emit `mov %cr3` without
inline asm, and TinyGo lacks Go's compiler intrinsics.
One line of assembly, called from `gooosOnResume`.

The corresponding Go declaration in
`src/goroutine_tss.go` (or a new `src/cr3.go`):

```go
//go:linkname writeCR3 writeCR3
//go:nosplit
func writeCR3(pml4 uintptr)
```

`//go:nosplit` because `gooosOnResume` is `//go:nosplit`
(`src/goroutine_tss.go:148`).

## 3. `elfExec` decomposition

Today `elfExec` (`src/process.go:138-234`) loads the
ELF, maps pages, spawns the goroutine, **and blocks**
on `<-child.exitCh` (line 222). Multi-process needs
the spawn half independent of the wait half:

```go
// New: src/process.go

// elfSpawn loads filename, allocates a fresh PML4, populates
// user mappings, spawns ring3Wrapper, and returns immediately.
// The returned *Process is registered in procByPID; the caller
// is responsible for processWait.
func elfSpawn(filename, args string, parent *Process) (*Process, fdErr) {
    data := fsSendRead(filename)
    if data == nil { return nil, fdErrBad }
    entry, phdrs, ok := elfParse(data)
    if !ok { return nil, fdErrBad }

    child := &Process{
        parent:  parent,
        exitCh:  make(chan uintptr, 1),
        poolIdx: -1,
        pid:     allocPID(),
    }
    child.pml4 = newProcPML4()  // §2.3: PML4 + per-proc PDP, PDP[0]=boot PD ref
    inheritFDs(child, parent)   // see shell_io_fd_table.md §6

    userFlags := uintptr(pagePresent | pageWrite | pageUser)

    // Map PT_LOADs into child.pml4 at the unchanged ELF vaddr.
    for i := range phdrs {
        ph := &phdrs[i]
        startPage := ph.Vaddr &^ (pageSize - 1)
        endAddr := ph.Vaddr + uintptr(ph.Memsz)
        for addr := startPage; addr < endAddr; addr += pageSize {
            paddr := allocPage()
            mapPageInto(child.pml4, addr, paddr, userFlags)
            processRecordPage(child, addr, paddr)
        }
        // Populate file contents — see §3.2 (always via paddr).
    }

    // Arg page, user stack — same pattern at unchanged vaddrs
    // (0x40300000, 0x7FFF0000+).

    child.EntryPoint = entry                          // unchanged
    child.StackTop = userStackBase + 2*pageSize       // unchanged

    procByPID[child.pid] = child
    procByTask[taskCurrent()] // (registered inside ring3Wrapper)
    go ring3Wrapper(child)
    return child, fdErrOK
}

// processWait blocks until proc exits and returns the exit code.
// Removes proc from procByPID on completion.
func processWait(proc *Process) (uintptr, fdErr) {
    code := <-proc.exitCh
    delete(procByPID, proc.pid)
    return code, fdErrOK
}

// elfExec is preserved as a thin spawn+wait wrapper for any
// caller (or syscall path) that wants synchronous semantics.
func elfExec(filename, args string, parent *Process) (uintptr, bool) {
    child, err := elfSpawn(filename, args, parent)
    if err != fdErrOK { return 0, false }
    code, _ := processWait(child)
    return code, true
}
```

### 3.1 `ring3Wrapper` change

`ring3Wrapper` (`src/process.go:117`) gains a CR3 swap
right after `tssSetRSP0ForCurrentG` and before
`jumpToRing3`:

```go
func ring3Wrapper(proc *Process) {
    ring3WrapperHandle = taskCurrent()
    idx, kernelStackTop := ring3StackAcquire()
    proc.poolIdx = idx
    setCurrentProc(proc)
    registerRing3GWithStack(kernelStackTop, proc) // see §3.3
    tssSetRSP0ForCurrentG()
    setGateDPL3(0x80)
    writeCR3(proc.pml4)  // NEW: switch to this proc's address space
    jumpToRing3(proc.EntryPoint, proc.StackTop)
}
```

### 3.2 Loading PT_LOAD bytes into the child's pages

**Hard rule**: kernel-side code that populates child
pages writes **only via the physical address** returned
by `allocPage`. The kernel never dereferences a vaddr
that is mapped only in the child's PML4 (the kernel's
own CR3 still points at the boot PML4, where those
vaddrs are unmapped).

`allocPage` returns physical addresses inside the
0..1 GiB range that the boot PML4 identity-maps, so
`*(*byte)(unsafe.Pointer(paddr))` from the kernel
hits the same physical bytes the child will see.

Loader code:

```go
for i := range phdrs {
    ph := &phdrs[i]
    for j := uint64(0); j < ph.Filesz; j++ {
        // Find the physical page backing ph.Vaddr+j in the child PML4.
        offset := ph.Vaddr + uintptr(j)
        paddr := walkAndGetPaddrIn(child.pml4, offset)
        // paddr is identity-mapped in the kernel half.
        *(*byte)(unsafe.Pointer(paddr)) = data[ph.Offset+j]
    }
}
```

The same rule applies to populating the arg page and
zeroing the user stack: walk the child's PML4 to get
the paddr, write through paddr.

`walkAndGetPaddrIn(pml4, vaddr)` is a new helper that
walks a specified PML4 instead of always reading CR3
(unlike the existing `walkAndGetPaddr` at
`src/vm.go:243`).

### 3.3 `gooosOnResume` extension (nosplit-safe)

Today `gooosOnResume` (`src/goroutine_tss.go:149-159`)
sets TSS.RSP0 for Ring-3 goroutines. It is `//go:nosplit`
(line 148) which means it must not allocate, must not
park, and must not call any function that could grow
the goroutine stack. **Map operations in TinyGo are
not nosplit-safe** (a hash collision can call into the
runtime allocator).

The hook adds CR3 swap, so we must avoid a second map
lookup. Solution: extend the existing `gInfo` side
table (`src/goroutine_tss.go:27`) to cache `*Process`
alongside `stackTop`:

```go
// src/goroutine_tss.go (modified)

type gInfo struct {
    stackTop uintptr
    proc     *Process // NEW — cached at register time, read in nosplit hook
}

// registerRing3GWithStack now takes the *Process so the
// hook never needs to consult procByTask.
func registerRing3GWithStack(stackTop uintptr, proc *Process) {
    t := taskCurrent()
    if t == 0 { return }
    gInfoByTask[t] = &gInfo{stackTop: stackTop, proc: proc}
}

//go:nosplit
func gooosOnResume() {
    t := taskCurrent()
    if t == 0 { return }
    gi := gInfoByTask[t]      // single map lookup, same as today
    if gi == nil { return }   // not a Ring-3 goroutine; skip
    tssSetRSP0(gi.stackTop)
    if gi.proc != nil && gi.proc.pml4 != 0 {
        writeCR3(gi.proc.pml4) // pure pointer load + asm; nosplit-safe
    }
}
```

**No new map lookup** in the hot path — `gi.proc` is a
plain pointer field on a struct already retrieved from
the existing `gInfoByTask[t]` lookup. Map access is no
worse than today's hook.

CR3 write auto-flushes the TLB (no need to call
`invlpg` manually). One-time cost per goroutine resume:
~100 cycles + first-touch page-walk re-fills. Acceptable.

**First-resume race**: `ring3Wrapper` runs
`registerRing3GWithStack(...)` before its first
voluntary yield, but `gooosOnResume` fires on EVERY
resume including the very first one (when the
scheduler dispatches the goroutine). On that first
fire, `gInfoByTask[t]` may not yet contain `t`'s
entry — the `gi == nil` short-circuit skips both
TSS.RSP0 and CR3, leaving the kernel's boot PML4
active. This is safe because `ring3Wrapper`'s
prologue (up to `writeCR3` itself) only touches
kernel-half memory. After `writeCR3` runs explicitly
inside the wrapper, every subsequent resume sees the
populated `gi` and swaps correctly.

Document this invariant in `src/goroutine_tss.go` as
a comment above `gooosOnResume`.

## 4. `savedParent` removal

The `savedParent` global (`src/process.go:54`) and the
save / restore dance in `elfExec` /
`processExit` (`src/process.go:152-156`,
`:259-268`) exist solely because parent and child
shared the same PML4 — to switch from one to the other
the kernel had to remap pages.

With per-process PML4, there is no shared address
space; each process's pages are isolated in its own
PML4. The save / restore code disappears entirely:

- `elfExec` no longer touches the parent's mappings
  (just creates the child's PML4).
- `processExit` no longer restores anything; it just
  frees the child's user pages and PML4.

The `parent *Process` field on `Process` stays — used
for orphan reparenting and for the foreground stdin
model (§7).

## 5. New syscalls

### 5.1 `sys_spawn` — non-blocking process creation

```
sys_spawn(name_ptr, name_len, args_ptr, args_len) → pid | -fdErr
```

Syscall number 15 (see canonical table in
`shell_io_fd_table.md §5.1`).

```go
func sysSpawnHandler(frame *SyscallFrame) {
    parent := currentProc()
    if parent == nil { frame.RAX = sysFail(fdErrBad); return }
    name := readUserString(frame.RDI, frame.RSI)
    args := readUserString(frame.RDX, frame.R10)
    child, err := elfSpawn(name, args, parent)
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    frame.RAX = uintptr(child.pid)
}
```

### 5.2 `sys_wait` — block on a child's exit

```
sys_wait(pid) → exit_code | -fdErr
```

Syscall number 16 (see canonical table in
`shell_io_fd_table.md §5.1`).

```go
func sysWaitHandler(frame *SyscallFrame) {
    pid := uint32(frame.RDI)
    parent := currentProc()
    if parent == nil { frame.RAX = sysFail(fdErrBad); return }
    child := procByPID[pid]
    if child == nil || child.parent != parent {
        frame.RAX = sysFail(fdErrBad); return
    }
    code, _ := processWait(child)
    frame.RAX = code
}
```

A `pid == 0` argument could mean "wait for any child"
in a future iteration; not supported in v1. Document.

### 5.3 PID allocation

```go
// src/process.go (new):
var nextPID uint32 = 1

func allocPID() uint32 {
    pid := nextPID
    nextPID++
    return pid
}

var procByPID = make(map[uint32]*Process)
```

Monotonic counter; wraps at 2^32 (4 billion procs)
which is irrelevant for shell workloads. Reserve
PID 0 as "invalid".

## 6. Concurrency bound: `ring3StackPool`

`ring3StackPool` has 32 slots
(`src/ring3_pool.go:21` `const maxRing3Procs = 32`).
Each spawned process consumes one slot via
`ring3StackAcquire` in `ring3Wrapper`.

Behavior on the 33rd `sys_spawn`:

- `ring3StackAcquire` reads from the pool's free-slot
  channel (`ring3StackPoolCh`); the channel is empty
  because all 32 slots are in use.
- The shell goroutine that called `sys_spawn` blocks
  inside the channel receive until a slot frees.
- This is acceptable for v1: shell hangs until a
  pipeline stage finishes. No deadlock because at
  least one pipeline stage will eventually exit and
  release its slot.

Documented in `shell_io_overview.md §8` as
`R-mp-pool-cap`. Mitigation if it becomes a problem:
bump `maxRing3Procs` (memory cost: 8 KiB × 32 = 256
KiB → bumping to 64 doubles to 512 KiB; well within
the 4 MiB heap).

## 7. Stdin contention: foreground model

When two processes have fd 0 backed by the same
`consoleStdin` (the default after fd inheritance),
two readers race on `<-keyboardCh`. Whoever Goes first
gets the byte. Unpredictable.

**Foreground model:** the shell tracks
`currentForeground *Process`. Only the foreground
process's `consoleStdin.Read` actually consumes from
`keyboardCh`; non-foreground readers see EOF.

```go
// src/fd.go (consoleStdin extended):

var foregroundProc *Process // set by shell

type consoleStdin struct{}

func (consoleStdin) Read(buf []byte) (int, fdErr) {
    if currentProc() != foregroundProc {
        return 0, fdErrEOF
    }
    return readKeyboardLine(buf), fdErrOK
}
```

The shell sets `foregroundProc` to:
- itself when at the prompt;
- the spawned child when running a foreground
  command;
- the **tail** of a pipeline when running pipes
  (because that's the stage closest to the user).

For pipe stages other than the tail, fd 0 is dup2'd
to a pipe read end (not `consoleStdin`), so the
foreground check doesn't apply to them.

Trade-offs documented in
`shell_io_overview.md §8 R-foreground-stdin-policy`.

## 8. Process lifecycle: zombies and orphans

### 8.1 Zombies

If a child exits but its parent never calls
`sys_wait`, the child's `Process` struct stays in
`procByPID` (so a future `sys_wait` can find it).
This is a small leak: ~1 KiB per unwaited child.

For v1 we accept it — the shell consistently waits.
A future `sys_wait(-1)` (wait for any child) would
let a busy parent reap zombies opportunistically.
Listed in `shell_io_overview.md §7`.

### 8.2 Orphans

If a parent exits while its child is still running,
the child becomes orphaned. Real Unix reparents to
PID 1 (init); gooos's "PID 1" is the boot shell.

For v1: the boot shell's idle loop (after the shell
ELF call returns, in `src/elf.go:237`) is
`for { hlt() }`, so the shell process never exits.
Orphans can therefore only happen if a non-shell
parent dies first — which doesn't happen in the only
spawn path we have (shell spawns children). Document;
revisit when nested shells become possible.

## 9. Pipe interaction (cross-reference)

The shell builds a pipeline as described in
`shell_io_pipes.md §3.4`. The exact `sys_spawn` /
`sys_wait` calls:

```go
// "cmd1 | cmd2":
fds := Pipe()                 // [readFd, writeFd]
saveStdout()
Dup2(fds[1], Stdout); Close(fds[1])
pid1 := Spawn("cmd1.elf", a1)
restoreStdout()

saveStdin()
Dup2(fds[0], Stdin); Close(fds[0])
pid2 := Spawn("cmd2.elf", a2)
restoreStdin()

// At this point both children run concurrently. Shell
// holds neither pipe end. cmd1 writes to its fd 1
// (= write end); cmd2 reads from its fd 0 (= read end).
// When cmd1 exits, processExit closes its fd 1 →
// pipe reader sees EOF on next read.

Wait(pid2)   // shell blocks on the tail of the pipeline
// pid1 may have already exited; its Process is a zombie
// until reaped. For v1 we leak (acceptable per §8.1).
```

## 10. Userland API surface (`user/gooos/proc.go`)

```go
// user/gooos/proc.go (extended):

func Spawn(name string, args string) (pid int, errno int)
func Wait(pid int) (exitCode int, errno int)

// Existing Exec stays as a synchronous spawn+wait wrapper:
func Exec(name string, args string) int {
    pid, err := Spawn(name, args)
    if err != 0 { return -1 }
    code, _ := Wait(pid)
    return code
}
```

## 11. Files to add / modify

| File | Change |
|---|---|
| `src/stubs.S` | add `writeCR3` (3 lines) |
| `src/cr3.go` | **new (small)** — `//go:linkname writeCR3` declaration |
| `src/vm.go` | add `mapPageInto(pml4, vaddr, paddr, flags)`, `unmapPageFrom(pml4, vaddr)`, `walkAndGetPaddrIn(pml4, vaddr)` — variants of existing helpers that take an explicit PML4 arg instead of reading CR3 |
| `src/process.go` | add `Process.pml4`, `Process.pid`; add `procByPID`, `nextPID`, `allocPID`; add `newProcPML4()`, `freeProcPML4()`; rewrite `elfExec` as `elfSpawn + processWait`; remove `savedParent` global; CR3 swap in `ring3Wrapper`; package-scope `foregroundProc *Process` + `setForegroundProc`/`getForegroundProc` accessors |
| `src/elf.go` | route the loader through `mapPageInto(child.pml4, …)` at the ELF's linked vaddr (`0x40100000+`, unchanged); populate page contents by writing through the `paddr` returned by `allocPage` (per the §3.2 hard rule) — never via the child-only vaddr |
| `src/goroutine_tss.go` | add `proc *Process` field to `gInfo` (line 27); change `registerRing3GWithStack` to take `*Process`; extend `gooosOnResume` to call `writeCR3(gi.proc.pml4)` via the cached pointer |
| `src/userspace.go` | add `sysSpawn` (15) and `sysWait` (16) constants + handlers; the existing `sysExec` calls `elfExec` which is now a thin wrapper |
| `src/fd.go` | `consoleStdin.Read` consults `getForegroundProc()` from `src/process.go`; returns `(0, fdErrEOF)` when caller is not foreground |
| `src/main.go` | nothing (process state is per-`Process`, not global) |
| `user/gooos/syscall.go` | add `sysSpawn` / `sysWait` constants |
| `user/gooos/proc.go` | add `Spawn` / `Wait`; rewrite `Exec` as spawn+wait |
| `user/cmd/sh/main.go` | use `Spawn` + `Wait` for pipelines; foreground tracking |

## 12. Verification

1. `make build` clean.
2. **Smoke (single-process regression)**: 10/10
   `bash tmp/test_sendkey.sh` green. The `Exec` wrapper
   preserves synchronous semantics so existing user
   binaries are unaffected.
3. **Spawn smoke**: shell command "spawn hello;
   spawn hello" — both `hello.elf` instances run
   concurrently, both produce output on serial. Output
   may interleave (no atomic-line guarantee).
4. **PID allocation**: 50 sequential spawns produce
   PIDs 1..50 (or 1..50 × pipeline depth) without
   collision.
5. **Address space isolation**: a deliberate test ELF
   that writes a sentinel byte at `0x800040000400`
   should not affect a concurrent process running the
   same code (each writes into its own physical page).
6. **Pipe round-trip** (depends on
   `shell_io_pipes.md` phase 5): `echo hello | cat`
   produces `hello`.
7. **Foreground model**: spawn a process that does
   `sys_read(Stdin)`, then before responding, spawn
   another. Type something. Confirm the second
   (foreground) process receives it; the first sees
   EOF on its read.
8. **`make run-smp` regression**: SMP-v1 still boots
   (APs idle). Multi-process is BSP-only in this
   round; SMP v2 (`deferred_smp_v2.md`) is unaffected.
9. **Stress**: 50 sequential `echo | cat | wc -c`
   pipelines from the shell; assert no heap growth
   (per-process PML4 cleanup in `processExit`
   plus `ring3StackPool` recycling keeps memory
   bounded).

## 13. Dependencies

- `shell_io_fd_table.md` — `Process.fds` field
  inherited from parent on spawn.
- `shell_io_pipes.md §3` — concurrent pipe variant
  needs multi-process; sequential variant works
  without it.
- `shell_io_overview.md §6 D1, D5` — design
  decisions confirmed.

## 14. Open questions

1. **PML4 page allocation pressure**. Each spawn
   allocates 1 PML4 + 1 per-process PDP + ~1 PD +
   ~K PT pages (K small). At 32 concurrent procs
   that's ~100 page-table pages (~400 KiB). Within
   the 4 MiB heap, but approaching 10% — track via
   the item-13 stack-audit pattern.
2. **Boot-shell PML4**. The boot shell's `Process` is
   allocated in `elfLoad` (`src/elf.go:190`); it
   needs a PML4 too. Same `newProcPML4()` path,
   called once before `ring3Wrapper` runs the shell.
3. **`procByTask` vs `procByPID` consistency**. Both
   maps must be updated in lockstep on spawn / exit.
   A small helper `(register|unregister)Proc(*Process)`
   should encapsulate both. Listed as a v1 hygiene
   item.
4. **Reaping zombies**. v1 leaks. A future
   `sys_wait(-1)` is the natural fix. Track in
   `shell_io_overview.md §7`.
5. **Note on TinyGo codegen** (was an open question;
   resolved). TinyGo emits 32-bit absolute immediates
   for string-literal addresses (verified via
   `objdump -d user/build/hello.elf`: e.g.,
   `mov $0x40100342, %edi`). This rules out
   relocating user binaries to a different vaddr
   without PIE. The per-process PML4 design (§2)
   sidesteps the issue by keeping user vaddrs at
   their link-time `0x40100000+` while giving each
   process its own PT entries — no relocation
   needed. Recorded here so a future revisit doesn't
   repeat the analysis.

## 15. Risk register delta

- **Retires**: `R-shell-no-multiprocess`,
  `R-savedparent-global`,
  `R-elfExec-synchronous`,
  `R-stdin-contention` (via foreground model).
- **Adds**:
  - `R-pml4-kernel-share-correctness` — every
    per-process PDP[0] entry must point at the same
    boot PD physical page (the kernel identity map
    for 0..1 GiB); divergence breaks ISR delivery
    (the ISR's RIP and stack are in the kernel half).
    Mitigated by funnelling all PML4 allocation
    through a single `newProcPML4()` helper that
    copies the boot PDP's first entry by value.
  - `R-cr3-swap-cost` — `mov %cr3` flushes the entire
    TLB on every goroutine resume to a Ring-3 task.
    Cost is small but measurable (one full page-walk
    per cold access). Documented as accepted; an
    INVPCID optimization is possible but out of scope.
  - `R-zombie-leak` — see open question 4.
