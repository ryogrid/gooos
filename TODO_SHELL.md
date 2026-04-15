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

- [ ] **1b** — `fileFd` + `fsAppend`.
  - [ ] Add `fsAppend(name string, data []byte) int` to
    `src/fs.go`.
  - [ ] `fileFd` struct + `Read` / `Write` / `Close` in
    `src/fd.go` with `fileModeRead` / `fileModeWrite` /
    `fileModeAppend`.
  - [ ] Verify: `make build` clean.
  - [ ] Verify: 10/10 sendkey.

- [ ] **1c** — syscall ABI extension + user-binary rebuild.
  - [ ] `sys_open` (12), `sys_close` (13), `sys_dup2` (14)
    constants + handlers in `src/userspace.go`.
  - [ ] `sysReadHandler` / `sysWriteHandler` rewritten to
    dispatch through `Process.fds`.
  - [ ] `sysFail(fdErr) uintptr` helper.
  - [ ] `user/gooos/syscall.go` constants for new syscalls.
  - [ ] `user/gooos/io.go`: `Print` passes `Stdout`;
    `ReadLine` passes `Stdin` (3-arg syscall); add
    `Open` / `Close` / `Dup2` / `Read(fd,…)` /
    `Write(fd,…)` first-class wrappers.
  - [ ] `make embed-user` re-emits all five user ELFs.
  - [ ] **Single commit** containing kernel + userland
    + regenerated `src/user_binaries.go`.
  - [ ] Verify: `make build` clean.
  - [ ] Verify: 10/10 sendkey (existing shell + binaries
    work end-to-end through the new fd path).

- [ ] **1d** — `fd_probe` ELF.
  - [ ] `user/cmd/fd_probe/main.go`: opens `hello.txt`,
    reads via `Read(fd, buf)`, writes to stdout via
    `Write(Stdout, buf)`, closes; tries opening missing
    file and prints expected error.
  - [ ] Embedded via `scripts/embed_elfs.sh`.
  - [ ] Verify: shell command `fd_probe` round-trips file
    content; serial log shows expected output and error.
  - [ ] Verify: 10/10 sendkey.

## Phase 2 — redirection

- [ ] **2** — shell redirection (`<`, `>`, `>>`).
  - [ ] `user/cmd/sh/parse.go` (new): `tokenize`,
    `parseLine`, `cmdLine` struct, `errSyntax`.
  - [ ] `user/cmd/sh/main.go` `executeCommand` rewritten
    to handle `cmdLine`; `saveStdio` / `restoreStdio` dance
    around exec.
  - [ ] Failure paths: syntax error, open failure both
    print to stderr and skip the command.
  - [ ] New harness `tmp/test_redirect.sh`:
    `echo hi > out.txt`; `cat out.txt`; `echo more >> out.txt`;
    `wc < out.txt`.
  - [ ] Verify: harness passes; `make build` clean;
    10/10 sendkey.

## Phase 3 — sequential pipe

- [ ] **3** — sequential pipe + `sys_pipe`.
  - [ ] `src/pipe.go` (new): `seqPipeBuf`, `seqPipeReader`,
    `seqPipeWriter`, `newSeqPipe()`.
  - [ ] `sys_pipe` (17) constant + handler in
    `src/userspace.go`; `procAllocFD` allocates the two
    fds with rollback on partial failure.
  - [ ] Userland: `gooos.Pipe()` in `user/gooos/io.go`.
  - [ ] Shell parser handles `|` (single-stage); pipe
    orchestration in `executeCommand`.
  - [ ] New harness `tmp/test_pipe.sh`:
    `echo hello | wc -c` → 6.
  - [ ] Verify: harness passes; 10/10 sendkey.

## Phase 4 — multi-process foundation

- [ ] **4a** — `writeCR3` helper.
  - [ ] 3-line `writeCR3` in `src/stubs.S`
    (`movq %rdi, %cr3; ret`).
  - [ ] `src/cr3.go` (new): `//go:linkname writeCR3 writeCR3`
    + `//go:nosplit func writeCR3(uintptr)`.
  - [ ] Verify: `make build` clean; 10/10 sendkey
    (writeCR3 not yet called from anywhere).

- [ ] **4b** — variant page-table helpers.
  - [ ] `mapPageInto(pml4, vaddr, paddr, flags)` in
    `src/vm.go`.
  - [ ] `unmapPageFrom(pml4, vaddr)`.
  - [ ] `walkAndGetPaddrIn(pml4, vaddr) uintptr`.
  - [ ] Existing helpers refactored to thin wrappers
    over the *In variants (or kept as-is — pick whichever
    minimizes diff).
  - [ ] Verify: `make build` clean; 10/10 sendkey.

- [ ] **4c** — `newProcPML4` / `freeProcPML4`.
  - [ ] Allocate fresh PML4 + per-process PDP page; set
    `procPDP[0] = bootPDP[0]` by value (shared kernel
    identity map).
  - [ ] `freeProcPML4(pml4)` walks per-process PDP
    entries (skips index 0), frees PT/PD/PDP/PML4 pages.
  - [ ] Lives in `src/process.go` or new
    `src/proc_pml4.go`.
  - [ ] Verify: `make build` clean; 10/10 sendkey
    (helpers not yet wired into spawn).

- [ ] **4d** — `gInfo.proc` cache + `gooosOnResume` CR3 swap.
  - [ ] Add `proc *Process` field to `gInfo` in
    `src/goroutine_tss.go:27`.
  - [ ] `registerRing3GWithStack(stackTop, proc)` 2-arg
    signature.
  - [ ] `gooosOnResume` calls
    `writeCR3(gi.proc.pml4)` if `gi.proc != nil &&
    gi.proc.pml4 != 0`. **No second map lookup**.
  - [ ] Document the first-resume `gi == nil`
    short-circuit invariant.
  - [ ] Update `ring3Wrapper` to pass `proc` into
    `registerRing3GWithStack`.
  - [ ] Verify: `make build` clean; 10/10 sendkey
    (no behavior change yet because Process.pml4 is
    always 0 — verified by 10/10 trials).

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
