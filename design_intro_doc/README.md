# gooos Design Introduction

A self-contained set of English documents that explain the design and current implementation of **gooos**, an educational hobby operating system written in TinyGo for x86_64.

If you have studied operating-system theory in a university course (processes, virtual memory, scheduling, file systems, system calls, interrupts) but have **never built a real kernel**, this document set is written for you. Every textbook concept is connected to a concrete struct, function, and `path/to/file.go:line` in the gooos source tree.

---

## Audience contract

This documentation assumes:

- You can read **Go** fluently. You do not need to know **TinyGo** internals beforehand — the parts that matter are explained in [Chapter 11](./11_tinygo_baremetal.md).
- You have a textbook understanding of:
  - Processes and process address spaces
  - Virtual memory and page tables
  - Threads and scheduling
  - Synchronization primitives (locks, condition variables, semaphores)
  - File systems and file descriptors
  - System calls and the user/kernel boundary
  - Interrupts and interrupt handling
- You have **not** previously written a kernel, parsed an ELF, configured an x86_64 page table by hand, or wired up a multiprocessor boot.

What you should walk away with after reading the full set:

- A mental map from each textbook concept to the file, struct, and function in gooos that implements it.
- A working understanding of the design choices that make gooos different from a textbook OS — most importantly, the **Route C** design that replaces Go goroutines with hand-rolled `KernelThread` objects in Ring 0.
- The ability to boot gooos in QEMU on your own machine within ~30 minutes ([Chapter 02](./02_build_and_run.md)).

---

## How to read this set

There are three reading paths. Pick the one that matches your goal.

### Path A — "I want to run it first" (≈90 minutes)

```
README.md  →  01_architecture_overview.md  →  02_build_and_run.md  →  03_boot_and_init.md  →  11_tinygo_baremetal.md
```

This route gets you to a booting kernel quickly and gives you just enough conceptual background to understand the boot output, then jumps straight to the TinyGo specifics so the build pipeline makes sense.

### Path B — "Theory-first deep dive" (≈5–6 hours)

```
01  →  03  →  04  →  05  →  06  →  07  →  08  →  09  →  10  →  11  →  02
```

Read in numerical order. Build & run is read **last** as confirmation, not introduction. This is the most thorough path.

### Path C — "Just tell me how Route C works" (≈2 hours)

```
01  →  05  →  06  →  09  →  11
```

Architectural overview, then the kernel-thread runtime, then SMP & preemption, then synchronization, then the TinyGo bare-metal layer. Skips boot, memory, processes, syscalls, and I/O.

---

## Chapter list

| # | File | What it covers |
|---|---|---|
| 01 | [Architecture Overview](./01_architecture_overview.md) | Big-picture block diagram, the four scheduling tiers, kernel address-space map, the "no goroutine in kernel Ring 0" decision |
| 02 | [Building and Running gooos](./02_build_and_run.md) | TinyGo + runtime patch, Makefile pipeline, `target.json` decoded, QEMU run targets, 30-minute first-run walkthrough |
| 03 | [Boot and Initialization](./03_boot_and_init.md) | Multiboot-1 handshake, 32-bit→long-mode transition, IDT setup, ordered `main()` initialization |
| 04 | [Memory Management](./04_memory_management.md) | 4-level paging, bump+freeStack page allocator, `mapPage`/`unmapPage`, per-process PML4, kernel and user address-space maps |
| 05 | [Kernel Thread Runtime (Route C)](./05_kernel_thread_runtime.md) | The single biggest divergence from textbook OS design: `KernelThread`, per-CPU FIFO queues, hand-rolled context switch, the kthread pool |
| 06 | [SMP and Preemption](./06_smp_and_preemption.md) | ACPI MADT walk, INIT-SIPI-SIPI, per-CPU storage, M6/M7 invariants, IPI vectors, preempt phases |
| 07 | [Processes and Userspace](./07_processes_and_userspace.md) | `Process` struct, `elfSpawn`, the Ring-3 wrapper kthread, `iretq` boundary, the 22 user programs |
| 08 | [System Calls](./08_syscalls.md) | `int 0x80` path, full syscall-number table, blocking I/O via `kschedYield` / `KEvent`, SIGALRM frame rewrite |
| 09 | [Synchronization Primitives](./09_synchronization.md) | Spinlocks with rank-based deadlock prevention, `KEvent`, bounded MPSC queues, the timer wheel |
| 10 | [Drivers, Filesystem, and Networking](./10_drivers_filesystem_network.md) | The shared "IRQ → enqueue → kthread server → user wake" pattern: PS/2 keyboard, e1000 NIC + IPv4/UDP/TCP, in-memory flat file system |
| 11 | [TinyGo and Bare-Metal Specifics](./11_tinygo_baremetal.md) | `scheduler=none` vs `scheduler=tasks`, conservative GC under a custom scheduler, the TinyGo runtime patch, ISR safety |

---

## Coverage matrix — textbook OS topic → chapter

| Textbook topic | Where it lives in this set |
|---|---|
| What an OS does at all | [01](./01_architecture_overview.md) |
| Build / toolchain / how to boot | [02](./02_build_and_run.md) |
| Boot loaders, ROM → kernel handoff, init | [03](./03_boot_and_init.md) |
| CPU descriptors (GDT, IDT, TSS) | [03](./03_boot_and_init.md) |
| Interrupts and ISRs | [03](./03_boot_and_init.md), [10](./10_drivers_filesystem_network.md) |
| Virtual memory, paging, page tables | [04](./04_memory_management.md) |
| Physical memory allocation | [04](./04_memory_management.md) |
| Garbage collection and heap layout | [04](./04_memory_management.md), [11](./11_tinygo_baremetal.md) |
| Threads and scheduling | [05](./05_kernel_thread_runtime.md) |
| Context switching | [05](./05_kernel_thread_runtime.md) |
| Multiprocessor (SMP) bring-up | [06](./06_smp_and_preemption.md) |
| Preemption | [06](./06_smp_and_preemption.md), [05](./05_kernel_thread_runtime.md) |
| Inter-processor communication (IPIs) | [06](./06_smp_and_preemption.md) |
| Processes (creation, exec, wait, exit) | [07](./07_processes_and_userspace.md) |
| ELF loading | [07](./07_processes_and_userspace.md) |
| Privilege levels (Ring 0 vs Ring 3) | [07](./07_processes_and_userspace.md), [08](./08_syscalls.md) |
| System calls | [08](./08_syscalls.md) |
| Signals | [08](./08_syscalls.md) |
| Locks, condition variables, queues | [09](./09_synchronization.md) |
| Deadlock prevention | [09](./09_synchronization.md) |
| Device drivers | [10](./10_drivers_filesystem_network.md) |
| File system | [10](./10_drivers_filesystem_network.md) |
| Networking stack (Ethernet/ARP/IP/UDP/TCP) | [10](./10_drivers_filesystem_network.md) |
| File descriptors, pipes | [10](./10_drivers_filesystem_network.md) |
| User programs, shells, IPC | [07](./07_processes_and_userspace.md), [10](./10_drivers_filesystem_network.md) |
| Language-runtime / GC integration with the kernel | [11](./11_tinygo_baremetal.md) |

---

## Conventions used throughout this set

### Code citations

Pointers into the gooos source tree appear as `path/to/file.go:NNN` — for example, `src/kthread_sched.go:484`. These are **not clickable links**; they are textual coordinates. Open the file in your editor at the cited line to see the implementation.

We use code citations rather than embedded code dumps for two reasons:

1. The code evolves; line numbers age more gracefully than copied excerpts.
2. The reader's editor is a better tool for browsing surrounding context than markdown.

### Abbreviations

Every abbreviation is **expanded on its first occurrence within each chapter file**. So you may see `MMU (Memory Management Unit)` in several chapters — that is intentional. Each chapter is meant to be readable on its own.

The first time a textbook abbreviation appears in a chapter, it is written out in parentheses; subsequent occurrences in that chapter use the short form alone.

### "What / Why / How / Where"

Each major concept in each chapter is explained along four axes:

- **What** — the function or responsibility.
- **Why** — the reason it exists, often the design tradeoff being made.
- **How** — the implementation strategy: data structures, key functions, control flow.
- **Where** — the file and line in the gooos source where you can find it.

When you do not need all four, the chapter will still answer them in some order; this is just the rhythm of the book.

### Cross-references

Every link in this set is **relative** and points within `design_intro_doc/`. There are no links to external URLs, to other directories in the gooos repository, or to any web resource. The intent is that this directory, alone, is enough to understand gooos at the level of "I know what every part is for and where it lives."

---

## Quick start (full details in [Chapter 02](./02_build_and_run.md))

1. Install dependencies: TinyGo 0.40.1, GNU `as`, `ld.lld`, `qemu-system-x86_64`, `grub-mkrescue`, `xorriso`.
2. From the repository root, apply the TinyGo runtime patch:
   ```
   ./scripts/patch_tinygo_runtime.sh
   ```
3. Build the bootable ISO image:
   ```
   make iso
   ```
4. Boot in QEMU with 4 logical CPUs:
   ```
   make run-smp
   ```
5. Watch the serial output until the boot shell prompt appears. Try `ls`, `cat hello`, `ps`, `cpuhog`, `udpecho`, etc.

Boot output you should see:
- Banner from `serialPrintln` calls in `src/main.go`.
- Per-CPU "AP entered scheduler" lines (one per AP).
- "shell ready" marker.
- Shell `$ ` prompt.

If anything fails, [Chapter 02](./02_build_and_run.md) has a troubleshooting section.

---

## What this set is *not*

- **Not a reference manual.** Every claim is anchored in code, but exhaustive symbol-by-symbol coverage is not the goal. The goal is *understanding*.
- **Not a roadmap.** Future work (IOAPIC IRQ steering, dynamic process migration, on-disk file systems, IPv6, etc.) is mentioned only when the *absence* of a feature explains a current design choice.
- **Not a tutorial for writing your own OS.** Reading these documents will give you a clear picture of one design — gooos's. They will not walk you through making your own from scratch.

---

## Status snapshot

The implementation described here corresponds to the **post-Route C / M6 / M7 as-built** state of gooos:

- Kernel runs under `scheduler=none` with hand-rolled `KernelThread` objects ([Chapter 05](./05_kernel_thread_runtime.md)).
- Kernel uniprocessor on the BSP (Bootstrap Processor); APs (Application Processors) come up but stay idle in kernel mode (M6 invariant).
- Ring-3 user processes dispatch on APs only via per-CPU `kschedQueuesRing3[cpu]` queues with round-robin spawn (M7 invariant), excluding the BSP. The boot shell is the lone Ring-3 wrapper hosted on the BSP.
- All hardware interrupts land on the BSP today — IOAPIC steering is deferred.
- User-space programs use TinyGo `scheduler=tasks` and may use cooperative goroutines and channels freely within a single process.
