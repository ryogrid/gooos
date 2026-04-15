# Claude Code Plan-Mode Prompt: Execute Phase B — Retire the Hand-written Scheduler and Channel APIs

You are entering plan mode to **implement and verify** the design
already written in `impldoc/phase_b_*.md`. Read those five documents
first — they are the authoritative specification. Your job is to
produce working code, not to redesign.

## The authoritative design

Read in order before planning:

1. `impldoc/phase_b_overview.md` — dependency DAG, implementation
   ordering, updated risk register, B1 close-out, coverage table
   mapping each deferred item (B1, B3–B11) to its home section.
2. `impldoc/phase_b_channel_migrations.md` — B3 (serialChannel
   retirement, confirmed dead code) and B4 (fsRequestChannel →
   native `chan`).
3. `impldoc/phase_b_keyboard_irq.md` — B5 ring-buffer + pump
   goroutine; x86-TSO ordering proof across all four
   writer/reader pairings; `//go:nosplit` enforcement.
4. `impldoc/phase_b_ring3_and_exec.md` — B9 `elfExec` →
   `ring3Wrapper` + `exitCh`; TSS.RSP0 side-table with
   `gooosOnResume` goroutine-switch hook.
5. `impldoc/phase_b_teardown.md` — B6 fatal handlers
   (`serialPanicPrint`), B7 `createTask`→`go`, B8 delete
   `src/scheduler.go`, B10 delete `src/channel.go`, B11 SMP AP
   idle loop.

Also read `CLAUDE.md` for project workflow rules, the current
`TODO.md` for the Deferred section each item derives from, and
`impldoc/goroutine_design_*.md` for Phase-A context.

Phase A state assumed:
- `scheduler=tasks` is live in `src/target.json`; `go` and `chan`
  work inside Ring 0.
- `~/.local/tinygo/` is the writable TinyGo tree; patches are
  applied via `scripts/patch_tinygo_runtime.sh`.
- `gooos_in_interrupt_depth` in `src/isr.S` is wired; `task.Pause()`
  correctly identifies ISR context.
- 10/10 sendkey trials currently pass against the hybrid (custom
  scheduler + TinyGo runtime) state.

Do **not** re-litigate design decisions made in `phase_b_*.md`. If a
design turns out to be wrong during implementation, record the
issue as a deferred item (see below) and pick the least invasive
workaround.

## Workflow requirements

### 1. TODO_B.md at the project root

At the very start of implementation, **create `TODO_B.md` at the
project root** (`/home/ryo/work/gooos/TODO_B.md`) — this is a
**new file**, not a replacement for the existing `TODO.md`. Do not
modify `TODO.md` except to update its Deferred section when B-items
move from pending to done.

Populate `TODO_B.md` with a checklist that mirrors
`impldoc/phase_b_overview.md §3`'s recommended ordering, expanded
into concrete verifiable steps. Example skeleton:

```
# TODO_B — Phase B migrations

## Items

- [ ] B1 — close-out note (no code change)
- [ ] B3 — retire serialChannel / serialSend / serialTaskEntry
- [ ] B4 — fsRequestChannel → native chan *fsRequest
- [ ] B6 — fatal handlers use serialPanicPrint
- [ ] B5 — keyboard IRQ ring buffer + keyboardPump goroutine
- [ ] B7 — createTask → go serialTask() / go fsTask() / go keyboardPump()
- [ ] B9 — elfExec → ring3Wrapper + exitCh (TSS side-table +
         gooosOnResume patch)
- [ ] B11 — SMP AP idle loop: sti+hlt
- [ ] B8 — delete src/scheduler.go + *TaskAddr linkname decls
- [ ] B10 — delete src/channel.go + strip src/switch.S

## Verification gates (after all items above)

- [ ] make build clean, nm "| grep U" empty
- [ ] 10/10 sendkey trials pass
- [ ] stress test passes
- [ ] make run-smp reaches shell with 4 cores
- [ ] grep -rE "TODO|FIXME|HACK|XXX|temporarily" src/ returns zero

## Deferred

(empty at start — populated when items are encountered)
```

As work progresses, **mark each `[ ]` as `[x] — commit <hash>`**
with a short note. Do not delete rows; the file is an audit trail.

### 2. Commit after every completed TODO_B.md item

After each row flips to `[x]`, stage and commit. Convention:

- Prefix: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, etc.
- Subject ≤ 70 characters, imperative mood.
- Body explains why and references the `TODO_B.md` bullet.
- Include the Claude co-author trailer required by `CLAUDE.md`.

**Do not** `git push`, create branches, or switch branches without
explicit user instruction.

### 3. Subagent delegation

Use subagents aggressively when they help:

- **Explore** agents for surveys (e.g., confirm dead-code
  inventory in `src/` before B3/B8/B10 deletions, enumerate every
  caller of a symbol before rewriting).
- **Plan** agents for sub-design decisions the docs leave open
  (e.g., exact layout of `gooosOnResume`'s patch against TinyGo's
  `task_stack_amd64.go`).
- A **reviewer** subagent at the end of implementation (see §5).

Dispatch independent investigations in a single message with
multiple parallel tool calls.

### 4. Verification gates

Before requesting any manual user verification, all four gates
must be green:

1. `make build` succeeds; `nm tmp/kernel.bin | grep " U "` is
   empty; `make check-multiboot` passes.
2. 10/10 trials of `tmp/test_sendkey.sh` pass (pf=0, exit=3,
   cat=1).
3. `tmp/stress_test.sh` passes (pf=0, exit=6, cat=1).
4. `make run-smp` reaches the shell with 4 cores online.

If any gate fails at an intermediate step, **stop and diagnose
root cause**. Do not patch around failures. Do not request manual
verification prematurely.

### 5. Reviewer subagent pass

Once every TODO_B.md item is `[x]` and all four gates are green,
launch a reviewer subagent (`general-purpose` is fine) with the
following charge:

- Read `impldoc/phase_b_*.md` and every modified/added source
  file.
- Verify each deferred item's design mandate is implemented.
- Flag any TODO/FIXME/HACK/XXX/temporarily comments left in
  `src/`, `user/`, or anywhere else in the repo.
- Flag any place where the implementation diverged silently
  from the design.
- Classify findings **CRITICAL / MAJOR / MINOR**.

Address every CRITICAL and MAJOR finding. For MINOR findings,
either fix or record explicitly ("Reviewer flagged X; left as-is
because Y") in the final report. If the reviewer's first pass
yields substantial findings (>3 design-level issues), run a
second pass after the edits.

### 6. Final reconciliation

Before reporting completion:

- Re-read `TODO_B.md`; every `[ ]` must now be `[x]`.
- `grep -rE "TODO|FIXME|HACK|XXX|temporarily"` over `src/`,
  `user/`, and any new files. Report each hit or confirm zero
  hits.
- Run `make build` one final time to confirm clean state.
- Run the 10-trial sendkey, stress, and SMP gates one more time
  (so the final commit is the one that passes).
- `git status --short` should show only committed changes and
  the usual untracked-by-design items (`archive/`, `hoge.md`,
  `scripts/ralph/`, etc.).

If residual work surfaces, do that work (and commit) before
declaring the task complete.

### 7. README.md update

After all items are done and the reviewer pass is clean, **update
`README.md`** to reflect the post-Phase-B state:

- The "Progress" milestone table no longer has the legacy
  scheduler/channel rows as the active implementation; they are
  replaced by TinyGo-native goroutine + channel.
- The "Where assembly is used" section removes every asm that
  went away (most `*TaskAddr` stubs in `switch.S`; possibly the
  whole `src/switch.S` if nothing survives).
- The architecture diagram is updated so service tasks are shown
  as goroutines, not `createTask`-launched Tasks.
- The `src/` layout tree inside the README matches the post-B8
  and post-B10 state (no `scheduler.go`, no `channel.go`).
- The Prerequisites section (already documenting
  `~/.local/tinygo/` and `scripts/patch_tinygo_runtime.sh`)
  gains a note if `gooosOnResume` patching requires a
  script update.

Commit the README change as the final commit of the task.

### 8. Out-of-scope tracking

Some items may prove harder than the design anticipated. For
each such case:

- Record it in `TODO_B.md`'s `## Deferred` section with a
  one-line summary and a reference (`file:line` or design-doc
  section) to where the design expected the behavior.
- **Do not silently drop any design mandate.** If you cannot
  implement it, say so explicitly in the deferred list.

At the end, summarize the deferred list in the final report to
the user.

### 9. Manual verification policy

Do not ask the user to manually test until all four verification
gates (§4) pass across the full sendkey test matrix. Partial
results are not sufficient — the user's time is valuable and the
automated harness is the cheaper first pass.

## Scope fences

- Do not change the Ring-3 user ABI, `user/` sources, or the
  12-syscall dispatch contract.
- Do not introduce features beyond what the design docs specify.
- Do not rewrite `src/vm.go`, `src/process.go`, or `src/elf.go`
  beyond the changes explicitly called out in
  `impldoc/phase_b_ring3_and_exec.md` (elfExec only).
- SMP v2, precise GC, growable stacks, ISR-safety lint — all
  flagged Deferred in the design docs. Do not start them.

## Language policy

- Code: Go preferred; `.S` only where the design already
  identifies an asm requirement (B5's ring buffer may use Go
  with `//go:nosplit`; B9's `gooosOnResume` is a patched Go
  function inside TinyGo's runtime).
- Comments and documentation: English.
- User-facing messages during this session: match the user's
  language (typically Japanese).

## Starting point

When you enter plan mode, do not jump to creating TODO_B.md.
First build a concrete plan of attack that:

1. Confirms which of the 10 items the design docs cover, and
   that no unforeseen code references would be broken by the
   ordering in `impldoc/phase_b_overview.md §3`.
2. Identifies any place where the design underspecifies and
   proposes a concrete resolution (e.g., the exact line numbers
   in `~/.local/tinygo/src/internal/task/task_stack_amd64.go`
   where `gooosOnResume` is inserted).
3. States the commit-per-item cadence and the rollback plan if
   a commit's sendkey regression test fails.

Present that plan for approval via `ExitPlanMode` before any
code changes begin.
