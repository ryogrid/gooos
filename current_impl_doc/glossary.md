# Glossary of Abbreviations

An alphabetical glossary of uppercase abbreviations used throughout the gooos
implementation documentation (`impldoc/` and `current_impl_doc/`).

---

## A

### ABI – Application Binary Interface
The low-level calling convention that defines how user-space programs invoke
kernel syscalls.  In gooos the syscall ABI places the syscall number in `RAX`
and arguments in `RDI`, `RSI`, `RDX`, and `R10`, matching a System V–style
convention.

### ACPI – Advanced Configuration and Power Interface
A platform-discovery standard.  gooos parses ACPI tables (specifically the
MADT) during early boot to enumerate LAPIC IDs and IOAPIC addresses, which is
essential for SMP bring-up.

### AP – Application Processor
Any CPU core other than the BSP.  During SMP initialization each AP receives an
INIT–SIPI–SIPI sequence from the BSP and enters a halt loop until work is
available.

### APIC – Advanced Programmable Interrupt Controller
The umbrella term for the modern x86 interrupt-delivery subsystem that replaces
the legacy PIC.  It comprises a per-CPU LAPIC and one or more IOAPICs.

### ASCII – American Standard Code for Information Interchange
The 7-bit character encoding used for keyboard scan-code translation and
console text I/O in gooos.

### ASLR – Address Space Layout Randomisation
A security technique that randomises virtual-address placement.  gooos does
**not** currently implement ASLR; this is listed as a known limitation.

### AST – Abstract Syntax Tree
The intermediate tree representation produced by the Tiny-C interpreter's
parser.  Source code is parsed into an AST node tree, then evaluated
directly.

### ATA – AT Attachment
A disk-interface standard (also known as IDE/PATA).  An ATA block-device driver
is listed as deferred future work in the gooos roadmap.

---

## B

### BIOS – Basic Input/Output System
Legacy x86 firmware.  GRUB relies on BIOS services to load the gooos kernel
image before control is handed to long-mode bootstrap code.

### BSP – Bootstrap Processor
The primary CPU that executes all early boot code—loading the GDT, IDT, page
tables, and ACPI discovery—before waking the APs.

### BSS – Block Started by Symbol
The ELF section that holds zero-initialized (uninitialized) global data.  gooos
linker scripts ensure BSS is placed after DATA and zeroed at startup.

---

## C

### CAS – Compare-And-Swap
An atomic CPU instruction used to implement lock-free data structures and
spinlocks.  gooos uses CAS-based atomics for per-CPU run-queue operations
and the SPSC ring buffer.

### CFS – Completely Fair Scheduler
The Linux kernel's default scheduler, referenced in gooos documentation as a
design comparison point; gooos itself uses a simpler FIFO round-robin model.

### CLI – Clear Interrupt Flag
The x86 `CLI` instruction that disables maskable interrupts.  gooos brackets
critical sections (e.g., ISR entry, scheduler decisions) with `CLI`/`STI`
pairs to prevent re-entrance.

### CR2 – Control Register 2
An x86 control register that holds the faulting virtual address on a page
fault.  The gooos page-fault handler reads CR2 alongside RIP and the error code
for diagnostics.

### CR3 – Control Register 3
An x86 control register that points to the top-level page table (PML4).  gooos
switches CR3 on every user↔kernel transition to enforce per-process address
spaces.

### CRT – C Runtime
The minimal C startup code (`crt0`) linked into user-space ELF binaries so that
the entry point and stack are set up before `main` runs.

---

## D

### DAG – Directed Acyclic Graph
A graph structure referenced in the design documents for dependency analysis
in build and scheduling contexts.

### DMA – Direct Memory Access
A hardware mechanism that lets peripherals read/write memory without CPU
involvement.  Listed as future work in gooos for block-device and network
drivers.

---

## E

### EFER – Extended Feature Enable Register
MSR `0xC0000080`.  gooos sets the LME and NXE bits in EFER during early boot
to enable 64-bit long mode and no-execute page protection.

### ELF – Executable and Linkable Format
The binary format used for gooos user-space programs.  The kernel's `exec`
syscall parses ELF headers, maps PT_LOAD segments, and jumps to the entry
point.

### EOF – End of File
A sentinel condition returned by read syscalls when no more data is available.
In gooos, closing the write end of a pipe causes readers to receive EOF.

### EOI – End of Interrupt
A signal written to the LAPIC (or legacy PIC) to acknowledge that an interrupt
has been serviced, allowing the next interrupt of equal or lower priority to be
delivered.

### EPIPE – Error: Broken Pipe
An error code returned when a process writes to a pipe whose read end has
been closed.

---

## F

### FD – File Descriptor
An integer handle into a process's file-descriptor table.  gooos implements a
per-process FD table supporting console, pipe, and filesystem descriptors.

### FIFO – First-In, First-Out
A queue discipline.  The gooos kernel scheduler maintains a FIFO run queue of
runnable goroutine-based tasks.

### FS – File System
The in-memory (RAM-only) filesystem used by gooos.  It provides a flat
namespace and is lost on reboot.

### FSBASE – FS Segment Base
An MSR that sets the base address of the FS segment register, used internally
by TinyGo and gooos for goroutine-local storage (TLS-like purposes).

---

## G

### GC – Garbage Collection / Garbage Collector
Automatic memory reclamation.  gooos supports two GC modes for user-space
programs: `gc=conservative` (stack/register scanning) and `gc=leaking` (never
frees).

### GDT – Global Descriptor Table
An x86 data structure that defines memory segments (code, data, TSS) and
privilege levels.  gooos rebuilds the GDT during boot to include Ring-3
segments and a per-CPU TSS entry.

### GNU – GNU's Not Unix
The toolchain project.  gooos build scripts reference GNU `ld`, `as`, and
related tools alongside LLVM/TinyGo.

### GRUB – GRand Unified Bootloader
The bootloader used by gooos.  GRUB loads the kernel via Multiboot, sets up
initial protected mode, and hands control to the assembly bootstrap that
transitions to long mode.

---

## H

### HLT – Halt
An x86 instruction that puts the CPU into a low-power state until the next
interrupt arrives.  The gooos idle loop executes `STI; HLT; CLI` to wait for
work.

---

## I

### ICR – Interrupt Command Register
A LAPIC register used to send IPIs.  gooos writes the ICR to issue INIT, SIPI,
and reschedule IPIs to other cores.

### IDT – Interrupt Descriptor Table
A 256-entry x86 table that maps each interrupt/exception vector to its handler
(ISR).  gooos builds the IDT in `idt.go` and installs stubs from `isr.S`.

### INIT – Initialization IPI
The first IPI sent to an AP during the INIT–SIPI–SIPI startup sequence.  It
resets the target processor to a known state.

### IOAPIC – I/O Advanced Programmable Interrupt Controller
A system-level interrupt router that receives external IRQs (keyboard, PIT,
etc.) and delivers them to one or more LAPICs via redirection-table entries.

### IPC – Inter-Process Communication
Mechanisms for data exchange between goroutines or processes.  gooos provides
channels (SPSC ring buffers) and pipes as IPC primitives.

### IPI – Inter-Processor Interrupt
A software-triggered interrupt sent from one CPU to another via the LAPIC ICR.
gooos uses IPIs for AP wake-up (INIT–SIPI–SIPI) and, in planned SMP v2 work,
for cross-CPU scheduler wake-ups.

### IRQ – Interrupt Request
A hardware signal from a device (keyboard, timer, etc.) requesting CPU
attention.  gooos routes IRQs through the IOAPIC or legacy PIC to the
appropriate ISR.

### ISR – Interrupt Service Routine
The handler function invoked when an interrupt or exception fires.  gooos
defines ISR entry points in `isr.S` which save registers into a `SyscallFrame`
before calling Go handlers.

---

## K

### KVM – Kernel-based Virtual Machine
The Linux hypervisor module.  gooos is typically run under QEMU with KVM
acceleration for development and testing.

---

## L

### LAPIC – Local Advanced Programmable Interrupt Controller
The per-CPU interrupt controller that handles IPIs, the LAPIC timer, and
delivery of IOAPIC-routed IRQs.  gooos programs the LAPIC during SMP
initialization.

### LD – Linker
The linker tool (`ld.lld` / GNU `ld`) that combines object files and a linker
script to produce the final kernel or user-space ELF binary.

### LIFO – Last-In, First-Out
A stack discipline.  Referenced in design discussions for deferred-work and
stack-reclaim strategies.

### LMA – Load Memory Address
The physical address at which an ELF segment is loaded.  Used in linker scripts
to separate load-time placement from runtime VMA.

### LME – Long Mode Enable
Bit 8 of the EFER MSR.  Setting LME (together with PAE and PG) activates
x86-64 long mode during gooos early boot.

### LLVM – Low-Level Virtual Machine
The compiler infrastructure behind TinyGo.  gooos user-space programs are
compiled by TinyGo, which uses LLVM as its code-generation backend.

### LVT – Local Vector Table
LAPIC registers (timer, LINT0, LINT1, error, etc.) that control how local
interrupt sources are delivered.

---

## M

### MADT – Multiple APIC Description Table
An ACPI table that lists all LAPICs and IOAPICs in the system.  gooos parses
the MADT to discover per-CPU LAPIC IDs and IOAPIC base addresses.

### MMIO – Memory-Mapped I/O
A technique where device registers are accessed through normal memory
read/write operations at specific physical addresses (e.g., the LAPIC at
`0xFEE00000`).

### MSR – Model-Specific Register
A set of x86 control registers accessible via `RDMSR`/`WRMSR`.  gooos
programs MSRs such as EFER, FSBASE, and LAPIC base.

---

## N

### NMI – Non-Maskable Interrupt
A high-priority interrupt that cannot be disabled by `CLI`.  gooos accounts for
NMI in its IDT setup.

### NXE – No-eXecute Enable
Bit 11 of the EFER MSR.  When set, page-table entries can mark pages as
non-executable, providing a basic defense against code-injection attacks.

---

## O

### OOM – Out of Memory
A condition where no more physical or virtual memory can be allocated.  gooos
currently panics on OOM rather than gracefully reclaiming memory.

---

## P

### PAE – Physical Address Extension
An x86 paging mode that extends physical addresses to 36+ bits and is a
prerequisite for long mode.  gooos enables PAE before setting LME.

### PD – Page Directory
The second-level page-table structure in x86-64 four-level paging (below PDP,
above PT).

### PDP – Page Directory Pointer
The third-level page-table structure sitting between PML4 and PD in x86-64
paging.

### PIC – Programmable Interrupt Controller
The legacy Intel 8259 interrupt controller.  gooos initializes (and masks) the
PIC before switching to the APIC for interrupt delivery.

### PID – Process ID
A numeric identifier for each user-space process.  gooos assigns PIDs when
loading ELF binaries via `exec`.

### PIE – Position-Independent Executable
An ELF binary whose code can run at any virtual address without modification.
Relevant to user-space linking options in gooos.

### PIT – Programmable Interval Timer
The Intel 8253/8254 timer chip.  gooos programs the PIT at 100 Hz to generate
the system tick used by the scheduler.

### PML4 – Page Map Level 4
The top-level page-table structure in x86-64 four-level paging.  CR3 points to
the PML4.

### POSIX – Portable Operating System Interface
A family of standards for Unix-like OS APIs.  gooos implements a small
POSIX-ish subset (e.g., line/word/byte counts in `wc`).

### PT – Page Table
The lowest-level page-table structure containing individual page-table entries
(PTEs) that map virtual pages to physical frames.

### PTE – Page Table Entry
A single entry in a page table that encodes the physical frame address and
permission bits (read/write, user/supervisor, no-execute, etc.).

---

## Q

### QEMU – Quick EMUlator
The primary emulation/virtualisation platform for developing and testing gooos.
QEMU with KVM provides the reference x86-64 virtual machine.

---

## R

### RAM – Random-Access Memory
Volatile main memory.  The gooos filesystem is RAM-only and all data is lost on
reboot.

### REPL – Read-Eval-Print Loop
An interactive execution mode planned (deferred) for the Tiny-C interpreter.

### RSDP – Root System Description Pointer
The initial ACPI structure located by scanning BIOS memory regions.  gooos
follows RSDP → RSDT → MADT to discover multi-processor topology.

### RSDT – Root System Description Table
The ACPI table referenced by the RSDP that contains pointers to other ACPI
tables such as the MADT.

---

## S

### SDK – Software Development Kit
The `user/gooos/` package that provides user-space helper functions (syscall
wrappers, console I/O, file operations) for writing gooos applications.

### SIPI – Startup IPI
The second IPI in the INIT–SIPI–SIPI sequence.  It provides the real-mode
entry-point address for an AP and must be sent twice per the Intel
specification.

### SMAP – Supervisor Mode Access Prevention
A CPU feature that prevents the kernel from accidentally reading/writing
user-space memory.  gooos does **not** yet enable SMAP.

### SMEP – Supervisor Mode Execution Prevention
A CPU feature that prevents the kernel from executing user-space code.  gooos
does **not** yet enable SMEP.

### SMI – System Management Interrupt
A special interrupt that enters System Management Mode (firmware context).
Mentioned in hardware-interrupt documentation as a non-maskable event.

### SMP – Symmetric Multi-Processing
Running the OS kernel across multiple CPU cores.  gooos implements SMP v1
(APs discovered and halted) with v2 (per-CPU run queues, IPI-based wake-up)
planned.

### SPSC – Single-Producer, Single-Consumer
A lock-free ring-buffer design used for gooos IPC channels.  Each channel is
backed by a fixed-size SPSC ring allocated in BSS.

### SSE – Streaming SIMD Extensions
x86 vector instructions.  gooos keeps SSE disabled; some Go standard-library
packages (e.g., `time`) break because they rely on SSE.

### STI – Set Interrupt Flag
The x86 `STI` instruction that re-enables maskable interrupts after a `CLI`.

### STDIN – Standard Input
File descriptor 0.  gooos provides `consoleStdin` as the default STDIN for the
foreground process.

### STDOUT – Standard Output
File descriptor 1.  gooos provides `consoleStdout` as the default STDOUT for
the foreground process.

---

## T

### TLB – Translation Lookaside Buffer
A CPU cache of recent virtual-to-physical address translations.  gooos must
perform TLB shootdowns (via IPI) when page-table entries change on SMP
systems.

### TSO – Total Store Order
The x86 memory-ordering model that guarantees stores are visible in program
order.  gooos relies on TSO to simplify certain lock-free algorithms.

### TSS – Task State Segment
A per-CPU x86 structure that stores the kernel stack pointer (`RSP0`).  On
every user→kernel transition the CPU loads RSP0 from the TSS to switch to the
kernel stack.

### TUI – Text User Interface
A terminal-based visual interface.  gooos includes a vi-like modal TUI editor
as a user-space application.

---

## U

### UART – Universal Asynchronous Receiver/Transmitter
A serial-port controller.  gooos initializes a UART for early debug output and
serial console access.

### USB – Universal Serial Bus
A peripheral bus standard.  gooos currently supports only PS/2 keyboard input;
USB HID support is listed as future work.

---

## V

### VGA – Video Graphics Array
The legacy text-mode display at physical address `0xB8000`.  gooos writes
directly to the VGA text buffer for console output.

### VMA – Virtual Memory Address
The runtime virtual address at which an ELF segment is mapped, as opposed to
its LMA (load-time physical address).

---
## Auto-Extracted Uppercase Keywords Index

This index is generated from all Markdown files in the repository using the regex `/\b[A-Z]{2,}\b/`.
The following terms appeared in Markdown but do not yet have a dedicated expanded glossary entry above.
Because this is a raw regex extraction, it intentionally includes non-abbreviation uppercase tokens as well.

- AB
- ABANDONED
- ACK
- AD
- AFTER
- AHCI
- ALIGN
- ALL
- ALLOC
- AND
- ANY
- API
- APICID
- APPEND
- ARG
- ARM
- ARP
- AS
- ASDE
- AT
- AVX
- BA
- BAR
- BDA
- BEFORE
- BLOCKING
- BNF
- BOOTP
- BOOTREPLY
- BOOTREQUEST
- BOTH
- BREAKING
- BROKEN
- BSIZE
- BUILD
- BUT
- CA
- CASE
- CC
- CD
- CHANGELOG
- CHOSEN
- CI
- CLAUDE
- CLOSED
- CLOSING
- CMD
- CMDS
- CODE
- COMMON
- COMPLETE
- CONTENTS
- COUNT
- CPU
- CRC
- CRITICAL
- CRTC
- CS
- CSO
- CSS
- CTRL
- CWD
- CWR
- DATA
- DCE
- DD
- DE
- DEFERRED
- DGRAM
- DHCP
- DHCPACK
- DHCPDISCOVER
- DHCPNAK
- DHCPOFFER
- DHCPREQUEST
- DISCARD
- DISCOVER
- DM
- DNS
- DO
- DORA
- DOWN
- DPL
- DS
- DSCP
- EAGAIN
- EAX
- EC
- ECE
- ECN
- ECX
- EDX
- EEPROM
- EFI
- ELFS
- ELSE
- EM
- END
- ENTRY
- EOP
- ES
- ESTABLISHED
- EVERY
- EXEC
- EXISTS
- EXIT
- FAIL
- FATAL
- FCS
- FE
- FF
- FILE
- FILENAME
- FIN
- FINAL
- FIRST
- FIXME
- FLIP
- FOR
- FOUND
- FP
- FR
- FROM
- FXSAVE
- GAP
- GAS
- GB
- GDB
- GDTR
- GET
- GOOOS
- GOOS
- GP
- GPR
- GS
- GSI
- GTK
- GUI
- GW
- HACK
- HEAD
- HID
- HIGH
- HMP
- HOME
- HTTP
- HUGE
- HW
- IA
- ICMP
- ID
- IDE
- IDENT
- IDTR
- IF
- IFCS
- IHL
- II
- IMC
- IMS
- INDEPENDENT
- INFORM
- INSERT
- INSIDE
- INT
- INVPCID
- IO
- IOAPICID
- IOAPICVER
- IOMMU
- IOREDTBL
- IOREGSEL
- IOWIN
- IP
- IRR
- IS
- ISA
- ISN
- ISO
- ITR
- JSON
- KB
- KBD
- KEEP
- KERNEL
- LANDED
- LDSCRIPT
- LICENSE
- LINT
- LISTEN
- LIVE
- LOAD
- LOC
- LOG
- LR
- LRU
- LSB
- LSC
- LU
- MAC
- MAJOR
- MAKE
- MB
- MCP
- MECHANISM
- MF
- MINOR
- MIT
- MM
- MMX
- MN
- MON
- MP
- MSL
- MSS
- MTA
- MTU
- MUST
- NAK
- NAME
- NAT
- NDP
- NEEDED
- NET
- NEVER
- NEW
- NEXT
- NF
- NIC
- NNN
- NNNN
- NO
- NOBITS
- NOLOAD
- NON
- NOP
- NORMAL
- NOT
- NOTE
- NOW
- NS
- NUL
- NULL
- NUMBER
- OFF
- OFFER
- OK
- OLD
- ONE
- ONLY
- OOB
- OR
- ORIGINAL
- OS
- OSFXSR
- OSXMMEXCPT
- OTHER
- OUT
- OUTSIDE
- OVERFLOW
- OWN
- PARTIAL
- PASS
- PATA
- PATCHES
- PAUSE
- PC
- PCB
- PCD
- PCI
- PF
- PG
- PHDRS
- PHONY
- PING
- PMTUD
- PORT
- POST
- PPID
- PR
- PRD
- PRESENT
- PRINTLN
- PROCESSES
- PS
- PSH
- PTB
- PWT
- RAH
- RAL
- RAX
- RBP
- RBX
- RCTL
- RCX
- RDBAH
- RDBAL
- RDH
- RDI
- RDLEN
- RDMSR
- RDT
- RDX
- README
- READONLY
- RECEIVE
- RECOMMENDED
- RECV
- RELEASE
- RELOCATION
- REPLY
- REQUEST
- REQUIRED
- RESOLVED
- RETURN
- RFC
- RFLAGS
- RIP
- RISC
- RISK
- RKL
- RKR
- RMW
- RNG
- ROM
- RPL
- RQ
- RS
- RSI
- RSP
- RST
- RTO
- RTT
- RTTVAR
- RW
- RWX
- RX
- SACK
- SAFE
- SCHED
- SE
- SECRC
- SECTIONS
- SEND
- SET
- SHA
- SHARED
- SHOULD
- SIGALRM
- SIGILL
- SIGINT
- SIGKILL
- SIGPIPE
- SIGSEGV
- SIGTERM
- SIGURG
- SIMD
- SLU
- SMSS
- SOMAXCONN
- SP
- SPA
- SPECIAL
- SQ
- SRCS
- SRTT
- SS
- STACK
- STALE
- STATE
- STATUS
- STOP
- STREAM
- STRING
- SUBSUMED
- SUITABLE
- SUPERSEDED
- SVR
- SWS
- SYN
- SYSV
- TAP
- TARGET
- TB
- TBD
- TCB
- TCG
- TCP
- TCTL
- TD
- TDBAH
- TDBAL
- TDH
- TDLEN
- TDT
- TEST
- TESTS
- THA
- THIS
- TICKS
- TIMEOUT
- TIMEWAIT
- TINYGO
- TINYGOROOT
- TLS
- TLV
- TN
- TO
- TODO
- TOP
- TOS
- TPA
- TQ
- TR
- TTAS
- TTL
- TX
- TXDW
- UAF
- UDP
- UEFI
- UI
- UNCERTAIN
- UNCHANGED
- UNSUITABLE
- UP
- UPPER
- URG
- US
- USER
- UX
- VA
- VAR
- VLAN
- VM
- VNC
- WASM
- WC
- WHILE
- WIP
- WITH
- WITHOUT
- WNOHANG
- WRITE
- WRMSR
- WUNTRACED
- XID
- XOR
- XX
- XXX
- XXXXXX
- YY
- YYYY
- YYYYMMDD
