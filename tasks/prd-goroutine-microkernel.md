# PRD: Goroutine-Native Microkernel

## Introduction

Transform gooos from a monolithic demo kernel into a microkernel where Go's concurrency model — channels and select — serves as the native IPC mechanism. Tasks communicate via typed message channels instead of shared memory. OS services (filesystem, keyboard, serial) run as isolated kernel tasks connected by channels. Userspace programs load from ELF binaries and interact with services through a channel-based syscall interface.

This is the direction that only a Go OS can explore: making goroutine-style concurrency the fundamental OS abstraction, not a userspace library feature.

## Goals

- Replace the bump-only page allocator with a free-list allocator that can reclaim pages
- Replace the round-robin scheduler with a wait-queue-based scheduler supporting `yield()`, `sleep()`, and blocking primitives
- Implement typed message channels with bounded buffering, blocking send/recv, and unbuffered rendezvous semantics
- Implement select-like multiplexed waiting across multiple channels
- Decompose monolithic kernel services (filesystem, keyboard, serial) into isolated channel-connected tasks
- Replace the single hardcoded `int 0x80` syscall with a proper register-based syscall ABI and dispatch table
- Load and execute ELF64 binaries from the in-memory filesystem in Ring 3
- (Stretch) Enable SMP scheduling with per-CPU run-queues and work-stealing

## User Stories

### US-001: Page-frame free-list allocator

**Description:** As a kernel developer, I need a page-frame allocator that can both allocate and free 4 KiB pages so that tasks, channels, and page tables can release memory when no longer needed.

**Acceptance Criteria:**
- [ ] `freePage(paddr uintptr)` adds a page back to the free list
- [ ] `allocPage()` first checks the free list before bumping `nextFreePage`
- [ ] Free list is a singly-linked intrusive list (the first 8 bytes of each free page store the next-page pointer)
- [ ] Existing callers of `allocPage()` in `vm.go`, `scheduler.go`, `userspace.go`, `smp.go`, `gdt.go` continue to work unchanged
- [ ] Test: allocate a page, free it, allocate again — returns the same physical address
- [ ] Kernel boots and all existing demos pass in QEMU (`make run`)

### US-002: Wait-queue primitive

**Description:** As a kernel developer, I need a `WaitQueue` data structure so that tasks can block on a condition and be woken by another task or an interrupt handler.

**Acceptance Criteria:**
- [ ] `WaitQueue` struct with an intrusive linked list of waiting task IDs
- [ ] `waitQueueSleep(wq *WaitQueue)` sets current task to `taskBlocked`, enqueues its ID, and calls `schedule()`
- [ ] `waitQueueWakeOne(wq *WaitQueue)` dequeues one task, sets it to `taskReady`
- [ ] `waitQueueWakeAll(wq *WaitQueue)` sets all queued tasks to `taskReady`
- [ ] Operations are safe to call from interrupt context (wakeOne/wakeAll) and from task context (sleep)
- [ ] Test: two tasks — one sleeps on a wait-queue, the other wakes it after a delay; serial log confirms correct wake order

### US-003: Run-queue scheduler with yield and sleep

**Description:** As a kernel developer, I need a scheduler that supports explicit `yield()` and timed `taskSleep(ticks)` so that tasks can voluntarily give up the CPU or sleep for a duration, instead of only being preempted by the PIT timer.

**Acceptance Criteria:**
- [ ] `Task` struct extended with a `wakeupTick uint64` field
- [ ] `yield()` moves current task to end of ready queue and calls `schedule()`
- [ ] `taskSleep(ticks uint64)` computes `wakeupTick = pitTicks + ticks`, sets task to `taskBlocked`, inserts into a sleep queue sorted by `wakeupTick`, and calls `schedule()`
- [ ] Timer IRQ handler checks the sleep queue head; wakes all tasks whose `wakeupTick <= pitTicks`
- [ ] Preemptive round-robin still works for tasks that do not call `yield()` or `taskSleep()`
- [ ] Demo tasks refactored: replace `for pitTicks < target { hlt() }` spin-wait with `taskSleep(N)`
- [ ] Kernel boots and demo tasks switch correctly in QEMU

### US-004: Typed message channels (bounded buffer)

**Description:** As a kernel developer, I need typed message channels so that tasks can send and receive `unsafe.Pointer`-sized messages with blocking semantics, enabling safe inter-task communication without shared mutable state.

**Acceptance Criteria:**
- [ ] `Channel` struct: fixed-capacity ring buffer of `uintptr` slots, sender `WaitQueue`, receiver `WaitQueue`, count, read/write indices, capacity
- [ ] `chanCreate(capacity int) *Channel` allocates a channel; capacity=0 means unbuffered
- [ ] `chanSend(ch *Channel, val uintptr)` blocks calling task if buffer is full; wakes one receiver on successful send
- [ ] `chanRecv(ch *Channel) uintptr` blocks calling task if buffer is empty; wakes one sender on successful receive
- [ ] Unbuffered channel (capacity=0): sender blocks until a receiver calls `chanRecv`, and vice versa (rendezvous)
- [ ] Test: producer task sends 10 messages, consumer task receives them; serial log shows correct order
- [ ] Test: unbuffered channel with two tasks; both block until the other is ready

### US-005: Select-like multiplexed channel wait

**Description:** As a kernel developer, I need a `selectWait` function that blocks a task until any one of multiple channels is ready, so that a service task can listen on multiple input channels simultaneously.

**Acceptance Criteria:**
- [ ] `SelectCase` struct: pointer to `Channel`, direction (send or recv), and value (for send)
- [ ] `selectWait(cases []SelectCase) (index int, val uintptr)` blocks until at least one case is ready, returns the index and received value
- [ ] If multiple cases are ready simultaneously, one is chosen (e.g., first ready in array order)
- [ ] The calling task is registered on all relevant wait-queues and removed from all upon wakeup (no spurious double-wake)
- [ ] Test: a task selects on two channels; messages arrive on each at different times; serial log shows correct dispatch

### US-006: Microkernel service — keyboard

**Description:** As a kernel developer, I want the keyboard driver to run as an isolated kernel task that owns IRQ1 and sends `KeyEvent` messages on a channel, so that keyboard input is decoupled from the interrupt handler.

**Acceptance Criteria:**
- [ ] `KeyEvent` struct with `scancode uint8` and `ascii byte` fields
- [ ] `keyboardChannel` global channel created during init
- [ ] IRQ1 handler reads scancode, translates to ASCII, and calls `chanSend(keyboardChannel, ...)` (non-blocking from interrupt: if buffer full, drop the event)
- [ ] VGA echo logic moved from IRQ handler into a separate consumer task that calls `chanRecv(keyboardChannel)`
- [ ] Serial logging of keystrokes still works
- [ ] Keyboard still echoes characters to VGA correctly in QEMU

### US-007: Microkernel service — filesystem

**Description:** As a kernel developer, I want the filesystem to run as a kernel task that receives request messages and replies on per-request reply channels, so that filesystem access is serialized and decoupled from callers.

**Acceptance Criteria:**
- [ ] `FSRequest` struct with `op uint8` (create/write/read/list), `name string`, `data []byte`, and `replyCh *Channel`
- [ ] `FSResponse` struct with `data []byte`, `names []string`, `ok bool`
- [ ] `fsTaskEntry()` function: loops calling `chanRecv(fsRequestChannel)`, dispatches to existing `fsCreate`/`fsWrite`/`fsRead`/`fsList`, sends result on `replyCh`
- [ ] Helper functions for callers: `fsSendCreate(name) bool`, `fsSendWrite(name, data) bool`, `fsSendRead(name) []byte`, `fsSendList() []string` — each creates a reply channel, sends request, blocks on reply
- [ ] Existing filesystem demo in `main.go` refactored to use the new helpers
- [ ] Serial log confirms create/write/read/list still work correctly

### US-008: Microkernel service — serial output

**Description:** As a kernel developer, I want serial output to be handled by a dedicated kernel task that receives byte-slice messages and drains them to COM1, so that serial writes from multiple tasks are serialized without explicit locking.

**Acceptance Criteria:**
- [ ] `serialChannel` global channel with capacity 16
- [ ] `serialTaskEntry()` function: loops calling `chanRecv`, writes received data to COM1 byte by byte
- [ ] `serialSend(msg string)` helper: sends message to `serialChannel`
- [ ] Direct `serialPrint`/`serialPrintln` calls still available for use from interrupt context (where blocking is not allowed)
- [ ] Boot log and demo output still appear on serial in QEMU

### US-009: Syscall ABI with register-based dispatch

**Description:** As a kernel developer, I need a proper syscall dispatch table with register-based argument passing so that userspace programs can invoke multiple kernel services through `int 0x80`.

**Acceptance Criteria:**
- [ ] Syscall ABI: `rax`=syscall number, `rdi`/`rsi`/`rdx`/`r10`/`r8`/`r9`=arguments, return value in `rax`
- [ ] ISR stub for vector 0x80 saves the full user register frame and passes it to the Go dispatcher
- [ ] Dispatch table indexed by `rax` with at least these syscalls:
  - `sys_yield` (0): yield current task's time slice
  - `sys_exit` (1): terminate current task
  - `sys_send` (2): send message to a channel (channel ID in `rdi`, pointer in `rsi`, length in `rdx`)
  - `sys_recv` (3): receive message from a channel (channel ID in `rdi`, buffer in `rsi`, max length in `rdx`)
  - `sys_spawn` (4): create a new task from an entry address (`rdi`)
  - `sys_print` (5): write string to serial (buffer in `rdi`, length in `rsi`)
- [ ] Invalid syscall numbers return `-1` in `rax`
- [ ] The existing hardcoded syscall 0 (print hello) still works via `sys_print`
- [ ] Test from Ring 3: call `sys_print` with a buffer, verify output on serial

### US-010: ELF64 loader for userspace binaries

**Description:** As a kernel developer, I need an ELF64 loader that reads binaries from the in-memory filesystem and launches them in Ring 3, so that userspace programs are real compiled binaries instead of hardcoded machine code.

**Acceptance Criteria:**
- [ ] `elfLoad(name string) (entryAddr uintptr, err bool)` reads an ELF64 file from the filesystem
- [ ] Verifies ELF magic (`\x7fELF`), class (ELFCLASS64), machine (EM_X86_64), type (ET_EXEC)
- [ ] Iterates `e_phnum` program headers; for each PT_LOAD segment:
  - Allocates user-space virtual pages covering `[p_vaddr, p_vaddr + p_memsz)`
  - Maps pages with user-accessible flags (present | write | user)
  - Copies `p_filesz` bytes from file data to the mapped pages
  - Zeros remaining `p_memsz - p_filesz` bytes (BSS)
- [ ] Allocates a user stack (8 KiB) at a fixed address (e.g., `0x7FFF0000`)
- [ ] Calls `jumpToRing3(e_entry, stackTop)` to start execution
- [ ] Test: store a minimal TinyGo-compiled static ELF in the filesystem, load and execute it; serial shows output from the user program
- [ ] Binaries are TinyGo cross-compiled (`tinygo build -target=...`) with a bare-metal target producing static ELF64

### US-011: SMP per-CPU scheduling (stretch goal)

**Description:** As a kernel developer, I want each Application Processor to have its own run-queue and schedule tasks independently, with work-stealing to balance load, so that the microkernel utilizes all available cores.

**Acceptance Criteria:**
- [ ] Each AP has its own GDT, IDT, TSS loaded during `apEntry()`
- [ ] Per-CPU data accessed via GS-base (`wrmsr` on `IA32_GS_BASE`, MSR 0xC0000101)
- [ ] Per-CPU `currentTask` pointer and local run-queue
- [ ] Work-stealing: when a CPU's run-queue is empty, it attempts to dequeue from another CPU's run-queue (with try-lock to avoid contention)
- [ ] Channel `wakeOne`/`wakeAll` that wake a task on a remote CPU send an IPI (LAPIC ICR, fixed delivery mode) to trigger reschedule on the target CPU
- [ ] Timer IRQ (IRQ0) handled independently on each CPU via Local APIC timer (not PIT, which fires only on BSP)
- [ ] Test with `make run-smp` (4 cores): demo tasks run on different CPUs, serial log shows task switches on multiple CPUs

## Functional Requirements

- FR-1: `freePage(paddr)` returns a page to a free list; `allocPage()` consumes from the free list first
- FR-2: `WaitQueue` supports `sleep`, `wakeOne`, `wakeAll` for arbitrary blocking conditions
- FR-3: `yield()` voluntarily relinquishes the CPU; `taskSleep(ticks)` blocks for a duration
- FR-4: `Channel` supports bounded and unbuffered modes with blocking send/recv
- FR-5: `selectWait(cases)` blocks until any one of multiple channels is ready
- FR-6: Keyboard driver runs as a task, publishes `KeyEvent` on a channel
- FR-7: Filesystem runs as a task, processes `FSRequest` messages, replies on per-request channels
- FR-8: Serial output task serializes writes from multiple producers
- FR-9: Syscall dispatch table with register-based ABI supports at least 6 syscalls
- FR-10: ELF64 loader parses program headers, maps PT_LOAD segments, and launches in Ring 3
- FR-11: (Stretch) Per-CPU run-queues with work-stealing and IPI-based cross-CPU wakeup

## Non-Goals

- No POSIX compatibility (no `fork`, `exec`, `pipe`, `signal`)
- No virtual memory isolation between user processes (all share one address space for now)
- No network stack (covered by Roadmap 2)
- No disk-backed filesystem — the in-memory filesystem is sufficient
- No dynamic linking or shared libraries — ELF binaries are static
- No goroutine runtime from TinyGo — channels and scheduling are implemented from scratch in kernel code
- No UEFI boot — Multiboot 1 / GRUB is sufficient

## Technical Considerations

- **Interrupt-safe channel operations:** `chanSend` from an IRQ handler must not block. The keyboard IRQ handler should use a non-blocking send variant (drop if full) or a sufficiently large buffer.
- **GC interaction with channels:** Channel buffers store `uintptr` values. If they hold heap pointers, the conservative GC should still find them (the buffer is in the `.data`/`.bss` region or heap-allocated). Verify no false-negative collection.
- **Lock ordering for select:** `selectWait` registers on multiple wait-queues. To prevent deadlocks, always acquire wait-queue locks in channel-address order (lowest address first).
- **ELF loading and address space:** User ELF binaries link at addresses above `0x400000` (conventional Linux default). The identity-mapped 1 GiB (boot.S) covers this range, so user code pages must be mapped at 4 KiB granularity with `pageUser` set. A proper per-process address space is out of scope.
- **Syscall register save/restore:** The current ISR stub in `isr.S` saves all GPRs. For syscalls, the Go dispatcher must be able to read arguments from the saved register frame and write the return value back to `rax` in that frame before `iretq`.
- **SMP stretch goal dependencies:** Requires per-CPU LAPIC timer calibration (PIT used for reference), per-CPU GDT/IDT/TSS setup in `apEntry()`, and assembly changes to `switch.S` for GS-base-relative per-CPU data access.

### Key source files to modify

| File | Changes |
|---|---|
| `src/vm.go` | Add `freePage()`, free-list head variable |
| `src/scheduler.go` | Add `WaitQueue`, `yield()`, `taskSleep()`, sleep queue, run-queue refactor |
| `src/main.go` | Refactor init sequence to spawn service tasks, remove inline demos |
| `src/userspace.go` | Syscall dispatch table, register-based ABI, ELF loader |
| `src/fs.go` | Add `fsTaskEntry()`, request/response structs, helper wrappers |
| `src/keyboard.go` | Move to channel-based event publishing |
| `src/serial.go` | Add `serialTaskEntry()`, channel-based output |
| `src/interrupt.go` | Extend `go_interrupt_handler` to pass full register frame for syscalls |
| `src/isr.S` | Modify vector 0x80 stub to pass register frame pointer |
| `src/smp.go` | (Stretch) Per-CPU init, LAPIC timer, IPI wakeup |
| `src/switch.S` | (Stretch) GS-base per-CPU data access |
| **New:** `src/channel.go` | Channel struct, `chanCreate`, `chanSend`, `chanRecv`, `selectWait` |
| **New:** `src/elf.go` | ELF64 parser and loader |

## Success Metrics

- All 3 microkernel services (keyboard, filesystem, serial) run as independent tasks connected by channels
- A TinyGo-compiled ELF binary loads from the in-memory filesystem and executes in Ring 3, calling syscalls
- The kernel boots and all services function correctly in QEMU (`make run`)
- Channel throughput: at least 100,000 messages/second between two tasks (measured via PIT ticks)
- No regression in existing functionality (VGA output, GC, timer, keyboard echo)

## Open Questions

1. Should channels be identified by integer IDs (for syscall ABI) or by pointer (for kernel-internal use)? A hybrid approach (pointer internally, ID table for userspace) may be needed.
2. What is the maximum channel buffer capacity? A fixed upper bound (e.g., 256 slots) simplifies allocation but may limit throughput for high-volume producers.
3. Should `selectWait` support a default (non-blocking) case, or is blocking-only sufficient for the initial implementation?
4. For the ELF loader: should the kernel support only statically linked binaries, or also handle simple relocations (R_X86_64_RELATIVE) for PIE executables?
