#!/usr/bin/env bash
# Apply scripts/tinygo_runtime.patch to the TinyGo tree rooted at
# $TINYGO_SRC (default: $HOME/.local/tinygo/src).
#
# The patch installs four gooos-specific artifacts into TinyGo's
# runtime — see scripts/tinygo_runtime.patch for the full unified
# diff, and README.md § "User-writable TinyGo copy + runtime
# patches" for what each artifact does:
#
#   src/runtime/runtime_gooos.go             (new file)
#   src/runtime/interrupt/interrupt_gooos.go (new file)
#   src/internal/task/task_stack.go          (adds state.stackTop)
#   src/internal/task/task_stack_amd64.go    (gooosOnResume hook)
#
# Idempotent: re-running on an already-patched tree is a no-op.
# A sentinel line in runtime_gooos.go is used to decide whether
# the patch has been applied already; `patch --forward --batch`
# is applied otherwise.

set -euo pipefail

TINYGO_SRC="${TINYGO_SRC:-$HOME/.local/tinygo/src}"
PATCH_FILE="$(cd "$(dirname "$0")" && pwd)/tinygo_runtime.patch"

if [[ ! -f "$PATCH_FILE" ]]; then
    echo "error: patch file not found at $PATCH_FILE" >&2
    exit 1
fi

if [[ ! -d "$TINYGO_SRC/runtime" ]]; then
    echo "error: TinyGo runtime not found at $TINYGO_SRC/runtime" >&2
    echo "set TINYGO_SRC to override the path" >&2
    exit 1
fi

TINYGO_ROOT="$(dirname "$TINYGO_SRC")"
SENTINEL_FILE="$TINYGO_SRC/runtime/runtime_gooos.go"
SENTINEL_TEXT="gooos-local runtime bodies"

if [[ -f "$SENTINEL_FILE" ]] && grep -q "$SENTINEL_TEXT" "$SENTINEL_FILE"; then
    echo "already-applied: tinygo runtime patch present at $TINYGO_SRC"
    echo "(delete $SENTINEL_FILE and the in-place changes to re-run)"
    exit 0
fi

# Fresh apply. --forward --batch keeps this non-interactive; any
# conflict means the tree is not pristine and the user must
# investigate.
patch -p1 -d "$TINYGO_ROOT" --forward --batch < "$PATCH_FILE"

echo
echo "tinygo runtime patched at $TINYGO_SRC"
echo
cat <<'EOF'
The patch installs:
  runtime_gooos.go             — sleepTicks/ticks/deadlock/main/exit etc.
  interrupt/interrupt_gooos.go — Disable/Restore/In + State
  task_stack.go                — adds state.stackTop field
  task_stack_amd64.go          — calls gooosOnResume() in resume()

Kernel-side symbols these files expect (already provided by gooos):
  src/target.json                 "baremetal" in build-tags
  src/isr.S                       gooos_in_interrupt_depth (.bss)
  src/stubs.S                     cli, sti, hlt, outb, readFlags,
                                  restoreFlags
  src/pit.go                      main.pitTicks
  src/goroutine_irq.go            Go-side linkname binding
  src/goroutine_tss.go            gooosOnResume body

Re-run after TinyGo upgrades or whenever $HOME/.local/tinygo is
refreshed. To revert manually:
  rm $TINYGO_SRC/runtime/runtime_gooos.go
  rm $TINYGO_SRC/runtime/interrupt/interrupt_gooos.go
  patch -R -p1 -d $TINYGO_ROOT < scripts/tinygo_runtime.patch
EOF
