# Heap Allocator & GC-Enabled Demo — Detailed Implementation Design

> **Audience**: This document is written for a Claude Code session (or a human engineer) who will execute it verbatim. Every file path is relative to the repository root. Every code block is copy-pasteable. Follow the sections in order.
>
> **Prerequisite**: The hello-world milestone must already be working. See `impldoc/helloworld_cgo_design.md` and `impldoc/helloworld_cgo_implementation_report.md` for context on the existing codebase and the TinyGo build quirks discovered during that milestone.

---

## 1. Overview & Goals

Extend the gooos bare-metal x86_64 kernel to support **dynamic memory allocation** by:

1. Switching `target.json` from `"gc": "none"` to `"gc": "leaking"` (TinyGo's bump allocator — allocates but never frees).
2. Providing a functional `mmap` stub that returns a real heap memory region, so TinyGo's `runtime_unix.go:preinit()` can initialize `heapStart` / `heapEnd`.
3. Changing the boot flow so `boot.S` calls TinyGo's runtime `main` entry point (which runs the full initialization chain: `preinit` → `initHeap` → `initAll` → user `main()`), instead of calling `kernel_main` directly.
4. Writing a VGA demo in Go's `func main()` that exercises heap allocation (`make`, `append`, `new`, string concatenation) and displays results on screen.

### Out of Scope

- `gc: "conservative"` (mark/sweep GC) — requires `__ehdr_start`, `tinygo_scanCurrentStack`, and `findGlobals()` infrastructure. Deferred to a future milestone (see §9).
- Goroutines / scheduler (keep `"scheduler": "none"`).
- Interrupts, IDT, keyboard, serial, timer.
- Virtual memory beyond the existing 1 GiB identity map.

### Success Criteria

- `make build` succeeds with no linker errors.
- `make run` or `make run-kernel` shows three lines on the VGA screen proving heap allocation works:
  - Line 0: `Hello, Heap!` (built via string concatenation)
  - Line 1: `Heap works!` (built via `make` + `append`)
  - Line 2: `new(uint64) = 42` (proves `new()` works)
- No triple-fault; kernel halts cleanly.

---

## 2. TinyGo GC Investigation Results (Empirical)

### 2.1 Undefined symbols with `gc: "leaking"`

Changing `target.json` from `gc: "none"` to `gc: "leaking"` and building with `tinygo build -target=src/target.json -o tmp/kernel_go.o ./src` produces an object with **the same five** undefined symbols as before:

```
$ nm tmp/kernel_go.o | grep " U " | sort -u
                 U abort
                 U mmap
                 U raise
                 U tinygo_register_fatal_signals
                 U write
```

**No new stubs are required.** The `memcpy` symbol (referenced in `gc_leaking.go:realloc`) is resolved internally by LLVM as a built-in.

### 2.2 TinyGo runtime entry symbol

The TinyGo-generated object exports a `main` symbol (from `runtime_unix.go`):

```
$ nm tmp/kernel_go.o | grep -E " T "
00000000000000f4 T main
000000000000016f T tinygo_handle_fatal_signal
0000000000000268 T kernel_main
```

`main` is the C-ABI entry point defined by `//export main` in `runtime_unix.go`. Its signature is `main(argc int32, argv *unsafe.Pointer) int`. This is distinct from the user's `main.main` (Go's package-level main function), which has a different mangled symbol name.

### 2.3 `runtime_unix.go:preinit()` — the heap initialization path

When `gc != "none"`, `preinit()` becomes live code (not dead). Its heap initialization logic:

```go
func preinit() {
    heapMaxSize = 1 * 1024 * 1024 * 1024  // 1 GB
    for {
        addr := mmap(nil, heapMaxSize, PROT_READ|PROT_WRITE,
                      MAP_PRIVATE|MAP_ANONYMOUS, -1, 0)
        if addr == unsafe.Pointer(^uintptr(0)) {  // MAP_FAILED = -1
            heapMaxSize /= 2
            if heapMaxSize < 4096 {
                runtimePanic("cannot allocate heap memory")
            }
            continue  // retry with smaller size
        }
        heapStart = uintptr(addr)
        heapEnd = heapStart + heapSize  // heapSize = 128 KiB initially
        break
    }
}
```

**Key insight**: `preinit()` retries with progressively smaller `heapMaxSize` if `mmap` returns -1. Our `mmap` stub can exploit this: return -1 for requests larger than our physical heap, and return `_heap_start` for requests that fit. This naturally caps `heapMaxSize` at our heap size.

### 2.4 `gc_leaking.go:alloc()` and `zero_new_alloc`

The leaking GC's `alloc()` is a bump allocator: it advances `heapptr` and calls `zero_new_alloc(pointer, size)`. On the non-baremetal path (our case), `zero_new_alloc` is a **no-op** (`zero_new_alloc_noop.go`, build tag: `gc.leaking && !baremetal && !nintendoswitch`). It assumes memory from `mmap` is pre-zeroed.

**Implication**: Our heap region **must be zeroed before first use**. The leaking GC's bump pointer is monotonically increasing and never reuses freed memory, so zeroing once at boot is sufficient. We achieve this by defining the heap as a `@nobits` input section (in `stubs.S`), which the linker places in a PT_LOAD segment with `memsz > filesz`. The ELF loader (GRUB or QEMU's Multiboot loader) zero-fills this gap per the ELF spec.

**Additional safety note**: QEMU zeroes all guest RAM at startup, so this works even if the ELF loader has a bug. On real hardware, RAM is not guaranteed to be zero after POST. If real hardware is ever targeted, add an explicit zeroing loop in `boot.S` (e.g., `rep stosb` over the heap region) before `call main`.

### 2.5 `growHeap()` behavior

`runtime_unix.go:growHeap()` extends `heapEnd` up to `heapStart + heapMaxSize`:

```go
func growHeap() bool {
    if heapSize == heapMaxSize {
        return false
    }
    heapSize = (heapSize * 4 / 3) &^ 4095  // grow by ~33%, page-aligned
    if heapSize > heapMaxSize {
        heapSize = heapMaxSize
    }
    setHeapEnd(heapStart + heapSize)
    return true
}
```

Since our `mmap` stub caps `heapMaxSize` at the physical heap size (4 MiB), `growHeap()` will never extend `heapEnd` beyond `_heap_end`. When the heap is full, `alloc()` panics with "out of memory" (which becomes `ud2` via `panic-strategy: "trap"`).

### 2.6 Full runtime call chain

When `boot.S` calls `main(0, nil)`:

```
runtime_unix.go: main(argc=0, argv=nil)
    ├── preinit()
    │   └── mmap(nil, heapMaxSize, ...) → retry loop → our stub returns _heap_start
    │       heapStart = _heap_start, heapEnd = _heap_start + 128 KiB
    ├── tinygo_register_fatal_signals() → noop stub
    ├── stackTop = getCurrentStackPointer() → reads RSP via LLVM intrinsic
    └── runMain()
        └── run()                          [scheduler_none.go]
            ├── initHeap()                 [gc_leaking.go: heapptr = heapStart]
            ├── initAll()                  [compiler-generated package init]
            └── callMain()                 [→ user's main.main()]
                └── func main() { ... }    [our VGA demo code]
```

---

## 3. Design Decisions

### 3.1 GC mode: `gc: "leaking"` (not `gc: "conservative"`)

The leaking GC requires **zero** new stubs beyond the existing five. The conservative GC would require `__ehdr_start` (ELF header in memory — problematic with Multiboot), `tinygo_scanCurrentStack` (assembly trampoline), `memset`, and `memcpy`. See §9 for the upgrade path.

### 3.2 Heap region: linker-defined, 4 MiB, after BSS

**Current memory layout** (from the hello-world binary):

| Region | Address Range | Size |
|---|---|---|
| `.multiboot` | `0x100000` | 12 B |
| `.text` | `0x100010 – 0x10035E` | ~850 B |
| `.rodata` | `0x100360 – 0x100416` | ~180 B |
| `.data` | `0x100420 – 0x100436` | 22 B |
| `.bss` (stack + page tables) | `0x101000 – 0x108010` | ~28 KiB |
| **Heap (NEW)** | `~0x109000 – ~0x509000` | **4 MiB** |
| VGA text buffer | `0xB8000 – 0xB9000` | 4 KiB |
| (unused until 1 GiB) | `0x509000 – 0x40000000` | ~1017 MiB |

4 MiB is generous for a demo and well within QEMU's default 128 MB RAM. The identity map covers the full 1 GiB.

### 3.3 Boot flow: call TinyGo's `main`, not `kernel_main`

The TinyGo runtime's `main(argc, argv)` runs the full initialization chain: `preinit()` (heap via mmap), `initHeap()` (GC init), `initAll()` (package init), then `callMain()` (user's `main()`). This is the cleanest way to get a properly initialized heap without `//go:linkname` hacks.

The user's demo code moves from `//export kernel_main` to the standard Go `func main()`.

### 3.4 `mmap` stub strategy

The stub uses the retry loop in `preinit()` to naturally cap `heapMaxSize`:

1. `preinit()` calls `mmap(nil, 1GB, ...)` → stub sees 1 GB > 4 MiB → returns -1
2. `preinit()` halves to 512 MB → still > 4 MiB → returns -1
3. ... continues halving: 256 MB, 128 MB, 64 MB, 32 MB, 16 MB, 8 MB ...
4. `preinit()` calls `mmap(nil, 4MB, ...)` → stub sees 4 MB ≤ 4 MiB → returns `_heap_start`
5. `heapMaxSize = 4 MB`, `heapStart = _heap_start`, `heapEnd = _heap_start + 128 KiB`

---

## 4. Memory Layout Diagram

```
Physical Address Space (identity-mapped 0 – 1 GiB)

0x00000000  ┌──────────────────────────────┐
            │  BIOS / BDA / Legacy I/O     │
0x000B8000  │  VGA Text Buffer (4 KiB)     │
0x000B9000  │  ...                         │
0x00100000  ├──────────────────────────────┤  ← Kernel load base
            │  .multiboot (12 B)           │
            │  .text     (~850 B)          │
            │  .rodata   (~180 B)          │
            │  .data     (~22 B)           │
0x00101000  │  .bss                        │  ← 4 KiB aligned
            │    stack_bottom  (16 KiB)    │
            │    stack_top = 0x105000      │
            │    pml4 (4 KiB)              │
            │    pdp  (4 KiB)              │
            │    pd   (4 KiB)              │
            │    runtime BSS               │
~0x00109000 ├──────────────────────────────┤  ← _heap_start (4 KiB aligned)
            │                              │
            │  Heap (4 MiB, NOBITS)        │
            │  Bump-allocated by gc.leaking│
            │                              │
~0x00509000 ├──────────────────────────────┤  ← _heap_end
            │  (unused: ~1017 MiB)         │
0x40000000  └──────────────────────────────┘  ← End of identity map
```

---

## 5. File Specifications

### 5.1 `src/target.json` (modified)

**Change**: `"gc": "none"` → `"gc": "leaking"`

```json
{
    "llvm-target": "x86_64-unknown-linux-elf",
    "cpu": "x86-64",
    "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
    "build-tags": ["gooos"],
    "goos": "linux",
    "goarch": "amd64",
    "gc": "leaking",
    "scheduler": "none",
    "panic-strategy": "trap",
    "linker": "ld.lld",
    "rtlib": "compiler-rt"
}
```

### 5.2 `src/linker.ld` (modified)

**Changes**: Add `.heap` section with `_heap_start` / `_heap_end`. Add `_globals_start` / `_globals_end` / `_stack_top` for future gc=conservative.

```ld
/* src/linker.ld — kernel layout with heap region. Load address = 1 MiB. */

ENTRY(_start)

SECTIONS
{
    . = 0x100000;

    /* Multiboot 1 header MUST be within the first 8 KiB of the kernel
     * file and 4-byte aligned. KEEP prevents --gc-sections from dropping it. */
    .multiboot : ALIGN(4)
    {
        KEEP(*(.multiboot))
    }

    .text : ALIGN(16)
    {
        *(.text .text.*)
    }

    .rodata : ALIGN(16)
    {
        *(.rodata .rodata.*)
    }

    _globals_start = .;

    .data : ALIGN(16)
    {
        *(.data .data.*)
    }

    /* .bss holds the stack AND the 4 KiB-aligned page tables (pml4, pdp,
     * pd). CR3 requires 4 KiB alignment on PML4, so ALIGN(4096) is
     * load-bearing — do not relax it. */
    .bss : ALIGN(4096)
    {
        *(COMMON)
        *(.bss .bss.*)
    }

    _globals_end = .;

    /* Heap region: 4 MiB, page-aligned. The .heap input section is defined
     * in stubs.S as @nobits, so ld.lld emits it in a PT_LOAD segment with
     * memsz > filesz. The ELF loader (GRUB or QEMU) zero-fills the
     * memsz - filesz gap, satisfying the leaking GC's assumption that
     * mmap'd memory is pre-zeroed.
     *
     * NOTE: Do NOT use (NOLOAD) here. Despite its name, (NOLOAD) has
     * different semantics in GNU ld (skip loading entirely) vs ld.lld
     * (set SHT_NOBITS). Using an explicit @nobits input section is
     * portable across both linkers. */
    .heap : ALIGN(4096)
    {
        _heap_start = .;
        *(.heap)
        _heap_end = .;
    }

    /* _stack_top is defined in boot.S as a label; re-export it here
     * for potential future use by gc=conservative. */
    _stack_top = stack_top;

    /DISCARD/ :
    {
        *(.eh_frame)
        *(.eh_frame_hdr)
        *(.rel.eh_frame)
        *(.rela.eh_frame)
        *(.note .note.*)
        *(.comment)
    }
}
```

**Notes**:
- `_globals_start` / `_globals_end` bracket `.data` and `.bss`. The conservative GC's `findGlobals()` would scan this range for root pointers. Not used by the leaking GC, but cheap to add now. **Caveat for future gc=conservative**: `.bss` currently includes the kernel stack and page tables (pml4/pdp/pd). Page table entries contain physical addresses with flag bits (e.g., `0x83`) that the conservative scanner could misinterpret as heap pointers. For gc=conservative, consider separating boot infrastructure into a `.bss.boot` section outside the globals range.
- The `.heap` input section is defined as `@nobits` in `stubs.S`. The linker emits it in a `PT_LOAD` segment with `memsz > filesz`, so the ELF loader allocates and zero-fills the region. This is more portable than using the linker's `(NOLOAD)` directive, which has inconsistent semantics across GNU ld and ld.lld.
- `_stack_top = stack_top` re-exports the label from `boot.S` for the runtime. This is harmless if unused.

### 5.3 `src/stubs.S` (modified)

**Changes**: Replace `mmap` stub with a functional implementation. All other stubs remain.

```gas
/*
 * src/stubs.S -- libc/runtime symbol stubs for bare-metal TinyGo.
 *
 * TinyGo's linux runtime glue (runtime_unix.go, compiled because goos=linux)
 * references a handful of libc/runtime symbols. Some are dead code, some
 * become live when gc != "none". We provide minimal implementations here.
 *
 *   abort                         -- runtime.runtimePanicAt fallback (dead code)
 *   write                         -- runtime.putchar (dead code)
 *   mmap                          -- runtime_unix.preinit: heap allocation (LIVE with gc != none)
 *   raise                         -- tinygo_handle_fatal_signal (dead code)
 *   tinygo_register_fatal_signals -- tinygo main startup (called, but noop is safe)
 */

    .text

    /* ---- abort: halt the CPU (should never be reached) -------------- */
    .global abort
abort:
    cli
1:  hlt
    jmp     1b

    /* ---- write: return 0 (dead code, putchar path) ------------------ */
    .global write
write:
    xorq    %rax, %rax
    ret

    /* ---- mmap: return _heap_start if size fits, else -1 ------------- */
    /*                                                                   */
    /* Called by runtime_unix.go:preinit() to reserve virtual heap space. */
    /* preinit() starts with 1 GB and halves on failure until success.   */
    /*                                                                   */
    /* SysV AMD64 ABI parameters:                                        */
    /*   rdi = addr  (ignored, always nil)                               */
    /*   rsi = length (requested size)                                   */
    /*   edx = prot  (ignored)                                           */
    /*   ecx = flags (ignored)                                           */
    /*   r8d = fd    (ignored)                                           */
    /*   r9  = offset (ignored)                                          */
    /*                                                                   */
    /* Returns: pointer to heap region, or -1 (MAP_FAILED).              */
    .global mmap
mmap:
    /* Calculate available heap size: _heap_end - _heap_start */
    movq    $_heap_end, %rax
    movq    $_heap_start, %rcx
    subq    %rcx, %rax              /* rax = available heap bytes */
    cmpq    %rax, %rsi              /* requested (rsi) > available (rax)? */
    ja      .mmap_fail
    movq    $_heap_start, %rax      /* success: return _heap_start */
    ret
.mmap_fail:
    movq    $-1, %rax               /* failure: return MAP_FAILED */
    ret

    /* ---- raise: return 0 (dead code, signal handler path) ----------- */
    .global raise
raise:
    xorq    %rax, %rax
    ret

    /* ---- tinygo_register_fatal_signals: noop (called but safe) ------ */
    .global tinygo_register_fatal_signals
tinygo_register_fatal_signals:
    ret

    /* ---- Heap region (4 MiB, NOBITS) -------------------------------- */
    /* Defined here as an @nobits section so the linker places it in a    */
    /* PT_LOAD segment with memsz > filesz, causing the ELF loader to    */
    /* zero-fill it. This is portable across GNU ld and ld.lld.          */
    .section .heap,"aw",@nobits
    .skip 0x400000                      /* 4 MiB heap */

    /* Mark the stack as non-executable to silence ld.lld's warning. */
    .section .note.GNU-stack,"",@progbits
```

### 5.4 `src/boot.S` (modified)

**Changes**: Call `main` instead of `kernel_main`. Pass `argc=0, argv=nil`.

```gas
/*
 * src/boot.S — Multiboot 1 header, 32->64-bit bootstrap, call into TinyGo.
 *
 * Assembled by GNU `as` into an ELF64 relocatable (mixed .code32 / .code64
 * via explicit directives), then linked together with the TinyGo-emitted
 * Go object file via src/linker.ld. AT&T syntax throughout.
 */

    .set MB_MAGIC,    0x1BADB002
    .set MB_FLAGS,    0x00000000
    .set MB_CHECKSUM, -(MB_MAGIC + MB_FLAGS)

    /* ---- Multiboot 1 header ----------------------------------------- */
    /* Must be 4-byte aligned and reside within the first 8 KiB of the   */
    /* kernel file. The linker script places .multiboot first.           */
    .section .multiboot, "a", @progbits
    .align 4
    .long MB_MAGIC
    .long MB_FLAGS
    .long MB_CHECKSUM

    /* ---- BSS: stack + page tables ----------------------------------- */
    /* Use the bare .bss directive (not .section .bss, ...). Assemblers  */
    /* are picky about re-declaring well-known ELF sections with         */
    /* explicit flags; the bareword form always works.                   */
    .bss
    .align 16
stack_bottom:
    .skip 16384                      /* 16 KiB kernel stack */
    .global stack_top
stack_top:

    .align 4096
pml4:
    .skip 4096                       /* 1 x PML4 table */
pdp:
    .skip 4096                       /* 1 x PDP  table */
pd:
    .skip 4096                       /* 1 x PD   table (512 x 2 MiB = 1 GiB) */

    /* ---- 64-bit GDT (placed in .rodata) ----------------------------- */
    .section .rodata,"a",@progbits
    .align 8
gdt64:
    .quad 0                                                          /* null descriptor    */
    .set GDT64_CODE, . - gdt64
    .quad (1 << 43) | (1 << 44) | (1 << 47) | (1 << 53)              /* 64-bit code seg    */
    .set GDT64_DATA, . - gdt64
    .quad (1 << 41) | (1 << 44) | (1 << 47)                          /* 64-bit data seg    */
gdt64_end:
    .align 8
gdt64_pointer:
    .word gdt64_end - gdt64 - 1                                      /* limit              */
    .quad gdt64                                                      /* base (64-bit addr) */

    /* ---- 32-bit entry point ----------------------------------------- */
    .text
    .code32
    .global _start
    .extern main
_start:
    /* Set up the stack (grows down). */
    movl    $stack_top, %esp
    cld

    /* PML4[0] = &pdp | PRESENT | WRITE */
    movl    $pdp, %eax
    orl     $0x03, %eax
    movl    %eax, pml4

    /* PDP[0] = &pd | PRESENT | WRITE */
    movl    $pd, %eax
    orl     $0x03, %eax
    movl    %eax, pdp

    /* Fill PD[0..511] with 2 MiB huge pages identity-mapping 0..1 GiB.
     * Each entry: (i * 0x200000) | PRESENT | WRITE | HUGE (= 0x83).
     * Page-table entries are 8 bytes. We explicitly write both halves
     * rather than relying on the loader to zero .bss, so the code works
     * even if pd is ever moved to .data or a zero-fill is skipped.     */
    xorl    %ecx, %ecx
1:
    movl    %ecx, %eax
    shll    $21, %eax                /* eax = i * 2 MiB (2^21 = 0x200000) */
    orl     $0x83, %eax              /* PRESENT | WRITE | PAGE_SIZE       */
    movl    %eax, pd(, %ecx, 8)      /* low 32 bits of entry i            */
    movl    $0,   pd+4(, %ecx, 8)    /* high 32 bits -- explicitly zero   */
    incl    %ecx
    cmpl    $512, %ecx
    jne     1b

    /* CR3 = physical address of PML4 */
    movl    $pml4, %eax
    movl    %eax, %cr3

    /* CR4.PAE (bit 5) = 1 */
    movl    %cr4, %eax
    orl     $(1 << 5), %eax
    movl    %eax, %cr4

    /* EFER.LME (bit 8) = 1 via MSR 0xC0000080 */
    movl    $0xC0000080, %ecx
    rdmsr
    orl     $(1 << 8), %eax
    wrmsr

    /* CR0.PG (bit 31) = 1 -- paging on, CPU enters compatibility mode. */
    movl    %cr0, %eax
    orl     $(1 << 31), %eax
    movl    %eax, %cr0

    /* Load the 64-bit GDT and far-jump to a 64-bit code segment. */
    lgdt    gdt64_pointer
    ljmp    $GDT64_CODE, $long_mode_start

    /* ---- 64-bit code ------------------------------------------------ */
    .code64
long_mode_start:
    /* Reload segment registers with the 64-bit data selector. */
    movw    $GDT64_DATA, %ax
    movw    %ax, %ds
    movw    %ax, %es
    movw    %ax, %fs
    movw    %ax, %gs
    movw    %ax, %ss

    /* Call TinyGo's runtime entry point (main from runtime_unix.go).
     * Signature: main(argc int32, argv *unsafe.Pointer) int
     * Pass argc=0, argv=nil since this is a bare-metal kernel. */
    xorl    %edi, %edi              /* argc = 0 */
    xorl    %esi, %esi              /* argv = nil (zero-extended to 64-bit) */
    call    main

    /* If main ever returns, disable interrupts and halt forever. */
hang:
    cli
    hlt
    jmp     hang

    /* Mark the stack as non-executable to silence linker warnings. */
    .section .note.GNU-stack,"",@progbits
```

**Changes from hello-world version**:
- `.extern kernel_main` → `.extern main`
- `call kernel_main` → `xorl %edi, %edi; xorl %esi, %esi; call main`
- Added `.global stack_top` so the linker script can reference it via `_stack_top = stack_top`.

### 5.5 `src/main.go` (rewritten)

**Changes**: Remove `//export kernel_main`. Move demo code to `func main()`. Exercise heap allocation.

```go
// src/main.go — TinyGo kernel entry point with heap-allocation demo.
//
// With gc="leaking" in target.json, TinyGo's runtime initializes the heap
// via preinit() -> mmap -> initHeap() before calling this main(). Dynamic
// allocation (make, append, new, string +) now works.

package main

import "unsafe"

const (
	vgaAddr   = uintptr(0xB8000)
	vgaWidth  = 80
	vgaHeight = 25
	vgaCells  = vgaWidth * vgaHeight
	colorAttr = uint16(0x0F00) // bright white on black
)

// vgaWriteLine writes a string to the given row of the VGA text buffer.
func vgaWriteLine(row int, s string) {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	offset := row * vgaWidth
	for i := 0; i < len(s) && offset+i < vgaCells; i++ {
		vga[offset+i] = uint16(s[i]) | colorAttr
	}
}

// vgaClear fills the entire VGA text buffer with spaces.
func vgaClear() {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	for i := 0; i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}
}

// makeGreeting builds a greeting string via heap-allocating concatenation.
// The //go:noinline directive prevents LLVM from optimizing the heap
// allocations into stack allocations via escape analysis.
//
//go:noinline
func makeGreeting() string {
	a := "Hello"
	b := ", "
	c := "Heap"
	d := "!"
	return a + b + c + d
}

// makeMessage builds a message via make() + append(), proving that slice
// growth (which heap-allocates a new backing array) works. The initial
// capacity is intentionally small (2) so that the first append of "Heap"
// (4 bytes) exceeds it and forces a heap-allocated grow.
//
//go:noinline
func makeMessage() string {
	buf := make([]byte, 0, 2)
	buf = append(buf, "Heap"...)
	buf = append(buf, ' ')
	buf = append(buf, "works!"...)
	return string(buf)
}

// allocateUint64 uses new() to heap-allocate a uint64 and returns the
// pointer. Returning the pointer forces it to escape, ensuring the
// compiler cannot stack-allocate it.
//
//go:noinline
func allocateUint64() *uint64 {
	p := new(uint64)
	*p = 42
	return p
}

func main() {
	vgaClear()

	// Line 0: string concatenation (heap-allocated intermediate strings)
	greeting := makeGreeting()
	vgaWriteLine(0, greeting)

	// Line 1: make() + append() (heap-allocated slice backing array)
	msg := makeMessage()
	vgaWriteLine(1, msg)

	// Line 2: new() (heap-allocated pointer — returned to force escape)
	p := allocateUint64()
	if *p == 42 {
		vgaWriteLine(2, "new(uint64) = 42")
	}
}
```

**Notes**:
- `//go:noinline` on `makeGreeting()` and `makeMessage()` prevents inlining, keeping the demo code self-contained. The heap allocations within these functions happen inside TinyGo runtime calls (`runtime.stringConcat`, `runtime.stringFromBytes`) that always call `alloc()`, so they are not affected by escape analysis. `allocateUint64()` returns a `*uint64` (pointer, not value) to force the allocation to escape — without this, escape analysis could stack-allocate `new(uint64)` since the pointer wouldn't leave the function.
- The `main()` function is TinyGo's user entry point, called via `runtime_unix.go:main()` → `run()` → `callMain()` → `main.main()`. No `//export` needed.
- The old `//export kernel_main` function is removed entirely. `boot.S` now calls TinyGo's runtime `main` directly.

### 5.6 `Makefile` (no changes required)

The existing Makefile works as-is. The two-step build (`as` → `tinygo build -o *.o` → `ld.lld`) is not affected by the gc mode change.

---

## 6. Build Process

No changes to build commands. From the repository root:

```bash
make clean
```
```bash
make build
```

Under the hood:

```bash
as --64 src/boot.S -o tmp/boot.o
as --64 src/stubs.S -o tmp/stubs.o
tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/stubs.o tmp/kernel_go.o
```

---

## 7. Verification

### 7.1 Binary-level checks

1. `make build` completes with no errors.
2. Check undefined symbols in the Go object (should be exactly the existing 5):
   ```bash
   nm tmp/kernel_go.o | grep " U " | sort -u
   ```
   Expected:
   ```
                    U abort
                    U mmap
                    U raise
                    U tinygo_register_fatal_signals
                    U write
   ```
3. No unresolved symbols in the final binary:
   ```bash
   nm tmp/kernel.bin | grep " U "
   ```
   Expected: no output.
4. Heap symbols exist:
   ```bash
   nm tmp/kernel.bin | grep _heap
   ```
   Expected: `_heap_start` and `_heap_end` at addresses after BSS.
5. Multiboot header is valid:
   ```bash
   grub-file --is-x86-multiboot tmp/kernel.bin
   ```
   Expected: exit 0.
6. Heap section visible in ELF:
   ```bash
   objdump -h tmp/kernel.bin | grep -E '\.heap|\.bss'
   ```
   Expected: `.bss` followed by `.heap`, both with `ALLOC` flag.
7. Program headers show the heap region:
   ```bash
   readelf -l tmp/kernel.bin
   ```
   Expected: a `LOAD` segment covering `.data`, `.bss`, and `.heap` with `memsz` much larger than `filesz`.

### 7.2 Runtime verification (QEMU, performed by user)

```bash
make run
```
or:
```bash
make run-kernel
```

**Expected VGA output** (3 lines, bright white on black, rest of screen cleared):

```
Hello, Heap!
Heap works!
new(uint64) = 42
```

The guest must not triple-fault or reboot. The `-no-reboot -no-shutdown` flags freeze the VM on any crash.

---

## 8. Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| `runtimePanic("cannot allocate heap memory")` — QEMU shows nothing or hangs | `mmap` stub returns -1 for all sizes, including those ≤ 4 MiB. | Check the `mmap` stub's size comparison logic. Ensure `_heap_start` and `_heap_end` are defined in linker script. Run `nm tmp/kernel.bin | grep _heap` to verify they have the correct values. |
| Triple fault immediately after boot | `main` symbol not found, or argument-passing convention mismatch. | Check `nm tmp/kernel_go.o | grep " T main"` — the symbol must exist. Verify boot.S passes `edi=0, esi=0` before `call main`. |
| `Hello, World!` still appears (old demo) | `main.go` still has `//export kernel_main` and boot.S still calls `kernel_main`. | Remove the `//export kernel_main` function. Change boot.S to `call main`. |
| VGA shows "Hello, Heap!" but lines 1–2 are missing | `makeMessage()` or `allocateUint64()` crash silently (ud2 from panic trap). The heap may not be initialized. | Check that `preinit()` runs: add a VGA write at the very start of `main()` before any allocation. If that shows, the issue is in alloc. Check `mmap` return value. |
| Linker error: `undefined symbol: _heap_start` | `.heap` section missing from `linker.ld`, or the `@nobits` `.heap` section not defined in `stubs.S`. | Verify `linker.ld` has the `.heap` output section with `_heap_start = .; *(.heap); _heap_end = .;`. Verify `stubs.S` has `.section .heap,"aw",@nobits` followed by `.skip 0x400000`. |
| Linker error: `undefined symbol: main` | TinyGo object doesn't export `main`. | Verify gc is "leaking" (not "none"). With gc="none", `runtime_unix.go:main()` might be dead-code-eliminated. Check `nm tmp/kernel_go.o | grep " T main"`. |
| Allocated values contain garbage (not zero) | `.heap` section not zero-initialized by the loader. | Verify `.heap` is `@nobits` (check `readelf -S tmp/kernel.bin`) and is within a PT_LOAD segment (check `readelf -l`). The ELF loader zeros `memsz - filesz` for LOAD segments. As a fallback, add a zeroing loop in boot.S before `call main`. |
| `out of memory` panic (ud2) during allocation | Heap is full. The 4 MiB bump allocator ran out of space. | Increase the heap size in linker.ld (change `0x400000` to `0x1000000` for 16 MiB). |
| `grub-file --is-x86-multiboot` fails | Adding `.heap` pushed `.multiboot` beyond the first 8 KiB of the file. | This should not happen since `.heap` is after `.bss` (at the end). Verify with `objdump -h` that `.multiboot` still has `File off < 0x2000`. |
| New undefined symbol: `memcpy` | `realloc()` in gc_leaking.go references `memcpy`. Appears if TinyGo's optimizer keeps the `realloc` path. | Add a `memcpy` stub to stubs.S: `.global memcpy; memcpy: movq %rdi,%rax; movq %rdx,%rcx; rep movsb; ret` |
| New undefined symbol: `memset` | Some code path references `memset`. | Add a `memset` stub: `.global memset; memset: movq %rdi,%r8; movb %sil,%al; movq %rdx,%rcx; rep stosb; movq %r8,%rax; ret` |

---

## 9. Future Work: Upgrading to `gc: "conservative"`

The leaking GC proves heap allocation works but never reclaims memory. Upgrading to `gc: "conservative"` (mark/sweep) would require:

1. **`__ehdr_start`**: The non-baremetal `findGlobals()` in TinyGo parses the ELF program headers at `__ehdr_start` to locate `.data`/`.bss` for root scanning. With Multiboot, the ELF file header is not loaded into memory by default. Solutions:
   - Use `PHDRS` directive in the linker script to place the ELF header at a known address within a LOAD segment.
   - Or add a custom `findGlobals()` that uses `_globals_start`/`_globals_end` (already defined in our linker script) and somehow override the default.

2. **`tinygo_scanCurrentStack`**: An assembly trampoline that pushes all callee-saved registers onto the stack, then calls `tinygo_scanstack(sp)`. The implementation is in TinyGo's `src/runtime/asm_amd64.S`. Copy it into our `stubs.S` or a new `gc_asm.S`.

3. **`memset` and `memcpy`**: Both would be required. Stubbing with `rep stosb`/`rep movsb` is sufficient.

4. **Stack scanning**: `stackTop` is set by `runtime_unix.go:main()` via `getCurrentStackPointer()` (LLVM intrinsic). This already works with our boot flow change.

---

## 10. References

- `impldoc/helloworld_cgo_design.md` — hello-world milestone design (boot flow, memory layout, GDT, page tables).
- `impldoc/helloworld_cgo_implementation_report.md` — deviations discovered during hello-world implementation.
- TinyGo source: `/usr/local/lib/tinygo/src/runtime/gc_leaking.go`, `runtime_unix.go`, `scheduler_none.go`, `zero_new_alloc_noop.go`.
- OSDev Wiki: "Higher Half Kernel", "Page Frame Allocation" — consult when extending beyond the leaking GC.
