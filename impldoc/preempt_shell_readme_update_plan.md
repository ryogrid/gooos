# Preempt + Shell Batch — README & Doc Update Plan

## Scope

Anchor-text-based rewrite rules for every `README.md` row, `current_impl_doc/*.md` paragraph, `impldoc/smp_deferred_and_known_issues.md` section, and `TODO_SMP*.md` tick-box that drifts when any subset of features 2.1 / 2.2 / 2.3 / 2.4 / 2.5 lands. Closes the batch.

Extends, does not replace, `impldoc/smp_unblock_readme_update_plan.md` (the SMP-unblock batch's companion).

## Cross-links

- `preempt_shell_overview.md` — batch entry point.
- Per-feature: `preempt_kernel_goroutines.md` (2.1), `preempt_user_goroutines.md` (2.2), `shell_multicore_preempt.md` (2.3), `shell_background_jobs.md` (2.4), `shell_ps_command.md` (2.5).
- Prior template: `impldoc/smp_unblock_readme_update_plan.md`.

## 1. Principle — grep-replace over line-number edits

Line numbers in long README paragraphs rot fast (each table-row edit shifts every row below). This plan targets **stable anchor strings** that uniquely identify the fragment to replace. Every edit rule has the form:

```
Anchor: "<stable substring>"
Match: (describe where the anchor appears — row / section / paragraph)
Replace (variant A — full landing): <text>
Replace (variant B — partial landing): <text>
Replace (variant C — feature deferred): <text>
```

Apply with `grep -n '<anchor>' <file>` to confirm uniqueness, then `sed` or Edit-tool replacement. If the anchor matches multiple times, narrow by adding more surrounding context; the plan shows the minimal uniquifier at authoring time — regenerate if the doc has drifted since.

## 2. Target: `README.md`

### 2.1 Tagline (§Project tagline, ~L3)

**Anchor** (as of `smp-take4` HEAD): `live multi-core work-stealing`. Uniquely appears in the tagline sentence.

No changes required when **only** 2.3 / 2.4 / 2.5 land — the tagline is scheduler-focused.

If 2.1 lands: append "— goroutines are now **preemptible** via a BSP-timer + IPI-broadcast mechanism (see SMP row and `impldoc/preempt_shell_overview.md`)". Keep the sentence under 2 lines of Markdown.

### 2.2 Progress table — Scheduler row

**Anchor**: first table cell `Scheduler | Done |`.

- **Variant A (2.1 lands alone OR 2.1 + 2.2 both land)**: replace `"Done"` status cell with `"Done (preemptive)"`. Replace description cell with:
  ```
  **TinyGo native goroutines** (`scheduler=cores` — multi-core,
  per-CPU runqueues + work-stealing, BSP-timer-driven preemption
  via IPI broadcast). APs yield at IPI arrival boundaries. TSS.RSP0
  updated per-Ring-3-goroutine via the `gooosOnResume` hook. See
  `impldoc/preempt_kernel_goroutines.md` [and `preempt_user_goroutines.md`
  if 2.2 also landed].
  ```
- **Variant B (2.1 does not land in this batch)**: no change to Scheduler row. Preemption landing shifts to a future batch.

### 2.3 Progress table — SMP row

**Anchor**: `SMP | Done (v2 on TinyGo 0.40.1, multi-core \`scheduler=cores\`)`.

The row currently describes live work-stealing + the AP LAPIC timer deferral. Edits depend on which features land:

- **Variant A — 2.1 lands (kernel preemption added)**: inside the existing SMP description, replace the sentence `"an AP running a compute-bound goroutine can only be unstuck by a cooperative yield or channel op"` with `"APs are preempted via BSP-timer-driven IPI broadcast (vector 0xFB); AP-local LAPIC timer remains deferred (see impldoc/preempt_kernel_goroutines.md §Future)"`.
- **Variant B — 2.1 does not land**: no change.

### 2.4 Progress table — Shell row

**Anchor**: the row starting with `BusyBox-style shell`.

- **Variant A — 2.4 lands (`&` / background jobs)**: append to the description cell: `"; background execution via \`&\` with a 16-slot jobs table (\`impldoc/shell_background_jobs.md\`)"`.
- **Variant B — 2.5 lands (`ps`)**: in the external-ELF enumeration (the parenthesized list that includes `smpprobe`), insert `ps, ` alphabetically. Append to the description cell: `"; \`ps\` command lists processes via \`sys_listprocs\` (\`impldoc/shell_ps_command.md\`)"`.
- **Variant C — both 2.4 and 2.5 land**: apply both A and B additions.

### 2.5 Progress table — Syscall ABI row

**Anchor**: `34-syscall register-based dispatch`.

Updated post-reviewer-CRITICAL-#1 resolution: syscall numbers are 34 (sys_waitpid), 35 (sys_sigaction), 36 (sys_sigreturn), 37 (sys_listprocs). Four new syscalls if all features land.

- **Variant A — 2.4 + 2.2 + 2.5 all land (adds #34, #35, #36, #37)**: update "34-syscall" to "38-syscall". In the base-set enumeration, append `, sys_waitpid, sys_sigaction, sys_sigreturn, sys_listprocs`. Also update the **Userspace row** anchor `"34 syscalls"` to `"38 syscalls"` (the number appears twice in the README — once in each row).
- **Variant B — only 2.4 lands (+1 syscall)**: `"34-syscall"` → `"35-syscall"`; append `sys_waitpid`.
- **Variant C — only 2.2 lands (+2 syscalls)**: `"34-syscall"` → `"36-syscall"`; append `sys_sigaction, sys_sigreturn`.
- **Variant D — only 2.5 lands (+1 syscall)**: `"34-syscall"` → `"35-syscall"`; append `sys_listprocs`.
- **Variant E — subset combinations**: sum appropriately.

### 2.6 Progress table — Userspace row

**Anchor**: `Ring 3 execution via \`iretq\`` in the Userspace row's description.

- **Variant A — 2.2 lands**: append to the description cell: `". User goroutines within one process are preemptible via a kernel-delivered SIGALRM-style signal (\`impldoc/preempt_user_goroutines.md\`)."`.
- **Variant B — 2.2 does not land**: no change.

### 2.7 Known limitations — "Shell does not support job control" bullet

**Anchor**: `Shell does not support job control` (L406 in current HEAD).

- **Variant A — 2.4 lands**: replace the entire bullet. New text:
  ```
  - **Shell job control is minimal.** `&` background execution
    is supported with a 16-slot jobs table
    (`impldoc/shell_background_jobs.md`), but `jobs`/`fg`/`bg`
    built-ins, Ctrl-C, and signal handling are deferred.
  ```
- **Variant B — 2.4 does not land**: no change.

### 2.8 Known limitations — "SMP user-mode Ring-3 disabled" bullet (STALE as of smp-take4)

**Anchor**: `SMP user-mode Ring-3 disabled` (L416 in current HEAD).

This bullet is **already stale** — the M4 fix landed at commit `5aea173` (well before this batch). It should have been removed in the `b481473 docs(smp): refresh post-unblock state` commit and was missed. **This batch removes it unconditionally**, regardless of which 2.x features land.

Replace with: *delete the bullet*.

Record the removal in the overview's `§Reviewer findings` as a pre-existing drift fixed in passing.

### 2.9 New Known-limitation: "No preemption" not present in README today

The README does **not** have a standalone "no preemption" bullet today — the point is embedded inside the SMP row. If 2.1 does not land, no new bullet is added. If 2.1 lands, the SMP-row edit in §2.3 handles the removal of the cooperative-yield caveat.

### 2.10 Audit greps post-edit

After applying the conditional edits, verify:

```
grep -n 'scheduler=tasks' README.md           # expect 0 kernel-side references
grep -n 'SMP user-mode Ring-3 disabled' README.md # expect 0
grep -n 'No \`&\` background jobs' README.md     # expect 0 if 2.4 landed
grep -n 'cooperative yield or channel op' README.md # expect 0 if 2.1 landed
grep -cE '^\| [A-Z]' README.md                  # expect UNCHANGED row count (no rows added/removed)
```

## 3. Target: `current_impl_doc/scheduler.md`

### 3.1 SMP v2 subsection extension (post-`dc58dbc`)

**Anchor**: `### SMP v2 (current, post-M3 unblock landing, 2026-04-20)`.

Structure (post-`dc58dbc`): subsections "In one line", "How it works", "How to verify", "The remaining constraint: AP preemption", "Related docs".

- **Variant A — 2.1 lands**: in "The remaining constraint: AP preemption" subsection, rewrite to reflect that preemption is now live via BSP timer + IPI broadcast. Rename the subsection heading to `#### Preemption (live)` and update body to describe vector 0xFB + the `## Future: per-CPU AP timer` section in `impldoc/preempt_kernel_goroutines.md`.
- **Variant B — 2.1 does not land**: no change.

### 3.2 Standalone "Preemption (or lack thereof)" section (~L147)

**Anchor**: `## Preemption (or lack thereof)` at L147.

- **Variant A — 2.1 lands**: rewrite section title to `## Preemption`. Rewrite body to describe kernel preemption (2.1) + user preemption (2.2, if landed). Retain the reference to cooperative yield points as fallback. Reference impldoc set.
- **Variant B — 2.1 does not land**: no change.

### 3.3 Append new subsection "Ring-3 signal delivery" (2.2 only)

If 2.2 lands, add a new subsection after "Ring-3 Wrapper Lifecycle" documenting the mechanism-B signal path (kernel rewriting the iretq frame). Cross-link `impldoc/preempt_user_goroutines.md`.

## 4. Target: `current_impl_doc/known_issues.md`

### 4.1 Mindmap (~L20 — `no preemption` node)

**Anchor**: `no preemption` under the Kernel/Runtime mindmap node.

- **Variant A — 2.1 lands**: delete the `no preemption` node and its child `cpu-bound goroutine starves others`. Optionally add a new `preemption granularity: 10ms` node documenting the quantum.
- **Variant B — 2.1 does not land**: no change.

### 4.2 Mindmap — "no job control" node under Shell (~L44)

**Anchor**: `no job control` under Shell subtree.

- **Variant A — 2.4 lands**: rename node to `partial job control`, add child `no fg/bg/jobs builtins yet`.
- **Variant B — 2.4 does not land**: no change.

### 4.3 Kernel Active Limitations table — "No preemption" row (~L61)

**Anchor**: `| No preemption | CPU-bound goroutine starves the scheduler |`.

- **Variant A — 2.1 lands**: remove the row entirely.
- **Variant A' — 2.1 + 2.2 both land**: remove the row and replace with `| Preemption granularity 10 ms | A 10 ms sleeping-sibling blackout is possible under contention | \`impldoc/preempt_shell_overview.md\` |`.
- **Variant B — 2.1 does not land**: no change.

### 4.4 Kernel Active Limitations table — "SMP v1: APs halt after boot" row (~L60, STALE)

This row is **stale** as of `smp-take4` and should have been removed at `b481473`. This batch removes it unconditionally.

Replace with: delete the row.

### 4.5 Shell & I/O Active Limitations table — "No `&` / `fg` / `bg`" row (~L83)

**Anchor**: `| No \`&\` / \`fg\` / \`bg\` |`.

- **Variant A — 2.4 lands**: rewrite to `| No \`fg\` / \`bg\` builtins | foreground/background job switching deferred | \`impldoc/shell_background_jobs.md §11.3\` |` (keeping a narrower version of the same limitation).
- **Variant B — 2.4 does not land**: no change.

### 4.6 Add new row to Shell & I/O table for `ps` landing

- **Variant A — 2.5 lands**: No new limitation row (2.5 is a feature addition, not a limitation). Instead, add a new positive-capability mention in the passive-voice comment at the top of the Shell section: `"(ps command available via impldoc/shell_ps_command.md)"`.

## 5. Target: `impldoc/smp_deferred_and_known_issues.md`

### 5.1 §2.2 AP LAPIC timer — status update if 2.1 lands

**Anchor**: the PARTIAL status header at §2.2.

- **Variant A — 2.1 lands via BSP+IPI (the chosen path)**: add a `## Update (<date>)` paragraph noting that feature 2.1 explicitly does NOT unblock the AP LAPIC timer; preemption is BSP-timer-driven instead. The AP LAPIC timer remains a deferred future item. Cross-link `impldoc/preempt_kernel_goroutines.md §Future`.
- **Variant B — 2.1 does not land**: no change.

### 5.2 §5 or wherever "No preemption" is documented

Grep `impldoc/smp_deferred_and_known_issues.md` for `preempt` — if any caveat says "preemption is deferred" or similar, update to reflect 2.1's landing (or lack thereof).

## 6. Targets: `TODO_SMP3.md` / `TODO_SMP4.md` / new `TODO_SMP5.md`

### 6.1 `TODO_SMP3.md` / `TODO_SMP4.md`

These files track the *prior* SMP batch. They do **not** need ticks for this batch's features; leave them as-is. If §7 of this doc (the Stop Conditions) finds pre-existing stale items, flag them in the overview's `§Reviewer findings`.

### 6.2 `TODO_SMP5.md` (NEW — tracker for THIS batch)

Create at project root, modeled on `TODO_SMP4.md`. Structure:

```
# TODO_SMP5 — Preempt + Shell Enhancements

## Feature 2.1 — Kernel goroutine preemption
- [ ] 2.1-1. PreemptDisable field + offset const (src/percpu.go)
       Verify: make build clean
       Commit: feat(smp): ...
- [ ] 2.1-2. Spinlock preemptDisable integration
       Verify: make build; existing regression green
       Commit: feat(smp): ...
...

## Feature 2.2 — User goroutine preemption
- [ ] 2.2-1. PCB signal fields
...

## Feature 2.3 — Multi-core shell scheduling + preempt verification
- [ ] 2.3-1. cpuhog.elf + markerprint.elf
...

## Feature 2.4 — Shell & background execution
- [ ] 2.4-1. sys_waitpid #34 handler + dispatch
...

## Feature 2.5 — ps command + sys_listprocs
- [ ] 2.5-1. Process.LastCpuID field
...

## Deferred further
- AP LAPIC timer (`impldoc/smp_deferred_and_known_issues.md §2.2`) — remains disabled by design (2.1 chose BSP+IPI path).
- Shell job control builtins (`fg`, `bg`, `jobs`) — deferred past 2.4.
- Wildcard waitpid (`pid == -1`) — deferred past 2.4.
- `ps` flags (`-e`, `-u`, `-f`) — deferred past 2.5.
- SMP-safe GC (M5 of prior batch) — still deferred.

## Reviewer findings
*Populated during the mandatory reviewer pass.*
```

One checkbox per commit in each feature doc's §Commit-per-edit Plan (8 + 8 + 4 + 5 + 4 = 29 feature checkboxes, plus doc and review passes).

## 7. Ordering

Apply the edits in this order to avoid merge noise:

1. Feature 2.1 commits land (8). Apply §2.2 / §2.3 / §3.1 / §3.2 / §4.1 / §4.3 / §5.1 per "2.1 lands" variants.
2. Feature 2.2 commits land (8). Apply §2.4 (if only 2.2) / §2.5 / §2.6 / §3.3 / §4.1 (variant A') additions.
3. Feature 2.3 commits land (4). No README drift — sub-gate harnesses are test scripts; drift is tracked only in `TODO_SMP5.md`.
4. Feature 2.4 commits land (5). Apply §2.4 (Shell row) / §2.5 / §2.7 / §4.2 / §4.5 per "2.4 lands" variants.
5. Feature 2.5 commits land (4). Apply §2.4 (Shell row ps mention) / §2.5 / §4.6.
6. Batch-closing commit: the stale-removal edits (§2.8, §4.4) — unconditional — plus consolidation if multiple variants composed.

## 8. Commit plan for THIS update-plan's edits

One `docs(readme): refresh progress / known-issues / impldoc drift for preempt+shell batch` commit **at the end of the batch**, grouping all variant edits decided by which features landed. If multiple features in this batch are landed in separate sub-sessions, each can commit its own `docs(readme): … for feature 2.x` — the overview's Design Decisions section records the actual cadence.

## 9. Verification (audit greps after edit pass)

```
# Scheduler row shows preemption (if 2.1 landed)
grep -n 'preemptive' README.md

# SMP user-mode Ring-3 bullet removed (unconditional)
grep -n 'SMP user-mode Ring-3 disabled' README.md    # expect 0

# Shell `&` bullet updated (if 2.4 landed)
grep -n 'does not support job control' README.md     # expect 0 if 2.4 landed

# Syscall count matches actual (35/36/37 depending on which subset landed)
grep -cE '^\| [A-Z]' README.md                       # expect UNCHANGED row count

# known_issues.md stale SMP v1 row removed
grep -n 'SMP v1: APs halt' current_impl_doc/known_issues.md  # expect 0

# scheduler.md preemption section reflects landing
grep -n '## Preemption (or lack thereof)' current_impl_doc/scheduler.md  # expect 0 if 2.1 landed

# TODO_SMP5.md exists and tracks all 5 features
ls TODO_SMP5.md && grep -cE '^## Feature 2\.' TODO_SMP5.md  # expect 5
```

Any grep returning nonzero when it should be zero is a stop condition: investigate the missed anchor, update the edit rule here, re-run.

## 10. Deliverables

- 1 `docs(readme): …` commit (or N, if split per-feature) at the end of the batch.
- Updated: `README.md`, `current_impl_doc/scheduler.md`, `current_impl_doc/known_issues.md`, `impldoc/smp_deferred_and_known_issues.md`, new `TODO_SMP5.md`.
- Nothing under `src/` / `user/` / `scripts/` changes from this doc.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
