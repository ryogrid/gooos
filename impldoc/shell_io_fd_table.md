# Shell IO — Per-Process File Descriptor Table

This document specifies the foundation every other shell-IO
feature rests on: a per-process file-descriptor table inside
`Process`, an extensible `FileDesc` interface, and the
syscall-ABI extension that exposes them.

Resolves blockers **B4** (sys_read hardcoded to keyboard) and
**B5** (no Process.fds) from `shell_io_overview.md §1`.
Independent of multi-process and pipes; ships first.

## 1. Why the fd table is foundational

Today every IO syscall has a hardwired backend:

- `sysWriteHandler` (`src/userspace.go:110-127`) writes to VGA
  + serial unconditionally; the `fd` argument in RDX merely
  toggles VGA on/off (fd=0 means VGA + serial; fd=1 means
  serial only).
- `sysReadHandler` (`src/userspace.go:137-187`) reads from
  the global `keyboardCh` line-buffered, with no `fd`
  parameter at all.

Redirection wants `sys_write` to a file descriptor that maps
to a file. Pipes want `sys_read` / `sys_write` to map to a
shared in-kernel buffer. Neither is expressible with the
current shape. A `Process.fds` table indexed by a small
integer gives both features the indirection they need with no
ad-hoc plumbing.

## 2. `Process.fds` field

Add a fixed-size array of file-descriptor slots to
`Process` (`src/process.go:24`):

```go
const procMaxFDs = 16

type Process struct {
    // ... existing fields ...

    fds [procMaxFDs]FileDesc // nil = closed slot
}
```

**Rationale for `procMaxFDs = 16`:**

- 3 stdio (0=stdin, 1=stdout, 2=stderr).
- 4 redirect headroom (`>`, `<`, `>>`, plus follow-up `2>`).
- 8 pipe-end headroom (4 stages × 2 ends; in practice multi-
  stage pipelines close intermediate ends as soon as they're
  duped).
- A fixed-size array avoids `make([]FileDesc, ...)` allocation
  on every spawn and copies cheaply on `exec` inheritance.
- Larger N (e.g., 64) buys little for shell workloads and
  inflates the per-process struct.

`procMaxFDs` is a `const` so any future growth is a single
recompile.

## 3. `FileDesc` interface

A single Go interface so syscall handlers stay polymorphic
and new backends (pipes, sockets, future devices) drop in
without touching the dispatch table:

```go
// src/fd.go (new file)

type FileDesc interface {
    // Read up to len(buf) bytes into buf. Returns (n, err).
    // err == fdErrEOF when the underlying source is exhausted.
    // Blocks the calling goroutine if no data is available
    // and the descriptor's read end is not closed.
    Read(buf []byte) (int, fdErr)

    // Write len(buf) bytes from buf. Returns (n, err).
    // err == fdErrPipe when the read end is closed.
    Write(buf []byte) (int, fdErr)

    // Close releases descriptor-specific resources.
    // Idempotent; multiple closes return nil.
    Close() fdErr
}

type fdErr uint64

const (
    fdErrOK   fdErr = 0
    fdErrEOF  fdErr = 1
    fdErrPipe fdErr = 2
    fdErrBad  fdErr = 3 // bad fd, invalid pointer, etc.
)
```

`fdErr` is a `uint64` instead of Go `error` so the syscall
handler can pass it through to user space as a sentinel
return value (`-fdErrXxx` for failures), avoiding any
allocation in error paths.

## 4. Concrete `FileDesc` implementations (initial set)

Three impls land with this phase; pipe ends are added by
`shell_io_pipes.md`; future device descriptors slot in the
same way.

### 4.1 `consoleStdin`

Wraps the existing line-buffered keyboard read. Replaces the
current ad-hoc body of `sysReadHandler`.

```go
type consoleStdin struct{}

func (consoleStdin) Read(buf []byte) (int, fdErr) {
    // Existing line-buffer logic from sysReadHandler:
    // block on <-keyboardCh, echo, accumulate until Enter,
    // copy out.
    return readKeyboardLine(buf), fdErrOK
}

func (consoleStdin) Write([]byte) (int, fdErr) {
    return 0, fdErrBad
}

func (consoleStdin) Close() fdErr { return fdErrOK } // no-op
```

A single instance (package-scope `var stdinFD = consoleStdin{}`)
is shared — no per-process state.

### 4.2 `consoleStdout`

Wraps VGA + serial output. Replaces the current body of
`sysWriteHandler` for fd=1 (POSIX stdout).

```go
type consoleStdout struct {
    toVGA bool // true for fd=0/1, false for fd=2 (stderr → serial only)
}

func (c consoleStdout) Write(buf []byte) (int, fdErr) {
    for i := 0; i < len(buf); i++ {
        if c.toVGA {
            vgaConsolePutChar(buf[i])
        }
        serialPutChar(buf[i])
    }
    return len(buf), fdErrOK
}

func (consoleStdout) Read([]byte) (int, fdErr) {
    return 0, fdErrBad
}

func (consoleStdout) Close() fdErr { return fdErrOK }
```

Two package-scope instances:
`stdoutFD = consoleStdout{toVGA: true}` (fd 1) and
`stderrFD = consoleStdout{toVGA: false}` (fd 2).

### 4.3 `fileFd`

Wraps a name in the in-memory FS (`src/fs.go`) plus an offset
and a mode. Read/write delegate to existing `fsRead` / `fsWrite`
helpers; write semantics depend on mode (truncate vs append).

```go
type fileMode uint8

const (
    fileModeRead   fileMode = 1
    fileModeWrite  fileMode = 2 // truncate on open
    fileModeAppend fileMode = 3 // O_APPEND
)

type fileFd struct {
    name   string
    offset int
    mode   fileMode
}

func (f *fileFd) Read(buf []byte) (int, fdErr) {
    if f.mode != fileModeRead {
        return 0, fdErrBad
    }
    data := fsSendRead(f.name) // existing helper
    if data == nil {
        return 0, fdErrBad
    }
    if f.offset >= len(data) {
        return 0, fdErrEOF
    }
    n := copy(buf, data[f.offset:])
    f.offset += n
    return n, fdErrOK
}

func (f *fileFd) Write(buf []byte) (int, fdErr) {
    switch f.mode {
    case fileModeWrite:
        // Append-into-buffer semantics; flush at close. For v1
        // we can write through (each Write replaces the file
        // tail) since the FS is in-memory.
        return fsAppend(f.name, buf), fdErrOK
    case fileModeAppend:
        return fsAppend(f.name, buf), fdErrOK
    default:
        return 0, fdErrBad
    }
}

func (f *fileFd) Close() fdErr { return fdErrOK }
```

`fsAppend` may need adding to `src/fs.go` if the existing
`fsWrite` is "replace whole file" rather than "append". The
implementation choice is one Go function, ~10 lines.

`fileFd` is heap-allocated per `sys_open`; closed slots set
`Process.fds[i] = nil` so the conservative GC reclaims them
after `processExit` walks the table.

## 5. Syscall ABI extension

### 5.1 New syscall numbers

Add to `src/userspace.go` constants block (`src/userspace.go:46`)
and `user/gooos/syscall.go:27`:

**Canonical syscall table (single source of truth):**

| # | Name | Args | Defined in |
|---|---|---|---|
| 0–11 | (existing) | unchanged | `src/userspace.go:46-58` |
| 12 | `sys_open`  | `(path_ptr, path_len, mode) → fd \| -fdErr` | `shell_io_fd_table.md` |
| 13 | `sys_close` | `(fd) → 0 \| -fdErr`                        | `shell_io_fd_table.md` |
| 14 | `sys_dup2`  | `(oldfd, newfd) → newfd \| -fdErr`          | `shell_io_fd_table.md` |
| 15 | `sys_spawn` | `(path_ptr, path_len, args_ptr, args_len) → pid \| -fdErr` | `shell_io_multiprocess.md §5.1` |
| 16 | `sys_wait`  | `(pid) → exit_code \| -fdErr`               | `shell_io_multiprocess.md §5.2` |
| 17 | `sys_pipe`  | `(fdpair_ptr) → 0 \| -fdErr`                | `shell_io_pipes.md §3.3` |

`sys_read` (slot 2, existing) keeps its number but
**gains an `fd` first argument** in this round — see
§5.2 below.

Sibling docs reference this table by number. **No two
syscalls share a number.**

### 5.2 Modified `sys_read` ABI (BREAKING)

Today: `sysRead(buf, max)` — RDI=buf, RSI=max.
After:  `sysRead(fd, buf, max)` — RDI=fd, RSI=buf, RDX=max.

Decision (per `shell_io_overview.md §6 D4`): clean break,
rebuild user binaries.

User-side change required (single line in
`user/gooos/io.go:24`):

```go
func ReadLine() string {
    var buf [128]byte
    n := syscall3(sysRead, 0, uintptr(unsafe.Pointer(&buf[0])), 128)
    return string(buf[:n])
}
```

`syscall2` → `syscall3`; first arg now explicit `0` (stdin fd).
After change, `make embed-user` re-emits all user ELFs
(`scripts/embed_elfs.sh`); kernel and userland agree on the
new layout.

No other shipped user binary calls `sys_read` — `cat` / `wc`
read files via `sys_fs_read(name)` (`user/cmd/cat/main.go`,
`user/cmd/wc/main.go`); `ls` uses `sys_fs_list`; `hello` only
writes. Only `sh` and the new `fd_probe` care.

### 5.3 `sys_write` ABI repurpose (NON-BREAKING)

Today: `sysWrite(buf, len, fd)` — RDI=buf, RSI=len, RDX=fd
where fd ∈ {0, 1} controls whether VGA is also written.

After: same wire signature, but fd values gain POSIX meaning:

| fd | Today | After |
|---|---|---|
| 0 | VGA + serial | stdin (read end only — `Write` returns -fdErrBad) |
| 1 | serial only | stdout (default = VGA + serial unless redirected) |
| 2 | (unused) | stderr (= serial only by convention) |
| 3+ | (unused) | per-process file / pipe descriptors |

Existing user binaries call `sys_write` with fd=0 to mean
"print to console". After repurposing they call it with
fd=1 (POSIX stdout). The change is a single-line edit in
`user/gooos/io.go:11` (`Print` passes 1 instead of 0) plus
the same in `user/rt0.S` `write` stub if any. **This IS a
behavioral change for shipped binaries: anything previously
hardcoding fd=0 to mean console-print now means stdin.**

Because the user-binary rebuild (§5.2) is already required by
`sys_read`, this rebuild is the same `make embed-user` cycle.
Document the rebuild in TODO_DEF.md / README.md.

### 5.4 New syscall handlers (sketch)

```go
// sysOpenHandler: src/userspace.go (new)
func sysOpenHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    name := readUserString(frame.RDI, frame.RSI)
    mode := fileMode(frame.RDX)
    fd, err := procOpen(proc, name, mode)
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    frame.RAX = uintptr(fd)
}

// sysCloseHandler:
func sysCloseHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    err := procClose(proc, int(frame.RDI))
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    frame.RAX = 0
}

// sysDup2Handler:
func sysDup2Handler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    err := procDup2(proc, int(frame.RDI), int(frame.RSI))
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    frame.RAX = frame.RSI
}

// sysFail packs a fdErr into RAX as -err so userspace sees a
// negative return value.
func sysFail(e fdErr) uintptr { return ^uintptr(uint64(e)) + 1 }
```

`procOpen`, `procClose`, `procDup2` live in `src/fd.go` along
with the `FileDesc` interface and helpers.

### 5.5 Modified `sysReadHandler` and `sysWriteHandler`

Both handlers become trivial dispatchers through the fd table:

```go
func sysReadHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    fd := int(frame.RDI)
    buf := frame.RSI
    maxLen := frame.RDX
    fdObj := procGetFD(proc, fd)
    if fdObj == nil { frame.RAX = sysFail(fdErrBad); return }

    // Read into a kernel scratch buffer (no direct user write
    // from inside FileDesc impls — keeps them backend-agnostic).
    var scratch [256]byte
    n, err := fdObj.Read(scratch[:min(int(maxLen), 256)])
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    for i := 0; i < n; i++ {
        *(*byte)(unsafe.Pointer(buf + uintptr(i))) = scratch[i]
    }
    frame.RAX = uintptr(n)
}
```

`sysWriteHandler` mirrors it. Scratch buffer cap (256) is
small; large transfers loop. Sized to fit on the kernel
stack without growing per-frame use.

## 6. fd inheritance on `exec`

When the shell calls `sys_exec` (or future `sys_spawn`), the
child inherits the parent's `Process.fds` table:

```go
// Inside elfExec / elfSpawn, immediately after the child
// Process is allocated (currently src/process.go:163-167
// for elfExec; mirrored for elfSpawn):
for i := 0; i < procMaxFDs; i++ {
    child.fds[i] = parent.fds[i]
}
```

The slot copy is shallow: parent and child point at the
**same** `FileDesc` interface value. Concrete consequences:

- `consoleStdin` / `consoleStdout` are package-scope
  singletons; the share is intentional and inert.
- `*pipeReader` / `*pipeWriter` (defined in
  `shell_io_pipes.md §3.2`) are shared. The reference
  count is implicit in the fd table (each `Process.fds`
  entry holding the pointer counts as a hold);
  `processExit` walking the table and calling `Close`
  drops the references. The `Close` impls are
  idempotent (see `shell_io_pipes.md §3.3`) so a parent
  that closes after the child has already closed is
  safe.
- `*fileFd` is shared. **The file offset is shared
  too** (POSIX semantics: parent and child see the
  same cursor). For the shell's redirection use case
  this is fine — the shell `dup2`'s and `close`'s
  before exec, so the child holds the only reference.
  If a future caller wants per-process offset, the
  caller must clone (call `sys_open` again rather than
  inherit). Document.

After spawn the **shell** restores its own fds (it dup2'd
the redirected one onto fd 1, so it must restore fd 1 to
`stdoutFD` before the next command). Standard POSIX
practice; lives in shell code, not the kernel.

**Close-on-exec is deferred.** No `O_CLOEXEC` flag,
no `fcntl`. Listed in `shell_io_overview.md §7`.

## 7. fd lifecycle in `processExit`

Walk the table on exit and call `Close()` on every non-nil
slot. Currently `processExit` (`src/process.go:242`) only
frees user pages and pool slot; this adds:

```go
for i := 0; i < procMaxFDs; i++ {
    if proc.fds[i] != nil {
        proc.fds[i].Close()
        proc.fds[i] = nil
    }
}
```

This is what closes the pipe ends so the other end sees EOF
(see `shell_io_pipes.md §3.5`). Without it, dangling pipe
goroutines block forever.

## 8. Boot-time initialization of `Process.fds` for the shell

The boot shell's `Process` struct (allocated in `src/elf.go`'s
`elfLoad`, currently around line 190) is the only one not
inheriting from a parent. Initialize its fds to console
defaults:

```go
// In elfLoad after the Process is allocated:
proc.fds[0] = stdinFD          // POSIX stdin
proc.fds[1] = stdoutFD         // POSIX stdout
proc.fds[2] = stderrFD         // POSIX stderr
// fds[3..15] left nil
```

All subsequent exec'd processes inherit through §6 above.

## 9. Userland API surface (`user/gooos/`)

After the kernel side ships, expose first-class wrappers:

```go
// user/gooos/io.go (extended)
const (
    Stdin  = 0
    Stdout = 1
    Stderr = 2
)

// Already-redefined to take fd explicitly:
func Write(fd int, buf []byte) int { ... }
func Read(fd int, buf []byte) int  { ... }

// New:
func Open(name string, mode int) (fd int, err int)
func Close(fd int) error
func Dup2(oldfd, newfd int) error
```

The shell uses these directly when building redirections /
pipes. The existing `Print` / `Println` / `ReadLine` keep
their old signatures and forward to `Write(Stdout, …)` /
`Read(Stdin, …)` — back-compat for the rest of the userland.

## 10. Files to add / modify

| File | Change |
|---|---|
| `src/fd.go` | **new** — `FileDesc` interface, `fdErr`, `consoleStdin`, `consoleStdout`, `fileFd`, `procOpen` / `procClose` / `procDup2` / `procGetFD` helpers |
| `src/process.go` | add `fds [16]FileDesc` to `Process`; extend `processExit` to walk + close fds; child fd inheritance in `elfExec` |
| `src/userspace.go` | add `sysOpen` / `sysClose` / `sysDup2` constants and dispatch; rewrite `sysReadHandler` / `sysWriteHandler` to dispatch through fds; add `sysFail` helper |
| `src/elf.go` | initialize boot-shell stdio fds in `elfLoad` |
| `src/fs.go` | **add `fsAppend(name, data) int`** — the existing `fsWrite` (`src/fs.go`) replaces the whole file, so neither `fileModeWrite` (write-through) nor `fileModeAppend` can reuse it. `fsAppend` extends a file by `len(data)` bytes; `fileFd.Write` calls it in both modes. ~15 lines |
| `user/gooos/syscall.go` | add `sysOpen` / `sysClose` / `sysDup2` constants |
| `user/gooos/io.go` | rewrite `ReadLine` to pass `Stdin`; expose `Open` / `Close` / `Dup2` / `Read` / `Write` first-class |
| `user/cmd/fd_probe/main.go` | **new** — opens `hello.txt`, reads, writes via the new fds; registered in `scripts/embed_elfs.sh` |

## 11. Verification

1. `make build` clean.
2. `bash tmp/test_sendkey.sh` 10 trials green — confirms
   shell still works after the `sys_read` / `sys_write` ABI
   shift.
3. Boot, run the new `fd_probe` ELF (from the shell):
   - Opens `hello.txt` (already in the embedded FS).
   - Reads bytes via `Read(fd, buf)`.
   - Writes them to stdout via `Write(Stdout, buf)`.
   - Closes the fd.
   - Calls `sys_open` on a missing file; expects an error.
4. `make stress_test.sh` regression unchanged.
5. Manual smoke: `cat hello.txt` (uses `sys_fs_read`, not
   the fd table — should be unaffected, proving the
   `sys_fs_read` path is not regressed).

## 12. Dependencies

- None. This is the foundation.
- All four sibling docs (`shell_io_redirection.md`,
  `shell_io_pipes.md`, `shell_io_multiprocess.md`) depend
  on this one.

## 13. Open questions

1. **Should `Read` / `Write` syscalls block or return
   short reads?** Today `sys_read` blocks on `keyboardCh`
   line-buffered. After fd table, `consoleStdin` keeps
   that semantics; `pipeReader.Read` will block on its
   chan (see `shell_io_pipes.md §3.4`); `fileFd.Read`
   never blocks (returns full data or EOF). Document
   "blocking depends on fd type" — match POSIX where
   simplest.
2. **Is the 256-byte kernel scratch buffer in `sysReadHandler`
   too small?** A `cat` reading a 30 KiB ELF goes via
   `sys_fs_read` (whole-file copy with a 4 KiB cap), not
   `sys_read`. For pipe / console reads, 256 bytes per
   syscall is fine — Go's `bufio` would loop. Revisit if
   a real workload pushes against this.
3. **`fileFd` write-mode semantics.** The in-memory FS
   today is "replace whole file" via `fsWrite`. `fileFd`
   in `fileModeWrite` accumulates via repeated
   `fsAppend`. If a program opens, partially writes, then
   crashes, the file is left half-populated. Acceptable
   for an in-memory FS that doesn't survive reboot.

## 14. Risk register delta

- **Retires**: `R-shell-no-fd` (implicit prerequisite of
  every shell-IO feature).
- **Adds**:
  - `R-fd-table-leak` — if a syscall handler returns
    without freeing a partially-allocated fd (e.g.,
    `sys_open` succeeds but the user buffer copy fails),
    the slot leaks until `processExit`. Mitigated by the
    sequence "allocate slot last; never partially
    allocate".
  - `R-sys-read-abi-break` — every shipped binary that
    calls `sys_read` must rebuild. Mitigated by the
    `make embed-user` rebuild step in the implementation
    plan: every user ELF lives in this repo and is
    re-emitted into `src/user_binaries.go` in one
    command, so kernel and userland always ship together.
    No runtime guard exists or is needed (a sentinel
    "RDX==0 means legacy" check is unreliable because
    callers may pass 0 as a legitimate `max` argument).
