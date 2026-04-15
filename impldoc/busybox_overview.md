# BusyBox-Style Interactive Shell — Architecture Overview

## 1. Motivation

gooos currently loads a single hand-crafted 277-byte ELF binary that performs a fixed sequence of syscalls and halts. There is no interactive shell, no way to run arbitrary programs, and no mechanism to compile user programs separately from the kernel.

This design adds:
- An interactive command-line shell running in Ring 3
- Multiple TinyGo-compiled user programs (commands) loaded into the in-memory filesystem at boot
- A userland SDK so that user programs can be compiled with `tinygo build` against a gooos-specific target
- Kernel extensions (syscalls, process lifecycle, FS capacity) to support the above

## 2. High-Level Architecture

```
                          Boot
                            │
                            v
                   ┌─────────────────┐
                   │  Kernel (main)  │
                   │  1. Init HW     │
                   │  2. Load ELFs   │  Embeds user ELF binaries into
                   │     into FS     │  in-memory filesystem at boot
                   │  3. exec shell  │
                   └────────┬────────┘
                            │ elfExec("sh.elf", "")
                            v
              ┌──────────────────────────┐
              │  /sh.elf  (Ring 3)       │
              │  Interactive shell loop  │
              │  ┌────────────────────┐  │
              │  │ prompt → readline  │  │
              │  │ parse cmd + args   │  │
              │  │ built-in? → run    │  │
              │  │ external? ──────────────── sys_exec("cmd.elf", args)
              │  │ loop              │  │          │
              │  └────────────────────┘  │          v
              └──────────────────────────┘   ┌─────────────┐
                                             │ cmd.elf     │
                                             │ (Ring 3)    │
                                             │ runs → exit │
                                             └──────┬──────┘
                                                    │ sys_exit(0)
                                                    v
                                             shell resumes
```

## 3. Components

### 3.1 Kernel Extensions

| Component | Change |
|---|---|
| Filesystem | Increase limits: 32 files, 64 KiB per file |
| Task table | Increase to 32 tasks; reclaim exited task slots and free stack pages |
| Syscall ABI | Redesigned: 12 syscalls covering console I/O, filesystem, process lifecycle, memory |
| ELF loader | New `elfExec()`: save parent pages, unmap, load child ELF, pass arguments, block parent until child exits, restore parent pages on child exit |
| Virtual memory | Track per-process mapped user pages (vaddr+paddr) for cleanup on exit and save/restore on exec |
| Argument passing | Kernel copies argument string to `0x40300000` in user memory; retrievable via `sys_getargs` |
| VGA console | Scrollable VGA text console with cursor management for user output |

### 3.2 Userland SDK (`user/`)

```
user/
├── target.json              # TinyGo target for gooos userspace ELF
├── linker_user.ld           # Linker script: entry at 0x00401000, user heap region
├── runtime/
│   ├── rt0.S                # Minimal startup: call main, then sys_exit
│   └── syscall_stubs.S      # int 0x80 wrappers for each syscall
├── gooos/                   # Go package importable by user programs
│   ├── syscall.go           # Raw syscall wrappers (Syscall0..Syscall4)
│   ├── io.go                # Print, Println, ReadLine, Fprintf
│   ├── fs.go                # ReadFile, WriteFile, ListDir
│   ├── proc.go              # Exec, Exit, Yield, Sleep, Args
│   └── mem.go               # sbrk-based allocator support
└── cmd/                     # User programs (each produces one ELF)
    ├── sh/main.go           # Interactive shell
    ├── echo/main.go         # echo command
    ├── cat/main.go          # cat command
    ├── ls/main.go           # ls command
    ├── wc/main.go           # wc command
    └── hello/main.go        # hello world
```

### 3.3 Build Pipeline

```
┌──────────────┐    tinygo build     ┌────────────┐
│ user/cmd/sh/ │ ─────────────────── │ sh.elf     │ ──┐
│   main.go    │  -target=target.json│ (ET_EXEC)  │   │
└──────────────┘                     └────────────┘   │
┌──────────────┐    tinygo build     ┌────────────┐   │  go:embed or
│ user/cmd/cat/│ ─────────────────── │ cat.elf    │ ──┤  objcopy into
│   main.go    │                     └────────────┘   │  kernel data
└──────────────┘                     ...               │  section
                                                       v
                                               ┌──────────────┐
                                               │  kernel.bin  │
                                               │  (ELF blobs  │
                                               │   in .rodata)│
                                               └──────────────┘
                                                       │
                                              boot: fsCreate + fsWrite
                                              for each embedded ELF
```

Since TinyGo does not support `go:embed` in bare-metal targets, user ELF binaries are converted to Go byte-array source files via a build script (`scripts/embed_elf.sh`) and compiled into the kernel as `[]byte` constants in a generated `src/user_binaries.go` file.

### 3.4 Shell Commands

| Command | Type | Description |
|---|---|---|
| `help` | Built-in | List available commands |
| `echo [args...]` | Built-in | Print arguments to console |
| `clear` | Built-in | Clear VGA screen |
| `exit` | Built-in | Halt the system |
| `ls` | External | List files in the filesystem |
| `cat <file>` | External | Display file contents |
| `wc <file>` | External | Count lines, words, bytes |
| `hello` | External | Print "Hello, World!" |

Built-in commands are functions within `sh.elf`. External commands are separate ELF binaries invoked via `sys_exec`.

## 4. Constraints and Non-Goals

- **No storage I/O**: All files are in-memory only
- **No job control**: No background processes, no Ctrl+C (commands run to completion)
- **No pipes or redirection**: Commands operate on stdin/stdout only
- **No environment variables**: Simplified shell
- **No per-process address space**: All user programs share the same page table (kernel maps/unmaps pages on exec)
- **Single user process at a time**: Shell blocks while a command runs; no concurrent user processes
- **No dynamic linking**: All ELF binaries are statically linked

## 5. Document Index

| Document | Description |
|---|---|
| [busybox_syscall_abi.md](busybox_syscall_abi.md) | Complete syscall ABI specification |
| [busybox_kernel_changes.md](busybox_kernel_changes.md) | Required kernel modifications |
| [busybox_userland_sdk.md](busybox_userland_sdk.md) | TinyGo userland SDK and build system |
| [busybox_shell_spec.md](busybox_shell_spec.md) | Shell specification and commands |
