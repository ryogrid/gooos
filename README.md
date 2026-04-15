# gooos

An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The project explores how far Go can go as a kernel language — boot, memory management, interrupts, scheduling, channel-based IPC, microkernel services, userspace with ELF loading, and an interactive shell — with assembly used only where the hardware demands it.

## Progress

| Milestone | Status | Description |
|---|---|---|
| Boot to VGA output | Done | Multiboot 1 boot, 32→64-bit transition, VGA text output |
| Heap allocation | Done | 4 MiB heap via linker-defined region, bump allocator, `make`/`append`/`new` working |
| Serial output (COM1) | Done | `outb`/`inb` assembly stubs, COM1 at 115200 baud 8N1, `serialPrint()` logging |
| IDT + interrupt handlers | Done | 256-entry IDT, ISR assembly stubs with Go dispatcher, PIC 8259A remap (IRQs → vectors 32-47) |
| PIT / timer | Done | PIT channel 0 at 100 Hz, global tick counter, preemption-ready |
| PS/2 keyboard driver | Done | IRQ1 handler, scancode set 1 → ASCII (lowercase + punctuation), VGA echo |
| Virtual memory management | Done | Page fault handler, `mapPage`/`unmapPage` with 4 KiB granularity, bump + LIFO free stack with `allocPagesContig` for kernel stacks |
| Scheduler | Done | Preemptive round-robin, per-task kernel stacks, PIT-driven task switching, TSS RSP0 update on context switch |
| Userspace | Done | Ring 3 execution via `iretq`, TSS for privilege transitions, `int 0x80` syscall interface (12 syscalls) |
| Filesystem | Done | In-memory flat filesystem: `Create`/`Write`/`Read`/`List`/`Delete` (32 entries, 40 KiB each) |
| SMP | Done | ACPI MADT AP discovery, 16-bit real-mode trampoline, INIT-SIPI-SIPI, multi-core boot |
| Channel IPC + select | Done | Bounded/unbuffered typed message channels, blocking `chanSend`/`chanRecv`, non-blocking `chanTrySend`, `selectWait` multiplexer |
| Microkernel services | Done | Serial and filesystem run as isolated kernel tasks communicating via channels |
| Syscall ABI | Done | 12-syscall register-based dispatch: `sys_exit`, `sys_write`, `sys_read`, `sys_exec`, `sys_fs_read/write/list`, `sys_yield`, `sys_sleep`, `sys_getargs`, `sys_sbrk`, `sys_vga_clear` |
| ELF64 loader | Done | Parse ELF64 headers, map PT_LOAD segments, per-process page tracking, parent page save/restore for exec |
| BusyBox-style shell | Done | Interactive shell (`sh.elf`) with built-in commands (help, echo, clear, exit) and external ELF commands (ls, cat, wc, hello) compiled with TinyGo |

### Where assembly is used

Go cannot express certain CPU-level operations. These remain in assembly:

- **Boot bootstrap** (`boot.S`): Multiboot header, 32→64-bit mode switch, page table setup, GDT load
- **ISR stubs** (`isr.S`): 256 interrupt entry points — save registers, call Go dispatcher, `iretq`; passes register frame pointer for syscall argument access
- **Context switch** (`switch.S`): Save/restore callee-saved registers + RSP for task switching; entry-point address stubs for service tasks and `elfExecTrampoline`
- **AP trampoline** (`trampoline.S`): 16-bit real-mode → 32-bit → 64-bit mode transition for SMP
- **Port I/O & CPU control** (`stubs.S`): `outb`/`inb`, `cli`/`sti`/`hlt`, `lidt`/`lgdt`/`ltr`, `invlpg`, CR2/CR3 access, `memcpy`/`memset`, `jumpToRing3`, `readFlags`/`restoreFlags`
- **Synthetic ELF header** (`stubs.S`): Fake `__ehdr_start` in `.rodata` for GC's `findGlobals()`
- **User startup** (`user/rt0.S`): `_start`, syscall wrappers (`syscall0`-`syscall4`), TinyGo runtime stubs (`mmap`, `write`, `abort`, `memcpy`, `memset`)

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
                                |  - initAll(): package init               |
                                |  - callMain() → user main()              |
                                +--------------------+---------------------+
                                                     |
                                                     v
                              +----------------------------------------------+
                              |  main()  (main.go)                           |
                              |  - Serial, IDT, PIC, PIT, Keyboard, VM      |
                              |  - SMP: INIT-SIPI-SIPI multi-core boot      |
                              |  - GDT + TSS (per-task kernel stacks)       |
                              |  - Scheduler init, service tasks            |
                              |  - Store user ELFs in filesystem            |
                              |  - Load sh.elf → Ring 3 shell               |
                              +----------------------------------------------+
                                                     |
                  +----------------------------------+----------------------------------+
                  |                                  |                                  |
    Service Tasks (Ring 0)                 Shell (Ring 3)               External Commands (Ring 3)
    ┌──────────────────────┐        ┌──────────────────┐          ┌──────────────────┐
    │ Serial Output Task   │        │ sh.elf           │          │ ls.elf / cat.elf │
    │  chanRecv → COM1 TX  │        │  $ prompt        │  exec    │ hello.elf / wc.elf│
    ├──────────────────────┤        │  built-in: help, │ -------> │                  │
    │ Filesystem Task      │        │   echo, clear    │          │  TinyGo compiled │
    │  FSRequest/FSResponse│        │  external: ls,   │ <------- │  sys_exit returns │
    │  via reply channels  │        │   cat, hello, wc │  exit    │  to shell         │
    └──────────────────────┘        └──────────────────┘          └──────────────────┘
```

## Repository layout

```
gooos/
├── CLAUDE.md                                       # project workflow guide
├── Makefile                                        # three-phase build: user → embed → kernel
├── README.md                                       # this file
├── go.mod                                          # module github.com/ryogrid/gooos
├── grub/
│   └── grub.cfg                                    # GRUB Multiboot config for ISO boot
├── scripts/
│   └── embed_elfs.sh                               # convert user ELFs to Go byte arrays
├── current_impl_doc/                               # implementation documentation
│   ├── overview.md                                 # architecture, boot, memory layout
│   ├── syscalls.md                                 # 12-syscall ABI reference
│   ├── scheduler.md                                # task management, process lifecycle
│   ├── memory.md                                   # page allocator, page tables
│   ├── ipc.md                                      # channels, service tasks
│   ├── userland.md                                 # SDK, build system, user programs
│   └── known_issues.md                             # workarounds, limitations
├── impldoc/                                        # design documents (English)
│   ├── busybox_overview.md                         # BusyBox shell design
│   ├── busybox_syscall_abi.md                      # syscall ABI design
│   ├── busybox_kernel_changes.md                   # kernel modification design
│   ├── busybox_userland_sdk.md                     # userland SDK design
│   └── busybox_shell_spec.md                       # shell specification
├── user/                                           # userland SDK and programs
│   ├── Makefile                                    # build all user ELFs
│   ├── target.json                                 # TinyGo target for userspace (gc=leaking)
│   ├── linker_user.ld                              # linker script (entry at 0x40100000)
│   ├── rt0.S                                       # startup assembly + syscall stubs
│   ├── go.mod                                      # user module
│   ├── gooos/                                      # Go package for user programs
│   │   ├── syscall.go                              # raw syscall wrappers
│   │   ├── io.go                                   # Print, Println, ReadLine
│   │   ├── fs.go                                   # ReadFile, ListDir
│   │   └── proc.go                                 # Exec, Exit, Args, Yield, Sleep
│   └── cmd/                                        # user programs
│       ├── sh/main.go                              # interactive shell
│       ├── hello/main.go                           # hello world
│       ├── ls/main.go                              # list files
│       ├── cat/main.go                             # display file contents
│       └── wc/main.go                              # word/line/byte count
└── src/                                            # kernel source
    ├── boot.S                                      # Multiboot 1 header + 32→64 bootstrap
    ├── isr.S                                       # 256 ISR entry stubs (macro-generated)
    ├── switch.S                                    # context switch + task entry stubs
    ├── trampoline.S                                # AP trampoline (16-bit → 64-bit for SMP)
    ├── stubs.S                                     # port I/O, CPU control, GC support
    ├── linker.ld                                   # section layout, heap, .pagetables, _alloc_start
    ├── target.json                                 # TinyGo target: gc=leaking, scheduler=none
    ├── main.go                                     # kernel entry: init, task creation, shell launch
    ├── serial.go                                   # COM1 serial output + serial task
    ├── idt.go                                      # IDT setup + lidt
    ├── interrupt.go                                # table-driven interrupt dispatcher + syscall dispatch
    ├── pic.go                                      # 8259A PIC remap + EOI
    ├── pit.go                                      # PIT timer (100 Hz, IRQ0)
    ├── keyboard.go                                 # PS/2 keyboard driver
    ├── vm.go                                       # virtual memory: mapPage, unmapPage, bump allocator
    ├── vga.go                                      # VGA console with cursor and scrolling
    ├── scheduler.go                                # scheduler: WaitQueue, yield, taskSleep, round-robin
    ├── channel.go                                  # channel IPC: send/recv, select, channel ID table
    ├── elf.go                                      # ELF64 parser and loader
    ├── process.go                                  # process lifecycle: elfExec, processExit, page save/restore
    ├── gdt.go                                      # runtime GDT + TSS, per-task RSP0 update
    ├── userspace.go                                # Ring 3 setup, 12-syscall ABI dispatch
    ├── fs.go                                       # in-memory filesystem + FS task
    ├── smp.go                                      # SMP: LAPIC, ACPI MADT, INIT-SIPI-SIPI
    └── user_binaries.go                            # generated: embedded user ELF byte arrays
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

This runs three phases:

1. **User programs**: `make -C user all` — compiles TinyGo user programs (`sh`, `hello`, `ls`, `cat`, `wc`) into ELF binaries
2. **Embed**: `scripts/embed_elfs.sh` — converts user ELFs to Go byte arrays in `src/user_binaries.go`
3. **Kernel**: assembles `.S` files, compiles all Go with TinyGo, links with `ld.lld` into `tmp/kernel.bin`

### Verify the build

```bash
make check-multiboot                                    # grub-file --is-x86-multiboot
nm tmp/kernel.bin | grep " U "                          # must be empty (no unresolved symbols)
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

**Expected output**: VGA shows kernel initialization, then an interactive shell prompt. Type `help` to see available commands:

```
gooos shell v0.1
Type 'help' for available commands.

$ help
Built-in commands:
  help       Show this help message
  echo       Print arguments
  clear      Clear the screen
  exit       Halt the system

External commands:
  ls         List files
  cat FILE   Display file contents
  wc FILE    Count lines, words, bytes
  hello      Print greeting

$ ls
hello.txt
sh.elf
hello.elf
ls.elf
cat.elf
wc.elf

$ cat hello.txt
Hello from the gooos filesystem!
This is a test file.

$ hello
Hello, World from gooos userspace!
```

## Documentation

See `current_impl_doc/` for detailed implementation documentation:

- [Architecture Overview](current_impl_doc/overview.md) — boot flow, memory map, task model
- [Syscall ABI](current_impl_doc/syscalls.md) — 12-syscall reference
- [Scheduler](current_impl_doc/scheduler.md) — task states, context switch, process lifecycle
- [Memory](current_impl_doc/memory.md) — page allocator, page tables, linker layout
- [IPC](current_impl_doc/ipc.md) — channels, service tasks
- [Userland](current_impl_doc/userland.md) — SDK, build system, user programs
- [Known Issues](current_impl_doc/known_issues.md) — workarounds and limitations

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **IDT**, **PIC**, **PIT**, **PS/2 Keyboard**, **Paging**, **TSS**, and **SMP** are the canonical references for the hardware interfaces this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
