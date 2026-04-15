# TODO_SHELL — Shell IO Implementation

Tracks every concrete work item from `impldoc/shell_io_*.md`.
Order follows `shell_io_overview.md §5` (foundation →
redirection → sequential pipes → multi-process foundation →
concurrent pipes).

Mark `- [x]` only after the implementation **and** its
verification step pass. One commit per top-level item.

User-confirmed pre-decisions (`shell_io_overview.md §6`):
- D1 PML4-per-process; D2 chan-byte pipe; D3 16 fds;
  D4 clean rebuild for sys_read ABI; D5 foreground-only stdin.

TinyGo amd64 codegen re-verified 2026-04-15:
`objdump -d user/build/hello.elf` shows
`mov $0x40100342, %edi` — absolute 32-bit immediates.
The per-process PML4 design (keep user vaddrs at link-time
`0x40100000+`, give each process its own PT entries) is sound.

---

## Phase 0 — Bootstrap

- [x] Bootstrap commit lands TODO_SHELL.md (`7ababf9`).

## Phase 1 — fd table foundation

- [x] **1a** — `FileDesc` interface + `Process.fds`.
  - [x] New `src/fd.go`: `FileDesc` interface,
    `fdErr` enum (`OK`/`EOF`/`Pipe`/`Bad`), helpers
    `procGetFD` / `procAllocFD` / `procClose` / `procDup2`.
  - [x] Concrete impls `consoleStdin`, `consoleStdout`,
    package-scope singletons (`stdinFD`, `stdoutFD`,
    `stderrFD`).
  - [x] `Process.fds [16]FileDesc` field added; Process
    struct comment updated.
  - [x] `processExit` calls `procCloseAll` before pool
    release.
  - [x] Boot-shell stdio fds initialized in
    `src/elf.go:elfLoad` via `procInitStdio`.
  - [x] Verify: `make build` clean.
  - [x] Verify: 10/10 `bash tmp/test_sendkey.sh`.

- [x] **1b** — `fileFd` + `fsAppend`.
  - [x] Added `fsAppend`, `fsTruncate`, `fsSize` helpers to
    `src/fs.go`.
  - [x] `fileFd` struct + `openFileFd` constructor + `Read`
    / `Write` / `Close` in `src/fd.go` with `fileModeRead`
    / `fileModeWrite` (truncate-on-open) / `fileModeAppend`
    (POSIX O_APPEND-style).
  - [x] Verify: `make build` clean.
  - [x] Verify: 10/10 sendkey.

- [x] **1c** — syscall ABI extension + user-binary rebuild.
  - [x] `sys_open` (12), `sys_close` (13), `sys_dup2` (14)
    constants + handlers in `src/userspace.go`.
  - [x] `sysReadHandler` / `sysWriteHandler` rewritten to
    dispatch through `Process.fds` (via 256-byte kernel
    scratch buffer per chunk).
  - [x] `sysFail(fdErr) uintptr` helper (`src/fd.go`).
  - [x] `user/gooos/syscall.go` constants for new syscalls.
  - [x] `user/gooos/io.go` rewritten: POSIX `Stdin`/`Stdout`
    /`Stderr` consts; `Open`/`Close`/`Dup2`/`Read`/`Write`
    first-class; `Print` passes `Stdout`; `ReadLine` passes
    `Stdin` (3-arg).
  - [x] `user/rt0.S` `write` stub simplified — the new
    kernel ABI matches C's `write(fd, buf, count)`
    directly so the register shuffle is removed.
  - [x] **fd inheritance fix**: `elfExec` shallow-copies
    `parent.fds` into `child.fds` (caught by sendkey
    regression: `cat hello.txt` was silently failing
    because the child had nil fds). Per
    `shell_io_fd_table.md §6`.
  - [x] `make embed-user` re-emitted all five user ELFs.
  - [x] Single commit covers kernel + userland.
  - [x] Verify: `make build` clean (lint + verify-globals
    green).
  - [x] Verify: 10/10 sendkey (`pf=0 exit=3 cat=1` — full
    end-to-end through new fd path).

- [x] **1d** — `fdprobe` ELF (renamed from `fd_probe`
  because the gooos keyboard handler doesn't decode shift +
  minus, so `_` is unreachable from the sendkey harness;
  documented inline).
  - [x] `user/cmd/fdprobe/main.go`: opens `hello.txt`,
    reads via `Read(fd, buf)`, writes to stdout via
    `Write(Stdout, buf)`, closes; tries opening
    `nope.txt` and confirms negative return.
  - [x] Embedded via `scripts/embed_elfs.sh` (entry added
    to `user/Makefile` `CMDS`).
  - [x] `src/main.go` writes `fdprobe.elf` into the FS
    alongside the other user binaries.
  - [x] Verify: `tmp/test_fd_probe.sh` PASS:
    `contents=1 read_write=1 err=1 pf=0`.
  - [x] Verify: 10/10 sendkey.

## Phase 2 — redirection

- [x] **2** — shell redirection (`<`, `>`, `>>`).
  - [x] `src/keyboard.go` shift handling: tracks
    left/right shift make/break, shifted ASCII table
    for symbols and uppercase letters. Verification
    prerequisite (gives the sendkey harness `<`, `>`,
    `|`, `_`, etc.).
  - [x] `user/cmd/sh/parse.go` (new): `tokenize`,
    `parseLine`, `cmdLine` struct, `joinArgs`.
  - [x] `user/cmd/sh/main.go` `executeCommand` (now
    `executeCmdLine`) handles `cmdLine`; saved-stdio
    dup2 dance via slots 10/11.
  - [x] Failure paths: syntax error, open failure both
    print to stderr (`gooos.Println` for v1; serial only
    until stderr split) and skip the command.
  - [x] `user/rt0.S`: `memmove` added (now needed by
    `runtime.sliceAppend` for the parser's `[]string`
    slices).
  - [x] `src/fs.go` `maxFileData` bumped 40 KiB → 64 KiB;
    `sh.elf` grew to ~47 KiB after the parser landed.
  - [x] New harness `tmp/test_redirect.sh`:
    `echo hello > out.txt; cat out.txt` produces
    `hello` on serial.
  - [x] Verify: `bash tmp/test_redirect.sh` PASS
    (`hello_lines=1 pf=0`); 10/10 sendkey.

- [x] **3** — sequential pipe + `sys_pipe`.
  - [x] `src/pipe.go` (new): `seqPipeBuf`, `seqPipeReader`,
    `seqPipeWriter`, `newSeqPipe()` — sequential variant
    (writer fills, reader drains, no concurrency).
    Idempotent `Close` on both ends to survive fd
    inheritance.
  - [x] `sys_pipe` (17) constant + handler in
    `src/userspace.go`; `procAllocFD` allocates the two
    fds with rollback if the second one fails.
  - [x] `user/gooos/syscall.go` `sysPipe` constant.
  - [x] `user/gooos/io.go` `Pipe()` returns
    `(rfd, wfd, errno)`.
  - [x] Shell parser refactored: `parsePipeline` splits on
    `|` into per-stage `cmdLine`s; `tokenize` recognizes
    `|` as a token. `executePipeline` dispatches:
    1-stage → existing redirect path, 2-stage →
    `executeTwoStagePipe`, 3+ stages → "not supported in
    this round" message (handled in phase 5).
  - [x] `executeTwoStagePipe` orchestrates fd dance:
    save stdout, dup2(wfd→stdout), exec stage 1, restore
    stdout, close wfd (writer-done now), save stdin,
    dup2(rfd→stdin), exec stage 2, restore stdin,
    close rfd. Order matters — closing wfd before stage 1
    runs would mark the writer done prematurely (writes
    still proceed but the discipline is misleading).
  - [x] `user/cmd/cat/main.go` extended: with no
    filename arg, reads stdin in 256-byte chunks until
    EOF and writes to stdout (POSIX `cat`). Lets the
    pipe harness validate end-to-end data flow with no
    new ELF needed.
  - [x] New harness `tmp/test_pipe.sh`:
    `echo hello | cat` produces `hello` on serial.
  - [x] Verify: harness PASS (`pf=0 exit=1
    hello_lines=1`); 10/10 sendkey.

## Phase 4 — multi-process foundation

- [x] **4a** — `writeCR3` helper.
  - [x] 3-line `writeCR3` in `src/stubs.S`
    (`movq %rdi, %cr3; ret`).
  - [x] `src/cr3.go` (new): `//go:linkname writeCR3 writeCR3`
    + `//go:nosplit func writeCR3(uintptr)`.
  - [x] Verify: `make build` clean; 10/10 sendkey
    (writeCR3 not yet called from anywhere — ISR-lint
    sees no caller; future 4d wires it).

- [x] **4b** — variant page-table helpers.
  - [x] `mapPageInto(pml4, vaddr, paddr, flags)` in
    `src/vm.go`.
  - [x] `unmapPageFrom(pml4, vaddr)` (no `invlpg` —
    caller's TLB doesn't have the entry; CR3 swap will
    flush when the proc actually runs).
  - [x] `walkAndGetPaddrIn(pml4, vaddr) uintptr`.
  - [x] Existing CR3-reading helpers kept as-is to
    minimize diff (kernel paths still use them).
  - [x] Verify: `make build` clean; 10/10 sendkey
    (helpers unused so far; lint sees them as unused).

- [x] **4c** — `newProcPML4` / `freeProcPML4`.
  - [x] `src/proc_pml4.go` (new). `newProcPML4` allocs a
    fresh PML4 page and copies the boot PML4[0] entry
    verbatim into it (shares the boot PDP — and
    therefore the 0..1 GiB identity map — by reference).
    `pml4SharedKernelPDP` cached on first use.
  - [x] `freeProcPML4(pml4)` walks PML4[1..511] (skips
    [0] which is shared) → freePDP → freePD → freePage.
    User physical pages themselves are freed by
    processExit's existing UserPaddrs walk; this only
    releases the table machinery.
  - [x] Verify: `make build` clean; 10/10 sendkey
    (helpers unused — wired in 4e).

- [x] **4d** — `gInfo.proc` cache + `gooosOnResume` CR3 swap.
  - [x] Added `proc *Process` field to `gInfo`.
  - [x] `registerRing3GWithStack(stackTop, proc)` 2-arg
    signature; ring3Wrapper passes `proc`.
  - [x] `gooosOnResume` calls `writeCR3(gi.proc.pml4)`
    when `gi.proc != nil && gi.proc.pml4 != 0`. Still
    one map lookup (`gInfoByTask[t]`); the rest is
    pointer-load + asm — nosplit-safe.
  - [x] First-resume `gi == nil` invariant documented in
    the function's comment.
  - [x] Added `Process.pml4 uintptr` field (zero until
    4e populates it). `gooosOnResume` short-circuits CR3
    swap when pml4 == 0, so this commit ships the hook
    behavior with no functional change yet.
  - [x] Verify: `make build` clean; 10/10 sendkey
    (Process.pml4 is always 0 in this commit; pipe
    harness PASS unchanged).

- [ ] **4e** — `elfSpawn` + `processWait` split.
  - [ ] Split `elfExec` into `elfSpawn(name, args, parent)
    → (*Process, fdErr)` + `processWait(*Process)
    → (uintptr, fdErr)`. `elfExec` becomes
    `spawn + wait`.
  - [ ] Add `Process.pml4`, `Process.pid`, `procByPID
    map[uint32]*Process`, `nextPID uint32 = 1`,
    `allocPID`.
  - [ ] `registerProc(*Process)` / `unregisterProc(*Process)`
    helpers update both `procByTask` and `procByPID`
    (resolves multiprocess.md §14 Q3).
  - [ ] `elfSpawn` allocates `child.pml4 = newProcPML4()`,
    populates user mappings via `mapPageInto`, writes
    PT_LOAD bytes via paddr only.
  - [ ] `ring3Wrapper` calls `writeCR3(proc.pml4)` after
    `tssSetRSP0ForCurrentG`, before `jumpToRing3`.
  - [ ] `processExit` calls `freeProcPML4(proc.pml4)`
    after page free.
  - [ ] Verify: `make build` clean; 10/10 sendkey (single
    process exec via the new path; no concurrency yet).

- [ ] **4f** — `savedParent` removal.
  - [ ] Drop the global `savedParent SavedMapping` in
    `src/process.go`.
  - [ ] Drop the save block in `elfExec`/`elfSpawn` and
    the restore block in `processExit`.
  - [ ] `SavedMapping` type can be removed if no other
    references.
  - [ ] Verify: 10/10 sendkey (each process now in its
    own PML4 — sanity check the prior single-process
    behavior is unaffected).

- [ ] **4g** — `sys_spawn` + `sys_wait` wiring.
  - [ ] `sys_spawn` (15), `sys_wait` (16) constants +
    handlers in `src/userspace.go`.
  - [ ] Userland: `Spawn(name, args) (pid, errno)` and
    `Wait(pid) (exitCode, errno)` in
    `user/gooos/proc.go`. `Exec` becomes a thin
    `spawn + wait` wrapper.
  - [ ] Verify: `make build` clean; 10/10 sendkey
    (existing shell still uses `Exec` which preserves
    semantics).
  - [ ] Verify: probe ELF (`spawn_probe`) spawns two
    instances of `hello`, both produce serial output
    concurrently.

- [ ] **4h** — foreground stdin model.
  - [ ] `foregroundProc *Process` package-scope in
    `src/process.go`; `setForegroundProc` /
    `getForegroundProc` accessors.
  - [ ] `consoleStdin.Read` returns `(0, fdErrEOF)` when
    `currentProc() != getForegroundProc()`.
  - [ ] Shell sets foreground around `Spawn`/`Wait`
    sequences (its own PID when at prompt; child PID
    when waiting).
  - [ ] Verify: 10/10 sendkey (shell remains the
    foreground when reading the prompt; children take
    over while running).

## Phase 5 — concurrent pipe

- [ ] **5** — concurrent pipe (`chan byte`).
  - [ ] Replace `seqPipe*` in `src/pipe.go` with `pipe`
    struct backed by `chan byte` (4 KiB cap).
  - [ ] `pipeReader.Read` parks on `<-p.ch` until data
    or EOF (chan close).
  - [ ] `pipeWriter.Write` parks on `p.ch <-` when full;
    re-checks `p.rdClosed` per byte for prompt EPIPE.
  - [ ] **Idempotent `Close`** on both ends (guard with
    `wrClosed` / `rdClosed`).
  - [ ] Multi-stage shell pipelines via nested
    `sys_pipe` + `sys_spawn` + close-the-end-you-don't-own.
  - [ ] Stress harness extends `tmp/test_pipe.sh`:
    `cat hello.txt | cat | wc -c` agrees with
    `cat hello.txt | wc -c`.
  - [ ] Verify: 10/10 sendkey; harness passes.

## Phase C — reviewer pass

- [ ] Launch general-purpose reviewer subagent.
  - [ ] CRITICAL findings addressed inline.
  - [ ] MAJOR findings addressed inline.
  - [ ] MINOR findings: fixed or recorded in
    `## Reviewer follow-ups (MINOR)` below with rationale.
  - [ ] Second pass if first surfaces > 3 design-level
    issues.

## Phase D — Final reconciliation

- [ ] All items in this file are `- [x]`.
- [ ] `git log` shows one commit per implemented item
  (plus reviewer-fix commits).
- [ ] Repo-wide `Grep` for `TODO|FIXME|XXX|HACK` over
  `src/`, `user/`, `scripts/`, `Makefile`, `target.json`:
  no new hits.
- [ ] Repo-wide `Grep` for `unimplemented|not implemented`:
  same.
- [ ] Final sendkey: 10/10 `make run` + each new
  harness script (`test_redirect.sh`, `test_pipe.sh`).
- [ ] `README.md` updated:
  - [ ] Progress table extended with rows for fd table,
    redirection, pipes, multi-process.
  - [ ] New syscalls documented (`sys_open`,
    `sys_close`, `sys_dup2`, `sys_spawn`, `sys_wait`,
    `sys_pipe`).
  - [ ] `sys_read` ABI shift documented (3-arg).
  - [ ] Retired risks listed.
  - [ ] New `make` targets / harness scripts noted.
- [ ] Final report to user.

## Reviewer follow-ups (MINOR)

(empty — populated by Phase C if any minor issues are
deferred rather than fixed)

## Further deferred

(empty — populated if a feature must slip out of this
task's scope; include reason + unlock condition)
