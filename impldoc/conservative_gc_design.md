# Conservative GC (Mark/Sweep) — Detailed Implementation Design

> **Audience**: Claude Code session or human engineer. Every file path is relative to the repository root. Code blocks are copy-pasteable. Follow sections in order.
>
> **Prerequisite**: The leaking GC milestone must be working (`gc: "leaking"`, heap demo). See `impldoc/heap_gc_design.md`.

---

## 1. Overview & Goals

Upgrade from `gc: "leaking"` (bump-allocate, never free) to `gc: "conservative"` (mark/sweep with real memory reclamation):

1. Switch `target.json` from `"gc": "leaking"` to `"gc": "conservative"`.
2. Add `tinygo_scanCurrentStack` assembly trampoline for GC stack scanning.
3. Add `memset` stub (required by the block-based allocator).
4. Provide a **synthetic ELF header** so `findGlobals()` can scan `.data`/`.bss` for root pointers.
5. Write a VGA demo that allocates many objects, triggers GC, and displays reclamation statistics.

### Out of Scope

- Goroutines / scheduler (keep `"scheduler": "none"`).
- Interrupts, IDT, keyboard, serial, timer.
- Virtual memory beyond the existing 1 GiB identity map.
- Restructuring `.bss` to separate boot infrastructure from runtime globals (accept conservative false positives).

### Success Criteria

- `make build` succeeds with no linker errors.
- VGA output shows GC statistics proving memory was reclaimed:
  ```
  Conservative GC Demo
  Mallocs: NNN  TotalAlloc: NNN
  GC done. Frees: NNN  HeapInuse: NNN
  Post-GC alloc OK - GC works!
  ```
- `runtime.ReadMemStats()` shows `Frees > 0` after `runtime.GC()`.
- No triple-fault; kernel halts cleanly.

---

## 2. Empirical Investigation Results

### 2.1 New undefined symbols with `gc: "conservative"`

Building with `gc: "conservative"` produces **3 new** undefined symbols beyond the leaking GC's set:

| Symbol | Status |
|---|---|
| `abort` | existing stub |
| `memcpy` | existing stub |
| `memmove` | existing stub |
| `mmap` | existing stub (functional) |
| `raise` | existing stub |
| `tinygo_register_fatal_signals` | existing stub |
| `write` | existing stub |
| **`__ehdr_start`** | **NEW** — auto-provided by ld.lld, but points to unmapped memory (see §3.1) |
| **`memset`** | **NEW** — needed by `runtime.alloc()` and `runtime.startMark()` |
| **`tinygo_scanCurrentStack`** | **NEW** — assembly trampoline for register/stack scanning |

Linking with just `memset` and `tinygo_scanCurrentStack` added succeeds (zero linker errors). `__ehdr_start` is auto-provided by ld.lld.

### 2.2 Runtime risk: `__ehdr_start` points to garbage

ld.lld computes `__ehdr_start` as the virtual address corresponding to file offset 0 of the ELF binary. For our kernel (first LOAD segment at file offset `0x158`, VMA `0x100000`), this gives `__ehdr_start = 0x100000 - 0x158 = 0x0FFEA8`.

GRUB's Multiboot loader loads only PT_LOAD segments — the ELF file header at offset 0 is **not** loaded. Address `0x0FFEA8` contains whatever was in RAM before boot (BIOS data, zeros, garbage). When `findGlobals()` reads the ELF header fields at this address, the kernel would either triple-fault or silently skip all globals scanning (leaving heap objects unreachable → all freed by GC → crash on next access).

**This is a runtime crash waiting to happen, even though the link succeeds.**

### 2.3 GC call chain (with `scheduler: "none"`)

```
runtime.GC()
  └── runGC()
        ├── markStack()
        │     └── scanCurrentStack()               [asm trampoline]
        │           ├── push rbx, rbp, r12-r15      [expose registers to scanner]
        │           └── tinygo_scanstack(rsp)        [Go function]
        │                 └── markRoots(rsp, stackTop)
        ├── findGlobals(markRoots)                   [reads __ehdr_start → our synthetic header]
        │     └── markRoots(_globals_start, _globals_end)
        ├── finishMark()
        └── sweep()                                  [frees unmarked blocks]
```

`runtime.GC()` and `runtime.ReadMemStats()` both work with `scheduler: "none"`.

---

## 3. Design Decisions

### 3.1 Synthetic ELF header to override `__ehdr_start`

Define a minimal synthetic ELF64 header + one program header in `.rodata` via assembly, and export the label as `__ehdr_start`. Because user-defined global symbols take precedence over linker auto-definitions, ld.lld uses our symbol instead of the computed one.

The synthetic program header describes exactly the globals region (`_globals_start` to `_globals_end`), so `findGlobals()` scans only `.data` + `.bss` — not the heap (which the GC manages internally).

### 3.2 False positives from page tables in `.bss`

`_globals_start` to `_globals_end` currently brackets `.data` and `.bss`. The `.bss` contains the kernel stack (16 KiB) and page tables (pml4/pdp/pd, 12 KiB). Page table entries contain values like `0x000000000010_0083` (physical address with PRESENT|WRITE|HUGE flags). If such a value happens to fall within the heap address range (`~0x109000–0x509000`), the conservative GC would treat it as a live pointer, preventing the referenced block from being freed.

**Impact**: Some heap blocks may never be freed (false retention). For a 4 MiB heap with ~500 allocations, this is negligible and does not affect correctness — the GC simply becomes slightly less effective. Acceptable for this milestone.

### 3.3 `tinygo_scanCurrentStack` from TinyGo's canonical implementation

TinyGo's `/usr/local/lib/tinygo/src/runtime/asm_amd64.S` provides the reference implementation. It:
1. Pushes all 6 callee-saved registers (rbx, rbp, r12-r15) onto the stack so the GC can scan them.
2. Aligns RSP to 16 bytes.
3. Calls `tinygo_scanstack(rsp)` (a Go function in `gc_stack_raw.go`).
4. Cleans up the stack and returns.

Registers are NOT individually popped — they were only pushed for scanning, not for preservation. A single `addq $56, %rsp` cleans up (48 bytes of registers + 8 bytes alignment).

---

## 4. File Specifications

### 4.1 `src/target.json` (modified)

**Change**: `"gc": "leaking"` → `"gc": "conservative"`

```json
{
    "llvm-target": "x86_64-unknown-linux-elf",
    "cpu": "x86-64",
    "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
    "build-tags": ["gooos"],
    "goos": "linux",
    "goarch": "amd64",
    "gc": "conservative",
    "scheduler": "none",
    "panic-strategy": "trap",
    "linker": "ld.lld",
    "rtlib": "compiler-rt"
}
```

### 4.2 `src/stubs.S` (modified)

**Changes**: Add `tinygo_scanCurrentStack`, `memset`, and synthetic ELF header with `__ehdr_start`. All other stubs remain unchanged.

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
 *   mmap                          -- runtime_unix.preinit: heap allocation (LIVE)
 *   raise                         -- tinygo_handle_fatal_signal (dead code)
 *   tinygo_register_fatal_signals -- tinygo main startup (called, noop)
 *   memcpy                        -- runtime.stringConcat, runtime.sliceAppend
 *   memmove                       -- runtime.sliceAppend (overlap-safe)
 *   memset                        -- runtime.alloc (zero new blocks), GC mark phase
 *   tinygo_scanCurrentStack       -- GC stack/register scanner trampoline
 *   __ehdr_start                  -- synthetic ELF header for findGlobals()
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

    /* ---- memcpy: simple forward byte copy ---------------------------- */
    .global memcpy
memcpy:
    /* memcpy(dest=rdi, src=rsi, n=rdx) -> rax = dest */
    movq    %rdi, %rax
    movq    %rdx, %rcx
    rep movsb
    ret

    /* ---- memmove: overlap-safe byte copy ----------------------------- */
    .global memmove
memmove:
    /* memmove(dest=rdi, src=rsi, n=rdx) -> rax = dest */
    movq    %rdi, %rax
    cmpq    %rsi, %rdi
    je      .memmove_done
    jb      .memmove_fwd
    /* dest > src: copy backward to handle overlap */
    leaq    -1(%rdi, %rdx), %rdi
    leaq    -1(%rsi, %rdx), %rsi
    movq    %rdx, %rcx
    std
    rep movsb
    cld
    ret
.memmove_fwd:
    movq    %rdx, %rcx
    rep movsb
.memmove_done:
    ret

    /* ---- memset: fill memory with a byte value ---------------------- */
    /* memset(dest=rdi, c=esi, n=rdx) -> rax = dest                     */
    .global memset
memset:
    movq    %rdi, %r8               /* save dest for return value */
    movb    %sil, %al               /* byte value to fill */
    movq    %rdx, %rcx              /* count */
    rep stosb                       /* fill [rdi..rdi+rcx) with al */
    movq    %r8, %rax               /* return original dest */
    ret

    /* ---- tinygo_scanCurrentStack: GC register/stack scanner --------- */
    /* Pushes all callee-saved registers onto the stack so the GC can    */
    /* scan them for heap pointers, then calls tinygo_scanstack(rsp).    */
    /* Canonical implementation from TinyGo's src/runtime/asm_amd64.S.  */
    .global tinygo_scanCurrentStack
tinygo_scanCurrentStack:
    pushq   %rbx
    pushq   %rbp
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $8, %rsp                /* maintain 16-byte stack alignment */
    movq    %rsp, %rdi              /* pass RSP as first arg */
    callq   tinygo_scanstack        /* Go function in gc_stack_raw.go */
    addq    $56, %rsp               /* clean up: 6 regs (48) + align (8) */
    retq

    /* ================================================================= */
    /* Synthetic ELF header for findGlobals()                            */
    /*                                                                   */
    /* os_linux.go:findGlobals() parses the ELF header at __ehdr_start   */
    /* to locate writable PT_LOAD segments for GC root scanning. GRUB    */
    /* does not load the real ELF header into memory, so we provide a    */
    /* fake header that describes exactly the globals region              */
    /* (_globals_start to _globals_end).                                 */
    /*                                                                   */
    /* Overrides ld.lld's auto-defined __ehdr_start (user-defined global */
    /* symbols take precedence over linker-generated hidden symbols).    */
    /* ================================================================= */

    .section .rodata,"a",@progbits
    .align 8
    .global __ehdr_start
__ehdr_start:
    /* --- Elf64_Ehdr (64 bytes) --- */
    .byte   0x7f, 0x45, 0x4c, 0x46  /* e_ident[0..3]: ELF magic */
    .byte   2                        /* e_ident[4]: ELFCLASS64 */
    .byte   1                        /* e_ident[5]: ELFDATA2LSB (little-endian) */
    .byte   1                        /* e_ident[6]: EV_CURRENT */
    .byte   0                        /* e_ident[7]: ELFOSABI_NONE */
    .byte   0                        /* e_ident[8]: ABI version */
    .zero   7                        /* e_ident[9..15]: padding */
    .short  2                        /* e_type: ET_EXEC */
    .short  0x3E                     /* e_machine: EM_X86_64 */
    .long   1                        /* e_version: EV_CURRENT */
    .quad   0                        /* e_entry: (unused by findGlobals) */
    .quad   64                       /* e_phoff: program headers at offset 64 (right after this header) */
    .quad   0                        /* e_shoff: (unused) */
    .long   0                        /* e_flags */
    .short  64                       /* e_ehsize: sizeof(Elf64_Ehdr) */
    .short  56                       /* e_phentsize: sizeof(Elf64_Phdr) */
    .short  1                        /* e_phnum: 1 program header */
    .short  0                        /* e_shentsize: (unused) */
    .short  0                        /* e_shnum: (unused) */
    .short  0                        /* e_shstrndx: (unused) */

    /* --- Elf64_Phdr (56 bytes) — describes the writable globals region --- */
    .long   1                        /* p_type: PT_LOAD */
    .long   6                        /* p_flags: PF_R | PF_W */
    .quad   0                        /* p_offset: (unused by findGlobals) */
    .quad   _globals_start           /* p_vaddr: start of .data+.bss */
    .quad   0                        /* p_paddr: (unused) */
    .quad   0                        /* p_filesz: (unused by findGlobals) */
    .quad   _globals_size            /* p_memsz: size of globals region */
    .quad   0                        /* p_align: (unused) */

    /* ---- Heap region (4 MiB, NOBITS) -------------------------------- */
    /* Defined here as an @nobits section so the linker places it in a    */
    /* PT_LOAD segment with memsz > filesz, causing the ELF loader to    */
    /* zero-fill it. This is portable across GNU ld and ld.lld.          */
    .section .heap,"aw",@nobits
    .skip 0x400000                      /* 4 MiB heap */

    /* Mark the stack as non-executable to silence ld.lld's warning. */
    .section .note.GNU-stack,"",@progbits
```

### 4.3 `src/linker.ld` (modified)

**Change**: Add `_globals_size` symbol (used by the synthetic ELF program header).

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
     * load-bearing — do not relax it.
     *
     * NOTE: The conservative GC scans this region for root pointers.
     * Page table entries (e.g., 0x83 flags) may cause false positives
     * (the GC treats them as live heap pointers, preventing some blocks
     * from being freed). This is harmless for correctness but slightly
     * reduces GC effectiveness. */
    .bss : ALIGN(4096)
    {
        *(COMMON)
        *(.bss .bss.*)
    }

    _globals_end = .;
    _globals_size = _globals_end - _globals_start;

    /* Heap region: 4 MiB, page-aligned. The .heap input section is defined
     * in stubs.S as @nobits, so ld.lld emits it in a PT_LOAD segment with
     * memsz > filesz. The ELF loader (GRUB or QEMU) zero-fills the
     * memsz - filesz gap, satisfying the GC's assumption that heap memory
     * is pre-zeroed.
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
     * for the GC's stack scanning (markRoots scans sp to stackTop). */
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

### 4.4 `src/main.go` (rewritten)

**Changes**: Demo that proves GC reclamation. Uses `runtime.GC()` and `runtime.ReadMemStats()`.

```go
// src/main.go — Conservative GC demo for the gooos bare-metal kernel.
//
// With gc="conservative", TinyGo's mark/sweep GC automatically reclaims
// unreachable objects. This demo allocates many objects, triggers GC, and
// displays reclamation statistics on the VGA text buffer.

package main

import (
	"runtime"
	"unsafe"
)

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

// utoa converts a uint64 to its decimal string representation.
// Implemented manually because importing strconv or fmt would pull in
// OS-dependent runtime code that does not work in bare-metal.
func utoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte // max uint64 is 20 digits
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// allocateGarbage creates a heap-allocated object and returns a pointer.
// The caller discards it, making it garbage collectible.
//
//go:noinline
func allocateGarbage() *[256]byte {
	p := new([256]byte)
	p[0] = 42
	return p
}

func main() {
	vgaClear()
	vgaWriteLine(0, "Conservative GC Demo")

	// Phase 1: Allocate many objects that immediately become garbage.
	const numAllocs = 500
	for i := 0; i < numAllocs; i++ {
		_ = allocateGarbage()
	}

	// Read stats before GC.
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	vgaWriteLine(1, "Mallocs: "+utoa(before.Mallocs)+"  TotalAlloc: "+utoa(before.TotalAlloc))

	// Phase 2: Trigger garbage collection.
	runtime.GC()

	// Read stats after GC.
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	vgaWriteLine(2, "GC done. Frees: "+utoa(after.Frees)+"  HeapInuse: "+utoa(after.HeapInuse))

	// Phase 3: Allocate again to prove memory was reclaimed.
	// If GC did not free anything, the heap would eventually fill up.
	for i := 0; i < 100; i++ {
		_ = allocateGarbage()
	}
	vgaWriteLine(3, "Post-GC alloc OK - GC works!")
}
```

**Notes**:
- `allocateGarbage()` is `//go:noinline` and returns a `*[256]byte`. The caller discards the pointer with `_`, making the object unreachable immediately. After the loop, all 500 objects are garbage.
- `runtime.GC()` triggers the mark/sweep cycle synchronously. With `scheduler: "none"`, there are no goroutines to pause.
- `runtime.ReadMemStats()` populates `Mallocs`, `Frees`, `HeapInuse`, `TotalAlloc`, etc. from the block-based allocator's counters.
- `utoa()` converts uint64 to decimal string. String concatenation with `+` heap-allocates, which is fine because the GC just freed memory.
- The Phase 3 loop (100 more allocations) would fail with "out of memory" if the GC hadn't reclaimed the Phase 1 garbage. Its success proves reclamation.
- `//go:noinline` prevents the compiler from inlining `allocateGarbage()` and stack-allocating the result.

### 4.5 `src/boot.S` (no changes)

No modifications needed. `boot.S` already calls `main(0, nil)` and exports `stack_top`.

### 4.6 `Makefile` (no changes)

The existing two-step build works as-is.

---

## 5. Build Process

```bash
make clean
```
```bash
make build
```

Under the hood (unchanged):

```bash
as --64 src/boot.S -o tmp/boot.o
as --64 src/stubs.S -o tmp/stubs.o
tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/stubs.o tmp/kernel_go.o
```

---

## 6. Verification

### 6.1 Binary-level checks

1. `make build` completes with no errors.
2. Undefined symbols in the Go object:
   ```bash
   nm tmp/kernel_go.o | grep " U " | sort -u
   ```
   Expected (10 symbols — 7 inherited + 3 new):
   ```
                    U __ehdr_start
                    U abort
                    U memcpy
                    U memmove
                    U memset
                    U mmap
                    U raise
                    U tinygo_register_fatal_signals
                    U tinygo_scanCurrentStack
                    U write
   ```
3. No unresolved symbols in the final binary:
   ```bash
   nm tmp/kernel.bin | grep " U "
   ```
   Expected: no output.
4. `__ehdr_start` is defined (our synthetic header, not linker's auto-def):
   ```bash
   nm tmp/kernel.bin | grep __ehdr_start
   ```
   Expected: an address in `.rodata` (e.g., `0x100XXX R __ehdr_start`).
5. GC-related symbols exist:
   ```bash
   nm tmp/kernel.bin | grep -E "tinygo_scan|runGC|sweep|markRoots"
   ```
   Expected: `tinygo_scanCurrentStack` (T), `tinygo_scanstack` (T), and various `runtime.*` symbols.
6. Heap and globals symbols:
   ```bash
   nm tmp/kernel.bin | grep -E "_heap_|_globals_"
   ```
   Expected: `_heap_start`, `_heap_end`, `_globals_start`, `_globals_end`, `_globals_size`.
7. Multiboot header valid:
   ```bash
   grub-file --is-x86-multiboot tmp/kernel.bin
   ```
   Expected: exit 0.
8. Synthetic ELF header in .rodata (not at the pre-kernel address):
   ```bash
   nm tmp/kernel.bin | grep __ehdr_start
   ```
   The address should be within `.rodata` (between `.text` end and `.data` start), NOT at `0x0FFEA8`.

### 6.2 Runtime verification (QEMU, performed by user)

```bash
make run
```
or:
```bash
make run-kernel
```

**Expected VGA output** (4 lines, bright white on black):

```
Conservative GC Demo
Mallocs: NNN  TotalAlloc: NNN
GC done. Frees: NNN  HeapInuse: NNN
Post-GC alloc OK - GC works!
```

Where:
- `Mallocs` should be > 500 (500 allocateGarbage calls + internal allocations from string ops)
- `Frees` should be > 0 (proves GC freed objects)
- `HeapInuse` after GC should be much less than `TotalAlloc` (proves reclamation)
- Line 3 appears (proves post-GC allocations succeed)

---

## 7. Troubleshooting

| Symptom | Likely Cause | Fix |
|---|---|---|
| Triple fault immediately after `runtime.GC()` | `findGlobals()` reading garbage at `__ehdr_start`. | Check `nm tmp/kernel.bin | grep __ehdr_start` — address must be in `.rodata` (not `0x0FFEA8`). Verify the synthetic ELF header's magic bytes with `objdump -s -j .rodata tmp/kernel.bin | head`. |
| `Frees: 0` after GC (no reclamation) | GC scans too wide a range and finds false positives keeping all objects alive. | This could happen if `_globals_size` is too large or the program header scans the heap. Check `nm tmp/kernel.bin | grep _globals` — `_globals_end` must be BEFORE `_heap_start`. |
| Linker error: `undefined symbol: tinygo_scanCurrentStack` | Missing from stubs.S. | Verify stubs.S contains the `.global tinygo_scanCurrentStack` label and the pushq/callq/addq/retq sequence. |
| Linker error: `undefined symbol: memset` | Missing from stubs.S. | Add the memset stub (see §4.2). |
| `out of memory` panic during Phase 3 | GC didn't free enough. Heap too small or too many false positives. | Increase heap from 4 MiB to 16 MiB (change `.skip 0x400000` to `.skip 0x1000000` in stubs.S and adjust nothing else — the mmap stub auto-adapts). |
| Triple fault during `tinygo_scanCurrentStack` | Stack misalignment or `tinygo_scanstack` symbol missing. | Check `nm tmp/kernel.bin | grep tinygo_scanstack` — must be a T symbol (defined by TinyGo). Verify the `subq $8, %rsp` alignment in the trampoline. |
| GC statistics show `Mallocs` = 0 | `ReadMemStats` not implemented for this GC mode. | This should not happen — `gc_blocks.go` tracks mallocs/frees. If it does, verify gc is "conservative" (not "none" or "leaking"). |
| New undefined symbol: `getpagesize` | `os_linux.go` declares it extern. Some code paths may reach it. | Add stub: `.global getpagesize; getpagesize: movl $4096, %eax; ret` |
| VGA shows only line 0, then hangs | `allocateGarbage()` loop panicked (ud2 from out-of-memory or nil pointer). | Reduce `numAllocs` from 500 to 50 to test. If that works, the heap is too small for 500 × 256-byte allocations + GC metadata. Increase heap size. |

---

## 8. Known Limitations

1. **False positives from page tables**: Page table entries in `.bss` may look like heap pointers to the conservative scanner. Some objects may never be freed. Acceptable for this milestone.
2. **No memory growth**: `growHeap()` in `runtime_unix.go` extends `heapEnd` within the mmap'd region (capped at 4 MiB by our stub). If the heap fills, `alloc()` panics. For larger workloads, increase the `.heap` region in `stubs.S`.
3. **Single-threaded GC**: With `scheduler: "none"`, the GC runs synchronously during `runtime.GC()` or when `alloc()` can't find free blocks. No concurrent marking.
4. **`utoa` allocates**: The `utoa()` function allocates a string on the heap for each call. This adds to the allocation count. Not a problem since GC is active.

---

## 9. Future Work

- **Reduce false positives**: Separate boot infrastructure (stack, page tables) from runtime globals in the linker script (`.bss.boot` vs `.bss`).
- **Precise GC** (`gc: "precise"`): Uses compiler-generated layout bitmaps for exact pointer identification. Eliminates false positives entirely but requires additional infrastructure.
- **Larger heap**: Increase from 4 MiB to match QEMU's RAM (e.g., 64 MiB or more).
- **Goroutines**: Enable `scheduler` for concurrent GC and multi-tasking.

---

## 10. References

- `impldoc/heap_gc_design.md` — leaking GC milestone design (heap region, mmap stub, boot flow).
- `impldoc/conservetive_gc_desing_guide.md` — reference guide with Path B (conservative GC stubs).
- TinyGo source: `/usr/local/lib/tinygo/src/runtime/gc_blocks.go`, `gc_conservative.go`, `gc_stack_raw.go`, `asm_amd64.S`, `os_linux.go`.
