# gooos

An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The project explores how far Go can go as a kernel language — boot, memory management, garbage collection, and beyond — with assembly used only where the hardware demands it.

## Progress

| Milestone | Status | Description |
|---|---|---|
| Boot to VGA output | Done | Multiboot 1 boot, 32→64-bit transition, VGA text "Hello, World!" |
| Heap allocation (leaking GC) | Done | 4 MiB heap via linker-defined region, bump allocator, `make`/`append`/`new` working |
| Conservative GC (mark/sweep) | Done | Automatic garbage collection with real memory reclamation, `runtime.GC()` + `runtime.ReadMemStats()` |
| Serial output (COM1) | Planned | Logging channel independent of VGA; requires port I/O (assembly or C glue) |
| IDT + interrupt handlers | Planned | Interrupt Descriptor Table setup, ISR/IRQ stubs (assembly), handler dispatch in Go |
| PIT / timer | Planned | Programmable Interval Timer for preemptive scheduling; requires IDT first |
| PS/2 keyboard driver | Planned | Keyboard input via IRQ1; requires IDT first |
| Virtual memory management | Planned | Page-fault handler, on-demand paging, kernel/user address space separation |
| Userspace | Planned | Ring 3 execution, syscall interface (assembly for `syscall`/`sysret`) |
| Filesystem | Planned | Minimal in-memory or FAT filesystem |
| SMP | Planned | Multi-core boot (AP startup via SIPI, requires assembly for trampoline) |
| Scheduler / goroutines | Planned | Preemptive task switching; TinyGo `scheduler` integration |

### Where assembly is unavoidable

Go cannot express certain CPU-level operations. These remain in assembly (or minimal C):

- **Boot bootstrap** (`boot.S`): Multiboot header, 32→64-bit mode switch, page table setup, GDT load, `lgdt`/`ljmp`
- **GC stack scanner** (`stubs.S`): `tinygo_scanCurrentStack` pushes callee-saved registers for the GC to scan
- **Libc stubs** (`stubs.S`): `memcpy`, `memset`, `mmap` — low-level memory operations called by TinyGo's runtime
- **Future: IDT setup**: `lidt` instruction, ISR/IRQ entry stubs that save registers and call Go handlers
- **Future: Context switch**: Save/restore register state for task switching
- **Future: Syscall entry**: `syscall`/`sysret` transition between ring 0 and ring 3
- **Future: SMP trampoline**: AP startup code must run in real mode / 16-bit protected mode

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
                                    |  - call main(0, nil)                 |
                                    +--------------------+-----------------+
                                                         |
                                                         v
                                +------------------------------------------+
                                |  TinyGo runtime main (runtime_unix.go)   |
                                |  - preinit(): mmap stub -> heap init     |
                                |  - initHeap(): GC block metadata setup   |
                                |  - initAll(): package init               |
                                |  - callMain() -> user main()             |
                                +--------------------+---------------------+
                                                     |
                                                     v
                                          +-----------------------------+
                                          |  main()  (main.go)          |
                                          |  - allocate objects         |
                                          |  - runtime.GC()            |
                                          |  - display stats on VGA    |
                                          +--------------+--------------+
                                                         |
                                                         v
                                          +-----------------------------+
                                          |  hang:  cli ; hlt ; jmp .   |
                                          +-----------------------------+
```

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
│   ├── helloworld_cgo_design.md                    # boot + VGA hello-world design
│   ├── helloworld_cgo_implementation_report.md     # hello-world implementation report
│   ├── heap_gc_design.md                           # heap allocator (leaking GC) design
│   ├── conservative_gc_design.md                   # conservative GC (mark/sweep) design
│   └── conservetive_gc_desing_guide.md             # conservative GC reference guide
└── src/
    ├── boot.S                                      # multiboot header + 32->64 bootstrap + GDT
    ├── linker.ld                                   # section layout, heap region, globals symbols
    ├── main.go                                     # conservative GC demo (runtime.GC + ReadMemStats)
    ├── stubs.S                                     # mmap, memcpy, memset, tinygo_scanCurrentStack, synthetic ELF header
    └── target.json                                 # TinyGo target: gc=conservative, scheduler=none
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

Under the hood this runs four commands:

```bash
as --64 src/boot.S -o tmp/boot.o
as --64 src/stubs.S -o tmp/stubs.o
tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/stubs.o tmp/kernel_go.o
```

### Why two-step and not one-shot `tinygo build`?

TinyGo's `target.json` has `linkerscript` and `extra-files` fields that would, in theory, let a single `tinygo build` assemble `boot.S`, link with our linker script, and produce `kernel.bin` in one command. In practice, TinyGo 0.33.0 resolves those paths **relative to its own install directory** (`/usr/local/lib/tinygo/`), not relative to the target.json file or the project root. That makes the single-step approach unusable for out-of-tree projects.

What saves the day is the undocumented behaviour that `tinygo build -o <file>.o` emits a relocatable ELF object instead of a fully linked binary. We use that to compile the Go code, assemble the assembly sources ourselves with GNU `as`, and drive `ld.lld` directly.

### Verify the build

```bash
make check-multiboot                                                       # grub-file --is-x86-multiboot
objdump -h tmp/kernel.bin                                                  # expect .multiboot at File off < 0x2000
readelf -l tmp/kernel.bin                                                  # expect first LOAD at VirtAddr 0x100000
nm tmp/kernel.bin | grep " U "                                             # must be empty (no unresolved symbols)
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

**Expected output**: the QEMU window shows the conservative GC demo — allocation statistics, GC reclamation results, and a confirmation that post-GC allocation succeeds.

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **Higher Half Kernel**, and **Bare Bones** are the canonical reference for the boot sequence this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
