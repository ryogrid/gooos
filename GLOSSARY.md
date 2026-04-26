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

### ACK – Acknowledgment
In TCP, the ACK flag and acknowledgment number confirm receipt of peer data.
gooos networking docs and tests use ACK-driven state progression in the TCP
state machine.

### AP – Application Processor
Any CPU core other than the BSP.  During SMP initialization each AP receives an
INIT–SIPI–SIPI sequence from the BSP and enters a halt loop until work is
available.

### APIC – Advanced Programmable Interrupt Controller
The umbrella term for the modern x86 interrupt-delivery subsystem that replaces
the legacy PIC.  It comprises a per-CPU LAPIC and one or more IOAPICs.

### APICID – APIC Identifier
The hardware identifier assigned to a CPU's LAPIC.  gooos uses APICID values
from ACPI MADT discovery and runtime snapshots to target IPIs per core.

### API – Application Programming Interface
The callable surface exposed by a module.  In gooos this term appears for both
kernel-internal interfaces and user-space syscall wrapper interfaces in the
SDK.

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

### BAR – Base Address Register
A PCI configuration-space register that advertises a device's MMIO or I/O
region base address.

### BAR0 – Base Address Register 0
The first PCI BAR.  gooos uses e1000 BAR0 as the MMIO base for NIC register
access.

### BIOS – Basic Input/Output System
Legacy x86 firmware.  GRUB relies on BIOS services to load the gooos kernel
image before control is handed to long-mode bootstrap code.

### BSP – Bootstrap Processor
The primary CPU that executes all early boot code—loading the GDT, IDT, page
tables, and ACPI discovery—before waking the APs.

### BSS – Block Started by Symbol
The ELF section that holds zero-initialized (uninitialized) global data.  gooos
linker scripts ensure BSS is placed after DATA and zeroed at startup.

### BDA – BIOS Data Area
A legacy memory region populated by BIOS with platform metadata.  x86 boot-time
code and ACPI discovery discussions reference BDA/EBDA scanning boundaries.

### BOOTP – Bootstrap Protocol
The predecessor protocol to DHCP for host boot-time network configuration.
Mentioned in gooos network documentation around DHCP packet semantics.

### BSIZE – Buffer Size
A size field used in device and protocol configuration, including e1000 RX
buffer-size related settings.

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

### COM1 – Communications Port 1
The first PC serial port (typically I/O base `0x3F8`).  gooos uses COM1 for
early UART debug output and serial-console interaction.

### CPU – Central Processing Unit
The processor core that executes kernel and user code.  gooos heavily uses
per-CPU state (run queues, TSS fields, LAPIC metadata) in SMP-related paths.

### CSO – Checksum Offset
A descriptor field indicating checksum placement/start offset in NIC
offload-capable transmit paths.

### CR2 – Control Register 2
An x86 control register that holds the faulting virtual address on a page
fault.  The gooos page-fault handler reads CR2 alongside RIP and the error code
for diagnostics.

### CR3 – Control Register 3
An x86 control register that points to the top-level page table (PML4).  gooos
switches CR3 on every user↔kernel transition to enforce per-process address
spaces.

### CR0 – Control Register 0
An x86 control register containing global CPU mode bits (for example PE/PG).
gooos boot code updates CR0 as part of protected-mode and paging enablement.

### CR4 – Control Register 4
An x86 control register that enables extended architectural features (for
example PAE).  gooos configures CR4 during long-mode bring-up.

### CWR – Congestion Window Reduced
A TCP control flag used by ECN-capable peers to signal congestion-window
reduction.

### CRT – C Runtime
The minimal C startup code (`crt0`) linked into user-space ELF binaries so that
the entry point and stack are set up before `main` runs.

---

## D

### DAG – Directed Acyclic Graph
A graph structure referenced in the design documents for dependency analysis
in build and scheduling contexts.

### DCE – Data Communication Equipment
A serial-link role term used in modem/control-line conventions.

### DHCP – Dynamic Host Configuration Protocol
An IPv4 network-configuration protocol.  gooos networking docs and demos use
DHCP exchanges (DISCOVER/OFFER/REQUEST/ACK) to obtain runtime IP settings.

### DMA – Direct Memory Access
A hardware mechanism that lets peripherals read/write memory without CPU
involvement.  Listed as future work in gooos for block-device and network
drivers.

### DPL – Descriptor Privilege Level
The privilege level field in x86 segment and gate descriptors.  gooos IDT/GDT
setup uses DPL values to control user vs kernel access to entries.

### DNS – Domain Name System
The naming system that resolves hostnames to IP addresses.  DNS support is
documented in gooos networking plans and user-space tooling discussions.

### DLAB – Divisor Latch Access Bit
A UART line-control bit that switches serial-port registers to baud-divisor
access mode.

### DSCP – Differentiated Services Code Point
An IPv4 header traffic-class field (within TOS/DS field) used for QoS marking.

### DTR – Data Terminal Ready
A serial modem-control signal bit used when bringing up COM ports.

---

## E

### EFER – Extended Feature Enable Register
MSR `0xC0000080`.  gooos sets the LME and NXE bits in EFER during early boot
to enable 64-bit long mode and no-execute page protection.

### EAX – Extended Accumulator Register
A 32-bit general-purpose x86 register (low half of RAX).  Appears in x86 ABI
and low-level trap/assembly discussions.

### EBDA – Extended BIOS Data Area
A BIOS-managed memory region typically referenced from the BDA.  ACPI RSDP
search procedures commonly include EBDA-based scanning.

### ECX – Extended Counter Register
A 32-bit general-purpose x86 register (low half of RCX).  Used in low-level
assembly paths and calling-convention contexts.

### ECE – ECN-Echo
A TCP control flag used with ECN to report congestion signaling.

### EDF – Earliest Deadline First
A scheduling policy that prioritizes the runnable entity with the nearest
deadline.

### EDX – Extended Data Register
A 32-bit general-purpose x86 register (low half of RDX).  Appears in assembly
stubs and CPU feature/ABI-oriented discussions.

### EFI – Extensible Firmware Interface
Modern firmware interface standard (superseded by UEFI naming).  Mentioned in
boot-flow context as an alternative to legacy BIOS environments.

### ELF – Executable and Linkable Format
The binary format used for gooos user-space programs.  The kernel's `exec`
syscall parses ELF headers, maps PT_LOAD segments, and jumps to the entry
point.

### ELF64 – 64-bit Executable and Linkable Format
The 64-bit ELF class variant used by x86-64 binaries.  gooos user/kernel build
artifacts and loader checks reference ELF64-specific header values.

### ELFCLASS64 – ELF Class 64
The ELF identification constant indicating a 64-bit object format.  Used when
validating ELF headers before loading user programs.

### ELFDATA2LSB – ELF Little-Endian Data Encoding
The ELF identification constant indicating little-endian byte order.  gooos ELF
loader checks this field to reject incompatible binaries.

### EOF – End of File
A sentinel condition returned by read syscalls when no more data is available.
In gooos, closing the write end of a pipe causes readers to receive EOF.

### EOI – End of Interrupt
A signal written to the LAPIC (or legacy PIC) to acknowledge that an interrupt
has been serviced, allowing the next interrupt of equal or lower priority to be
delivered.

### EOP – End of Packet
A NIC descriptor/control bit indicating the final descriptor fragment for one
transmitted packet.

### ESTABLISHED – TCP Established State
The TCP connection state after a successful handshake, where full-duplex data
transfer is permitted.

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

### FCS – Frame Check Sequence
The Ethernet trailer checksum (CRC-32) used to detect frame corruption.

### FS – File System
The in-memory (RAM-only) filesystem used by gooos.  It provides a flat
namespace and is lost on reboot.

### FSBASE – FS Segment Base
An MSR that sets the base address of the FS segment register, used internally
by TinyGo and gooos for goroutine-local storage (TLS-like purposes).

---

## G

### GDB – GNU Debugger
A source-level debugger used for low-level kernel troubleshooting.  gooos
development notes reference GDB/QEMU debugging workflows.

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

### HMP – Human Monitor Protocol
QEMU's monitor command interface used for runtime VM inspection/control.  gooos
test scripts reference HMP for scripted diagnostics.

---

## I

### ICR – Interrupt Command Register
A LAPIC register used to send IPIs.  gooos writes the ICR to issue INIT, SIPI,
and reschedule IPIs to other cores.

### ICMP – Internet Control Message Protocol
An IPv4 control protocol used for diagnostics such as echo request/reply.
gooos network documents reference ICMP as part of the IPv4 stack scope.

### IDT – Interrupt Descriptor Table
A 256-entry x86 table that maps each interrupt/exception vector to its handler
(ISR).  gooos builds the IDT in `idt.go` and installs stubs from `isr.S`.

### IDTR – Interrupt Descriptor Table Register
The x86 register that holds the IDT base and limit loaded by `LIDT`.  gooos
trap setup relies on correct IDTR installation per CPU.

### INIT – Initialization IPI
The first IPI sent to an AP during the INIT–SIPI–SIPI startup sequence.  It
resets the target processor to a known state.

### IHL – Internet Header Length
The IPv4 header-length field that encodes header size in 32-bit words.  gooos
currently handles unfragmented packets with baseline `IHL=5` headers.

### IMS – Interrupt Mask Set
An e1000 register used to unmask selected NIC interrupt causes.

### IMC – Interrupt Mask Clear
The e1000 register used to mask (disable) selected interrupt causes.

### IFCS – Insert Frame Check Sequence
A NIC transmit option that appends Ethernet FCS in hardware.

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

### IRQ0 – Interrupt Request 0
The legacy PIC timer interrupt line, historically mapped from PIT ticks.  gooos
docs mention IRQ0 when comparing PIC-era and APIC-era timer routing.

### IRQ1 – Interrupt Request 1
The legacy PIC keyboard interrupt line.  gooos keyboard handling docs and test
harnesses frequently reference IRQ1 delivery behavior.

### ISR – Interrupt Service Routine
The handler function invoked when an interrupt or exception fires.  gooos
defines ISR entry points in `isr.S` which save registers into a `SyscallFrame`
before calling Go handlers.

### ISN – Initial Sequence Number
The starting TCP sequence number used when establishing a connection.  gooos
TCP-state and retransmission notes refer to ISN handling.

### ISO – International Organization for Standardization
The standards body referenced in systems and tooling documentation contexts.

---

## J

### JSON – JavaScript Object Notation
A text-based structured data format used by tools and configuration/reporting
artifacts.

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

### LISTEN – TCP Listen State
A passive-open TCP state in which a socket waits for incoming SYN segments.

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

### LINT0 – Local Interrupt Input 0
A LAPIC local interrupt pin configured via the LVT.  Mentioned in gooos APIC
initialization and interrupt-routing documentation.

### LINT1 – Local Interrupt Input 1
The second LAPIC local interrupt pin configured via the LVT, often associated
with NMI delivery paths in x86 setups.

---

## M

### MAC – Media Access Control
The link-layer addressing scheme used by Ethernet.  gooos networking code and
docs reference source/destination MAC addresses in frame handling.

### MADT – Multiple APIC Description Table
An ACPI table that lists all LAPICs and IOAPICs in the system.  gooos parses
the MADT to discover per-CPU LAPIC IDs and IOAPIC base addresses.

### MMIO – Memory-Mapped I/O
A technique where device registers are accessed through normal memory
read/write operations at specific physical addresses (e.g., the LAPIC at
`0xFEE00000`).

### MPSC – Multi-Producer, Single-Consumer
A queue/ring design where multiple writers feed one reader.  gooos design notes
contrast MPSC patterns with SPSC channel rings.

### MSS – Maximum Segment Size
The maximum TCP payload size in one segment.  gooos TCP design and test docs
refer to MSS in segmentation and flow-control behavior.

### MSR – Model-Specific Register
A set of x86 control registers accessible via `RDMSR`/`WRMSR`.  gooos
programs MSRs such as EFER, FSBASE, and LAPIC base.

### MTA – Multicast Table Array
An e1000 register block used to filter multicast destination addresses.

### MTU – Maximum Transmission Unit
The largest payload size a network interface transmits as a single frame.
gooos network diagnostics reference MTU when discussing fragmentation limits.

---

## N

### NMI – Non-Maskable Interrupt
A high-priority interrupt that cannot be disabled by `CLI`.  gooos accounts for
NMI in its IDT setup.

### NIC – Network Interface Controller
A hardware network adapter.  gooos networking paths and diagnostics discuss NIC
RX/TX behavior for the emulated e1000 device.

### NUL – Null Character
The zero byte (`\0`) used as a C-style string terminator and separator.

### NET – Network
Shorthand used in gooos documents and scripts for networking subsystems and
tests.

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

### PCD – Page-Level Cache Disable
A page-attribute bit used to disable caching for mapped memory regions.

### PCI – Peripheral Component Interconnect
The bus standard used to discover and configure devices such as NICs.  gooos
network-driver work references PCI config-space probing for e1000 bring-up.

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

### PCB – Process Control Block
The per-process kernel bookkeeping record (register context, memory mappings,
FD table state, and lifecycle metadata).

### PPID – Parent Process ID
The process identifier of a process's parent.  gooos process-management notes
and shell semantics mention PPID-style parent/child relationships.

### PSH – Push
A TCP control flag requesting immediate delivery of queued data to the
receiving application.

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

### PWT – Page-Level Write-Through
A page-attribute bit used to request write-through caching policy.

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

### RFC – Request for Comments
The standards-document series used to define Internet protocols.  gooos network
design notes cite RFC behavior as an interoperability reference.

### RIP – Instruction Pointer Register
The x86-64 register holding the next instruction address.  gooos trap logs and
page-fault diagnostics report RIP for crash triage.

### RSDP – Root System Description Pointer
The initial ACPI structure located by scanning BIOS memory regions.  gooos
follows RSDP → RSDT → MADT to discover multi-processor topology.

### RSDT – Root System Description Table
The ACPI table referenced by the RSDP that contains pointers to other ACPI
tables such as the MADT.

### RCX – Register CX (64-bit)
The 64-bit general-purpose x86 register whose low 32-bit portion is ECX.
gooos low-level ABI and trap discussions reference RCX in register snapshots.

### R10 – Register 10
A caller-clobbered x86-64 general-purpose register used by the syscall ABI for
the fourth argument.

### RAX – Register AX (64-bit)
The primary x86-64 accumulator register used by gooos syscall ABI for syscall
numbers and return values.

### RDI – Register DI (64-bit)
A general-purpose x86-64 register used by gooos syscall ABI for the first
argument.

### RDX – Register DX (64-bit)
A general-purpose x86-64 register used by gooos syscall ABI for the third
argument.

### RSI – Register SI (64-bit)
A general-purpose x86-64 register used by gooos syscall ABI for the second
argument.

### RSP – Stack Pointer Register
The x86-64 register pointing to the current stack top.  gooos context-switch
and ISR entry/exit paths preserve and restore RSP across transitions.

### RSP0 – Ring-0 Stack Pointer
The kernel stack pointer field stored in the TSS and loaded by hardware on
user-to-kernel privilege transitions.

### RST – Reset
In TCP, a reset segment that abruptly terminates a connection or rejects an
invalid flow state.  gooos TCP docs use RST in state-machine explanations.

### RFLAGS – Flags Register
The x86-64 status/control register containing condition and interrupt-enable
bits.  gooos trap and context-switch code preserves RFLAGS across transitions.

### RTO – Retransmission Timeout
The TCP timer threshold after which unacknowledged data is retransmitted.
gooos TCP implementation plans discuss RTO behavior for reliability.

### RTT – Round-Trip Time
The observed elapsed time between transmission and acknowledgment reception.
gooos TCP tuning notes reference RTT for timeout estimation.

### RTTVAR – RTT Variation
The TCP estimator tracking RTT variance, typically combined with SRTT to derive
RTO.

### R11 – Register 11
A caller-clobbered x86-64 general-purpose register.  Appears in syscall/trap
save-restore paths.

### R12 – Register 12
A callee-saved x86-64 general-purpose register used in context-switch state.

### R13 – Register 13
A callee-saved x86-64 general-purpose register tracked in kernel thread context
frames.

### RAH0 – Receive Address High 0
The high 32-bit e1000 register for primary station MAC address storage.

### RAL0 – Receive Address Low 0
The low 32-bit e1000 register for primary station MAC address storage.

### RCTL – Receive Control
An e1000 receive-path control register that configures RX enable and filtering
behavior.

### RPL – Requested Privilege Level
The privilege-level bits in an x86 segment selector, used in ring-transition
checks.

### RDBAH – Receive Descriptor Base Address High
The high 32-bit e1000 register of the RX descriptor ring base pointer.

### RDBAL – Receive Descriptor Base Address Low
The low 32-bit e1000 register of the RX descriptor ring base pointer.

### RDH – Receive Descriptor Head
The e1000 RX ring head index managed by hardware.

### RDLEN – Receive Descriptor Length
The e1000 register that stores total RX descriptor ring size in bytes.

### RDT – Receive Descriptor Tail
The e1000 RX ring tail index managed by software.

### RSS – Receive Side Scaling
A NIC feature for distributing receive processing across multiple queues/CPUs.

### RXT0 – Receive Timer Interrupt 0
An e1000 interrupt cause/mask bit used for receive-related interrupt events.

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

### SACK – Selective Acknowledgment
TCP extension allowing acknowledgment of non-contiguous data ranges.  gooos TCP
design notes reference SACK compatibility and behavior.

### SMP – Symmetric Multi-Processing
Running the OS kernel across multiple CPU cores.  gooos implements SMP v1
(APs discovered and halted) with v2 (per-CPU run queues, IPI-based wake-up)
planned.

### SHA – Secure Hash Algorithm
A family of cryptographic hash functions.  Appears in tooling/integrity notes
within the project documentation.

### SECRC – Strip Ethernet CRC
An e1000 receive option that removes the Ethernet FCS from delivered buffers.

### SPSC – Single-Producer, Single-Consumer
A lock-free ring-buffer design used for gooos IPC channels.  Each channel is
backed by a fixed-size SPSC ring allocated in BSS.

### SRTT – Smoothed Round-Trip Time
The filtered RTT estimator used by TCP timeout algorithms.  gooos TCP timing
notes reference SRTT in RTO calculation.

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

### SWS – Silly Window Syndrome
A TCP inefficiency pattern caused by tiny advertised windows and small sends.
gooos TCP design discussions mention SWS while describing flow behavior.

### SPA – Sender Protocol Address
The ARP field containing the sender's protocol-layer address (IPv4 in gooos).

### SYN – Synchronize
The TCP control flag used during connection establishment.  gooos TCP state
machine documentation references SYN and SYN/ACK transitions.

---

## T

### TCB – Transmission Control Block
The per-connection state record for a TCP endpoint (sequence numbers, timers,
window/state fields).  gooos TCP design notes reference TCB-style connection
tracking.

### TCG – Tiny Code Generator
QEMU's built-in software CPU emulation backend used when hardware acceleration
such as KVM is unavailable.

### TCP – Transmission Control Protocol
A reliable, connection-oriented transport protocol over IPv4.  gooos includes
TCP stack implementation work, state-machine docs, and socket-level tests.

### TCTL – Transmit Control
An e1000 register that enables and configures transmit-path behavior.

### TDBAH – Transmit Descriptor Base Address High
The high 32-bit e1000 register of the TX descriptor ring base pointer.

### TDBAL – Transmit Descriptor Base Address Low
The low 32-bit e1000 register of the TX descriptor ring base pointer.

### TDH – Transmit Descriptor Head
The e1000 TX ring head index managed by hardware.

### TDLEN – Transmit Descriptor Length
The e1000 register that stores total TX descriptor ring size in bytes.

### TDT – Transmit Descriptor Tail
The e1000 TX ring tail index managed by software.

### TCPLISTEN – TCP Listen State
The passive-open state where a socket waits for inbound SYN requests.  gooos
network docs and demos reference TCPLISTEN behavior.

### TINYGOROOT – TinyGo Root Directory
The root path for TinyGo's runtime/toolchain files used during builds.  gooos
build scripts and environment setup docs reference TINYGOROOT.

### TLB – Translation Lookaside Buffer
A CPU cache of recent virtual-to-physical address translations.  gooos must
perform TLB shootdowns (via IPI) when page-table entries change on SMP
systems.

### TSO – Total Store Order
The x86 memory-ordering model that guarantees stores are visible in program
order.  gooos relies on TSO to simplify certain lock-free algorithms.

### TOS – Type of Service
The IPv4 header traffic-class field (now DS field including DSCP/ECN bits).

### TSS – Task State Segment
A per-CPU x86 structure that stores the kernel stack pointer (`RSP0`).  On
every user→kernel transition the CPU loads RSP0 from the TSS to switch to the
kernel stack.

### TUI – Text User Interface
A terminal-based visual interface.  gooos includes a vi-like modal TUI editor
as a user-space application.

### TPA – Target Protocol Address
The ARP field identifying the IP address being queried/resolved.  gooos ARP
packet handling notes reference TPA alongside sender/target hardware fields.

### TTL – Time To Live
The IPv4 header field that limits packet hop count to prevent routing loops.
gooos network stack docs reference TTL handling in IP processing.

### THA – Target Hardware Address
The ARP field containing the target MAC address.

---

## U

### UART – Universal Asynchronous Receiver/Transmitter
A serial-port controller.  gooos initializes a UART for early debug output and
serial console access.

### UDP – User Datagram Protocol
A connectionless transport protocol used by gooos echo demos and socket tests.
UDP appears in both kernel network-path docs and user-space networking examples.

### USB – Universal Serial Bus
A peripheral bus standard.  gooos currently supports only PS/2 keyboard input;
USB HID support is listed as future work.

### URG – Urgent
A TCP control flag indicating that the urgent pointer field is meaningful.

---

## V

### VGA – Video Graphics Array
The legacy text-mode display at physical address `0xB8000`.  gooos writes
directly to the VGA text buffer for console output.

### VMA – Virtual Memory Address
The runtime virtual address at which an ELF segment is mapped, as opposed to
its LMA (load-time physical address).

### VLAN – Virtual Local Area Network
A Layer-2 tagging mechanism (IEEE 802.1Q) used to partition Ethernet broadcast
domains.

---

## W

### WNOHANG – Wait No Hang
POSIX `waitpid` option that returns immediately when no child has exited.
Referenced in process-wait semantics and compatibility notes.

### WSL2 – Windows Subsystem for Linux 2
A virtualized Linux runtime on Windows used as a development environment.
gooos documentation references WSL2-specific setup and QEMU execution notes.

---

## X

### XID – Transaction Identifier
A request/response correlation field used in protocols such as DHCP.  gooos
DHCP path documentation references XID matching.

---
