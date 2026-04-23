# Keyboard IRQ Ring and Wake Path — Delta

**Scope:** extends `current_impl_0421_night/07_filesystem_fd_shell_io.md` **§Keyboard Ownership and stdin Semantics**. Baseline's FS, FD, pipe, shell I/O, and foreground-ownership contracts are unchanged. Only the *internal mechanism* of how a blocking stdin reader reaches a keyboard event has been refactored.

## Summary of Changes Since `a384b1a`

1. New file `src/keyboard_irq.go` provides a lock-free SPSC ring (`gooosKbdRing[64]`, power-of-2 mask) with ISR producer (`keyboardIRQSend`) and blocking consumer (`keyboardReadEventBlocking`). Commit `dfcd404`.
2. IRQ1 handler (`handleKeyboard`) in `src/keyboard.go:180` now pushes scancode+ASCII+mods events into the ring via `keyboardIRQSend`; no more dedicated `keyboardPump` goroutine or `keyboardCh` channel.
3. BSP-only fallback `pollKeyboardFallback` in `src/keyboard.go:198` polls the PS/2 controller status port (0x64) when no IRQ1 has been observed — lets the system recover from lost IRQ delivery.
4. x86-TSO re-check-before-hlt race fix: `keyboardReadEventBlocking` on BSP does `sti() → compare head/tail → hlt()` atomically w.r.t. the IRQ1 write. Commit `50cc6ce` (after the earlier revert pair `838c044` / `12d1b4d`).
5. `restoreBSPVirtualWire` in `src/smp.go:116` reasserts BSP LVT0/LVT1 and unmasks both PICs, called from `bootActivatePostShellReady` in the non-IOAPIC path. Commit `dfcd404`.

## Current Design

### Ring buffer (`src/keyboard_irq.go`)

```
const kbdRingSize = 64            // power of two; head/tail indices are unsigned and unwrapping
var gooosKbdRing [kbdRingSize]uint32   // event encoding: scancode|ascii<<8|mods<<16|flags<<24
var gooosKbdHead uint32           // writer — monotonic increment; ISR-only
var gooosKbdTail uint32           // reader — monotonic increment; consumer-only
```

Single-producer (ISR), single-consumer (blocking reader). Drop-on-full policy: if the producer sees `head - tail >= kbdRingSize` it silently drops (`src/keyboard_irq.go:35`). Matches the old `chanTrySend` semantics.

Memory ordering: x86-TSO gives load-after-store and store-after-store orderings; no atomic ops needed for this SPSC layout on the BSP-only-consumer v1. Comment references `impldoc/phase_b_keyboard_irq.md §3` for the proof.

### ISR producer (`src/keyboard.go:180`)

`handleKeyboard(vector uint64)` is `//go:nosplit`:

1. `inb(kbdDataPort)` reads the scancode.
2. First-entry latch: `kbdIRQSeen` flips 0→1 and `MARKER: M8 handleKeyboard first entry` is printed (diagnostic trigger for netDiag).
3. `lapicSendEOI()` or `picSendEOI(1)` depending on `ioapicActive`.
4. `processKeyboardScancode(scancode)` — translates, packs `scancode | ascii<<8 | mods<<16 | flags<<24` into one `uint32` event, calls `keyboardIRQSend(event)`.

### Consumer (`src/keyboard_irq.go:84`)

```go
func keyboardReadEventBlocking() uint32 {
    for {
        if ev, ok := keyboardIRQRecv(); ok {     // ring non-empty → take it
            markKeyboardDrainCPU()
            return ev
        }
        if cpuID() == 0 {                        // BSP: poll-fallback → sti/hlt
            if pollKeyboardFallback() { continue }
            if ev, ok := keyboardIRQRecv(); ok {
                markKeyboardDrainCPU()
                return ev
            }
            sti()
            if gooosKbdTail != gooosKbdHead {    // re-check after sti()
                continue
            }
            hlt()
            continue
        }
        gooosSchedulerYield()                    // AP: yield to the scheduler
    }
}
```

The `sti() → re-check → hlt()` sequence closes the race identified by `50cc6ce`:

- Before: reader saw empty ring, called `sti()`, then `hlt()`. If IRQ1 arrived **between** the empty check and the `hlt`, the ISR's ring write is lost-w.r.t.-wake — the CPU halts forever.
- After: after `sti()`, on x86 any pending IRQ1 has **already** woken the CPU and delivered its ring write (x86-TSO: loads after `sti` see all ISR-committed stores). The reader re-compares `gooosKbdHead` against `gooosKbdTail` and only `hlt`s if the ring is still empty. If an IRQ1 arrives exactly now, it simply wakes the `hlt` as usual.

### Fallback polling (`src/keyboard.go:198`)

`pollKeyboardFallback()` is `//go:nosplit`:

1. If `kbdIRQSeen != 0` (real IRQ1 has ever fired), return false — do not poll.
2. Check PS/2 status port (`inb(kbdStatusPort) & 0x01`); return false if no data is waiting.
3. Latch `kbdPollSeen = 1` and emit `MARKER: M8P keyboard poll fallback first entry`.
4. Read `inb(kbdDataPort)` and feed `processKeyboardScancode(...)` — same downstream path as the ISR would take.
5. Return true — the caller loops to try `keyboardIRQRecv` again.

Net effect: on boots where IRQ1 never fires (LAPIC misroute, missing PIC unmask, etc.), BSP polling drains keystrokes into the same ring. Once a real IRQ1 arrives, `kbdIRQSeen` latches and the fallback short-circuits forever.

### BSP virtual-wire restore (`src/smp.go:116`)

Called from `bootActivatePostShellReady()` in the non-IOAPIC path:

```go
lapicWrite(lapicRegLVT0, 0x00000700)  // ExtINT, unmasked
lapicWrite(lapicRegLVT1, 0x00000400)  // NMI, unmasked
outb(pic1Data, 0x00)                   // PIC1: unmask all
outb(pic2Data, 0x00)                   // PIC2: unmask all
```

Covers the empirically-observed case where late SMP boot transitions leave legacy IRQ1 delivery in a half-configured state. BSP-only; APs keep LINT0/LINT1 masked (see `src/smp.go:321`).

### Per-CPU "pump drained" marker (`src/keyboard_irq.go:60`)

`kbdPumpCpuSeen[maxCPUs] uint32` — flag array, not counter (per the project's hang-prevention lesson about u64 increments in ISR-adjacent paths). Flipped once per CPU the first time a blocking reader on that CPU drains an event. Reported by `netDiag` as `pump:NNNN` for continuity with the previous dedicated-pump diagnostic.

## Current Implementation Details

- `src/keyboard_irq.go:18` — `kbdRingSize = 64` (power of two; `h & (kbdRingSize - 1)` replaces modulo).
- `src/keyboard_irq.go:33` — `keyboardIRQSend(event uint32)`, `//go:nosplit`.
- `src/keyboard_irq.go:46` — `keyboardIRQRecv() (uint32, bool)`, `//go:nosplit`.
- `src/keyboard_irq.go:62` — `markKeyboardDrainCPU()`.
- `src/keyboard_irq.go:84` — `keyboardReadEventBlocking()`.
- `src/keyboard.go:98` — `var kbdIRQSeen uint32`.
- `src/keyboard.go:99` — `var kbdPollSeen uint32`.
- `src/keyboard.go:180` — `handleKeyboard(vector uint64)` — IRQ1 ISR.
- `src/keyboard.go:198` — `pollKeyboardFallback()`.
- `src/pit.go:55` — PIT-tick-triggered invocation of `pollKeyboardFallback()` (BSP only).
- `src/smp.go:116` — `restoreBSPVirtualWire()`.
- `src/net.go:193–208` — `netDiag` reporting block: flags `kbdIRQSeen`, `kbdPollSeen`, `kbdPumpCpuSeen[]`.

## Diff-from-Baseline Notes

- Baseline's §Keyboard Ownership description says "actual line input read through keyboard channel and line buffer (`readKeyboardLine`)". The function name `readKeyboardLine` still exists in `src/fd.go` and remains the entry point for `consoleStdin.Read`, but its internals now call `keyboardReadEventBlocking()` from the IRQ ring — no Go channel, no dedicated pump goroutine. Foreground ownership semantics (return `fdErrEOF` if `currentProc() != getForegroundProc()`) are unchanged.
- The previous keyboard architecture had `keyboardPump` draining `gooosKbdRing` into a `keyboardCh` channel, with blocking stdin readers parking on `<-keyboardCh`. That goroutine is gone. Rationale (from commit `dfcd404` + `smp_preempt_problem/README.md §1`): the channel-parking path was vulnerable to cross-CPU timing and could lose events when the scheduler migrated the parking goroutine.
- Baseline §FD/FS Invariants are unchanged.
- A new implicit invariant: **blocking stdin reads can only be served correctly from BSP-hosted goroutines**, because only BSP has `sti/hlt` idle wake + polling fallback. An AP-hosted consumer calls `gooosSchedulerYield()` repeatedly instead, which is correct but burns CPU. The shell and all user processes are normally BSP-originated goroutines at the time they take their first stdin read.

## Open Questions / Known Gaps

- Keyboard reliability is improved but **not fully deterministic**. Observed behavior per `smp_preempt_problem/README.md §Confirmed Current Status`: the first interactive input succeeds more often than before, but not 100%. Remaining failure modes are believed to be scheduler/runtime-layer (post-shell SMP scheduling path) rather than keyboard-ring issues.
- The ring drops on full. At 100 Hz PIT and typical typing rates this never happens, but a bursty paste via QEMU `sendkey` can plausibly fill 64 slots — untested.
- `netDiag` still reports `pump:NNNN` per-CPU counts even though the pump goroutine is gone; the name is preserved for continuity with older test-harness grep patterns.
- `gooosSchedulerYield` on APs is a tight loop around ring-check + yield; a blocking stdin reader migrated to an AP with an empty ring will burn ~100% CPU on that AP until a keystroke arrives. No mitigation today — in practice the reader is always on BSP.
