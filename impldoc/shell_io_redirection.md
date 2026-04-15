# Shell IO — Redirection (`>`, `<`, `>>`)

This document specifies redirection support in the shell:
`cmd > file`, `cmd < file`, `cmd >> file`. The smallest
user-visible win after the fd table lands.

Depends on `shell_io_fd_table.md` (file-descriptor table,
`sys_open` / `sys_close` / `sys_dup2`, `fileFd` impl). Independent
of pipes and multi-process — ships in a single-process world
because the existing synchronous `sys_exec` is fine for
sequential redirection.

## 1. Problem statement

The shell at `user/cmd/sh/main.go:32-52` has a single dispatch
mode: parse `cmd args` into two strings, run the built-in or
exec `cmd.elf`. There is no way to send a child's stdout to a
file or feed a file's contents to a child's stdin. Workflows
like `echo hello > out.txt` followed by `cat out.txt` are
impossible today.

The fd-table foundation (`shell_io_fd_table.md`) gives the
shell the primitives it needs — `Open`, `Dup2`, `Close`,
`Exec` already inherit the fd table. The remaining work is
shell-side parsing and orchestration.

## 2. Scope

In scope:

- `cmd ARG... > FILE` — stdout to file (truncate on open).
- `cmd ARG... >> FILE` — stdout to file (append).
- `cmd ARG... < FILE` — stdin from file.
- Exactly one redirection per side (either `>` or `>>`,
  optionally `<`). Multiple redirections (`> a < b`) are
  supported.
- Order independence: the parser accepts any of
  `cmd args > out`, `cmd > out args`, `cmd args > out < in`
  and treats the redirection target as the token immediately
  following the operator.

Out of scope (recorded in `shell_io_overview.md §7`):

- `2>` stderr redirection.
- `&>` / `>&` stdout+stderr combination.
- Heredocs (`<<EOF`).
- File descriptor numbers as targets (`2>&1`).
- Globs / variable expansion / quoting (the existing parser
  already doesn't support these).

## 3. Shell parser changes

The current parser (`user/cmd/sh/main.go:23-30`) splits on the
first space into `cmd` and `args` strings. Redirection requires
tokenization. New parser:

```go
// user/cmd/sh/parse.go (new file)

type cmdLine struct {
    argv      []string // [0] = command name, [1..] = args
    stdinFile string   // "" if no '<'
    stdoutFile string  // "" if no '>'/'>>'
    appendOut  bool    // true if '>>' (vs '>')
}

func parseLine(line string) (cmdLine, error) {
    toks := tokenize(line)
    var c cmdLine
    for i := 0; i < len(toks); i++ {
        switch toks[i] {
        case ">":
            if i+1 >= len(toks) { return c, errSyntax }
            c.stdoutFile = toks[i+1]; c.appendOut = false
            i++
        case ">>":
            if i+1 >= len(toks) { return c, errSyntax }
            c.stdoutFile = toks[i+1]; c.appendOut = true
            i++
        case "<":
            if i+1 >= len(toks) { return c, errSyntax }
            c.stdinFile = toks[i+1]
            i++
        default:
            c.argv = append(c.argv, toks[i])
        }
    }
    if len(c.argv) == 0 { return c, errSyntax }
    return c, nil
}

// tokenize splits on whitespace AND treats '<', '>', '>>' as
// standalone tokens even when not whitespace-separated, so
// `cat>out` parses as ["cat", ">", "out"].
func tokenize(line string) []string { /* simple state machine */ }
```

The existing `parseCommand` (one-line split) is replaced by
`parseLine`. The `executeCommand` dispatcher receives a
`cmdLine` instead of two strings.

## 4. Execution sequence

For each command line that has at least one redirection, the
shell runs this sequence between `parseLine` and the existing
`gooos.Exec` call:

```go
// In executeCommand, after parsing:

// 1. Save the shell's current stdio fds so we can restore them
//    after exec returns. Built-in commands run in-process and
//    would inherit the redirected fds otherwise.
//    `sys_dup2` returns the new fd; we ignore it (it equals
//    the requested target).
const (
    savedStdinFD  = 10  // safe slot above 2 and below maxFDs
    savedStdoutFD = 11
)

if c.stdinFile != "" || c.stdoutFile != "" {
    gooos.Dup2(gooos.Stdin,  savedStdinFD)
    gooos.Dup2(gooos.Stdout, savedStdoutFD)
}

// 2. Open the redirection targets and dup2 onto stdio.
if c.stdinFile != "" {
    fd, err := gooos.Open(c.stdinFile, gooos.OpenRead)
    if err != 0 {
        gooos.Println("sh: " + c.stdinFile + ": cannot open")
        restoreStdio(c)
        return
    }
    gooos.Dup2(fd, gooos.Stdin)
    gooos.Close(fd)
}
if c.stdoutFile != "" {
    mode := gooos.OpenWrite
    if c.appendOut { mode = gooos.OpenAppend }
    fd, err := gooos.Open(c.stdoutFile, mode)
    if err != 0 {
        gooos.Println("sh: " + c.stdoutFile + ": cannot open")
        restoreStdio(c)
        return
    }
    gooos.Dup2(fd, gooos.Stdout)
    gooos.Close(fd)
}

// 3. Run the command (built-in or exec). Both inherit the
//    redirected fds via Process.fds inheritance on exec or
//    via the shell's own fds for built-ins.
runCommand(c.argv)

// 4. Restore the shell's stdio so subsequent prompts and
//    built-ins see the console again.
restoreStdio(c)
```

`restoreStdio` walks back: `Dup2(savedStdoutFD, Stdout)`,
`Close(savedStdoutFD)`, same for stdin. Idempotent; only
acts if the saved-slot is non-empty.

For commands without redirection the shell skips the whole
dance — zero overhead on the happy path.

## 5. Built-in vs external commands

- **Built-ins** (`echo`, `help`, `clear`) run in the shell's
  own process. After the dup2 dance they automatically write
  to the redirected fd via `gooos.Println` →
  `Write(Stdout, …)`. This is exactly the behavior we want:
  `echo hi > out.txt` does the right thing for free.
- **External commands** are exec'd via `gooos.Exec`. The child
  inherits `Process.fds` per the rule in
  `shell_io_fd_table.md §6`, so its fd 1 already points at
  the file. No special handling.

## 6. Failure paths

| Failure | Behavior |
|---|---|
| `>` / `<` / `>>` not followed by a token | Shell prints "sh: syntax error: missing redirect target" and skips the command. |
| `<` target doesn't exist | `sys_open` returns `-fdErrBad`; shell prints "sh: FILENAME: cannot open" and skips. |
| `>` / `>>` target's parent directory missing | The in-memory FS is flat (no directories per `src/fs.go`), so this can't happen. If a future hierarchical FS introduces it, the same `cannot open` path covers it. |
| `>` overwrites a file currently open elsewhere | The in-memory FS allows concurrent writes; last writer wins. Document as known limitation. Not a concern in single-process v1. |
| Built-in opens succeed but command fails (e.g., `echo` succeeds; the file is truncated) | Standard POSIX: redirection happens before command runs, so `cmd args > out` truncates `out` regardless of whether `cmd` produces output. Match this. |

## 7. New userland helpers (`user/gooos/`)

Already specified in `shell_io_fd_table.md §9`:

```go
// user/gooos/io.go

const (
    Stdin  = 0
    Stdout = 1
    Stderr = 2
)

const (
    OpenRead   = 1
    OpenWrite  = 2
    OpenAppend = 3
)

func Open(name string, mode int) (fd int, errno int)
func Close(fd int) int
func Dup2(oldfd, newfd int) int
```

The shell uses `Open` with the appropriate mode; redirection
is just three syscalls per side.

## 8. Files to add / modify

| File | Change |
|---|---|
| `user/cmd/sh/parse.go` | **new** — `parseLine`, `tokenize`, `cmdLine` struct, `errSyntax` |
| `user/cmd/sh/main.go` | rewrite `executeCommand` to use `parseLine`; insert dup2 dance for redirected commands; `restoreStdio` helper |
| `user/cmd/sh/builtin.go` | **new (refactor)** — split `cmdHelp`, `cmdEcho`, `cmdClear` into their own file so `main.go` stays the orchestrator |
| `user/gooos/io.go` | already extended in `shell_io_fd_table.md §9`; no further change |

No kernel-side changes for redirection — all the kernel
plumbing is in the fd-table phase.

## 9. Verification

1. `make build` clean.
2. Boot kernel; from the shell:
   - `echo hello > out.txt`
   - `cat out.txt` → prints `hello`.
3. `echo more >> out.txt` then `cat out.txt` → prints
   `hello\nmore` (or however newlines compose).
4. `wc < out.txt` → prints line / word / byte count of the
   file contents (validates `<` redirect into a child that
   reads stdin via `sys_read(Stdin, …)`).
5. Syntax-error path: `echo hi >` → shell prints
   "sh: syntax error" and returns to prompt without
   crashing.
6. Missing-file path: `cat < missing.txt` → shell prints
   "sh: missing.txt: cannot open" and returns to prompt.
7. 10/10 `bash tmp/test_sendkey.sh` regression — the
   existing harness doesn't use redirection but must
   continue to work.

A new harness (`tmp/test_redirect.sh`) optionally extends the
sendkey pattern: send `echo hi > out.txt`, then `cat out.txt`,
then assert "hi" appears in serial output.

## 10. Dependencies

- `shell_io_fd_table.md` (foundation).
- No dependency on pipes or multi-process.

## 11. Open questions

1. **Truncation on `>` open.** `fileFd` in
   `fileModeWrite` should truncate on `sys_open`. The
   in-memory FS today (`src/fs.go`) has `fsCreate` (which
   truncates) and `fsWrite` (which replaces all data).
   `procOpen` for `OpenWrite` should call `fsCreate(name)`
   first to ensure truncation, then return a `fileFd` with
   offset 0. Document this in `shell_io_fd_table.md §4.3`'s
   `fileFd` initialization.
2. **`>>` semantics on a missing file.** POSIX `>>` creates
   the file if missing. Match this — `procOpen` for
   `OpenAppend` calls `fsCreate(name)` if the file doesn't
   exist, then returns a `fileFd` with offset = current
   file length.
3. **Multiple `>` to the same file in one command.** Last
   one wins (matches POSIX). Parser currently accepts only
   one `stdoutFile` slot; second `>` overwrites the first.
   Document.

## 12. Risk register delta

- **Retires**: `R-shell-no-redirection`.
- **Adds**:
  - `R-redirect-no-truncate` — until `procOpen` for
    `OpenWrite` does the implicit truncate, two consecutive
    `>` to the same file leave stale data. Mitigated by
    open-q #1.
  - `R-shell-restore-stdio-leak` — if a built-in panics
    between dup2-save and dup2-restore, the shell continues
    with redirected stdio. Acceptable in v1 (built-ins
    don't panic in normal flow); document.
