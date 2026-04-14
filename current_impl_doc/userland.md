# Userland SDK and User Programs

## TinyGo Target (`user/target.json`)

```json
{
  "llvm-target": "x86_64-unknown-none-elf",
  "cpu": "x86-64",
  "features": "-mmx,-sse,-sse2,...,-avx512f",
  "goos": "linux",
  "goarch": "amd64",
  "gc": "leaking",
  "scheduler": "none",
  "panic-strategy": "trap",
  "linker": "ld.lld",
  "rtlib": "compiler-rt",
  "default-stack-size": 4096
}
```

Key: `gc=leaking` (no garbage collection), `scheduler=none` (no goroutines), SSE disabled, `goos=linux` (required by TinyGo to compile with Unix runtime stubs).

## Linker Script (`user/linker_user.ld`)

- Entry: `_start` at `0x40100000` (above kernel's 1 GiB identity map)
- Sections: `.text`, `.rodata`, `.data`, `.bss`
- `_heap_start` symbol after `.bss` for `sys_sbrk`

## Startup Assembly (`user/rt0.S`)

### `_start`
```asm
_start:
    call main          # TinyGo runtime entry → main.main
    xor  %edi, %edi    # exit code = 0
    xor  %eax, %eax    # syscall 0 = sys_exit
    int  $0x80
    jmp  .             # unreachable
```

### Syscall Wrappers (`syscall0`..`syscall4`)
Remap Go's SysV ABI registers to kernel syscall ABI:
- Go caller: `RDI=nr, RSI=a1, RDX=a2, RCX=a3, R8=a4`
- Kernel ABI: `RAX=nr, RDI=a1, RSI=a2, RDX=a3, R10=a4`

### Runtime Stubs
Required by TinyGo's `runtime_unix.go` (5 undefined symbols):

| Stub | Implementation |
|---|---|
| `abort` | `sys_exit(0)` + halt loop |
| `raise` | return 0 (no-op) |
| `tinygo_register_fatal_signals` | no-op (`ret`) |
| `write(fd, buf, count)` | remap to `sys_write(buf, count, fd)` |
| `mmap(addr, length, ...)` | if length <= 1 MiB: `sys_sbrk(length)`, else return -1 |
| `memcpy(dest, src, n)` | `rep movsb` |
| `memset(dest, c, n)` | `rep stosb` |

TinyGo's `preinit()` calls `mmap` starting with 1 GiB, halving on failure until <= 1 MiB. The `mmap` stub caps at 1 MiB and delegates to `sys_sbrk`.

## Go Package (`user/gooos/`)

### `syscall.go` — Raw Wrappers
```go
func syscall0(nr uintptr) uintptr            // linkname to assembly
func syscall1(nr, a1 uintptr) uintptr
func syscall2(nr, a1, a2 uintptr) uintptr
func syscall3(nr, a1, a2, a3 uintptr) uintptr
func syscall4(nr, a1, a2, a3, a4 uintptr) uintptr
```

Constants: `sysExit=0` through `sysVgaClear=11` (matching kernel `src/userspace.go`).

### `io.go` — Console I/O
- `Print(s)` / `Println(s)` — `sys_write` to fd=0 (VGA+serial)
- `ReadLine()` — `sys_read` into 128-byte buffer, returns string
- `VgaClear()` — `sys_vga_clear`

### `fs.go` — File Operations
- `ReadFile(name) []byte` — `sys_fs_read` with 64 KiB **heap-allocated** buffer (stack is only 8 KiB)
- `ListDir() []string` — `sys_fs_list` with 4 KiB heap-allocated buffer, parses NUL-separated names

### `proc.go` — Process Control
- `Exit(code)`, `Exec(path, args) int`, `Args() string`, `Yield()`, `Sleep(ms)`

## User Programs (`user/cmd/`)

| Program | Binary Size | Description |
|---|---|---|
| `sh` | 37 KiB | Interactive shell with built-in commands |
| `hello` | 27 KiB | Prints "Hello, World from gooos userspace!" |
| `ls` | 33 KiB | Lists files via `ListDir()` |
| `cat` | 33 KiB | Displays file contents via `ReadFile(Args())` |
| `wc` | 35 KiB | Counts lines, words, bytes in a file |

### Shell (`user/cmd/sh/main.go`)

Main loop: `VgaClear()` → prompt `$ ` → `ReadLine()` → parse (split on first space) → dispatch

**Built-in commands**: `help`, `echo`, `clear`, `exit`
**External commands**: `Exec(cmd + ".elf", args)` — prints "command not found" on failure

## Build System

### User Makefile (`user/Makefile`)
Two-step per command:
```bash
tinygo build -target=target.json -o build/<cmd>_go.o ./cmd/<cmd>
ld.lld -m elf_x86_64 -n -T linker_user.ld -o build/<cmd>.elf build/rt0.o build/<cmd>_go.o
```

### Embedding (`scripts/embed_elfs.sh`)
Converts `user/build/*.elf` to `src/user_binaries.go`:
```go
var userElf_sh = [...]byte{ 0x7f, 0x45, 0x4c, 0x46, ... }
var userElf_hello = [...]byte{ ... }
// etc.
```

### Root Makefile Integration
`make build` = `make user` → `make embed-user` → assemble `.S` files → `tinygo build` kernel → `ld.lld` link
