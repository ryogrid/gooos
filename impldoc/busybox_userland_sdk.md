# BusyBox Shell — Userland SDK and Build System

## 1. Overview

User programs for gooos are compiled with TinyGo using a custom target that produces static ELF64 executables linked at a fixed virtual address. A minimal runtime provides startup code and syscall wrappers. A `gooos` Go package offers high-level functions (`Println`, `ReadLine`, `Exec`, etc.) so that user programs read like normal Go code.

## 2. Directory Layout

```
user/
├── Makefile                    # Builds all user programs
├── target.json                 # TinyGo target for gooos userspace
├── linker_user.ld              # Linker script for user ELFs
├── rt0.S                       # Startup assembly (call main, sys_exit)
├── gooos/                      # Go package for user programs
│   ├── syscall.go              # Low-level syscall wrappers
│   ├── io.go                   # Console I/O (Print, ReadLine)
│   ├── fs.go                   # File operations
│   └── proc.go                 # Process control (Exec, Exit, Args)
└── cmd/                        # User program sources
    ├── sh/main.go
    ├── echo/main.go
    ├── cat/main.go
    ├── ls/main.go
    ├── wc/main.go
    └── hello/main.go
```

## 3. TinyGo Target (`user/target.json`)

```json
{
  "inherits": [],
  "llvm-target": "x86_64-unknown-none-elf",
  "cpu": "x86-64",
  "features": "-mmx,-sse,-sse2,-sse3,-ssse3,-sse4.1,-sse4.2,-avx,-avx2,-avx512f",
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

Key choices:
- **`gc: "leaking"`**: Simplest GC — allocates but never frees. Acceptable for short-lived command programs. Avoids the complexity of making conservative GC work in userspace (which requires `findGlobals()`, synthetic ELF headers, and stack scanning — all tied to kernel internals). If a program needs real GC, `gc: "conservative"` can be attempted later with a userspace-specific `__ehdr_start` and mmap stub.
- **`scheduler: "none"`**: No goroutines — user programs are single-threaded.
- **SSE disabled**: Matches the kernel — no floating-point context saving on syscalls.
- **`goos: "linux"`**: TinyGo requires a recognized GOOS. We override the runtime's OS-specific functions.

## 4. Linker Script (`user/linker_user.ld`)

```ld
ENTRY(_start)

SECTIONS
{
    . = 0x40100000;

    .text : {
        *(.text._start)
        *(.text .text.*)
    }

    .rodata : {
        *(.rodata .rodata.*)
    }

    . = ALIGN(4096);

    .data : {
        *(.data .data.*)
    }

    .bss : {
        *(.bss .bss.*)
        *(COMMON)
    }

    . = ALIGN(4096);
    _heap_start = .;
}
```

- Entry point `_start` at `0x40100000` — above the kernel's 1 GiB identity map (`0x00000000`-`0x3FFFFFFF` uses 2 MiB huge pages). This is critical: `mapPage()` cannot split existing huge page entries, so user addresses must be outside the identity-mapped range.
- Sections laid out contiguously: `.text`, `.rodata`, `.data`, `.bss`
- `_heap_start` symbol marks where `sys_sbrk` begins allocating
- The existing kernel ELF binary uses `0x40010000` as entry point — user programs use `0x40100000` to avoid overlap

## 5. Startup Assembly (`user/rt0.S`)

```asm
.section .text._start, "ax"
.global _start
_start:
    # Stack is already set up by the kernel (RSP = 0x7FFFFFFF0)

    # Call TinyGo's runtime entry point.
    # TinyGo emits a 'main' symbol that performs runtime initialization
    # (preinit → initHeap → initAll → callMain → main.main).
    # The exact symbol name must be verified empirically by compiling
    # a test binary and inspecting with: nm build/test.o | grep -i main
    # It may be 'main', 'runtime.main', or '_main' depending on the
    # TinyGo version and target configuration.
    call main

    # If main returns, call sys_exit(0)
    xor  %edi, %edi       # exit code = 0
    xor  %eax, %eax       # syscall 0 = sys_exit
    int  $0x80

    # Should not reach here
    jmp  .
```

This replaces TinyGo's default `_start` / `runtime_unix.go` startup which would call `mmap` and other Linux-specific functions.

**Empirical verification required**: Before implementing, compile a minimal test program with TinyGo using the target.json, inspect the resulting object file with `nm` and `objdump` to determine: (a) the correct runtime entry symbol name, (b) which runtime stubs are called (unresolved symbols), (c) the actual binary size.

## 6. Runtime Overrides

TinyGo's runtime for `goos=linux` calls OS functions like `mmap`, `write`, `exit`, etc. These must be replaced with gooos equivalents.

### `user/gooos/runtime_stubs.go`

```go
package gooos

// Stubs that TinyGo's runtime calls internally.
// These are linked via //go:linkname or //export.

//export write
func write(fd int32, buf unsafe.Pointer, count int32) int32 {
    // Map to sys_write
    return int32(syscallWrite(uintptr(buf), uintptr(count), uintptr(fd)))
}

//export exit
func exit(code int32) {
    syscallExit(uintptr(code))
}

//export mmap
func mmap(addr unsafe.Pointer, length uintptr, prot, flags, fd int32, offset uintptr) unsafe.Pointer {
    // TinyGo's runtime calls mmap during preinit() starting with a 1 GiB
    // request and halving on failure until it gets a small enough region.
    // We cap the allocation to a reasonable user heap size (e.g., 1 MiB)
    // and return failure for anything larger. TinyGo will keep halving.
    const maxUserHeap = 1 * 1024 * 1024 // 1 MiB
    if length == 0 || length > maxUserHeap {
        return unsafe.Pointer(uintptr(0xFFFFFFFFFFFFFFFF)) // MAP_FAILED
    }
    result := syscallSbrk(length)
    if result == 0xFFFFFFFFFFFFFFFF {
        return unsafe.Pointer(uintptr(0xFFFFFFFFFFFFFFFF))
    }
    return unsafe.Pointer(result)
}
```

These stubs allow TinyGo's runtime initialization (`preinit`, `initHeap`, etc.) to work without Linux. The `mmap` stub redirects to `sys_sbrk`, which the kernel handles by mapping new pages.

**Note**: This is the most fragile part of the design. TinyGo's runtime may call additional OS functions not listed here. The exact set of required stubs must be determined empirically by compiling a minimal user program and resolving linker errors. Common additional stubs include:
- `clock_gettime` → return 0 (no real clock)
- `usleep` → call `sys_sleep`
- `getpagesize` → return 4096

## 7. Syscall Wrappers (`user/gooos/syscall.go`)

```go
package gooos

import "unsafe"

// Raw syscall functions — implemented in assembly (user/rt0.S or inline).
// Each loads RAX with the syscall number, args into RDI/RSI/RDX/R10,
// executes int 0x80, and returns RAX.

func syscall0(nr uintptr) uintptr
func syscall1(nr, a1 uintptr) uintptr
func syscall2(nr, a1, a2 uintptr) uintptr
func syscall3(nr, a1, a2, a3 uintptr) uintptr
func syscall4(nr, a1, a2, a3, a4 uintptr) uintptr

const (
    SYS_EXIT      = 0
    SYS_WRITE     = 1
    SYS_READ      = 2
    SYS_EXEC      = 3
    SYS_FS_READ   = 4
    SYS_FS_WRITE  = 5
    SYS_FS_LIST   = 6
    SYS_YIELD     = 7
    SYS_SLEEP     = 8
    SYS_GETARGS   = 9
    SYS_SBRK      = 10
    SYS_VGA_CLEAR = 11
)

func syscallExit(code uintptr)              { syscall1(SYS_EXIT, code) }
func syscallWrite(buf, len, fd uintptr) uintptr { return syscall3(SYS_WRITE, buf, len, fd) }
func syscallRead(buf, max uintptr) uintptr  { return syscall2(SYS_READ, buf, max) }
func syscallSbrk(incr uintptr) uintptr      { return syscall1(SYS_SBRK, incr) }
// ... etc.
```

### Assembly for `syscall0`..`syscall4` (`user/rt0.S`)

```asm
.global syscall0
syscall0:
    movq %rdi, %rax       # nr
    int  $0x80
    ret

.global syscall1
syscall1:
    movq %rdi, %rax       # nr
    movq %rsi, %rdi       # a1
    int  $0x80
    ret

.global syscall2
syscall2:
    movq %rdi, %rax       # nr
    movq %rsi, %rdi       # a1
    movq %rdx, %rsi       # a2
    int  $0x80
    ret

.global syscall3
syscall3:
    movq %rdi, %rax       # nr
    movq %rsi, %rdi       # a1
    movq %rdx, %rsi       # a2
    movq %rcx, %rdx       # a3
    int  $0x80
    ret

.global syscall4
syscall4:
    movq %rdi, %rax       # nr
    movq %rsi, %rdi       # a1
    movq %rdx, %rsi       # a2
    movq %rcx, %rdx       # a3
    movq %r8,  %r10       # a4
    int  $0x80
    ret
```

## 8. High-Level I/O (`user/gooos/io.go`)

```go
package gooos

import "unsafe"

// Print writes a string to VGA + serial.
func Print(s string) {
    if len(s) == 0 {
        return
    }
    syscallWrite(
        uintptr(unsafe.Pointer(unsafe.StringData(s))),
        uintptr(len(s)),
        0,  // fd=0 → VGA+serial
    )
}

// Println writes a string followed by a newline.
func Println(s string) {
    Print(s)
    Print("\n")
}

// ReadLine reads one line of input from the keyboard (blocking).
// Returns the input without the trailing newline.
func ReadLine() string {
    var buf [128]byte
    n := syscallRead(uintptr(unsafe.Pointer(&buf[0])), 128)
    return string(buf[:n])
}
```

## 9. File Operations (`user/gooos/fs.go`)

```go
package gooos

import "unsafe"

// ReadFile reads the full contents of a named file.
// Returns nil if the file does not exist.
//
// The buffer is heap-allocated (not stack-allocated) because the user
// stack is only 8 KiB — a 64 KiB stack variable would overflow immediately.
// With leaking GC, the buffer is never freed, which is acceptable for
// short-lived command programs.
func ReadFile(name string) []byte {
    buf := make([]byte, 65536)
    nameBytes := []byte(name)
    n := syscall4(SYS_FS_READ,
        uintptr(unsafe.Pointer(&nameBytes[0])),
        uintptr(len(name)),
        uintptr(unsafe.Pointer(&buf[0])),
        65536,
    )
    if n == 0xFFFFFFFFFFFFFFFF {
        return nil
    }
    return buf[:n]
}

// ListDir returns all filenames in the filesystem.
func ListDir() []string {
    var buf [4096]byte
    n := syscall2(SYS_FS_LIST,
        uintptr(unsafe.Pointer(&buf[0])),
        4096,
    )
    // Parse NUL-separated names
    var names []string
    start := 0
    for i := uintptr(0); i < n; i++ {
        if buf[i] == 0 {
            if int(i) > start {
                names = append(names, string(buf[start:i]))
            }
            start = int(i) + 1
        }
    }
    return names
}
```

## 10. Process Control (`user/gooos/proc.go`)

```go
package gooos

import "unsafe"

// Exit terminates the current process with the given exit code.
func Exit(code int) {
    syscallExit(uintptr(code))
}

// Exec runs a named program and blocks until it completes.
// Returns the child's exit code.
func Exec(path string, args string) int {
    result := syscall4(SYS_EXEC,
        uintptr(unsafe.Pointer(unsafe.StringData(path))),
        uintptr(len(path)),
        uintptr(unsafe.Pointer(unsafe.StringData(args))),
        uintptr(len(args)),
    )
    if result == 0xFFFFFFFFFFFFFFFF {
        return -1
    }
    return int(result)
}

// Args returns the argument string passed to this process.
func Args() string {
    var buf [256]byte
    n := syscall2(SYS_GETARGS,
        uintptr(unsafe.Pointer(&buf[0])),
        256,
    )
    return string(buf[:n])
}

// Yield voluntarily yields the CPU.
func Yield() {
    syscall0(SYS_YIELD)
}

// Sleep sleeps for approximately ms milliseconds.
func Sleep(ms int) {
    ticks := uintptr(ms / 10) // 100 Hz PIT → 10 ms per tick
    if ticks == 0 {
        ticks = 1
    }
    syscall1(SYS_SLEEP, ticks)
}
```

## 11. Build System (`user/Makefile`)

```makefile
TINYGO  ?= tinygo
AS      ?= as
TARGET  := target.json
LDSCRIPT := linker_user.ld
BUILD   := build

CMDS := sh echo cat ls wc hello
ELFS := $(patsubst %,$(BUILD)/%.elf,$(CMDS))

.PHONY: all clean

all: $(ELFS)

$(BUILD):
	mkdir -p $(BUILD)

$(BUILD)/rt0.o: rt0.S | $(BUILD)
	$(AS) --64 rt0.S -o $(BUILD)/rt0.o

$(BUILD)/%.elf: cmd/%/main.go gooos/*.go $(BUILD)/rt0.o $(LDSCRIPT) $(TARGET)
	$(TINYGO) build -target=$(TARGET) \
	    -ldflags="-T $(LDSCRIPT)" \
	    -o $@ ./cmd/$*

clean:
	rm -rf $(BUILD)
```

**Note**: TinyGo's handling of extra assembly files and custom linker scripts in `target.json` may require the `extra-files` and `linkerscript` fields. If TinyGo resolves paths relative to its install directory (the same issue the kernel build has), the user Makefile must use the same two-step approach: compile Go to `.o`, assemble `rt0.S` separately, and link with `ld.lld`.

### Two-Step Fallback

```makefile
$(BUILD)/%.elf: cmd/%/main.go gooos/*.go $(BUILD)/rt0.o $(LDSCRIPT) $(TARGET)
	$(TINYGO) build -target=$(TARGET) -o $(BUILD)/$*_go.o ./cmd/$*
	ld.lld -m elf_x86_64 -n -T $(LDSCRIPT) \
	    -o $@ $(BUILD)/rt0.o $(BUILD)/$*_go.o
```

## 12. Top-Level Build Integration

The root `Makefile` gains a `user` target:

```makefile
.PHONY: user embed-user

user:
	$(MAKE) -C user all

embed-user: user
	bash scripts/embed_elfs.sh

build: embed-user $(KERNEL_BIN)
```

This ensures user binaries are compiled, embedded into `src/user_binaries.go`, and then compiled into the kernel.

## 13. Example User Program (`user/cmd/hello/main.go`)

```go
package main

import "user/gooos"

func main() {
    gooos.Println("Hello, World!")
}
```

## 14. Open Questions and Risks

1. **TinyGo runtime compatibility** (HIGH RISK): The exact set of runtime stubs (`mmap`, `write`, `exit`, `clock_gettime`, `usleep`, `getpagesize`, etc.) required by TinyGo's internal runtime must be determined empirically. Compile a minimal test program, link it, and resolve all undefined symbols. This is the highest-risk item and should be tackled first as a proof-of-concept before implementing kernel changes.

2. **TinyGo entry point symbol** (HIGH RISK): The `rt0.S` startup calls `main`, but the actual TinyGo runtime entry symbol depends on the target. Must verify with `nm` on a compiled object file. May be `main`, `runtime.main`, `_main`, etc.

3. **Binary size**: TinyGo with `gc=leaking` and `scheduler=none` produces binaries around 10-40 KiB for simple programs. If binaries exceed 64 KiB, the FS file size limit must be increased further.

4. **`unsafe.StringData`**: Available in Go 1.20+. If TinyGo does not support it, use `unsafe.Pointer(&([]byte(s))[0])` instead (allocates, but acceptable with leaking GC).

5. **Stack size**: The 8 KiB user stack (2 pages) may be insufficient for programs that use deep call stacks or large local variables. Page faults at addresses near `0x7FFF0000` indicate stack overflow. Consider adding a guard page (one unmapped page below the stack) to make stack overflow distinguishable from other page faults.

6. **TSS RSP0**: All Ring 3 → Ring 0 transitions share a single kernel stack (set in TSS RSP0 during `gdtInit`). This is safe because only one user process runs at a time, but would need per-task kernel stacks if concurrent user processes are ever added.

7. **FS `.bss` growth**: Increasing `maxFileData` to 64 KiB with 32 files adds ~2 MiB to `.bss`. Consider dynamic allocation via `allocPage()` if this is too large. The kernel's 4 MiB heap and linker script must accommodate the growth.

8. **Register shuffling in syscall wrappers**: The assembly wrappers remap Go's SysV ABI register assignments (RDI=nr, RSI=a1, RDX=a2, RCX=a3, R8=a4) to the syscall ABI (RAX=nr, RDI=a1, RSI=a2, RDX=a3, R10=a4). This is correct but non-obvious — each wrapper contains a comment explaining the mapping.
