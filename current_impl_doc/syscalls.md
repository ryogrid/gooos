# Syscall ABI Reference

## Calling Convention

Syscalls are invoked from Ring 3 via `int 0x80`. The kernel ISR stub (`isr.S:isr_common`) saves all registers, dispatches to `go_interrupt_handler` (`interrupt.go`), which special-cases vector `0x80` to call `syscallDispatch` (`userspace.go`).

| Register | Purpose |
|---|---|
| `RAX` | Syscall number (input); return value (output) |
| `RDI` | Argument 1 |
| `RSI` | Argument 2 |
| `RDX` | Argument 3 |
| `R10` | Argument 4 |

Return: 0 or positive on success, `0xFFFFFFFFFFFFFFFF` (-1) on error.

Callee-saved registers (`RBX`, `RBP`, `R12`-`R15`) are preserved across syscalls.

## SyscallFrame Layout (`userspace.go`)

The saved register frame (lowest address first):

```
R15, R14, R13, R12, R11, R10, R9, R8,   (pushed by isr_common)
RBP, RDI, RSI, RDX, RCX, RBX, RAX,      (pushed by isr_common)
Vector, ErrorCode,                        (pushed by ISR macro)
RIP, CS, RFLAGS, RSP, SS                  (pushed by CPU on privilege change)
```

## Syscall Table

| Nr | Name | RDI | RSI | RDX | R10 | Returns | Handler |
|---|---|---|---|---|---|---|---|
| 0 | `sys_exit` | exit_code | — | — | — | (no return) | `sysExitHandler` |
| 1 | `sys_write` | buf_ptr | buf_len | fd | — | bytes written | `sysWriteHandler` |
| 2 | `sys_read` | buf_ptr | buf_max | — | — | bytes read | `sysReadHandler` |
| 3 | `sys_exec` | path_ptr | path_len | arg_ptr | arg_len | child exit code | `sysExecHandler` |
| 4 | `sys_fs_read` | path_ptr | path_len | out_buf | out_max | bytes read / -1 | `sysFsReadHandler` |
| 5 | `sys_fs_write` | path_ptr | path_len | data_ptr | data_len | 0 / -1 | `sysFsWriteHandler` |
| 6 | `sys_fs_list` | buf_ptr | buf_max | — | — | bytes written | `sysFsListHandler` |
| 7 | `sys_yield` | — | — | — | — | 0 | `sysYieldHandler` |
| 8 | `sys_sleep` | ticks | — | — | — | 0 | `sysSleepHandler` |
| 9 | `sys_getargs` | buf_ptr | buf_max | — | — | bytes written | `sysGetargsHandler` |
| 10 | `sys_sbrk` | increment | — | — | — | old break / -1 | `sysSbrkHandler` |
| 11 | `sys_vga_clear` | — | — | — | — | 0 | `sysVgaClearHandler` |

## Syscall Details

### `sys_exit` (0)
Terminates the current process via `processExit(exitCode)`. If the process has a parent (was launched by `sys_exec`), the parent is unblocked and receives the exit code. If no parent, halts the CPU.

### `sys_write` (1)
Writes up to 4096 bytes to an output device.
- `fd=0`: VGA console (scrollable, cursor-tracked via `vgaConsolePutChar`) + serial
- `fd=1`: serial only

### `sys_read` (2)
Blocking line-buffered keyboard input with kernel-side echo. The kernel:
1. Blocks on `chanRecv(userKeyboardChannel)` in a loop (via `waitQueueSleep` with `sti+hlt` idle)
2. Echoes printable characters to VGA + serial
3. Handles backspace (removes last char)
4. On Enter: copies line buffer to user memory, returns byte count

Global 128-byte kernel line buffer (`sysReadLineBuf`).

### `sys_exec` (3)
Loads an ELF binary from the in-memory filesystem and executes it as a child process. The calling task blocks until the child exits. Nested exec is rejected (only one level of parent-child nesting supported). See [scheduler.md](scheduler.md) for the full elfExec/processExit flow.

### `sys_fs_read` (4), `sys_fs_write` (5), `sys_fs_list` (6)
Filesystem operations routed through the FS service task via channel IPC. See [ipc.md](ipc.md).
- `sys_fs_list` returns NUL-separated filenames: `"file1\0file2\0file3\0"`
- `sys_fs_write` auto-creates the file if it doesn't exist

### `sys_sbrk` (10)
Grows the per-process user heap. Each process has a `HeapBreak` pointer (initialized to end of last PT_LOAD segment, page-aligned). On call:
1. Saves `oldBreak = HeapBreak`
2. Allocates and maps pages from `oldBreak` to `oldBreak + increment`
3. Records pages in process table for cleanup
4. Returns `oldBreak`

Used by TinyGo's `mmap` stub in `user/rt0.S` to provide heap memory.

## Userspace Syscall Wrappers

User programs call syscalls via assembly stubs in `user/rt0.S`:

```
syscall0(nr)           → RAX=nr, int $0x80
syscall1(nr, a1)       → RAX=nr, RDI=a1, int $0x80
syscall2(nr, a1, a2)   → RAX=nr, RDI=a1, RSI=a2, int $0x80
syscall3(nr, a1, a2, a3) → RAX=nr, RDI=a1, RSI=a2, RDX=a3, int $0x80
syscall4(nr, a1, a2, a3, a4) → RAX=nr, RDI=a1, RSI=a2, RDX=a3, R10=a4, int $0x80
```

Note: Go's SysV ABI passes function args in RDI/RSI/RDX/RCX/R8. The assembly shims remap these to the kernel's syscall register convention.
