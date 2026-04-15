# Claude Code Plan-Mode Prompt: Implement Ring-0 Goroutine & Channel Support in gooos

You are entering plan mode to **implement and verify** the design already
written in `impldoc/goroutine_design_*.md`. Read those four documents first
— they are the authoritative specification. Your job is to produce working
code, not to redesign.

## The authoritative design

Read in order before planning:

1. `impldoc/goroutine_design_overview.md` — scheduler recommendation
   (`scheduler=tasks`), rejected alternatives, v1 scope, and the four
   prerequisite spikes in §7.
2. `impldoc/goroutine_design_scheduler.md` — scheduler replacement,
   stack model, preemption hook, required runtime stubs, TSS.RSP0
   per-Ring-3-goroutine hook.
3. `impldoc/goroutine_design_channels_and_isr.md` — native channel
   migration, keyboard IRQ ring buffer, `interrupt.In()` primitive,
   ISR-context safety rules.
4. `impldoc/goroutine_design_gc_and_smp.md` — GC root reachability,
   SMP v1 (BSP-only), complete files-to-touch catalog (§5), verification
   plan (§6), open risks (§8), suggested implementation ordering (§9).

Also read `CLAUDE.md` for the project workflow rules and the relevant
sections of `current_impl_doc/` to understand the current code. The v1
scope is defined in the design docs and is not up for renegotiation here;
if you encounter a design decision that is missing or ambiguous, record
it as a deferred item (see “Out-of-scope tracking” below) and pick the
least invasive resolution.

## Workflow requirements

### 1. TODO.md at the project root

At the very start of implementation, **create (or overwrite, clearing
previous contents) a file `TODO.md` at the project root** (i.e.,
`/home/ryo/work/gooos/TODO.md`). Populate it with an ordered checklist
that mirrors the implementation ordering in
`goroutine_design_gc_and_smp.md §9`, expanded into concrete, verifiable
steps. Example granularity (use your judgment — split further if a step
spans multiple files):

- [ ] Spike 1: confirm `runtime_unix.go` collision resolution path
- [ ] Spike 2: `scheduler=tasks` links with trivial `go`/`chan` program
- [ ] Spike 3: `interrupt.In()` shim via `in_interrupt_depth`
- [ ] Spike 4: boot-goroutine stack size mechanism identified
- [ ] Add `src/goroutine_stubs.go` (`sleepTicks`, `ticks`, …)
- [ ] Flip `src/target.json` to `scheduler=tasks`
- [ ] Migrate `serialChannel` → native `chan string`
- [ ] Migrate `fsRequestChannel` → native `chan *fsRequest`
- [ ] Replace keyboard IRQ path with ring buffer + pump goroutine
- [ ] Rewrite fatal handlers to `serialPanicPrint` (no heap alloc)
- [ ] Replace `createTask` calls in `src/main.go` with `go` launches
- [ ] Delete `src/scheduler.go`
- [ ] Delete `src/channel.go`
- [ ] Convert `elfExec` to `ring3Wrapper` + `exitCh`
- [ ] Strip dead stubs from `src/switch.S` (keep only
  `elfExecTrampolineAddr`, `taskReturnHaltAddr`)
- [ ] Run goroutine smoke tests (§6.2 of the GC/SMP doc)
- [ ] Run 10-trial sendkey regression (`tmp/test_sendkey.sh`)
- [ ] Run stress test (`tmp/stress_test.sh`)
- [ ] Run SMP sanity (`make run-smp`)

As you work, **mark each item completed in place** (change `[ ]` → `[x]`
with a short note — e.g., commit hash or verification summary). Do not
delete items; the running checklist is itself a record.

### 2. Commit after every completed TODO.md item

After each `[ ]` becomes `[x]`, run `git add` on the relevant files and
`git commit`. Commit message convention:

- Prefix: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, etc.
- Subject ≤ 70 characters, imperative mood.
- Body (optional) explains why; reference the TODO.md bullet that was
  closed.
- Include the Claude co-author trailer required by `CLAUDE.md`.

**Do not** `git push`, create branches, or switch branches without explicit
user instruction.

### 3. Subagent delegation

Use subagents aggressively when they help — especially:

- **Explore** agents to survey multiple files before a non-trivial edit
  or to verify TinyGo runtime behavior against
  `/usr/local/lib/tinygo/src/`.
- **Plan** agents for any sub-design decision the docs leave open (e.g.,
  exact shape of the `wantReschedule` asm epilogue).
- A dedicated **reviewer** subagent at the very end of implementation
  (see below).

When multiple independent explorations or edits can run in parallel,
dispatch them in a single message with multiple tool calls.

### 4. Verification gates

Before requesting any manual user verification, all four gates must be
green (they are also listed in `goroutine_design_gc_and_smp.md §7`):

1. `make build` succeeds; `nm tmp/kernel.bin | grep " U "` is empty.
2. 10/10 trials of `tmp/test_sendkey.sh` pass (pf=0, exit=3, cat=1).
3. `tmp/stress_test.sh` passes (pf=0, exit=6, cat=1).
4. `make run-smp` reaches the shell with 4 cores online.

If a gate fails, **stop and investigate root cause.** Do not patch around
failures; do not request manual verification prematurely. Only after all
gates are green on the final build do you report back for user hands-on
testing.

### 5. Reviewer subagent pass

Once implementation is complete and all verification gates are green,
launch a reviewer subagent (`code-reviewer` or `general-purpose`) with
the following charge:

- Read the four design docs and all modified/added source files.
- Check that every mandate from the design is implemented.
- Flag any TODO/FIXME/HACK/XXX comments left in the code.
- Flag any place where the implementation diverged silently from the
  design.
- Classify findings CRITICAL / MAJOR / MINOR.

Address every CRITICAL and MAJOR finding. For MINOR findings, either fix
or record explicitly (“Reviewer flagged X; left as-is because Y”) in the
final report. If the reviewer’s first pass produced substantial findings,
run a second pass after the edits.

### 6. Final reconciliation

At the very end, before reporting completion:

- Re-read `TODO.md` and verify every item is checked off.
- Grep the entire `src/`, `user/`, and any new files for `TODO`,
  `FIXME`, `HACK`, `XXX`, and `temporarily` comments. Report each hit
  or confirm zero hits.
- Run `make build` one final time to confirm a clean state.
- Verify no stray files (build artifacts, debug logs) were left in the
  repo. `git status --short` should show only committed changes and the
  usual untracked-by-design items (`archive/`, `tasks/`, `scripts/`,
  etc.).

If any of these surface residual work, do that work (and commit) before
declaring the task complete.

### 7. Out-of-scope tracking

Some design items may prove harder than estimated or require design
changes mid-flight. For each such item:

- Record it in a new section at the bottom of `TODO.md` titled
  `## Deferred (out-of-scope for this session)`.
- Include a one-line summary and a reference (`file:line` or section) to
  where the design expected the behavior.
- **Do not silently drop any design mandate.** If you cannot implement
  it, say so explicitly in the deferred list.

At the end, summarize the deferred list in your final report to the
user.

### 8. Manual verification policy

Do not ask the user to manually test until all four verification gates
(§4) pass across the full sendkey test matrix. Partial results are not
sufficient — the user's time is valuable and the automated harness is
the cheaper first pass.

## Scope fences

- Do not change the Ring-3 user ABI, `user/` sources, or the 12-syscall
  dispatch contract.
- Do not introduce new features beyond what the design docs specify.
- Do not rewrite `src/vm.go`, `src/process.go`, or `src/elf.go` beyond
  the changes explicitly called out in
  `goroutine_design_gc_and_smp.md §5`.

## Language policy

- Code: Go preferred; use `.S` only where CPU instructions require it
  (the design already identifies these places).
- Comments and documentation: English.
- User-facing messages during this session: match the user's language
  (typically Japanese).

## Starting point

When you enter plan mode, do not skip straight to `TODO.md` creation.
First build a concrete plan of attack that:

1. Lists the four spikes and the pass criterion for each.
2. Identifies any place where the design underspecifies and proposes a
   resolution.
3. Confirms the estimated blast radius (number of files touched per
   step) matches the ordering in `goroutine_design_gc_and_smp.md §9`.

Present that plan for approval via ExitPlanMode before any code changes
begin.
