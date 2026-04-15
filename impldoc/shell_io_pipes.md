# Shell IO — Pipes (`cmd1 | cmd2`)

This document specifies pipe support: `cmd1 | cmd2`,
`cmd1 | cmd2 | cmd3`. Two semantic flavors are designed —
sequential (no multi-process) as a stop-gap, then concurrent
(true Unix pipes) once `shell_io_multiprocess.md` lands.

Resolves the user-visible piece of the
"shell can't compose programs" gap. Depends on
`shell_io_fd_table.md` for the descriptor model; the
concurrent variant additionally depends on
`shell_io_multiprocess.md`.

## 1. Why two flavors

Sequential pipes ("buffer cmd1's output, then run cmd2 with
that buffer as stdin") are implementable on top of the
single-process kernel that exists today. Phase 3 of the
overview's phasing (`shell_io_overview.md §5`) ships them as
the second user-visible win, before the heavy multi-process
work.

Concurrent pipes ("cmd1 and cmd2 run at the same time, cmd2
reads bytes from cmd1's stdout as they're produced") require
two Ring-3 processes alive at once — i.e., multi-process.
Phase 5 ships them after multi-process lands, replacing the
sequential implementation transparently from the user's
perspective.

The two implementations share the syscall ABI (`sys_pipe`
returns two fds either way) and the userland API. Only the
kernel-side `pipeReader` / `pipeWriter` implementations
differ.

## 2. Sequential pipe (phase 3)

### 2.1 Mechanism

Single-process; the shell orchestrates two sequential exec
calls connected by a kernel-side buffer pipe. The shell
calls `sys_pipe` (same syscall used by the concurrent
variant), the kernel returns two fds backed by a `seqPipe`
implementation whose buffer lives in kernel memory.

```go
// src/pipe.go (new, sequential variant):

const seqPipeMaxBytes = 64 * 1024  // 64 KiB per pipe

type seqPipeBuf struct {
    data   []byte  // grow-as-needed up to seqPipeMaxBytes
    offset int     // read cursor for the reader side
    closed bool    // writer has closed
}

type seqPipeWriter struct{ buf *seqPipeBuf }
type seqPipeReader struct{ buf *seqPipeBuf }

func (w *seqPipeWriter) Write(p []byte) (int, fdErr) {
    if w.buf.closed { return 0, fdErrPipe }
    room := seqPipeMaxBytes - len(w.buf.data)
    n := len(p); if n > room { n = room }
    w.buf.data = append(w.buf.data, p[:n]...)
    return n, fdErrOK
}

func (w *seqPipeWriter) Close() fdErr {
    w.buf.closed = true
    return fdErrOK
}

func (r *seqPipeReader) Read(p []byte) (int, fdErr) {
    if r.buf.offset >= len(r.buf.data) {
        if r.buf.closed { return 0, fdErrEOF }
        // Should never block in sequential pipe — cmd1
        // has already finished by the time cmd2 reads.
        return 0, fdErrEOF
    }
    n := copy(p, r.buf.data[r.buf.offset:])
    r.buf.offset += n
    return n, fdErrOK
}
```

### 2.2 Shell orchestration (sequential)

```go
// In executeCommand for "cmd1 | cmd2":

fds, err := gooos.Pipe()  // [readFd, writeFd]
if err != 0 { ... bail ... }

// Run cmd1 with stdout → write end.
saveStdout()
gooos.Dup2(fds[1], gooos.Stdout)
gooos.Close(fds[1])  // shell no longer needs the write end
gooos.Exec("cmd1.elf", c.argv1)
restoreStdout()

// Now cmd1 is done. Drain its data from the read end into
// stdin for cmd2.
saveStdin()
gooos.Dup2(fds[0], gooos.Stdin)
gooos.Close(fds[0])
gooos.Exec("cmd2.elf", c.argv2)
restoreStdin()
```

The `seqPipe` implementation cooperates with the existing
synchronous `sys_exec`: cmd1 runs to completion, fills the
buffer; cmd2 then runs and drains it.

### 2.3 Caveats of sequential pipes

- **Memory bound**: 64 KiB per pipe (configurable by
  `seqPipeMaxBytes`). Larger outputs lose tail bytes — `Write`
  returns short. Programs that don't tolerate short writes
  (rare) will see truncation.
- **No true concurrency**: a producer-side program that
  never exits (e.g., `cat /dev/zero`) hangs cmd2 forever
  because cmd1 never finishes. Document as known limitation;
  real fix is phase 5.
- **No back-pressure**: the writer fills until the buffer
  cap, then drops. Real pipes block the writer when the
  reader is slow.

These caveats are why phase 5 (concurrent pipe) is the
actual goal. Phase 3 ships the sequential variant because
it's a one-week project that delivers `echo hello | wc -c`
working without waiting for the multi-process refactor.

## 3. Concurrent pipe (phase 5)

### 3.1 Mechanism

Both stages run as independent Ring-3 processes. A kernel
`Pipe` object connects them via a buffered Go channel.
Writer-side `Write` parks (via `<-`) when the channel is
full; reader-side `Read` parks when it's empty. The TinyGo
scheduler handles the parking automatically through chan
operations — no manual coordination needed.

### 3.2 `Pipe` design

```go
// src/pipe.go (concurrent variant; replaces or coexists with seqPipe):

const pipeBufBytes = 4096

type pipe struct {
    ch       chan byte // make(chan byte, pipeBufBytes)
    rdClosed bool      // reader-side close
    wrClosed bool      // writer-side close
}

type pipeReader struct{ p *pipe }
type pipeWriter struct{ p *pipe }

func newPipe() (*pipeReader, *pipeWriter) {
    p := &pipe{ch: make(chan byte, pipeBufBytes)}
    return &pipeReader{p}, &pipeWriter{p}
}

func (r *pipeReader) Read(buf []byte) (int, fdErr) {
    if r.p == nil { return 0, fdErrBad }
    for i := 0; i < len(buf); i++ {
        b, ok := <-r.p.ch
        if !ok {
            if i == 0 { return 0, fdErrEOF }
            return i, fdErrOK
        }
        buf[i] = b
    }
    return len(buf), fdErrOK
}

func (r *pipeReader) Write([]byte) (int, fdErr) {
    return 0, fdErrBad
}

func (r *pipeReader) Close() fdErr {
    // Idempotent (same rationale as pipeWriter.Close below):
    // fd inheritance can land the same *pipeReader in both
    // parent and child Process.fds, so Close may run twice.
    if r.p.rdClosed {
        return fdErrOK
    }
    r.p.rdClosed = true
    // Note: don't close the chan from the reader side; that
    // would race with writer sends. Writers check rdClosed
    // and stop sending instead.
    return fdErrOK
}

func (w *pipeWriter) Write(buf []byte) (int, fdErr) {
    if w.p == nil { return 0, fdErrBad }
    if w.p.rdClosed { return 0, fdErrPipe }
    for i := 0; i < len(buf); i++ {
        // Re-check on every byte for prompt EPIPE delivery.
        if w.p.rdClosed { return i, fdErrPipe }
        w.p.ch <- buf[i]
    }
    return len(buf), fdErrOK
}

func (w *pipeWriter) Read([]byte) (int, fdErr) {
    return 0, fdErrBad
}

func (w *pipeWriter) Close() fdErr {
    // Idempotent: fd inheritance (shell_io_fd_table.md §6)
    // shares the same *pipeWriter between parent and child,
    // so processExit can run Close on both. Without the
    // wrClosed guard the second `close(w.p.ch)` would panic.
    if w.p.wrClosed {
        return fdErrOK
    }
    w.p.wrClosed = true
    close(w.p.ch)
    return fdErrOK
}
```

Both `Close` impls use plain `bool` check-then-set on
`rdClosed` / `wrClosed`. On a single CPU, gooos's
cooperative scheduling makes the sequence atomic for our
purposes (no two goroutines run simultaneously between
yields). SMP v2 would need `atomic.CompareAndSwap`.

**Buffer size: 4096 bytes**, picked because:

- Matches the `sys_write` per-call cap
  (`src/userspace.go:115`).
- Small enough that two stages making forward progress see
  sub-millisecond latency.
- Large enough that bursty producers don't ping-pong the
  scheduler on every byte.

### 3.3 `sys_pipe` syscall

```
sys_pipe(fdpair_ptr) → 0 | -fdErr
  RDI = pointer to two consecutive uint64s in user memory
        (kernel writes [readFd, writeFd])
```

Syscall number 17. See the canonical table in
`shell_io_fd_table.md §5.1` (12=open, 13=close, 14=dup2,
15=spawn, 16=wait, 17=pipe).

```go
// src/userspace.go (new handler):
func sysPipeHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil { frame.RAX = sysFail(fdErrBad); return }
    rd, wr := newPipe()
    rdFd, err := procAllocFD(proc, rd)
    if err != fdErrOK { frame.RAX = sysFail(err); return }
    wrFd, err := procAllocFD(proc, wr)
    if err != fdErrOK {
        procClose(proc, rdFd) // clean up partial allocation
        frame.RAX = sysFail(err); return
    }
    // Write [rdFd, wrFd] into user buffer.
    out := (*[2]uint64)(unsafe.Pointer(frame.RDI))
    out[0] = uint64(rdFd); out[1] = uint64(wrFd)
    frame.RAX = 0
}
```

### 3.4 Refcounting pipe ends

When two processes share a pipe end (e.g., the shell holds
the read end while the child also has it, briefly), the
underlying `pipe` struct must not be freed until both
holders close. This is handled by the conservative GC
naturally — both `pipeReader` instances point at the same
`*pipe`; once neither is referenced from any `Process.fds`,
the GC reclaims everything.

The shell pattern is:

1. `sys_pipe` → shell holds both ends.
2. `sys_dup2(write_end, 1)` → write end now also at fd 1.
3. `sys_close(write_end)` → original slot freed; only fd 1
   holds the write end.
4. `sys_spawn(cmd1)` → cmd1 inherits fd 1 (write end shared
   with shell? No — shell closes its fd 1 below).
5. Shell restores its own fd 1 (`Dup2(savedStdout, 1)`).
6. `sys_dup2(read_end, 0)` → read end now at fd 0.
7. `sys_close(read_end)`.
8. `sys_spawn(cmd2)` → cmd2 inherits fd 0.
9. Shell restores fd 0.
10. After both spawns, only cmd1's fd 1 holds the write end
    and only cmd2's fd 0 holds the read end. Shell holds
    neither.
11. cmd1's `processExit` closes its fds → `pipeWriter.Close`
    closes the chan → cmd2 sees EOF on next `Read`.

The dance ensures the shell does not keep an extra
reference that would prevent EOF delivery. Standard POSIX
hygiene.

### 3.5 Closing-half semantics

| Event | Effect |
|---|---|
| Writer calls `Close` | `close(p.ch)`. Reader's next `<-p.ch` returns `(_, false)`; reader `Read` returns `(0, fdErrEOF)`. |
| Reader calls `Close` | Sets `p.rdClosed = true`. Writer's next `Write` returns `(_, fdErrPipe)`. The chan is NOT closed (writer still holds it; closing twice would panic). |
| Both ends closed | Pipe struct unreferenced; GC reclaims. |
| Process exits without closing | `processExit` walks `Process.fds` and calls `Close` on each (per `shell_io_fd_table.md §7`). Same effect as explicit close. |

EPIPE delivery: when a writer hits a closed reader, the user
gets a negative return value (`-fdErrPipe`). No SIGPIPE
signal — signals are out of scope (`shell_io_overview.md §7`).
The user program sees the failure and decides what to do.

### 3.6 Multi-stage pipelines

`a | b | c` is just nested `sys_pipe`s in the shell:

```go
// Pseudo-code for "a | b | c":
fds_ab := Pipe()
fds_bc := Pipe()

// a: stdout → fds_ab[1]
saveStdout()
Dup2(fds_ab[1], Stdout)
Close(fds_ab[1])
Spawn("a.elf")
restoreStdout()

// b: stdin ← fds_ab[0], stdout → fds_bc[1]
saveStdin(); saveStdout()
Dup2(fds_ab[0], Stdin); Close(fds_ab[0])
Dup2(fds_bc[1], Stdout); Close(fds_bc[1])
Spawn("b.elf")
restoreStdin(); restoreStdout()

// c: stdin ← fds_bc[0]
saveStdin()
Dup2(fds_bc[0], Stdin); Close(fds_bc[0])
Spawn("c.elf")
restoreStdin()

Wait(c)  // shell blocks on the tail process
```

The shell shows up as a transient holder of each pipe end
between `Pipe()` and `Close()`; the discipline of "close the
end you don't own" applies at every step. Each stage is a
separate `sys_spawn` (depends on
`shell_io_multiprocess.md §5`).

## 4. Userland API surface

```go
// user/gooos/io.go (extended):

// Pipe returns [readFd, writeFd] or (nil, errno).
func Pipe() ([2]int, int)

// Spawn / Wait defined in user/gooos/proc.go (see
// shell_io_multiprocess.md §9).
```

## 5. Files to add / modify

| File | Change |
|---|---|
| `src/pipe.go` | **new** — `pipe`, `pipeReader`, `pipeWriter`, `seqPipe*` (phase 3), `newPipe`, `procAllocFD` helper |
| `src/userspace.go` | add `sysPipe` constant (17); add `sysPipeHandler` to dispatch |
| `user/gooos/syscall.go` | add `sysPipe` constant |
| `user/gooos/io.go` | add `Pipe()` wrapper |
| `user/cmd/sh/main.go` | parser handles `\|`; `executeCommand` orchestrates pipe stages |
| `user/cmd/sh/parse.go` | extend `parseLine` to return a slice of `cmdLine` (one per stage) plus pipe markers |

## 6. Verification

**Phase 3 (sequential):**

1. `make build` clean.
2. `echo hello | wc -c` from the shell → prints `6` (5 + newline).
3. `cat hello.txt | wc -l` → prints line count of
   `hello.txt`.
4. Multi-stage smoke: `echo abc | cat | wc -c` →  4.
5. Cap test: send a producer that emits > 64 KiB and confirm
   short-write behavior matches design (no kernel crash).

**Phase 5 (concurrent):**

1. All phase-3 tests still pass.
2. `cat hello.txt | cat` produces identical output to
   `cat hello.txt`.
3. Long-stream stress: a producer that emits 1 MiB to a
   `cat | wc -c` pipeline; assert wc reports 1 MiB and no
   bytes are dropped (concurrent variant has back-pressure;
   sequential would truncate).
4. Reader-close test: a pipeline whose tail exits early
   (e.g., `yes | head -n 5` — once `head` is added);
   producer's next write returns EPIPE, producer exits
   cleanly.
5. 10/10 `bash tmp/test_sendkey.sh` regression.

## 7. Dependencies

- `shell_io_fd_table.md` (foundation for both phases).
- `shell_io_multiprocess.md` (concurrent variant only).
- The redirection design (`shell_io_redirection.md`) is
  independent but shares parser infrastructure — combining
  pipes + redirection (`cat < in | wc > out`) requires the
  parser to handle both in one line.

## 8. Open questions

1. **Should sequential pipes ship if multi-process is
   imminent?** Per `shell_io_overview.md §10 Q1`,
   recommended yes — exercises the fd-table plumbing and
   gives users a working `|` early. Reject only if phase 4
   is committed for the same week.
2. **Pipe buffer reuse on reader close.** When `pipeReader`
   closes, the chan still has buffered bytes. They're
   garbage-collected when the writer also closes (chan
   becomes unreachable). Document as accepted small-scale
   memory waste.
3. **EPIPE without SIGPIPE.** Real Unix sends SIGPIPE on
   write to a closed pipe; we return EPIPE only. Programs
   that don't check write return values will silently lose
   output. Document as a known limitation; matches the "no
   signals" decision in `shell_io_overview.md §7`.
4. **Pipe end inheritance across `sys_spawn`**. Children
   inherit the parent's fd table (per
   `shell_io_fd_table.md §6`). The discipline in §3.4
   relies on the shell carefully closing the ends it does
   not want the child to inherit. Standard POSIX; document
   prominently in the shell code.

## 9. Risk register delta

- **Retires** (phase 3): `R-shell-no-pipes-sequential`.
- **Retires** (phase 5): `R-shell-no-pipes-concurrent`.
- **Adds**:
  - `R-pipe-buffer-truncate` (phase 3 only) — sequential
    pipe drops bytes past `seqPipeMaxBytes`. Documented;
    retired by phase 5.
  - `R-pipe-end-leak` — if the shell forgets to close a
    pipe end before spawning the next stage, the child
    inherits an extra reference and the chan never closes.
    Mitigated by the rigorous close-after-dup2 discipline
    in §3.4.
  - `R-pipe-deadlock` — if a producer fills a 4 KiB chan
    and the consumer never reads (e.g., crashes before
    `Read`), the producer parks forever. Mitigated by
    `processExit` walking the fd table and closing the
    consumer's reader, which sets `rdClosed = true` and
    unparks the producer with EPIPE on its next write.
