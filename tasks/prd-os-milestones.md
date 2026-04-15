# PRD: OS Milestone Implementation (Serial through SMP)

## Introduction

Implement the 9 remaining "Planned" milestones from the gooos README Progress table, transforming the project from a GC demo into a minimal but functional operating system kernel. Each milestone is a self-contained learning exercise exploring how far Go (TinyGo) can go as a kernel language, with assembly used only where the hardware demands it. The scope for each milestone is **minimal viable** — the smallest implementation that proves the concept works.

## Goals

- Implement all 9 Planned milestones in dependency-driven order
- Each milestone produces a VGA demo showing the new capability
- Maintain the existing build system (two-step: `as` → `tinygo build -o *.o` → `ld.lld`)
- Keep all kernel logic in Go where possible; use assembly only for CPU-level operations (port I/O, `lidt`, ISR stubs, context switch, `syscall`/`sysret`, SMP trampoline)
- Each milestone follows: design doc → review → implement → code review → verify
- Document TinyGo bare-metal limitations encountered along the way

## Implementation Order (dependency-driven)

```
1. Serial output (COM1)          — no dependencies
2. IDT + interrupt handlers      — no dependencies (enables all IRQ-based features)
3. PIT / timer                   — requires IDT
4. PS/2 keyboard driver          — requires IDT
5. Virtual memory management     — requires IDT (page-fault handler)
6. Scheduler / goroutines        — requires PIT (timer tick for preemption)
7. Userspace                     — requires VM + Scheduler
8. Filesystem                    — requires Userspace (or at least kernel-side file API)
9. SMP                           — requires Scheduler + IDT (APIC)
```

## User Stories

### US-001: Serial Output (COM1)

**Description:** As a developer, I want the kernel to output text to COM1 so that I can view logs in the QEMU terminal (`-serial stdio`) without needing a GUI for the VGA display.

**Acceptance Criteria:**
- [ ] Implement `outb(port, val)` and `inb(port)` as assembly stubs for x86 port I/O
- [ ] Initialize COM1 (0x3F8): set baud rate 115200, 8N1, no flow control
- [ ] Implement `serialPutChar(c byte)` and `serialPrint(s string)` in Go
- [ ] VGA line 0 shows "Serial: OK" and the same message appears on QEMU's `-serial stdio`
- [ ] `make build` succeeds; `make run-kernel` shows serial output in terminal
- [ ] All code comments in English

### US-002: IDT + Interrupt Handlers

**Description:** As a developer, I want the kernel to set up an Interrupt Descriptor Table and handle CPU exceptions and hardware IRQs so that interrupts can be used for timer, keyboard, and page faults.

**Acceptance Criteria:**
- [ ] Define a 256-entry IDT in Go (or assembly data section)
- [ ] Implement `lidt` in assembly to load the IDT register
- [ ] Create ISR stub macros in assembly: save registers, call a Go handler, restore, `iretq`
- [ ] Remap the PIC (8259A) so IRQ0-15 map to vectors 32-47 (avoid conflict with CPU exceptions)
- [ ] Implement a Go-side interrupt dispatcher: `func handleInterrupt(vector uint64)`
- [ ] Handle division-by-zero (vector 0) as a test: display "Exception: #DE" on VGA
- [ ] Enable interrupts with `sti` after IDT is loaded
- [ ] VGA shows "IDT: loaded, 256 entries" and "Interrupts: enabled"
- [ ] `make build` succeeds; no triple-fault when interrupts are enabled

### US-003: PIT / Timer

**Description:** As a developer, I want a hardware timer (PIT) generating periodic interrupts so that the kernel has a notion of time and can implement preemptive scheduling later.

**Acceptance Criteria:**
- [ ] Program PIT channel 0 to fire IRQ0 at ~100 Hz (10ms interval)
- [ ] Increment a global tick counter in the IRQ0 handler
- [ ] VGA shows "Timer: NNNN ticks" that updates live (or shows final count after a short spin)
- [ ] `make build` succeeds; kernel runs without triple-fault for at least several seconds

### US-004: PS/2 Keyboard Driver

**Description:** As a developer, I want the kernel to read keyboard input via the PS/2 controller so that the user can interact with the OS.

**Acceptance Criteria:**
- [ ] Handle IRQ1 (keyboard interrupt) in the IDT
- [ ] Read scancodes from port 0x60 in the IRQ1 handler
- [ ] Implement a minimal scancode set 1 → ASCII mapping (letters, digits, space, enter)
- [ ] Display typed characters on VGA (simple echo to a dedicated line)
- [ ] VGA shows "Keyboard: ready" and echoes keystrokes below
- [ ] `make build` succeeds; typing in QEMU window produces visible output

### US-005: Virtual Memory Management

**Description:** As a developer, I want the kernel to manage virtual memory so that page faults can be handled and kernel/user address spaces can be separated in the future.

**Acceptance Criteria:**
- [ ] Handle page fault (vector 14) in the IDT: display faulting address and error code on VGA
- [ ] Implement `mapPage(vaddr, paddr, flags)` in Go to manipulate the 4-level page table
- [ ] Implement `unmapPage(vaddr)` in Go
- [ ] Demonstrate: map a new page, write to it, unmap it, show success on VGA
- [ ] VGA shows "VM: map/unmap OK"
- [ ] `make build` succeeds; no triple-fault during map/unmap operations

### US-006: Scheduler / Goroutines

**Description:** As a developer, I want a basic cooperative or preemptive task scheduler so that multiple tasks can run concurrently.

**Acceptance Criteria:**
- [ ] Define a Task struct (ID, stack pointer, state, entry function)
- [ ] Implement context switch in assembly (save/restore callee-saved registers + RSP)
- [ ] Implement a round-robin scheduler in Go: `schedule()` picks the next runnable task
- [ ] PIT timer interrupt triggers `schedule()` for preemptive switching
- [ ] Create 2-3 demo tasks that each print to different VGA lines
- [ ] VGA shows interleaved output from multiple tasks proving concurrent execution
- [ ] `make build` succeeds; tasks switch without triple-fault

### US-007: Userspace

**Description:** As a developer, I want the kernel to run a simple user-mode program in Ring 3 so that privilege separation is demonstrated.

**Acceptance Criteria:**
- [ ] Set up a TSS (Task State Segment) with kernel stack pointer for privilege-level transitions
- [ ] Load TSS via `ltr` instruction (assembly)
- [ ] Create a user-mode code/data segment in the GDT (DPL=3)
- [ ] Implement `sysret` or `iretq`-based transition to Ring 3 (assembly)
- [ ] Implement a minimal syscall handler (e.g., `syscall` instruction entry → Go handler → `sysret`)
- [ ] User program calls a "print" syscall that writes to VGA via the kernel
- [ ] VGA shows "User: Hello from Ring 3!"
- [ ] `make build` succeeds; user program runs without GP fault

### US-008: Filesystem

**Description:** As a developer, I want a minimal in-memory filesystem so that the kernel can create, read, and write files.

**Acceptance Criteria:**
- [ ] Implement a simple in-memory filesystem in Go (flat directory, fixed-size file entries)
- [ ] Support operations: `Create(name)`, `Write(name, data)`, `Read(name) []byte`, `List() []string`
- [ ] Demonstrate: create a file, write "hello", read it back, list directory
- [ ] VGA shows "FS: create/write/read OK" and lists files
- [ ] `make build` succeeds

### US-009: SMP (Symmetric Multi-Processing)

**Description:** As a developer, I want the kernel to boot additional CPU cores (Application Processors) so that multi-core execution is demonstrated.

**Acceptance Criteria:**
- [ ] Parse ACPI MADT (or hardcode for QEMU) to discover AP count
- [ ] Write a 16-bit real-mode AP trampoline in assembly (placed below 1 MiB)
- [ ] Send INIT-SIPI-SIPI sequence to APs via the Local APIC
- [ ] Each AP transitions to long mode and increments a shared counter
- [ ] BSP waits for APs to report, then displays count on VGA
- [ ] VGA shows "SMP: N cores online"
- [ ] `make build` succeeds; `make run-kernel` with `-smp 4` shows 4 cores

## Functional Requirements

- FR-1: Each milestone must build with the existing Makefile (`make build`) without modification to build commands (new `.S` or `.go` files are added to `src/` as needed)
- FR-2: Each milestone must produce a VGA demo visible in QEMU
- FR-3: Assembly is used only for operations Go cannot express: port I/O (`in`/`out`), `lidt`, `iretq`, `sti`/`cli`, context switch register save/restore, `syscall`/`sysret`, `ltr`, APIC MMIO, real-mode trampoline
- FR-4: All Go code must work with TinyGo 0.33.0, `gc: "conservative"`, `scheduler: "none"` (until the Scheduler milestone changes it)
- FR-5: Each milestone follows the workflow: design doc (English markdown in `impldoc/`) → subagent review → implement → subagent code review → binary-level verification
- FR-6: Each milestone is committed as a separate git commit (no push without user instruction)
- FR-7: Serial output (once implemented) should be used alongside VGA for logging in subsequent milestones

## Non-Goals

- Production-grade robustness or security
- Full POSIX compatibility
- Networking stack
- Graphics mode (stick to VGA text mode)
- Sound / audio
- USB support
- UEFI boot (stick to BIOS/Multiboot 1)
- Full keyboard layout support (US-ASCII subset is sufficient)
- Disk I/O (filesystem is in-memory only)
- Multi-architecture support (x86_64 only)

## Technical Considerations

- **TinyGo constraints**: `gc: "conservative"`, `scheduler: "none"` (until Scheduler milestone). The `baremetal` build tag must NOT be used. `runtime_unix.go` is compiled because `goos=linux`.
- **Identity map**: The first 1 GiB is identity-mapped via 2 MiB huge pages. The VM milestone may need to switch to 4 KiB page granularity for specific ranges.
- **Heap**: 4 MiB at `0x109000–0x509000`. May need enlargement for later milestones.
- **Stubs**: New assembly stubs (e.g., `outb`, `inb`, `lidt`, ISR entries) are added to `src/stubs.S` or new `.S` files in `src/`.
- **Makefile**: Currently compiles `src/boot.S` and `src/stubs.S`. If new `.S` files are added, the Makefile needs corresponding rules.
- **QEMU flags**: Serial uses `-serial stdio`. SMP uses `-smp N`. These are already in Makefile's `run`/`run-kernel` targets (serial) or can be added.

## Design Considerations

- Each milestone's VGA demo overwrites `src/main.go` (or extends it) to showcase the new feature
- Assembly stubs should be grouped logically: port I/O stubs, IDT stubs, context switch stubs, etc.
- Go-side interrupt handling should use a table-driven dispatcher (`handlers[vector]()`) for extensibility
- The scheduler should start as cooperative (explicit `yield()`) and become preemptive once PIT is wired in

## Success Metrics

- All 9 milestones compile and link with `make build`
- Each milestone's VGA demo runs in QEMU without triple-fault
- Serial output works from milestone 1 onward (visible in `-serial stdio`)
- Keyboard input is echoed on VGA from milestone 4 onward
- Multiple tasks run concurrently from milestone 6 onward
- A user-mode program runs in Ring 3 from milestone 7 onward
- All code reviewed by subagent with zero blockers at each milestone

## Open Questions

1. Should the Scheduler milestone change `target.json` to enable TinyGo's built-in scheduler (`"scheduler": "tasks"` or `"scheduler": "coroutines"`), or should we implement a fully custom scheduler in Go?
2. For the Filesystem milestone, should it be accessible from userspace (via syscall) or kernel-only?
3. For SMP, should we use the Local APIC timer instead of (or in addition to) the PIT for per-core scheduling?
4. How large should the heap be increased for later milestones? 4 MiB may be tight with multiple tasks + filesystem.
