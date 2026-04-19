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
WG="$TINYGO_SRC/runtime/wait_gooos.go"
WGU="$TINYGO_SRC/runtime/wait_gooos_user.go"
TS="$TINYGO_SRC/internal/task/task_stack.go"
TS64="$TINYGO_SRC/internal/task/task_stack_amd64.go"
TSU="$TINYGO_SRC/internal/task/task_stack_unicore.go"

# Canonical "clean" state: all six gooos runtime files present, both
# runtime_gooos.go variants carry their kernelspace discriminator,
# task_stack{,_amd64,_unicore}.go hunks are in place, and the
# scheduler hunks landed on scheduler_cooperative.go (0.40.1 split
# from the 0.33.0 monolithic scheduler.go).
SCHED="$TINYGO_SRC/runtime/scheduler_cooperative.go"

if [[ -f "$RG" && -f "$RGU" && -f "$IG" && -f "$IGU" && -f "$WG" && -f "$WGU" ]] \
    && grep -q '&& kernelspace' "$RG" \
    && grep -q '&& !kernelspace' "$RGU" \
    && grep -q 'gooosStackOverflow' "$TSU" \
    && grep -q 'gooosOnResume' "$TS64" \
    && grep -q 'stackTop uintptr' "$TS" \
    && grep -q 'runqueues' "$SCHED" \
    && grep -q 'stealWork' "$SCHED" \
    && grep -q 'apScheduler' "$SCHED" \
    && grep -q 'systemStacks' "$TS64" \
    && grep -q 'currentTasks' "$TSU" \
    && grep -q 'gooos_spinlockAcquire' "$TINYGO_SRC/internal/task/queue.go"; then
    echo "already-applied: tinygo runtime patch (SMP v2 on 0.40.1) present at $TINYGO_SRC"
    echo "(delete the gooos* runtime files and the in-place changes to re-run)"
    exit 0
fi

# Either fresh tree, v1 state (old runtime_gooos.go without the
# kernelspace tag, no _user sibling), or an incomplete v3 (0.40.1)
# apply. Remove all six new-file artifacts so the new-file hunks
# always create fresh; `patch`'s new-file semantics append when the
# target exists, which would duplicate bodies on re-apply. task_stack*
# / scheduler_cooperative modify hunks are safe to re-attempt because
# --forward skips already-applied ones.
rm -f "$RG" "$RGU" "$IG" "$IGU" "$WG" "$WGU"

# Apply. `--forward` makes the modify-file hunks idempotent against
# v1 state; `--batch` keeps it non-interactive. Swallow the non-zero
# exit from already-applied hunks and rely on the post-condition
# grep to confirm success.
if ! patch -p1 -d "$TINYGO_ROOT" --forward --batch < "$PATCH_FILE"; then
    echo "(patch reported some hunks already applied; verifying end state…)"
fi

# --forward leaves .rej files next to modify hunks when the hunks
# were already applied. That's the expected outcome, not a failure;
# clear the residuals so future runs don't confuse the state.
rm -f "$TINYGO_SRC/internal/task/task_stack.go.rej" \
      "$TINYGO_SRC/internal/task/task_stack_amd64.go.rej" \
      "$TINYGO_SRC/internal/task/task_stack_unicore.go.rej" \
      "$TINYGO_SRC/internal/task/queue.go.rej" \
      "$TINYGO_SRC/runtime/scheduler_cooperative.go.rej" \
      "$TINYGO_SRC/runtime/gc_blocks.go.rej" \
      "$TINYGO_SRC/runtime/wait_other.go.rej"

# Post-condition: every expected artifact is in place and carries
# the right discriminator. Any miss means the tree is in an
# unexpected shape; bail so the user can investigate.
fail=0
for f in "$RG" "$RGU" "$IG" "$IGU" "$WG" "$WGU"; do
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
if ! grep -q 'stackTop uintptr' "$TS"; then
    echo "error: $TS is missing the stackTop struct field" >&2
    fail=1
fi
if ! grep -q 'gooosStackOverflow' "$TSU"; then
    echo "error: $TSU is missing the gooosStackOverflow hook" >&2
    fail=1
fi
if ! grep -q 'gooosOnResume' "$TS64"; then
    echo "error: $TS64 is missing the gooosOnResume hook" >&2
    fail=1
fi
# SMP v2 (0.40.1) post-conditions.
if ! grep -q 'runqueues' "$SCHED"; then
    echo "error: $SCHED is missing per-CPU runqueues" >&2
    fail=1
fi
if ! grep -q 'stealWork' "$SCHED"; then
    echo "error: $SCHED is missing the stealWork helper" >&2
    fail=1
fi
if ! grep -q 'apScheduler' "$SCHED"; then
    echo "error: $SCHED is missing the apScheduler entry" >&2
    fail=1
fi
if ! grep -q 'systemStacks' "$TS64"; then
    echo "error: $TS64 is missing per-CPU systemStacks" >&2
    fail=1
fi
if ! grep -q 'currentTasks' "$TSU"; then
    echo "error: $TSU is missing per-CPU currentTasks" >&2
    fail=1
fi
if (( fail )); then
    exit 1
fi

echo
echo "tinygo runtime patched at $TINYGO_SRC"
echo
cat <<'EOF'
The patch installs (TinyGo 0.40.1 layout):
  runtime/runtime_gooos.go              — kernel bodies (gooos && baremetal && kernelspace)
  runtime/runtime_gooos_user.go         — userspace bodies (gooos && baremetal && !kernelspace)
  runtime/wait_gooos.go                 — kernel waitForEvents (sti; hlt; cli)
  runtime/wait_gooos_user.go            — userspace waitForEvents (no-op)
  runtime/interrupt/interrupt_gooos.go       — kernel interrupt shims
  runtime/interrupt/interrupt_gooos_user.go  — userspace no-op interrupt shims
  runtime/scheduler_cooperative.go      — per-CPU runqueues[17], schedLock,
                                          stealWork(), apScheduler(), push-site
                                          retargeting (was scheduler.go in 0.33.0)
  runtime/gc_blocks.go                  — explicit heapLock spinlock
                                          (task.PMutex is no-op under
                                          tinygo.unicore / scheduler=tasks)
  runtime/wait_other.go                 — adds && !gooos to the build tag
  internal/task/task_stack.go           — adds state.stackTop
  internal/task/task_stack_amd64.go     — per-CPU systemStacks[17], gooosOnResume
  internal/task/task_stack_unicore.go   — per-CPU currentTasks[17], gooosStackOverflow
                                          (this file is new in 0.40.x under
                                          scheduler=tasks; was task_stack.go
                                          in 0.33.0)
  internal/task/queue.go                — per-Queue spinlock (gooos_spinlockAcquire)

Kernel-side symbols (already provided by gooos):
  src/target.json                 "kernelspace" in build-tags
  src/isr.S                       gooos_in_interrupt_depth (.bss)
  src/stubs.S                     cli, sti, hlt, outb, readFlags, restoreFlags,
                                  spinlockAcquire, spinlockRelease
  src/pit.go                      main.pitTicks
  src/goroutine_tss.go            gooosOnResume body
  src/panic.go                    gooosStackOverflow body
  src/percpu.go                   cpuID (%gs:0 read)

Userspace-side symbols (provided by gooos):
  user/rt0.S                      syscall1, syscall3 (via //go:linkname),
                                  cpuID / spinlockAcquire / spinlockRelease stubs
  user/gooos/runtime_hooks.go     gooosOnResume / gooosStackOverflow bodies

Re-run after TinyGo upgrades or whenever $HOME/.local/tinygo0.40.1 is
refreshed. To revert manually:
  rm $TINYGO_SRC/runtime/runtime_gooos.go
  rm $TINYGO_SRC/runtime/runtime_gooos_user.go
  rm $TINYGO_SRC/runtime/interrupt/interrupt_gooos.go
  rm $TINYGO_SRC/runtime/interrupt/interrupt_gooos_user.go
  rm $TINYGO_SRC/runtime/wait_gooos.go
  rm $TINYGO_SRC/runtime/wait_gooos_user.go
  patch -R -p1 -d $TINYGO_ROOT < scripts/tinygo_runtime.patch
EOF
