# Known Issues, Workarounds, and Limitations

## Active Workarounds (Should Be Properly Fixed)

### 1. Schedule Switch Logging Disabled
- **File**: `src/scheduler.go` — `serialPrint("Switch: ...")` commented out
- **Reason**: `utoa()` for task ID formatting allocates on the kernel Go heap (string concatenation). In ISR context (timer handler), this could trigger the GC (when enabled), causing reentrancy issues or metadata corruption.
- **Impact**: No serial log of task switches. Uncomment for debugging, but be aware of heap allocation side effects.

## Functional Limitations

### Input
- **No shift key / uppercase**: Keyboard driver maps only lowercase letters, digits, and basic punctuation. No Shift, Caps Lock, or modifier key tracking.
- **No arrow keys, Tab, Ctrl+C**: Only printable characters, Backspace, Enter, and Space are handled.

### Process Model
- **No nested exec**: `sys_exec` from a child process is rejected. The `savedParent` variable is a single global, supporting only one level of parent-child nesting (shell → command).
- **sys_read not concurrent**: `sysReadLineBuf` is a global 128-byte buffer. If multiple tasks called `sys_read` simultaneously, they would corrupt each other's input. Currently only one user task reads at a time.
- **No user pointer validation**: Syscall handlers dereference user-provided buffer addresses without checking they are within the valid user range (>= `0x40000000`). A malicious user program could read/write kernel memory.

### Memory
- **No sbrk bounds check**: `sys_sbrk` does not enforce an upper limit on heap growth. If the heap grows past the user address range, it could collide with other mappings.
- **Single address space**: All user processes share the same page table (CR3). Parent pages are unmapped during child exec and restored on exit. Concurrent user processes are not supported.

### Shell
- **echo is built-in only**: `echo` runs as a shell function, not a separate ELF binary.
- **wc command untested**: Built and embedded but not verified via automated tests.

### Code Quality
- **Dead code**: `keyboardConsumerTask`, demo task functions (A/B/C), channel test tasks, select test tasks, and `userPrintTask` remain in source files but are not spawned at boot. Their assembly address helpers also remain in `switch.S`.
- **NUL termination**: `sys_read` does not NUL-terminate the returned string (design doc says "if space permits" but implementation doesn't do it).

## Debugging Notes

- **Page fault handler** (`src/vm.go:handlePageFault`): displays faulting address (CR2), error code, and RIP from the saved ISR frame. Halts the CPU.
- **Error code bits**: bit 0 = present/not-present, bit 1 = read/write, bit 2 = supervisor/user.
- **QEMU debug**: `make run` with `-d int 2>int.log` captures all interrupt events. Search for `check_exception` to find double/triple faults. `info status` in QEMU monitor shows `paused (shutdown)` on triple fault.
- **Serial output**: All kernel log messages go to COM1 (`-serial stdio`). User `sys_write` with fd=0 also outputs to serial.
