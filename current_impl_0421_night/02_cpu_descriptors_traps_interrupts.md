# CPU Descriptors, Trap Frames, and Interrupt Dispatch

## IDT Model (`src/idt.go`)

- IDT has 256 entries (`idtTable [256]IDTEntry`).
- `idtInit()` loads ISR stub addresses from `isr_table` (`src/isr.S`) and installs all gates through `setGate()`.
- Gate attributes:
  - kernel interrupt gate: `0x8E`
  - Ring 3-callable interrupt gate: `0xEE` (used by `setGateDPL3(0x80)` for syscall vector)
- AP CPUs must load IDTR via `idtLoadAP()` in `apEntry`.

## ISR Assembly Contract (`src/isr.S`)

### Entry shape

For vectors with no CPU-pushed error code, stub pushes dummy error code first, then vector number.

For vectors with CPU error code, stub pushes only vector.

All vectors branch to `isr_common`.

### `isr_common` behavior

1. Pushes 15 GPRs in fixed order.
2. Increments per-CPU interrupt depth at `%gs:4`.
3. Conditionally increments syscall depth `%gs:44` when vector is `0x80` (syscall) or `0xFB` (preempt IPI).
4. Passes args to Go dispatcher:
   - `%rdi` = vector
   - `%rsi` = error code
   - `%rdx` = frame pointer
5. Calls `go_interrupt_handler`.
6. Mirrors depth decrements (`%gs:44` then `%gs:4`).
7. Restores 15 GPRs and executes `iretq`.

## Go Dispatcher (`src/interrupt.go`)

- `go_interrupt_handler(vector, errorCode, framePtr)`:
  - stores per-CPU last error and frame in `lastErrorCodes[cpu]` and `lastFramePtrs[cpu]`
  - special-cases vector `0x80` by calling `syscallDispatch((*SyscallFrame)(unsafe.Pointer(framePtr)))`
  - otherwise dispatches to `handlers[vector]` if registered

## Syscall Trap Frame ABI (`src/userspace.go`)

`SyscallFrame` field order mirrors ISR push order and CPU-pushed frame suffix:

- GPRs: `R15..RAX`
- ISR metadata: `Vector`, `ErrorCode`
- return frame: `RIP`, `CS`, `RFLAGS`, `RSP`, `SS`

All syscall handlers read/write register fields in this struct directly.

## GDT/TSS Model (`src/gdt.go`)

- GDT entries:
  - null
  - kernel code/data
  - user code/data (DPL=3)
  - TSS low/high
- Per-CPU GDT/TSS arrays (`perCPUGDT`, `perCPUTSS`) are built by `gdtInitPerCPU(cpuIdx)`.
- `tssSetRSP0(rsp0)` writes current CPU TSS RSP0 at offset 4.

## Critical Invariants

1. `idtDesc` must be initialized once on BSP before AP `idtLoadAP()` can reuse it.
2. Vector `0x80` gate must remain DPL=3 to allow userland `int 0x80`.
3. ISR frame layout and `SyscallFrame` layout are lockstep ABI; any ordering change requires updating both assembly and Go struct.
4. `%gs` offsets used in ISR (`4`, `44`) must remain synchronized with `PerCPU` field offsets.

## Failure Modes

- AP with IDTR unset can triple-fault on first exception/interrupt.
- Incorrect TSS descriptor base or stale RSP0 causes Ring 3 -> Ring 0 stack corruption during syscall/interrupt entry.
- Calling runtime operations that may park/allocate inside ISR path can trigger panic or deadlock; ISR helpers are intentionally `//go:nosplit` where required.
