# gooos Architecture Overview

## What is gooos

An experimental x86_64 operating system written in Go (TinyGo 0.33.0) + GNU assembly (~4,200 lines). It boots via GRUB/Multiboot 1, runs a preemptive round-robin scheduler with microkernel-style service tasks, and provides a BusyBox-like interactive shell in Ring 3 userspace.

## Boot Sequence (`src/boot.S` + `src/main.go`)

1. **BIOS/GRUB** loads `kernel.bin` (ELF, Multiboot 1) at physical address `0x100000` (1 MiB)
2. **`_start`** (32-bit, `boot.S`):
   - Sets up 16 KiB kernel stack
   - Builds 1 GiB identity map using 512 x 2 MiB huge pages (PML4 -> PDP -> PD)
   - Enables PAE, Long Mode (EFER.LME), Paging (CR0.PG)
   - Loads 64-bit GDT, far-jumps to long mode
3. **`long_mode_start`** (64-bit, `boot.S`):
   - Reloads segment registers
   - Calls TinyGo's `main(argc=0, argv=nil)`
4. **`main()`** (`main.go`) initializes subsystems in order:
   - Serial (COM1 115200 8N1), IDT (256 entries), PIC (remap IRQ 0-15 to vectors 32-47)
   - PIT timer (100 Hz), PS/2 keyboard (IRQ1)
   - Enables interrupts (`sti`)
   - VM init, ELF parser test, FS direct test
   - SMP boot (INIT-SIPI-SIPI, APs halt after reporting)
   - GDT rebuild with Ring 3 segments + TSS
   - Scheduler init (task 0 = main/boot)
   - Spawns 2 service tasks: serial output, filesystem
   - Stores 5 user ELF binaries + `hello.txt` in filesystem
   - Enables preemptive scheduling (`schedReady = true`)
   - Loads `sh.elf` and jumps to Ring 3 (does not return)

## Memory Layout

```
Physical/Virtual Address Map (identity-mapped 0-1 GiB via 2 MiB huge pages):

0x00000000 - 0x000FFFFF   Reserved (BIOS, VGA at 0xB8000, etc.)
0x00100000 - 0x00108xxx   Kernel .text + .multiboot
0x00108xxx - 0x001xxxxx   Kernel .rodata (includes synthetic ELF header)
0x001xxxxx - 0x002xxxxx   Kernel .data (_globals_start) + .bss (_globals_end)
0x002xxxxx - 0x006D1000   Kernel .heap (4 MiB, _heap_start to _heap_end)
0x006D1000 + 4096          Guard gap (1 page, prevents GC metadata off-by-one)
0x006D2000 - 0x006D4FFF   .pagetables section (PML4, PDP, PD — boot-time page tables)
0x006D5000                 _alloc_start (bump allocator begins here)
0x006D5000 - 0x3FFFFFFF   Available for allocPage() (identity-mapped)

Above 1 GiB (user virtual addresses, mapped at 4 KiB granularity):
0x40100000 - 0x401FFFFF   User code/data (.text, .rodata, .data, .bss)
0x40300000 - 0x403FFFFF   Argument page (kernel writes args before exec)
0x40102000 - 0x40201FFF   User heap (grown via sys_sbrk, ~1 MiB)
0x7FFF0000 - 0x7FFF1FFF   User stack (8 KiB, 2 pages, grows downward)
```

## Task Model

- **Max 32 tasks** (`maxTasks`), currently 3 active at shell startup (task 0 = shell, task 1 = serial, task 2 = FS)
- Each task has: 4 KiB context-switch stack + 8 KiB per-task kernel stack (for TSS RSP0)
- **5 task states**: Running (0), Ready (1), Blocked (2), Exited (3), Free (4)
- Preemptive round-robin scheduling driven by PIT timer at 100 Hz
- Context switch saves/restores callee-saved registers (rbx, rbp, r12-r15) + RSP

## Kernel/Userspace Boundary

- User programs run in **Ring 3** with separate virtual address pages mapped with `pageUser` flag
- Syscalls via `int 0x80` (12 syscalls, see [syscalls.md](syscalls.md))
- TSS RSP0 updated per-task on context switch to prevent ISR frame corruption
- Page tables shared (single CR3) but user pages mapped/unmapped per-process by elfExec/processExit

## Build Pipeline

```
user/cmd/*.go  --[tinygo build]--> user/build/*.elf
user/build/*.elf --[embed_elfs.sh]--> src/user_binaries.go (Go byte arrays)
src/*.go + src/*.S --[tinygo build + as + ld.lld]--> tmp/kernel.bin
tmp/kernel.bin --[grub-mkrescue]--> tmp/kernel.iso
```

Key: `make build` runs all phases. `make run` boots the ISO in QEMU.

## Key Design Decisions

| Decision | Rationale |
|---|---|
| TinyGo (not standard Go) | Small binary, bare-metal support, `gc`/`scheduler` control |
| `gc=leaking` (workaround) | Conservative GC's metadata memset corrupts page tables |
| Bump-only page allocator | Free list next-pointers corrupted page tables on reuse |
| Per-task kernel stacks | Shared TSS RSP0 caused ISR frame corruption between tasks |
| Channel-based microkernel | FS/serial run as tasks, decoupled from syscall handlers |
| Embedded user ELFs | No disk I/O; binaries compiled into kernel `.rodata` |
