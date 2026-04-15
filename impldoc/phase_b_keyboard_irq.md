# Phase B — Keyboard IRQ Ring Buffer + Pump (B5)

This document specifies the exact replacement for `handleKeyboard`'s
current `chanTrySend` into `userKeyboardChannel`. It is the only
Phase-B item with a genuine ISR-safety hazard, per the Phase-A
design (`impldoc/goroutine_design_channels_and_isr.md §3.3`). The
design here is implementation-ready: every symbol is named, every
invariant is spelled out.

## 1. Problem recap

Today (`src/keyboard.go:71-90`):

```go
func handleKeyboard(vector uint64) {
    scancode := inb(kbdDataPort)
    picSendEOI(1)
    if scancode&0x80 != 0 { return }
    var ascii byte
    if scancode < 128 { ascii = scancodeToASCII[scancode] }
    event := uintptr(scancode) | (uintptr(ascii) << 8)
    chanTrySend(keyboardChannel, event)
    chanTrySend(userKeyboardChannel, event)
}
```

`chanTrySend` (`src/channel.go:187-211`) is a custom, lock-free
function that directly manipulates the channel's ring buffer
indices. It never calls into the TinyGo runtime, so it does not
trip `interrupt.In()` and is safe today.

Naive migration to a native `chan uintptr`:

```go
select {
case keyboardCh <- event:
default:
}
```

is **unsafe**. `select` with a `default` case routes through
TinyGo's `chanSelect` (`/home/ryo/.local/tinygo/src/runtime/chan.go`),
which takes `interrupt.Disable()` and may touch the channel's
`blocked` linked list. Both operations interact with the runqueue
scanning in `gc_blocks.go:444-451`, which also holds
`interrupt.Disable()` — on single-core they can't deadlock, but
the `ch.blocked` mutation under ISR is a precise GC
safepoint hazard that we cannot prove safe without a race analysis
we are unwilling to sign off on.

Conclusion: the ISR must not touch any Go runtime channel
machinery. A hand-rolled single-producer / single-consumer ring
buffer staged by a dedicated goroutine is the clean alternative.

## 2. Design overview

```
+----------------------+     +-------------------+     +--------------------+
|  handleKeyboard ISR  | --> | keyboardIRQSend   | --> | gooos_kbd_ring     |
|  (src/keyboard.go)   |     | (no Go runtime)   |     | (.bss, 64 slots)   |
+----------------------+     +-------------------+     +--------+-----------+
                                                                |
                                                                v
                                                       +--------------------+
                                                       |  keyboardPump      |
                                                       |  goroutine         |
                                                       |  (src/keyboard.go) |
                                                       +--------+-----------+
                                                                |
                                                                | keyboardCh <- ev
                                                                v
                                                       +--------------------+
                                                       |  sysReadHandler    |
                                                       |  (src/userspace.go)|
                                                       +--------------------+
```

- ISR writes scancode+ASCII events into a fixed-size ring in
  `.bss`.
- A dedicated `keyboardPump` goroutine polls the ring and forwards
  events into a native Go `chan uintptr`, which is what
  `sysReadHandler` reads from.
- The ISR never touches a Go channel. The pump never runs in ISR
  context. Both invariants are enforced by naming convention
  (`…IRQ` suffix on ISR-safe functions) and `//go:nosplit`.

## 3. Ring buffer design

### 3.1 Layout

```go
// src/keyboard_irq.go

const kbdRingSize = 64  // power of two → index &= mask

var (
    gooosKbdRing     [kbdRingSize]uint32 // slot = (scancode << 8) | ascii
    gooosKbdHead     uint32              // writer (ISR) index, monotonically ++
    gooosKbdTail     uint32              // reader (pump) index, monotonically ++
)
```

- `kbdRingSize = 64` is ample: at 100 Hz PIT and typical keyboard
  rates (~10 keystrokes/sec worst case), the buffer drains much
  faster than it fills.
- Power-of-two size lets us use `idx & (kbdRingSize-1)` instead of
  modulo.
- Slot type is `uint32` so the whole slot fits in a single
  naturally-aligned store on x86-64 — no torn reads.
- `gooosKbdHead` and `gooosKbdTail` are monotonically incremented
  `uint32`s. They wrap around 2³²; as long as `head - tail < size`
  (fits in uint32), correctness holds.

The symbol names are prefixed `gooos` to make their role as
cross-ISR-runtime bridges obvious. They must live in `.bss`; the
implementer may define them via a small Go file with
`//go:linkname` to an asm-declared `.bss` block, following the
pattern established by `gooos_in_interrupt_depth` in
`src/isr.S:159-164`.

### 3.2 `keyboardIRQSend` (ISR-side writer)

```go
//go:nosplit
func keyboardIRQSend(event uint32) {
    h := gooosKbdHead
    if h-gooosKbdTail >= kbdRingSize {
        return // buffer full, drop
    }
    gooosKbdRing[h&(kbdRingSize-1)] = event
    gooosKbdHead = h + 1
}
```

- `//go:nosplit` — must not trigger a stack-growth check. (gooos
  does not grow stacks, but the pragma also tells TinyGo the
  function is safe to call from the no-runtime ISR context.)
- No allocation, no `//go:linkname` to runtime functions, no
  channel ops.
- Drop-on-full is acceptable: the same guarantee the current
  `chanTrySend` offers.

### 3.3 `keyboardIRQRecv` (pump-side reader)

```go
//go:nosplit
func keyboardIRQRecv() (uint32, bool) {
    t := gooosKbdTail
    if t == gooosKbdHead {
        return 0, false
    }
    event := gooosKbdRing[t&(kbdRingSize-1)]
    gooosKbdTail = t + 1
    return event, true
}
```

Runs in the pump goroutine's context, not ISR. Still marked
`//go:nosplit` because it executes a tight polling loop and the
inline avoidance of stack checks helps latency.

### 3.4 Memory ordering (the hard part)

On x86-TSO, four pairwise orderings matter. All four are
guaranteed by the instruction-set rules; no fences or
`atomic.*` primitives are needed for v1 (single-CPU BSP).

**Writer (ISR) store ordering**:
`gooosKbdRing[idx] = event` **before** `gooosKbdHead = h + 1`.
x86 preserves store-store order. A reader that observes the new
`head` value is guaranteed to observe the fresh slot contents.

**Reader (pump) load ordering**:
Load `gooosKbdHead` **before** load `gooosKbdRing[t & mask]`.
x86 preserves load-load order. If the pump observes
`head > tail`, the subsequent slot read returns the committed
event. This is the pair the reviewer specifically called out to
state explicitly.

**Reader's read-before-bump**:
`event = gooosKbdRing[t & mask]` **before** `gooosKbdTail = t + 1`.
x86-TSO orders load-before-store, so even if the ISR fires
between the two (observing the old `tail` and storing a new
event), the pump's read has already captured the correct slot.

**Writer's tail read**:
The writer reads `gooosKbdTail` to compute the full check
`h - tail < size`. If the pump has just bumped tail, the writer
may observe either value; both are safe. If the pump is
mid-read (between slot-read and tail-bump), the writer sees the
old tail and treats the slot as still occupied — acceptable
under drop-on-full semantics.

**Invariant**: single-CPU x86-64 with ISR-writer and
goroutine-reader never racing on any one variable more than is
strictly necessary; TSO provides all four required orderings
via ordinary `mov` instructions.

Under SMP v2, the pump goroutine could migrate to an AP while
the ISR still runs on BSP. Cross-CPU memory ordering on x86 is
also TSO, so the same guarantees hold in principle — but Go's
optimizer may reorder loads/stores in ways TSO does not cover.
Move to `atomic.StoreUint32` / `atomic.LoadUint32` under v2.
Flag as `R-b5-smp-atomics` and defer.

### 3.5 No torn reads

`uint32` slots are 4-byte aligned (Go guarantees 4-byte alignment
for `[N]uint32` globals). All x86-64 aligned 32-bit accesses are
atomic. No `uint64` or bigger → no torn reads possible.

### 3.6 Interrupts during `keyboardIRQRecv`

If an ISR fires while the pump is mid-`keyboardIRQRecv`:

- The ISR runs `keyboardIRQSend`, which bumps head. On x86 the
  store-ordering rules above ensure the pump still sees a
  consistent slot.
- `keyboardIRQRecv` only mutates `gooosKbdTail`. The ISR does
  not read tail (only writer reads head as `h-tail`). So the ISR
  mid-pump-read scenario cannot corrupt the pump's in-progress
  read.

The one race-looking interaction: the ISR reads `gooosKbdTail` to
compute `h-tail` for the full-check. If `gooosKbdTail` is updated
right after the ISR's load, the ISR may spuriously believe the
buffer is fuller than it is and drop an event that could have
been enqueued. This is benign — drop-on-full is explicitly
acceptable.

### 3.7 Implementation note — consider asm if TinyGo inlines Go away

If TinyGo's compiler inlines `keyboardIRQSend` into `handleKeyboard`
and the inlined version fails to preserve store ordering (e.g.,
reorders the slot write past the head bump due to aggressive
register scheduling), the design falls back to a hand-written asm
stub that does the two stores in strict order. The implementer
should `objdump -d tmp/kernel.bin | grep -A20 handleKeyboard` after
the first build and verify the emitted code matches the intent.

## 4. `handleKeyboard` new body

```go
// src/keyboard.go

func handleKeyboard(vector uint64) {
    scancode := inb(kbdDataPort)
    picSendEOI(1)
    if scancode&0x80 != 0 { return }
    var ascii byte
    if scancode < 128 { ascii = scancodeToASCII[scancode] }
    event := uint32(scancode) | (uint32(ascii) << 8)
    keyboardIRQSend(event)
}
```

- The dead `chanTrySend(keyboardChannel, event)` call at
  `src/keyboard.go:88` is removed.
- The `keyboardChannel` variable at `src/keyboard.go:64` is
  deleted; its consumer `keyboardConsumerTask` was never spawned
  (dead code).

## 5. `keyboardPump` goroutine

```go
// src/keyboard.go

var keyboardCh = make(chan uint32, 16)

func keyboardPump() {
    for {
        ev, ok := keyboardIRQRecv()
        if !ok {
            runtime.Gosched()
            continue
        }
        keyboardCh <- ev
    }
}
```

- Runs at Ring 0 as a normal goroutine. Spawned from `main()`
  alongside `go fsTask()` after B7.
- `runtime.Gosched()` on empty is a compromise. A `hlt` would
  consume less CPU but requires the runtime to know to resume on
  IRQ — that wiring is larger than v1 needs. A busy-poll yield
  is acceptable at 100 Hz preemption (each yield gets preempted
  within ~10 ms anyway).
- The outgoing channel `keyboardCh` has a small buffer (16) to
  absorb bursts without forcing the pump to block.

### 5.1 Behavior if `keyboardCh` fills

If the consumer (`sysReadHandler`) is slow, `keyboardCh <- ev`
blocks. The pump goroutine parks. The IRQ ring buffer may
overflow and drop events. This is the same failure mode the
current `chanTrySend(userKeyboardChannel, event)` exhibits at
`src/keyboard.go:89` — acceptable for v1.

## 6. `sysReadHandler` changes

```go
// src/userspace.go (sysReadHandler, around line 141)

// Old:
// event := chanRecv(userKeyboardChannel)

// New:
event := <-keyboardCh
```

The rest of the handler (scancode decoding, backspace handling,
etc.) is unchanged. The `event` variable's type shifts from
`uintptr` to `uint32`; all the bit-extract expressions at
`src/userspace.go:142-143` still compile.

## 7. Deletions

Once B5 lands, the following become dead and should be removed in
the same commit:

- `src/keyboard.go:64` — `keyboardChannel = chanCreate(16)`.
- `src/keyboard.go:65` — `userKeyboardChannel = chanCreate(16)`.
- `src/keyboard.go:71-96` — `handleKeyboard`'s call to
  `chanTrySend(keyboardChannel, event)`; keep the rest.
- `src/keyboard.go:92-107` — `keyboardConsumerTaskAddr`
  `//go:linkname` declaration and the `keyboardConsumerTask`
  function (dead).
- `src/main.go:341` — `chanRegister(userKeyboardChannel)`
  (the channel ID is no longer used by any syscall).

## 8. Verification

1. **Build**: `make build` clean.
2. **Sendkey regression**: 10/10 trials. This harness types
   characters via QEMU monitor `sendkey`, which exercises the
   ISR → ring → pump → `sysReadHandler` → user `sys_read` path.
   If any trial drops a character, the failure is visible as
   `exit` mismatch.
3. **Stress**: `tmp/stress_test.sh`.
4. **Latency check** (one-time dev validation): type 100
   characters via the `sendkey` harness in rapid succession and
   confirm all reach the shell. Latency above 100 ms per
   character (roughly 10 tick quanta) indicates the pump is not
   keeping up; tune `kbdRingSize` or add a channel-buffer size
   bump.
5. **Objdump sanity**: `objdump -d tmp/kernel.bin | sed -n
   '/handleKeyboard>/,/ret/p'` should show the store to
   `gooosKbdRing[idx]` before the store to `gooosKbdHead`. If
   reordered, follow the asm-stub fallback in §3.7.

## 9. Dependencies

- **Predecessors**: none. B5 can land first in Phase B — it is
  the highest-risk item, so front-loading it de-risks the rest.
- **Blocks**: B10 (deleting `src/channel.go`) — until B5 removes
  `chanTrySend(userKeyboardChannel, event)` and `chanCreate(16)`
  calls, `src/channel.go` has live callers.

## 10. Open questions

- **Should `gooosKbdRing` and the indices live in asm or Go?**
  The Phase-A `gooos_in_interrupt_depth` precedent uses asm; the
  analogue here would be an asm `.bss` block with `.skip 256`
  for the ring plus two `uint32` indices. Go-side access via
  `//go:linkname`. A pure-Go definition with `//go:noinline`
  references (the pattern in `src/goroutine_irq.go`) may also
  work; the implementer should try Go-first and fall back to asm
  only if TinyGo DCEs the ring array.
- **Should `keyboardPump` park on an empty ring via `sti; hlt`
  instead of `Gosched`?** Parking is more power-efficient but
  requires the runtime to re-schedule the pump on every IRQ, and
  we don't have an easy hook for "wake pump on keyboard IRQ"
  without roping the runtime scheduler into the ISR. Deferred
  to a future optimization pass.

## 11. Reviewer notes (to be populated after review pass)

(none yet)
