# Hello World Kernel — Detailed Implementation Design (TinyGo + Assembly, x86_64)

> **Audience**: This document is written for a Claude Code session (or a human engineer) who will execute it verbatim. Every file path is relative to the repository root directory. Every code block is intended to be copy-pasteable as-is. Follow the sections in order.

---

## 1. Overview & Goals

Build a minimal bootable x86_64 "kernel" that:

1. Is loaded by **GRUB** via **Multiboot 1** at physical address `0x100000` (1 MiB).
2. Executes a short assembly bootstrap (`boot.S`) that:
   - Sets up a stack.
   - Builds a 4-level page table that identity-maps the first 1 GiB using 2 MiB huge pages.
   - Enables PAE, Long Mode (EFER.LME), and paging (CR0.PG).
   - Loads a 64-bit GDT and far-jumps into 64-bit code.
   - Calls a Go-defined function named `kernel_main`.
3. Runs `kernel_main`, implemented in **TinyGo**, which writes `Hello, World!` to the VGA text buffer at physical address `0xB8000` and returns.
4. On return, the assembly disables interrupts and halts the CPU forever.

### Out of Scope (explicitly, for this milestone)

- No heap, no garbage collection (deferred to a future milestone).
- No IDT / interrupt handling.
- No keyboard, serial, or timer drivers.
- No ACPI, SMP, or userspace.
- No filesystem.
- No cgo (`import "C"`). Go ↔ assembly interop uses **TinyGo's `//export` directive only**, which exposes a Go function as a C-ABI symbol that assembly can `call`.

### Success Criteria

- `qemu-system-x86_64 -cdrom tmp/kernel.iso` shows `Hello, World!` at the top-left of the VGA screen in white-on-black.
- `grub-file --is-x86-multiboot tmp/kernel.bin` exits with status 0.
- The guest does not triple-fault (no reboot loop when `-no-reboot` is passed).

---

## 2. Target Environment

| Item | Value |
| --- | --- |
| CPU architecture | x86_64 (IA-32e Long Mode) |
| Boot protocol | Multiboot 1 (magic `0x1BADB002`) |
| Bootloader | GRUB 2 (`grub-mkrescue`) |
| Load address | `0x100000` (1 MiB) |
| Host OS | WSL2 Ubuntu 24.04 |
| Emulator | QEMU (`qemu-system-x86_64`) |
| Go toolchain | TinyGo (pinned known-good: `v0.33.0` or later) |
| Assembler | GNU `as` via `clang` (invoked by TinyGo through `extra-files`) |
| Linker | `ld.lld` (invoked by TinyGo) |

---

## 3. Architecture / Boot Flow

```
+--------+  power-on
|  BIOS  | ---------------+
+--------+                |
                          v
                     +---------+   loads kernel.bin (ELF, multiboot1)
                     |  GRUB   | -----------------------------------+
                     +---------+                                    |
                                                                    v
                                          +-----------------------------+
                                          |  _start  (boot.S, .code32)  |
                                          |  - set up stack             |
                                          |  - build PML4/PDP/PD        |
                                          |  - CR3 = &pml4              |
                                          |  - CR4.PAE = 1              |
                                          |  - EFER.LME = 1             |
                                          |  - CR0.PG  = 1              |
                                          |  - lgdt gdt64_pointer       |
                                          |  - ljmp $code64_sel, ...    |
                                          +--------------+--------------+
                                                         |
                                                         v
                                    +--------------------------------------+
                                    |  long_mode_start  (boot.S, .code64)  |
                                    |  - reload DS/ES/FS/GS/SS             |
                                    |  - call kernel_main                  |
                                    +--------------------+-----------------+
                                                         |
                                                         v
                                          +-----------------------------+
                                          |  kernel_main  (main.go,     |
                                          |   TinyGo, //export)         |
                                          |  - clear VGA text buffer    |
                                          |  - write "Hello, World!"    |
                                          +--------------+--------------+
                                                         |
                                                (returns to boot.S)
                                                         |
                                                         v
                                          +-----------------------------+
                                          |  hang:  cli ; hlt ; jmp .   |
                                          +-----------------------------+
```

---

## 4. Memory Layout

All addresses are **physical** (identity-mapped during the hello-world milestone).

| Region | Start | Size | Notes |
| --- | --- | --- | --- |
| BIOS / BDA | `0x00000000` | 1 KiB | untouched |
| Conventional RAM | `0x00000500` | ~639 KiB | untouched |
| VGA text buffer | `0x000B8000` | 4 KiB | written by `kernel_main` |
| Kernel load base | `0x00100000` | — | set by linker script |
| `.multiboot` | `0x00100000`+ | 12 B + pad | must be in first 8 KiB of the **file** |
| `.text` (boot + Go) | after `.multiboot` | varies | `.code32` bootstrap, `.code64` long-mode, Go code |
| `.rodata` | after `.text` | varies | GDT, Go string literals |
| `.data` | after `.rodata` | varies | usually empty for this milestone |
| `.bss` | after `.data` | ~16 KiB + 3×4 KiB + padding | stack + PML4 + PDP + PD; SHT_NOBITS, zeroed by both GRUB's and QEMU's ELF loaders per the ELF PT_LOAD spec |
| Page tables | inside `.bss` | 3×4 KiB | `pml4`, `pdp`, `pd`, each 4 KiB-aligned |
| Stack | inside `.bss` | 16 KiB | top = `stack_top` |
| Identity-mapped VA | `0x00000000`–`0x40000000` | 1 GiB | 512 × 2 MiB huge pages via `pd[0..511]` |

---

## 5. File Tree (to be created)

```
gooos/
├── CLAUDE.md                         (existing)
├── impldoc/
│   └── helloworld_cgo_design.md      (this document)
├── src/
│   ├── boot.S                        (NEW — multiboot header + 32→64 bootstrap)
│   ├── main.go                       (NEW — TinyGo kernel_main)
│   ├── linker.ld                     (NEW — section layout, load base 0x100000)
│   └── target.json                   (NEW — TinyGo bare-metal target)
├── grub/
│   └── grub.cfg                      (NEW — single menuentry)
├── Makefile                          (NEW — build / iso / run / clean)
└── tmp/                              (scratch: boot.o, kernel.bin, kernel.iso, isodir/)
```

---

## 6. File Specifications (full source, copy-pasteable)

### 6.1 `src/boot.S`

**Purpose**: First instructions the CPU executes after GRUB hands off control. Provides the Multiboot 1 header so GRUB recognizes the kernel, sets up a stack, builds page tables, switches to Long Mode, and calls the Go-defined `kernel_main`.

**Syntax**: GNU assembler (GAS), AT&T syntax. Mixed `.code32` and `.code64` sections in a single file. Clang's integrated assembler (invoked by TinyGo via `extra-files`) processes this file.

```gas
/*
 * src/boot.S — Multiboot 1 header, 32→64-bit bootstrap, call into TinyGo.
 *
 * Assembled by clang's integrated assembler (invoked from TinyGo via
 * target.json's "extra-files"). AT&T syntax throughout. Section layout is
 * controlled by src/linker.ld.
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
    /* Use the bare .bss directive (not .section .bss, ...). Clang's    */
    /* integrated assembler is picky about re-declaring well-known ELF   */
    /* sections with explicit flags; the bareword form always works.     */
    .bss
    .align 16
stack_bottom:
    .skip 16384                      /* 16 KiB kernel stack */
stack_top:

    .align 4096
pml4:
    .skip 4096                       /* 1 × PML4 table */
pdp:
    .skip 4096                       /* 1 × PDP  table */
pd:
    .skip 4096                       /* 1 × PD   table (512 × 2 MiB = 1 GiB) */

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
    .extern kernel_main
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
    movl    $0,   pd+4(, %ecx, 8)    /* high 32 bits — explicitly zero    */
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

    /* CR0.PG (bit 31) = 1 — paging on, CPU enters compatibility mode.  */
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

    /* Call the Go-defined entry point (exposed via //export kernel_main). */
    call    kernel_main

    /* If kernel_main ever returns, disable interrupts and halt forever. */
hang:
    cli
    hlt
    jmp     hang
```

**Notes**:
- The multiboot header's three fields (`magic`, `flags`, `checksum`) total 12 bytes. With `.align 4` and the `KEEP` directive in the linker script, it is forced to the start of the loadable image.
- `(1 << 43) | (1 << 44) | (1 << 47) | (1 << 53)` = code/non-system/present/64-bit code — the minimal set for a long-mode code segment per the canonical OSDev long-mode GDT.
- `(1 << 41) | (1 << 44) | (1 << 47)` = writable/non-system/present — minimal data segment.
- The PD-fill loop uses `shll $21, %eax` because `2^21 = 0x200000 = 2 MiB`. Both halves of each 8-byte entry are written explicitly, so the loop is correct even if `.bss` is not pre-zeroed.
- The EFER update uses `rdmsr` → `or` → `wrmsr`: `rdmsr` reads MSR `0xC0000080` into `EDX:EAX`, the `orl $(1<<8), %eax` sets LME in the low dword, and `wrmsr` writes `EDX:EAX` back. `EDX` is preserved between the two instructions, so the high bits of EFER (NXE, reserved) are not corrupted.
- **Stack-alignment invariant**: the SysV AMD64 ABI requires `RSP % 16 == 0` just before a `call`, so at function entry `RSP % 16 == 8`. Our stack satisfies this: `stack_top` follows `.skip 16384` after `.align 16`, so `stack_top` is 16-byte aligned; `call kernel_main` pushes 8 bytes of return address, so `kernel_main` sees `RSP % 16 == 8`. Do not add ad-hoc `push`/`sub` between `long_mode_start` and `call kernel_main` without maintaining this invariant.

---

### 6.2 `src/main.go`

**Purpose**: The Go-level kernel entry point. Exposed as the C-ABI symbol `kernel_main` via TinyGo's `//export` directive. Writes "Hello, World!" directly to the VGA text buffer and returns.

```go
// src/main.go — TinyGo kernel_main that writes to the VGA text buffer.
//
// Compiled by TinyGo against src/target.json. The //export directive below
// makes kernel_main appear to the linker as a C-ABI symbol that boot.S can
// reach via `call kernel_main`.

package main

import "unsafe"

// colorAttr is the VGA character attribute byte in the high byte of each
// 16-bit cell. 0x0F = bright white foreground on black background.
const colorAttr uint16 = 0x0F00

const (
	vgaWidth  = 80
	vgaHeight = 25
	vgaCells  = vgaWidth * vgaHeight
)

//export kernel_main
func kernel_main() {
	// Treat the VGA text buffer as a fixed-size array of 16-bit cells.
	vga := (*[vgaCells]uint16)(unsafe.Pointer(uintptr(0xB8000)))

	// Clear the screen (space characters with our attribute).
	for i := 0; i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}

	// Write the greeting at the top-left corner.
	msg := "Hello, World!"
	for i := 0; i < len(msg); i++ {
		vga[i] = uint16(msg[i]) | colorAttr
	}
}

// TinyGo requires a package-level main() to exist, but in bare-metal mode
// with scheduler="none" and gc="none" it is never called.
func main() {}
```

**Notes**:
- `unsafe.Pointer(uintptr(0xB8000))` is the standard TinyGo idiom for a fixed-address MMIO pointer. The fixed-size array type `*[vgaCells]uint16` gives us bounds-checked indexing.
- We do **not** import any package besides `unsafe`. Standard library packages like `fmt` pull in the Go runtime and will not link with `gc="none"` / `scheduler="none"`.
- The attribute byte `0x0F` (high byte) means **bright white on black**. To change colors, edit `colorAttr`.
- **The `//export kernel_main` directive MUST sit immediately above `func kernel_main()` with NO blank line between them.** If a blank line slips in, TinyGo silently treats the directive as a normal comment and the symbol is **not** exported — the subsequent link fails with `undefined reference to kernel_main`. This is the single most common footgun with `//export`.
- Do not add any `import "C"` — that is standard cgo, which pulls in the Go runtime and will not link in a freestanding environment. `//export` in a bare-metal TinyGo target is a separate mechanism that works without cgo.

---

### 6.3 `src/linker.ld`

**Purpose**: Tell the linker to place the kernel at load address `0x100000`, to put `.multiboot` first (so the Multiboot 1 header is within the first 8 KiB of the file), and to lay out `.text`/`.rodata`/`.data`/`.bss` in a conventional order.

```ld
/* src/linker.ld — minimal kernel layout. Load address = 1 MiB. */

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

    .data : ALIGN(16)
    {
        *(.data .data.*)
    }

    /* .bss holds the stack *and* the 4 KiB-aligned page tables (pml4,    */
    /* pdp, pd). CR3 requires 4 KiB alignment on PML4, so ALIGN(4096) is  */
    /* load-bearing — do not relax it.                                    */
    .bss : ALIGN(4096)
    {
        *(COMMON)
        *(.bss .bss.*)
    }

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
- `ENTRY(_start)` makes the ELF entry point match the `_start` label in `boot.S`. GRUB / QEMU's Multiboot loader ignores this for Multiboot 1 (it uses the `entry_addr` from the header if set, else the ELF entry), but having it correct avoids surprises when booting with `qemu-system-x86_64 -kernel`.
- `.eh_frame`, `.note.*`, and `.comment` are discarded to keep the binary compact and to ensure nothing non-loadable gets placed before `.multiboot` in the file layout.

---

### 6.4 `src/target.json`

**Purpose**: TinyGo target definition for a bare-metal x86_64 kernel. Tells TinyGo to:
- target an x86_64 ELF (via the `linux` triple for compiler-rt availability — see notes),
- disable SSE/MMX/AVX so the compiler does not emit vector instructions (SSE would fault until `CR4.OSFXSR` is set, which this milestone does not do),
- skip GC and scheduler,
- invoke `ld.lld` with our linker script,
- include `boot.S` as an extra file so it is assembled and linked alongside the Go code.

```json
{
    "llvm-target": "x86_64-unknown-linux-elf",
    "cpu": "x86-64",
    "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
    "build-tags": ["baremetal", "gooos"],
    "goos": "linux",
    "goarch": "amd64",
    "gc": "none",
    "scheduler": "none",
    "panic-strategy": "trap",
    "linker": "ld.lld",
    "rtlib": "compiler-rt",
    "linkerscript": "linker.ld",
    "extra-files": ["boot.S"]
}
```

**Notes**:
- `"linkerscript"` and `"extra-files"` paths are resolved **relative to `target.json`**, so the file names above assume `target.json`, `linker.ld`, and `boot.S` all live in `src/`.
- **`llvm-target` is `x86_64-unknown-linux-elf`, not `x86_64-unknown-none-elf`.** This looks wrong for a bare-metal kernel, but it is deliberate: TinyGo ships a prebuilt `compiler-rt` (which provides `memset`/`memcpy`/`memmove`) only for the targets it bundles, and `x86_64-unknown-linux-*` is one of them while `x86_64-unknown-none-elf` is not. The target triple affects only LLVM codegen and runtime-library selection; the actual CPU never runs Linux. We override every hosted assumption via `gc`, `scheduler`, `panic-strategy`, our own linker script, and the 32→64 bootstrap in `boot.S`.
- `"goos": "linux"` and `"goarch": "amd64"` are required by TinyGo to select the right parts of the Go standard library; the hosted runtime is replaced by the bare-metal settings below them.
- The `features` string disables every SSE/MMX/AVX family. This forces LLVM to avoid emitting vector instructions in generated code. Bare-metal code cannot use SSE until `CR4.OSFXSR` / `CR4.OSXMMEXCPT` are set and an `FXSAVE` save area is provided, which this milestone intentionally does **not** do. `kernel_main` uses no floating-point at all, so the feature flags are belt-and-braces against TinyGo runtime glue accidentally emitting `movaps`/`xorps`/etc.
- Do **not** add `+soft-float`. LLVM's x86 backend does not implement a soft-float mode; the feature name is silently ignored or warned about. On x86 with SSE disabled, LLVM falls back to x87 for any FP that sneaks in — which needs no setup (CR0.EM=0, CR0.MP=1 are the defaults).
- If TinyGo errors with `unknown target option "linkerscript"` or `"extra-files"`, update TinyGo to ≥ 0.30.0.

---

### 6.5 `grub/grub.cfg`

**Purpose**: Minimal GRUB configuration embedded in the ISO. A single menu entry that `multiboot`-loads `/boot/kernel.bin` and boots it immediately.

```grub
set timeout=0
set default=0

menuentry "gooos: Hello World" {
    multiboot /boot/kernel.bin
    boot
}
```

**Notes**:
- `multiboot` (not `multiboot2`) because our header uses the Multiboot 1 magic `0x1BADB002`.
- `timeout=0` skips the menu entirely so QEMU boots straight into the kernel.

---

### 6.6 `Makefile`

**Purpose**: One-command build, ISO assembly, and QEMU run. Each recipe line is a single shell command (no `&&`, no pipes, no subshells).

```makefile
# gooos/Makefile
# Targets: build (default), iso, run, run-kernel, clean, check-multiboot

TINYGO ?= tinygo
QEMU   ?= qemu-system-x86_64

SRC_DIR := src
GRUB_DIR := grub
TMP_DIR := tmp
ISO_DIR := $(TMP_DIR)/isodir

TARGET_JSON := $(SRC_DIR)/target.json
KERNEL_BIN  := $(TMP_DIR)/kernel.bin
KERNEL_ISO  := $(TMP_DIR)/kernel.iso

SRCS := $(SRC_DIR)/boot.S $(SRC_DIR)/main.go $(SRC_DIR)/linker.ld $(SRC_DIR)/target.json

.PHONY: all build iso run run-kernel clean check-multiboot

all: build

build: $(KERNEL_BIN)

$(TMP_DIR):
	mkdir -p $(TMP_DIR)

$(KERNEL_BIN): $(SRCS) | $(TMP_DIR)
	$(TINYGO) build -target=$(TARGET_JSON) -o $(KERNEL_BIN) ./$(SRC_DIR)

check-multiboot: $(KERNEL_BIN)
	grub-file --is-x86-multiboot $(KERNEL_BIN)

iso: $(KERNEL_ISO)

$(KERNEL_ISO): $(KERNEL_BIN) $(GRUB_DIR)/grub.cfg
	rm -rf $(ISO_DIR)
	mkdir -p $(ISO_DIR)/boot/grub
	cp $(KERNEL_BIN) $(ISO_DIR)/boot/kernel.bin
	cp $(GRUB_DIR)/grub.cfg $(ISO_DIR)/boot/grub/grub.cfg
	grub-mkrescue -o $(KERNEL_ISO) $(ISO_DIR)

run: $(KERNEL_ISO) check-multiboot
	$(QEMU) -cdrom $(KERNEL_ISO) -serial stdio -no-reboot -no-shutdown

run-kernel: $(KERNEL_BIN) check-multiboot
	$(QEMU) -kernel $(KERNEL_BIN) -serial stdio -no-reboot -no-shutdown

clean:
	rm -rf $(TMP_DIR)
```

---

## 7. Build Process (manual equivalents of the Makefile)

> `CLAUDE.md` requires one shell command per invocation. The list below respects that: each line is a **single** command with no `&&`, `;`, `|`, or `$(...)` compounding.

Run all commands from the repository root directory.

### 7.0 Smoke-test `extra-files` with a stub `boot.S` FIRST

The most load-bearing assumption in this design is that TinyGo's `"extra-files"` target field compiles `.S` files by dispatching them to clang's integrated assembler. If your TinyGo version does not do this, the entire single-step build collapses and you must use the fallback in section 11.

Before you write the full `boot.S`, drop a one-line stub to confirm the path works:

```bash
mkdir -p src
```
Create a temporary `src/boot.S` containing **only**:
```gas
    .text
    .global _start
_start:
    hlt
```
And a temporary minimal `src/main.go`:
```go
package main

//export kernel_main
func kernel_main() {}

func main() {}
```
Then run the build:
```bash
tinygo build -target=src/target.json -o tmp/kernel.bin ./src
```

If this succeeds and produces `tmp/kernel.bin`, the single-step build path works — proceed to section 7.1 and replace the stub files with the real ones from section 6. If it fails with errors like `error: unknown file extension '.S'` or `clang: error: cannot assemble`, switch to the two-step fallback (section 11, "TinyGo rejects .S in extra-files"). Do not attempt to debug the full build until the smoke test passes.

### 7.1 Full build

1. Create the scratch directory:
   ```bash
   mkdir -p tmp
   ```
2. Build the kernel binary (TinyGo compiles `main.go`, assembles `boot.S` via clang, and links with `linker.ld` in one step):
   ```bash
   tinygo build -target=src/target.json -o tmp/kernel.bin ./src
   ```
3. Sanity-check that the resulting binary contains a valid Multiboot 1 header:
   ```bash
   grub-file --is-x86-multiboot tmp/kernel.bin
   ```
   Exit status 0 = OK. Any other exit status means the header is missing or beyond the first 8 KiB — see the troubleshooting section.
4. Inspect section layout (optional but recommended for first-time builds):
   ```bash
   objdump -h tmp/kernel.bin
   ```
   `.multiboot` must be the first allocated section at VMA `0x100000`.
5. Prepare the ISO staging directory:
   ```bash
   rm -rf tmp/isodir
   ```
   ```bash
   mkdir -p tmp/isodir/boot/grub
   ```
6. Stage the kernel and GRUB config:
   ```bash
   cp tmp/kernel.bin tmp/isodir/boot/kernel.bin
   ```
   ```bash
   cp grub/grub.cfg tmp/isodir/boot/grub/grub.cfg
   ```
7. Build the bootable ISO:
   ```bash
   grub-mkrescue -o tmp/kernel.iso tmp/isodir
   ```

---

## 8. WSL2 Ubuntu 24.04 Toolchain Setup

Run each command individually (one per `Bash` invocation).

### 8.1 System packages

```bash
sudo apt update
```
```bash
sudo apt install -y build-essential grub-pc-bin grub-common xorriso mtools qemu-system-x86 lld
```

Package roles:
- `build-essential` — pulls in `gcc`, `make`, and `binutils` (`as`, `ld`, `objdump`, `objcopy`, `readelf`, `nm`).
- `grub-pc-bin` — GRUB's BIOS (i386-pc) boot files. Required by `grub-mkrescue` to build a legacy-BIOS-bootable ISO, which is what QEMU's default machine boots.
- `grub-common` — provides `grub-mkrescue` and `grub-file`.
- `xorriso`, `mtools` — required helpers for `grub-mkrescue`.
- `qemu-system-x86` — provides `qemu-system-x86_64`.
- `lld` — provides `ld.lld`, invoked by TinyGo per `target.json`.

### 8.2 TinyGo installation

TinyGo is not packaged by Ubuntu; install the official `.deb` release. Pick the latest amd64 `.deb` from **https://github.com/tinygo-org/tinygo/releases** (this document was authored against `v0.33.0`; any later version should work).

```bash
wget https://github.com/tinygo-org/tinygo/releases/download/v0.33.0/tinygo_0.33.0_amd64.deb -O tmp/tinygo.deb
```
```bash
sudo dpkg -i tmp/tinygo.deb
```
```bash
tinygo version
```

`tinygo version` must print a version ≥ `0.30.0` for the `target.json` fields used here (`linkerscript`, `extra-files`) to work. If the host has a pre-existing older TinyGo installation, `sudo apt remove tinygo` first, then reinstall.

### 8.3 KVM note (optional)

QEMU runs fine in TCG (software emulation) mode on WSL2 and does **not** require `/dev/kvm`. Do **not** pass `-enable-kvm` unless you have verified that nested virtualization is enabled in your Windows host and `/dev/kvm` exists inside WSL2. For a tiny kernel like this, TCG is fast enough.

---

## 9. Running in QEMU

### 9.1 Primary path — bootable ISO via GRUB

```bash
qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown
```

- `-cdrom tmp/kernel.iso` mounts the ISO as a virtual CD-ROM; the default QEMU PC machine boots from it via BIOS → GRUB.
- `-serial stdio` redirects the guest's COM1 to your terminal (useful once we add serial logging; ignored for now).
- `-no-reboot -no-shutdown` freezes the VM on a triple-fault instead of silently rebooting — this makes bootstrap bugs immediately visible.

### 9.2 Secondary path — direct kernel boot (no GRUB, fastest iteration)

QEMU's `-kernel` flag, when given an ELF file with a valid Multiboot 1 header in the first 8 KiB, uses QEMU's built-in Multiboot 1 loader (`hw/i386/multiboot.c`) to load the kernel directly, skipping BIOS and GRUB entirely. If the header is missing or invalid, QEMU falls back to the Linux `bzImage` boot protocol, which will fail silently or produce garbage — so this path also indirectly verifies that the Multiboot header is well-formed.

```bash
qemu-system-x86_64 -kernel tmp/kernel.bin -serial stdio -no-reboot -no-shutdown
```

Use this during development for faster edit-run cycles. Switch back to the ISO path for end-to-end verification. If `-cdrom` works but `-kernel` does not (or vice versa), the bug is almost certainly in the Multiboot header or linker layout, not in your kernel logic.

### 9.3 Display on WSL2

- **WSLg (default on Windows 11 + recent WSL2)**: the QEMU GTK window just works; nothing extra required.
- **No WSLg / headless**: add `-display none -vnc :0` and connect a VNC viewer from Windows to `localhost:5900`, or use `-display gtk` if you have an X server such as VcXsrv running.
- Do **not** use `-nographic` for verification — it detaches the VGA text console, and the hello-world test relies on the VGA buffer to display success.

---

## 10. Verification

Success for this milestone means **all** of the following hold.

### 10.1 Binary-level checks

1. `grub-file --is-x86-multiboot tmp/kernel.bin` exits with status 0.
2. `objdump -h tmp/kernel.bin` shows `.multiboot` with a `File off` value strictly less than `0x2000` (i.e., within the first 8 KiB of the file) **and** with VMA `0x00100000`. Equivalently, `readelf -l tmp/kernel.bin` must show the first `LOAD` segment starting at virtual address `0x100000` with `.multiboot` as its first section. `objdump -h` reports sections in section-header-table order, not file order; the `File off` column is the authoritative check.
3. `objdump -d tmp/kernel.bin` piped to grep for SSE mnemonics must produce no matches:
   ```bash
   objdump -d tmp/kernel.bin | grep -E 'xmm|movaps|movups|movapd|movupd|movsd|movss'
   ```
   Any hit means the TinyGo code is emitting SSE despite the `features` flags in `target.json`, and the guest will fault on first execution.
4. `nm tmp/kernel.bin | grep kernel_main` shows `kernel_main` as a `T` (text) symbol. (You can skip this if `nm` is not installed; not a blocker.)

### 10.2 Runtime checks in QEMU

1. Launch with the primary command:
   ```bash
   qemu-system-x86_64 -cdrom tmp/kernel.iso -serial stdio -no-reboot -no-shutdown
   ```
2. Within ~1 second, the QEMU window should show GRUB flashing and then disappear, leaving a VGA text console with **"Hello, World!"** rendered in bright white on black at row 0, columns 0–12.
3. QEMU does not reboot or exit. (If it reboots, `-no-reboot` would have turned that into a halt instead; check the QEMU console for a triple-fault message.)
4. Pressing `Ctrl+Alt+Q` (or closing the window) ends the session cleanly.

### 10.3 Optional: diff against `run-kernel`

As an additional sanity check, run the secondary path and confirm the same output:

```bash
qemu-system-x86_64 -kernel tmp/kernel.bin -no-reboot -no-shutdown
```

If the ISO path works but `-kernel` does not (or vice versa), the issue is almost always in the Multiboot header or linker layout — see troubleshooting.

---

## 11. Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `grub-file --is-x86-multiboot` fails. | `.multiboot` is missing, beyond the first 8 KiB of the file, not 4-byte aligned, or `--gc-sections` stripped it. | Confirm `KEEP(*(.multiboot))` is in `linker.ld` and that `.multiboot` is the **first** output section in `SECTIONS`. Confirm the section directive in `boot.S` is `.section .multiboot, "a", @progbits` and that `.long MB_MAGIC` is the very first emission in that section. Inspect the file offset via `objdump -h tmp/kernel.bin` — the `File off` column for `.multiboot` must be below `0x2000`. |
| QEMU reboot-loops (even with `-no-reboot` it just halts immediately). | Triple fault during the 32→64 transition. Usually a bad page table entry, unaligned `CR3`, or `CR0.PG` set before `EFER.LME`. | Re-check the order: `CR3` → `CR4.PAE` → `EFER.LME` → `CR0.PG` → `ljmp`. Confirm `pml4` is 4 KiB-aligned (`.align 4096` in `boot.S` **and** `ALIGN(4096)` on `.bss` in `linker.ld`) and that PD entries have the `PAGE_SIZE` flag (`0x83`). |
| Linker error: `undefined reference to kernel_main`. | Missing or mistyped `//export kernel_main` in `main.go`, or TinyGo stripped it, or a blank line between the `//export` directive and the `func` declaration. | Confirm the `//export` comment has **no space** between `//` and `export`, that the directive line is **immediately** followed by `func kernel_main()` with no blank line between, and that the function is package-level. Rebuild. |
| Linker error: `ld.lld: error: undefined symbol: memset` (or `memcpy`, `memmove`, `__stack_chk_fail`). | TinyGo did not ship a `compiler-rt` build for the configured `llvm-target`, or `rtlib` is not wired in. | Confirm `target.json` has `"llvm-target": "x86_64-unknown-linux-elf"` (not `-none-elf`) and `"rtlib": "compiler-rt"`. As a last resort, add a tiny `mem.S` containing hand-rolled `memset`/`memcpy`/`memmove` to `"extra-files"`. |
| TinyGo build fails with `error: unknown file extension '.S'` or `clang: error: cannot assemble`. | The installed TinyGo version's `extra-files` handler does not dispatch `.S` to clang's assembler. | Fall back to the two-step build (see section 11.1 "Two-step fallback build" below). |
| General protection fault (#GP) or invalid opcode as soon as Go code runs. | TinyGo emitted SSE instructions but SSE is not enabled. | Verify the `features` string in `target.json` disables SSE, and rebuild. Run `objdump -d tmp/kernel.bin | grep -E 'xmm|movaps|movups|movapd|movupd|movsd|movss'` — there should be zero matches. If there are, the TinyGo runtime pulled in FP code; check that `main.go` imports **nothing** except `unsafe`. |
| `grub-mkrescue: xorriso: command not found`. | Missing helper tools. | `sudo apt install -y xorriso mtools` and retry. |
| `grub-mkrescue` produces an ISO but QEMU says "No bootable device". | `grub-pc-bin` is missing, so `grub-mkrescue` built an EFI-only ISO that the default QEMU BIOS machine cannot boot. | `sudo apt install -y grub-pc-bin` and rebuild the ISO. |
| TinyGo error: `unknown target option "linkerscript"` or `"extra-files"`. | TinyGo version too old. | Upgrade TinyGo to ≥ 0.30.0 (see section 8.2). |
| VGA shows garbage characters instead of "Hello, World!". | Wrong cell stride or attribute byte in `main.go`. | Each cell is 2 bytes: low byte = ASCII, high byte = attribute. Confirm `colorAttr` is `uint16(0x0F00)` (attribute in the **high** byte). |

### 11.1 Two-step fallback build (if `extra-files` does not accept `.S`)

If the smoke test in section 7.0 failed because TinyGo would not compile `boot.S` through `extra-files`, build the kernel in two steps instead. Remove `"extra-files"` and `"linkerscript"` from `target.json` for this fallback so TinyGo does not try to link; we will drive the link ourselves with `ld.lld`.

1. Assemble `boot.S` to an object file with clang's integrated assembler (targeting the same triple as `target.json`):
   ```bash
   clang -target x86_64-unknown-linux-elf -c src/boot.S -o tmp/boot.o
   ```
2. Compile Go code to an object (no final link). The exact flag depends on the TinyGo version; the following works on TinyGo ≥ 0.30 when the output extension is `.o`:
   ```bash
   tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
   ```
   If TinyGo still tries to link, add `-no-debug` or consult `tinygo help build` for the object-only emit flag (some builds use `-c`, others infer from the output extension).
3. Link manually with our script:
   ```bash
   ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/kernel_go.o
   ```
4. Continue from section 7.1 step 3 (the `grub-file` sanity check) onward.

---

## 12. Out of Scope / Future Work

Not implemented in this milestone, in order of likely next steps:

1. **Heap / GC** — requires parsing the Multiboot memory map, building a page-frame allocator, mapping virtual heap pages, and providing `malloc`/`free` stubs for the TinyGo GC (switch `gc` from `"none"` to `"conservative"` in `target.json`).
2. **Serial output** — COM1 port I/O via a small C or inline-assembly glue layer.
3. **IDT and interrupt handlers** — required before enabling the keyboard or timer.
4. **Keyboard driver** — PS/2 controller polling or interrupt-driven.
5. **ACPI, SMP, userland, filesystems** — long-term.

---

## 13. References

- OSDev Wiki: "Multiboot", "Setting Up Long Mode", "Higher Half Kernel", "Bare Bones" — consult when extending this design.
- TinyGo docs: target JSON fields (`linkerscript`, `extra-files`, `features`) under `tinygo/targets/` in the TinyGo source repository.
- `impldoc/helloworld_cgo_implementation_report.md` — what was actually built and where this design diverged from reality during implementation.
