# Deferred — Hygiene (items 10, 11, 12, 14, 15, 16)

Covers inventory items 10, 11, 12, 14, 15, 16 from
`deferred_overview.md §1`. These are smaller individual items
that share a common theme: making the build process enforce
invariants the kernel relies on, and measuring a few runtime
properties we assume but do not verify.

- §2: item 10 — ISR-safety lint / CI enforcement
- §3: item 14 — `R-runtime-alloc-reentry` enforcement
- §4: item 15 — `R-global-layout` re-verification on every build
- §5: item 12 — `time.After` / full `time` package verification
- §6: item 11 — `R-sleep-granularity` (10 ms PIT floor) mitigation
- §7: item 16 — `R-keyboard-latency` measurement + optimization

## 1. Shared context

Phase A/B settled several invariants by manual review:

- **ISRs may not allocate.** Heap allocation inside an ISR races
  with the conservative collector's mark phase.
- **`go` / `chan` may not be called from ISR context.** Both
  spawn or resolve via `task.Pause`, which is ISR-unsafe.
- **Every TinyGo runtime global that holds GC roots** (e.g.,
  `sleepQueue`, `timerQueue`, `runqueue`) must live inside the
  `_globals_start`..`_globals_end` range that
  `findGlobals` scans.

These are review-time responsibilities today — no automated
check. The hygiene items below add Makefile targets that
enforce them on every build.

## 2. Item 10 — ISR-safety lint

### 2.1 Problem

`goroutine_design_channels_and_isr.md §3.1` stipulates: ISR
handlers must not allocate, must not park, must not spawn
goroutines. Today this is reviewer-enforced; a future
contributor may accidentally drop a `serialPrintln("foo: " +
utoa(x))` into `handleTimer` and break the system
non-deterministically.

### 2.2 Design

A new `make lint` target that runs an AST-walking Go program
over `src/*.go`. The walker:

1. Enumerates every function registered via
   `registerHandler(vector, fn)` — grep plus a simple parse of
   `src/main.go` and `src/interrupt.go`.
2. For each such function, walks the static call graph (up to
   some depth, say 4) and flags any call to:
   - `serialPrintln`, `serialPrint` (they write directly to
     the UART, which is fine — but string concatenation in the
     argument allocates; flag the `+` operator between two
     `string`-typed operands inside arguments).
   - `make(chan ...)`, channel send/recv syntax.
   - `go` statements.
   - Any function that itself triggers `runtime_alloc`
     (conservative approximation: anything that returns a
     slice, map, or interface constructed inline).
3. Reports violations with `file:line` and a one-line
   description.

The walker lives at `scripts/lint_isr.go` (written in regular
Go, built with `go build` at Make time; not `tinygo build`).
It uses the `go/ast` + `go/token` packages.

### 2.3 Integration

`Makefile` additions:

```make
LINT_BIN := tmp/lint_isr

$(LINT_BIN): scripts/lint_isr.go | $(TMP_DIR)
	go build -o $(LINT_BIN) ./scripts/lint_isr.go

.PHONY: lint
lint: $(LINT_BIN)
	$(LINT_BIN) src/

build: lint
```

Wiring `lint` as a `build` prerequisite makes `make build`
fail on violations. For developer inner loops, `make -j
build` remains fast because `lint_isr` is cached unless
`scripts/lint_isr.go` changes.

### 2.4 False positives

Three patterns will legitimately trip a naive walker:

1. `serialPrintln("constant string")` — no concat, no alloc.
   Walker: permit literal-only `string` args.
2. `//go:nosplit` helpers that the ISR calls deliberately
   (e.g., `keyboardIRQSend`) — already known-safe.
   Walker: exempt functions with a `//go:nosplit` pragma at
   their definition.
3. `hlt()` / `cli()` / `sti()` stubs — trivially safe.
   Walker: exempt calls whose callee is implemented in `.S`
   (heuristic: the Go body is `//go:linkname` only, with no
   Go statements).

## 3. Item 14 — Runtime-alloc-reentry enforcement

### 3.1 Problem

Related to item 10: `go func() {}()` implicitly calls
`runtime_alloc` (for the stack). If an ISR-reachable function
contains a `go` statement, the allocator is invoked from ISR
context. The conservative GC can be mid-mark; re-entry
corrupts.

### 3.2 Design

The lint walker (§2) subsumes this item. The `go` and `chan`
checks are the enforcement. No separate infrastructure.

Document in the Makefile's `lint` target's help message that
runtime-alloc reentry is one of the conditions checked.

## 4. Item 15 — Global-layout re-verification

### 4.1 Problem

`goroutine_design_gc_and_smp.md §1.2` requires TinyGo runtime
globals (`sleepQueue`, `timerQueue`, `runqueue`) to land inside
`_globals_start`..`_globals_end`. Phase A spike 2 verified
this once. A TinyGo upgrade could rearrange the runtime's
section layout and move one of these globals outside the scan
range. The collector would then miss Task pointers held there,
and live goroutines would be wrongly swept.

### 4.2 Design

A new `make verify-globals` target. It runs:

```bash
objdump -t tmp/kernel.bin \
  | awk '/runtime\.(sleepQueue|timerQueue|runqueue)/ {print $1, $NF}'
```

Then checks each symbol's virtual address against
`_globals_start` / `_globals_end` (extracted from the same
`nm` / `objdump` output). If any global is outside, print a
failure and exit non-zero.

Implement as a shell script `scripts/verify_globals.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

KERNEL=${1:-tmp/kernel.bin}

start=$(nm "$KERNEL" | awk '$3 == "_globals_start" { print $1 }')
end=$(nm   "$KERNEL" | awk '$3 == "_globals_end"   { print $1 }')

if [[ -z "$start" || -z "$end" ]]; then
    echo "verify-globals: missing _globals_start/_globals_end in $KERNEL" >&2
    exit 1
fi

start_dec=$((16#$start))
end_dec=$((16#$end))

bad=0
while read -r addr name; do
    [[ -z "$addr" || -z "$name" ]] && continue
    a=$((16#$addr))
    if (( a < start_dec || a >= end_dec )); then
        printf 'verify-globals: %s @ 0x%s outside [_globals_start, _globals_end) [0x%s, 0x%s)\n' \
            "$name" "$addr" "$start" "$end" >&2
        bad=1
    fi
done < <(nm "$KERNEL" | awk '
    $3 ~ /^runtime\.(sleepQueue|timerQueue|runqueue)$/ { print $1, $3 }
')

exit $bad
```

The `awk` regex enumerates the three TinyGo runtime globals that
hold GC roots today; extend it as new roots are introduced (any
field containing `*task.Task` references). The script reads `nm`
output (preferred over `objdump -t` because `nm`'s columns are
stable across binutils versions).

### 4.3 Integration

```make
.PHONY: verify-globals
verify-globals: $(KERNEL_BIN)
	bash scripts/verify_globals.sh $(KERNEL_BIN)

build: verify-globals
```

## 5. Item 12 — `time.After` / full `time` package verification

### 5.1 Problem

Phase A enabled `time.Sleep` via `sleepTicks` /
`nanosecondsToTicks` wrappers in the patched TinyGo runtime.
`time.After`, `time.Ticker`, `time.Timer` were never
explicitly verified; the design doc
(`goroutine_design_channels_and_isr.md §4`) says:
"requires `sleepTicks` / `ticksToNanoseconds` AND a working
`time` package build. Confirm by grepping `nm tmp/kernel.bin`
for `time.*` symbols after the runtime-collision spike lands.
If `time` does not link, fall back to raw `sleepTicks` for
timeouts and implement `afterTicks(ticks timeUnit) <-chan
struct{}` locally."

### 5.2 Design

**Spike first**: a tiny kernel test:

```go
// Temporary in main.go, guarded by a const.
func timeAfterSpike() {
    ch := make(chan struct{})
    go func() {
        <-time.After(20 * time.Millisecond)
        close(ch)
    }()
    <-ch
    serialPrintln("time.After: OK")
}
```

If it links and prints "time.After: OK" within ~30 ms of boot,
no further work. Remove the spike.

If it fails to link (missing symbols from the `time` package):

**Implement `afterTicks` locally:**

```go
// src/afterticks.go

// afterTicks returns a channel that becomes readable after
// `ticks` PIT ticks (10 ms each at 100 Hz). A replacement for
// time.After on targets where the time package does not link.
//
// Implementation uses TinyGo's sleepTicks (the same primitive
// time.Sleep is built on, see scripts/tinygo_runtime.patch) so
// the spawned goroutine parks on the runtime sleep queue
// rather than spinning. A spin-and-Gosched loop would burn a
// scheduler quantum per tick across every outstanding timeout.
func afterTicks(ticks uint64) <-chan struct{} {
    ch := make(chan struct{}, 1)
    go func() {
        sleepTicks(timeUnit(ticks)) // //go:linkname'd to runtime.sleepTicks
        ch <- struct{}{}
    }()
    return ch
}
```

`sleepTicks` and `timeUnit` are exported from the patched TinyGo
runtime (see `~/.local/tinygo/src/runtime/runtime_gooos.go`); the
gooos kernel reaches them via `//go:linkname`. If `sleepTicks`
is not visible at the gooos package level today, add a one-line
`//go:linkname` shim in `src/afterticks.go`.

Use from any kernel goroutine that needs a timeout:

```go
select {
case v := <-ch:
    // ...
case <-afterTicks(2):
    // timeout (20 ms)
}
```

### 5.3 Files to modify

| Path | Change |
|---|---|
| `src/main.go` | temporary spike (behind `const timeAfterSpike = false`) |
| `src/afterticks.go` | **new**, conditional on spike failure |

## 6. Item 11 — Sleep granularity (10 ms PIT floor)

### 6.1 Problem

`sleepTicks` in `runtime_gooos.go` (patched TinyGo runtime)
uses PIT ticks at 100 Hz. Minimum sleep is 10 ms. Shorter
sleeps round up. In practice no gooos goroutine today needs
sub-10-ms sleep, but future latency-sensitive code might.

### 6.2 Design

Option (defer): keep 10 ms. Document the limitation in
`README.md` and `current_impl_doc/`. Revisit if a concrete
caller appears.

Option (mitigate): retrofit sleep to use the LAPIC timer's
one-shot mode. Each `sleepTicks(d)` programs the LAPIC timer
to fire after `d * calibrated_ticks`, then `sti + hlt`. The
LAPIC timer's resolution is sub-microsecond.

Recommended: **defer** until a caller justifies the work.
`deferred_smp_v2.md §3` already programs each AP's LAPIC
timer for periodic preemption; an SMP-v2 landing can
opportunistically add one-shot support as a free bonus.

### 6.3 Files to modify

No changes unless a concrete caller appears. If one does,
editing `~/.local/tinygo/src/runtime/runtime_gooos.go` to
swap PIT polling for LAPIC one-shot is a ~30-line change.

## 7. Item 16 — Keyboard latency measurement

### 7.1 Problem

`phase_b_keyboard_irq.md §3.4` says: "the bounded ring-buffer
staged by a consumer goroutine eliminates [the ISR-safety
concern] at the cost of one scheduler quantum (≤10 ms) of
added keystroke latency."

The actual latency has not been measured. If it is routinely
near 20 ms per keystroke (two quantums), fast typing will lag
visibly.

### 7.2 Design

A measurement extension to `tmp/test_sendkey.sh`:

```bash
#!/usr/bin/env bash
# Burst 100 keystrokes with no inter-key delay; measure
# wall-clock time from first sendkey to last echoed character
# on serial.
...
start=$(date +%s%N)
for _ in $(seq 1 100); do
    echo "sendkey a" | nc -q 0 -U "$MON"
done
# wait for 100 'a' echoes on serial
while [[ $(grep -c "^a" "$OUT") -lt 100 ]]; do sleep 0.01; done
end=$(date +%s%N)
echo "$((end - start)) ns for 100 keys"
```

Run once, record. If total time < 2 s (20 ms/key), acceptable.
If ≥ 2 s, investigate. Likely optimizations:

1. Reduce pump's `runtime.Gosched` churn.
2. Wake pump via IPI on IRQ (SMP v2 territory).
3. Tighten the ring-buffer `cap` to trigger back-pressure
   earlier.

### 7.3 Deliverable

If measurement shows no issue, document the result in
`current_impl_doc/` and close the item. If measurement shows
latency ≥ 20 ms/keystroke, open a dedicated design for the
chosen optimization.

No file-level code changes proposed in this doc; the design
is "measure first".

## 8. Dependencies

Items 10 + 14 share infrastructure (`scripts/lint_isr.go`).
Item 15 shares style with Makefile wiring (`make
verify-globals`).
Items 11, 12, 16 are independent measure-first tasks.

## 9. Verification

For each item:

1. Item 10 + 14:
   - Add a violating line (e.g., `serialPrintln("x" +
     utoa(1))` in `handleTimer`). Run `make lint`. Must
     fail with a clear message.
   - Remove the violation. Rerun. Must pass.
   - `make build` still succeeds on the cleaned tree.
2. Item 15:
   - Run `make verify-globals` on the current binary. Must
     pass.
   - Shift `sleepQueue` out of `.bss` (via a temporary
     linker hack). Must fail.
   - Revert. Must pass.
3. Item 12:
   - Enable the spike. Boot. Observe "time.After: OK" or a
     link failure.
4. Item 11:
   - Documentation-only. No verification step until a
     caller appears.
5. Item 16:
   - Run the extended sendkey measurement once. Record the
     result.
   - Decision tree: if < 20 ms/key, close item; if ≥ 20
     ms/key, open a new design for the chosen optimization.

## 10. Open questions

1. **Should `make lint` fail in CI only, or fail every
   local build?** Arguments for local: catches mistakes
   before they are pushed. Arguments for CI-only: keeps the
   inner dev loop fast. Recommended: local, because the
   lint is fast (<1 s).
2. **Does `make verify-globals` need to run on every build,
   or only on TinyGo upgrades?** Running every build adds
   ~100 ms. Acceptable.
3. **Is the keyboard-latency budget tight enough that a
   synthetic burst is representative?** Real users type at
   ~5 Hz, far below the harness. The harness is designed to
   stress; its numbers upper-bound real latency.
4. **Which TinyGo version is the `time` package known to
   work with?** Verify against TinyGo 0.33.0 (current). Any
   upgrade should rerun the spike.

## 11. Risk register delta

- **Retires**: `R-isr-safety-enforcement`,
  `R-runtime-alloc-reentry`, `R-global-layout`.
- **Retires on measurement** (item 16): `R-keyboard-latency`
  if measurement confirms ≤ 20 ms/keystroke.
- **Documents** (item 11): `R-sleep-granularity` remains as
  a "documented limitation" until a caller demands
  sub-10-ms sleep.
- **Adds**: none. The lint + verify infrastructure is
  standard and low-risk.
