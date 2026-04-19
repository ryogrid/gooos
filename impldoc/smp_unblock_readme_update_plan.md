# SMP Unblock — README & Documentation Update Plan

**Scope.** Concrete grep-replace rules for `README.md`, `current_impl_doc/scheduler.md`, `impldoc/smp_deferred_and_known_issues.md`, and `TODO_SMP3.md` that an implementation agent lands **as the closing step** once at least M3 of this batch is green. The rules are conditional on which of M2 / M3 / M4 actually land.

**Cross-links.**
- Batch overview: `impldoc/smp_unblock_overview.md`.
- Milestones: `impldoc/smp_unblock_milestones_and_verification.md`.
- Per-milestone designs: `impldoc/smp_m2_ap_lapic_timer.md`, `impldoc/smp_m3_cores_promotion.md`, `impldoc/smp_m4_ring3_fault.md`.
- Prior Wave-2-deferred plan this supersedes for M2/M3/M4 scope: `impldoc/readme_update_plan.md`.

---

## 1. Principle: grep-replace over line-number edits

Line numbers in `README.md` and the other docs drift every time the file is edited. The prior review of the 0.33.0 → 0.40.1 migration docs flagged ~5 off-by-one citations caused by relying on absolute line numbers across commit boundaries. Therefore:

- Every rule below targets a **stable string** (a Markdown section heading, a distinctive phrase, or a bullet lead) — not a line number.
- Absolute line numbers shown are **pre-batch snapshot** (current `smp-take3` tip, `2a1a13d`) and are included only as a reading aid. After the first edit in a file, re-locate subsequent rules by their anchor text.

---

## 2. `README.md`

File length at batch entry: **441 lines**. Reference:

```
grep -n '\(TinyGo\|scheduler=\|SMP\|stealWork\|Known limitations\)' README.md
```

### 2.1 Project tagline (line ~3)

**Anchor:** `An experimental x86_64 operating system written in **Go (TinyGo) + GNU assembly**.`

**Condition:** Apply when M3 lands (regardless of M2 / M4 status).

**Current:**
```
service loops are plain `go func()` goroutines, IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace.
```

**Rewrite (if M3 + M4 both land):**
```
service loops are plain `go func()` goroutines distributed across CPUs by TinyGo's `scheduler=cores` runtime + gooos's `stealWork` peer-scan, IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace.
```

**Rewrite (if M3 lands with kernel-only-affinity fallback; M4 deferred):**
```
service loops are plain `go func()` goroutines distributed across CPUs by TinyGo's `scheduler=cores` runtime + gooos's kernel-only `stealWork` (Ring-3 wrappers pinned to BSP pending M4 resolution), IPC is Go's built-in `chan`, and Ring 3 processes are goroutines that `iretq` into userspace.
```

**Rewrite (if only M2 lands, M3 / M4 deferred):** no change — Wave 1 state is preserved.

### 2.2 Scheduler progress row

**Anchor:** the row in the Progress table whose first column is `Scheduler`.

**Current:**
```
| Scheduler | Done | **TinyGo native goroutines** (`scheduler=tasks`). Cooperative; PIT IRQ drives `sleepTicks`. TSS.RSP0 updated per-Ring-3-goroutine via the `gooosOnResume` hook in the patched TinyGo runtime |
```

**Rewrite (if M3 lands):**
```
| Scheduler | Done (multi-core) | **TinyGo native goroutines** (`scheduler=cores`). Cooperative within each CPU; LAPIC timer (100 Hz per CPU per M2, or BSP-only if M2 deferred) drives `sleepTicks`. Per-CPU runqueues with gooos `stealWork` round-robin peer scan. TSS.RSP0 updated per-Ring-3-goroutine via the `gooosOnResume` hook in the patched TinyGo runtime |
```

**Rewrite (if only M2 lands, M3 deferred):** no change in this row.

### 2.3 SMP progress row

**Anchor:** the row in the Progress table whose first column is `SMP` (currently reads `Done (v2 on TinyGo 0.40.1, BSP-only scheduling)`).

**Current:** (Wave 1 state documented at `smp-take3` tip)
```
| SMP | Done (v2 on TinyGo 0.40.1, BSP-only scheduling) | **Multi-processor infrastructure.** ... `stealWork` helper is shipped in the patched runtime but **intentionally not called** from the scheduler's pop site; enabling it triggers the AP Ring-3 triple-fault below. **Remaining issue:** Ring-3 user code triple-faults when stolen by an AP ... All goroutines currently run on BSP (CPU 0). The migration to TinyGo 0.40.1 + `scheduler_cooperative.go` preserves this contract; promotion to `scheduler=cores` (with live stealWork) is deferred to `TODO_SMP3.md` M3 after the Ring-3 AP fault is resolved. See `impldoc/smp_deferred_and_known_issues.md` and `impldoc/smp_migration_overview.md` for details |
```

**Rewrite (if M2 + M3 + M4 all land):**
```
| SMP | Done (v3 on TinyGo 0.40.1, multi-core scheduling) | **Multi-processor scheduling.** TinyGo `scheduler=cores` with gooos per-CPU runqueues + `stealWork` round-robin peer scan. APs enter the scheduler after `bspBootDone` gating, steal kernel and Ring-3 goroutines from peer runqueues, and idle in `waitForEvents` between steals. LAPIC timer (100 Hz per CPU) drives preemption. Verified under `-smp 4` / `-smp 8` / `-smp 16`. See `impldoc/smp_unblock_overview.md` for the milestone trail. |
```

**Rewrite (if M3 + M4 land, M2 deferred — most likely outcome):**
```
| SMP | Done (v3 on TinyGo 0.40.1, multi-core scheduling; BSP-only LAPIC timer) | **Multi-processor scheduling.** TinyGo `scheduler=cores` with gooos per-CPU runqueues + `stealWork` round-robin peer scan. APs enter the scheduler after `bspBootDone` gating and steal kernel + Ring-3 goroutines from peer runqueues. Only BSP runs a LAPIC timer (100 Hz); per-AP LAPIC timer pending M2 fix of the ISR dual-counter race (see `impldoc/smp_deferred_and_known_issues.md §2.2`). Verified under `-smp 4` / `-smp 8` / `-smp 16`. See `impldoc/smp_unblock_overview.md`. |
```

**Rewrite (if M2 + M3 land, M4 fallback):**
```
| SMP | Done (v3 on TinyGo 0.40.1, kernel multi-core + Ring-3 on BSP) | **Multi-processor scheduling.** TinyGo `scheduler=cores` with gooos per-CPU runqueues + `stealWork` round-robin peer scan (kernel-only affinity). APs run kernel goroutines; Ring-3 wrappers are pinned to BSP pending M4 resolution of the AP `iretq` triple-fault. LAPIC timer (100 Hz per CPU). See `impldoc/smp_unblock_overview.md`. |
```

**Rewrite (if M3 lands with kernel-only-affinity fallback; M4 + M2 both deferred):**
```
| SMP | Done (v3 on TinyGo 0.40.1, kernel multi-core + Ring-3 on BSP; BSP-only LAPIC timer) | **Multi-processor scheduling.** TinyGo `scheduler=cores` with gooos per-CPU runqueues + `stealWork` round-robin peer scan (kernel-only affinity). APs run kernel goroutines; Ring-3 wrappers pinned to BSP pending M4. Only BSP runs a LAPIC timer (100 Hz); per-AP LAPIC timer pending M2. See `impldoc/smp_unblock_overview.md`. |
```

**Rewrite (if only M2 lands):** append `AP LAPIC timer at 100 Hz landed (M2); ` to the existing description, nothing else changes.

### 2.4 "Running the demos" SMP section

**Anchor:** the subsection `Multi-core (SMP):` that contains `make run-smp        # -smp 4 for 4 cores`.

**Condition:** Apply when M3 lands.

**Rewrite (add a cross-link paragraph):**
```
Multi-core (SMP):

make run-smp        # -smp 4 for 4 cores; goroutines distributed across all CPUs

See `impldoc/smp_unblock_overview.md` and `impldoc/smp_milestones_and_verification.md` for the per-CPU runqueue architecture and the SMP test-harness matrix.
```

### 2.5 Known limitations — "SMP user-mode Ring-3 disabled" bullet

**Anchor:** the bullet under `## Known limitations` starting with `**SMP user-mode Ring-3 disabled.**`.

**Condition:** Apply when M4 lands. If M4 is still deferred, keep the bullet but update the rationale.

**Current:**
```
- **SMP user-mode Ring-3 disabled.** APs boot and enter the scheduler but only BSP (CPU 0) runs goroutines for now; AP-side `iretq` into Ring 3 triple-faults under investigation. See `impldoc/smp_deferred_and_known_issues.md`.
```

**Action (M4 resolved):** **Delete the bullet entirely.**

**Action (M4 deferred but M3 landed via fallback):** replace with:
```
- **SMP Ring-3 on APs disabled (fallback mode).** Kernel goroutines run on APs, but Ring-3 wrappers are pinned to BSP pending M4 resolution of the AP `iretq` triple-fault. See `impldoc/smp_deferred_and_known_issues.md §2.1`.
```

### 2.6 New SMP-verification section (optional)

**Condition:** Apply when M3 lands AND `scripts/test_smp_basic.sh` / `scripts/test_smp_ring3.sh` exist.

After the existing `Multi-core (SMP):` subsection, append:

```
### SMP verification

Run the SMP-specific harnesses:

bash scripts/test_smp_basic.sh          # kernel goroutine distribution across CPUs (M3)
bash scripts/test_smp_ring3.sh          # Ring-3 goroutines on APs (M4)

Optional stress: rerun existing harnesses with `-smp 4`:

SMP=4 bash scripts/test_tcp_phase5.sh

See `impldoc/smp_unblock_milestones_and_verification.md §Harness Extension Plan`.
```

Only add if the harnesses referenced exist at the commit boundary — do not reference vapour.

### 2.7 Toolchain-setup section (no change expected)

**Anchor:** `## User-writable TinyGo copy + runtime patches (required)` heading.

Already reflects `~/.local/tinygo0.40.1/` and `scheduler_cooperative.go`. The Wave 2 patch additions (numCPU, spinlock vars, scheduler_cores.go retargeting) do **not** change the high-level user setup. Keep as-is. (If the list of patched files in that section grows stale after M3, add `runtime/scheduler_cores.go` alongside the existing `runtime/scheduler_cooperative.go` bullet.)

### 2.8 Audit greps post-edit

After all applicable rules land:

```
grep -n 'scheduler=tasks' README.md     # expect 0 matches if M3 landed
grep -n '0\.33\.0' README.md             # expect only historical "was named X in 0.33.0" lines
grep -n '\.local/tinygo/' README.md      # expect 0 matches (legacy path)
grep -n 'BSP-only' README.md             # expect 0 matches if M3 landed
```

Commit only after all four grep results match expectation.

---

## 3. `current_impl_doc/scheduler.md`

**Anchor:** the paragraph in `## SMP v2 (current, post-TinyGo-0.40.1 migration)` subsection.

**Current (written during C-4 of the 0.40.1 migration):**
```
- `stealWork()` exists in the patched runtime but **intentionally not called** from the scheduler's pop site. Wiring it triggers the Ring-3 `iretq` triple-fault on APs (see `impldoc/smp_deferred_and_known_issues.md §2.1`); enabling it is tracked as `TODO_SMP3.md` milestone M3.
- Only BSP runs a LAPIC timer (100 Hz). Enabling per-AP LAPIC timers hits a separate ISR-depth race captured as M2 in `TODO_SMP3.md` and `impldoc/smp_deferred_and_known_issues.md §2.2`.
...
Net effect: all goroutines currently execute on BSP (CPU 0).
```

**Rewrite (if M3 + M4 + M2 all land):**
```
- `stealWork()` runs from the scheduler's pop site; APs steal both kernel and Ring-3 goroutines from peer runqueues.
- Every CPU runs its own LAPIC timer at 100 Hz (M2 per-CPU counter migration complete; `gooos_in_interrupt_depth` retired in favour of `%gs:4` + `%gs:12` syscall-depth pair).
- Ring-3 wrappers execute on APs (M4 root cause fixed; see commit `<hash>` and `impldoc/smp_deferred_and_known_issues.md §2.1 Resolved`).
...
Net effect: goroutines execute across all available CPUs.
```

**Rewrite (partial-landing variants):** mirror the Scheduler progress row logic from §2.2 above.

### 3.1 Audit grep

```
grep -n 'intentionally not called\|BSP only\|BSP-only' current_impl_doc/scheduler.md
# Expect 0 matches if M3 landed (or explicitly historical context only).
```

---

## 4. `impldoc/smp_deferred_and_known_issues.md`

### 4.1 §2.1 — Ring-3 Triple Fault on APs

**Condition:** M4 resolved.

Prepend a **"## 2.1 Ring-3 Triple Fault on APs — Resolved"** marker:

```
### 2.1 Ring-3 Triple Fault on APs (RESOLVED <YYYY-MM-DD>, commit <hash>)

**Root cause (confirmed):** <hypothesis row (a-e) that matched evidence>.

**Fix:** <one-line summary pointing to the commit>.

**Historical investigation** (kept for archival):
<original symptom/hypothesis/workaround text here>
```

The goal: preserve the historical writeup (other docs cross-link into it) but prepend a clear "Resolved" banner.

### 4.2 §2.2 — AP LAPIC Timer Race

**Condition:** M2 resolved.

Same pattern as §2.1: prepend "Resolved" banner + root-cause confirmation + commit hash. Preserve historical text.

### 4.3 §5 — TinyGo Runtime SMP Gaps

**Condition:** M3 landed.

Edit the "Work stealing" gap row from "dormant" to "active":

**Before (current post-migration):**
```
| Work stealing | **Dormant** | TinyGo `scheduler_cooperative.go`: `stealWork()` function exists but is **not called** from the scheduler's pop site. Wiring it triggers the Ring-3 AP triple-fault below. Enabling deferred to `TODO_SMP3.md` M3 |
```

**After (M3 lands):**
```
| Work stealing | Done | TinyGo `scheduler_cores.go`: `stealWork()` wired into the pop-on-nil path; round-robin peer scan. APs steal kernel (and Ring-3 if M4 landed) goroutines from peer runqueues. |
```

Update the patch-size line too (current: "~800 lines post-migration"; after M3: recompute via `wc -l scripts/tinygo_runtime.patch`).

---

## 5. `TODO_SMP3.md`

Tick the milestone items that landed. The current file has M2 / M3 / M4 / M5 deferred with **strike-through `~~…~~` markdown** around the bullet body plus a trailing `(deferred)` tag. For each landed milestone, apply two edits per bullet:

1. **Flip the checkbox:** `- [ ]` → `- [x]`.
2. **Remove the `~~` markers** around the bullet title and drop the trailing `(deferred)` text; append `(commit <hash>)` instead.

Concrete before/after for one bullet:

**Before:**
```
- [ ] ~~**M2-1. Migrate `interrupt.In()` to read per-CPU `%gs:4` counter only**~~ (deferred)
```

**After:**
```
- [x] **M2-1. Migrate `interrupt.In()` to read per-CPU `%gs:4` counter only** (commit abc1234)
```

Trim the "Deferred further" tail: delete the paragraphs that describe the M2 / M3 / M4 deferral rationale once those milestones land. Keep M5 and any remaining deferrals.

Add a new "Reviewer findings" section at the end of the file referencing the reviewer pass run for the M2/M3/M4 batch.

---

## 6. Ordering

Apply the edits in this order (so cross-references remain valid at each intermediate state):

1. **`impldoc/smp_deferred_and_known_issues.md`** first — downstream docs point at §2.1 / §2.2 / §5, so update them before the README row rewrites cite them.
2. **`current_impl_doc/scheduler.md`** next — it is read by README's cross-link, so fix before README grep-replace.
3. **`README.md`** — apply §2.1-2.6 rules in grep-replace mode; audit with §2.8 greps.
4. **`TODO_SMP3.md`** — ticks last so the commit history shows the milestone complete at the last step.

---

## 7. Commit Plan for This Batch

Single `docs(…): …` commit per file is acceptable; grouping into one `docs: M2/M3/M4 results` commit is also acceptable as long as the audit greps in §2.8 pass at commit time.

Example commit sequence:
1. `docs(smp): mark M4 resolved in smp_deferred_and_known_issues §2.1`
2. `docs(smp): mark M2 resolved in smp_deferred_and_known_issues §2.2`
3. `docs(smp): update §5 work-stealing row to Done`
4. `docs(impl): refresh current_impl_doc/scheduler.md SMP paragraph`
5. `docs(README): multi-core SMP scheduling status`
6. `docs(smp): tick M2/M3/M4 in TODO_SMP3 + trim Deferred tail`
7. `docs(smp): reviewer findings for M2/M3/M4 batch`

All local; **no `git push`**; no branch operations; no `master` merges without user order.

---

## 8. Verification

- `grep -n 'scheduler=tasks' README.md` → 0 matches (if M3 landed).
- `grep -n '0\.33\.0' README.md impldoc/ current_impl_doc/` → only historical context lines (explicitly "was named X in 0.33.0").
- `grep -rnE 'TODO|FIXME|XXX' src/ user/ scripts/` → no new markers vs. the pre-batch baseline.
- `git diff master -- README.md` reviewed by eye: every Progress-table row matches the post-batch reality.
- `TODO_SMP3.md` — grep for unchecked items in the M2/M3/M4 sections → 0 (unless explicitly deferred with rationale in a new "Deferred further" entry).

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
