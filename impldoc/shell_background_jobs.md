# Shell Background Execution with `&` (feature 2.4)

## Scope

Add POSIX-style `&` background execution to the gooos shell (`user/cmd/sh`). A command or pipeline terminated by `&` spawns and returns control to the shell immediately; the shell tracks the background job and prints a completion line when it reaps.

Support requires a new non-blocking-wait kernel syscall. Per the overview's Design Decisions table: **new dedicated syscall #34 `sys_waitpid`** with POSIX-matching `(pid, options, status*)` signature. Existing `sys_wait` (#16) stays untouched.

Out of scope: Ctrl-C / SIGINT, foreground/background job switching (`fg`/`bg` builtins), job-ID arithmetic beyond a simple numeric display. Call these out explicitly; they are deferred.

## Cross-links

- `preempt_shell_overview.md` — batch entry point + Design Decisions table (syscall #34 for non-blocking wait).
- `shell_ps_command.md` — 2.5 also extends the shell + process-table surface; 2.4 ships first per the overview commit order.
- `shell_multicore_preempt.md` — 2.3's `test_smp_shell_preempt.sh` harness depends on `&` syntax; if 2.4 lands later, 2.3 uses the §3.2 fallback.
- `preempt_shell_milestones_and_verification.md` — 2.4 Entry/Exit rolled into the unified gates.
- `preempt_shell_readme_update_plan.md` — "No `&` background jobs" Known-limitation bullet is removed when 2.4 lands.

## 1. Current State

- Shell REPL at `user/cmd/sh/main.go:13 main()`: `ReadLine` → `parsePipeline(line)` → `executePipeline(p)`. Always blocks in `gooos.Wait()` for each spawned pid (`main.go:136` for pipelines; `Exec` blocks internally for single-stage external commands via `main.go:228 gooos.Exec`).
- Tokenizer at `user/cmd/sh/parse.go:90 tokenize()` recognizes space/tab/`<`/`|`/`>`/`>>`. **`&` is not recognized** — it would end up in `cur` as part of a command name and produce "sh: command not found".
- `cmdLine` struct at `parse.go:5-10`: `argv`, `stdinFile`, `stdoutFile`, `appendOut`. No background flag.
- `pipeline` struct at `parse.go:14-16`: `stages []cmdLine`. No pipeline-level flags. **2.4 needs a pipeline-level `background bool`** because `cmd1 | cmd2 &` puts the whole pipeline in background (POSIX).
- Kernel syscall `sys_wait` (#16) at `src/userspace.go:736 sysWaitHandler` blocks on `processWait(proc)` which receives on `proc.exitCh`. There is no non-blocking variant.
- `processWait` at `src/process.go:354` also performs a **foreground-keyboard transfer** — the parent yields keyboard ownership to the waited-on child. For background processes this is wrong; the shell must retain foreground and the background process must see EOF on stdin reads (already the behavior for non-foreground processes per `src/process.go:96-101`).
- Syscall numbers 0..33 used (`src/userspace.go:47-85`). First free is **34**; 2.4 claims it. (2.2 claims 35, 2.5 claims 36 — see overview.)

## 2. Design

### 2.1 Parser: `&` token

`tokenize` at `parse.go:90-125` gains a new `case '&':` that flushes `cur` and emits `"&"` as a standalone token. Positioned between `case '|':` and `case '>':` for source-order readability.

Constraint: `&&` (shell-logical-and, hypothetical future feature) would be a lookahead just like `>>` already is at `parse.go:113`. Design decision for 2.4: **reject `&&` at parse time** with `"sh: syntax error"`. This keeps the door open for future logical-and without committing. Pattern:

```go
case '&':
    flush()
    if i+1 < len(line) && line[i+1] == '&' {
        toks = append(toks, "&&") // hands to parseStage which rejects
        i++
    } else {
        toks = append(toks, "&")
    }
```

`parseStage` (`parse.go:53`) must reject any stage containing `&&`, `&`, or bare empty argv. The `&` token is consumed at the `parsePipeline` level, not `parseStage`.

### 2.2 Pipeline grammar

`parsePipeline` at `parse.go:20-49` gains a post-flush check: if the final token is `&`, drop it from the last stage's token list and set `p.background = true`.

New field on `pipeline`:

```go
type pipeline struct {
    stages     []cmdLine
    background bool // new: set when command line ended with '&'
}
```

Invariants:
- `&` may only appear as the last token. Mid-pipeline `&` is a syntax error.
- `|` before `&` is OK (the whole pipeline is background).
- `&` alone (no stages) is a syntax error.

### 2.3 Shell-local jobs table

Fixed-size array of 16 slots, shell-local. Each slot records:

```go
type jobEntry struct {
    id      int    // 1..16, stable for life of the job
    pid     int    // kernel pid
    cmd     string // display name (first stage's argv[0] for pipelines)
    done    bool   // set by the reap poll when sys_waitpid reports exited
    exit    int    // exit code when done
}

var jobs [16]jobEntry
var nextJobID int = 1
```

**Cap rationale.** `maxRing3Procs = 32` (`src/ring3_pool.go:20`); 16 simultaneous background jobs leaves half the pool for foreground + shell. If the table is full when `&` is used, the shell prints `"sh: too many background jobs"` and reverts to foreground (still spawns; still waits). Documented.

### 2.4 Spawn path

`executePipeline` (`main.go:39`) gains a `background` parameter; REPL at `main.go:30` calls `executePipeline(p)` (now `executePipeline(p, p.background)`).

Single-stage foreground external commands currently use `gooos.Exec` (`main.go:228`, which blocks internally). For `&`, single-stage path must convert to `Spawn` + no-Wait so the shell controls the (non-)wait. Refactor:

- Extract a new `runCommandBackground(argv, jobEntry)` that calls `gooos.Spawn(argv[0]+".elf", joinArgs(argv))`, stores pid in the jobEntry, and returns without blocking.
- `runCommand` (`main.go:213`) unchanged for foreground builtins.
- `executeCmdLine` (`main.go:154`) grows a `background` flag; when set, skips the builtin path (no background builtins) and calls `runCommandBackground`.

For pipelines, `executeConcurrentPipe` (`main.go:57`) grows a `background` parameter; the tail loop at `main.go:134-138` (the per-pid `gooos.Wait`) is skipped. Instead, each spawned pid is recorded in a fresh jobEntry.

**Foreground-transfer invariant.** `processWait` at `src/process.go:354-358` calls `setForegroundProc(proc)` on entry. Background jobs must NOT trigger this. Because the new path uses `sys_spawn` + **no `sys_wait` call**, foreground transfer does not happen. The reap poll uses `sys_waitpid` (§3) which does not transfer foreground. Invariant: **`sys_waitpid` handler MUST NOT call `setForegroundProc`.** Verified by reviewer check.

### 2.5 Reap poll

Between REPL iterations, before `gooos.Print("$ ")`, the shell calls `reapBackgroundJobs()`:

```go
func reapBackgroundJobs() {
    for i := range jobs {
        je := &jobs[i]
        if je.pid == 0 || je.done { continue }
        status, reaped := gooos.Waitpid(je.pid, gooos.WNOHANG)
        if reaped {
            je.done = true
            je.exit = status
            gooos.Println("[" + strconv.Itoa(je.id) + "] " + strconv.Itoa(je.pid) +
                " done exit=" + strconv.Itoa(status) + " " + je.cmd)
            *je = jobEntry{} // free slot
        }
    }
}
```

Completion-line format matches bash convention enough to be readable; exact format is implementation detail.

**Reap-on-new-foreground.** When the user enters a foreground command, `reapBackgroundJobs()` is called once before spawning it, so the shell's next prompt is not racing a completion notification against the spawning child.

**Reap-on-shell-exit.** `exit` builtin does NOT wait on background jobs; they are orphaned and the kernel eventually reclaims them when the shell's ring3Wrapper exits (the parent pointer in the child's `Process` still refers to the dead shell, but `processWait` never runs, so `processExit` still runs and drops the pool slot via `processExit` path in `src/process.go`). Documented; out of scope to do better.

## 3. sys_waitpid — kernel ABI

### 3.1 Signature

`sys_waitpid(pid int32, options uint32, status *int32) → int32`

Register ABI:
- `RAX = 34`
- `RDI = pid` (signed, -1 not supported in 2.4; see §Rejected alternatives)
- `RSI = options` (bit 0 = `WNOHANG`; other bits reserved, must be 0)
- `RDX = status` (user vaddr to write exit code into; NULL / 0 = ignore)
- Returns in `RAX`:
  - Positive (= pid) — child exited; status written if not NULL; PCB removed from `procByPID`.
  - 0 — `WNOHANG` set and child still running. No change to child.
  - Negative (= `-fdErrBad` etc.) — error.

### 3.2 Errno semantics

- `pid < 1` — `fdErrBad` (2.4 does not support wildcard waits).
- `pid` not in `procByPID` — `fdErrBad`.
- Child's parent is not `currentProc()` — `fdErrBad`.
- `options & ^WNOHANG != 0` — `fdErrBad`.

### 3.3 Handler pseudocode (`src/userspace.go` tail)

Reviewer MAJOR fold: simplified to WNOHANG-only (the blocking fallback is redundant — callers who want blocking semantics use `sys_wait` #16). Removes the deadlock-prone double-reap race.

```go
const WNOHANG = 1

// --- Syscall 34: sys_waitpid ---
// RDI = pid, RSI = options (must include WNOHANG), RDX = status vaddr (may be 0).
// Returns pid on reap, 0 on still-running, negative on error.
// BLOCKING waits are NOT supported; use sys_wait #16 for that.
func sysWaitpidHandler(frame *SyscallFrame) {
    parent := currentProc()
    if parent == nil {
        frame.RAX = sysFail(fdErrBad)
        return
    }
    pid := int32(frame.RDI)
    options := uint32(frame.RSI)
    statusVaddr := uintptr(frame.RDX)
    if pid < 1 || options&^WNOHANG != 0 || options&WNOHANG == 0 {
        frame.RAX = sysFail(fdErrBad) // non-blocking-only: WNOHANG required
        return
    }
    fl := procLock.Acquire()
    child := procByPID[uint32(pid)]
    // child.parent is immutable post-spawn; safe to read without extra lock.
    if child == nil || child.parent != parent {
        procLock.Release(fl)
        frame.RAX = sysFail(fdErrBad)
        return
    }
    procLock.Release(fl)
    // Non-blocking receive.
    select {
    case exitCode := <-child.exitCh:
        if statusVaddr != 0 {
            writeU32Through(parent.pml4, statusVaddr, uint32(exitCode))
        }
        // Reap: double-check child is still in procByPID (a concurrent
        // waitpid racer could have reaped already; not an error, we just
        // skip the delete).
        fl := procLock.Acquire()
        if procByPID[child.pid] == child {
            delete(procByPID, child.pid)
        }
        procLock.Release(fl)
        frame.RAX = uintptr(pid) // positive = reaped
    default:
        frame.RAX = 0 // still running
    }
}
```

Key invariants:
- **Foreground-transfer invariant** — this handler does NOT call `setForegroundProc`. Confirmed by reviewer check (g).
- **Reap race** — the `procByPID[child.pid] == child` check before delete is the MAJOR-fold resolution for the concurrent-reaper race.
- **`child.parent` immutability** — documented. `Process.parent` is set once in `elfSpawn:249` and never reassigned.
- **Blocking waits** — callers who want blocking semantics use `sys_wait` #16 (existing). This decision eliminates the double-reap deadlock path reviewer flagged as MAJOR.

The `select { case ... default: }` pattern is the TinyGo-supported non-blocking channel-receive idiom, already used elsewhere in the kernel (e.g. the net stack). No new primitive.

`writeU32Through` is spec'd in `preempt_user_goroutines.md §4.2`: walks the user's PML4 via `walkAndGetPaddrIn` (at `src/process.go:311`) and writes a u32 through the identity-mapped kernel half.

### 3.4 SDK wrapper (`user/gooos/proc.go`)

```go
const WNOHANG = 1

// Waitpid is the non-blocking sibling of Wait. If WNOHANG is set in
// options and the child is still running, returns (0, false). On reap,
// returns (exitcode, true). On error (bad pid, bad options, etc.),
// returns (negative errno, false).
func Waitpid(pid int, options uint32) (int, bool) {
    var status int32
    r := syscall3(sysWaitpid, uintptr(pid), uintptr(options),
        uintptr(unsafe.Pointer(&status)))
    rs := int64(r)
    if rs < 0 {
        return int(rs), false
    }
    if rs == 0 {
        return 0, false // still running
    }
    return int(status), true // reaped; `rs == pid` confirmed by kernel
}
```

New syscall-number const `sysWaitpid = 34` in `user/gooos/syscall.go:47-54`.

## 4. Commit-per-edit Plan

1. `feat(syscall): sys_waitpid #34 handler + dispatch` — `src/userspace.go` number const, handler, dispatch case; `user/gooos/syscall.go` number const; `user/gooos/proc.go` `Waitpid` wrapper + `WNOHANG` const. Build-only: no user of the new syscall yet.
2. `feat(sh): parser recognises & token and pipeline.background` — `user/cmd/sh/parse.go` tokenize + parsePipeline edits. Shell still always foreground (executor ignores `.background`). Build-only regression-neutral.
3. `feat(sh): jobs table + reap poll` — new `user/cmd/sh/jobs.go` file with `jobEntry`, `jobs[16]`, `reapBackgroundJobs()`. Main loop calls `reapBackgroundJobs()` before each prompt. Still no `&` handling — table is always empty.
4. `feat(sh): executor honors pipeline.background` — `main.go` `executePipeline`/`executeCmdLine`/`executeConcurrentPipe` gain `background` parameter; background path uses `Spawn` only and records in the jobs table. Foreground path unchanged. **This is the visible-behavior commit.**
5. `test(sh): harness for shell background execution` — `scripts/test_shell_background.sh` boots shell under `-smp 1`, issues `hello &` via QEMU monitor sendkey, verifies completion-line appears within 3 s.

## 5. Per-File Edits

Kernel (`/home/ryo/work/gooos/src/`):
- `userspace.go:47-85` — add `sysWaitpid = 34`.
- `userspace.go:95 syscallDispatch` — add `case sysWaitpid: sysWaitpidHandler(frame)`.
- `userspace.go` tail — new `sysWaitpidHandler` per §3.3. Add `const WNOHANG = 1` in the same file (or in `src/process.go` next to `procLock`).
- No changes to `src/process.go` itself; `processWait` and `sys_wait` stay as-is.

User SDK (`/home/ryo/work/gooos/user/gooos/`):
- `syscall.go:47-54` — add `sysWaitpid = 34`.
- `proc.go` — append `const WNOHANG = 1` and `Waitpid` wrapper at the tail.

Shell (`/home/ryo/work/gooos/user/cmd/sh/`):
- `parse.go:5-10` — no change to `cmdLine`.
- `parse.go:14-16` — add `background bool` to `pipeline`.
- `parse.go:20-49 parsePipeline` — after final flush, if last token was `&`, drop it and set `p.background = true`.
- `parse.go:90-125 tokenize` — insert `case '&':` with `&&`-lookahead.
- `main.go:30` — `executePipeline(p, p.background)`.
- `main.go:39 executePipeline` — add `background` param; thread through.
- `main.go:44,47 executeCmdLine / executeConcurrentPipe` — both gain `background` param; when set, skip `gooos.Wait` call, record pid in `jobs` table.
- `main.go:13 main()` REPL — insert `reapBackgroundJobs()` call before `gooos.Print("$ ")`.
- `jobs.go` (NEW) — `jobEntry`, `jobs[16]`, `reapBackgroundJobs()`, `registerJob(pid, cmd)`.

Scripts:
- `scripts/test_shell_background.sh` (NEW).

## 6. Entry Criteria

- `smp-take4` HEAD or later.
- `make build && make lint && make verify-globals` clean.
- Full regression matrix green.
- Reviewing engineer has read the foreground-transfer invariant at `src/process.go:96-102,354-358`.

## 7. Exit Criteria

- `scripts/test_shell_background.sh` PASS under `-smp 1` and `-smp 4`.
- `sh` interactive acceptance:
  - `hello &` prints `[1] <pid> hello` immediately; then a completion line within 1–2 s.
  - `ls | wc &` prints `[1] <pid> ls` immediately; completion line within 1–2 s.
  - `hello` (no `&`) works exactly as before.
  - `hello && ls` prints `"sh: syntax error"`.
  - 17 simultaneous `cpuhog &` calls: first 16 succeed; 17th prints `"sh: too many background jobs"` and runs foreground.
- `scripts/test_net.sh`, `scripts/test_tcp_phase{1..5}.sh`, `scripts/test_gochan.sh`, `scripts/test_pipe_matrix.sh` remain PASS (shell-smoke regression).
- `grep -n 'No &' README.md` — after this feature lands, the "No `&` background jobs" bullet is removed per `preempt_shell_readme_update_plan.md`.

## 8. Rollback

- Primary: `git revert` commit 4 (executor change). Foreground behavior restored; parser still accepts `&` but ignores it (treats as a no-op token that parsePipeline drops). Safe intermediate state.
- Secondary: revert commits 3, 2, 1 in reverse order.

## 9. Risks

- **Leaked background PCBs on shell exit**. If a background child is still running when the shell's `exit` builtin fires, the PCB remains in `procByPID` with a dangling `parent` pointer. `processExit` still runs and drops the ring3 pool slot; `procByPID` entry is never reaped. Mitigation: cap at 16 jobs + documented behavior. A future `exit` builtin enhancement can iterate the jobs table and `Waitpid(pid, 0)` each.
- **Completion-line races with prompt**. If a background job completes mid-`ReadLine`, the completion line interleaves with user input. Mitigation: reap only at prompt boundary (`reapBackgroundJobs()` is called before `gooos.Print("$ ")`, not during `ReadLine`). Minor UX quirk; acceptable.
- **Pipeline-background tail-stage retirement order**. `executeConcurrentPipe` spawns all stages then, for background, registers each pid in the jobs table. The kernel exit order is determined by the pipeline's data flow (stage 1 exits on EOF from stage 0, etc.). The jobs table prints one completion line per stage, so a 3-stage pipeline produces 3 completion lines — one per stage. **Design choice**: acceptable; alternative would be to track only the tail pid, but then intermediate-stage failures are invisible to the user.
- **`sys_waitpid` vs `sys_wait` code duplication**. Both eventually run `<-child.exitCh`. 2.4 does NOT unify them because `sys_wait` still has the foreground-transfer side effect that 2.4 explicitly excludes. Reviewer bullet (g) verifies the non-transfer invariant.
- **Option bit-rot**. The `options` field is `uint32`; only bit 0 (WNOHANG) is defined. Reserved bits must be rejected, not silently dropped, so a future addition (e.g. `WUNTRACED`) cannot accidentally succeed on an old kernel. Test: calling `sys_waitpid(pid, 2, NULL)` returns `fdErrBad`.

## 10. Deliverables

- 5 commits per §4.
- New files: `user/cmd/sh/jobs.go`, `scripts/test_shell_background.sh`.
- Modified: `src/userspace.go`, `user/gooos/syscall.go`, `user/gooos/proc.go`, `user/cmd/sh/parse.go`, `user/cmd/sh/main.go`.
- User-visible: `&` works; `[id] pid done exit=N cmd` notification format.

## 11. Rejected Alternatives

### 11.1 Overload `sys_wait` with a flags argument

Rejected per user decision. Overloading would mean `sys_wait(pid, flags)` with `flags & WNOHANG`. Pro: no new syscall number. Con: ABI churn — every existing caller (SDK's `Wait`) would need updating, and the syscall's behavior changes depending on flags in a way that also should (per design) opt out of foreground transfer. New syscall is cleaner.

### 11.2 Wildcard wait (pid == -1)

Rejected for 2.4. POSIX `waitpid(-1, ...)` reaps any child. Useful for generic reap loops, but the shell's jobs table walks its own pid list; it does not need wildcard semantics. Adding wildcard requires a kernel-side scan of `procByPID` under `procLock` — doable but not needed. Mentioned here so a future extension knows the path.

### 11.3 Job builtins (`fg`, `bg`, `jobs`, `wait`)

Deferred. The jobs table is in place; adding a `jobs` builtin to list it is ~10 LoC. `fg` and `bg` require signal delivery to pause/resume (out of scope for 2.4). `wait` without a pid argument (wait-for-all-background) is a natural extension. All tracked as "Deferred further" in `TODO_SMP5.md`.

## Reviewer MINOR notes

*Populated during the mandatory reviewer pass.*
