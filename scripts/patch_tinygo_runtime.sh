#!/usr/bin/env bash
# Apply scripts/tinygo_runtime.patch to the TinyGo tree rooted at
# $TINYGO_SRC (default: $HOME/.local/tinygo/src).
#
# The patch installs six gooos-specific artifacts into TinyGo's
# runtime — see scripts/tinygo_runtime.patch for the full unified
# diff, and README.md § "User-writable TinyGo copy + runtime
# patches" for what each artifact does:
#
#   src/runtime/runtime_gooos.go                     (kernel, new)
#   src/runtime/runtime_gooos_user.go                (user,   new)
#   src/runtime/interrupt/interrupt_gooos.go         (kernel, new)
#   src/runtime/interrupt/interrupt_gooos_user.go    (user,   new)
#   src/internal/task/task_stack.go                  (adds state.stackTop + gooosStackOverflow)
#   src/internal/task/task_stack_amd64.go            (gooosOnResume hook)
#
# The kernel and userspace runtime bodies share the (gooos &&
# baremetal) build-tag prefix and are disambiguated by the
# `kernelspace` tag on src/target.json. Userspace builds
# (user/target.json) deliberately omit `kernelspace`.
#
# Idempotent: re-running on an already-patched tree is a no-op.
# Upgrade-safe: a tree with only the v1 runtime files (lacking
# the `kernelspace` tag) is repaired in place — the stale
# runtime_gooos.go / interrupt_gooos.go are removed and the
# patch is reapplied to land the tightened tags plus the two new
# userspace siblings.

set -euo pipefail

# Dual-version detection. Default to 0.40.1 (new canonical location);
# fall back to the legacy 0.33.0 tree with a deprecation warning if
# only it exists. Drop the legacy branch after SMP migration M3 lands.
if [[ -z "${TINYGO_SRC:-}" ]]; then
    if [[ -d "$HOME/.local/tinygo0.40.1/src" ]]; then
        TINYGO_SRC="$HOME/.local/tinygo0.40.1/src"
    elif [[ -d "$HOME/.local/tinygo/src" ]]; then
        TINYGO_SRC="$HOME/.local/tinygo/src"
        echo "warning: using deprecated 0.33.0 tree at $TINYGO_SRC" >&2
        echo "         upgrade to 0.40.1 per README.md (path changed" \
             "to ~/.local/tinygo0.40.1)" >&2
    else
        echo "error: neither 0.40.1 nor legacy 0.33.0 TinyGo tree found" >&2
        echo "       expected ~/.local/tinygo0.40.1/src or ~/.local/tinygo/src" >&2
        echo "       set TINYGO_SRC to override the path" >&2
        exit 1
    fi
fi

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

RG="$TINYGO_SRC/runtime/runtime_gooos.go"
RGU="$TINYGO_SRC/runtime/runtime_gooos_user.go"
IG="$TINYGO_SRC/runtime/interrupt/interrupt_gooos.go"
IGU="$TINYGO_SRC/runtime/interrupt/interrupt_gooos_user.go"
TS="$TINYGO_SRC/internal/task/task_stack.go"
TS64="$TINYGO_SRC/internal/task/task_stack_amd64.go"

# Canonical "clean" state: all four runtime files present, both
# runtime_gooos.go variants carry their kernelspace discriminator,
# and the task_stack.go hunks are in place.
SCHED="$TINYGO_SRC/runtime/scheduler.go"
CHAN="$TINYGO_SRC/runtime/chan.go"
GCBLK="$TINYGO_SRC/runtime/gc_blocks.go"

if [[ -f "$RG" && -f "$RGU" && -f "$IG" && -f "$IGU" ]] \
    && grep -q '&& kernelspace' "$RG" \
    && grep -q '&& !kernelspace' "$RGU" \
    && grep -q 'gooosStackOverflow' "$TS" \
    && grep -q 'gooosOnResume' "$TS64" \
    && grep -q 'runqueues' "$SCHED" \
    && grep -q 'systemStacks' "$TS64" \
    && grep -q 'gooosCpuID' "$CHAN" \
    && grep -q 'gooos_spinlockAcquire' "$TINYGO_SRC/internal/task/queue.go"; then
    echo "already-applied: tinygo runtime patch (SMP v2) present at $TINYGO_SRC"
    echo "(delete the gooos* runtime files and the in-place changes to re-run)"
    exit 0
fi

# Either fresh tree, v1 state (old runtime_gooos.go without the
# kernelspace tag, no _user sibling), or an incomplete v2 apply.
# Remove all four runtime files so the new-file hunks always create
# fresh; `patch`'s new-file semantics append when the target exists,
# which would duplicate bodies on re-apply. task_stack* modify
# hunks are safe to re-attempt because --forward skips
# already-applied ones.
rm -f "$RG" "$RGU" "$IG" "$IGU"

# Apply. `--forward` makes the modify-file hunks idempotent against
# v1 state; `--batch` keeps it non-interactive. Swallow the non-zero
# exit from already-applied hunks and rely on the post-condition
# grep to confirm success.
if ! patch -p1 -d "$TINYGO_ROOT" --forward --batch < "$PATCH_FILE"; then
    echo "(patch reported some hunks already applied; verifying end state…)"
fi

# --forward leaves .rej files next to task_stack*.go when the
# modify-file hunks were already applied (v1 tree). That's the
# expected outcome, not a failure; clear the residuals so future
# runs don't confuse the state.
rm -f "$TINYGO_SRC/internal/task/task_stack.go.rej" \
      "$TINYGO_SRC/internal/task/task_stack_amd64.go.rej" \
      "$TINYGO_SRC/runtime/scheduler.go.rej" \
      "$TINYGO_SRC/runtime/chan.go.rej" \
      "$TINYGO_SRC/runtime/gc_blocks.go.rej"

# Post-condition: every expected artifact is in place and carries
# the right discriminator. Any miss means the tree is in an
# unexpected shape; bail so the user can investigate.
fail=0
for f in "$RG" "$RGU" "$IG" "$IGU"; do
    if [[ ! -f "$f" ]]; then
        echo "error: expected file not present after patch: $f" >&2
        fail=1
    fi
done
if ! grep -q '&& kernelspace' "$RG"; then
    echo "error: $RG is missing the kernelspace build tag" >&2
    fail=1
fi
if ! grep -q '&& !kernelspace' "$RGU"; then
    echo "error: $RGU is missing the !kernelspace build tag" >&2
    fail=1
fi
if ! grep -q 'gooosStackOverflow' "$TS"; then
    echo "error: $TS is missing the gooosStackOverflow hook" >&2
    fail=1
fi
if ! grep -q 'gooosOnResume' "$TS64"; then
    echo "error: $TS64 is missing the gooosOnResume hook" >&2
    fail=1
fi
# SMP v2 post-conditions.
if ! grep -q 'runqueues' "$SCHED"; then
    echo "error: $SCHED is missing per-CPU runqueues" >&2
    fail=1
fi
if ! grep -q 'systemStacks' "$TS64"; then
    echo "error: $TS64 is missing per-CPU systemStacks" >&2
    fail=1
fi
if ! grep -q 'gooosCpuID' "$CHAN"; then
    echo "error: $CHAN is missing gooosCpuID calls" >&2
    fail=1
fi
if (( fail )); then
    exit 1
fi

echo
echo "tinygo runtime patched at $TINYGO_SRC"
echo
cat <<'EOF'
The patch installs:
  runtime_gooos.go              — kernel bodies (gooos && baremetal && kernelspace)
  runtime_gooos_user.go         — userspace bodies (gooos && baremetal && !kernelspace)
  interrupt/interrupt_gooos.go       — kernel interrupt shims
  interrupt/interrupt_gooos_user.go  — userspace no-op interrupt shims
  task_stack.go                 — adds state.stackTop + gooosStackOverflow hook
  task_stack_amd64.go           — calls gooosOnResume() in resume()

Kernel-side symbols (already provided by gooos):
  src/target.json                 "kernelspace" in build-tags
  src/isr.S                       gooos_in_interrupt_depth (.bss)
  src/stubs.S                     cli, sti, hlt, outb, readFlags, restoreFlags
  src/pit.go                      main.pitTicks
  src/goroutine_tss.go            gooosOnResume body
  src/panic.go                    gooosStackOverflow body

Userspace-side symbols (provided by gooos):
  user/rt0.S                      syscall1, syscall3 (via //go:linkname)
  user/gooos/runtime_hooks.go     gooosOnResume / gooosStackOverflow bodies

Re-run after TinyGo upgrades or whenever $HOME/.local/tinygo is
refreshed. To revert manually:
  rm $TINYGO_SRC/runtime/runtime_gooos.go
  rm $TINYGO_SRC/runtime/runtime_gooos_user.go
  rm $TINYGO_SRC/runtime/interrupt/interrupt_gooos.go
  rm $TINYGO_SRC/runtime/interrupt/interrupt_gooos_user.go
  patch -R -p1 -d $TINYGO_ROOT < scripts/tinygo_runtime.patch
EOF
