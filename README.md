# gooos

An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The project explores how far Go can go as a kernel language — boot, memory management, garbage collection, interrupts, scheduling, userspace, and beyond — with assembly used only where the hardware demands it.

## Progress

| Milestone | Status | Description |
|---|---|---|
| Boot to VGA output | Done | Multiboot 1 boot, 32→64-bit transition, VGA text output |
| Heap allocation (leaking GC) | Done | 4 MiB heap via linker-defined region, bump allocator, `make`/`append`/`new` working |
| Conservative GC (mark/sweep) | Done | Automatic garbage collection with real memory reclamation via synthetic ELF header for `findGlobals()` |
| Serial output (COM1) | Done | `outb`/`inb` assembly stubs, COM1 at 115200 baud 8N1, `serialPrint()` logging |
| IDT + interrupt handlers | Done | 256-entry IDT, ISR assembly stubs with Go dispatcher, PIC 8259A remap (IRQs → vectors 32-47) |
| PIT / timer | Done | PIT channel 0 at 100 Hz, global tick counter, preemption-ready |
| PS/2 keyboard driver | Done | IRQ1 handler, scancode set 1 → ASCII, VGA echo buffer with backspace/enter |
| Virtual memory management | Done | Page fault handler, `mapPage`/`unmapPage` with 4 KiB granularity, bump page-frame allocator |
| Scheduler | Done | Preemptive round-robin with context switch assembly, PIT-driven task switching |
| Userspace | Done | Ring 3 execution via `iretq`, TSS for privilege transitions, `int 0x80` syscall interface |
| Filesystem | Done | In-memory flat filesystem: `Create`/`Write`/`Read`/`List` (16 entries, 4 KiB each) |
| SMP | Done | ACPI MADT AP discovery, 16-bit real-mode trampoline, INIT-SIPI-SIPI, multi-core boot |

### Where assembly is used

Go cannot express certain CPU-level operations. These remain in assembly:

- **Boot bootstrap** (`boot.S`): Multiboot header, 32→64-bit mode switch, page table setup, GDT load
- **ISR stubs** (`isr.S`): 256 interrupt entry points — save registers, call Go dispatcher, `iretq`
- **Context switch** (`switch.S`): Save/restore callee-saved registers + RSP for task switching
- **AP trampoline** (`trampoline.S`): 16-bit real-mode → 32-bit → 64-bit mode transition for SMP
- **Port I/O & CPU control** (`stubs.S`): `outb`/`inb`, `lidt`, `sti`/`hlt`, `lgdt`/`ltr`, `invlpg`, CR2/CR3 access, `memcpy`/`memset`, `tinygo_scanCurrentStack`, `jumpToRing3`
- **Synthetic ELF header** (`stubs.S`): Fake `__ehdr_start` in `.rodata` for GC's `findGlobals()`

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
                                          |  - 16 KiB stack             |
                                          |  - PML4/PDP/PD (1 GiB ID)  |
                                          |  - CR3/CR4/EFER/CR0        |
                                          |  - lgdt + ljmp to 64-bit   |
                                          +--------------+--------------+
                                                         |
                                                         v
                                +------------------------------------------+
                                |  TinyGo runtime main (runtime_unix.go)   |
                                |  - preinit(): mmap stub → heap init      |
                                |  - initHeap(): GC block metadata setup   |
                                |  - initAll(): package init               |
                                |  - callMain() → user main()              |
                                +--------------------+---------------------+
                                                     |
                                                     v
                              +----------------------------------------------+
                              |  main()  (main.go)                           |
                              |  - Serial COM1 init                          |
                              |  - IDT + ISR stubs + PIC remap + sti         |
                              |  - PIT timer (100 Hz)                        |
                              |  - Keyboard IRQ1 handler                     |
                              |  - VM: mapPage / unmapPage demo              |
                              |  - Filesystem: create / write / read         |
                              |  - GDT rebuild + TSS + Ring 3 userspace      |
                              |  - Scheduler: 3 preemptive tasks             |
                              |  - SMP: INIT-SIPI-SIPI multi-core boot      |
                              +----------------------------------------------+
                                            |              |
                     +----------------------+    +---------+---------+
                     v                           v                   v
              +-----------+               +-----------+       +-----------+
              | Task 0    |               | Task 1    |       | Task 2    |
              | Ring 3    |               | (demo A)  |       | (demo B)  |
              | int 0x80  |               | VGA line  |       | VGA line  |
              +-----------+               +-----------+       +-----------+
```

## Repository layout

```
gooos/
├── CLAUDE.md                                       # project workflow guide
├── Makefile                                        # two-step build: as → tinygo build → ld.lld
├── README.md                                       # this file
├── go.mod                                          # module github.com/ryogrid/gooos
├── prd.json                                        # Ralph PRD (milestone tracking)
├── grub/
│   └── grub.cfg                                    # GRUB Multiboot config for ISO boot
├── impldoc/                                        # design documents (English)
│   ├── helloworld_cgo_design.md
│   ├── helloworld_cgo_implementation_report.md
│   ├── heap_gc_design.md
│   ├── conservative_gc_design.md
│   └── conservetive_gc_desing_guide.md
├── tasks/
│   └── prd-os-milestones.md                        # PRD for all OS milestones
└── src/
    ├── boot.S                                      # Multiboot 1 header + 32→64 bootstrap
    ├── isr.S                                       # 256 ISR entry stubs (macro-generated)
    ├── switch.S                                    # Context switch (save/restore regs + RSP)
    ├── trampoline.S                                # AP trampoline (16-bit → 64-bit for SMP)
    ├── stubs.S                                     # Port I/O, lidt, sti, memcpy/memset, GC scanner, synthetic ELF header
    ├── linker.ld                                   # Section layout, heap, globals symbols
    ├── target.json                                 # TinyGo target: gc=conservative, scheduler=none
    ├── main.go                                     # Kernel entry: init + demo orchestration
    ├── serial.go                                   # COM1 serial output (115200 8N1)
    ├── idt.go                                      # IDT setup + lidt
    ├── interrupt.go                                # Table-driven interrupt dispatcher
    ├── pic.go                                      # 8259A PIC remap + EOI
    ├── pit.go                                      # PIT timer (100 Hz, IRQ0)
    ├── keyboard.go                                 # PS/2 keyboard (IRQ1, scancode → ASCII)
    ├── vm.go                                       # Virtual memory: mapPage, unmapPage, page fault
    ├── scheduler.go                                # Preemptive round-robin scheduler
    ├── gdt.go                                      # Runtime GDT + TSS for Ring 3
    ├── userspace.go                                # Ring 3 setup + int 0x80 syscall
    ├── fs.go                                       # In-memory filesystem
    └── smp.go                                      # SMP: LAPIC, ACPI MADT, INIT-SIPI-SIPI
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

Under the hood:

```bash
as --64 src/boot.S -o tmp/boot.o
as --64 src/stubs.S -o tmp/stubs.o
as --64 src/isr.S -o tmp/isr.o
as --64 src/switch.S -o tmp/switch.o
as --64 src/trampoline.S -o tmp/trampoline.o
tinygo build -target=src/target.json -o tmp/kernel_go.o ./src
ld.lld -m elf_x86_64 -n -T src/linker.ld -o tmp/kernel.bin tmp/boot.o tmp/stubs.o tmp/isr.o tmp/switch.o tmp/trampoline.o tmp/kernel_go.o
```

### Why two-step and not one-shot `tinygo build`?

TinyGo's `target.json` has `linkerscript` and `extra-files` fields that would, in theory, let a single `tinygo build` do everything. In practice, TinyGo 0.33.0 resolves those paths **relative to its own install directory**, not the project root. So we assemble `.S` files with GNU `as`, compile Go with `tinygo build -o *.o`, and link with `ld.lld` directly.

### Verify the build

```bash
make check-multiboot                                    # grub-file --is-x86-multiboot
nm tmp/kernel.bin | grep " U "                          # must be empty (no unresolved symbols)
nm tmp/kernel.bin | grep __ehdr_start                   # must be in .rodata (synthetic ELF header)
```

## Run in QEMU

> Requires a display (WSLg, X server, or VNC) for VGA output. Serial output goes to the terminal.

Single core:

```bash
make iso
make run            # boots from GRUB ISO, serial on stdio
```

Multi-core (SMP):

```bash
make run-smp        # -smp 4 for 4 cores
```

**Expected output**: VGA shows kernel initialization status (serial, IDT, PIT, keyboard, VM, filesystem, GDT, scheduler, userspace, SMP), demo task counters updating live, and "User: Hello from Ring 3!" from userspace. Terminal shows serial log.

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **IDT**, **PIC**, **PIT**, **PS/2 Keyboard**, **Paging**, **TSS**, and **SMP** are the canonical references for the hardware interfaces this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
