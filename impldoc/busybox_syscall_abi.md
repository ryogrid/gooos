# BusyBox Shell — Syscall ABI Specification

## 1. Calling Convention

Syscalls are invoked via `int 0x80` from Ring 3. The kernel ISR stub saves the full register frame, dispatches to `syscallDispatch()`, and returns via `iretq`.

| Register | Purpose |
|---|---|
| `RAX` | Syscall number (input); return value (output) |
| `RDI` | Argument 1 |
| `RSI` | Argument 2 |
| `RDX` | Argument 3 |
| `R10` | Argument 4 |
| `R8` | Argument 5 |
| `R9` | Argument 6 |

Return value in `RAX`: 0 or positive on success, `0xFFFFFFFFFFFFFFFF` (-1) on error.

Callee-saved registers (`RBX`, `RBP`, `R12`-`R15`) are preserved across syscalls.

## 2. Syscall Table

| Nr | Name | RDI | RSI | RDX | R10 | Returns | Description |
|---|---|---|---|---|---|---|---|
| 0 | `sys_exit` | exit_code | — | — | — | (no return) | Terminate current process |
| 1 | `sys_write` | buf_ptr | buf_len | fd | — | bytes written | Write to output (fd: 0=VGA+serial, 1=serial only) |
| 2 | `sys_read` | buf_ptr | buf_max | — | — | bytes read | Read one line from keyboard (blocking, line-buffered) |
| 3 | `sys_exec` | path_ptr | path_len | arg_ptr | arg_len | child exit code | Load ELF from FS, run to completion, return exit code |
| 4 | `sys_fs_read` | path_ptr | path_len | out_buf | out_max | bytes read or -1 | Read file contents into buffer |
| 5 | `sys_fs_write` | path_ptr | path_len | data_ptr | data_len | 0 or -1 | Write data to file (create if absent) |
| 6 | `sys_fs_list` | buf_ptr | buf_max | — | — | bytes written | List filenames (NUL-separated) into buffer |
| 7 | `sys_yield` | — | — | — | — | 0 | Yield CPU to next ready task |
| 8 | `sys_sleep` | ticks | — | — | — | 0 | Sleep for N PIT ticks (~N*10 ms) |
| 9 | `sys_getargs` | buf_ptr | buf_max | — | — | bytes written | Copy argument string into buffer |
| 10 | `sys_sbrk` | increment | — | — | — | old break addr or -1 | Grow user heap by increment bytes |
| 11 | `sys_vga_clear` | — | — | — | — | 0 | Clear VGA text buffer |

## 3. Syscall Details

### 3.0 `sys_exit` (0)

Terminates the current user process. Sets the task state to `taskExited`.  If the process was launched via `sys_exec` from a parent (e.g., the shell), the parent is unblocked and receives the exit code.

- `RDI`: Exit code (0 = success)
- Does not return.

### 3.1 `sys_write` (1)

Writes a byte buffer to an output device.

- `RDI`: Pointer to buffer in user memory
- `RSI`: Number of bytes to write (max 4096)
- `RDX`: File descriptor:
  - `0` — VGA text buffer (appended line-by-line) + serial
  - `1` — Serial only
- Returns: Number of bytes written

The kernel validates that the buffer address is within user-accessible memory (above `0x00400000`). VGA output appends text starting from a scrollable region (lines 1-24, line 0 reserved for status).

### 3.2 `sys_read` (2)

Reads one line of input from the keyboard. The kernel buffers keystrokes internally and returns when Enter is pressed. Backspace is handled by the kernel.

- `RDI`: Pointer to destination buffer in user memory
- `RSI`: Maximum bytes to read
- Returns: Number of bytes placed in buffer (excluding the terminating NUL; NUL is appended if space permits)
- The returned string does **not** include the newline character

The kernel echoes typed characters to VGA and serial as the user types.

### 3.3 `sys_exec` (3)

Loads an ELF binary from the in-memory filesystem and executes it as a child process. The calling task (shell) blocks until the child exits.

- `RDI`: Pointer to filename string
- `RSI`: Length of filename
- `RDX`: Pointer to argument string (space-separated)
- `R10`: Length of argument string
- Returns: Child's exit code (from `sys_exit`), or -1 if the file is not found or the ELF is invalid

Implementation:
1. Kernel copies the filename and argument strings from user memory to kernel buffers
2. Reads the ELF binary from FS
3. Allocates a new task slot (reusing a `taskFree` slot if available)
4. **Saves** the parent's user page mappings (vaddr→paddr pairs) in the parent's `Process` struct
5. **Unmaps** the parent's user pages (clears PTEs but does NOT free physical pages)
6. Maps the child's PT_LOAD segments into the virtual address range (e.g., `0x40100000`+)
7. Copies the argument string to `0x40300000` (argument page)
8. Allocates user stack at `0x7FFF0000` (2 pages)
9. Creates a kernel-mode trampoline task that calls `jumpToRing3(entry, stackTop)` to enter Ring 3
10. Sets the child task state to `taskReady`
11. Sets the calling (parent) task state to `taskBlocked`
12. When the child calls `sys_exit`, the kernel:
    - Unmaps and frees all child user pages (code, data, heap, stack, argument page)
    - Frees the child's kernel stack page
    - **Restores** the parent's saved page mappings (re-creates PTEs to parent's preserved physical pages)
    - Sets child state to `taskFree` (slot available for reuse)
    - Stores the exit code
    - Unblocks the parent, writing the exit code to the parent's `RAX`

The child inherits the parent's channel registrations (keyboard channel ID 0, etc.) so it can perform I/O via the same channel IDs.

Note: `taskCount` is a high-water mark that is never decremented. The scheduler scans up to `taskCount` and skips `taskFree`/`taskExited` slots.

### 3.4 `sys_fs_read` (4)

Reads the full contents of a named file.

- `RDI`: Pointer to filename string
- `RSI`: Length of filename
- `RDX`: Pointer to output buffer
- `R10`: Maximum bytes to read
- Returns: Number of bytes read, or -1 if file not found

### 3.5 `sys_fs_write` (5)

Creates a file (if it does not exist) and writes data to it, replacing any existing contents.

- `RDI`: Pointer to filename string
- `RSI`: Length of filename
- `RDX`: Pointer to data buffer
- `R10`: Length of data
- Returns: 0 on success, -1 on error (filesystem full, data too large)

### 3.6 `sys_fs_list` (6)

Lists all filenames in the filesystem.

- `RDI`: Pointer to output buffer
- `RSI`: Maximum bytes
- Returns: Total bytes written

Filenames are written as NUL-separated strings: `"file1\0file2\0file3\0"`.

### 3.7 `sys_yield` (7)

Voluntarily yields the CPU to the next ready task. Equivalent to a cooperative scheduling point.

### 3.8 `sys_sleep` (8)

Blocks the calling task for a specified number of PIT ticks (~10 ms each at 100 Hz).

- `RDI`: Number of ticks to sleep

### 3.9 `sys_getargs` (9)

Copies the argument string passed to the current process into the provided buffer. For the initial shell process, this returns an empty string.

- `RDI`: Pointer to buffer
- `RSI`: Maximum bytes
- Returns: Number of bytes written (may be 0 if no arguments)

### 3.10 `sys_sbrk` (10)

Grows the user heap by `increment` bytes. The kernel maintains a per-process break pointer (initially at the end of the last PT_LOAD segment, page-aligned up). When the break crosses a page boundary, new pages are allocated and mapped with user flags.

- `RDI`: Number of bytes to grow (must be positive; shrinking is not supported)
- Returns: Previous break address (the start of the newly allocated region), or -1 if allocation fails

This syscall supports TinyGo's conservative GC, which needs to allocate heap memory. The userland runtime's `sbrk()` function calls this syscall.

### 3.11 `sys_vga_clear` (11)

Clears the VGA text buffer (all 80x25 cells set to space with white-on-black attribute).

## 4. Memory Layout for User Processes

```
0x00000000 - 0x3FFFFFFF    Kernel identity map (1 GiB, 2 MiB huge pages — NOT user-accessible)
0x40000000 - 0x400FFFFF    (reserved — VM demo range)
0x40100000 - 0x401FFFFF    User code (.text, .rodata) — mapped from ELF PT_LOAD
0x40200000 - 0x402FFFFF    User data (.data, .bss) — mapped from ELF PT_LOAD
0x40300000 - 0x403FFFFF    Argument page — kernel writes arg string here before exec
0x40400000 - 0x40BFFFFF    User heap — grown via sys_sbrk (max ~8 MiB)
0x7FFF0000 - 0x7FFFFFFF    User stack (8 KiB, 2 pages, grows downward)
```

All user addresses are above `0x40000000`, outside the kernel's boot-time 1 GiB identity map (which covers `0x00000000`-`0x3FFFFFFF` using 2 MiB huge pages set up in `boot.S`). This is critical: `mapPage()` does not split existing 2 MiB huge page entries, so user pages **must not** overlap the identity-mapped range. The kernel maps user pages at 4 KiB granularity with `pagePresent | pageWrite | pageUser`.

The current ELF binaries already use `0x40010000` as their entry point, and the user stack is at `0x7FFF0000` (PDP index 1, outside PDP[0]'s 1 GiB coverage). Both are safe.

### Address space during `sys_exec`

When the shell calls `sys_exec`, the kernel must preserve the shell's page mappings while the child runs. The sequence is:

1. **Save** the shell's user page mappings (virtual-to-physical pairs) in the shell's `Process` struct
2. **Unmap** the shell's user pages (clear PTEs) but do **not** free the physical pages
3. **Map** the child's PT_LOAD segments, argument page, and stack into the same virtual address region
4. The child runs; the shell is blocked
5. When the child calls `sys_exit`:
   - **Unmap and free** all child user pages (including heap, stack, argument page)
   - **Restore** the shell's saved page mappings (re-create PTEs pointing to the shell's preserved physical pages)
6. The shell resumes execution with its original code, data, and stack intact

This means the shell and child use the **same virtual address range** but different physical pages, swapped in/out by the kernel.

## 5. Backward Compatibility

The old syscall numbers (0-5) are replaced entirely. The hand-crafted user ELF binary in `userspace.go` is removed and replaced by the TinyGo-compiled shell binary.
