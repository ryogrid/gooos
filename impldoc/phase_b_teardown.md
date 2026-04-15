# Phase B — Teardown (B6, B7, B8, B10, B11)

Five small-to-medium migrations. Each retires a pocket of legacy
infrastructure once its predecessors have drained the callers.

## 1. Scope

| Item | Topic                                            | Section |
|------|--------------------------------------------------|---------|
| B6   | Fatal handlers → non-allocating `serialPanicPrint` | §2     |
| B7   | `createTask` → `go` in `src/main.go`             | §3      |
| B8   | Delete `src/scheduler.go` + dead stubs           | §4      |
| B10  | Delete `src/channel.go`, strip `src/switch.S`    | §5      |
| B11  | `src/smp.go` AP idle loop `sti; hlt`             | §6      |

## 2. B6 — Fatal handlers

### 2.1 Today

`handlePageFault` (`src/vm.go:273`) and `handleDivisionError`
(`src/main.go:87`) both use `serialPrintln` + `vgaWriteLine`.
`serialPrintln` itself is allocation-free (direct UART writes),
but the string concatenation via `+` in the current fatal bodies
allocates:

```go
// src/vm.go
msg := "PF: addr=0x" + hextoa(uint64(faultAddr)) + " err=0x" + hextoa(errCode) + " rip=0x" + hextoa(uint64(faultRIP))
vgaWriteLine(12, msg)
serialPrintln(msg)
```

Every `+` is a heap allocation of a new string. `hextoa` itself
allocates via `string(buf[i:])`. Under `gc=conservative` this
triggers the allocator, which under ISR context interacts with
whatever the interrupted goroutine was doing in its own alloc
path. Phase A documented this as `R-fatal-detail-loss`.

### 2.2 New helper

Add to `src/serial.go`:

```go
// serialPanicPrint writes `n` bytes from `buf` to COM1 with no
// heap allocation and no channel involvement. Safe from any
// context including ISR during heap-allocator reentrancy.
//
//go:nosplit
func serialPanicPrint(buf []byte) {
    for i := 0; i < len(buf); i++ {
        serialPutChar(buf[i])
    }
}
```

And an allocation-free hex helper:

```go
// src/serial.go

// panicHexBuf is a static scratch buffer for the fatal path.
// A single fault handler runs before halt, so no concurrent use.
var panicHexBuf [96]byte

// appendHex writes "0x" + 16 hex digits of v into buf starting
// at offset off. Returns the new offset. No allocation.
//go:nosplit
func appendHex(buf []byte, off int, v uint64) int {
    const hex = "0123456789ABCDEF"
    buf[off] = '0'; off++
    buf[off] = 'x'; off++
    for i := 60; i >= 0; i -= 4 {
        buf[off] = hex[(v>>uint(i))&0xF]
        off++
    }
    return off
}

//go:nosplit
func appendStr(buf []byte, off int, s string) int {
    for i := 0; i < len(s); i++ {
        buf[off] = s[i]
        off++
    }
    return off
}
```

### 2.3 New `handlePageFault`

```go
// src/vm.go

//go:nosplit
func handlePageFault(vector uint64) {
    faultAddr := readCR2()
    errCode := lastErrorCode
    frame := (*SyscallFrame)(unsafe.Pointer(lastFramePtr))
    faultRIP := frame.RIP

    off := 0
    off = appendStr(panicHexBuf[:], off, "PF addr=")
    off = appendHex(panicHexBuf[:], off, uint64(faultAddr))
    off = appendStr(panicHexBuf[:], off, " err=")
    off = appendHex(panicHexBuf[:], off, errCode)
    off = appendStr(panicHexBuf[:], off, " rip=")
    off = appendHex(panicHexBuf[:], off, uint64(faultRIP))
    panicHexBuf[off] = '\r'; off++
    panicHexBuf[off] = '\n'; off++

    serialPanicPrint(panicHexBuf[:off])

    // VGA: vgaWriteLine uses a fixed-size array slot write —
    // no heap allocation. Safe to keep.
    // But the original code passed `msg` (a Go string). We
    // build a string header without allocating via unsafe:
    s := bytesToString(panicHexBuf[:off])
    vgaWriteLine(12, s)

    for { hlt() }
}

// bytesToString constructs a string header pointing at b's data
// without copying. Lifetime: same as b. Safe here because
// panicHexBuf outlives the halt loop.
//go:nosplit
func bytesToString(b []byte) string {
    // Convert via unsafe to avoid allocation.
    return *(*string)(unsafe.Pointer(&b))
}
```

`vgaWriteLine` (`src/main.go:33-39`) iterates a string and
writes to the VGA MMIO buffer. No heap. Confirmed
allocation-safe.

### 2.4 New `handleDivisionError`

Same pattern, shorter:

```go
// src/main.go

//go:nosplit
func handleDivisionError(vector uint64) {
    off := 0
    off = appendStr(panicHexBuf[:], off, "DE vector=0 — division error\r\n")
    serialPanicPrint(panicHexBuf[:off])
    vgaWriteLine(7, bytesToString(panicHexBuf[:off]))
    for { hlt() }
}
```

### 2.5 Verification

- `objdump -d tmp/kernel.bin | sed -n '/handlePageFault>/,/ret\|hlt/p'`
  shows no `call runtime.alloc` / `call runtime.concatstrings`.
- Dev-only trigger: add a `*(*byte)(unsafe.Pointer(uintptr(0x1))) = 42`
  after boot; the kernel should fault, print `PF addr=0x…`, and
  halt. Manually verified; removed before commit.
- Dev-only #DE trigger: `var x int; x = 1/x` forces a division
  error (TinyGo may optimize this away; use inline asm
  `"divq %rcx"` with rcx=0 if necessary).

### 2.6 Dependencies

None. Can land independently at any time.

## 3. B7 — `createTask` → `go` in `main.go`

### 3.1 Today

`src/main.go:333-338`:

```go
serialChannel = chanCreate(16)
createTask(serialTaskEntryAddr()) // Task 1 — serial output

fsRequestChannel = chanCreate(8)
createTask(fsTaskEntryAddr()) // Task 2 — filesystem
```

### 3.2 After B3, B4, B5

```go
// serial — deleted entirely by B3. No replacement line needed.

// fs — migrated by B4 to native chan.
go fsTask()

// keyboard — added by B5.
go keyboardPump()
```

### 3.3 Also delete

- `initScheduler()` call at `src/main.go:329`. After B8, the
  function itself is gone; leaving the call in place fails the
  build. The call must be removed in the same commit that does
  B7 if B8 also lands at the same time; or the call must remain
  until B8 lands and only be removed then.
  - Recommended: delete `initScheduler()` in B8 (together with
    the rest of `src/scheduler.go`) so B7's diff stays minimal.

### 3.4 Verification

Sendkey 10/10; stress. SMP sanity deferred to B11 (§6.4).

### 3.5 Dependencies

- After B3, B4, B5 (each migration's body must exist in
  goroutine form).

## 4. B8 — Delete `src/scheduler.go` + dead stubs

### 4.1 Scope

- Delete `src/scheduler.go` entirely.
- Delete `src/switch.S:53-143` — every `*TaskAddr` entry-point
  stub for goroutines that don't exist (`demoTaskA/B/C`,
  `chanProducerTask`, `chanConsumerTask`, `chanRendezvousA/B`,
  `selectTestTask`, `selectProducerA/B`, `userPrintTask`,
  `keyboardConsumerTask`, `serialTaskEntry`, `fsTaskEntry`,
  `fsDemoTaskAddr`). Also delete `fsDemoTask` in `src/fs.go:267`
  and any other demo-task Go bodies whose addr-stubs are
  removed here.
- Keep `taskReturnHaltAddr` in `src/switch.S` (still a useful
  safety-net return address for initial task stacks — though
  after B8 nothing allocates task stacks, so this could also be
  deleted; B10 makes the call).
- **B9 deletes `elfExecTrampolineAddr` and the Go
  `elfExecTrampoline` body** — see
  `phase_b_ring3_and_exec.md §8`. The deletion is part of B9's
  commit, not B8's, so the B8 diff does not need to touch the
  trampoline symbols.

### 4.2 Dead `//go:linkname` declarations to remove

Audit each source file after B3, B4, B5, B7 land:

- `src/serial.go:76-79` — `serialTaskEntryAddr` linkname decl
  (removed by B3).
- `src/fs.go:149-152` — `fsTaskEntryAddr` linkname decl (removed
  by B4 or B7).
- `src/keyboard.go:92-96` — `keyboardConsumerTaskAddr` (removed
  by B5).
- `src/channel.go:332-336, 344-348, 384-388, 398-402, 420-424,
  434-438, 524-528` — all `chanProducerTaskAddr`,
  `chanConsumerTaskAddr`, `chanRendezvousAAddr`,
  `chanRendezvousBAddr`, `selectTestTaskAddr`,
  `selectProducerAAddr`, `selectProducerBAddr`,
  `userPrintTaskAddr`. These go with `src/channel.go` in B10.
- `src/scheduler.go` — every `demoTask*Addr` and
  `taskReturnHaltAddr` decl. These go with the file in B8.

### 4.3 Deleted functions

From `src/scheduler.go`:
- `Task` struct
- `tasks [maxTasks]Task` + related globals
- `initScheduler`, `createTask`, `taskReclaim`
- `schedule`, `yield`, `taskSleep`
- `WaitQueue` struct and the five wait-queue functions
- `sleepQueue` + `sleepQueueWakeExpired`
- Demo tasks A/B/C

All callers of the above are gone by the time B8 lands
(B3 removes `chanRecv`/`chanSend` on `serialChannel`; B4 on
`fsRequestChannel`; B5 on `userKeyboardChannel`; B7 on
`createTask`; B9 on `schedule()`/`taskBlocked` and
`elfExecTrampoline`).

### 4.4 `handleTimer` change — cooperative model

Today `handleTimer` calls `schedule()` directly, forcing a
context switch on every PIT tick. After B8 there is no
`schedule()` to call. Kernel goroutines scheduled by TinyGo's
runtime **only yield cooperatively**.

```go
// src/pit.go (after B8)

//go:nosplit
func handleTimer(vector uint64) {
    pitTicks++
    picSendEOI(0)
    // No call into TinyGo's runtime scheduler. Doing so from ISR
    // context would trip interrupt.In() checks inside the
    // runtime's park/unpark paths (see
    // impldoc/goroutine_design_channels_and_isr.md §3.5).
}
```

**Consequences** — all acceptable for v1, but must be called out
so the implementer does not add a regression:

1. **Kernel goroutines must yield voluntarily.** Every long-
   running kernel goroutine must include at least one channel
   op, `time.Sleep`, or `runtime.Gosched()` per outer loop. The
   two kernel goroutines shipped in Phase B comply:
   - `fsTask`: blocks in `for req := range fsReqCh` — channel
     recv is a yield point.
   - `keyboardPump`: `runtime.Gosched()` in the empty-ring
     branch — direct yield. Sufficient, because the runqueue
     rotates at every Gosched.
2. **Ring-3 user programs run uninterrupted until the next
   syscall.** The 12-syscall ABI guarantees every useful user
   program syscalls within ≤100 ms worth of work (shell reads,
   FS ops, writes). A buggy user program in an infinite
   computation without syscalls will freeze the shell. v1
   accepts this; the shipped programs (`ls`, `cat`, `hello`,
   `wc`) all syscall quickly.
3. **Idle BSP.** When every kernel goroutine is parked and no
   Ring-3 goroutine is runnable, TinyGo's scheduler enters its
   own idle path — ultimately calling `sleepTicks` from
   `runtime_gooos.go`, which `sti; hlt`s until the next IRQ.
   Verified via the existing Phase-A boot (shell prompt is
   idle; sendkey keystrokes wake it through the keyboard IRQ →
   ring buffer → pump → `sysReadHandler` chain).

**Future v2 upgrade**: `wantReschedule` flag set in
`handleTimer`, consumed by `isr_common` epilogue — see
`impldoc/goroutine_design_scheduler.md §5.3`. Adds a
forced-yield path for cases (1) and (2). Deferred.

### 4.5 Verification

- Build succeeds after deletion.
- Sendkey 10/10.
- Stress.

### 4.6 Dependencies

- After B3, B4, B5, B7, B9.

## 5. B10 — Delete `src/channel.go`, strip `src/switch.S`

### 5.1 Scope

- Delete `src/channel.go` entirely.
- Finish pruning `src/switch.S`: delete `taskReturnHaltAddr`
  (no longer used) and `elfExecTrampolineAddr` (B9's new
  `ring3Wrapper` replaces the trampoline).
  - **OR**: keep `src/switch.S` with only `taskReturnHaltAddr`
    if the implementer discovers a reason to retain it.
    Recommended: delete if unused, to leave no dead symbols.
  - After full strip, `src/switch.S` may be deleted; the
    Makefile's `SWITCH_S` / `SWITCH_O` rules go with it.

### 5.2 Deleted types / functions

- `Channel` struct
- `chanPool`, `chanIDTable`, related globals
- `chanCreate`, `chanFree`, `chanRegister`, `chanLookup`
- `chanSend`, `chanRecv`, `chanTrySend`
- `SelectCase`, `selectWait`, `chanRecvReady`, `chanSendReady`,
  `chanRecvDirect`, `chanSendDirect`
- All test task bodies (`chanProducerTask`, `chanConsumerTask`,
  `chanRendezvousA`, `chanRendezvousB`, `selectTestTask`,
  `selectProducerA`, `selectProducerB`, `userPrintTask`) and
  their `*Addr` decls.

### 5.3 Verification

- Build succeeds.
- Sendkey 10/10; stress; SMP.
- `grep -rn "chanCreate\|chanSend\|chanRecv\|chanTrySend\|selectWait\|WaitQueue" src/`
  returns zero hits.

### 5.4 Dependencies

- After B3, B4, B5, B8, B9. Every custom-channel or custom-
  scheduler caller is gone.

## 6. B11 — SMP AP idle loop

### 6.1 Today

`src/smp.go:191-213` — `apEntry(apIndex uint64)`:

```go
func apEntry(apIndex uint64) {
    // (print "AP N online")
    // Halt forever (interrupts disabled on AP).
    for {
        hlt()
    }
}
```

Interrupts are not enabled on APs. They cannot receive IPIs,
which is mostly OK for v1 (we don't send any) but breaks future
features (wake-on-IPI for task migration, cross-core TLB
shootdown).

### 6.2 After B11

```go
func apEntry(apIndex uint64) {
    // (print "AP N online") — unchanged

    // Idle: enable interrupts and halt until an IPI arrives.
    // v1 does not send any IPIs to APs, so the AP sleeps
    // indefinitely. The sti+hlt idiom is kept so a future SMP
    // v2 can wake APs without touching this file.
    sti()
    for {
        hlt()
    }
}
```

### 6.3 Non-interference with BSP runqueue

APs never call into the TinyGo scheduler, never call `go`, never
touch the runqueue. The one asynchronous path that could wake
them — a spurious IRQ delivered by the LAPIC — resolves to the
default handler (`handleDefaultIRQ` at `src/main.go:95-98`),
which just sends EOI. No runqueue interaction.

### 6.4 Verification

- `make run-smp` reaches shell with 4 cores online.
- Serial shows "AP 1 online", "AP 2 online", "AP 3 online" as
  today.
- Sendkey 10/10 under `-smp 4`.

### 6.5 Dependencies

- None in principle. B11 is a one-line Go change plus a comment.
  Can land any time. Convention: land with B9 so all scheduler-
  ownership changes group together.

## 7. Open questions

- **Should `src/switch.S` file be deleted or shrunk?** If after
  B10 it contains zero symbols, delete the file and its Makefile
  rules. If one symbol survives (`taskReturnHaltAddr` with a
  new use case), keep the file. Decide at B10 time based on
  actual state.
- **B8's `handleTimer` loses PIT-driven Ring-3 preemption.** A
  future decision point: do we need explicit preemption of
  Ring-3 user programs? v1 accepts the cooperative model
  because all shipped user programs are short-lived. Revisit if
  a user program hangs the shell.

## 8. Reviewer notes (to be populated after review pass)

(none yet)
