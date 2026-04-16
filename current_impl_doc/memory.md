# Memory Management

gooos runs in 64-bit long mode with a single identity-mapped
kernel region and per-process user page tables layered on top.

## Physical / Virtual Memory Map

```mermaid
block-beta
    columns 1
    block:kernel
        columns 1
        A["0x00000000–0x000FFFFF<br/>BIOS reserved (VGA text @ 0xB8000)"]
        B["0x00100000–0x001xxxxx<br/>kernel .text + .multiboot + .rodata"]
        C["_globals_start<br/>.data + .bss (incl. 16 KiB stack)"]
        D["_globals_end / _heap_start<br/>4 MiB kernel heap (.heap @nobits)"]
        E["_heap_end<br/>1 page guard gap"]
        F[".pagetables (PML4, PDP, PD for boot)"]
        G["_alloc_start<br/>free pages for allocPage bump / LIFO"]
    end
    space
    block:user
        columns 1
        H["0x40100000 — user .text / .rodata / .data / .bss"]
        I["_heap_start inside user .bss — 256 KiB fixed heap"]
        J["0x40300000 — argument page (kernel copies argv here)"]
        K["0x7FFF0000 — user stack (8 KiB, 2 pages, grows down)"]
    end
```

### Kernel Linker Script (`src/linker.ld`)

```ld
SECTIONS {
    . = 0x100000;                       /* load @ 1 MiB */
    .multiboot : ALIGN(4)  { KEEP(*(.multiboot)) }
    .text      : ALIGN(16) { *(.text .text.*) }
    .rodata    : ALIGN(16) { *(.rodata .rodata.*) }
    _globals_start = .;                 /* conservative-GC root range begin */
    .data      : ALIGN(16) { *(.data .data.*) }
    .bss       : ALIGN(4096) { *(COMMON) *(.bss .bss.*) }
    _globals_end = .;
    .heap      : ALIGN(4096) {          /* 4 MiB reserved for kernel heap */
        _heap_start = .;
        *(.heap)
        _heap_end = .;
    }
    . += 4096;                          /* guard gap */
    .pagetables : ALIGN(4096) { *(.pagetables) }
    . = ALIGN(4096);
    _alloc_start = .;                   /* bump allocator base */
    _stack_top = stack_top;             /* re-exported for GC stack scan */
}
```

- **Identity map**: `boot.S` builds a single PML4 + PDP + PD
  covering `[0, 1 GiB)` with 512 × 2 MiB huge pages.
- **User pages** (0x40000000 and above) are mapped at 4 KiB
  granularity by `elfSpawn` / `elfLoad`, each with
  `pagePresent | pageWrite | pageUser`.
- **`_globals_start..end`** is the range the conservative GC
  scans for live pointers. `scripts/verify_globals.sh` asserts
  every TinyGo runtime queue (`runqueue`, `sleepQueue`,
  `timerQueue`) lands inside this window.

## Page Allocator (`src/vm.go`)

Bump + LIFO free-stack hybrid. Freed pages are pushed onto a
`.bss`-resident stack; the next `allocPage()` pops the most
recently freed page. The stack is bounded to keep metadata
small (`freeStackCap = 4096`, i.e. 32 KiB of `.bss`).

**`allocPage` path**:

```mermaid
stateDiagram-v2
    [*] --> CheckStack
    CheckStack: allocPage() — cli guard
    CheckStack --> PopFree : freeStackLen > 0
    CheckStack --> Bump    : freeStackLen == 0
    PopFree: pop freeStack[--freeStackLen]
    Bump: page = nextFreePage<br/>nextFreePage += 4 KiB
    PopFree --> Zero
    Bump --> Zero
    Zero: memset(page, 0, 4 KiB)
    Zero --> [*]: return page
```

**`freePage` path**:

```mermaid
stateDiagram-v2
    [*] --> Enter
    Enter: freePage(p) — cli guard
    Enter --> ZeroFree
    ZeroFree: memset(p, 0, 4 KiB)
    ZeroFree --> Check
    Check: freeStackLen < freeStackCap ?
    Check --> PushFree : yes
    Check --> Drop     : no (stack full)
    PushFree: freeStack[freeStackLen++] = p
    Drop: page leaks (rare)
    PushFree --> [*]
    Drop --> [*]
```

### API Summary

| Function | Purpose | Notes |
|---|---|---|
| `allocPage() uintptr` | 4 KiB page | Free-stack first, then bump |
| `freePage(p)` | Return a page | Zeroes; drops if stack full |
| `allocPagesContig(n)` | `n` contiguous pages | Bump only (bypasses stack) — used for kernel stacks |
| `mapPage(va, pa, flags)` | Map into current PML4 (boot CR3) | 4 KiB granularity |
| `unmapPage(va)` | Unmap + `invlpg` | |
| `mapPageInto(pml4, va, pa, flags)` | Map into a specific PML4 | Used by `elfSpawn` for child processes |
| `walkAndGetPaddrIn(pml4, va)` | Walk a per-process PML4 | Read-only |

All allocation/mapping functions run with CLI set
(`readFlags` + `restoreFlags`) so an ISR cannot observe
half-written page tables.

## Per-Process PML4

Since 4e (commit `b96f83d`), each user process owns its own PML4
page. The PML4 shares PDP[0] with the kernel's boot PML4, so
kernel addresses (< 1 GiB) remain valid in every process. User
virtual addresses (≥ 1 GiB) are per-process.

```mermaid
classDiagram
    class Process {
        +fds [16]FileDesc
        +pml4 uintptr
        +pid uint32
        +exitCh chan uintptr
        +parent *Process
        +StackTop uintptr
        +HeapBreak uintptr
        +UserPages [512]uintptr
    }
    class PML4 {
        +entry[0] : shares kernel PDP
        +entry[1..511] : per-process user PDPs
    }
    class gInfo {
        +stackTop uintptr
        +proc *Process
    }
    Process "1" --> "1" PML4 : pml4 field
    Process "1" --> "1" gInfo : registerRing3GWithStack
```

### CR3 Swap on Goroutine Resume

```mermaid
sequenceDiagram
    participant Sched as TinyGo scheduler
    participant Task as resume()
    participant Hook as gooosOnResume
    participant CR3

    Sched->>Task: pick next runnable goroutine
    Task->>Hook: call before swapTask
    Hook->>Hook: t = taskCurrent()
    Hook->>Hook: gi = gInfoByTask[t]
    alt gi == nil (kernel-only goroutine)
        Hook-->>Task: return (leave CR3 alone)
    else gi.proc != nil
        Hook->>CR3: writeCR3(gi.proc.pml4<br/>or bootPML4 if 0)
        Hook->>Hook: tssSetRSP0(gi.stackTop)
    end
    Task->>Task: swapTask(s.sp)
    Task->>Sched: back to scheduler loop
```

`gooosOnResume` is `//go:nosplit` and must not allocate — the
map lookup is the only heap touch, and the cached `gi.proc`
pointer lets us avoid a second map probe for the CR3 swap.

## User Heap Model

```mermaid
flowchart TB
    A[user/rt0.S defines mmap wrapper<br/>→ sys_sbrk] --> B{baremetal.go:<br/>growHeap → false}
    B -->|yes, always| C[No runtime heap growth<br/>under gc=leaking + baremetal]
    C --> D[user/linker_user.ld reserves<br/>256 KiB inside .bss]
    D --> E[_heap_start .. _heap_end<br/>= fixed 256 KiB]
    E --> F[bump-allocator inside user<br/>gc=leaking never reclaims]
```

- **256 KiB user heap** per process, reserved at link time
  inside `.bss` so the kernel's ELF loader maps it as
  PT_LOAD memsz pages.
- **`gc=leaking`**: every `make`, `append`, `new`, goroutine
  stack allocation is permanent. Fine for short-lived user
  programs; larger programs should escalate to `gc=conservative`.
- `sys_sbrk` exists and `HeapBreak` is tracked per-process,
  but the current user runtime (baremetal.go) never calls
  `mmap`/`sbrk` — the fixed reservation is enough.

## In-Memory Filesystem (`src/fs.go`)

The FS is a flat 32-entry table in `.bss`. Each entry holds
`maxFileData = 131072` bytes (128 KiB) → 4 MiB total FS
footprint. Served by the `fsTask` goroutine over the
`fsReqCh` channel (see `ipc.md`).

```mermaid
classDiagram
    class FileSystem {
        +files [32]FileEntry
    }
    class FileEntry {
        +name string
        +data [131072]byte
        +size int
        +used bool
    }
    FileSystem "1" --> "32" FileEntry : in .bss
```

Sized to absorb the current largest user binary
(`tinyc.elf` at ~124 KiB) with a doubling-ahead headroom per
policy.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
