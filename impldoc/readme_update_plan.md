# README.md Update Plan — TinyGo 0.40.1 Migration

**Scope.** Precise edits to `README.md` that accompany each migration wave. Edits are split Wave 1 (toolchain swap) / Wave 2 (cores-mode promotion) so they land with their corresponding source-level changes and are bisectable independently.

**Cross-links.**
- Toolchain switch that drives the edits: `impldoc/toolchain_switch_plan.md`
- Milestone cadence: `impldoc/smp_milestones_and_verification.md`
- Rollback that un-does these edits: `impldoc/rollback_plan.md`
- Top-level index: `impldoc/smp_migration_overview.md`

`README.md` is **441 lines** total as of the migration start. Line references below are **pre-migration** line numbers — after the first edit lands, line numbers shift. An implementation agent should re-locate each subsequent edit by its Markdown-section heading or by quoted context text, not by the pre-migration line number. Where possible, edits below are described as grep-replace rules over stable content rather than absolute line numbers.

---

## Wave 1 Edits — Toolchain Swap

Land with the same commit as `Makefile` + `scripts/patch_tinygo_runtime.sh` updates (per `impldoc/toolchain_switch_plan.md §3 commit 5`).

### Edit 1 — `README.md:173`

**Current:**
```
- **TinyGo 0.33.0** (LLVM 18.1.2) — install from the official `.deb` at <https://github.com/tinygo-org/tinygo/releases>
```

**After:**
```
- **TinyGo 0.40.1** (LLVM 19 or later — verify with `tinygo version`) — install from the official `.deb` or tarball at <https://github.com/tinygo-org/tinygo/releases>
```

Replace LLVM version with whatever the 0.40.1 `.deb` actually ships (capture during M0 install). Do not leave "18.1.2" — it is false after the migration.

### Edit 2 — `## User-writable TinyGo copy + runtime patches` section (original line 188 heading)

Rewrite to reference 0.40.1. Apply these grep-replace rules over the entire section (everything under that heading until the next `##` heading):

1. `~/.local/tinygo/` → `~/.local/tinygo0.40.1/` — every occurrence. Expected sites include bash `mkdir`/`cp`/`rm` instructions and the sentence "system TinyGo at `/usr/local/lib/tinygo/`".
2. `/usr/local/lib/tinygo/` → `/usr/local/lib/tinygo0.40.1/` — **but verify at M0** that the 0.40.1 `.deb` actually installs to this path. If the install path differs, use whatever path M0's `dpkg -L tinygo` step reveals.
3. `TinyGo 0.33.0 tree` → `TinyGo 0.40.1 tree` — single occurrence in the phrase "pristine TinyGo 0.33.0 tree".
4. `runtime/scheduler.go` (as a file-path reference inside the patched-files bullet list) → `runtime/scheduler_cooperative.go` for Wave 1. (Wave 2 edits this bullet again — see Edit 15.)

Also update bullets under the "The patch installs:" list where file paths changed. Expected affected bullets:
- `runtime/scheduler.go (patched in place)` — reword to `runtime/scheduler_cooperative.go (patched in place — this file was named scheduler.go in 0.33.0)`.

After applying, re-read the section end-to-end and confirm no residual `0.33.0` or bare-`.local/tinygo` path appears.

### Edit 3 — `README.md:252–259` (Reverting subsection)

Update every `~/.local/tinygo/src/` path to `~/.local/tinygo0.40.1/src/`.

Add a line for the new file introduced in the current patch (`runtime/wait_gooos.go` already listed at 256; no new file added at Wave 1).

### Edit 4 — `README.md:387`

**Current:**
```
`~/.local/tinygo/src/runtime/runtime_gooos.go:sleepTicks`
```

**After:**
```
`~/.local/tinygo0.40.1/src/runtime/runtime_gooos.go:sleepTicks`
```

### Edit 5 — `README.md:3` (project tagline)

**Current:**
```
An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The kernel runs on **TinyGo's native goroutine runtime** (`scheduler=tasks`, `gc=conservative`) — service loops are plain `go func()` goroutines, IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace. Assembly is used only where the CPU demands it.
```

**After (Wave 1 — `scheduler=tasks` still):**
No edit. Wave 1 keeps `scheduler=tasks`.

### Edit 6 — `README.md:18` (Scheduler progress row)

No edit in Wave 1. Still `scheduler=tasks`. Wave 2 updates this row.

### Edit 7 — `README.md:21` (SMP progress row)

No edit in Wave 1. Still "Done (v2, BSP-only scheduling)". Wave 2 updates this row.

### Edit 8 — `README.md:400–403` (Known limitations)

No edit in Wave 1 — the limitation text ("AP-side iretq into Ring 3 triple-faults under investigation") is still true after Wave 1. Wave 2 (M3) removes the "APs enter the scheduler but only BSP runs goroutines" clause; Wave 2 at M4 removes the triple-fault clause.

### Edit 9 — `README.md:31` (verify-globals row)

No edit at Wave 1. The row already references the correct symbols (`runqueue`, `sleepQueue`, `timerQueue`). If the symbol name `runqueue` changes after the cores promotion in Wave 2 (becomes `runqueues`), update this row accordingly at Wave 2.

---

## Wave 2 Edits — Cores-Mode Promotion

Land after M3 Exit gate passes, with `src/target.json` flip (per `impldoc/toolchain_switch_plan.md §3 commit 9`).

### Edit 10 — `README.md:3`

Change `scheduler=tasks` reference:

**After (Wave 2):**
```
An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**. The kernel runs on **TinyGo's native multi-core goroutine runtime** (`scheduler=cores`, `gc=conservative`) — service loops are plain `go func()` goroutines distributed across CPUs by the scheduler and gooos's `stealWork` helper, IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace. Assembly is used only where the CPU demands it.
```

### Edit 11 — `README.md:18` (Scheduler row)

**Current:**
```
| Scheduler | Done | **TinyGo native goroutines** (`scheduler=tasks`). Cooperative; PIT IRQ drives `sleepTicks`. TSS.RSP0 updated per-Ring-3-goroutine via the `gooosOnResume` hook in the patched TinyGo runtime |
```

**After:**
```
| Scheduler | Done (multi-core) | **TinyGo native goroutines** (`scheduler=cores`). Cooperative within a CPU; LAPIC timer (100 Hz per CPU) drives `sleepTicks` and work distribution. Per-CPU runqueues with gooos `stealWork` round-robin peer scan. TSS.RSP0 updated per-Ring-3-goroutine via the `gooosOnResume` hook in the patched TinyGo runtime |
```

### Edit 12 — `README.md:21` (SMP row)

Depending on which milestones landed:

**If M3 only (kernel goroutines distributed; Ring-3 still BSP-only):**
```
| SMP | Done (v3, kernel goroutines on APs) | **Multi-processor scheduling.** TinyGo `scheduler=cores` + gooos `runqueues[17]` + work stealing. APs boot via INIT-SIPI-SIPI, enter the cores scheduler after `bspBootDone`, and run kernel goroutines under `-smp 4`. **Remaining issue:** Ring-3 user code triple-faults when stolen by an AP — kernel bug under investigation. See `impldoc/smp_deferred_and_known_issues.md §2.1`. |
```

**If M3 + M4 (full Ring-3 on APs):**
```
| SMP | Done (v3, full multi-core) | **Multi-processor scheduling.** TinyGo `scheduler=cores` + gooos `runqueues[17]` + work stealing. Both kernel and Ring-3 goroutines distributed across APs. LAPIC timer on every CPU. GC stop-the-world via `gcPauseCore` IPI. Verified under `-smp 4` / `-smp 8` / `-smp 16`. |
```

**If M3 + M5 without M4:**
Mix of the two above; Ring-3 limitation remains.

### Edit 13 — `README.md:400–403` (Known limitations)

Remove or modify the "SMP user-mode Ring-3 disabled" bullet based on milestones landed:

- If M4 landed: **remove the bullet entirely** (no longer a limitation).
- If M4 not landed: replace with:
  ```
  - **SMP Ring-3 on APs disabled.** Kernel goroutines run on APs, but Ring-3 user processes still run only on BSP (AP-side `iretq` triple-faults under investigation). See `impldoc/smp_deferred_and_known_issues.md §2.1`.
  ```

### Edit 14 — `README.md:306–309` ("Running the demos" SMP mention)

**Current:**
```
Multi-core (SMP):

make run-smp        # -smp 4 for 4 cores
```

**After (Wave 2):**
```
Multi-core (SMP):

make run-smp        # -smp 4 for 4 cores; goroutines distributed across all CPUs
```

Add a pointer:
```
See `impldoc/smp_scheduler_design.md` for per-CPU runqueue architecture and `impldoc/smp_milestones_and_verification.md` for the SMP test harness matrix.
```

### Edit 15 — `README.md:188–246` (toolchain-setup section)

Minor updates to reflect Wave 2 patch additions (`numCPU=17`, `lockFutex`, `gcPauseCore`). Add a bullet after line 225 (the existing task-queue bullet):

```
- **`runtime/scheduler_cores.go`** (patched in place) — per-CPU `runqueues[17]`, `stealWork` round-robin peer scan, `apScheduler()` entry for AP cores (file is the multi-core scheduler in 0.40.1; `scheduler_cooperative.go` is used for the `scheduler=tasks` intermediate build).
```

And under `runtime_gooos.go`:

```
- **`runtime/runtime_gooos.go`** (new) — kernel bodies for `sleepTicks`, `ticks`, `putchar`, `exit`, `abort`, the bare-metal `main` entry point, plus SMP linkname bodies `numCPU = 17`, `lockFutex`/`unlockFutex`, `gcPauseCore`, `currentCPU`.
```

### Edit 16 — New section or paragraph about SMP verification

After line 309 ("Running the demos" SMP paragraph), optionally add:

```
### SMP verification

Run the SMP-specific harnesses (added during the 0.40.1 migration):

bash scripts/test_smp_basic.sh          # kernel goroutine distribution across CPUs
bash scripts/test_smp_ring3.sh          # Ring-3 goroutines on APs (post-M4)
bash scripts/test_smp_gc_stress.sh      # GC stop-the-world under allocation pressure (post-M5)

See `impldoc/smp_milestones_and_verification.md` for the full matrix.
```

Only add entries for harnesses that actually exist at the time of the commit.

---

## Wave 2 "Nothing-Changed" Audit

After making every edit above, re-grep `README.md` for the string `0.33.0`. **Zero matches expected.** Re-grep for `.local/tinygo/` (without the version suffix). **Zero matches expected** (all should be `.local/tinygo0.40.1/`). Commit only after both greps are empty.

---

## Optional: Migration Note at Top of README

Briefly mention the toolchain version bump in a dedicated paragraph (say after line 172):

```
> **Toolchain note (2026-04):** gooos moved from TinyGo 0.33.0 to 0.40.1 to adopt upstream's `scheduler=cores` multi-core runtime. See `impldoc/smp_migration_overview.md` for the migration plan and rollback procedure.
```

Deferred decision — add only if the project maintainers want the migration visible on README's first screen. Default: do not add (`impldoc/` is the right home for migration history).

---

## Non-README Docs Not Updated Here

- `current_impl_doc/scheduler.md` — as-built reference; updates happen at the same Wave 2 cadence but are scoped to that file. Tracked as its own edit in the implementation session (not this migration plan).
- `current_impl_doc/known_issues.md` — update to remove resolved items at each milestone; tracked per-milestone, not batched here.
- `impldoc/smp_overview.md` and siblings — unchanged; this migration set extends them, not replaces them.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
