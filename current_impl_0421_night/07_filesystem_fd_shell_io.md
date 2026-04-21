# Filesystem, FD Table, Pipes, Keyboard, and Shell I/O Semantics

## In-Memory Filesystem (`src/fs.go`)

Core model:

- flat namespace, fixed slots (`maxFiles = 32`)
- fixed per-file capacity (`maxFileData = 262144` bytes)
- storage in global `fs FileSystem` (static arrays)

Primary operations:

- `fsCreate`, `fsWrite`, `fsRead`, `fsAppend`, `fsTruncate`, `fsSize`, `fsList`, `fsDelete`

Service-mode API:

- `fsTask` goroutine handles serialized requests via `fsReqCh`
- wrappers: `fsSendCreate`, `fsSendWrite`, `fsSendRead`, `fsSendList`, `fsSendDelete`

## FD Layer (`src/fd.go`)

`FileDesc` interface:

- `Read([]byte) (int, fdErr)`
- `Write([]byte) (int, fdErr)`
- `Close() fdErr`

Per-process table:

- `Process.fds [procMaxFDs]FileDesc`
- `procMaxFDs = 16`

Concrete FD backends:

- `consoleStdin`
- `consoleStdout`
- `fileFd`
- pipe endpoints from `src/pipe.go`
- `socketFd` from `src/netsock.go`

## Keyboard Ownership and stdin Semantics

`consoleStdin.Read` enforces foreground ownership:

- if `currentProc() != getForegroundProc()`, returns EOF (`fdErrEOF`)
- actual line input read through keyboard channel and line buffer (`readKeyboardLine`)

Implication:

- concurrent Ring 3 processes do not race for keyboard input
- parent/child foreground transfer is mediated by wait path

## Pipe Semantics (`src/pipe.go`)

- byte-stream pipe backed by channel buffering
- reader/writer refcount helpers integrated with FD duplication and inheritance
- close behavior follows expected stream semantics:
  - writer close eventually yields EOF to reader
  - reader close causes writer-side error behavior on write path

## Shell I/O Behavior

Boot and command execution path in `src/main.go` + `user/cmd/sh`:

- boot shell and command ELFs are materialized into in-memory FS
- shell parses and executes command pipelines and redirections
- background mode (`&`) exists with job tracking
- stdin to background process is constrained by foreground ownership policy

## FD/FS Invariants

1. FD slot operations must always validate table bounds and nil state.
2. FD inheritance for spawn is selective: socket descriptors are intentionally excluded.
3. `fsTask` serialization must be used where ordering matters across goroutines.
4. foreground process ownership must be transitioned atomically with process wait/reap behavior.

## Capacity and Failure Notes

- FS has hard cap on file count and per-file size.
- Append/truncate behaviors are bounded by fixed-size backing arrays.
- No persistent backing store; reboot resets filesystem state.
