#!/usr/bin/env bash
# Patch the locally installed TinyGo runtime with gooos-specific files
# required for `scheduler=tasks` + bare-metal x86_64. Run with sudo.
#
# Required because `goos=linux` pulls in `runtime_unix.go` (libc
# calls) and the `interrupt` package has no amd64-baremetal backend.
# TinyGo's runtime files are owned by root and cannot be overlaid from
# gooos's source tree.
#
# Usage:
#   sudo bash scripts/patch_tinygo_runtime.sh
#
# Idempotent: re-running overwrites the installed files with the
# versions below. Remove /usr/local/lib/tinygo/src/runtime/runtime_gooos.go
# and /usr/local/lib/tinygo/src/runtime/interrupt/interrupt_gooos.go to
# revert.
#
# After applying, add "baremetal" to src/target.json's build-tags array
# (alongside "gooos") so this file is compiled in and runtime_unix.go /
# interrupt_none.go are compiled out.

set -euo pipefail

TINYGO_SRC="${TINYGO_SRC:-/usr/local/lib/tinygo/src}"

if [[ ! -d "$TINYGO_SRC/runtime" ]]; then
    echo "error: TinyGo runtime not found at $TINYGO_SRC/runtime" >&2
    echo "set TINYGO_SRC to override the path" >&2
    exit 1
fi

# --- runtime/runtime_gooos.go ---------------------------------------------
cat > "$TINYGO_SRC/runtime/runtime_gooos.go" <<'GOOOS_RUNTIME'
//go:build gooos && baremetal

// gooos-local runtime bodies for bare-metal x86_64 Ring 0.
// This file replaces the parts of runtime_unix.go that are incompatible
// with kernel mode. The kernel's main.go defines putchar, pitTicks, and
// the cli/sti/hlt primitives these stubs rely on.

package runtime

//go:linkname gooos_pitTicks main.pitTicks
var gooos_pitTicks uint64

//go:linkname gooos_cli main.cli
func gooos_cli()

//go:linkname gooos_sti main.sti
func gooos_sti()

//go:linkname gooos_hlt main.hlt
func gooos_hlt()

// sleepTicks idles on the PIT counter until `d` ticks have elapsed.
// The kernel's timer ISR increments gooos_pitTicks at 100 Hz.
func sleepTicks(d timeUnit) {
	deadline := gooos_pitTicks + uint64(d)
	for gooos_pitTicks < deadline {
		gooos_sti()
		gooos_hlt()
		gooos_cli()
	}
}

func ticks() timeUnit { return timeUnit(gooos_pitTicks) }

// 100 Hz PIT: one tick = 10 ms = 10_000_000 ns.
func ticksToNanoseconds(t timeUnit) int64  { return int64(t) * 10_000_000 }
func nanosecondsToTicks(ns int64) timeUnit { return timeUnit(ns / 10_000_000) }

// tinygo_register_fatal_signals has no analog on bare metal.
//
//export tinygo_register_fatal_signals
func tinygo_register_fatal_signals() {}

// putchar writes a single byte to COM1. Used by runtime.printstring
// before the main serial channel is running. Relies on the kernel's
// port I/O primitive.
//
//go:linkname gooos_outb main.outb
func gooos_outb(port uint16, value uint8)

func putchar(c byte) { gooos_outb(0x3F8, c) }

// preinit is called by the TinyGo runtime before initAll(). gooos's
// main.go already performs hardware init in its user-level main(),
// so preinit is a no-op here.
func preinit() {}

// main is the runtime-level entry point TinyGo expects from a
// bare-metal target. For gooos the boot chain is
// boot.S -> TinyGo runtime main -> runtime.run() -> user main().
// main() is provided by the runtime; the user's `func main()` in
// the `main` package is called via callMain() from run().
GOOOS_RUNTIME

# --- runtime/interrupt/interrupt_gooos.go ---------------------------------
cat > "$TINYGO_SRC/runtime/interrupt/interrupt_gooos.go" <<'GOOOS_INTERRUPT'
//go:build gooos && baremetal

package interrupt

//go:linkname gooos_readFlags main.readFlags
func gooos_readFlags() uintptr

//go:linkname gooos_restoreFlags main.restoreFlags
func gooos_restoreFlags(flags uintptr)

//go:linkname gooos_cli main.cli
func gooos_cli()

// Atomic counter bumped by the gooos common ISR prologue / epilogue
// (src/isr.S). Exposed here so In() can report ISR context.
//
//go:linkname gooos_inInterruptDepth main.inInterruptDepth
var gooos_inInterruptDepth uint32

// State holds RFLAGS at the moment of Disable() so Restore() can
// decide whether to re-enable interrupts.
type State uintptr

func Disable() State {
	flags := gooos_readFlags()
	gooos_cli()
	return State(flags)
}

func Restore(state State) { gooos_restoreFlags(uintptr(state)) }

func In() bool { return gooos_inInterruptDepth != 0 }
GOOOS_INTERRUPT

echo "patched: $TINYGO_SRC/runtime/runtime_gooos.go"
echo "patched: $TINYGO_SRC/runtime/interrupt/interrupt_gooos.go"
echo
echo "Next steps:"
echo "  1. add \"baremetal\" to src/target.json build-tags alongside \"gooos\""
echo "  2. add main.inInterruptDepth global (uint32) in src/goroutine_irq.go"
echo "  3. wire src/isr.S prologue/epilogue to inc/dec main.inInterruptDepth"
echo "  4. flip \"scheduler\": \"none\" -> \"tasks\" once everything else is in place"
