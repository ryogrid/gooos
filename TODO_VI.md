# TODO — vi-like Text Editor

Design sources: `impldoc/editor_raw_input.md`, `impldoc/editor_overview.md`.
One git commit per top-level item.

## Items

- [x] **1. Keyboard driver: Ctrl/Alt + extended keys**
  - [x] Add ctrlHeld, altHeld, extendedPrefix to src/keyboard.go.
  - [x] Track Ctrl/Alt make/break in handleKeyboard.
  - [x] Consume 0xE0 prefix, set extended flag.
  - [x] Pack mods (bits 16-18) + flags (bit 24) into event.
  - [x] Ctrl+letter → control-char ASCII mapping.
  - [x] `make build` clean + `test_sendkey.sh 1` PASS (`pf=0 exit=3 cat=1`).

- [x] **2. New syscalls (18-20) + kernel handlers**
  - [x] sysReadKey (18): foreground check, <-keyboardCh, unpack.
  - [x] sysVgaWriteAt (19): write char at (row, col) to 0xB8000.
  - [x] sysVgaSetCursor (20): CRT controller + software cursor.
  - [x] Add to dispatch switch in src/userspace.go.
  - [x] `make build` clean + `test_sendkey.sh 1` PASS.

- [x] **3. Userspace API wrappers**
  - [x] user/gooos/syscall.go: sysReadKey=18, sysVgaWriteAt=19,
        sysVgaSetCursor=20.
  - [x] user/gooos/io.go: ReadKey(), VgaWriteAt(), VgaSetCursor().
  - [x] `make build` clean.

- [x] **4. Fix ReadFile 64 KiB buffer**
  - [x] user/gooos/fs.go: 65536 → 131072.
  - [x] `make build` clean.

- [x] **5. Editor source files (5 files)**
  - [x] user/cmd/edit/keybinds.go — EditorCmd enum + Mode enum.
  - [x] user/cmd/edit/buffer.go — Buffer + cursor + editing ops.
  - [x] user/cmd/edit/screen.go — Viewport + rendering + status.
  - [x] user/cmd/edit/input.go — readCommand + vi-mode dispatch.
  - [x] user/cmd/edit/main.go — entry point + main loop + execute.
  - [x] Standalone tinygo build compiles cleanly.

- [x] **6. Build integration**
  - [x] user/Makefile CMDS += edit.
  - [x] src/main.go preloads edit.elf.
  - [x] `make build` clean; edit.elf = 91920 bytes (89.8 KiB).

- [x] **7. Test harness + PASS**
  - [x] tmp/test_edit.sh created + chmod +x (untracked).
  - [x] Fix: add trailing newline on save (POSIX convention) so
        `cat file` output ends with `\n` and grep matches.
  - [x] `bash tmp/test_edit.sh` → `pf=0 hello=1` → PASS.

- [x] **8. Regression matrix green**
  - [x] test_sendkey.sh → `pf=0 exit=3 cat=1`.
  - [x] test_goprobe.sh → PASS.
  - [x] test_gochan.sh → PASS.
  - [x] test_tinyc.sh → PASS.

- [x] **9. README update**
  - [x] Progress table rows (raw input, VGA control, editor).
  - [x] Shell command list updated (+ edit).
  - [x] Usage section with vi-mode key reference table.

- [x] **10. Reviewer pass + completeness**
  - [x] Reviewer subagent: CRITICAL=0, MAJOR=1 (fixed),
        MINOR=3 (1 fixed, 2 noted).
  - [x] `grep -rn TODO/FIXME/XXX` over user/cmd/edit/ +
        src/keyboard.go — zero hits.
  - [x] Cross-reference: 9 TODO items → 9 commits
        (`26d0a4f..49a16c6`), all checked.

## Deferred items

- **Undo/redo** — heap cost under gc=leaking. v2.
- **Horizontal scrolling** — lines > 80 chars truncated. v2.
- **Search (`/`, `?`)** — needs mini input loop. v2.
- **Syntax highlighting** — needs per-language tokenizer. v2.
- **Multiple buffers / split panes** — v2.

## Reviewer MINOR notes

Reviewer subagent: CRITICAL=0, MAJOR=1, MINOR=3.

1. **MAJOR-1 (fixed)**: `buffer.go:openLineAbove` did not
   assign `b.lines = newLines` — the `O` command was a no-op.
   Fixed by adding the missing assignment.
2. **MINOR-1 (fixed)**: `main.go:CmdSaveQuit` ignored
   saveFile return value. Fixed: now checks ok and shows error
   instead of quitting on failure.
3. **MINOR-2 (noted)**: `buffer.go:cursorRight` allows
   `cx == len(line)` (one past end). Vi Normal mode would
   clamp to `len-1`. Left as-is: the renderer and insert logic
   handle the extra position correctly, and clamping would
   require mode-awareness in cursor methods.
4. **MINOR-3 (noted)**: Status bar has no ModeCommand case — no
   mode label shown during `:` prompt. The `:` prompt appears
   on the message bar (row 24), which is functional. Left as-is
   to keep the status bar switch minimal.
