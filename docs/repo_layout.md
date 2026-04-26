# Repository layout

Refreshed 2026-04-19 against `tcp-take2`. Run `ls` in any
subdirectory for the current authoritative listing.

```
gooos/
├── CLAUDE.md                              # project workflow guide
├── Makefile                               # three-phase build: user → embed → kernel
├── README.md                              # top-level overview
├── LICENSE
├── gooos_mascot2.png                      # mascot
├── go.mod                                 # module github.com/ryogrid/gooos
├── TODO_NET4.md                           # fix checklist (most recent; prior ones in pasttodos/)
│
├── docs/                                  # README-companion walkthroughs
│   ├── networking_demos.md                # Path A/B/C/D/E end-to-end walkthrough
│   ├── user_programs.md                   # gochan / tinyc / edit / fdprobe / ...
│   └── repo_layout.md                     # this file
│
├── current_impl_doc/                      # as-built reference (8 files)
│   ├── overview.md                        # architecture, boot, memory layout
│   ├── syscalls.md                        # 34-syscall ABI reference
│   ├── scheduler.md                       # task management, process lifecycle
│   ├── memory.md                          # page allocator, page tables
│   ├── ipc.md                             # channels, service tasks
│   ├── userland.md                        # SDK, build system, user programs
│   ├── glossary.md                        # terminology (Ring-3 wrapper, goroutine kinds, etc.)
│   └── known_issues.md                    # active workarounds + resolved bugs
│
├── impldoc/                               # design docs (English, ~55 files)
│   ├── busybox_*.md                       # original shell/syscall design (5 files)
│   ├── conservative_gc_design.md          # legacy — predates Phase B
│   ├── conservetive_gc_desing_guide.md    # legacy (typo preserved)
│   ├── deferred_*.md                      # deferred-item tracking (6 files)
│   ├── editor_*.md                        # text-editor design (2 files)
│   ├── goroutine_design_*.md              # Phase B scheduler migration (4 files)
│   ├── heap_gc_design.md
│   ├── helloworld_cgo_*.md                # cgo-style minimum example (2 files)
│   ├── net_*.md                           # UDP/IP/Ethernet/DHCP design (8 files)
│   ├── net_tcp_*.md                       # TCP design (9 files)
│   ├── net_test_plan.md
│   ├── phase_b_*.md                       # Phase B migration (5 files)
│   ├── shell_io_*.md                      # shell IO design (5 files)
│   ├── smp_*.md                           # SMP v1 + v2 design (9 files)
│   ├── tinyc_interpreter.md               # Tiny C interpreter design
│   ├── userspace_conservative_gc_*.md     # user-side GC bring-up (4 files)
│   ├── userspace_goroutines_overview.md
│   ├── userspace_gc_and_stacks.md
│   ├── userspace_scheduler_integration.md
│   ├── userspace_tinygo_runtime.md
│   └── userspace_verification.md
│
├── pasttodos/                             # completed TODO checklists
│   ├── TODO_NET1.md                       # net stack Phase 1-4
│   ├── TODO_NET2.md                       # socket API + DHCP
│   └── TODO_NET3.md                       # TCP-1..5
│
├── tcp_problem/                           # handoff docs for the late-timing RX stall
│   ├── README.md
│   ├── 01_problem_statement.md
│   ├── 02_evidence_and_hypotheses.md
│   ├── 03_gooos_design_map.md
│   └── 04_investigation_next_steps.md
│
├── tcp_problem_review2/                   # second-round bug review (code-review.md etc.)
│
├── grub/
│   └── grub.cfg                           # GRUB Multiboot config for ISO boot
│
├── scripts/                               # build + test + patch helpers
│   ├── embed_elfs.sh                      # convert user ELFs to Go byte arrays
│   ├── lint_isr.go                        # AST walker — ISR-safety lint (make lint)
│   ├── patch_tinygo_runtime.sh            # apply tinygo_runtime.patch to ~/.local/tinygo
│   ├── tinygo_runtime.patch               # the runtime patch itself
│   ├── verify_globals.sh                  # kernel globals-range check (make verify-globals)
│   ├── verify_globals_user.sh             # userspace equivalent
│   ├── test_net.sh                        # Path A smoke test (make test-net)
│   ├── test_net_tap.sh                    # TAP-mode integration test (requires root)
│   ├── test_tcp_phase1.sh .. phase5.sh    # per-phase TCP harnesses
│   ├── test_tcp_latetiming.sh             # 15 s idle + nc reproducer
│   └── test_tcp_longidle.sh               # parametrised-idle variant (takes $1 seconds)
│
├── user/                                  # userspace SDK and programs
│   ├── Makefile                           # build all user ELFs
│   ├── target.json                        # TinyGo target for userspace (gc=conservative, scheduler=tasks)
│   ├── linker_user.ld                     # linker script (entry at 0x40100000, .heap section)
│   ├── rt0.S                              # _start + syscall0..syscall5 stubs
│   ├── runtime_asm_amd64.S                # TinyGo runtime longjmp + scanCurrentStack for user
│   ├── task_stack_amd64.S                 # TinyGo task switch for user
│   ├── go.mod                             # user module
│   ├── gooos/                             # SDK package imported by every user program
│   │   ├── syscall.go                     # raw syscall wrappers
│   │   ├── io.go                          # Print, Println, ReadLine, ReadKey
│   │   ├── fs.go                          # ReadFile, WriteFile, ListDir
│   │   ├── proc.go                        # Spawn, Wait, Exit, Args, Yield, Sleep
│   │   ├── net.go                         # UDP + TCP SDK (Socket, Bind, Send/Recv, TCP*)
│   │   ├── vga.go                         # full-screen VGA primitives (cells, cursor)
│   │   ├── runtime_hooks.go               # gooosOnResume / gooosStackOverflow (Ring-3 safe)
│   │   └── cpu.go                         # GetCpuID syscall wrapper
│   └── cmd/                               # user programs (15 ELFs embedded in the kernel ISO)
│       ├── sh/                            # interactive shell (parse.go + main.go + pipe.go)
│       ├── hello/main.go                  # hello-world smoke test
│       ├── ls/main.go
│       ├── cat/main.go
│       ├── wc/main.go
│       ├── fdprobe/main.go                # fd-table syscalls probe
│       ├── goprobe/main.go                # userspace goroutines probe
│       ├── gochan/main.go                 # pipeline + select demo
│       ├── smpprobe/main.go               # SMP / IPI probe
│       ├── tinyc/                         # Tiny C interpreter (6 files)
│       ├── edit/                          # vi-like editor (5 files)
│       ├── udpecho/main.go                # userspace UDP echo
│       ├── dhcp/main.go                   # RFC 2131 DHCP client
│       ├── tcpecho/main.go                # userspace TCP echo
│       └── tcpcli/main.go                 # TCP active-open client
│
└── src/                                   # kernel source (51 Go files + 7 .S files)
    ├── boot.S                             # Multiboot 1 header + 32→64 bootstrap
    ├── isr.S                              # 256 ISR entry stubs + gooos_in_interrupt_depth .bss
    ├── switch.S                           # taskReturnHalt + elfExecTrampoline address helpers
    ├── task_stack_amd64.S                 # imported TinyGo tinygo_startTask / tinygo_swapTask
    ├── runtime_asm_amd64.S                # imported TinyGo tinygo_longjmp
    ├── trampoline.S                       # AP trampoline (16-bit → 64-bit for SMP)
    ├── stubs.S                            # port I/O, CPU control, GC support
    ├── linker.ld                          # section layout, heap, .pagetables, _alloc_start
    ├── target.json                        # TinyGo target: gc=conservative, scheduler=tasks, kernelspace
    │
    │   # Core kernel infrastructure
    ├── main.go                            # kernel entry: init + service goroutine spawns
    ├── serial.go                          # COM1 serial output
    ├── idt.go                             # IDT setup + lidt
    ├── interrupt.go                       # table-driven interrupt dispatcher + syscall dispatch
    ├── pic.go                             # 8259A PIC remap + EOI
    ├── pit.go                             # PIT 100 Hz timer (IRQ0) — drives pitTicks
    ├── panic.go                           # allocation-free page-fault / divide-by-zero hex-dump
    ├── stack_audit.go                     # per-goroutine high-water-mark report (runStackAudit gate)
    │
    │   # Memory + paging
    ├── vm.go                              # mapPage / unmapPage, bump + LIFO free, allocPagesContig
    ├── cr3.go                             # readCR3 / writeCR3 wrappers
    ├── proc_pml4.go                       # per-process PML4 with shared kernel PDP
    │
    │   # CPU + SMP
    ├── gdt.go                             # runtime GDT + TSS, per-CPU setup
    ├── smp.go                             # ACPI MADT, INIT-SIPI-SIPI, AP bringup
    ├── percpu.go                          # IA32_GS_BASE per-CPU storage
    ├── lapic_timer.go                     # LAPIC timer (100 Hz on BSP)
    ├── ioapic.go                          # IOAPIC init (currently disabled; PIC pass-through)
    ├── ipi.go                             # AP wakeup IPI handler
    ├── spinlock.go                        # Spinlock + rank-ordering comment (ranks 1-12)
    ├── goroutine_tss.go                   # TSS.RSP0 side-table + gooosOnResume CR3 hook
    ├── goroutine_irq.go                   # Go-side handle for gooos_in_interrupt_depth
    │
    │   # Userspace + process
    ├── process.go                         # Process + ring3Wrapper + exitCh lifecycle
    ├── ring3_pool.go                      # 32-slot kernel-stack pool for Ring-3 processes
    ├── userspace.go                       # 34-syscall register-based dispatch (int 0x80)
    ├── elf.go                             # ELF64 parser and loader
    ├── user_binaries.go                   # generated: embedded user ELF byte arrays
    │
    │   # Shell/FS/pipes/keyboard/VGA
    ├── fs.go                              # in-memory FS + go fsTask() over native chan
    ├── fd.go                              # FileDesc abstractions (stdin/stdout/file/pipe/socket)
    ├── pipe.go                            # anonymous pipes (chan byte backed)
    ├── keyboard.go                        # PS/2 keyboard IRQ handler (ISR-safe)
    ├── keyboard_irq.go                    # SPSC ring buffer + blocking keyboard-read wait path
    ├── vga.go                             # VGA console with cursor and scrolling
    │
    │   # Timers
    ├── afterticks.go                      # afterTicks single-dispatcher timer wheel (rank 12)
    │
    │   # Networking
    ├── pci.go                             # PCI config-space scan
    ├── e1000.go                           # Intel 82540EM driver (descriptor rings, MMIO)
    ├── e1000_irq.go                       # e1000 ISR (rxReadyFlag, lastICR, e1000IRQCount)
    ├── net.go                             # netInit, netRxLoop, netDiag
    ├── netbuf.go                          # 128×2048-byte packet buffer pool
    ├── netstats.go                        # NetStats counters
    ├── netutil.go                         # htons/htonl helpers (some unused)
    ├── ethernet.go                        # Ethernet frame parse/build
    ├── arp.go                             # ARP cache + resolver
    ├── ipv4.go                            # IPv4 parse/build + checksum + protocol demux
    ├── icmp.go                            # ICMP echo reply
    ├── udp.go                             # UDP bind table + pseudo-header checksum + echo server
    ├── netsock.go                         # socketFd + sys_socket..sys_shutdown (syscalls 22-33)
    ├── tcp.go                             # TCB + state machine + listener + tcpEchoServer
    ├── tcp_segment.go                     # TCP header parse/build + checksum + flags
    ├── tcp_retx.go                        # per-TCB retransmission queue + single-goroutine scanner
    ├── tcp_rtt.go                         # RFC 6298 SRTT/RTTVAR/RTO + Karn's rule
    ├── tcp_flow.go                        # flow control, SWS, rcv-window update
    └── tcp_cc.go                          # RFC 5681 slow-start / CA / fast retransmit
```

## Notes

- `tmp/` is a scratch directory (not under version control; used
  for test-run output, serial logs, ISO builds).
- `user/target.json` sets `gc=conservative` + `scheduler=tasks`;
  `src/target.json` adds `kernelspace` to disambiguate the patched
  TinyGo runtime bodies in `~/.local/tinygo/src/runtime/`.
- Commit tags follow `scope(subsys): ...` conventions; walk
  `git log --oneline` for examples.
