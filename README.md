# gooos

An experimental x86_64 "hello world" kernel written in **TinyGo + GNU assembly**. The goal is to learn and document what it takes to boot a Go-language kernel from a GRUB/Multiboot entry all the way to printing a message on the VGA text console — and to do it with as little C glue as possible.

## What it does

On boot, `gooos` performs the canonical 32→64-bit bring-up for a Multiboot 1 kernel and then hands control to a Go function:

1. GRUB (or QEMU's built-in Multiboot loader) loads the kernel ELF at physical address `0x100000`.
2. `src/boot.S` (32-bit) sets up a 16 KiB stack, builds a 4-level page table that identity-maps the first 1 GiB via 2 MiB huge pages, enables `CR4.PAE`, `EFER.LME`, and `CR0.PG`, loads a 64-bit GDT, and far-jumps into 64-bit code.
3. `src/boot.S` (64-bit) reloads data segment registers and calls `kernel_main`.
4. `src/main.go` defines `kernel_main` via TinyGo's `//export` directive. It clears the VGA text buffer at `0xB8000` and writes `Hello, World!` in bright white on black at the top-left corner.
5. On return, `boot.S` disables interrupts and halts the CPU forever.

No cgo (`import "C"`), no heap, no GC, no interrupts, no drivers. Everything the kernel needs is hand-written assembly or TinyGo with only `unsafe` imported.

## Architecture

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
                                          |  - CR3 / CR4.PAE            |
                                          |  - EFER.LME / CR0.PG        |
                                          |  - lgdt + ljmp              |
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
                                                         v
                                          +-----------------------------+
                                          |  hang:  cli ; hlt ; jmp .   |
                                          +-----------------------------+
```

See `impldoc/helloworld_cgo_design.md` for the full design (memory layout, GDT bits, register sequence, linker script, troubleshooting matrix), and `impldoc/helloworld_cgo_implementation_report.md` for what was actually built and where it deviated from the design.

## Status

| Component | Status |
|---|---|
| Boot assembly (`boot.S`) | Implemented, assembles cleanly |
| TinyGo `kernel_main` (`main.go`) | Implemented |
| Linker script (`linker.ld`) | Implemented |
| Multiboot 1 header | Valid (`grub-file --is-x86-multiboot` exits 0) |
| `.multiboot` placement | File offset `0x158`, VMA `0x00100000` |
| No SSE/MMX/AVX in output | Verified (0 matches in `objdump -d`) |
| `kernel_main` exported as C ABI symbol | Verified (`T kernel_main`) |
| No undefined symbols after link | Verified |
| QEMU runtime verification | **Pending** (requires a display — run it yourself) |

## Repository layout

```
gooos/
├── CLAUDE.md                                       # project workflow guide (for AI assistants)
├── Makefile                                        # two-step build: as -> tinygo build -> ld.lld
├── README.md                                       # this file
├── go.mod                                          # module github.com/ryogrid/gooos
├── grub/
│   └── grub.cfg                                    # single-menuentry GRUB config for the ISO
├── impldoc/
│   ├── helloworld_cgo_design.md                    # English design specification
│   └── helloworld_cgo_implementation_report.md     # English implementation report
└── src/
    ├── boot.S                                      # multiboot header + 32->64 bootstrap + GDT
    ├── linker.ld                                   # ENTRY(_start), VMA 0x100000, ALIGN(4096) .bss
    ├── main.go                                     # TinyGo kernel_main, //export
    ├── stubs.S                                     # dead-code stubs: abort, write, mmap, raise, ...
    └── target.json                                 # TinyGo bare-metal target definition
```

## Prerequisites

Tested on **WSL2 Ubuntu 24.04** with:

- **TinyGo 0.33.0** (LLVM 18.1.2) — install from the official `.deb` at <https://github.com/tinygo-org/tinygo/releases>
- **binutils** (`as`, `ld`, `objdump`, `readelf`, `nm`) — via `build-essential`
- **lld** — provides `ld.lld`
- **grub-pc-bin**, **grub-common** — provide `grub-file` and `grub-mkrescue`
- **xorriso**, **mtools** — required by `grub-mkrescue`
- **qemu-system-x86** — provides `qemu-system-x86_64`

Install in one shot:

```bash
sudo apt update
sudo apt install -y build-essential grub-pc-bin grub-common xorriso mtools qemu-system-x86 lld
# Then install TinyGo from the .deb release linked above.
```

## Build

```bash
make build
```

Under the hood this runs three commands:

```bash
as --64 src/boot.S -o tmp/boot.o
as --64 src/stubs.S -o tmp/stubs.o
tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/stubs.o tmp/kernel_go.o
```

### Why two-step and not one-shot `tinygo build`?

TinyGo's `target.json` has `linkerscript` and `extra-files` fields that would, in theory, let a single `tinygo build` assemble `boot.S`, link with our linker script, and produce `kernel.bin` in one command. In practice, TinyGo 0.33.0 resolves those paths **relative to its own install directory** (`/usr/local/lib/tinygo/`), not relative to the target.json file or the project root. That makes the single-step approach unusable for out-of-tree projects.

What saves the day is the undocumented behaviour that `tinygo build -o <file>.o` emits a relocatable ELF object instead of a fully linked binary. We use that to compile the Go code, assemble the assembly sources ourselves with GNU `as`, and drive `ld.lld` directly. See the implementation report (§3.1, §3.2) for details.

### Verify the build

```bash
make check-multiboot                                                       # grub-file --is-x86-multiboot
objdump -h tmp/kernel.bin                                                  # expect .multiboot at File off < 0x2000
readelf -l tmp/kernel.bin                                                  # expect first LOAD at VirtAddr 0x100000
objdump -d tmp/kernel.bin | grep -cE 'xmm|movaps|movups|movsd|movss'       # must print 0
nm tmp/kernel.bin | grep kernel_main                                       # must show a T symbol
```

## Run in QEMU

> QEMU runtime verification requires a display (WSLg, X server, or VNC). The CI-style binary-level checks above do not.

Primary path — bootable ISO via GRUB:

```bash
make iso
make run
```

Fastest iteration — QEMU's built-in Multiboot loader, skipping GRUB:

```bash
make run-kernel
```

Both commands pass `-no-reboot -no-shutdown` so a triple-fault freezes the guest instead of silently rebooting, which makes bring-up bugs immediately visible.

**Success**: the QEMU window shows `Hello, World!` in bright white on black at the top-left of the VGA text console. The guest stays halted (no reboot loop).

## Out of scope / future work

Not implemented in this milestone, in rough order of likelihood:

1. Heap and GC (parse the Multiboot memory map, implement a page-frame allocator, plug TinyGo's `conservative` GC into `malloc`/`free`).
2. Serial output on COM1 for a real logging channel.
3. IDT + interrupt handlers, so we can enable the keyboard and the PIT.
4. PS/2 keyboard driver.
5. ACPI, SMP, userspace, filesystems — long-term.

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **Higher Half Kernel**, and **Bare Bones** are the canonical reference for the boot sequence this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
