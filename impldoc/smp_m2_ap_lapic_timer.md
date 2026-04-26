# M2 — AP LAPIC Timer Race Fix

**Scope.** Fix the global-counter race in gooos's ISR-depth accounting so every CPU can run its own 100 Hz LAPIC timer without boot hangs or `blocked inside interrupt` panics. **Does not** cover Ring-3 AP scheduling (M4) or the `scheduler=cores` promotion (M3); those are sibling docs. M2 is **independent of M4** and can be worked in parallel.

**Cross-links.**
- Deferred-item charter: `TODO_SMP3.md §"Deferred further"` item 1.
- Primary symptom writeup: `impldoc/smp_deferred_and_known_issues.md §2.2`.
- Milestone entry in this batch: `impldoc/smp_unblock_overview.md`.
- Unified schedule: `impldoc/smp_unblock_milestones_and_verification.md §M2`.
- Existing SMP v2 per-CPU ISR-depth design: `impldoc/smp_percpu_and_sync.md §6`.
- Current ISR prologue/epilogue: `src/isr.S:110-111, 129-130` (dual counter) + `src/isr.S:166, 168` (`gooos_in_interrupt_depth` `.bss` declaration).

---

## 1. Root Cause

From `impldoc/smp_deferred_and_known_issues.md §2.2`:

> **Root cause (suspected)**: the ISR prologue's dual-counter approach (`incl gooos_in_interrupt_depth(%rip)` + `incl %gs:4`) races on the global counter when multiple CPUs fire timer ISRs simultaneously. `incl` is a non-atomic read-modify-write on x86; two concurrent `incl` on the same address can lose an update, leaving the global counter permanently elevated.

The global counter at `src/isr.S:168` (`gooos_in_interrupt_depth`) is incremented at `src/isr.S:110` (prologue) and decremented at `src/isr.S:129` (epilogue). When the global stays stuck high after a racy `incl`, every subsequent `interrupt.In()` reads the stuck value and reports true, making the entire runtime think it is perpetually in interrupt context.

The per-CPU counter at `%gs:4` is unaffected by the race (each CPU's GS base points at a different `PerCPU` block — `impldoc/smp_percpu_and_sync.md §1.3`), but the current `interrupt.In()` implementation (via `~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go:33` `func In() bool { return false }`) is actually a *workaround* for a secondary issue (see §2 below), not a read of either counter.

## 2. The Subtlety That Blocks a Naïve Fix

`impldoc/smp_deferred_and_known_issues.md §2.2` continues:

> **Fix approach**: remove the global counter entirely and switch `interrupt.In()` to read the per-CPU `%gs:4` counter. This was attempted but caused "blocked inside interrupt" panics because syscall handlers call `task.Pause()` while the per-CPU ISR depth is 1 (which is correct — the goroutine IS in ISR context).

**Why.** gooos syscalls use `int 0x80` dispatched through the common IDT/ISR path (`src/interrupt.go:6` "Vector 0x80 (int 0x80) is special-cased for syscall dispatch."). The ISR prologue increments `%gs:4` before calling the handler; the syscall handler (e.g., `src/userspace.go:98 sysExitHandler` and siblings) then calls into runtime code that may invoke `task.Pause()` (via `sys_yield`, `sys_sleep`, blocking `sys_read`, etc.). `task.Pause()` (in the patched TinyGo runtime) panics when `interrupt.In()` returns true. So migrating `interrupt.In()` to the per-CPU counter would make every blocking syscall tip the panic.

The current workaround — `interrupt.In()` always returns `false` — neutralises the check but makes `interrupt.In()` useless as a gating signal for any future caller that relies on it.

## 3. Candidate Fix Strategies

Three options; each trades off invasiveness, blast radius, and future extensibility.

### 3.1 Strategy A — Per-CPU counter + "syscall in progress" flag (RECOMMENDED)

**Design.** Keep the per-CPU ISR-depth counter at `%gs:4` and retire the global `gooos_in_interrupt_depth` at `src/isr.S:166-168`. Add a second per-CPU byte/word at a fixed `PerCPU` offset — say `%gs:12` (`pcpuOffSyscallDepth`, to be added to `impldoc/smp_percpu_and_sync.md §1.3`'s layout table, stealing from the existing padding) — that the syscall dispatch path increments on entry and decrements on exit. `interrupt.In()` returns:

```
(%gs:4 != 0) && (%gs:12 == 0)
```

i.e., true when the CPU is in a real ISR but not in a syscall handler.

**Required edits:**

- `src/isr.S:106-114` — prologue: drop `incl gooos_in_interrupt_depth(%rip)` (line 110); keep `incl %gs:4` (line 111). Add a conditional branch: if the interrupt vector is 0x80 (syscall), increment `%gs:12` too.

  Each ISR stub in gooos pushes its vector number onto the stack before jumping to the common prologue. At the moment the prologue runs, the vector is at `120(%rsp)` (offset computed from the 15-register push frame already present; confirm at M2 entry with `grep -n '120(%rsp)' src/isr.S` — the existing Go-side handler dispatch uses this offset). Concretely:

  ```
      /* Prologue: per-CPU ISR depth + conditional syscall depth */
      incl    %gs:4                           /* ISR depth */
      cmpq    $0x80, 120(%rsp)                /* vector == 0x80? */
      jne     .Lnosys_enter
      incl    %gs:12                          /* syscall depth */
  .Lnosys_enter:
  ```

  Place the compare-and-branch BEFORE any scratch-register usage (other than what the push frame already clobbered) so `%rdi` / `%rsi` remain preserved for the downstream `go_interrupt_handler` call.

- `src/isr.S:126-132` — epilogue: matching drops and conditional decrement. Remove `decl gooos_in_interrupt_depth(%rip)` (line 129); keep `decl %gs:4` (line 130); conditional `decl %gs:12` if vector was 0x80.

  ```
      /* Epilogue: reverse order — syscall depth first, then ISR depth */
      cmpq    $0x80, 120(%rsp)
      jne     .Lnosys_exit
      decl    %gs:12
  .Lnosys_exit:
      decl    %gs:4
  ```

- `src/isr.S:152-168` — remove the `gooos_in_interrupt_depth` `.bss` symbol entirely. Delete the comment block at `:152-165`, the `.global` declaration at `:166`, the label at `:168`, and any `.skip` bytes reserving the counter (typically 4 bytes following the label). Preserve the `.section .text` resumption that follows.

- `src/percpu.go` — add `syscallDepth uint32` field to the `PerCPU` struct at offset 12 (after `interruptDepth` at offset 4-7 and `_pad0 uint32` at offset 8-11, or wherever the next free 4 bytes are). Also update the layout constant table to expose `pcpuOffSyscallDepth = 12`. Double-check the struct-size padding remains cache-line aligned (64 bytes per `impldoc/smp_percpu_and_sync.md §1.3`).
- `~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go:33` — rewrite `func In() bool { return false }` to read both per-CPU fields. Rough shape:

  ```go
  //go:linkname gooos_readInterruptDepth readInterruptDepth
  func gooos_readInterruptDepth() uint32

  //go:linkname gooos_readSyscallDepth readSyscallDepth
  func gooos_readSyscallDepth() uint32

  //go:nosplit
  func In() bool {
      return gooos_readInterruptDepth() != 0 && gooos_readSyscallDepth() == 0
  }
  ```
- `src/stubs.S` — add two small helpers: `readInterruptDepth` (movl `%gs:4`, `%eax`; ret) and `readSyscallDepth` (movl `%gs:12`, `%eax`; ret). These are already foreshadowed by `impldoc/smp_percpu_and_sync.md §6.2` which proposed `readInterruptDepth` as an assembly helper.
- `src/goroutine_irq.go` — update any existing Go-side readers to call the new per-CPU helpers; delete the `gooos_in_interrupt_depth` reader.
- `src/main.go` — un-gate `lapicTimerInitAP()` (remove whatever compile-time or runtime guard prevents it from running per CPU).

**Pro.** Preserves `interrupt.In()`'s original semantic for any future caller (e.g., `println` wants to avoid reentering the scheduler). Aligns with `impldoc/smp_percpu_and_sync.md §6.2` which anticipated this exact shape.

**Con.** Touches three files (`isr.S` + `percpu.go` + `interrupt_gooos.go`) plus one patch edit; largest surface area.

### 3.2 Strategy B — Syscall trap-gate promotion

**Design.** Change the `int 0x80` IDT entry from an interrupt-gate descriptor to a trap-gate descriptor (or equivalently, have the syscall stub `decl %gs:4` immediately on entry, before the handler runs). Under either mechanism the syscall handler runs with ISR depth 0 so `task.Pause()` is happy.

**Required edits:**

- `src/idt.go` — change the IDT installation for vector 0x80 so it uses a trap gate (type 0xF) instead of an interrupt gate (type 0xE).
- `src/isr.S:106-114` — remove the global counter but leave the per-CPU `incl %gs:4` as-is; no per-handler branching needed.
- Same `interrupt_gooos.go` migration as Strategy A: `In()` returns `(%gs:4) != 0`.

**Pro.** Minimal `isr.S` surgery; no new per-CPU field.

**Con.** Changing to a trap gate keeps interrupts enabled during the handler (IF stays at its pre-trap value). gooos's syscall handlers currently assume `cli` at entry (they run inline and re-enable only at Ring-3 return). Auditing every syscall handler for interrupt-safety before this flip is expensive — possibly more expensive than Strategy A's per-CPU-flag approach.

### 3.3 Strategy C — Keep `interrupt.In() == false` + per-CPU counter for other callers

**Design.** Drop the global `gooos_in_interrupt_depth` (fixes the race). Keep the per-CPU `%gs:4` counter. Leave `interrupt.In()` as-is (`return false`). Document that gooos does not use `interrupt.In()` as a gating signal.

**Required edits:**

- `src/isr.S` — drop global counter (lines 110, 129, 152, 166, 168). Keep `%gs:4`.
- `interrupt_gooos.go:33` — unchanged.
- `src/main.go` — un-gate `lapicTimerInitAP()`.

**Pro.** Cheapest. No new per-CPU field, no trap-gate audit, no handler edits.

**Con.** `interrupt.In()` remains a lie. Any future TinyGo upgrade that adds new callers of `interrupt.In()` (inside the runtime) will silently misbehave. Effectively kicks the can to whoever hits that next.

### 3.4 Recommendation

**Strategy A** is the recommended primary. Its cost (one new `PerCPU` field + two assembly helpers + one interrupt-depth conditional) is bounded and already foreshadowed by the SMP v2 design. Strategy B is the fallback if the per-handler branch on vector number in the ISR prologue proves too intrusive — but auditing every syscall handler for trap-gate safety is likely worse. Strategy C is the emergency escape hatch if Strategies A and B both produce regressions; accept as a deliberate "interrupt.In is not used" contract and document it.

---

## 4. Affected Files (Strategy A)

| File | Edit |
|---|---|
| `src/isr.S:106-114, 126-132, 152-168` | Drop global counter + prologue/epilogue; add vector-0x80 branch for `%gs:12` syscallDepth. |
| `src/stubs.S` | Add `readInterruptDepth` + `readSyscallDepth` asm helpers (both 2-instruction leaf functions). |
| `src/percpu.go` | Add `syscallDepth uint32` field to `PerCPU` + corresponding offset constant. Update struct-size padding to stay cache-line aligned (`impldoc/smp_percpu_and_sync.md §1.3`). |
| `src/goroutine_irq.go` | Replace any reader of `gooos_in_interrupt_depth` with the per-CPU helper. |
| `src/main.go` | Un-gate `lapicTimerInitAP()` — remove any `if false`, commented-out call, or `const apLapicTimerEnabled = false` guard. Confirm via grep at M2 start. |
| `~/.local/tinygo0.40.1/src/runtime/interrupt/interrupt_gooos.go` | Replace `func In() bool { return false }` with the `gooos_readInterruptDepth() != 0 && gooos_readSyscallDepth() == 0` body; add the two linkname declarations. |
| `scripts/tinygo_runtime.patch` | Regenerate after `interrupt_gooos.go` edit (`git -C /home/ryo/work/tinygo diff > …`). |
| `scripts/patch_tinygo_runtime.sh` | Update post-condition greps: the idempotency check currently looks for `&& kernelspace` / `&& !kernelspace` in `interrupt_gooos*.go`; add a grep for the new linkname declarations. |

## 5. Commit Plan

One commit per item; land in order:

| # | Subject | Files |
|---|---|---|
| 1 | `fix(smp): per-CPU readInterruptDepth + readSyscallDepth helpers` | `src/stubs.S` + `src/percpu.go` (new field + offset constant) |
| 2 | `fix(smp): drop global gooos_in_interrupt_depth; keep per-CPU counter only` | `src/isr.S` (prologue/epilogue + BSS removal) + `src/goroutine_irq.go` (reader migration) |
| 3 | `fix(smp): track syscall depth per-CPU so task.Pause works in syscall handlers` | `src/isr.S` (vector-0x80 branch) — must land together with #2 if splitting would leave a bisect window where `interrupt.In()` panics in syscall dispatch |
| 4 | `fix(smp): migrate runtime interrupt.In() to per-CPU counters` | patched `interrupt_gooos.go` + `scripts/tinygo_runtime.patch` (regenerated) + `scripts/patch_tinygo_runtime.sh` (idempotency grep update) |
| 5 | `fix(smp): enable AP LAPIC timer at 100Hz` | `src/main.go` un-gate |
| 6 | `test(smp): M2 regression matrix green under -smp 4` | (optional) new wrapper `scripts/test_smp_matrix.sh` if the regression matrix is not already `SMP=4`-aware |

Commits 2 and 3 may need to squash if bisect-safety requires it.

---

## 6. Verification

### 6.1 Build gates

- `make build` clean.
- `make lint` clean — `scripts/lint_isr.go` must not flag new ISR-unsafe constructs introduced by the isr.S edits.
- `make verify-globals` clean — confirm no new runtime global migrated outside `[_globals_start, _globals_end)`.
- `scripts/patch_tinygo_runtime.sh` idempotent (second run prints `already-applied:`).

### 6.2 Runtime gates

- `make run-smp` — boot under `-smp 4` reaches the shell prompt (same as M1 exit today). If the fix regresses this, the commit pair is wrong.
- Serial log greps:
  - `"LAPIC timer: N ticks/10ms"` — BSP calibration line, unchanged from today.
  - `"AP N: tick"` (or whatever per-AP tick marker is instrumented in `src/lapic_timer.go` — add one if absent; gate behind a `const apTimerTrace = true` boot-only flag).
  - **No** `"blocked inside interrupt"` lines anywhere.
- Existing regression matrix under `-smp 4`:
  ```
  bash scripts/test_net.sh                      # PASS
  bash scripts/test_tcp_phase1.sh               # PASS
  bash scripts/test_tcp_phase2.sh               # PASS
  bash scripts/test_tcp_phase3.sh               # PASS
  bash scripts/test_tcp_phase4.sh               # PASS
  bash scripts/test_tcp_phase5.sh               # PASS
  ```
  (These currently default to `-smp 1`. Either add an `SMP=4` env var override inline, or ship `scripts/test_smp_matrix.sh` as commit #6 above that reruns each harness under `-smp 4`.)

### 6.3 Dedicated M2 probe (REQUIRED)

The M2 Exit gate's "no `blocked inside interrupt` under `-smp 4`" check only catches the regression if an actual `task.Pause()` fires during syscall dispatch during the test window. A boot-time smoke test may not exercise that path. Therefore this probe is a **required** harness, not optional, to give M2 a confident green signal.

Add a 1-second boot-time stress probe in `src/main.go` (gated `const runM2Probe = true`, off in release):

```go
// M2 probe: spawn N goroutines, each calls time.Sleep(10ms) in a
// tight loop for 100 iterations. Each sleep acquires addSleepTask
// and is woken by either PIT or LAPIC timer. If the AP LAPIC timer
// is racy, the sleep queue eventually stalls; if interrupt.In() is
// wrong, the first time.Sleep panics with blocked-inside-interrupt.
//
// Succeeds when every goroutine completes its 100 iterations.
```

Emit `"m2_probe: PASS count=N"` on success; harness `scripts/test_smp_m2_timer.sh` greps for it. Add the harness alongside `scripts/test_smp_basic.sh` at M2 commit #5 or #6 per §5.

---

## 7. Rollback

If the commit series introduces a regression that survives a local debugging round:

1. `git revert <commit #5>` — re-gate `lapicTimerInitAP()` so the AP LAPIC timer returns to disabled.
2. `git revert <commit #4>` — restore the `return false` workaround in `interrupt_gooos.go` and regenerate patch.
3. If commit #2 or #3 caused the regression: `git revert` them too. The kernel boot under `-smp 4` falls back to the M1 Wave 1 safe state (APs idle in `waitForEvents`, only BSP runs a LAPIC timer).
4. Document observed failure in `impldoc/smp_deferred_and_known_issues.md §2.2` under a new "Investigation attempts" subsection with the commit range reverted and the observed symptom.

Crucially, the `gooos_in_interrupt_depth` global removal in commit #2 is the race-fixing change. If the later commits all revert but commit #2 stays, the race is still fixed for BSP-only operation without reintroducing the global.

---

## 8. Deliverables

1. Kernel + patched-runtime edits implementing Strategy A (or documented fallback).
2. AP LAPIC timer at 100 Hz, confirmed via serial-log tick markers.
3. Full regression matrix green under `-smp 4`.
4. `impldoc/smp_deferred_and_known_issues.md §2.2` marked Resolved with commit hashes.
5. No `git push`; no branch ops; no `master` merge without explicit user instruction.

## Reviewer MINOR notes

(Filled after the reviewer pass; none initially.)
