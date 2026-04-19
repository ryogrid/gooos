# Running gooos user programs

Demo walkthroughs for the non-networking Ring-3 programs shipped
in the `user/cmd/` tree. Networking demos (UDP/TCP/DHCP) live in
[`networking_demos.md`](networking_demos.md).

## gochan — native userspace goroutines + channels

`gochan` is a shell-invokable user program that exercises native
userspace goroutines + channels end-to-end: a three-stage pipeline
(producer → squarer → printer, joined by unbuffered `chan int`)
followed by a `select` race between two tickers that fire at 20 ms
and 30 ms.

Boot gooos (`make run` or `make iso` then QEMU) and at the shell
prompt:

```
$ gochan
```

Expected serial / VGA output (`PF=0` throughout):

```
gochan: pipeline demo (5 items across 3 goroutines)
gochan: squared=1
gochan: squared=4
gochan: squared=9
gochan: squared=16
gochan: squared=25
gochan: select over two tickers (alpha/beta)
gochan: got alpha
gochan: got beta
gochan: finished
```

- Source: `user/cmd/gochan/main.go`.
- Automated harness: `tmp/test_gochan.sh` — boots the kernel ISO
  in headless QEMU, sends `gochan` to the shell via monitor
  sendkey, and asserts every squared value, both select
  branches, the `finished` marker, and `PF=0`. Prints
  `result: PASS` on success.

## tinyc — Tiny C interpreter

`tinyc` interprets Tiny C source files — a C-subset with
integer-only types, 1D arrays, user-defined functions, and
`println` output. Several `.tc` test files are pre-loaded in the
filesystem at boot:

```
$ tinyc sum.tc
s = 45

$ tinyc fib.tc
13

$ tinyc array.tc
s = 45
```

- Source: `user/cmd/tinyc/` (6 files: token, lexer, AST, parser,
  evaluator, main).
- Design doc: `impldoc/tinyc_interpreter.md`.
- Automated harness: `tmp/test_tinyc.sh` — runs all 4 fixtures,
  asserts expected output + `PF=0`.

## edit — vi-like text editor

`edit` is a vi-like modal editor. Open a file from the shell:

```
$ edit hello.txt
```

The editor takes over the full 80x25 VGA screen. Key bindings:

| Mode | Keys | Action |
|---|---|---|
| Normal | `h`/`j`/`k`/`l` or arrows | Move cursor |
| Normal | `i` | Enter Insert mode |
| Normal | `a` | Enter Insert mode after cursor |
| Normal | `o` / `O` | Open line below / above |
| Normal | `x` | Delete character |
| Normal | `dd` | Delete line |
| Normal | `:` | Enter Command mode |
| Insert | any printable | Insert character |
| Insert | `Escape` | Return to Normal mode |
| Command | `:w` | Save file |
| Command | `:q` | Quit (refuses if unsaved; use `:q!` to force) |
| Command | `:wq` | Save and quit |

- Source: `user/cmd/edit/` (5 files: main, buffer, screen, input,
  keybinds).
- Design docs: `impldoc/editor_overview.md`,
  `impldoc/editor_raw_input.md`.
- Automated harness: `tmp/test_edit.sh`.

## Other user programs

The full roster of programs embedded in the kernel ISO:

| Program | Purpose |
|---|---|
| `sh` | interactive shell, built-ins + ELF exec + pipes + redirection |
| `hello` | one-shot "Hello, World" — smoke test |
| `ls` | list filesystem entries |
| `cat FILE` | stream file contents |
| `wc FILE` | word / line / byte counts |
| `fdprobe` | fd-table syscall verification probe |
| `goprobe` | userspace goroutines / channels probe |
| `gochan` | pipeline + select demo (above) |
| `smpprobe` | SMP / LAPIC / IPI probe |
| `tinyc` | Tiny C interpreter (above) |
| `edit` | vi-like editor (above) |
| `udpecho` | UDP port-17 echo server (userspace) |
| `dhcp` | RFC 2131 DHCP client |
| `tcpecho` | TCP port-8081 echo server (userspace) |
| `tcpcli` | TCP active-open client (`ip port message`) |
