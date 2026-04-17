# SMP v2 -- Kernel Data Audit

Full audit of every global mutable variable in the gooos kernel
that requires SMP-safety treatment. This is document 5 of 7 in
the `impldoc/smp_*.md` design set.

Addresses blocker B9 from `smp_overview.md`:

> B9: Shared data unsynchronized -- `procByTask`, `gInfoByTask`,
> `pitTicks`, etc.

Covers work plan item 12 from `smp_overview.md` section 4.

**Parent docs**:
- `smp_overview.md` -- master structure and work plan.
- `smp_percpu_and_sync.md` -- defines `Spinlock` primitive
  (`Acquire(flags)`/`Release(flags)`, xchg-based) and per-CPU
  storage (`PerCPU` struct via `%gs:offset`, `cpuID()` helper).

## 1. Methodology

The audit was conducted as follows:

1. **Enumerate globals**: grep for top-level `var` declarations
   in every `src/*.go` and `src/*.S` file. This includes `var`
   blocks and standalone `var` lines.

2. **Cross-reference read/write sites**: for each global, search
   all functions that read or write the variable. Identify
   whether the access occurs from ISR context, from a nosplit
   function, from a goroutine that may run on any CPU, or only
   during single-threaded boot.

3. **Classify**: assign each variable to one of four categories
   based on its access pattern under SMP:
   - **NEEDS_PERCPU** -- inherently per-CPU state; must become
     per-CPU arrays or `%gs`-relative fields.
   - **NEEDS_LOCK** -- shared mutable state accessed from
     multiple goroutines (potentially on different CPUs); must
     be protected by a spinlock.
   - **NEEDS_ATOMIC** -- single-word counters or indices where a
     spinlock is overkill; must use atomic load/store or atomic
     read-modify-write instructions.
   - **SAFE** -- immutable after boot, channel-serialized, or
     inherently per-goroutine; no changes needed.

4. **Document fix strategy**: for each non-SAFE variable, specify
   the concrete fix (which lock, which per-CPU field, which
   atomic operation) and cite the design doc that owns the fix if
   applicable.

## 2. NEEDS_PERCPU

Variables that are inherently per-CPU: each CPU must have its own
independent copy. Sharing these across CPUs causes data races
and logic errors (e.g., one CPU's ISR depth counter clobbering
another's).

| Variable | Location | Description | Fix Strategy |
|---|---|---|---|
| `gooos_in_interrupt_depth` | `src/isr.S:168` | ISR nesting counter. ISR prologue increments (`src/isr.S:110`), epilogue decrements (`src/isr.S:130`). Go side reads via `gooosInInterruptDepth` (`src/goroutine_irq.go:17`). | Move to `%gs:4` (`pcpuOffInterruptDepth`). Covered by `smp_percpu_and_sync.md` section 6. ISR prologue becomes `incl %gs:4`, epilogue becomes `decl %gs:4`. |
| `lastErrorCode` | `src/interrupt.go:20` | Error code from most recent interrupt, written by `go_interrupt_handler` (`src/interrupt.go:36`). | Per-CPU array `lastErrorCode[maxCPUs]`, indexed by `cpuID()`. Alternatively, pass error code through the ISR frame pointer (already available as `framePtr` argument) and eliminate the global entirely. |
| `lastFramePtr` | `src/interrupt.go:23` | Register frame pointer from ISR, written by `go_interrupt_handler` (`src/interrupt.go:37`). | Same treatment as `lastErrorCode`. Per-CPU array or eliminate by passing through ISR frame. |
| `tss[104]` | `src/gdt.go:37` | Single 104-byte Task State Segment. `tssSetRSP0` (`src/gdt.go:126-128`) writes RSP0 for Ring 3 -> Ring 0 transitions. Each CPU must have its own TSS so interrupt-driven kernel stack switching is CPU-local. | `perCPUTSS[maxCPUs][tssSize]byte`. Covered by `smp_percpu_and_sync.md` section 5. `tssSetRSP0` rewritten to index by `cpuID()`. |
| `systemStack` | `~/.local/tinygo/src/internal/task/task_stack_amd64.go:7` | TinyGo scheduler stack. The scheduler's `resume()` and `Pause()` use this as the pivot stack for goroutine switches. Two CPUs using the same scheduler stack simultaneously would corrupt each other's execution. | Per-CPU array in the TinyGo runtime patch. Covered by `smp_kernel_scheduler.md` section 3. Each CPU gets its own `systemStack` entry, selected by `cpuID()` at scheduler entry. |

**Rationale for per-CPU classification**: these variables are
written by the CPU that owns them (ISR depth on the CPU taking
the interrupt, TSS RSP0 for the CPU doing the context switch,
scheduler stack for the CPU running the scheduler). A lock would
be semantically wrong -- the data is not "shared and contended"
but "per-CPU and collision-prone."

## 3. NEEDS_LOCK

Variables accessed from multiple goroutines that may run on
different CPUs under SMP. A spinlock serializes access.

| Variable | Location | Description | Fix Strategy |
|---|---|---|---|
| `gInfoByTask` | `src/goroutine_tss.go:40` | `map[uintptr]*gInfo`. Written by `registerRing3GWithStack` (`src/goroutine_tss.go:114-120`), read by `gooosOnResume` (`src/goroutine_tss.go:180`), deleted by `unregisterRing3G` (`src/goroutine_tss.go:122-128`). **SPECIAL CASE** -- see section 6 below. Map access from nosplit is unsafe. | Replace map with fixed-size array `gInfoBySlot[maxRing3Procs]*gInfo` indexed by pool slot. Protect with `gInfoLock` spinlock. Lock ordering rank 3 per `smp_percpu_and_sync.md` section 4.3. |
| `procByTask` | `src/process.go:70` | `map[uintptr]*Process`. Written by `setCurrentProc` (`src/process.go:124-126`), read by `currentProc` (`src/process.go:119`), deleted by `clearCurrentProc` (`src/process.go:130-132`). | Protect with `procLock` spinlock. Lock ordering rank 2 per `smp_percpu_and_sync.md` section 4.3. |
| `procByPID` | `src/process.go:75` | `map[uint32]*Process`. Written by `elfSpawn` (`src/process.go:229`), deleted by `processWait` (`src/process.go:318`), read by `sysWaitHandler` (`src/userspace.go:694`). | Protect with same `procLock` spinlock as `procByTask`. Both maps are accessed together in several paths; single lock simplifies ordering. |
| `foregroundProc` | `src/process.go:97` | `*Process` pointer. Written by `setForegroundProc` (`src/process.go:100-102`), read by `getForegroundProc` (`src/process.go:105-107`) and `sysReadKeyHandler` (`src/userspace.go:475`). | Protect under `procLock` — `sys_exec`/`sys_spawn` already acquire `procLock` for `procByTask`/`procByPID` writes; `setForegroundProc` is called in the same path. Single lock simplifies ordering. Consistent with `smp_user_multicore.md §8`. |
| `vgaCursorRow` / `vgaCursorCol` | `src/vga.go:14-15` | Cursor position integers. Written and read by `vgaConsolePutChar` (`src/vga.go:20-52`), `vgaConsoleClear` (`src/vga.go:86-93`), `sysVgaSetCursorHandler` (`src/userspace.go:525-526`). | Protect with `vgaLock` spinlock. Lock ordering rank 4 per `smp_percpu_and_sync.md` section 4.3. All VGA console operations (`PutChar`, `Scroll`, `Clear`, `Print`) acquire `vgaLock`. |
| `sysReadLineBuf` / `sysReadLineLen` | `src/userspace.go:140-141` | Kernel-side console input buffer (128-byte array + length counter). Used by `readKeyboardLine` in `src/fd.go`. | Spinlock or per-process buffer. In practice, only the `foregroundProc` reads console input at any time (`consoleStdin.Read` returns EOF for non-foreground processes). If `foregroundProc` is made atomic (see above), only one CPU ever accesses these buffers simultaneously. **Conditional SAFE**: no lock needed if the foreground-process invariant holds and `foregroundProc` transitions are atomic. Add a comment documenting this invariant. |

## 4. NEEDS_ATOMIC

Single-word values where a full spinlock is unnecessary.
Atomic load/store or atomic read-modify-write suffices.

| Variable | Location | Description | Fix Strategy |
|---|---|---|---|
| `pitTicks` | `src/pit.go:22` | `uint64` tick counter. Written by `handleTimer` ISR (`src/pit.go:42`), read by `smpInit` (`src/smp.go:145-176`), `afterTicks` (`src/afterticks.go`), and `main` (`src/main.go:319-322`). | `pitTicks++` is a read-modify-write; on x86_64, an aligned 64-bit `mov` is atomic for reads and writes, but `inc` is not atomic across CPUs. Fix: use `lock xaddq $1` in the ISR (assembly helper `atomicInc64`) or `sync/atomic.AddUint64`. Readers use `atomic.LoadUint64`. The ISR runs with interrupts disabled on the BSP (PIT fires only on BSP via LINT0/IOAPIC routing), but readers may be on any CPU. |
| `nextPID` | `src/process.go:80` | `uint32` monotonic PID allocator. Written by `allocPID` (`src/process.go:84-88`). Currently a simple `nextPID++`. | `sync/atomic.AddUint32(&nextPID, 1)` (maps to `lock xaddl` on x86). Eliminates the need for `procLock` around PID allocation. |
| `gooosKbdHead` | `src/keyboard_irq.go:25` | Ring buffer write index. Written by `keyboardIRQSend` (`src/keyboard_irq.go:40-47`, nosplit ISR context). Read by `keyboardIRQRecv` (`src/keyboard_irq.go:56`). | `atomic.StoreUint32` on the producer write (`gooosKbdHead = h + 1` becomes `atomic.StoreUint32(&gooosKbdHead, h+1)`). Producer reads its own `gooosKbdHead` (same CPU, ISR context) so the load is local. Consumer reads via `atomic.LoadUint32(&gooosKbdHead)`. |
| `gooosKbdTail` | `src/keyboard_irq.go:26` | Ring buffer read index. Written by `keyboardIRQRecv` (`src/keyboard_irq.go:59`). Read by `keyboardIRQSend` (`src/keyboard_irq.go:42`). | `atomic.StoreUint32` on the consumer write. `atomic.LoadUint32` on both sides for cross-CPU visibility. The existing `src/keyboard_irq.go` comment at line 9 notes: "x86-TSO guarantees all four required orderings via plain mov"; this is true for BSP-only but under SMP with keyboard IRQ potentially routed to a different CPU via IOAPIC, explicit atomics make the correctness visible to reviewers. |

**Note on x86-TSO**: for aligned 32-bit and 64-bit values,
plain `mov` stores are atomic and stores are not reordered with
other stores on x86-TSO. So `atomic.Store` on x86 compiles to a
plain `mov`, and `atomic.Load` compiles to a plain `mov`. The
purpose of using `sync/atomic` is (a) to communicate intent to
the compiler (prevents reordering of Go-level reads/writes) and
(b) to be correct if the code is ever ported. For
read-modify-write operations like `pitTicks++`, the atomic
increment (`lock xadd`) is genuinely necessary.

## 5. SAFE

Variables that need no SMP changes. Each entry documents why.

| Variable | Location | Reason Safe |
|---|---|---|
| `gdtTable[gdtEntries]` | `src/gdt.go:36` | Immutable after `gdtInit()` during boot. Under SMP, becomes `perCPUGDT[maxCPUs]` but that is a per-CPU conversion covered by `smp_percpu_and_sync.md` section 5, not a data race fix. |
| `bootPML4` | `src/proc_pml4.go:30` | Captured once by `captureBootPML4()` (`src/proc_pml4.go:37-39`) during `main()`, before any Ring-3 goroutine runs. Read-only thereafter. |
| `pml4SharedKernelPDP0` | `src/proc_pml4.go:22` | Captured once by `captureKernelPDP0()` (`src/proc_pml4.go:46-57`). Idempotent init (checks `!= 0` before writing). Safe if only BSP calls it before APs run Ring-3, which is the case: `newProcPML4` is first called from `elfSpawn` in the BSP's `main()` goroutine, and APs do not run Ring-3 until the scheduler is distributed. |
| `apStacks[smpMaxAPs]` | `src/smp.go:50` | Written once by BSP during `smpInit()` (`src/smp.go:127-130`) before SIPI. Each AP reads only its own slot via the atomic counter in the trampoline. No concurrent write after SIPI. |
| `ring3StackPoolCh` | `src/ring3_pool.go:28` | Go buffered channel (`make(chan int, maxRing3Procs)`). Channels are inherently goroutine-safe by Go runtime guarantee. The TinyGo scheduler uses internal locks on channel operations. |
| `keyboardCh` | `src/keyboard_irq.go:31` | Go buffered channel (`make(chan uint32, 16)`). Same reasoning as `ring3StackPoolCh`. |
| `handlers[256]` | `src/interrupt.go:16` | Populated during boot in `main()` (`src/main.go:131-153`) via `registerHandler()` calls. After `smpInit()` returns and APs begin scheduling, no further handler registration occurs. Read-only during ISR dispatch (`src/interrupt.go:42`). See section 7 for the boot-phase invariant. |
| `fs` (FileSystem) | `src/fs.go:29` | All access goes through the `fsTask` goroutine (`src/fs.go:189-209`) via the `fsReqCh` channel (`src/fs.go:186`). The channel serializes all FS operations: one goroutine processes them sequentially. No direct access to `fs` from any other goroutine. See section 8 for details. |
| `fsReqCh` | `src/fs.go:186` | Go channel. Thread-safe by runtime guarantee. |
| `gooosKbdRing[64]` | `src/keyboard_irq.go:24` | Ring buffer data slots. Single-producer (ISR) writes slot before publishing `gooosKbdHead`; single-consumer reads slot after observing `gooosKbdTail != gooosKbdHead`. With atomic stores on head/tail (section 4), x86-TSO guarantees the data write is visible before the index update. No lock needed on the data array itself. |

## 6. Special Case: gInfoByTask nosplit -> Array Replacement

### Problem

`gooosOnResume` (`src/goroutine_tss.go:174-175`) is
`//go:nosplit` and reads `gInfoByTask[t]` on line 180:

```go
//go:nosplit
func gooosOnResume() {
    t := taskCurrent()
    ...
    gi := gInfoByTask[t]
```

Under SMP, concurrent access to `gInfoByTask` requires a
spinlock. Spinlock `Acquire()` is safe in nosplit context (it is
just `cli` + `xchg`, no allocation, no stack growth). However,
**Go map access itself can trigger runtime allocation**: the
hash table may need to grow (rehash) on insert, or the map
lookup may call into the runtime's hash function which on some
paths allocates. TinyGo's map implementation
(`hashmap{Get,Set,Delete}`) is not nosplit-safe.

### Current `gInfoByTask` comment

The existing code at `src/goroutine_tss.go:29-31` already
documents this concern:

> proc is cached here so gooosOnResume (//go:nosplit) can swap
> CR3 without a second map lookup; map access from a nosplit
> hook is unsafe (TinyGo's hash path can call into the runtime
> allocator).

### Solution

Replace the map with a fixed-size array indexed by the ring3
pool slot:

```go
// src/goroutine_tss.go (replacement)

var gInfoBySlot [maxRing3Procs]*gInfo

// gInfoBySlot is indexed by proc.poolIdx. The pool slot is
// acquired in ring3Wrapper and tracked in Process.poolIdx
// (src/ring3_pool.go, src/process.go:47).
```

`maxRing3Procs` is already defined as 32 in
`src/ring3_pool.go:20`. Pool indices range from 0 to 31,
providing a compact, bounded, allocation-free index.

**Register path** (`registerRing3GWithStack`): writes
`gInfoBySlot[proc.poolIdx]` instead of `gInfoByTask[t]`.

**Resume path** (`gooosOnResume`): needs `proc.poolIdx` to
index the array. Since `gooosOnResume` does not have `proc`
directly, it currently derives the `gInfo` from `taskCurrent()`.
Under the new scheme, we add a per-CPU field
`PerCPU.currentPoolIdx` (or the equivalent: a per-task mapping
stored when `setCurrentProc` runs). The simplest approach:
store `poolIdx` in a new per-CPU field that `setCurrentProc`
writes and `gooosOnResume` reads:

```go
func setCurrentProc(proc *Process) {
    procByTask[taskCurrent()] = proc
    setPerCPUPoolIdx(proc.poolIdx) // writes %gs:XX
}

//go:nosplit
func gooosOnResume() {
    idx := readPerCPUPoolIdx() // reads %gs:XX
    if idx < 0 {
        return // kernel-only goroutine
    }
    gi := gInfoBySlot[idx]
    ...
}
```

This eliminates map access entirely from the nosplit hot path.
Array indexing is a single bounds-checked memory load with no
allocation.

**Spinlock**: with the array, each slot is written by
`registerRing3G` (called once from `ring3Wrapper` goroutine)
and read by `gooosOnResume` (called on every resume of the
same goroutine). Since a goroutine runs on one CPU at a time,
the writer and reader for a given slot never execute
concurrently. Different slots are accessed by different
goroutines on different CPUs — no contention. **No spinlock
needed** on `gInfoBySlot` itself. The per-CPU
`currentPoolIdx` field (see `smp_percpu_and_sync.md §1.3`)
provides O(1) lookup from `gooosOnResume`.

## 7. Special Case: handlers[256] Boot-Phase Invariant

`handlers[256]` (`src/interrupt.go:16`) is populated exclusively
in `main()` before the scheduler distributes goroutines to APs:

```
main.go:131  registerHandler(0, handleDivisionError)
main.go:132  registerHandler(14, handlePageFault)
main.go:141  for i := 32; i <= 47; i++ { registerHandler(i, handleDefaultIRQ) }
main.go:147  registerHandler(32, handleTimer)
main.go:153  registerHandler(33, handleKeyboard)
```

After `smpInit()` returns (`src/main.go:349`) and eventually
`setupUserspace()` enters Ring 3, no further `registerHandler()`
calls occur. The array becomes read-only for the remainder of
the kernel's lifetime.

**Invariant**: "No handler registration after AP scheduler
start." As long as this invariant holds, no lock is needed on
`handlers`. The ISR dispatcher (`go_interrupt_handler`,
`src/interrupt.go:42`) reads `handlers[vector]` in ISR context
where spinlock acquisition would be undesirable anyway.

**Enforcement**: add a compile-time or boot-time assertion.
Simplest approach: a `bool handlersLocked` flag set after the
last `registerHandler` call. If `registerHandler` is called
after the flag is set, panic with a clear message:

```go
var handlersLocked bool

func registerHandler(vector int, handler InterruptHandler) {
    if handlersLocked {
        serialPrintln("FATAL: registerHandler after boot")
        for { hlt() }
    }
    handlers[vector] = handler
}
```

Set `handlersLocked = true` in `main()` after the last
`registerHandler` call and before `smpInit()`.

## 8. Special Case: FS Serialization via Channel

The `fs` global (`src/fs.go:29`) is a `FileSystem` struct
containing a `[32]FileEntry` array. It is accessed exclusively
by the following functions:

- `fsCreate` (`src/fs.go:33`)
- `fsWrite` (`src/fs.go:54`)
- `fsRead` (`src/fs.go:72`)
- `fsAppend` (`src/fs.go:89`)
- `fsTruncate` (`src/fs.go:111`)
- `fsSize` (`src/fs.go:122`)
- `fsList` (`src/fs.go:133`)
- `fsDelete` (`src/fs.go:146`)

All of these are called only from `fsTask` (`src/fs.go:189-209`),
which is a single goroutine that receives operations via
`fsReqCh` (`src/fs.go:186`). The channel serializes all FS
operations: exactly one goroutine processes them, sequentially.

External callers use the `fsSend*` wrapper functions
(`src/fs.go:211-239`), which send a request on `fsReqCh` and
block on a per-request reply channel. The Go channel is
thread-safe by runtime guarantee.

**Exception**: `main()` calls `fsCreate` and `fsWrite` directly
during boot (`src/main.go:307-427`). This is safe because it
happens before `go fsTask()` starts (`src/main.go:360`) and
before any AP runs goroutines. After boot, all access is
channel-serialized.

**Conclusion**: no spinlock needed on `fs`. The channel-based
architecture already provides correct serialization.

## 9. TinyGo Atomic Primitives Verification Plan

The fixes in section 4 rely on `sync/atomic` functions
(`AddUint64`, `LoadUint64`, `StoreUint32`, etc.) emitting
`lock`-prefixed or otherwise SMP-correct instructions on
x86_64 baremetal.

### Concern

TinyGo's `sync/atomic` implementation for baremetal targets may
fall back to plain `mov` instructions (since baremetal has
traditionally been single-core). On x86-TSO, plain aligned
`mov` is sufficient for load/store atomicity, but
read-modify-write operations like `AddUint64` **must** use
`lock xadd` or equivalent to be atomic across CPUs.

### Verification Steps

1. **Build a test binary** that calls `atomic.AddUint64`,
   `atomic.LoadUint64`, `atomic.StoreUint32`,
   `atomic.AddUint32`.

2. **Disassemble** via `objdump -d` and search for:
   - `lock xaddq` or `lock addq` for `AddUint64`
   - `lock xaddl` or `lock addl` for `AddUint32`
   - Plain `movq`/`movl` for `Load`/`Store` (acceptable on
     x86-TSO for aligned values)

3. **If TinyGo emits non-locked RMW**: the atomic Add functions
   are broken for SMP. Fix options:
   - (a) Patch TinyGo's `sync/atomic` to emit `lock` prefixed
     instructions on the gooos target.
   - (b) Write assembly helpers (`atomicAddUint64`,
     `atomicAddUint32`) in `src/stubs.S` and call them directly,
     bypassing `sync/atomic`.

4. **If TinyGo emits locked RMW**: `sync/atomic` is usable
   as-is. Prefer it for readability.

### Fallback Assembly Helpers

If option (b) is needed:

```asm
// src/stubs.S

// atomicAddUint64(ptr *uint64, delta uint64) uint64
// Returns the OLD value (like sync/atomic.AddUint64 - delta).
.global atomicAddUint64
atomicAddUint64:
    lock xaddq %rsi, (%rdi)
    movq %rsi, %rax
    ret

// atomicAddUint32(ptr *uint32, delta uint32) uint32
.global atomicAddUint32
atomicAddUint32:
    lock xaddl %esi, (%rdi)
    movl %esi, %eax
    ret
```

## 10. Ordered Conversion Plan

Variables are fixed in dependency order across the phased work
plan from `smp_overview.md` section 4. The phase assignments
below ensure that each variable is fixed before the code path
that exercises it runs on multiple CPUs.

### Phase 0 -- Foundation (work plan items 1-4)

These are prerequisites; no data audit fixes land here, but the
infrastructure they provide (per-CPU storage, spinlock primitive)
is used by all subsequent fixes.

### Phase 1 -- Kernel Scheduler on APs (work plan items 5-8)

| Priority | Variable | Reason |
|---|---|---|
| 1 | `gooos_in_interrupt_depth` | ISR fires on every CPU; must be per-CPU before APs take interrupts. |
| 2 | `tss[104]` | Each AP needs its own TSS before loading its GDT. |
| 3 | `systemStack` | Each AP needs its own scheduler stack before entering the scheduler loop. |
| 4 | `lastErrorCode` / `lastFramePtr` | ISR writes these on every interrupt; must be per-CPU before APs handle interrupts. |

### Phase 2 -- Shared Data Protection (work plan item 12)

| Priority | Variable | Reason |
|---|---|---|
| 1 | `gInfoByTask` -> `gInfoBySlot` | Array replacement (no spinlock needed — per-slot partitioning). Must land before any Ring-3 goroutine runs on an AP. Critical path: `gooosOnResume` fires on every goroutine switch. Per-CPU `currentPoolIdx` provides O(1) lookup. |
| 2 | `procByTask` / `procByPID` | Spinlock (`procLock`). Must land before `elfSpawn` can be called from AP goroutines. |
| 3 | `nextPID` | Atomic increment. Can land alongside `procLock` or independently. |
| 4 | `pitTicks` | Atomic increment. PIT fires on BSP only initially (IOAPIC routing comes later), but readers may be on any CPU once the scheduler distributes goroutines. |
| 5 | `gooosKbdHead` / `gooosKbdTail` | Atomic load/store. Keyboard IRQ is BSP-only until IOAPIC routes it. Readers (`keyboardPump`) may migrate to an AP. |
| 6 | `foregroundProc` | Protect under `procLock` (same lock as `procByTask`). Low urgency. |
| 7 | `vgaCursorRow` / `vgaCursorCol` | Spinlock (`vgaLock`). Low urgency -- VGA writes are slow and infrequent relative to scheduling. |
| 8 | `sysReadLineBuf` / `sysReadLineLen` | Document invariant or add spinlock. Lowest urgency. |

### Phase 3 -- Verification

Run the full `smp_verification.md` test matrix with all fixes
in place. Stress test: multiple Ring-3 processes on different
CPUs doing concurrent `sys_exec`, `sys_write`, and
`sys_read_key`.

## 11. Risk Register Delta

**Adds:**

| Risk ID | Description | Mitigation |
|---|---|---|
| `R-map-nosplit` | `gInfoByTask` map access from nosplit function may allocate, causing stack overflow or scheduler corruption. | Replace with fixed-size array (section 6). Highest priority fix in Phase 2. |
| `R-atomic-tinygo` | TinyGo `sync/atomic` may not emit `lock`-prefixed RMW on baremetal x86_64. | Verify via objdump (section 9). Fallback: assembly helpers. |
| `R-handler-reg` | Late `registerHandler` call after APs start scheduling would race on `handlers[256]`. | Boot-phase invariant enforcement (section 7). |
| `R-pitTicks-rmw` | `pitTicks++` in ISR is a non-atomic RMW. Under SMP with IOAPIC routing, two CPUs could both handle timer (unlikely but possible during routing transitions). | Atomic increment (section 4). |
| `R-vga-tearing` | Concurrent VGA writes from multiple CPUs produce garbled output. | `vgaLock` spinlock (section 3). Low severity -- cosmetic only. |

**Retires (when fixes land):**

| Risk ID | Retired By |
|---|---|
| `R-b9-shared-data` | Full audit + per-variable fixes (this document). |

## 12. Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
