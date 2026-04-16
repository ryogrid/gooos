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

- [ ] **8. Regression matrix green**
  - [ ] test_sendkey.sh PASS.
  - [ ] test_goprobe.sh PASS.
  - [ ] test_gochan.sh PASS.
  - [ ] test_tinyc.sh PASS.

- [ ] **9. README update**
  - [ ] Progress table row.
  - [ ] Usage section with vi-mode key reference.

- [ ] **10. Reviewer pass + completeness**
  - [ ] Reviewer subagent: no CRITICAL/MAJOR.
  - [ ] grep TODO/FIXME/XXX — no new markers.
  - [ ] Every checked item has a commit.

## Deferred items

(None yet.)

## Reviewer MINOR notes

(None yet.)
