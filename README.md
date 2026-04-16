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
| Userspace | Done | Ring 3 execution via `iretq`, TSS for privilege transitions, `int 0x80` syscall interface (12 syscalls); each user process is a `ring3Wrapper` goroutine |
| Filesystem | Done | In-memory flat filesystem: `Create`/`Write`/`Read`/`List`/`Delete` (32 entries, 96 KiB each); served by `fsTask` goroutine over native `chan *fsRequest` |
| SMP | Done (v1) | ACPI MADT AP discovery, 16-bit real-mode trampoline, INIT-SIPI-SIPI. APs idle at `sti; hlt`; BSP runs all goroutines. SMP v2 (per-CPU runqueues + work stealing + LAPIC IPI) is deferred; see `impldoc/deferred_smp_v2.md` and `TODO_DEF.md` "Further deferred" — needs a TinyGo runtime fork |
| Channel IPC + select | Done | **Native Go `chan` and `select`** in Ring 0. `fsReqCh`, `keyboardCh`, per-process `exitCh` are all `make(chan ...)` constructed by the TinyGo runtime |
| Syscall ABI | Done | 18-syscall register-based dispatch (all numbered; see `impldoc/shell_io_fd_table.md §5.1` for the canonical table): `sys_exit`, `sys_write(fd,buf,len)`, `sys_read(fd,buf,max)`, `sys_exec`, `sys_fs_read/write/list`, `sys_yield`, `sys_sleep`, `sys_getargs`, `sys_sbrk`, `sys_vga_clear`, `sys_open`, `sys_close`, `sys_dup2`, `sys_spawn`, `sys_wait`, `sys_pipe` |
| ELF64 loader | Done | Parse ELF64 headers, map PT_LOAD segments, per-process page tracking, parent page save/restore for exec |
| BusyBox-style shell | Done | Interactive shell (`sh.elf`) with built-in commands (help, echo, clear, exit) and external ELF commands (ls, cat, wc, hello, fdprobe, goprobe, gochan, tinyc, edit) compiled with TinyGo; supports `<`/`>`/`>>` redirection and N-stage `\|` pipes |
| File descriptor table | Done | Per-process `Process.fds [16]` of `FileDesc`; `consoleStdin` / `consoleStdout` / `fileFd` / `pipeReader` / `pipeWriter` impls; inheritance on exec; refcounted close on pipe ends |
| Shell redirection | Done | `cmd > file`, `cmd >> file`, `cmd < file` via shell-side `Open` + `Dup2` + `Close` dance; parser in `user/cmd/sh/parse.go` |
| Concurrent pipes | Done | `cmd1 \| cmd2 \| ...` — N-stage pipelines; kernel `pipe` backed by a 4 KiB `chan byte`; writer-close → reader-EOF, reader-close → writer-EPIPE; stages run on their own per-process PML4s |
| Multi-process | Done | Per-process PML4 sharing kernel PDP[0] with boot; CR3 swap on every goroutine resume via `gooosOnResume` (cached `gInfo.proc` for nosplit safety); `sys_spawn` + `sys_wait` for async exec; foreground-only stdin |
| ISR-safety lint | Done | `make lint` — AST walker (`scripts/lint_isr.go`) flags string-concat, channel ops, `go` statements, and runtime allocations inside ISR-reachable functions; runs as a `make build` prereq |
| Global-layout verification | Done | `make verify-globals` — asserts every TinyGo runtime queue (`runqueue`, `sleepQueue`, `timerQueue`) lands inside `_globals_start..end` so `findGlobals` covers it; `make build` prereq |
| Ring-3 stack pool | Done | Each Ring-3 process draws an 8 KiB kernel stack from `ring3StackPool` (`src/ring3_pool.go`); slot returns on `processExit` so per-exec heap leak shrinks from ~8 KiB to ~1 KiB |
| Allocation-free fatal handlers | Done | `handlePageFault`/`handleDivisionError` format CR2/RIP/errcode into a `.bss` `panicHexBuf` via no-alloc `appendHex`/`appendStr` helpers (`src/panic.go`); `//go:nosplit` |
| Stack-overflow diagnostic | Done | Patched `task.Pause()` calls `gooosStackOverflow(t)` on canary mismatch — prints task pointer + stack-top + canary address before halting, no allocation |
| Boot stack-size audit | Done | `stackSizeAudit()` (gated by `const runStackAudit`) reports per-goroutine high-water-mark usage on serial; off in release builds |
| `time.After` replacement | Done | `afterTicks(d uint64) <-chan struct{}` in `src/afterticks.go` — local stand-in because the TinyGo `time` package needs SSE we keep disabled |
| Raw keyboard input | Done | `sys_read_key` (syscall 18) delivers single keystrokes with modifier flags (Shift/Ctrl/Alt) and extended-key prefix (arrow keys, Home/End/Delete). Keyboard driver (`src/keyboard.go`) tracks Ctrl + Alt make/break and consumes 0xE0 prefix. Backward compatible with line-buffered `sys_read` |
| VGA cell + cursor control | Done | `sys_vga_write_at` (19) writes a character with color attribute at (row, col); `sys_vga_set_cursor` (20) programs the hardware cursor via CRT controller. Enables full-screen editors and TUI programs |
| Text editor (vi-like) | Done | `edit.elf` — modal text editor with Normal/Insert/Command modes. Navigate with h/j/k/l or arrow keys, insert text with `i`/`a`/`o`, save with `:w`, quit with `:q`. 5 Go source files under `user/cmd/edit/`. See `impldoc/editor_overview.md` |
| Tiny C interpreter | Done | `tinyc.elf` — tree-walking interpreter for a C-subset language (int-only, 1D arrays, functions, if/else/while/for, println). Hand-written recursive-descent parser + AST evaluator, ~1000 lines of Go. Invoked from the shell as `$ tinyc program.tc`. See `impldoc/tinyc_interpreter.md` for the design |
| Userspace goroutines & channels | Done | Ring-3 user binaries run on their own TinyGo `scheduler=tasks` runtime — native `go func()`, `chan`, `select`, and `time.Sleep` work inside a user process. Build-tag split (`kernelspace` on `src/target.json`) keeps the kernel and user runtime bodies disjoint; `user/gooos/runtime_hooks.go` supplies the Ring-3-safe `gooosOnResume` / `gooosStackOverflow`. `sys_sleep` routes through `afterTicks` on the kernel side so a sleeping user process no longer holds the CPU. Proven by `user/cmd/goprobe/main.go` (PASS/FAIL probe) + `tmp/test_goprobe.sh`, and demonstrated interactively by `user/cmd/gochan/main.go` — a shell-invokable 3-stage pipeline + `select` demo (`$ gochan`) with harness at `tmp/test_gochan.sh`. See `impldoc/userspace_goroutines_overview.md` for the design set |
| Userspace conservative GC | Done | `user/target.json` now runs `gc=conservative` (was `leaking`). User binaries gain `_globals_start`/`_globals_end` brackets + synthetic `__ehdr_start` Elf64 header in `user/rt0.S` so TinyGo's `findGlobals()` can locate root-scan ranges at runtime; `tinygo_scanCurrentStack` ported into `user/runtime_asm_amd64.S` for stack scanning. Per-process 1 MiB fixed heap (`.heap @nobits` section, `user/linker_user.ld`) with `Process.HeapLimit` + `sysSbrkHandler` ceiling (`userHeapLimit = 2 MiB`) prevents runaway `sys_sbrk`. `maxFileData` bumped to 256 KiB to absorb ~13–17 KiB of per-binary GC overhead. `fib(10)` in Tiny C now works (177 recursive frames reclaim cleanly); long-running user programs no longer leak. See `impldoc/userspace_conservative_gc_*.md` |

### Running the gochan demo

`gochan` is a shell-invokable user program that exercises native
userspace goroutines + channels end-to-end: a three-stage pipeline
(producer → squarer → printer, joined by unbuffered `chan int`)
followed by a `select` race between two tickers that fire at 20 ms
and 30 ms.

Boot gooos (`make run` or `make iso` then QEMU) and at the shell
prompt:

```
$ gochan
```

Expected serial / VGA output (`PF=0` throughout):

```
gochan: pipeline demo (5 items across 3 goroutines)
gochan: squared=1
gochan: squared=4
gochan: squared=9
gochan: squared=16
gochan: squared=25
gochan: select over two tickers (alpha/beta)
gochan: got alpha
gochan: got beta
gochan: finished
```

- Source: `user/cmd/gochan/main.go`.
- Automated harness: `tmp/test_gochan.sh` — boots the kernel ISO
  in headless QEMU, sends `gochan` to the shell via monitor
  sendkey, and asserts every squared value, both select
  branches, the `finished` marker, and `PF=0`. Prints
  `result: PASS` on success.

### Running the tinyc interpreter

`tinyc` interprets Tiny C source files — a C-subset with
integer-only types, 1D arrays, user-defined functions, and
`println` output. Several `.tc` test files are pre-loaded in the
filesystem at boot:

```
$ tinyc sum.tc
s = 45

$ tinyc fib.tc
13

$ tinyc array.tc
s = 45
```

- Source: `user/cmd/tinyc/` (6 files: token, lexer, AST, parser,
  evaluator, main).
- Design doc: `impldoc/tinyc_interpreter.md`.
- Automated harness: `tmp/test_tinyc.sh` — runs all 4 fixtures,
  asserts expected output + `PF=0`.

### Using the text editor

`edit` is a vi-like modal editor. Open a file from the shell:

```
$ edit hello.txt
```

The editor takes over the full 80x25 VGA screen. Key bindings:

| Mode | Keys | Action |
|---|---|---|
| Normal | `h`/`j`/`k`/`l` or arrows | Move cursor |
| Normal | `i` | Enter Insert mode |
| Normal | `a` | Enter Insert mode after cursor |
| Normal | `o` / `O` | Open line below / above |
| Normal | `x` | Delete character |
| Normal | `dd` | Delete line |
| Normal | `:` | Enter Command mode |
| Insert | any printable | Insert character |
| Insert | `Escape` | Return to Normal mode |
| Command | `:w` | Save file |
| Command | `:q` | Quit (refuses if unsaved; use `:q!` to force) |
| Command | `:wq` | Save and quit |

- Source: `user/cmd/edit/` (5 files: main, buffer, screen, input,
  keybinds).
- Design docs: `impldoc/editor_overview.md`,
  `impldoc/editor_raw_input.md`.
- Automated harness: `tmp/test_edit.sh`.

### Where assembly is used

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
- **User startup** (`user/rt0.S`): `_start`, syscall wrappers (`syscall0`-`syscall4`), TinyGo runtime stubs (`mmap`, `write`, `abort`, `memcpy`, `memset`)
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
    Kernel goroutines (Ring 0)             Shell goroutine (Ring 3)    External Commands (Ring 3)
    ┌──────────────────────┐        ┌──────────────────┐          ┌──────────────────┐
    │ go fsTask()          │        │ go ring3Wrapper  │          │ ls.elf / cat.elf │
    │  for req := range    │        │   (sh.elf)       │  exec    │ hello.elf / wc.elf│
    │    fsReqCh {…}       │        │  $ prompt        │ -------> │  go ring3Wrapper │
    ├──────────────────────┤        │  built-in: help, │          │   (cmd.elf)      │
    │ go keyboardPump()    │        │   echo, clear    │ <------- │  sys_exit → proc │
    │  ring → keyboardCh   │        │  external: ls,   │  exit    │  .exitCh delivers│
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
│   ├── target.json                                 # TinyGo target for userspace (gc=conservative, scheduler=tasks)
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
    ├── isr.S                                       # 256 ISR entry stubs + gooos_in_interrupt_depth .bss
    ├── switch.S                                    # taskReturnHalt + elfExecTrampoline address helpers
    ├── task_stack_amd64.S                          # imported TinyGo tinygo_startTask / tinygo_swapTask
    ├── runtime_asm_amd64.S                         # imported TinyGo tinygo_longjmp
    ├── trampoline.S                                # AP trampoline (16-bit → 64-bit for SMP)
    ├── stubs.S                                     # port I/O, CPU control, GC support
    ├── linker.ld                                   # section layout, heap, .pagetables, _alloc_start
    ├── target.json                                 # TinyGo target: gc=conservative, scheduler=tasks
    ├── main.go                                     # kernel entry: init + go fsTask / go keyboardPump
    ├── serial.go                                   # COM1 serial output (direct UART writes)
    ├── idt.go                                      # IDT setup + lidt
    ├── interrupt.go                                # table-driven interrupt dispatcher + syscall dispatch
    ├── pic.go                                      # 8259A PIC remap + EOI
    ├── pit.go                                      # PIT timer (100 Hz, IRQ0) — drives sleepTicks
    ├── keyboard.go                                 # PS/2 keyboard IRQ handler (ISR-safe)
    ├── keyboard_irq.go                             # SPSC ring buffer + keyboardPump goroutine
    ├── goroutine_tss.go                            # TSS.RSP0 side-table + gooosOnResume hook
    ├── goroutine_irq.go                            # Go-side handle for gooos_in_interrupt_depth
    ├── vm.go                                       # virtual memory: mapPage, unmapPage, bump + LIFO free
    ├── vga.go                                      # VGA console with cursor and scrolling
    ├── elf.go                                      # ELF64 parser and loader
    ├── process.go                                  # Process + ring3Wrapper + exitCh lifecycle
    ├── gdt.go                                      # runtime GDT + TSS, tssSetRSP0
    ├── userspace.go                                # Ring 3 setup, 12-syscall ABI dispatch
    ├── fs.go                                       # in-memory FS + go fsTask() over native chan
    ├── smp.go                                      # SMP: LAPIC, ACPI MADT, INIT-SIPI-SIPI, AP sti+hlt
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

### User-writable TinyGo copy + runtime patches (required)

gooos needs four local changes to TinyGo's runtime for
`scheduler=tasks` to work in Ring 0. The system TinyGo at
`/usr/local/lib/tinygo/` is root-owned, so the build uses a
user-writable copy at `$HOME/.local/tinygo/` (overridable via
the `TINYGOROOT` environment variable the Makefile exports).

The full edit is captured as a unified diff at
`scripts/tinygo_runtime.patch` (reviewable with
`git apply --stat scripts/tinygo_runtime.patch` against a
pristine TinyGo 0.33.0 tree). It touches four files:

1. **`runtime/runtime_gooos.go`** (new, `gooos && baremetal` build
   tag) — provides `sleepTicks`, `ticks`, `ticksToNanoseconds`,
   `nanosecondsToTicks`, `deadlock`, `putchar`, `preinit`, `exit`,
   `abort`, and the bare-metal `main` entry point that `boot.S`
   calls.
2. **`runtime/interrupt/interrupt_gooos.go`** (new) — implements
   `interrupt.Disable` / `Restore` / `In` + the `State` type,
   backed by gooos's `cli` / `readFlags` / `restoreFlags` and the
   `.bss` counter `gooos_in_interrupt_depth` (`src/isr.S`).
3. **`internal/task/task_stack.go`** (patched in place) — adds a
   `stackTop uintptr` field to the `state` struct and assigns it
   to `canaryPtr + stackSize` in `initialize()`. Needed so
   `src/goroutine_tss.go`'s side table can resolve each
   goroutine's kernel-stack top for TSS.RSP0.
4. **`internal/task/task_stack_amd64.go`** (patched in place) —
   inserts a `gooosOnResume()` call before `swapTask` in the
   `state.resume()` body. This hook is how the gooos kernel
   updates `TSS.RSP0` every time TinyGo's scheduler resumes a
   Ring-3 goroutine.

#### One-time setup after installing TinyGo

```bash
# 1. Mirror the system TinyGo into a user-writable location.
mkdir -p ~/.local/tinygo
cp -a /usr/local/lib/tinygo/. ~/.local/tinygo/

# 2. Apply scripts/tinygo_runtime.patch via the wrapper script.
#    (Equivalent: patch -p1 -d ~/.local/tinygo < scripts/tinygo_runtime.patch)
bash scripts/patch_tinygo_runtime.sh
```

The Makefile exports `TINYGOROOT=$HOME/.local/tinygo` and
invokes `~/.local/tinygo/bin/tinygo`, so `make build` picks up
the patched tree automatically.

The wrapper is **idempotent**: it uses a sentinel check on
`runtime_gooos.go` and skips with an `already-applied:` message
if the patch is already present. Re-run any time after a TinyGo
upgrade or after refreshing `~/.local/tinygo/`.

#### Reverting

```bash
# 1. Delete the two new files (patch -R leaves them empty, not gone).
rm ~/.local/tinygo/src/runtime/runtime_gooos.go
rm ~/.local/tinygo/src/runtime/interrupt/interrupt_gooos.go

# 2. Reverse the two in-place edits.
patch -R -p1 -d ~/.local/tinygo < scripts/tinygo_runtime.patch
```

Rationale: `impldoc/goroutine_design_scheduler.md §5.1` explains
why the runtime files are needed; `impldoc/phase_b_ring3_and_exec.md §4`
explains `gooosOnResume` and the `stackTop` field.

## Build

```bash
make build
```

This runs five phases:

1. **ISR-safety lint**: `scripts/lint_isr.go` walks every ISR-rooted call graph and rejects any string concat, channel op, `go` statement, or runtime allocation. Build fails on violation.
2. **User programs**: `make -C user all` — compiles TinyGo user programs (`sh`, `hello`, `ls`, `cat`, `wc`) into ELF binaries.
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

$ ls
hello.txt
sh.elf
hello.elf
ls.elf
cat.elf
wc.elf
fdprobe.elf

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
  `~/.local/tinygo/src/runtime/runtime_gooos.go:sleepTicks`
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
