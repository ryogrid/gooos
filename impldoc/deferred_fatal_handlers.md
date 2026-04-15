# Deferred — Fatal-handler detail preservation (item 8)

Covers inventory item 8 from `deferred_overview.md §1`. Restores
the CR2/RIP/error-code detail that the pre-Phase-B
`handlePageFault` / `handleDivisionError` emitted, without
reintroducing heap allocation into the ISR path.

## 1. Problem statement

`src/vm.go:280` (current `handlePageFault`) emits:

```text
PF: addr=0x4020FF80 err=0x07 rip=0x4010C430
```

Those three hex values come from `readCR2()`, `lastErrorCode`, and
the saved RIP on the ISR frame. The message is built via Go
string concatenation:

```go
msg := "PF: addr=0x" + hextoa(uint64(faultAddr)) + " err=0x" +
       hextoa(errCode) + " rip=0x" + hextoa(uint64(faultRIP))
```

Each `+` between two `string` operands allocates on the kernel
heap, and `hextoa` (`src/vm.go:291`) returns a freshly-allocated
`string([]byte)`. Under `scheduler=tasks` heap allocation from an
ISR can race with the conservative collector's mark phase and
corrupt runtime state. The same heap-alloc path lives in
`handleDivisionError` (`src/main.go`).

`phase_b_teardown.md §2.3` proposed a no-alloc
`panicHexBuf [64]byte` + `appendHex` / `appendStr` approach.
`TODO_B.md` records B6 as deferred post-Phase-B with the note
"basic system works; can land independently". `phase_b_teardown.md
§2.5` calls this specific case "R-fatal-detail-loss" in the risk
register.

Concrete symptom: today's handlers print full detail but at the
cost of an allocator call from ISR context — a known correctness
hazard. The Phase B teardown intentionally left the heap-alloc
path in place because no fault was observed during sendkey runs
and a clean redesign was preferred over a half-measure. This item
delivers that redesign: same output, no allocation.

## 2. Chosen approach: no-alloc static buffer + hex helpers

Match the design in `phase_b_teardown.md §2.2` almost exactly,
with one revision: extend the static buffer to 96 bytes so the
DE handler's longer message fits too, and guarantee the buffer
is ISR-dedicated (no concurrent user).

`src/serial.go:54 serialPrint` is already allocation-free (it
iterates `s` by index and calls `serialPutChar`), but it accepts
a `string`. Add one new sibling that takes a byte slice directly,
to avoid a `string([]byte)` conversion at the call site:

```go
// src/serial.go -- new helper, allocation-free.
//go:nosplit
func serialPrintBytes(b []byte) {
    for i := 0; i < len(b); i++ {
        serialPutChar(b[i])
    }
}
```

**New file** (recommended): `src/panic.go`. Exactly three
exports:

```go
// src/panic.go -- allocation-free kernel-panic output helpers.
// Used by fatal ISR handlers (handlePageFault, handleDivisionError)
// where heap allocation would race with the conservative GC.
//
// Single-threaded discipline: a fatal handler runs once and then
// halts; there is no concurrent user of panicHexBuf.

package main

var panicHexBuf [96]byte

//go:nosplit
func appendStr(buf []byte, off int, s string) int {
    for i := 0; i < len(s); i++ {
        buf[off] = s[i]
        off++
    }
    return off
}

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
```

`serialPrintBytes` (added above to `src/serial.go`) is the
no-alloc sink the panic helpers will write into.

## 3. Rewritten `handlePageFault` (`src/vm.go`)

```go
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

    serialPrintBytes(panicHexBuf[:off])
    vgaWriteLine(12, bytesToString(panicHexBuf[:off]))

    for { hlt() }
}
```

Two new Go-level requirements:

1. `bytesToString([]byte) string` — unsafe slice-header reinterpret
   that avoids the implicit allocation `string([]byte)` would
   perform. Placed in `src/panic.go`:

   ```go
   //go:nosplit
   func bytesToString(b []byte) string {
       return *(*string)(unsafe.Pointer(&b))
   }
   ```

   Lifetime: same as `b`. Safe here because `panicHexBuf` outlives
   the handler (it's a `.bss` global; the kernel halts after the
   handler returns).

2. Verify `vgaWriteLine` (`src/main.go:33-39`) does not allocate.
   The current body is a direct `[vgaCells]uint16` MMIO write via
   `unsafe.Pointer(vgaAddr)`; no `string` → `[]byte` conversion,
   no concat. Add a `//go:nosplit` annotation as a belt-and-braces
   guarantee that the compiler does not insert a stack-growth
   check (which could call `runtime_alloc`).

## 4. Rewritten `handleDivisionError` (`src/main.go`)

```go
//go:nosplit
func handleDivisionError(vector uint64) {
    off := 0
    off = appendStr(panicHexBuf[:], off, "#DE: division error\r\n")
    serialPrintBytes(panicHexBuf[:off])
    vgaWriteLine(7, bytesToString(panicHexBuf[:off]))
    for { hlt() }
}
```

No register detail is meaningful for #DE beyond "it fired"; the
message is intentionally short.

## 5. Dev-only trigger tests

One-shot validation that must run before committing:

- **#PF trigger** (remove before committing):
  ```go
  *(*byte)(unsafe.Pointer(uintptr(1))) = 42
  ```
  Placed at the end of `main()` just before the shell launch. The
  kernel should print `PF addr=0x1 err=0x02 rip=0x…` on serial
  and halt. Verify address and errcode match expectations.

- **#DE trigger** (remove before committing):
  ```go
  var a, b int = 1, 0
  _ = a / b
  ```
  TinyGo may optimize this to a constant panic rather than a
  hardware fault; if so, use inline asm:
  ```go
  //go:noinline
  func triggerDE() {
      asm("xorq %rax, %rax\n\t divq %rax")
  }
  ```
  (The asm form requires a helper in `src/stubs.S`; only worth the
  cost if the pure-Go trigger cannot be made to fire.)

## 6. Files to modify

| File | Change |
|---|---|
| `src/panic.go` | **new** (`package main`, like every other file under `src/`) — `panicHexBuf`, `appendHex`, `appendStr`, `bytesToString` |
| `src/vm.go` | rewrite `handlePageFault` to use the no-alloc path; `//go:nosplit` |
| `src/main.go` | rewrite `handleDivisionError` similarly; `//go:nosplit` |
| `src/main.go` | add `//go:nosplit` to `vgaWriteLine` (paranoia) |
| `src/serial.go` | add `serialPrintBytes([]byte)` allocation-free helper |

## 7. Dependencies

None. Item 8 is the most-independent of all deferred items. Can
land immediately.

## 8. Verification

1. `make build` clean.
2. `nm tmp/kernel.bin | grep handlePageFault` — confirm `__tinygo_alloc`
   is not reachable from the handler's static call graph:
   `objdump -d tmp/kernel.bin | awk '/<handlePageFault>/,/^$/{…}'`
   followed by a grep for `call.*alloc`. Zero hits required.
3. **Dev trigger (#PF)**: insert the `*(*byte)(…1) = 42` line in
   a scratch branch, `make iso && make run`, confirm serial log
   shows `PF addr=0x1 err=0x02 rip=0x…`. Remove the trigger.
4. **Sendkey regression**: 10/10 trials of
   `tmp/test_sendkey.sh` pass unchanged — the happy path does not
   fire fatal handlers, but we still run the suite to catch any
   build breakage.
5. Re-verify with `stress_test.sh`.

## 9. Open questions

1. **Should `panicHexBuf` be declared in assembly (`src/isr.S`) to
   guarantee no Go-side dead-code elimination?** The Phase B
   pattern for `gooos_in_interrupt_depth` used this trick because
   the Go side only *wrote* to the counter. Here the Go side both
   writes and reads `panicHexBuf`, so TinyGo should keep it
   alive. Left as Go-defined with a `//go:linkname`-free dummy
   reference inserted if DCE becomes a problem.
2. **Should a panic capture the last 16 bytes of the user stack?**
   Useful for post-mortem but requires reading `frame.RSP` and
   doing bounded copies — a minor extension worth keeping out of
   the v1 scope for this item.

## 10. Risk register delta

- **Retires**: `R-fatal-detail-loss`.
- **Adds**: none. The static buffer approach is well-trodden
  (used by the Linux kernel's early console, for example).
