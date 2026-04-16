# gooos Text Editor — Architecture & Design

This document specifies the userspace text editor (`edit`)
for gooos. It depends on the kernel-side raw input and VGA
control syscalls designed in `impldoc/editor_raw_input.md`.

## 1. Goal

A minimal, functional text editor invokable from the gooos
shell:

```
$ edit hello.txt
```

Opens an existing file (or creates a new empty buffer), allows
navigation + editing, and saves back to the in-memory
filesystem. The editor occupies the full 80×25 VGA screen while
running and restores the shell prompt on exit.

## 2. Buffer Model

### 2.1 Line Array

The buffer is a simple `[][]byte` — a slice of lines, each
line a byte slice. Justification:

- **Simplest** to implement; the VGA display is inherently
  line-oriented (80-column rows).
- **Adequate for the file size limit** (128 KiB max file per
  `maxFileData`). At an average of 40 chars/line, that's
  ~3200 lines — a small slice.
- **No gap-buffer or rope complexity**. Those pay off for
  large files with frequent mid-buffer edits; gooos files are
  tiny.
- **gc=leaking**: every append/splice grows the heap
  monotonically. A line array with in-place byte edits
  minimizes allocations. Line splits (Enter in the middle)
  and line joins (Backspace at column 0) each allocate one
  new `[]byte`; acceptable for short editing sessions.

```go
type Buffer struct {
    lines  [][]byte // line content (no trailing \n)
    cx, cy int      // cursor: column (byte offset), row (line index)
}
```

### 2.2 Cursor Model

- `cy` = current line index (0-based).
- `cx` = current byte offset within `lines[cy]` (0-based).
- `cx` is clamped to `[0, len(lines[cy])]` after every
  vertical movement.

### 2.3 Viewport

```go
type Viewport struct {
    topLine int // first visible line index
    rows    int // editable rows (23 = vgaHeight - statusBar - messageBar)
    cols    int // 80
}
```

The viewport scrolls to keep the cursor visible. Scroll
triggers:
- `cy < topLine` → `topLine = cy`
- `cy >= topLine + rows` → `topLine = cy - rows + 1`

## 3. Screen Model

### 3.1 Layout (80 × 25)

```
Row  0 – 22: text area (23 lines of file content)
Row 23:      status bar (filename, line/col, modified flag)
Row 24:      message bar (save confirmation, error messages)
```

### 3.2 Rendering

On every keypress, the editor redraws only **changed lines**
for efficiency, but a full-redraw fallback keeps the logic
simple. The rendering loop:

```go
func (e *Editor) render() {
    for screenRow := 0; screenRow < e.vp.rows; screenRow++ {
        fileRow := e.vp.topLine + screenRow
        if fileRow < len(e.buf.lines) {
            e.drawLine(screenRow, e.buf.lines[fileRow])
        } else {
            e.drawLine(screenRow, []byte("~"))
        }
    }
    e.drawStatusBar()
    e.drawMessageBar()
    // Position hardware cursor at (cy - topLine, cx)
    gooos.VgaSetCursor(e.buf.cy - e.vp.topLine, e.cursorScreenCol())
}
```

Each `drawLine` call uses `gooos.VgaWriteAt(row, col, ch, 0)`
per character, padding the rest of the row with spaces.

### 3.3 Status Bar

```
 hello.txt [modified]                          L:12 C:5
```

White-on-blue attribute (0x1F) to visually separate it from
the text area.

### 3.4 Message Bar

Transient messages (e.g., `Saved hello.txt (245 bytes)`,
`:w` / `:q` feedback). Cleared after the next keypress.

## 4. Key Bindings (vi-like, modal)

The editor uses **vi-style modal editing**. This avoids
reliance on Ctrl-chord delivery, which is fragile through
QEMU's `sendkey` interface — making automated testing via
`tmp/test_edit.sh` feasible. The trade-off (mode awareness)
is acceptable for a minimal editor.

### 4.1 Modes

| Mode | Entry | Behavior |
|---|---|---|
| **Normal** | default / `Escape` | Navigation + commands. Printable keys are commands, not inserted. |
| **Insert** | `i`, `a`, `o`, `O` | Printable keys are inserted at cursor. Escape returns to Normal. |
| **Command** | `:` from Normal | Line input at the message bar; Enter executes. |

The current mode is shown in the status bar:
`-- NORMAL --`, `-- INSERT --`, or `:` prompt.

### 4.2 Normal-Mode Bindings

| Key | Command | Description |
|---|---|---|
| `h` / Left arrow | cursorLeft | Move cursor left |
| `l` / Right arrow | cursorRight | Move cursor right |
| `j` / Down arrow | cursorDown | Move cursor down |
| `k` / Up arrow | cursorUp | Move cursor up |
| `0` | cursorHome | Move to beginning of line |
| `$` | cursorEnd | Move to end of line |
| `g` then `g` | cursorTop | Move to first line |
| `G` | cursorBottom | Move to last line |
| `w` | wordForward | Move to next word start |
| `b` | wordBackward | Move to previous word start |
| `x` | deleteChar | Delete character at cursor |
| `d` then `d` | deleteLine | Delete entire current line |
| `i` | enterInsert | Enter Insert mode at cursor |
| `a` | enterInsertAfter | Enter Insert mode after cursor |
| `o` | openLineBelow | Insert new line below, enter Insert |
| `O` | openLineAbove | Insert new line above, enter Insert |
| `:` | enterCommand | Enter Command mode |
| `Escape` | (no-op) | Stay in Normal mode |

### 4.3 Insert-Mode Bindings

| Key | Command | Description |
|---|---|---|
| Printable char | insertChar | Insert at cursor, advance |
| Enter | insertNewline | Split line at cursor |
| Backspace | deleteBackward | Delete char before cursor |
| Delete | deleteForward | Delete char at cursor |
| Arrow keys | cursor movement | Same as Normal mode |
| `Escape` | exitInsert | Return to Normal mode |

### 4.4 Command-Mode Bindings

Entered by pressing `:` in Normal mode. A prompt appears on
the message bar (row 24). The user types a command and presses
Enter. Supported commands:

| Command | Action |
|---|---|
| `:w` | Save file |
| `:q` | Quit (refuse if modified) |
| `:wq` | Save then quit |
| `:q!` | Quit without saving |

Unknown commands display `Unknown command: <cmd>` on the
message bar.

### 4.5 Key Detection Logic

```go
type Mode int
const (
    ModeNormal Mode = iota
    ModeInsert
    ModeCommand
)

func (e *Editor) readCommand() EditorCmd {
    sc, ascii, mods, flags := gooos.ReadKey()
    _ = mods // modifiers unused in vi-style bindings
    extended := flags & 1 != 0

    // Arrow keys work in all modes
    if extended {
        switch sc {
        case 0x48: return CmdUp
        case 0x50: return CmdDown
        case 0x4B: return CmdLeft
        case 0x4D: return CmdRight
        case 0x47: return CmdHome
        case 0x4F: return CmdEnd
        case 0x53: return CmdDeleteFwd
        }
    }

    switch e.mode {
    case ModeNormal:
        return e.normalKey(sc, ascii)
    case ModeInsert:
        return e.insertKey(sc, ascii)
    case ModeCommand:
        return e.commandKey(sc, ascii)
    }
    return CmdNone
}

func (e *Editor) normalKey(sc, ascii uint8) EditorCmd {
    switch ascii {
    case 'h': return CmdLeft
    case 'l': return CmdRight
    case 'j': return CmdDown
    case 'k': return CmdUp
    case '0': return CmdHome
    case '$': return CmdEnd
    case 'G': return CmdBottom
    case 'g': return e.readGPrefix() // gg
    case 'w': return CmdWordFwd
    case 'b': return CmdWordBack
    case 'x': return CmdDeleteFwd
    case 'd': return e.readDPrefix() // dd
    case 'i': return CmdEnterInsert
    case 'a': return CmdEnterInsertAfter
    case 'o': return CmdOpenBelow
    case 'O': return CmdOpenAbove
    case ':': return CmdEnterCommand
    }
    if sc == 0x01 { return CmdNone } // Escape: stay in Normal
    return CmdNone
}

func (e *Editor) insertKey(sc, ascii uint8) EditorCmd {
    if sc == 0x01 { return CmdExitInsert } // Escape
    if sc == 0x0E { return CmdDeleteBack } // Backspace
    if sc == 0x1C { return CmdNewline }    // Enter
    if ascii >= 0x20 && ascii < 0x7F {
        e.insertCh = ascii
        return CmdInsert
    }
    return CmdNone
}
```

### 4.6 Why vi-like (not Emacs-like)

QEMU's `sendkey` command reliably delivers plain ASCII keys
and scancodes but has inconsistent behavior for `ctrl-<key>`
combinations through the virtual PS/2 controller. A vi-style
modal design uses only:
- Printable ASCII in Normal mode (h/j/k/l, i/a/o, :, etc.)
- Escape (scancode 0x01) for mode transitions
- Arrow keys (extended scancodes 0x48/0x50/0x4B/0x4D)

All of these are testable via `sendkey h`, `sendkey ret`,
`sendkey esc`, and `sendkey up`/`sendkey down`. No Ctrl chords
required, making the automated `tmp/test_edit.sh` harness
reliable.

## 5. File I/O

### 5.1 Loading

```go
func loadFile(name string) *Buffer {
    data := gooos.ReadFile(name)
    if data == nil {
        // New file: start with one empty line
        return &Buffer{lines: [][]byte{{}}}
    }
    // Split on '\n'
    buf := &Buffer{}
    start := 0
    for i := 0; i < len(data); i++ {
        if data[i] == '\n' {
            buf.lines = append(buf.lines, copySlice(data[start:i]))
            start = i + 1
        }
    }
    // Trailing content without final newline
    if start <= len(data) {
        buf.lines = append(buf.lines, copySlice(data[start:]))
    }
    return buf
}
```

Uses `gooos.ReadFile()` (`user/gooos/fs.go:8`) which returns
the entire file content as a heap-allocated `[]byte`.

### 5.2 Saving

```go
func saveFile(name string, buf *Buffer) (int, bool) {
    fd := gooos.Open(name, gooos.OpenWrite) // truncate + open (creates if missing)
    if fd < 0 {
        // Open failed — FS is full (maxFiles reached) or other
        // kernel error. The kernel's openFileFd creates the file
        // automatically if it does not exist, so "file not found"
        // is NOT the cause here.
        return 0, false
    }
    total := 0
    for i, line := range buf.lines {
        n := gooos.Write(fd, line)
        total += n
        if i < len(buf.lines)-1 {
            gooos.Write(fd, []byte{'\n'})
            total++
        }
    }
    gooos.Close(fd)
    return total, true
}
```

Uses the fd-based write path: `gooos.Open()` + `gooos.Write()`
+ `gooos.Close()` (`user/gooos/io.go:22–60`). This is the
same path the shell's `>` redirection uses.

## 6. File Layout

```
user/cmd/edit/
    main.go     — entry point, main loop, argument handling
    buffer.go   — Buffer struct, line manipulation (insert, delete, split, join)
    screen.go   — Viewport, rendering, status bar, message bar
    input.go    — readCommand(), key detection, C-x prefix handling
    keybinds.go — EditorCmd enum, command dispatch table
```

All files in package `main`. Single `tinygo build` produces
`edit.elf`.

## 7. Main Loop

```go
func main() {
    filename := gooos.Args()
    if len(filename) == 0 {
        gooos.Println("usage: edit <filename>")
        gooos.Exit(1)
    }

    ed := NewEditor(filename)
    gooos.VgaClear()
    ed.render()

    for {
        cmd := ed.readCommand()
        ed.execute(cmd)
        ed.render()
        if ed.quit {
            break
        }
    }

    // Restore screen for shell
    gooos.VgaClear()
}
```

The loop is **synchronous**: read one key, execute the command,
redraw, repeat. No goroutines needed inside the editor; the
user-side TinyGo scheduler is idle.

## 8. Constraints and Limits

| Constraint | Value | Rationale |
|---|---|---|
| Max file size | 64 KiB (practical) | `maxFileData = 131072` (128 KiB) in `src/fs.go:12`, but `gooos.ReadFile()` at `user/gooos/fs.go:10` allocates only a 65536-byte (64 KiB) read buffer — files larger than 64 KiB are silently truncated on load. Pre-existing issue; fix by bumping the ReadFile buffer to 131072 as part of the editor implementation |
| Max lines | ~3200 | 128 KiB / 40 chars avg |
| Editable rows | 23 | 25 VGA rows − 1 status − 1 message |
| Columns | 80 | VGA text mode |
| Heap budget | 256 KiB | `user/linker_user.ld` .bss reservation; gc=leaking |
| ELF size | < 128 KiB | Must fit in `maxFileData`; expected 70–90 KiB (pure Go, no strconv needed for the editor itself; `itoa` for line/col display is a small helper) |
| Line length | unbounded | Lines longer than 80 chars are truncated at display; horizontal scrolling is a v2 feature |

## 9. Demo / Verification

### 9.1 Interactive Demo Session

```
$ edit hello.txt
```

Screen shows:

```
Hello from the gooos filesystem!
This is a test file.
~
~
... (empty lines shown as ~)
 hello.txt                                     L:1 C:1
```

User presses `$` to move to end of line 1, `a` to enter Insert
mode after cursor, types " — edited", presses `Escape`, then
`:w` + Enter to save:

```
 hello.txt [saved 54 bytes]           -- NORMAL -- L:1 C:33
```

User types `:q` + Enter to quit. Back at shell:

```
$ cat hello.txt
Hello from the gooos filesystem! — edited
This is a test file.
$
```

### 9.2 Harness: `tmp/test_edit.sh`

vi-style bindings make the harness reliable — every key is a
plain sendkey command with no Ctrl modifiers:

1. Boot, wait for shell.
2. `send_line "edit test_ed.txt"` → editor opens (new file,
   Normal mode).
3. `mon "sendkey i"` → enter Insert mode.
4. Send printable characters: `h`, `e`, `l`, `l`, `o`.
5. `mon "sendkey esc"` → back to Normal mode.
6. `mon "sendkey shift-semicolon"` → `:` enters Command mode.
7. Send `w`, `ret` → `:w` saves the file.
8. `mon "sendkey shift-semicolon"` → `:` again.
9. Send `q`, `ret` → `:q` quits.
10. `send_line "cat test_ed.txt"` → assert "hello" on serial.
11. Assert PF=0.

All sendkey commands use plain keys or `shift-<key>` (for `:`
and `$`), which QEMU handles reliably. No `ctrl-<key>` needed.

### 9.3 Regression

Existing harnesses (`test_sendkey.sh`, `test_goprobe.sh`,
`test_gochan.sh`, `test_tinyc.sh`) must still PASS — the
editor adds a new ELF but does not modify any shared kernel
code paths (the new syscalls are additive).

## 10. Build Integration

- Add `edit` to `user/Makefile` CMDS.
- Add `fsCreate("edit.elf")` + `fsWrite(...)` to `src/main.go`
  preload block.
- `scripts/embed_elfs.sh` regenerates `src/user_binaries.go`.
- Verify ELF < 128 KiB.

## 11. Out of Scope (v2+)

- **Undo/redo**: would need a change log or snapshot stack;
  heap cost under gc=leaking is a concern. Defer.
- **Horizontal scrolling**: lines longer than 80 are truncated.
  A scrollOffset per line would fix this. Defer.
- **Syntax highlighting**: needs a tokenizer per language +
  color attribute mapping. Defer.
- **Multiple buffers / split panes**: multiple Viewports +
  buffer switching. Defer.
- **Search (C-s)**: incremental search would need a mini input
  loop. Defer.
- **Mouse input**: no PS/2 mouse driver in gooos. Defer.
- **Unicode**: VGA text mode is 8-bit codepage only. Defer.

## 12. Risk Register

- **R-heap-exhaustion**: every `insertChar` / `insertNewline`
  allocates under gc=leaking. A 1000-line editing session with
  heavy insert/delete could exhaust 256 KiB. Mitigation: the
  editor is for short files (< 50 lines in practice); document
  the limit. Future: switch to gc=conservative.
- **R-elf-size**: the editor imports only `gooos` (no strconv,
  no time). Expected 70–90 KiB. If exceeded, bump maxFileData
  or trim features.
- **R-vga-flicker**: per-character `sys_vga_write_at` on every
  redraw (23 rows × 80 cols = 1840 syscalls). At ~1 µs/syscall
  in QEMU, that's ~2 ms per full redraw — imperceptible.
  Mitigation: dirty-line tracking reduces syscalls to ~80/key.
- **R-sendkey-reliability**: vi-style bindings use only plain
  keys, shift-keys, Escape, and arrow keys — all reliable via
  QEMU `sendkey`. Risk retired by the switch from Emacs-like
  to vi-like bindings.
- **R-shell-vga-state**: after the editor clears the screen
  and exits, the shell's VGA cursor state may be wrong. The
  editor calls `gooos.VgaClear()` on exit, which resets
  vgaCursorRow/Col to (0,0). The shell then prints `$ ` at
  the top. Acceptable.
- **R-foreground-ownership**: the editor process is the
  foreground process (set by `processWait` in `src/process.go:
  304`). `sys_read_key` only serves the foreground process.
  No conflict with the shell — the shell is blocked on
  `processWait` while the editor runs.

## 13. Dependencies

- `impldoc/editor_raw_input.md` — kernel prerequisites
  (syscalls 18–20).
- `user/gooos/io.go` — ReadKey(), VgaWriteAt(),
  VgaSetCursor(), ReadFile(), Open(), Write(), Close(),
  VgaClear(), Args(), Exit().

## 14. Reviewer Follow-ups

Reviewer subagent: CRITICAL=0, MAJOR=0, MINOR=4.

1. **MINOR-1 (fixed)**: `editor_raw_input.md` cited line 102
   for event encoding; actual line is 113. Corrected.
2. **MINOR-2 (fixed)**: `editor_raw_input.md` cited lines
   88–99 for shift logic; actual range is 86–95. Corrected.
3. **MINOR-3 (fixed)**: `editor_overview.md` saveFile() comment
   implied Open(OpenWrite) fails when file is missing; the
   kernel creates it automatically. Comment rewritten.
4. **MINOR-4 (noted)**: `user/gooos/fs.go:10` ReadFile()
   allocates a 64 KiB buffer, silently truncating files larger
   than 64 KiB even though maxFileData allows 128 KiB. Pre-
   existing gooos bug. Noted in §8 constraints table; fix should
   be folded into the editor implementation commit.
