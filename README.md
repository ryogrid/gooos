# gooos

An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly** (~4,200 lines). The project explores how far Go can go as a kernel language — boot, memory management, garbage collection, interrupts, scheduling, channel-based IPC, microkernel services, userspace with ELF loading, and beyond — with assembly used only where the hardware demands it.

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
| Page-frame free-list | Done | `freePage()`/`allocPage()` with singly-linked intrusive free list for page reclamation |
| WaitQueue / yield / sleep | Done | `WaitQueue` primitive, `yield()`, `taskSleep(ticks)` with sorted sleep queue and timer-driven wakeup |
| Channel IPC + select | Done | Bounded/unbuffered typed message channels, blocking `chanSend`/`chanRecv`, non-blocking `chanTrySend`, `selectWait` multiplexer |
| Microkernel services | Done | Keyboard, serial, and filesystem run as isolated kernel tasks communicating via channels |
| Syscall ABI | Done | Register-based dispatch table (`rax`=number, `rdi/rsi/rdx`=args): `sys_yield`, `sys_exit`, `sys_send`, `sys_recv`, `sys_spawn`, `sys_print` |
| ELF64 loader | Done | Parse ELF64 headers, map PT_LOAD segments into userspace, launch in Ring 3 |
| Channel syscalls | Done | `sys_send`/`sys_recv` for channel IPC from userspace; ELF user program performs keyboard recv → serial send round-trip |

### Where assembly is used

Go cannot express certain CPU-level operations. These remain in assembly:

- **Boot bootstrap** (`boot.S`): Multiboot header, 32→64-bit mode switch, page table setup, GDT load
- **ISR stubs** (`isr.S`): 256 interrupt entry points — save registers, call Go dispatcher, `iretq`; passes register frame pointer for syscall argument access
- **Context switch** (`switch.S`): Save/restore callee-saved registers + RSP for task switching; entry-point address stubs for all kernel service tasks (channel, select, serial, FS, keyboard, user-print)
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
                              |  - Serial, IDT, PIC, PIT, Keyboard, VM      |
                              |  - SMP: INIT-SIPI-SIPI multi-core boot      |
                              |  - GDT + TSS for Ring 3                     |
                              |  - Scheduler init (16 tasks)                |
                              |  - ELF load user.elf → Ring 3               |
                              +----------------------------------------------+
                                                     |
                  +----------------------------------+----------------------------------+
                  |                                  |                                  |
    Microkernel Services (Ring 0)         Demo & IPC Tasks (Ring 0)       Userspace (Ring 3)
    ┌──────────────────────┐             ┌─────────────────────┐        ┌──────────────────┐
    │ Keyboard Task        │ ──channel──▶│ Channel Producer    │        │ ELF User Program │
    │  IRQ1 → KeyEvent ch  │             │ Channel Consumer    │        │  sys_print       │
    ├──────────────────────┤             │ Unbuffered Rendezvou│        │  sys_recv (kbd)   │
    │ Serial Output Task   │◀──channel── │ Select Multiplexer  │        │  sys_send (print) │
    │  chanRecv → COM1 TX  │             ├─────────────────────┤        │  int 0x80 ABI    │
    ├──────────────────────┤             │ Demo Task A (50 t)  │        └──────────────────┘
    │ Filesystem Task      │             │ Demo Task B (75 t)  │                 │
    │  FSRequest/FSResponse│             │ Demo Task C (100 t) │           syscall dispatch
    │  via reply channels  │             └─────────────────────┘         (rax=nr, rdi/rsi/rdx)
    ├──────────────────────┤
    │ User Print Task      │◀──channel── sys_send from Ring 3
    │  chanRecv → serial   │
    └──────────────────────┘
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
│   ├── prd-os-milestones.md                        # PRD for base OS milestones
│   └── prd-goroutine-microkernel.md                # PRD for microkernel milestones
└── src/
    ├── boot.S                                      # Multiboot 1 header + 32→64 bootstrap
    ├── isr.S                                       # 256 ISR entry stubs (macro-generated), register frame for syscalls
    ├── switch.S                                    # Context switch + entry stubs for all service/demo tasks
    ├── trampoline.S                                # AP trampoline (16-bit → 64-bit for SMP)
    ├── stubs.S                                     # Port I/O, lidt, sti, memcpy/memset, GC scanner, synthetic ELF header
    ├── linker.ld                                   # Section layout, heap, globals symbols
    ├── target.json                                 # TinyGo target: gc=conservative, scheduler=none
    ├── main.go                                     # Kernel entry: init, task creation, ELF load
    ├── serial.go                                   # COM1 serial output + microkernel serial task
    ├── idt.go                                      # IDT setup + lidt
    ├── interrupt.go                                # Table-driven interrupt dispatcher + syscall dispatch
    ├── pic.go                                      # 8259A PIC remap + EOI
    ├── pit.go                                      # PIT timer (100 Hz, IRQ0)
    ├── keyboard.go                                 # PS/2 keyboard driver + microkernel keyboard task
    ├── vm.go                                       # Virtual memory: mapPage, unmapPage, freePage, page fault
    ├── scheduler.go                                # Scheduler: WaitQueue, yield, taskSleep, round-robin
    ├── channel.go                                  # Channel IPC: send/recv, select, channel ID table
    ├── elf.go                                      # ELF64 parser and loader for userspace binaries
    ├── gdt.go                                      # Runtime GDT + TSS for Ring 3
    ├── userspace.go                                # Ring 3 setup, syscall ABI (6 syscalls), ELF user program
    ├── fs.go                                       # In-memory filesystem + microkernel FS task
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

**Expected output**: VGA shows kernel initialization status (serial, IDT, PIT, keyboard, VM, free-list, ELF parser, filesystem, timer, SMP, GDT, scheduler). Demo task counters (A/B/C) update live on VGA lines 15-17. Channel IPC demos (buffered producer/consumer, unbuffered rendezvous, select multiplexer) run concurrently. Microkernel service tasks handle keyboard echo, serial output, and filesystem requests via channels. An ELF64 user program loads from the in-memory filesystem, executes in Ring 3, and performs a channel round-trip (keyboard recv → serial send) via `int 0x80` syscalls. Terminal shows serial log with task switches, channel operations, and userspace syscall output.

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **IDT**, **PIC**, **PIT**, **PS/2 Keyboard**, **Paging**, **TSS**, and **SMP** are the canonical references for the hardware interfaces this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
