# Hello World Kernel — Implementation Report

> **Status**: Implemented and binary-verified. QEMU runtime verification is pending and must be performed manually on a host with a GUI (WSL2 + WSLg or a display server).
>
> **Companion document**: `impldoc/helloworld_cgo_design.md` is the design specification this report implements.

---

## 1. Summary

A minimal bootable x86_64 kernel was produced that, per the design document:

1. Carries a valid Multiboot 1 header (magic `0x1BADB002`, flags `0`, checksum `0xE4524FFE`).
2. Loads at physical address `0x100000` via GRUB or QEMU's built-in Multiboot loader.
3. Bootstraps from 32-bit protected mode to 64-bit long mode in hand-written assembly (`src/boot.S`).
4. Calls a TinyGo-defined function `kernel_main` (exposed via `//export`) that writes `Hello, World!` to the VGA text buffer at `0xB8000`.
5. Halts cleanly on return.

All four binary-level checks from design §10.1 passed. See §4 of this report.

---

## 2. File Tree (as implemented)

```
gooos/
├── go.mod                                   (module github.com/ryogrid/gooos)
├── Makefile                                 (two-step build)
├── grub/
│   └── grub.cfg
└── src/
    ├── boot.S                               (multiboot header + 32->64 bootstrap)
    ├── main.go                              (TinyGo kernel_main, //export)
    ├── stubs.S                              (libc/runtime dead-code stubs)
    ├── linker.ld                            (ENTRY(_start), VMA 0x100000)
    └── target.json                          (TinyGo bare-metal target)
```

All code comments are in English.

---

## 3. Deviations from the Design Document

The design document's reviewer flagged two items as `UNCERTAIN` (B3 and B8). Both were exposed as real problems during implementation, and additional problems surfaced beyond what the reviewer predicted. This section documents every deviation and the chosen remedy, so that the companion design document can be amended.

### 3.1 TinyGo `extra-files` / `linkerscript` path resolution is install-dir-relative, not target-JSON-relative

**Design assumption (§6.4 notes)**: "`linkerscript` and `extra-files` paths are resolved **relative to `target.json`**."

**Reality**: TinyGo 0.33.0 resolves these paths relative to the TinyGo installation directory (`/usr/local/lib/tinygo/` on this host), **not** the directory containing `target.json`. Empirical evidence:

```
$ tinygo build -target=src/target.json -o tmp/kernel.bin ./src
failed to hash file: open /usr/local/lib/tinygo/boot.S: no such file or directory
```

The same resolution is used by built-in targets. For example, `targets/atmega328p.json` references its linker script as `"src/device/avr/atmega328p.ld"`, which resolves to `/usr/local/lib/tinygo/src/device/avr/atmega328p.ld`.

Passing an absolute path does **not** work either: TinyGo strips the leading `/` and then prepends the install directory, producing `/usr/local/lib/tinygo/<your-project-path>/src/boot.S`, which obviously does not exist.

**Remedy**: switch to the two-step fallback build from design §11.1 (assemble `boot.S` with GNU `as`, compile Go to a relocatable object, manually invoke `ld.lld`). The `linkerscript` and `extra-files` fields were removed from `target.json` because they are not used in the two-step path.

### 3.2 TinyGo with `-o <file>.o` emits a relocatable object file (undocumented)

When the `-o` argument has an `.o` extension, `tinygo build` skips the final link step and emits an ELF64 relocatable object file containing only the compiled Go code (plus TinyGo runtime). This behaviour is not listed in `tinygo help build`, but it is consistent and works on TinyGo 0.33.0. It is what makes the two-step fallback practical — without it the only alternative would be to symlink source files into TinyGo's install directory.

Verified:

```
$ tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
$ file tmp/kernel_go.o
tmp/kernel_go.o: ELF 64-bit LSB relocatable, x86-64, version 1 (SYSV), with debug_info, not stripped
```

### 3.3 Build tag `baremetal` is a catch-22 with `interrupt_none.go`

**Design (§6.4)**: `"build-tags": ["baremetal", "gooos"]`.

**Problem**: TinyGo's `src/runtime/interrupt/interrupt_none.go` is guarded by `//go:build !baremetal`, so adding `baremetal` to our build tags excludes the no-op implementations of `interrupt.Disable` / `interrupt.Restore`. The Go standard library's `internal/task/queue.go` (compiled unconditionally even with `scheduler: "none"`) then fails to resolve these symbols:

```
internal/task/queue.go:15:17: undefined: interrupt.Disable
internal/task/queue.go:17:13: undefined: interrupt.Restore
... (18 similar errors)
```

**Remedy**: removed `baremetal` from the tag list. `target.json` now has `"build-tags": ["gooos"]`.

### 3.4 Removing `baremetal` pulls in `runtime_unix.go` and requires libc/runtime stubs

Removing `baremetal` has a side-effect: TinyGo's `src/runtime/runtime_unix.go` is guarded by `(darwin || (linux && !baremetal && !wasip1 && !wasm_unknown && !wasip2)) && !nintendoswitch`. Without `baremetal`, and with `goos: linux`, this file is compiled, pulling in dead-code references to five libc / TinyGo runtime symbols:

```
$ nm tmp/kernel_go.o | grep " U " | sort -u
                 U abort
                 U mmap
                 U raise
                 U tinygo_register_fatal_signals
                 U write
```

All five references are unreachable on the boot path (`boot.S` calls `kernel_main` directly, bypassing TinyGo's `main`, `preinit`, and fatal-signal registration), but the linker still requires them to resolve.

Using a different build tag to exclude `runtime_unix.go` (`wasip1`, `wasm_unknown`, `nintendoswitch`, etc.) would pull in a wasm-specific runtime (`runtime_wasm_unknown.go` references `__wasm_call_ctors` and `wasm_memory_size`) that is even more inappropriate.

**Remedy**: added `src/stubs.S` providing minimal definitions for the five symbols. `abort` enters an infinite `cli; hlt` loop (so accidental dispatch is caught as a post-mortem freeze rather than wild execution); the others return zero or `-1`. All are marked `.global`. Linked into `tmp/kernel.bin` as a dedicated object file.

### 3.5 `LD ?= ld.lld` in Makefile silently loses to GNU make's builtin `LD=ld`

GNU make defines `LD = ld` as a builtin implicit variable, so `LD ?= ld.lld` is a no-op — the Makefile called GNU `ld` instead of `ld.lld`, producing deprecation warnings and an RWX LOAD segment warning. Fixed by using `LD := ld.lld` (unconditional assignment).

### 3.6 `.note.GNU-stack` missing from boot.S and stubs.S

GNU `as` does not emit a `.note.GNU-stack` section by default. `ld.lld` interprets its absence as "executable stack required" and warns about RWX LOAD segments. Both `boot.S` and `stubs.S` now end with:

```gas
    .section .note.GNU-stack,"",@progbits
```

After this fix, the linker produces no warnings and `.data` + `.bss` are the only RW segment (no RWX).

### 3.7 TinyGo needs a Go module

`tinygo build ./src` failed with `go: cannot find main module` until `go mod init github.com/ryogrid/gooos` was run at the repo root. A `go.mod` file is now committed alongside the source.

---

## 4. Binary-level Check Results (Design §10.1)

| # | Check | Result |
|---|---|---|
| 1 | `grub-file --is-x86-multiboot tmp/kernel.bin` | **exit 0** |
| 2 | `.multiboot` file offset < `0x2000` and VMA = `0x100000` | `File off = 0x158`, `VMA = 0x00100000` |
| 2b | `readelf -l`: first `LOAD` segment starts at virtual `0x100000` with `.multiboot` as first section | `LOAD[0]: Offset 0x158, VirtAddr 0x100000, Sections: .multiboot` |
| 2c | ELF entry point matches `_start` | `Entry point 0x100010` (= start of `.text`, where `_start` is) |
| 2d | `.bss` is 4 KiB-aligned (CR3 requirement on PML4) | `.bss  Algn 2**12` |
| 3 | `objdump -d \| grep -cE 'xmm\|movaps\|movups\|movapd\|movupd\|movsd\|movss'` | **0 matches** |
| 4 | `kernel_main` present as `T` (text) symbol | `0000000000100324 T kernel_main` |
| + | Multiboot header bytes | `02 B0 AD 1B / 00 00 00 00 / FE 4F 52 E4` — magic + flags + checksum = 0 mod 2^32 |
| + | No undefined symbols | `nm tmp/kernel.bin \| grep ' U '` returns nothing |
| + | No RWX `LOAD` segment | `.text` is R+E, `.data+.bss` is RW — no segment with W+X |

### Raw evidence

```
$ grub-file --is-x86-multiboot tmp/kernel.bin
$ echo $?
0

$ objdump -h tmp/kernel.bin
Sections:
Idx Name          Size      VMA               LMA               File off  Algn
  0 .multiboot    0000000c  0000000000100000  0000000000100000  00000158  2**2
                  CONTENTS, ALLOC, LOAD, READONLY, DATA
  1 .text         0000034e  0000000000100010  0000000000100010  00000170  2**4
                  CONTENTS, ALLOC, LOAD, READONLY, CODE
  2 .rodata       000000b6  0000000000100360  0000000000100360  000004c0  2**4
                  CONTENTS, ALLOC, LOAD, READONLY, DATA
  3 .data         00000016  0000000000100420  0000000000100420  00001420  2**4
                  CONTENTS, ALLOC, LOAD, DATA
  4 .bss          00007010  0000000000101000  0000000000101000  00001436  2**12
                  ALLOC
  ...

$ readelf -l tmp/kernel.bin
Elf file type is EXEC (Executable file)
Entry point 0x100010
There are 5 program headers, starting at offset 64

Program Headers:
  Type           Offset             VirtAddr           PhysAddr
                 FileSiz            MemSiz              Flags  Align
  LOAD           0x0000000000000158 0x0000000000100000 0x0000000000100000
                 0x000000000000000c 0x000000000000000c  R      0x4
  LOAD           0x0000000000000170 0x0000000000100010 0x0000000000100010
                 0x000000000000034e 0x000000000000034e  R E    0x10
  LOAD           0x00000000000004c0 0x0000000000100360 0x0000000000100360
                 0x00000000000000b6 0x00000000000000b6  R      0x10
  LOAD           0x0000000000001420 0x0000000000100420 0x0000000000100420
                 0x0000000000000016 0x0000000000007bf0  RW     0x1000
  GNU_STACK      0x0000000000000000 0x0000000000000000 0x0000000000000000
                 0x0000000000000000 0x0000000000000000  RW     0x0

 Section to Segment mapping:
  Segment Sections...
   00     .multiboot
   01     .text
   02     .rodata
   03     .data .bss
   04

$ objdump -d tmp/kernel.bin | grep -cE 'xmm|movaps|movups|movapd|movupd|movsd|movss'
0

$ nm tmp/kernel.bin | grep -E ' T (_start|kernel_main)'
0000000000100010 T _start
0000000000100324 T kernel_main

$ nm tmp/kernel.bin | grep ' U '
(empty)

$ xxd -s 0x158 -l 12 tmp/kernel.bin
00000158: 02b0 ad1b 0000 0000 fe4f 52e4            .........OR.
```

---

## 5. Final Artifact

```
$ file tmp/kernel.bin
tmp/kernel.bin: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, with debug_info, not stripped

$ ls -la tmp/kernel.bin
-rwxrwxr-x 1 ryo ryo 30416 <date>  tmp/kernel.bin
```

Four loadable segments, total `MemSiz` roughly 32 KiB dominated by `.bss` (16 KiB stack + 3 × 4 KiB page tables + alignment padding). `.data+.bss` has `FileSiz 0x16` vs `MemSiz 0x7bf0`, so SHT_NOBITS is zero-filled by the loader as expected.

---

## 6. How to Run in QEMU

QEMU runtime verification requires a display and was **not** executed as part of this implementation. Run the following on a host with WSLg, an X server, or VNC:

Primary (GRUB ISO):

```bash
make iso
make run
```

Secondary (direct kernel boot, skips GRUB):

```bash
make run-kernel
```

Success criteria (design §10.2): `Hello, World!` in bright white on black at the top-left of the VGA text console, guest does not triple-fault or reboot.

---

## 7. Recommended Design Document Amendments

The following changes to `impldoc/helloworld_cgo_design.md` would make it match reality for TinyGo 0.33.0:

1. **§6.4 (`target.json`)** — remove `linkerscript` and `extra-files` fields. Document that these fields are resolved relative to the TinyGo install directory and are therefore unusable for projects living outside it, unless you are willing to sudo-symlink your project into `/usr/local/lib/tinygo/` (brittle).
2. **§6.4** — change the build-tags list to `["gooos"]` only. Do **not** add `baremetal`, because it excludes `interrupt_none.go` and breaks `internal/task`.
3. **§6.4 / new §6.7** — add `src/stubs.S` as a required file, with the five libc/runtime stubs. Explain that these are dead-code references from `runtime_unix.go` that the linker still needs to resolve.
4. **§6.6 (`Makefile`)** — rewrite the build as two-step (`as` → `tinygo build -o *.o` → `ld.lld -T linker.ld`). Use `LD := ld.lld` (not `?=`) to override make's builtin.
5. **§7.0 (smoke test)** — replace the one-step smoke test with a test that `tinygo build -o tmp/kernel_go.o ./src` succeeds and produces an ELF relocatable. This is the real load-bearing step.
6. **§11 troubleshooting** — add: "make uses `ld` instead of `ld.lld`" and "`.note.GNU-stack` missing causes RWX LOAD warning" rows.
7. **§6.1 (`boot.S`) and §6.7 (new `stubs.S`)** — append `.section .note.GNU-stack,"",@progbits` to silence `ld.lld`'s executable-stack warning.

The core boot logic (Multiboot header, page table setup, long-mode transition, GDT, VGA write) is correct as designed and did not need any changes during implementation.
