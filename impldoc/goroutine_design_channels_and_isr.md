# Native Channels and ISR-Context Safety

This document specifies how TinyGo's built-in channel / `select` machinery
replaces `src/channel.go`, how the keyboard IRQ's non-blocking send fits
into the new model, and the rules every ISR must follow. It complements
`goroutine_design_scheduler.md`.

## 1. What gets replaced

`src/channel.go` today implements:

- `Channel` struct with a 256-slot ring buffer of `uintptr`
  (`src/channel.go:17-26`).
- Static pool of 32 channels (`chanPool`, `src/channel.go:30-31`).
- `chanCreate`, `chanSend`, `chanRecv`, `chanTrySend`, `chanFree`
  (`src/channel.go:63-211`).
- `SelectCase` / `selectWait` (`src/channel.go:213-322`).
- A vestigial `chanIDTable` + `chanRegister` + `chanLookup` API
  (`src/channel.go:34-61`) that was designed for userspace channel
  syscalls (`sys_send` / `sys_recv` in the original
  `tasks/prd-goroutine-microkernel.md`). The current 12-syscall ABI
  (`src/userspace.go:41-55`) does **not** expose these syscalls, so the
  table has no live reader from Ring 3. `main.go:327` still calls
  `chanRegister(userKeyboardChannel)` but the returned ID goes nowhere.

Test goroutines left over from the original microkernel design
(`chanProducerTask`, `chanConsumerTask`, `chanRendezvousA/B`,
`selectTestTask`, `selectProducerA/B`, `userPrintTask`) are all dead
code — `main.go` does not spawn them.

The full replacement: **delete `src/channel.go` entirely.** Existing
callers migrate as follows.

## 2. Migration of channel callers

### 2.1 Serial channel (`src/serial.go`, `src/main.go:319`)

Today: `serialChannel = chanCreate(16)` then `serialTask` receives pointers
to string literals and writes bytes to COM1.

After: declare a package-level variable

```go
var serialCh = make(chan string, 16)
```

`serialPrintln` and friends (`src/serial.go`) become `serialCh <- s`. The
`serialTask` goroutine receives via `<-serialCh` and writes each string's
bytes to COM1. The runtime handles parking/resuming the sender when the
buffer is full.

### 2.2 Filesystem request/reply (`src/fs.go`)

Today (`src/fs.go:204-261`): callers allocate a `replyCh := chanCreate(1)`,
package an `FSRequest{op, args, replyCh: replyCh}` into a static pool
slot, `chanSend(fsRequestChannel, ptr)`, then `chanRecv(replyCh)` to get
the response pointer.

After:

```go
type fsRequest struct {
    op   fsOp
    // ... args ...
    reply chan *fsResponse
}

var fsReqCh = make(chan *fsRequest, 8)

// Caller:
req := &fsRequest{op: opRead, name: name, reply: make(chan *fsResponse, 1)}
fsReqCh <- req
resp := <-req.reply
```

The static `fsReqPool` (`src/fs.go:199-`) and the manual slot-recycling
logic go away — the Go runtime's heap handles lifetime via the reply
channel's reachability.

### 2.3 Keyboard IRQ (§3.2 below)

Today `handleKeyboard` (`src/keyboard.go:71-90`) calls `chanTrySend`
twice — once to `keyboardChannel` (intended for a kernel consumer),
once to `userKeyboardChannel` (consumed by `sysReadHandler` via
`chanRecv(userKeyboardChannel)`).

After: `handleKeyboard` calls a custom non-blocking send helper against
a *single* Go native channel whose buffer is large enough to absorb
typing bursts. Details in §3.

### 2.4 Userspace channel IDs (removal)

`chanIDTable`, `chanRegister`, `chanLookup`, and the `chanRegister`
call in `main.go:327` are removed. When / if `sys_send` / `sys_recv`
return to the ABI in a future milestone, they can be re-introduced as
a thin `map[uint64]chan any` wrapper; the current design does not
implement them because no user program uses them.

## 3. ISR-context safety

### 3.1 The safety rule

> Interrupt handlers (the functions registered via `registerHandler` in
> `src/interrupt.go` and dispatched from `isr_common` in `src/isr.S`) run
> with interrupts disabled on the interrupted goroutine's stack. They
> **must not** call any function that allocates, parks the current
> goroutine, blocks on a channel send/recv, or spawns a new goroutine.

This rule must hold for every handler registered anywhere in the kernel.
A violation can corrupt the runtime's scheduler or the heap depending on
when the ISR fires relative to a runtime state transition.

### 3.2 Catalog of ISR-reachable calls and their safety status

| ISR / handler                 | Functions called                                             | Safety under `scheduler=tasks`                                    |
|------------------------------|--------------------------------------------------------------|-------------------------------------------------------------------|
| `handleTimer` (`src/pit.go`)  | `pitTicks++`, sleep-queue wake, PIC EOI, `runtime_tick()`    | Safe: pure counter increments + runtime-exposed tick hook          |
| `handleKeyboard` (`keyboard.go:71`) | `inb`, scancode table, `picSendEOI`, non-blocking send   | Safe iff the send is allocation-free (see §3.3)                    |
| `handlePageFault` (`vm.go:226`) | `readCR2`, `hextoa`, `serialPrintln`, `vgaWriteLine`, `hlt` | **Unsafe today** — `hextoa`/`serialPrintln` allocate strings      |
| `handleDivisionError` (`main.go:87`) | `serialPrintln`, `vgaWriteLine`                          | Unsafe — heap allocation via string concat                        |
| `handleDefaultIRQ` (`main.go:95`) | `picSendEOI`                                             | Safe                                                               |

The page-fault and division-error handlers allocate via string
concatenation. Today under `scheduler=none` / `gc=conservative` this works
because GC never runs during an ISR (conservative GC is only triggered
synchronously from `runtime_alloc`). Under `scheduler=tasks`, however,
if a goroutine was mid-allocation when a fault fires, an allocation
inside the handler can re-enter the allocator and corrupt its state.

**Design rule**: fatal handlers (`handlePageFault`,
`handleDivisionError`) must be rewritten to emit fixed-content messages
via a raw COM1 write path that bypasses `serialPrintln`'s channel send
and any string concatenation. A helper `serialPanicPrint(s string, n int)`
that writes bytes directly to port `0x3F8` without channel or heap
involvement is added to `src/serial.go`. Fatal handlers use only that.

### 3.3 Keyboard ISR — the hot path

`handleKeyboard` is the only ISR that participates in normal program
flow (it delivers events to the user task's `sys_read`). Under the new
model the handler must:

1. Read the scancode from port `0x60` (unchanged).
2. Send EOI (unchanged).
3. Translate scancode to ASCII via the existing static table
   (`keyboard.go:38-54`).
4. Enqueue the `(scancode, ascii)` pair into a buffered channel
   **without** parking if the buffer is full — drop on full is
   acceptable for v1.

Go's built-in non-blocking send uses `select`:

```go
select {
case keyboardCh <- event:
default:
    // buffer full, drop
}
```

However, `select` is **not** ISR-safe in general — the runtime's
`chanSelect` implementation locks per-channel state and can call into
the scheduler. In bare-metal single-core we sidestep the lock because
interrupts are disabled during ISR execution, but the runtime still
expects its internal bookkeeping to be consistent. The safe primitive
is a custom wrapper:

```go
//go:nosplit
func keyboardIRQSend(event uintptr) {
    // A hand-rolled circular buffer in .bss, sized 64, with atomic
    // head/tail indices updated only from ISR (writer) and the
    // kernel-side consumer goroutine (reader). Drops events silently
    // when the buffer is full.
}
```

A consumer goroutine drains this ring buffer and forwards events into
the native `keyboardCh chan uintptr`:

```go
func keyboardPump() {
    for {
        ev, ok := keyboardIRQRecv()  // non-blocking
        if ok {
            keyboardCh <- ev
        } else {
            runtime.Gosched()
        }
    }
}
```

`keyboardPump` runs as a goroutine. `keyboardCh` is the channel visible
to `sys_read` via `<-keyboardCh` inside `sysReadHandler`.

**Invariant**: The ISR only touches the ring buffer and atomic indices;
the consumer goroutine is the sole bridge into Go channel machinery.

The extra hop through the ring buffer adds latency (~one scheduling
quantum) but eliminates any ISR-vs-runtime race.

### 3.4 Enforcement

To prevent future regressions, mark ISR-safe helpers with a
`//go:nosplit` pragma and a naming convention suffix `…IRQ` (e.g.,
`keyboardIRQSend`). A new comment block at the top of
`src/interrupt.go` documents the rule for future reviewers.

There is no compile-time check that enforces "no heap alloc in ISR
context"; this is a review-time responsibility. The design documents
this as a known soft-enforcement gap in
`goroutine_design_gc_and_smp.md §8`.

### 3.5 `interrupt.In()` — required new primitive

TinyGo's runtime queries `interrupt.In()` at critical points
(`/usr/local/lib/tinygo/src/internal/task/task_stack.go:51-52` in
`task.Pause()`; `runtime/gc_blocks.go` uses
`interrupt.Disable()/Restore()` pair around the runqueue scan). There
is **no amd64-baremetal implementation** of this API in upstream
TinyGo. v1 provides one:

1. `src/isr.S` common prologue increments a `.bss` counter
   `in_interrupt_depth` before calling `go_interrupt_handler`; the
   epilogue decrements it.
2. A new Go file supplies
   ```go
   //go:linkname interruptIn internal/task.interruptIn
   //go:nosplit
   func interruptIn() bool { return in_interrupt_depth != 0 }
   ```
   (exact target name confirmed against `task_stack.go` during the
   implementation spike).
3. `interrupt.Disable()` and `interrupt.Restore()` are thin wrappers
   around the existing `readFlags`/`cli`/`restoreFlags` (`src/stubs.S`)
   with matching `//go:linkname` bridges.

Without this, any `task.Pause` reached from the wrong context panics
("blocked inside interrupt"). Failing loudly is better than silently
breaking; the counter + panic pattern is exactly what prevents
accidental ISR→`Pause` regressions.

### 3.6 Why the keyboard ring-buffer, not just `chanTrySend`-equivalent

The current `handleKeyboard` already uses a non-blocking send
(`src/channel.go:187-211 chanTrySend`). Converting naively to
`select { case keyboardCh <- e: default: }` seems equivalent, but TinyGo's
`chanSelect` takes `interrupt.Disable()` and may touch the `ch.blocked`
list — both of which invoke runtime bookkeeping that can conflict with
`gc_blocks.go:444-451`'s runqueue scan (which also takes
`interrupt.Disable()`). The bounded ring-buffer staged by a consumer
goroutine eliminates this concern at the cost of one scheduler quantum
(≤10 ms) of added keystroke latency. Latency cost is called out as an
open risk in `goroutine_design_gc_and_smp.md §8` (R-keyboard-latency).

## 4. `select` under the new scheduler

`select` works under `scheduler=tasks`
(`/usr/local/lib/tinygo/src/runtime/chan.go:554+`). No gooos-side changes
are needed beyond replacing the custom `selectWait` API callers. The
existing `selectWait` in `src/channel.go:283-322` has no live callers
(only test goroutines that are not spawned at boot), so removing it is
safe.

If a future kernel goroutine needs a timeout, the idiom is:

```go
select {
case v := <-ch:
    // ...
case <-time.After(100 * time.Millisecond):
    // timeout
}
```

`time.After` returns a channel that becomes readable after the sleep
duration. It requires `sleepTicks` / `ticksToNanoseconds` (implemented
per `goroutine_design_scheduler.md §5`) **and** a working `time`
package build. Under the `scheduler=tasks` + bare-metal target, the
`time` package is likely pulled in transitively; confirm by grepping
`nm tmp/kernel.bin` for `time.*` symbols after the runtime-collision
spike lands. If `time` does not link, fall back to raw `sleepTicks`
for timeouts and implement `afterTicks(ticks timeUnit) <-chan struct{}`
locally.

## 5. Summary of file changes

- **Removed**: `src/channel.go` (entire file).
- **Modified**: `src/serial.go`, `src/fs.go`, `src/keyboard.go`,
  `src/main.go`, `src/userspace.go` (the `sysReadHandler` channel
  access), plus the fatal-handler rewrites in `src/main.go` and
  `src/vm.go`.
- **Added**: a small IRQ ring-buffer file (e.g., `src/keyboard_irq.go`)
  holding `keyboardIRQSend` / `keyboardIRQRecv` / the ring buffer
  itself, and `serialPanicPrint` in `src/serial.go`.

A complete cross-file catalog is in
`goroutine_design_gc_and_smp.md §6`.
