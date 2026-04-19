# gooos

An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The kernel runs on **TinyGo's native goroutine runtime** (`scheduler=tasks`, `gc=conservative`) — service loops are plain `go func()` goroutines, IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace. Assembly is used only where the CPU demands it.

![gooos mascot](gooos_mascot2.png)

## Progress

| Milestone | Status | Description |
|---|---|---|
| Boot to VGA output | Done | Multiboot 1 boot, 32→64-bit transition, VGA text output |
| Heap allocation | Done | 4 MiB heap via linker-defined region, bump allocator, `make`/`append`/`new` working |
| Serial output (COM1) | Done | `outb`/`inb` assembly stubs, COM1 at 115200 baud 8N1, `serialPrintln()` direct-UART writes |
| IDT + interrupt handlers | Done | 256-entry IDT, ISR assembly stubs with Go dispatcher, PIC 8259A remap (IRQs → vectors 32-47) |
| PIT / timer | Done | PIT channel 0 at 100 Hz, global tick counter, drives `sleepTicks` for `time.Sleep` |
| PS/2 keyboard driver | Done | IRQ1 handler, scancode set 1 → ASCII, lock-free SPSC ring buffer drained by `keyboardPump` goroutine |
| Virtual memory management | Done | Page fault handler, `mapPage`/`unmapPage` with 4 KiB granularity, bump + LIFO free stack with `allocPagesContig` for kernel stacks |
| Scheduler | Done | **TinyGo native goroutines** (`scheduler=tasks`). Cooperative; PIT IRQ drives `sleepTicks`. TSS.RSP0 updated per-Ring-3-goroutine via the `gooosOnResume` hook in the patched TinyGo runtime |
| Userspace | Done | Ring 3 execution via `iretq`, TSS for privilege transitions, `int 0x80` syscall interface (34 syscalls — see the Syscall ABI row below and `current_impl_doc/syscalls.md`); each user process is a `ring3Wrapper` goroutine |
| Filesystem | Done | In-memory flat filesystem: `Create`/`Write`/`Read`/`List`/`Delete` (32 entries, 256 KiB each); served by `fsTask` goroutine over native `chan *fsRequest` |
| SMP | Done (v2, BSP-only scheduling) | **Multi-processor infrastructure.** Per-CPU storage (`IA32_GS_BASE`), per-CPU GDT/TSS, per-CPU runqueues with work stealing, LAPIC timer (BSP), IPI wakeup, spinlocks (page allocator, process maps, heap, Queue, sleep/timer queues), per-CPU `currentTask`. APs boot via INIT-SIPI-SIPI, enter the TinyGo scheduler after boot-phase gating (`bspBootDone`), and idle via `waitForEvents` (`sti; hlt; cli`). **Remaining issue:** Ring-3 user code triple-faults when stolen by an AP (`iretq` on AP per-CPU TSS — needs QEMU+GDB hardware-level debugging). All goroutines currently run on BSP (CPU 0). See `impldoc/smp_deferred_and_known_issues.md` for details |
| Channel IPC + select | Done | **Native Go `chan` and `select`** in Ring 0. `fsReqCh`, `keyboardCh`, per-process `exitCh` are all `make(chan ...)` constructed by the TinyGo runtime |
| Syscall ABI | Done | 34-syscall register-based dispatch (all numbered; see `current_impl_doc/syscalls.md` for the canonical table). Base set: `sys_exit`, `sys_write`, `sys_read`, `sys_exec`, `sys_fs_read/write/list`, `sys_yield`, `sys_sleep`, `sys_getargs`, `sys_sbrk`, `sys_vga_clear`, `sys_open`, `sys_close`, `sys_dup2`, `sys_spawn`, `sys_wait`, `sys_pipe`, `sys_read_key`, `sys_vga_write_at`, `sys_vga_set_cursor`, `sys_getcpuid`. Net stack adds `sys_socket`/`sys_bind`/`sys_sendto`/`sys_recvfrom`/`sys_net_config`/`sys_sendto_bcast` (Phase 5) and `sys_listen`/`sys_accept`/`sys_connect`/`sys_tcp_send`/`sys_tcp_recv`/`sys_shutdown` (TCP phases). |
| ELF64 loader | Done | Parse ELF64 headers, map PT_LOAD segments, per-process page tracking, parent page save/restore for exec |
| BusyBox-style shell | Done | Interactive shell (`sh.elf`) with built-in commands (help, echo, clear, exit) and external ELF commands (ls, cat, wc, hello, fdprobe, goprobe, gochan, tinyc, edit, plus net-stack demos `udpecho`, `dhcp`, `tcpecho`, `tcpcli`, `smpprobe`) compiled with TinyGo; supports `<`/`>`/`>>` redirection and N-stage `\|` pipes |
| File descriptor table | Done | Per-process `Process.fds [16]` of `FileDesc`; `consoleStdin` / `consoleStdout` / `fileFd` / `pipeReader` / `pipeWriter` / `socketFd` impls; inheritance on exec; refcounted close on pipe ends |
| Shell redirection | Done | `cmd > file`, `cmd >> file`, `cmd < file` via shell-side `Open` + `Dup2` + `Close` dance; parser in `user/cmd/sh/parse.go` |
| Concurrent pipes | Done | `cmd1 \| cmd2 \| ...` — N-stage pipelines; kernel `pipe` backed by a 4 KiB `chan byte`; writer-close → reader-EOF, reader-close → writer-EPIPE; stages run on their own per-process PML4s |
| Multi-process | Done | Per-process PML4 sharing kernel PDP[0] with boot; CR3 swap on every goroutine resume via `gooosOnResume` (cached `gInfo.proc` for nosplit safety); `sys_spawn` + `sys_wait` for async exec; foreground-only stdin |
| ISR-safety lint | Done | `make lint` — AST walker (`scripts/lint_isr.go`) flags string-concat, channel ops, `go` statements, and runtime allocations inside ISR-reachable functions; runs as a `make build` prereq |
| Global-layout verification | Done | `make verify-globals` — asserts every TinyGo runtime queue (`runqueue`, `sleepQueue`, `timerQueue`) lands inside `_globals_start..end` so `findGlobals` covers it; `make build` prereq |
| Ring-3 stack pool | Done | Each Ring-3 process draws an 8 KiB kernel stack from `ring3StackPool` (`src/ring3_pool.go`); slot returns on `processExit` so per-exec heap leak shrinks from ~8 KiB to ~1 KiB |
| Allocation-free fatal handlers | Done | `handlePageFault`/`handleDivisionError` format CR2/RIP/errcode into a `.bss` `panicHexBuf` via no-alloc `appendHex`/`appendStr` helpers (`src/panic.go`); `//go:nosplit` |
| Stack-overflow diagnostic | Done | Patched `task.Pause()` calls `gooosStackOverflow(t)` on canary mismatch — prints task pointer + stack-top + canary address before halting, no allocation |
| Boot stack-size audit | Done | `stackSizeAudit()` (gated by `const runStackAudit`) reports per-goroutine high-water-mark usage on serial; off in release builds |
| `time.After` replacement | Done | `afterTicks(d uint64) <-chan struct{}` in `src/afterticks.go` — local stand-in because the TinyGo `time` package needs SSE we keep disabled. Backed by a **single-dispatcher timer wheel** (one long-lived goroutine draining a fixed-size `[256]timerEntry` list under lock-rank 12) so repeated callers no longer allocate per-call `Task` structs in the patched TinyGo runtime — see `current_impl_doc/known_issues.md` §"afterTicks single-dispatcher timer wheel" and `tcp_problem_review2/` for the bug this fixed. |
| Raw keyboard input | Done | `sys_read_key` (syscall 18) delivers single keystrokes with modifier flags (Shift/Ctrl/Alt) and extended-key prefix (arrow keys, Home/End/Delete). Keyboard driver (`src/keyboard.go`) tracks Ctrl + Alt make/break and consumes 0xE0 prefix. Backward compatible with line-buffered `sys_read` |
| VGA cell + cursor control | Done | `sys_vga_write_at` (19) writes a character with color attribute at (row, col); `sys_vga_set_cursor` (20) programs the hardware cursor via CRT controller. Enables full-screen editors and TUI programs |
| Text editor (vi-like) | Done | `edit.elf` — modal text editor with Normal/Insert/Command modes. Navigate with h/j/k/l or arrow keys, insert text with `i`/`a`/`o`, save with `:w`, quit with `:q`. 5 Go source files under `user/cmd/edit/`. See `impldoc/editor_overview.md` |
| Tiny C interpreter | Done | `tinyc.elf` — tree-walking interpreter for a C-subset language (int-only, 1D arrays, functions, if/else/while/for, println). Hand-written recursive-descent parser + AST evaluator, ~1000 lines of Go. Invoked from the shell as `$ tinyc program.tc`. See `impldoc/tinyc_interpreter.md` for the design |
| Userspace goroutines & channels | Done | Ring-3 user binaries run on their own TinyGo `scheduler=tasks` runtime — native `go func()`, `chan`, `select`, and `time.Sleep` work inside a user process. Build-tag split (`kernelspace` on `src/target.json`) keeps the kernel and user runtime bodies disjoint; `user/gooos/runtime_hooks.go` supplies the Ring-3-safe `gooosOnResume` / `gooosStackOverflow`. `sys_sleep` routes through `afterTicks` on the kernel side so a sleeping user process no longer holds the CPU. Proven by `user/cmd/goprobe/main.go` (PASS/FAIL probe) + `tmp/test_goprobe.sh`, and demonstrated interactively by `user/cmd/gochan/main.go` — a shell-invokable 3-stage pipeline + `select` demo (`$ gochan`) with harness at `tmp/test_gochan.sh`. See `impldoc/userspace_goroutines_overview.md` for the design set |
| Userspace conservative GC | Done | `user/target.json` now runs `gc=conservative` (was `leaking`). User binaries gain `_globals_start`/`_globals_end` brackets + synthetic `__ehdr_start` Elf64 header in `user/rt0.S` so TinyGo's `findGlobals()` can locate root-scan ranges at runtime; `tinygo_scanCurrentStack` ported into `user/runtime_asm_amd64.S` for stack scanning. Per-process 1 MiB fixed heap (`.heap @nobits` section, `user/linker_user.ld`) with `Process.HeapLimit` + `sysSbrkHandler` ceiling (`userHeapLimit = 2 MiB`) prevents runaway `sys_sbrk`. `maxFileData` bumped to 256 KiB to absorb ~13–17 KiB of per-binary GC overhead. `fib(10)` in Tiny C now works (177 recursive frames reclaim cleanly); long-running user programs no longer leak. See `impldoc/userspace_conservative_gc_*.md` |
| Networking stack (e1000 + UDP/IP/Ethernet) | Done (Phases 1-4) | **Bare-metal TCP/IP stack over the Intel 82540EM NIC.** PCI bus scan + BAR0 MMIO mapping (`src/pci.go`, `src/e1000.go`); 64 RX / 32 TX legacy descriptors on contiguous DMA pages; static IP config (10.0.2.15/24, gw 10.0.2.2) matching QEMU slirp defaults. ARP cache 16 entries (LRU) with `arpResolve` 2-sec timeout via `afterTicks` (`src/arp.go`); IPv4 parse/build with ones-complement checksum (`src/ipv4.go`); ICMP echo reply (`src/icmp.go`); UDP with 8-entry bind table + pseudo-header checksum + kernel echo server on port 7 (`src/udp.go`). RX path is a single long-lived `netRxLoop` goroutine (polling `drainRxRing` + `runtime.Gosched`); the `e1000` ISR sets `rxReadyFlag` and updates `lastICR` / `e1000IRQCount` counters. 128×2048-byte buffer pool (`src/netbuf.go`); 18-counter `NetStats` + `netDiag` dumps at boot+5 s and every ~10 s afterwards via the `afterTicks` timer wheel (`src/netstats.go`, `src/net.go`). Verified end-to-end under `make run-net`: ICMP echo-reply self-test passes; host `nc -u 127.0.0.1 9999` round-trips through kernel echo server via hostfwd. Socket syscall API + userspace DHCP client land in Phase 5 below (`impldoc/net_socket_api.md`, `impldoc/net_dhcp_client.md`). See `impldoc/net_overview.md` and `pasttodos/TODO_NET1.md` |
| Socket API + DHCP client (Phase 5) | Done | **Ring-3 socket API over UDP + a from-scratch DHCP client.** Six new syscalls (22-27: `sys_socket`, `sys_bind`, `sys_sendto`, `sys_recvfrom`, `sys_net_config`, `sys_sendto_bcast`) in `src/netsock.go` — AF_INET + SOCK_DGRAM only; `socketFd` is a `FileDesc` backend owning a cap=16 receive channel that `udpBindWithChannel` hooks into the UDP dispatch. `sys_recvfrom` extends the design-doc ABI with `R8 = timeout_ticks` (0 = block forever) so clients can give up gracefully. User-space pointers are bounds-checked (`>= 0x40000000`) before every dereference. Ephemeral port ≡ 0 when the socket is unbound. `sys_sendto_bcast` routes through `udpSendRaw` with forced src 0.0.0.0 and broadcast MAC/IP — DHCP-specific path. Userspace SDK (`user/gooos/net.go`) exposes `Socket`/`Bind`/`UDPSendTo`/`UDPRecvFromTimeout`/`UDPSendBroadcast` + `GetIP`/`SetIP`/`GetNetmask`/`SetNetmask`/`GetGateway`/`SetGateway`/`GetDNS`/`SetDNS`/`GetMAC`/`ApplyNetConfig` + `IPv4`/`FormatIP`/`FormatMAC` helpers, with a 5-arg `syscall5` assembly stub in `user/rt0.S`. Two new userspace programs: `udpecho.elf` (20-line echo server on UDP 17 — smoke test) and `dhcp.elf` (RFC 2131 DORA client, ~330 LOC). Kernel `ipv4Handle` now accepts limited (255.255.255.255) and subnet-directed broadcast so DHCP can actually receive the OFFER. Verified under QEMU slirp: `dhcp` completes the full Discover→Offer→Request→Ack exchange against the built-in DHCP server, applies the lease (10.0.2.15 / 255.255.255.0 / gw 10.0.2.2 / DNS 10.0.2.3) via `sys_net_config`, and persists `/network.conf` readable via `cat network.conf`. See `impldoc/net_socket_api.md`, `impldoc/net_dhcp_client.md`, and `pasttodos/TODO_NET2.md` |
| TCP stack (Phases TCP-1..TCP-5) | Done | **Full-duplex reliable byte-stream transport with RFC 5681 congestion control and a Ring-3 `SOCK_STREAM` socket API.** IP protocol 6 demux into `tcpHandle` (`src/ipv4.go`) feeds a fixed 16-entry Transmission Control Block pool (`src/tcp.go`) with 8 KiB×2 per-TCB ring buffers (`tcpRingBuf`); RFC 793 eleven-state machine (LISTEN / SYN_SENT / SYN_RECEIVED / ESTABLISHED / FIN_WAIT_1 / FIN_WAIT_2 / CLOSE_WAIT / CLOSING / LAST_ACK / TIME_WAIT / CLOSED) with in-any-state RST abort. Per-TCB retransmission queue (`src/tcp_retx.go`, 64-entry ring) driven by a single-goroutine kernel-wide scanner that polls every 50 ms for expired RTO / TIME_WAIT / persist / delayed-ACK deadlines. RFC 6298 SRTT / RTTVAR / RTO estimator (`src/tcp_rtt.go`, fixed-point ×8 / ×4 scaling) with Karn's rule; RFC 5681 slow start + congestion avoidance + 3-dup-ACK fast retransmit + RTO-triggered cwnd collapse (`src/tcp_cc.go`). RFC 1122 SWS avoidance + RFC 793 §3.9 snd-window update guard in a shared `tcpAckUpdate` helper (`src/tcp_flow.go`). Lock-ordering extended to ranks 9 (`tcbTableLock`) / 10 (`tcpListenLock`) / 11 (`tcpTimerLock`); rank 12 (`timerListLock` / `afterTicks` wheel) sits above them. Six new syscalls 28-33 (`sys_listen`, `sys_accept`, `sys_connect`, `sys_tcp_send`, `sys_tcp_recv`, `sys_shutdown`) in `src/netsock.go`; `socketFd` extended with a kind discriminant so UDP and TCP sockets share the `FileDesc` fd table; `userBufInRange` gates every user-memory pointer. Userspace SDK adds `TCPSocket`/`TCPListen`/`TCPAccept`/`TCPConnect`/`TCPSend`/`TCPSendAll`/`TCPRecv`/`TCPShutdown` to `user/gooos/net.go` (no new `syscallN` stubs needed). Two new demo binaries: `tcpecho.elf` (Ring-3 echo server on port 8081 with goroutine-per-connection) and `tcpcli.elf` (argv `ip port message` active-open client). Verified end-to-end under `make run-net`: host `nc 127.0.0.1 10080` round-trips through the kernel TCP echo server on guest port 8080 (3-way handshake + data + peer-FIN + LAST_ACK → CLOSED) at any idle duration (15 s / 30 s / 60 s / 120 s / 300 s all PASS via `scripts/test_tcp_longidle.sh`). Phase 1-5 regression (UDP echo + DHCP DORA) continues to pass. See `impldoc/net_tcp_*.md` (nine design docs), `pasttodos/TODO_NET3.md`, and `TODO_NET4.md` (the late-timing RX stall fix). |

## Running the demos

Walkthroughs for the end-to-end user programs and networking
paths live under `docs/`:

- **[Networking demos (Paths A–E)](docs/networking_demos.md)** —
  kernel-builtin UDP echo, userspace `udpecho`, DHCP client,
  kernel TCP echo, userspace `tcpecho`/`tcpcli`. Includes the
  ASCII flow diagram, the "shell-on-serial" gotcha explanation,
  `netDiag` counter expectations, and the `scripts/test_tcp_*.sh`
  harness list.
- **[User programs (gochan / tinyc / edit)](docs/user_programs.md)**
  — non-networking Ring-3 demos. Covers the gochan pipeline + select
  demo, Tiny C interpreter fixtures, and the vi-like editor key
  bindings.

For the no-networking quick tour, `make run` + `help` at the
shell prompt is the fastest starting point — see the
**Run in QEMU** section below.

## Where assembly is used

Go cannot express certain CPU-level operations. These remain in assembly:

- **Boot bootstrap** (`boot.S`): Multiboot header, 32→64-bit mode switch, page table setup, GDT load
- **ISR stubs** (`isr.S`): 256 interrupt entry points — save registers, bump `gooos_in_interrupt_depth`, call Go dispatcher, decrement, `iretq`
- **TinyGo task context switch** (`task_stack_amd64.S`): `tinygo_startTask` / `tinygo_swapTask` — imported byte-equivalent from TinyGo's runtime because `tinygo build -o *.o` does not assemble `.S` itself
- **TinyGo runtime longjmp** (`runtime_asm_amd64.S`): `tinygo_longjmp` — same reason as above
- **Ring 3 trampolines** (`switch.S`): `taskReturnHalt` safety net + `elfExecTrampolineAddr` legacy hook (both shrinking targets)
- **AP trampoline** (`trampoline.S`): 16-bit real-mode → 32-bit → 64-bit mode transition for SMP
- **Port I/O & CPU control** (`stubs.S`): `outb`/`inb`, `cli`/`sti`/`hlt`, `lidt`/`lgdt`/`ltr`, `invlpg`, CR2 read / CR3 read+write (`readCR3`, `writeCR3`), `memcpy`/`memmove`/`memset`, `jumpToRing3`, `readFlags`/`restoreFlags`, `tinygo_scanCurrentStack`
- **Synthetic ELF header** (`stubs.S`): Fake `__ehdr_start` in `.rodata` for GC's `findGlobals()`
- **Keyboard IRQ ring** (`isr.S`, `keyboard_irq.go`): `.bss` head/tail/slot storage is assembled as 32-bit naturally-aligned mov's; x86-TSO makes the writes visible to `keyboardPump` without fences
- **User startup** (`user/rt0.S`): `_start`, syscall wrappers (`syscall0`-`syscall5`), TinyGo runtime stubs (`mmap`, `write`, `abort`, `memcpy`, `memset`)
- **User task context switch + longjmp** (`user/task_stack_amd64.S`, `user/runtime_asm_amd64.S`): `tinygo_startTask` / `tinygo_swapTask` / `tinygo_longjmp` — byte-equivalent imports of TinyGo runtime asm, needed once the user target flipped to `scheduler=tasks`. Same `tinygo build -o *.o` restriction as the kernel side

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
                                |  TinyGo runtime main (runtime_gooos.go)  |
                                |  - preinit(): mmap stub → heap init      |
                                |  - initAll(): package init               |
                                |  - callMain() → user main()              |
                                +--------------------+---------------------+
                                                     |
                                                     v
                              +----------------------------------------------+
                              |  main()  (main.go)                           |
                              |  - Serial, IDT, PIC, PIT, Keyboard, VM      |
                              |  - afterTicksInit() — timer-wheel dispatcher|
                              |  - SMP: INIT-SIPI-SIPI multi-core boot      |
                              |  - GDT + TSS (per-task kernel stacks)       |
                              |  - Scheduler init, service tasks            |
                              |  - Store user ELFs in filesystem            |
                              |  - Load sh.elf → Ring 3 shell               |
                              +----------------------------------------------+
                                                     |
                  +----------------------------------+----------------------------------+
                  |                                  |                                  |
    Kernel goroutines (Ring 0)             Shell goroutine (Ring 3)    External Commands (Ring 3)
    ┌──────────────────────┐        ┌──────────────────┐          ┌──────────────────┐
    │ go fsTask()          │        │ go ring3Wrapper  │          │ ls.elf / cat.elf │
    │  for req := range    │        │   (sh.elf)       │  exec    │ hello.elf / wc.elf│
    │    fsReqCh {…}       │        │  $ prompt        │ -------> │  go ring3Wrapper │
    ├──────────────────────┤        │  built-in: help, │          │   (cmd.elf)      │
    │ go keyboardPump()    │        │   echo, clear    │ <------- │  sys_exit → proc │
    │  ring → keyboardCh   │        │  external: ls,   │  exit    │  .exitCh delivers│
    ├──────────────────────┤        └──────────────────┘          └──────────────────┘
    │ go netRxLoop()       │
    │  drain e1000 RX ring │
    ├──────────────────────┤
    │ go timerDispatcher() │
    │  fire afterTicks()   │
    │  matured channels    │
    ├──────────────────────┤
    │ go tcpRTOScannerLoop │
    │  50 ms RTO/TIMEWAIT  │
    ├──────────────────────┤
    │ go udpEchoServer     │
    │ go tcpEchoServer     │
    └──────────────────────┘
```

## Repository layout

Full tree in **[docs/repo_layout.md](docs/repo_layout.md)**.
Top-level shape:

```
gooos/
├── README.md / CLAUDE.md / Makefile / go.mod / LICENSE
├── TODO_NET4.md                 # current-session fix checklist (prior ones in pasttodos/)
├── docs/                        # README-companion walkthroughs (networking, user programs, layout)
├── current_impl_doc/            # 8 as-built reference docs (overview, syscalls, scheduler, memory, ipc, userland, glossary, known_issues)
├── impldoc/                     # ~55 design docs (English)
├── pasttodos/                   # completed TODO checklists (NET1, NET2, NET3)
├── tcp_problem/                 # handoff package for the late-timing RX stall (pre-fix)
├── tcp_problem_review2/         # second-round bug review that motivated the afterTicks timer wheel
├── grub/grub.cfg                # GRUB Multiboot config
├── scripts/                     # build helpers, lint/verify scripts, TCP test harnesses, TinyGo runtime patch
├── user/                        # userspace SDK (user/gooos/) and 15 user programs (user/cmd/*)
└── src/                         # kernel source — 51 Go files + 7 .S files covering boot, scheduler, networking, TCP, SMP, GC hooks
```

## Prerequisites

Tested on **WSL2 Ubuntu 24.04** with:

- **TinyGo 0.40.1** (LLVM 20.1.1) — install from the official `.deb` or tarball at <https://github.com/tinygo-org/tinygo/releases>
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

### User-writable TinyGo copy + runtime patches (required)

gooos needs a set of local changes to TinyGo's runtime for
`scheduler=tasks` to work in Ring 0, plus SMP v2 per-CPU
runqueue support. The system TinyGo at
`/usr/local/lib/tinygo0.40.1/` (or wherever the `.deb` installs
it) is root-owned, so the build uses a user-writable copy at
`$HOME/.local/tinygo0.40.1/` (overridable via the `TINYGOROOT`
environment variable the Makefile exports).

The full edit is captured as a unified diff at
`scripts/tinygo_runtime.patch` (reviewable with
`git apply --stat scripts/tinygo_runtime.patch` against a
pristine TinyGo 0.40.1 tree). The patch installs:

- **`runtime/runtime_gooos.go`** (new, `gooos && baremetal && kernelspace`)
  — kernel bodies for `sleepTicks`, `ticks`, `putchar`, `exit`,
  `abort`, and the bare-metal `main` entry point that `boot.S`
  calls.
- **`runtime/runtime_gooos_user.go`** (new, `gooos && baremetal && !kernelspace`)
  — userspace equivalents that route through syscalls.
- **`runtime/interrupt/interrupt_gooos.go`** (new, kernel) and
  **`runtime/interrupt/interrupt_gooos_user.go`** (new, userspace)
  — `interrupt.Disable` / `Restore` / `In` implementations.
- **`runtime/wait_gooos.go`** (new, kernel) — `waitForEvents` as
  an `sti; hlt; cli` idle loop.
- **`runtime/wait_gooos_user.go`** (new, userspace) — `waitForEvents`
  no-op for Ring-3 builds (kernel preempts on timer IRQ).
- **`runtime/scheduler_cooperative.go`** (patched in place — this
  file was named `scheduler.go` in TinyGo 0.33.0) — per-CPU
  `runqueues[17]`, `schedLock` spinlock over sleep/timer queues,
  `runqueuePushTo`, `stealWork` round-robin peer scan,
  `apScheduler()` entry for AP cores, push-site retargeting in
  `scheduleTask` / `Gosched` / main scheduler loop.
- **`runtime/gc_blocks.go`** (patched in place) — explicit
  `heapLock` spinlock around alloc/GC, because upstream's
  `gcLock task.PMutex` is a no-op under `tinygo.unicore`
  (`scheduler=tasks`).
- **`runtime/wait_other.go`** (patched in place) — adds
  `&& !gooos` to the build tag so gooos builds use the
  gooos-specific `wait_gooos.go` / `wait_gooos_user.go`.
- **`internal/task/queue.go`**, **`task_stack.go`**,
  **`task_stack_amd64.go`**, **`task_stack_unicore.go`** (patched
  in place — `task_stack_unicore.go` is new in TinyGo 0.40.x for
  `scheduler=tasks`; the 0.33.0 gooos patch targeted
  `task_stack.go` for those hunks) — SMP-safe task queues,
  per-CPU `currentTasks[17]` and `systemStacks[17]`, the
  `stackTop` field + `gooosStackOverflow` hook, and the
  `gooosOnResume()` call that lets the gooos kernel update
  `TSS.RSP0` on every Ring-3 goroutine resume.

#### One-time setup after installing TinyGo

```bash
# 1. Mirror the system TinyGo 0.40.1 into a user-writable location.
#    (Adjust the source path if your .deb installs TinyGo elsewhere.)
mkdir -p ~/.local/tinygo0.40.1
cp -a /usr/local/lib/tinygo0.40.1/. ~/.local/tinygo0.40.1/

# 2. Apply scripts/tinygo_runtime.patch via the wrapper script.
#    (Equivalent: patch -p1 -d ~/.local/tinygo0.40.1 < scripts/tinygo_runtime.patch)
bash scripts/patch_tinygo_runtime.sh
```

The Makefile defaults to `TINYGOROOT=$HOME/.local/tinygo0.40.1`
and invokes `~/.local/tinygo0.40.1/bin/tinygo`, so `make build`
picks up the patched tree automatically.

The wrapper is **idempotent**: it verifies the expected files are
in place and carry the right build tags, and skips with an
`already-applied:` message if so. Re-run any time after a TinyGo
upgrade or after refreshing `~/.local/tinygo0.40.1/`.

#### Reverting

```bash
# 1. Delete the six new files (patch -R leaves them empty, not gone).
rm ~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go
rm ~/.local/tinygo0.40.1/src/runtime/runtime_gooos_user.go
rm ~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go
rm ~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos_user.go
rm ~/.local/tinygo0.40.1/src/runtime/wait_gooos.go
rm ~/.local/tinygo0.40.1/src/runtime/wait_gooos_user.go

# 2. Reverse the in-place edits.
patch -R -p1 -d ~/.local/tinygo0.40.1 < scripts/tinygo_runtime.patch
```

Rationale: `impldoc/goroutine_design_scheduler.md §5.1` explains
why the runtime files are needed; `impldoc/phase_b_ring3_and_exec.md §4`
explains `gooosOnResume` and the `stackTop` field;
`impldoc/smp_percpu_and_sync.md` covers the per-CPU queue story.

## Build

```bash
make build
```

This runs five phases:

1. **ISR-safety lint**: `scripts/lint_isr.go` walks every ISR-rooted call graph and rejects any string concat, channel op, `go` statement, or runtime allocation. Build fails on violation.
2. **User programs**: `make -C user all` — compiles all 15 TinyGo user programs under `user/cmd/*` into ELF binaries.
3. **Embed**: `scripts/embed_elfs.sh` — converts user ELFs to Go byte arrays in `src/user_binaries.go`.
4. **Kernel**: assembles `.S` files, compiles all Go with TinyGo, links with `ld.lld` into `tmp/kernel.bin`.
5. **Global-layout verify**: `scripts/verify_globals.sh` asserts every TinyGo runtime queue (`runqueue`, `sleepQueue`, `timerQueue`) lies inside `_globals_start..end` so the conservative GC can scan it.

You can also run the lint and the verify steps standalone:

```bash
make lint              # ISR-safety lint only
make verify-globals    # global-layout check only
```

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

With the e1000 NIC attached for networking demos:

```bash
make run-net        # adds -device e1000 + hostfwds for UDP/TCP demos
```

See `docs/networking_demos.md` for the 5 demo paths enabled by
`make run-net`.

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
  fdprobe    Verify the fd-table syscalls

Redirection:
  cmd > file       stdout to file (truncate)
  cmd >> file      stdout to file (append)
  cmd < file       stdin from file
```

(`sh.elf`'s `help` still advertises only the five original
external commands; the full set of user binaries embedded in the
kernel ISO — `ls`, `cat`, `wc`, `hello`, `fdprobe`, `goprobe`,
`gochan`, `smpprobe`, `tinyc`, `edit`, `udpecho`, `dhcp`,
`tcpecho`, `tcpcli` — is listed in `docs/user_programs.md` and
in `docs/repo_layout.md`.)

```
$ ls
hello.txt
sh.elf
hello.elf
ls.elf
cat.elf
wc.elf
fdprobe.elf
... and more ELFs (goprobe, gochan, smpprobe, tinyc, edit, udpecho, dhcp, tcpecho, tcpcli)

$ echo hello > out.txt
$ cat out.txt
hello

$ echo world | cat | cat
world

$ cat hello.txt
Hello from the gooos filesystem!
This is a test file.

$ hello
Hello, World from gooos userspace!
```

## Known limitations

- **Sleep granularity is 10 ms.** `time.Sleep` (and the local
  `afterTicks` replacement in `src/afterticks.go`) build on
  the PIT counter at 100 Hz, so any requested duration rounds
  up to the next 10 ms tick. No kernel goroutine currently
  needs sub-10-ms sleep; if a future caller does, retrofit
  `~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go:sleepTicks`
  to use the LAPIC timer in one-shot mode (see
  `impldoc/deferred_hygiene.md §6` for the design sketch).
- **Shell does not support job control.** No `&` background
  jobs, no `jobs` / `fg` / `bg` built-ins, no signals
  (SIGINT, SIGPIPE). Foreground process is always the most
  recently-spawned non-pipe-driven stage (or the shell
  itself at the prompt). See
  `impldoc/shell_io_overview.md §7` for the scope fences.
- **No shell-level stderr redirection.** `2>` / `&>` / `>&`
  are not parsed. Writing to fd 2 goes to serial only (no
  VGA mirror); programs have no way to redirect fd 2
  separately.
- **SMP user-mode Ring-3 disabled.** APs boot and enter the
  scheduler but only BSP (CPU 0) runs goroutines for now;
  AP-side `iretq` into Ring 3 triple-faults under
  investigation. See `impldoc/smp_deferred_and_known_issues.md`.

## Documentation

Reference docs live under `current_impl_doc/` (as-built) and
`impldoc/` (design). Companion walkthroughs under `docs/`.

As-built reference (start here):

- [Architecture Overview](current_impl_doc/overview.md) — boot flow, memory map, task model
- [Syscall ABI](current_impl_doc/syscalls.md) — 34-syscall reference
- [Scheduler](current_impl_doc/scheduler.md) — task states, context switch, process lifecycle
- [Memory](current_impl_doc/memory.md) — page allocator, page tables, linker layout
- [IPC](current_impl_doc/ipc.md) — channels, service tasks, `afterTicks` timer wheel
- [Userland](current_impl_doc/userland.md) — SDK, build system, user programs
- [Glossary](current_impl_doc/glossary.md) — terminology and goroutine kinds
- [Known Issues](current_impl_doc/known_issues.md) — workarounds, limitations, resolved bugs

Design docs (deeper dives — many superseded by as-builts; see
`docs/repo_layout.md` for a staleness-aware map):

- `impldoc/net_overview.md`, `impldoc/net_tcp_*.md` — networking stack design
- `impldoc/smp_*.md` — SMP v1 / v2 design
- `impldoc/userspace_*.md`, `impldoc/goroutine_design_*.md` — Phase B migration notes

Walkthroughs:

- [docs/networking_demos.md](docs/networking_demos.md) — 5 networking demo paths (A–E)
- [docs/user_programs.md](docs/user_programs.md) — gochan / tinyc / edit + program roster
- [docs/repo_layout.md](docs/repo_layout.md) — full repository tree

## License

[MIT License](LICENSE) — Copyright (c) 2026 Ryo Kanbayashi.

## Acknowledgements

- OSDev Wiki articles on **Multiboot**, **Setting Up Long Mode**, **IDT**, **PIC**, **PIT**, **PS/2 Keyboard**, **Paging**, **TSS**, and **SMP** are the canonical references for the hardware interfaces this project implements.
- **TinyGo** for making it plausible to write OS code in Go with minimal runtime dependencies.
