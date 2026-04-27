# 08 — Build config and TinyGo patch audit

Route C's kernel build drops TinyGo scheduling from the kernel side.
This doc enumerates every concrete change to `src/target.json`,
`Makefile`, `scripts/tinygo_runtime.patch`, and the live patched
TinyGo tree at `~/.local/tinygo0.40.1/`. User-side build stays
untouched (INVARIANT K5 from §01; confirmed in §07).

## Current build (STATUS-QUO)

`src/target.json` (verified `file:line 9`):

```json
"scheduler": "cores",
```

`user/target.json` (verified `file:line 9`):

```json
"scheduler": "tasks",
```

`scripts/tinygo_runtime.patch`: 1168 lines, 15 files touched (from
the Explore-agent baseline; re-verified via `grep -n '^---\|^+++'`
on the patch):

1. `src/internal/task/queue.go` — Queue + `lock uint32` + gooos
   spinlock linknames. Kernel-side scheduler substrate.
2. `src/internal/task/task_stack.go` — `stackTop` field +
   `gooosStackOverflow` hook.
3. `src/internal/task/task_stack_amd64.go` — amd64 specifics for
   the `stackTop` path.
4. `src/internal/task/task_stack_multicore.go` — multicore
   variant.
5. `src/internal/task/task_stack_unicore.go` — unicore variant
   used by `scheduler=tasks` (user-side).
6. `src/runtime/gc_blocks.go` — comment-only annotation about the
   "BSP-only allocates" workaround.
7. `src/runtime/interrupt/interrupt_gooos.go` — new; kernel-side
   `interrupt.Disable` / `Restore` / `In`.
8. `src/runtime/interrupt/interrupt_gooos_user.go` — new; userspace
   counterparts.
9. `src/runtime/runtime_gooos.go` — new; kernel `sleepTicks`,
   `ticks`, `putchar`, `exit`, `abort`, `main`.
10. `src/runtime/runtime_gooos_user.go` — new; userspace
    syscall-backed versions.
11. `src/runtime/scheduler_cooperative.go` — patched; per-CPU
    runqueues under `scheduler=cores` tag.
12. `src/runtime/scheduler_cores.go` — patched; `apScheduler`
    entry, `scheduleTask` push, `stealWork` round-robin. Largest
    single hunk set (~140 lines).
13. `src/runtime/wait_gooos.go` — new; `waitForEvents` =
    `sti; hlt; cli`.
14. `src/runtime/wait_gooos_user.go` — new; user-side no-op.
15. `src/runtime/wait_other.go` — patched; adds `&& !gooos` to
    other-target build tag.

## Route C target (PROPOSED)

`src/target.json` changes:

```json
-  "scheduler": "cores",
+  "scheduler": "none",
```

`"scheduler": "none"` is a valid TinyGo target option. Under
`scheduler=none`, TinyGo does not link a scheduler; the program's
`main` is called directly and any `go` statement in the *linked
code* becomes a compile error (TinyGo rejects it). This is exactly
what we want: Route C's kernel code has no `go` statements after
§06's migration, so the compile would fail if any regressed in.

### Alternative: custom `scheduler=gooos` tag

A gooos-specific scheduler tag would carve out our own
`scheduler_gooos.go` file to hold `kschedLoop` wiring under a
build-tag. The build tag would be `tinygo.scheduler.gooos` (to
match TinyGo's convention).

Pros of custom tag:

- Keeps a foothold for the TinyGo runtime to call `runtime.Gosched()`
  from an unexpected caller (it falls through to our implementation
  instead of a linker error).
- Symmetric with the existing `!gooos` tag on `wait_other.go`.

Cons:

- Larger patch surface; introduces a TinyGo-side concept we have to
  maintain across TinyGo versions.
- Doesn't catch regressions where someone re-adds a kernel
  goroutine (the scheduler tag would silently handle the goroutine
  via the gooos-native path).

**Route C recommendation**: use `scheduler=none` for the kernel;
keep the linker-error as the safety net. If an uncaught
`runtime.Gosched()` trickles in from a TinyGo library or an
`impldoc/` code sample, fix at the source. Reconsider in §10 if
that proves painful.

### `user/target.json` — unchanged

```json
"scheduler": "tasks",
```

Stays. User binaries keep cooperative goroutines, `chan`, `select`.

## Patch hunk audit

Per-file verdict for `scripts/tinygo_runtime.patch`:

### `src/internal/task/queue.go` (lines 1..97 of patch)

- **Status**: **flip → delete**.
- Rationale: no kernel-side TinyGo queue under `scheduler=none`. The
  hunk adds `lock uint32` + gooos spinlock linknames — all for the
  multi-core scheduler, which we're removing. The user-side does
  use `internal/task/queue.go`, but only through `scheduler=tasks`
  which doesn't use the SMP-spinlock variant.
- **Action for M5**: remove the hunk. Keep user-side behaviour
  identical (the unpatched upstream `queue.go` is what
  `scheduler=tasks` expects).

### `src/internal/task/task_stack.go` (patch lines 98..121)

- **Status**: **split** — user-side keeps, kernel-side loses.
- The `stackTop` field + `gooosStackOverflow` hook are still useful
  for the user-side TinyGo `scheduler=tasks`. The kernel side no
  longer uses `task.Task` at all (kernel threads are `KernelThread`),
  so the field is unreferenced when the kernel links without a
  scheduler.
- **Action for M5**: keep the patch hunk in the TinyGo tree, but
  the kernel build no longer pulls the `internal/task` package,
  so the hunk simply doesn't compile into `kernel.bin`. Verify via
  `nm tmp/kernel.bin | grep task_` — should show only the user
  linker-embedded symbols.

### `src/internal/task/task_stack_amd64.go` (patch lines 122..177)

- **Status**: **user-side keeps**; kernel unchanged (not linked).
- Same reasoning as `task_stack.go`.

### `src/internal/task/task_stack_multicore.go` (patch lines 178..205)

- **Status**: **delete**.
- Multi-core task-queue support is scheduler-adjacent; user-side
  `scheduler=tasks` is unicore. Kernel removes `scheduler=cores`.
  No consumer of the multicore variant remains.
- **Action for M5**: remove the hunk.

### `src/internal/task/task_stack_unicore.go` (patch lines 206..278)

- **Status**: **user-side keeps**; kernel unchanged (not linked).
- Covers `scheduler=tasks`. User-only.

### `src/runtime/gc_blocks.go` (patch lines 279..312)

- **Status**: **delete**.
- The comment-only annotation says "cross-CPU correctness under
  gooos relies on the BSP-only-allocates contract (APs park in
  `waitForEvents`) until the future M5 `gcPauseCore` IPI lands."
  §05 implements that IPI. The annotation is obsolete.
- **Action for M5**: remove the patch hunk; gc_blocks.go returns
  to pristine.

### `src/runtime/interrupt/interrupt_gooos.go` (patch lines 313..365)

- **Status**: **keep (trimmed)**.
- Body: `interrupt.Disable` = cli, `interrupt.Restore` = restore
  flags, `interrupt.In` returns true inside ISR. Useful for any
  TinyGo library code we still link (e.g. sync primitives that
  disable interrupts).
- **Action for M5**: keep the file; check if any function body
  drops in size now that `interrupt.In` consumer inside the TinyGo
  scheduler is gone. Trim where possible.

### `src/runtime/interrupt/interrupt_gooos_user.go` (patch lines 366..385)

- **Status**: **keep, user-only**. Untouched.

### `src/runtime/runtime_gooos.go` (patch lines 386..677)

- **Status**: **keep (rewired)**.
- Provides `sleepTicks`, `ticks`, `putchar`, `exit`, `abort`, `main`.
  The `main` entry is what `boot.S` calls; Route C keeps it but
  its body changes: after the existing early init (mmap stub, heap
  init), it calls our `kmain()` (defined in `src/main.go` once
  §09 M0 lands) which eventually enters `kschedLoop`. The file
  itself stays; the Go body in `src/main.go` provides the new
  control flow.
- `sleepTicks` today reads `pitTicks`. Route C keeps the same
  implementation. It's used from kernel code occasionally; under
  Route C any caller must be on a kernel thread (heading into
  `kschedTimedPark` is the better idiom, but `sleepTicks` as a
  spin-wait against `pitTicks` is tolerable for boot-time callers).
- **Action for M5**: trim the file only if pieces become dead. The
  existing `putchar`, `exit`, `abort` hooks stay verbatim.

### `src/runtime/runtime_gooos_user.go` (patch lines 678..768)

- **Status**: **keep, user-only**. Untouched.

### `src/runtime/scheduler_cooperative.go` (patch lines 769..974)

- **Status**: **delete**.
- Contains the `scheduler=cores` per-CPU runqueue scaffolding. Gone
  under Route C.
- **Action for M5**: remove the hunk wholesale.

### `src/runtime/scheduler_cores.go` (patch lines 975..1119)

- **Status**: **delete**.
- The biggest hunk set. Contains `apScheduler`, `scheduleTask`
  retargeting, `stealWork` scan, the F1-implicated `push-to-waker-
  queue` line (1004), and the `gooosWakeupCPU` bridge. All gone.
- **Action for M5**: remove the hunk wholesale.

### `src/runtime/wait_gooos.go` (patch lines 1120..1139)

- **Status**: **keep**.
- `waitForEvents` = `sti; hlt; cli`. Used as the idle-thread body
  under Route C (§02).
- **Action for M5**: keep. May rename to `kschedIdleBody` if the
  file position in the TinyGo runtime tree becomes awkward; optional.

### `src/runtime/wait_gooos_user.go` (patch lines 1140..1158)

- **Status**: **keep, user-only**. Untouched.

### `src/runtime/wait_other.go` (patch lines 1159..1168)

- **Status**: **keep** — the `!gooos` tag continues to matter for
  non-gooos build targets someone might enable (unlikely but
  cheap).

### Hunk-removal summary

| Patch region | Disposition | Lines (approx) |
|--------------|------------|----------------|
| queue.go | delete | ~97 |
| task_stack.go | keep (user) | ~24 |
| task_stack_amd64.go | keep (user) | ~56 |
| task_stack_multicore.go | delete | ~28 |
| task_stack_unicore.go | keep (user) | ~73 |
| gc_blocks.go | delete | ~34 |
| interrupt_gooos.go | keep (trimmed) | ~53 |
| interrupt_gooos_user.go | keep | ~20 |
| runtime_gooos.go | keep (rewire main) | ~292 |
| runtime_gooos_user.go | keep | ~91 |
| scheduler_cooperative.go | delete | ~206 |
| scheduler_cores.go | delete | ~145 |
| wait_gooos.go | keep | ~20 |
| wait_gooos_user.go | keep | ~19 |
| wait_other.go | keep | ~10 |

Delete total: ~510 lines. Trim total: ~20 lines. Remaining patch
size after M5: ~650 lines. That is the **smallest coherent set**
§hoge asks us to identify: user-side + a tiny kernel-side runtime
shim + an idle hook. TinyGo-version drift surface collapses from
"every scheduler file" to "a few kernel entry-point files".

## Live-tree sync

`~/.local/tinygo0.40.1/` must reflect the patch state after M5.
The existing wrapper `scripts/patch_tinygo_runtime.sh` is
idempotent (per README:258). After M5 the wrapper gets a matching
update so rerunning it produces the new trimmed patch output.

**Execution sequence for M5**:

1. `scripts/tinygo_runtime.patch` is edited to remove the delete-
   class hunks and update keep-trimmed hunks.
2. Revert old patch, apply new patch:
   ```
   patch -R -p1 -d ~/.local/tinygo0.40.1 < old_patch_backup
   bash scripts/patch_tinygo_runtime.sh
   ```
3. Rebuild kernel.
4. `make lint` + `make verify-globals` clean.

## Makefile changes

Grep `Makefile` for:

- `TINYGOROOT` — unchanged; still points at `~/.local/tinygo0.40.1`.
- `scheduler=cores` literal — not present (scheduler is driven by
  `target.json`). No Makefile edit needed.
- `verify-globals` — re-target in M5. The current `scripts/verify_globals.sh`
  asserts `runqueue`, `sleepQueue`, `timerQueue` symbols land
  inside `_globals_start..end`. Those symbols are gone under Route
  C; replace the assertions with new kthread-scheduler globals
  (`kschedQueues`, `kthreadAll`, `kthreadPool`, etc., PROPOSED in
  §02 / §05).

## `make lint` (ISR-safety)

`scripts/lint_isr.go` (an AST walker) rejects `go`, `make`,
channel ops, and runtime allocations inside ISR-rooted functions.
Route C makes this strictly stronger: under `scheduler=none`
there are no kernel-side goroutines at all, so `go` inside any
kernel function is a link error. Lint stays as a friendlier
pre-build check. No changes needed; §09 M5 verifies lint still
passes after `scheduler=none` flip.

## `make verify-globals` update

- Remove assertions: `runqueue`, `sleepQueue`, `timerQueue`.
- Add assertions: `kschedQueues`, `kschedIdle`, `kschedRunning`,
  `kthreadAll`, `kthreadPool`, `stwReleaseFlag`, `stwFrozenCount`.
- All new symbols are `.bss`-resident package-level variables in
  `src/kthread_*.go`, so they land inside `_globals_start..end`
  by default. The check is a safety net.

## Version-drift surface

After M5, a TinyGo 0.40.x → 0.41.x upgrade needs to touch only:

- `internal/task/task_stack*.go` (user-side stackTop field carrier).
- `runtime/runtime_gooos.go` (entry-point wiring; mostly stable
  unless TinyGo changes `main` signature).
- `runtime/interrupt/interrupt_gooos.go` (tiny).
- `runtime/wait_gooos.go` / `wait_other.go` tag plumbing.

Before Route C: every scheduler-file rename / refactor in upstream
TinyGo would be a merge conflict. After Route C: scheduler files
are pristine upstream, no conflict. This is the single largest
version-drift reduction §01's "Pros" list referenced.

## Summary of §08 touch-points (M5 only)

| File | Change |
|------|--------|
| `src/target.json` | `"scheduler": "cores"` → `"scheduler": "none"` |
| `scripts/tinygo_runtime.patch` | Delete ~510 lines across 4 hunks; trim ~20 lines |
| `scripts/patch_tinygo_runtime.sh` | Update the checksums / sentinel checks to match the new patch shape |
| `scripts/verify_globals.sh` | Replace `runqueue`/`sleepQueue`/`timerQueue` asserts with kthread-scheduler globals |
| `~/.local/tinygo0.40.1/` | Revert old patch, apply new |

No Makefile edit. No `go.mod` / `go.sum` change.

## Reviewer gates

- Every patch hunk categorized: **yes** (table + per-file verdicts).
- User-side patches stay intact: **yes** (flagged `keep, user-only`
  for each `*_user.go`).
- Smallest coherent set identified: **yes** (~650 lines).
- Build tag strategy justified: **yes** (`scheduler=none` preferred
  over `scheduler=gooos` with rationale).
