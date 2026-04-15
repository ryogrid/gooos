#!/usr/bin/env bash
# Patch the TinyGo runtime with gooos-specific files required for
# `scheduler=tasks` + bare-metal x86_64.
#
# Required because `goos=linux` pulls in `runtime_unix.go` (libc
# calls) and the `interrupt` package has no amd64-baremetal backend.
# TinyGo has no overlay flag, and Go's package system does not allow
# gooos's ./src to shadow runtime package files.
#
# gooos's Makefile points TINYGOROOT at $HOME/.local/tinygo (a
# user-writable copy of the system TinyGo tree). This script writes
# the two gooos-specific files into that tree.
#
# Usage:
#   bash scripts/patch_tinygo_runtime.sh
#
# Idempotent: re-running overwrites. Remove the two created files to
# revert. Override TINYGO_SRC for a non-default TinyGo root.

set -euo pipefail

TINYGO_SRC="${TINYGO_SRC:-$HOME/.local/tinygo/src}"

if [[ ! -d "$TINYGO_SRC/runtime" ]]; then
    echo "error: TinyGo runtime not found at $TINYGO_SRC/runtime" >&2
    echo "set TINYGO_SRC to override the path" >&2
    exit 1
fi

# --- runtime/runtime_gooos.go ---------------------------------------------
cat > "$TINYGO_SRC/runtime/runtime_gooos.go" <<'GOOOS_RUNTIME'
//go:build gooos && baremetal

// gooos-local runtime bodies for bare-metal x86_64 Ring 0.
// Replaces the parts of runtime_unix.go incompatible with kernel
// mode. The kernel defines putchar, pitTicks, and the cli/sti/hlt
// primitives these stubs rely on.

package runtime

type timeUnit int64

//go:linkname gooos_pitTicks main.pitTicks
var gooos_pitTicks uint64

// The kernel's assembly stubs in src/stubs.S use unqualified names
// (no package prefix), so we link directly to those symbols.

//go:linkname gooos_cli cli
func gooos_cli()

//go:linkname gooos_sti sti
func gooos_sti()

//go:linkname gooos_hlt hlt
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

// tinygo_register_fatal_signals is provided by src/stubs.S (the
// kernel has had this stub since before goroutine support). No Go
// body needed here.

// putchar writes a single byte to COM1. Used by runtime.printstring.
//
//go:linkname gooos_outb outb
func gooos_outb(port uint16, value uint8)

func putchar(c byte) { gooos_outb(0x3F8, c) }

// buffered is a no-op on bare metal — no line-buffered stdio.
func buffered() int { return 0 }

// getchar is not used by gooos; return 0 to avoid pulling in stdin.
func getchar() byte { return 0 }

// exit / abort: kernel panic paths. No process to exit; halt forever.
func exit(code int) {
	for {
		gooos_hlt()
	}
}

func abort() {
	for {
		gooos_hlt()
	}
}

// preinit is called by the runtime before initAll(). gooos's main.go
// performs all hardware init in user main(), so preinit is a no-op.
func preinit() {}

// main is the C-ABI entry point called from boot.S after the
// 32->64-bit bootstrap. run() initializes the heap, runs package
// init functions, and invokes the user-written main in main.go.
// If run() returns, halt forever.
//
//export main
func main() {
	preinit()
	run()
	exit(0)
}

// waitForEvents provided by wait_other.go (fallback). No override
// needed — that fallback body is a no-op, acceptable here.
GOOOS_RUNTIME

# --- runtime/interrupt/interrupt_gooos.go ---------------------------------
cat > "$TINYGO_SRC/runtime/interrupt/interrupt_gooos.go" <<'GOOOS_INTERRUPT'
//go:build gooos && baremetal

package interrupt

//go:linkname gooos_readFlags readFlags
func gooos_readFlags() uintptr

//go:linkname gooos_restoreFlags restoreFlags
func gooos_restoreFlags(flags uintptr)

//go:linkname gooos_cli cli
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
