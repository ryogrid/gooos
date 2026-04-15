# Shell IO — Overview and Execution Map

This document is the entry point to the design set for three
related shell-IO capabilities that the gooos kernel does not
yet support:

1. **Redirection** — `cmd > file`, `cmd < file`, `cmd >> file`.
2. **Pipes** — `cmd1 | cmd2`. Both a sequential variant ("buffer
   stdout from stage 1, then run stage 2 against that buffer")
   and the standard concurrent variant ("both stages run at the
   same time").
3. **Multi-process execution** — concurrent Ring-3 processes
   that can hold pipe ends, share a parent shell, and exit
   independently.

The four topic-specific files under `impldoc/shell_io_*.md`
each design one slice. This document is the coverage map and
the dependency DAG.

## 1. Inventory of blockers

Phase-1 exploration confirmed the following blockers in the
shipped kernel + userland. Each is mapped to the topic doc
that resolves it:

| # | Blocker | Location | Resolved in |
|---|---|---|---|
| B1 | `elfExec` synchronously waits on `<-child.exitCh` (no spawn-without-wait) | `src/process.go:222` | `shell_io_multiprocess.md §3` |
| B2 | `savedParent` is a single global `SavedMapping` ("v1 supports one level of exec nesting") | `src/process.go:55` | `shell_io_multiprocess.md §4` |
| B3 | All processes share a single CR3 / PML4; concurrent procs at `userStackBase=0x7FFF0000` and `argPageVaddr=0x40300000` would collide | `src/vm.go` mapPage/unmapPage; `src/boot.S:71-95` | `shell_io_multiprocess.md §2` |
| B4 | `sys_read` is hardcoded to `keyboardCh` (no `fd` arg) | `src/userspace.go:137-187` | `shell_io_fd_table.md §3` |
| B5 | No `Process.fds` table; `Process` struct has no IO indirection | `src/process.go:24` | `shell_io_fd_table.md §2` |
| B6 | `ring3StackPool` caps real concurrency at 32 slots | `src/ring3_pool.go:21` | `shell_io_multiprocess.md §6` (documented as v1 ceiling, no design change) |
| B7 | Stdin source is a single global `keyboardCh`; multi-proc reads would race | `src/keyboard_irq.go`, `src/userspace.go:145` | `shell_io_multiprocess.md §7` (foreground model) |

## 2. Corrections to the original problem framing

Phase-1 exploration surfaced four facts the design must
respect that were not obvious from a casual read:

1. **`sys_write` already takes `fd`** in RDX
   (`src/userspace.go:113`). Today fd=0 means "VGA + serial",
   fd=1 means "serial only". This is non-POSIX. The fd-table
   refactor repurposes the same parameter as POSIX-style
   stdout/stderr — wire-level signature unchanged, but
   **fd=0 gains stdin semantics**, so any user code that
   passes 0 to `sys_write` (today's
   `user/gooos/io.go:11` `Print`) now hits a read-only fd
   and the call returns `-fdErrBad`. The mandatory user-
   binary rebuild (`make embed-user`) updates `Print` to
   pass 1 (POSIX stdout) at the same time. Cross-ref:
   `shell_io_fd_table.md §5.3`.
2. **`sys_read` has no `fd` parameter** — `(buf, max)`
   only. Adding `fd` is a real ABI change. **User-confirmed
   decision: rebuild user binaries.** The single-line shift
   in `user/gooos/io.go` `ReadLine()` propagates via
   `make embed-user`.
3. **`cat` / `wc` / `ls` / `hello` read files via
   `sys_fs_read(name)`**, not via generic file descriptors
   (`user/cmd/cat/main.go`, `user/cmd/wc/main.go`,
   `user/cmd/ls/main.go`). They keep working unchanged for
   redirection and pipes — only the **shell** needs new
   plumbing for file descriptors. Programs that write via
   `gooos.Println()` already go through `sys_write` and so
   automatically observe redirected stdout.
4. **vaddr-sliding for multi-process is non-viable.** Every
   user ELF links at the same `0x400000+` PT_LOAD vaddr
   (`src/elf.go`'s loader maps wherever the ELF says), so
   two processes in the same PML4 collide on `.text`. The
   only viable option is **per-process PML4** with shared
   kernel mappings; vaddr-sliding is recorded as
   "considered and rejected" in
   `shell_io_multiprocess.md §2.3`.

## 3. The five documents

| File | Lines | Scope |
|---|---|---|
| `shell_io_overview.md` (this file) | ~200 | inventory, DAG, ordering, decisions, risk delta |
| `shell_io_fd_table.md` | ~250 | `Process.fds`, `FileDesc` interface, `sys_open`/`close`/`dup2`, `sys_read` ABI extension |
| `shell_io_redirection.md` | ~150 | shell parser changes; open + dup2 + close + exec sequence |
| `shell_io_pipes.md` | ~250 | `Pipe` object via `chan byte`; `sys_pipe`; sequential vs concurrent semantics |
| `shell_io_multiprocess.md` | ~400 | per-process PML4, CR3 swap, `elfExec` decomposition, `sys_spawn`/`sys_wait`, foreground model |

Every doc cites `file:line` for source references, has explicit
`Dependencies` / `Verification` / `Open questions` subsections,
and ends with a `Risk register delta`. The syscall ABI
extension is described in **exactly one place**
(`shell_io_fd_table.md`); other docs cross-reference.

## 4. Dependency DAG

```
                   shell_io_fd_table  (foundation)
                          │
        ┌─────────────────┼─────────────────────┐
        v                 v                     v
 shell_io_redirection   shell_io_pipes      shell_io_multiprocess
   (open + dup2)         (Pipe object)        (per-proc PML4,
                            │                  spawn + wait)
                            │                       │
                            │       ┌───────────────┘
                            v       v
                       concurrent pipes
                     (= pipes ∩ multiprocess)
```

- `shell_io_fd_table.md` is the foundation. Nothing else
  works without `Process.fds` and `sys_open`/`close`/`dup2`.
- `shell_io_redirection.md` needs only the fd table; ships
  in a single-process world (the existing synchronous
  `elfExec` is fine because redirection runs sequentially).
- `shell_io_pipes.md`'s **sequential** variant needs only
  the fd table — no concurrency; acceptable as a first
  user-visible pipe.
- `shell_io_pipes.md`'s **concurrent** variant + any
  background-job future work both depend on
  `shell_io_multiprocess.md`.
- `shell_io_multiprocess.md` is the heaviest doc — per-proc
  PML4, CR3 swap on `gooosOnResume`, `elfExec` decomposition.
  Independent of redirection but most natural after the fd
  table is in place.

## 5. Recommended implementation phasing

Smallest user-visible deliverable first:

1. **Phase 1 — fd table** (`shell_io_fd_table.md`).
   Foundation; no user-visible behavior change yet but
   enables everything else. Includes a `fd_probe` ELF for
   verification.
2. **Phase 2 — redirection** (`shell_io_redirection.md`).
   First user-visible win. `echo hi > out.txt` works.
3. **Phase 3 — sequential pipe** (`shell_io_pipes.md §3`).
   `cmd1 | cmd2` works with a buffer-and-replay
   implementation; no multi-process required. Memory
   bounded by max-buffer constant.
4. **Phase 4 — multi-process foundation**
   (`shell_io_multiprocess.md`). Per-process PML4,
   `sys_spawn`/`sys_wait`, foreground-only stdin. This is
   the heavy phase; budget several days.
5. **Phase 5 — concurrent pipe**
   (`shell_io_pipes.md §4`). `Pipe` via `chan byte` runs
   stages truly concurrently. Multi-stage `a | b | c`
   works. Stress test: `cat hello.txt | cat | wc -c`.

Phases 1–3 are independent commits and each ship something
useful; phase 4 is the structural change; phase 5 is the
payoff. Phases 4 and 5 may be sequenced in either order if
phase 4 lands but phase 5 work is interrupted.

## 6. Decisions resolved before design (user-confirmed)

| # | Decision | Resolution |
|---|---|---|
| D1 | PML4-per-process vs vaddr-sliding | **PML4-per-process**; vaddr-sliding non-viable (overlapping ELF link addresses) |
| D2 | Pipe buffer: `chan byte` vs custom ring | **`chan byte`** — matches existing `fsReqCh` / `keyboardCh` patterns; scheduler-friendly via the runtime's chan parking; lower implementation risk |
| D3 | Maximum fd count | **16** — 3 stdio + up to 4 redirects + 8 pipe ends headroom; small enough to inherit cheaply on exec |
| D4 | `sys_read` ABI compatibility | **Clean break, rebuild user binaries** — single-line change in `user/gooos/io.go`; `make embed-user` propagates to embedded ELFs |
| D5 | Foreground / background model | **Foreground-only** — most recently spawned, non-pipe-driven process gets keyboard. Background jobs (`&`, `jobs`, `fg`, `bg`) explicitly out of scope |

## 7. Out of scope (further-deferred work)

- Job control (`&`, `jobs`, `fg`, `bg`) — needs a real
  process-group abstraction.
- Signals (SIGINT on Ctrl-C, SIGPIPE) — interacts with
  goroutine scheduling in ways not yet designed.
- `2>` / `&>` / heredoc syntax in the shell parser.
- `select` / `poll` syscalls.
- `seek`, `lseek`, file modes beyond read/write/append.
- True POSIX file ownership / permissions.
- TinyGo runtime fork (R-tinygo-fork-divergence boundary).
- Any change to the existing chan-based IPC primitives.

These are not designed in this round; they are listed here so
a future contributor reading the design set knows what was
deliberately deferred.

## 8. Risk register delta summary

Per-doc `Risk register delta` sections provide detail; below
is the aggregate.

**Retired (when each phase lands):**

- `R-shell-no-redirection` — retired by phase 2.
- `R-shell-no-pipes-sequential` — retired by phase 3.
- `R-shell-no-multiprocess` — retired by phase 4.
- `R-shell-no-pipes-concurrent` — retired by phase 5.
- `R-savedparent-global` (existing risk for nested exec) —
  retired by phase 4.

**Added (implementation-time risks):**

- `R-fd-table-leak` — fds not closed on process exit could
  leak Pipe objects (mitigated by `processExit` walking
  `Process.fds`).
- `R-pml4-kernel-share-correctness` — every per-process PML4
  must point at the same kernel-half PD/PT physical pages;
  any divergence breaks ISR delivery. Mitigated by a single
  `pml4Init(*Process)` helper that all spawn paths use.
- `R-cr3-swap-cost` — `mov %cr3` flushes the entire TLB on
  every goroutine resume to a Ring-3 task. Cost is small
  (< 100 cycles + page-walk re-fill) but measurable;
  documented as accepted.
- `R-foreground-stdin-policy` — model is "most recently
  spawned, non-pipe-driven process". May surprise users
  in edge cases (e.g., shell built-in interleaves with
  child); documented in `shell_io_multiprocess.md §7`.

## 9. Reviewer-pass follow-ups (resolved)

A `general-purpose` reviewer ran against all five
`shell_io_*.md` files. Returned 3 CRITICAL + 6 MAJOR + 6
MINOR. All CRITICAL and MAJOR addressed in-place; MINOR
items folded in or recorded below:

**CRITICAL (fixed in-place):**

1. `gooosOnResume` map access inside `//go:nosplit` —
   resolved by caching `*Process` on the `gInfo` struct
   so the resume hook still does exactly one map lookup.
   See `shell_io_multiprocess.md §3.3`.
2. Kernel writing into child pages via vaddrs that are
   only mapped in the child PML4 — resolved by an
   explicit "always write via paddr" rule in
   `shell_io_multiprocess.md §3.2`.
3. `pipeWriter.Close` double-close panic when fd
   inheritance shares the writer between parent and
   child — resolved by idempotent close guards on both
   `pipeReader.Close` and `pipeWriter.Close`. See
   `shell_io_pipes.md §3.3`.

**MAJOR (fixed in-place):**

4. Stale `file:line` citations across multiple docs —
   regenerated against the actual source state.
5. `sys_read` legacy-shape guard — dropped (the
   user-binary rebuild is the real protection;
   sentinel-RDX heuristic is unreliable). See
   `shell_io_fd_table.md §14`.
6. `fileFd` offset semantics on inheritance — pinned
   to POSIX (shared offset, shared `*fileFd`); prose
   reworded. See `shell_io_fd_table.md §6`.
7. TinyGo `%rip`-relative assumption — verified via
   `objdump -d user/build/hello.elf` that TinyGo emits
   absolute 32-bit immediates (e.g.,
   `mov $0x40100342, %edi`). The whole
   `userPML4Base`-relocation idea is dropped; the
   per-process PML4 design now keeps user vaddrs at
   their link-time `0x40100000+` values and gives
   each process its own PT entries instead. See
   `shell_io_multiprocess.md §2`.
8. Syscall-number conflict — single canonical table in
   `shell_io_fd_table.md §5.1`; sibling docs cite by
   number rather than re-defining.
9. `procByTask` first-resume race — resolved by the
   `gInfo.proc` cache + the explicit `writeCR3` inside
   `ring3Wrapper` itself. The `gi == nil` short-circuit
   on the first resume is documented as safe because
   wrapper prologue only touches kernel-half memory.

**MINOR (folded or recorded):**

10. OpenBuffer pseudocode in pipes §2.1 — deleted; only
    the kernel-side `seqPipe` design remains.
11. Overview §2 — fd=0 read-only behavior change after
    the rebuild propagated as an explicit warning.
12. fd_table §10 — Process struct insertion points
    pinned (lines 24, 163-167).
13. Multiprocess §11 — `foregroundProc` placement
    pinned to `src/process.go`.
14. fd_table §10 — `fsAppend` listed as a real
    prerequisite (not a footnote) since `fsWrite` is
    replace-all and can't back the new modes.
15. Multiprocess §8.2 — boot-shell-never-exits
    citation corrected to `src/elf.go:237`.

## 10. Open questions (overview level)

1. **Should sequential pipe (phase 3) ship at all if
   phase 4 is also planned?** It's a stop-gap that gets
   replaced by phase 5. Recommended: yes — provides early
   user value and exercises fd-table plumbing under load
   before MP lands. Reject only if phase 4 is committed to
   land in the same week.
2. **Reaping orphans in foreground-only model.** Without
   a background-job concept, an orphan can only happen if
   the boot shell exits while a child is alive. The boot
   shell is designed never to exit (`setupUserspace`
   `for { hlt() }`). Practically there are no orphans;
   document and move on.
3. **Userland API surface for fd-aware I/O.** The
   `user/gooos/` Go runtime today wraps syscalls in
   `Print` / `ReadLine` / `Exec`. After the fd table, do
   we expose `Open` / `Close` / `Dup2` / `Pipe` / `Spawn`
   / `Wait` as first-class? Recommended: yes — see
   `shell_io_fd_table.md §6`.
