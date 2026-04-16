# Kernel Prerequisites for the gooos Text Editor — Raw Input & VGA Control

This document specifies the kernel-side changes needed before
the userspace text editor (`user/cmd/edit/`) can function.
Two capabilities are missing: character-at-a-time keyboard
input and programmatic VGA cursor/cell control from Ring 3.

## 1. Problem Statement

### 1.1 Keyboard: line-buffered only

`src/fd.go:readKeyboardLine()` (lines 59–97) accumulates
printable characters until Enter (scancode 0x1C) is pressed,
then returns the whole line. Backspace (0x0E) is handled
in-buffer. This is the only path a user program has to read
keyboard input — both `gooos.ReadLine()` and
`gooos.Read(Stdin, buf)` block until a full line is available.

An interactive editor requires **every keypress delivered
immediately**: cursor movement (arrow keys), Ctrl-chords
(`C-f`, `C-b`, `C-x C-s`), Escape, and printable characters.
Line-buffered mode cannot provide this.

### 1.2 Keyboard: missing modifier + special key tracking

`src/keyboard.go:handleKeyboard()` (lines 81–115) currently
tracks **Shift only** (left 0x2A / right 0x36, via `shiftHeld`
at line 66). It does not track:

- **Ctrl** (left 0x1D, right 0xE0+0x1D)
- **Alt** (left 0x38, right 0xE0+0x38)
- **Arrow keys** (up 0x48, down 0x50, left 0x4B, right 0x4D)
- **Escape** (0x01)
- **Tab** (0x0F)
- **Delete** (0xE0+0x53)
- **Home/End** (0xE0+0x47 / 0xE0+0x4F)

The event encoding `scancode | ascii<<8` (line 113) uses only
bits 0–15 of the uint32; bits 16–31 are free for modifiers.

### 1.3 VGA: no userspace cursor control

`src/vga.go` maintains `vgaCursorRow` / `vgaCursorCol`
(lines 14–15) and writes characters via `vgaConsolePutChar()`
(lines 20–52). The VGA text buffer at 0xB8000 is mapped
kernel-only (boot.S identity map, no `pageUser` bit). User
programs cannot:

- Position the cursor at an arbitrary (row, col).
- Write a character at a specific screen cell.
- Set the hardware blinking cursor position.

The editor needs all three.

## 2. New Syscalls

Three new syscalls, numbered 18–20 in the dispatch table
(`src/userspace.go:76`):

| Number | Name | Arguments | Returns |
|---|---|---|---|
| 18 | `sys_read_key` | RDI = buf_ptr (8 bytes) | RAX = 0 on success |
| 19 | `sys_vga_write_at` | RDI = row, RSI = col, RDX = char, R10 = attr | RAX = 0 |
| 20 | `sys_vga_set_cursor` | RDI = row, RSI = col | RAX = 0 |

### 2.1 `sys_read_key` (18) — raw single-keystroke read

Blocks until one keyboard event is available on `keyboardCh`,
then writes it to the caller's 8-byte buffer as a packed
`KeyEvent`:

```go
// KeyEvent is the raw keystroke delivered to userspace.
// Packed into 8 bytes for a single sys_read_key call.
type KeyEvent struct {
    Scancode uint8  // PS/2 scancode set 1 (make code only)
    ASCII    uint8  // translated ASCII (0 if non-printable)
    Mods     uint8  // bit flags: ModShift=1, ModCtrl=2, ModAlt=4
    Flags    uint8  // bit 0: extended key (0xE0 prefix)
    _pad     [4]byte
}
```

**Kernel handler** (`sysReadKeyHandler`):

```go
func sysReadKeyHandler(frame *SyscallFrame) {
    proc := currentProc()
    if proc == nil || proc != foregroundProc {
        frame.RAX = 0xFFFFFFFFFFFFFFFF // not foreground
        return
    }
    event := <-keyboardCh
    scancode := uint8(event & 0xFF)
    ascii := uint8((event >> 8) & 0xFF)
    mods := uint8((event >> 16) & 0xFF)
    flags := uint8((event >> 24) & 0xFF)
    buf := frame.RDI
    *(*uint8)(unsafe.Pointer(buf + 0)) = scancode
    *(*uint8)(unsafe.Pointer(buf + 1)) = ascii
    *(*uint8)(unsafe.Pointer(buf + 2)) = mods
    *(*uint8)(unsafe.Pointer(buf + 3)) = flags
    frame.RAX = 0
}
```

**No echo**. The handler does not call `vgaConsolePutChar` or
`serialPutChar` — the editor manages all screen output.

**Foreground only**. Non-foreground processes get error return,
matching the existing `consoleStdin` behavior
(`src/fd.go:99–108`).

**Backward compatible**. `sys_read` (2) and `readKeyboardLine`
are unchanged. Existing programs (`sh`, `cat`, `wc`) continue
using line-buffered input. `sys_read_key` is a new opt-in path.

### 2.2 `sys_vga_write_at` (19) — write char at (row, col)

Writes a single character with attribute byte at an arbitrary
VGA cell. Does NOT advance the cursor.

```go
func sysVgaWriteAtHandler(frame *SyscallFrame) {
    row := int(frame.RDI)
    col := int(frame.RSI)
    ch := byte(frame.RDX)
    attr := uint16(frame.R10)
    if row < 0 || row >= vgaHeight || col < 0 || col >= vgaWidth {
        frame.RAX = 0xFFFFFFFFFFFFFFFF
        return
    }
    vga := (*[vgaCells]uint16)(unsafe.Pointer(uintptr(0xB8000)))
    offset := row*vgaWidth + col
    if attr == 0 {
        attr = 0x0F // default: white on black
    }
    vga[offset] = uint16(ch) | (attr << 8)
    frame.RAX = 0
}
```

### 2.3 `sys_vga_set_cursor` (20) — move hardware cursor

Programs the VGA CRT controller (ports 0x3D4 / 0x3D5) to
position the blinking hardware cursor. Also updates the
kernel's software `vgaCursorRow` / `vgaCursorCol`.

```go
func sysVgaSetCursorHandler(frame *SyscallFrame) {
    row := int(frame.RDI)
    col := int(frame.RSI)
    if row < 0 || row >= vgaHeight || col < 0 || col >= vgaWidth {
        frame.RAX = 0xFFFFFFFFFFFFFFFF
        return
    }
    vgaCursorRow = row
    vgaCursorCol = col
    pos := uint16(row*vgaWidth + col)
    outb(0x3D4, 0x0F)
    outb(0x3D5, uint8(pos&0xFF))
    outb(0x3D4, 0x0E)
    outb(0x3D5, uint8((pos>>8)&0xFF))
    frame.RAX = 0
}
```

## 3. Keyboard Driver Changes

### 3.1 Ctrl + Alt modifier tracking

Add to `src/keyboard.go`:

```go
const (
    scLShift = 0x2A
    scRShift = 0x36
    scLCtrl  = 0x1D
    scLAlt   = 0x38
    scEsc    = 0x01
    scTab    = 0x0F
)

var ctrlHeld uint8
var altHeld  uint8
```

In `handleKeyboard()`, add make/break tracking for Ctrl and
Alt alongside the existing shift logic (lines 86–95):

```go
case scLCtrl:
    if scancode&0x80 == 0 { ctrlHeld++ } else { ctrlHeld-- }
    return // don't emit event for modifier-only press
case scLAlt:
    if scancode&0x80 == 0 { altHeld++ } else { altHeld-- }
    return
```

### 3.2 Extended key prefix (0xE0)

Some keys (arrow keys, Home, End, Delete, right Ctrl/Alt)
send a 0xE0 prefix byte before the actual scancode. Add a
`var extendedPrefix bool` flag:

```go
if scancode == 0xE0 {
    extendedPrefix = true
    return // wait for actual scancode
}
```

On the next scancode, set `flags |= 1` (extended bit) and
clear the flag. Arrow keys then map cleanly:

| Scancode (after 0xE0) | Meaning |
|---|---|
| 0x48 | Up arrow |
| 0x50 | Down arrow |
| 0x4B | Left arrow |
| 0x4D | Right arrow |
| 0x47 | Home |
| 0x4F | End |
| 0x53 | Delete |

### 3.3 Event encoding (expanded)

Pack modifier + extended flags into the existing uint32 event:

```
bits  0– 7: scancode (make code, 0x80 stripped)
bits  8–15: ASCII (0 for non-printable)
bits 16–18: modifiers (bit 0=Shift, bit 1=Ctrl, bit 2=Alt)
bit     24: extended prefix flag (0xE0-prefixed key)
```

```go
mods := uint8(0)
if shiftHeld > 0 { mods |= 1 }
if ctrlHeld > 0  { mods |= 2 }
if altHeld > 0   { mods |= 4 }
flags := uint8(0)
if extendedPrefix { flags |= 1; extendedPrefix = false }
event := uint32(scancode&0x7F) | uint32(ascii)<<8 |
         uint32(mods)<<16 | uint32(flags)<<24
```

### 3.4 Ctrl + letter → ASCII mapping

When Ctrl is held and the base ASCII is `'a'`–`'z'`, the
ASCII byte should be set to the control character:
`ascii = base - 'a' + 1` (Ctrl-A = 0x01, Ctrl-B = 0x02, ...,
Ctrl-Z = 0x1A). This is standard terminal behavior and lets
the editor's key-binding table match on the ASCII value
directly.

```go
if ctrlHeld > 0 && ascii >= 'a' && ascii <= 'z' {
    ascii = ascii - 'a' + 1
}
```

## 4. Userspace API

Add to `user/gooos/io.go`:

```go
const (
    sysReadKey      = 18
    sysVgaWriteAt   = 19
    sysVgaSetCursor = 20
)

// ReadKey blocks until a single keystroke is available and
// returns the raw key event. Fields: Scancode, ASCII, Mods
// (bit 0=Shift, 1=Ctrl, 2=Alt), Flags (bit 0=extended).
func ReadKey() (scancode, ascii, mods, flags uint8) {
    var buf [8]byte
    syscall1(sysReadKey, uintptr(unsafe.Pointer(&buf[0])))
    return buf[0], buf[1], buf[2], buf[3]
}

// VgaWriteAt writes a single character at (row, col) with the
// given color attribute. attr=0 uses the default (white on
// black).
func VgaWriteAt(row, col int, ch byte, attr uint16) {
    syscall4(sysVgaWriteAt, uintptr(row), uintptr(col),
             uintptr(ch), uintptr(attr))
}

// VgaSetCursor moves the hardware blinking cursor to (row, col).
func VgaSetCursor(row, col int) {
    syscall2(sysVgaSetCursor, uintptr(row), uintptr(col))
}
```

Also add the three new syscall constants to
`user/gooos/syscall.go:28`:

```go
sysReadKey      = 18
sysVgaWriteAt   = 19
sysVgaSetCursor = 20
```

## 5. Files to Modify

| File | Change |
|---|---|
| `src/keyboard.go` | Add ctrlHeld, altHeld, extendedPrefix; expand handleKeyboard logic; Ctrl+letter→ASCII mapping |
| `src/userspace.go` | Add syscall 18/19/20 to dispatch + handlers |
| `src/vga.go` | (Optional) extract vga pointer to a shared var if not already |
| `user/gooos/syscall.go` | Add sysReadKey, sysVgaWriteAt, sysVgaSetCursor constants |
| `user/gooos/io.go` | Add ReadKey(), VgaWriteAt(), VgaSetCursor() wrappers |

## 6. Backward Compatibility

- `sys_read` (2) is unchanged. `readKeyboardLine()` still
  works identically. Shell, cat, wc, tinyc — all unaffected.
- The new modifier tracking in `handleKeyboard` adds bits
  16–24 to the uint32 event. `readKeyboardLine()` extracts
  only bits 0–15 (`scancode & 0xFF`, `ascii >> 8 & 0xFF`),
  so the extra bits are harmless.
- Arrow-key scancodes (0x48, 0x50, 0x4B, 0x4D) were previously
  ignored (ascii = 0, no entry in scancodeToASCII). Now they
  still produce ascii = 0 but are delivered via `sys_read_key`.
  `readKeyboardLine()` skips them (ascii == 0 → not stored in
  the line buffer). No behavior change for line-buffered
  programs.
- 0xE0 prefix byte was previously emitted as a regular event
  with scancode 0xE0 and ascii 0. Now it's consumed as a
  prefix flag. `readKeyboardLine()` would have ignored it
  anyway (ascii 0 → not stored). No behavior change.

## 7. Dependencies

- None. This is the foundation; the editor design doc
  (`impldoc/editor_overview.md`) depends on these syscalls.

## 8. Verification

1. After implementing the keyboard changes, existing programs
   must still work: `tmp/test_sendkey.sh` × 1 PASS confirms
   the shell's line-buffered input path is unbroken.
2. A small test program (`user/cmd/keytest/main.go`) can be
   written to call `gooos.ReadKey()` in a loop, printing each
   event's scancode/ascii/mods/flags to serial. Verify arrow
   keys, Ctrl-C, Ctrl-X, Escape produce the expected values.
3. `sys_vga_write_at` and `sys_vga_set_cursor` verified by
   writing a character at each corner of the 80×25 screen and
   moving the hardware cursor.

## 9. Open Questions

1. **Should `sys_read_key` be a mode flag on `sys_read`
   instead?** A separate syscall is simpler: no state to manage,
   no risk of forgetting to reset the mode. Recommended: keep
   as a separate syscall.
2. **Hardware cursor enable/disable?** The VGA CRT controller
   can hide the cursor entirely (useful for a full-screen
   editor that draws its own cursor highlight). Add as a v2
   extension or a flag on `sys_vga_set_cursor`.
3. **Multi-byte VGA writes?** `sys_vga_write_at` writes one
   char at a time. A bulk variant (`sys_vga_write_line`)
   writing a row at once would reduce syscall overhead. Defer
   to v2; the per-char path is adequate for 80×25.

## 10. Risk Register

- **R-modifier-tracking-nosplit**: `handleKeyboard` is
  `//go:nosplit`. The new Ctrl/Alt/extended tracking uses only
  local variables and global uint8 counters — no allocations,
  no function calls beyond the existing `keyboardIRQSend`.
  Risk: low.
- **R-scancode-set-conflicts**: Some PS/2 keyboards send
  different scancodes for special keys. The design assumes
  scancode set 1 (the QEMU default for `-display none`).
  Mitigation: test on QEMU only; document hardware dependency.
- **R-event-encoding-compat**: Adding bits 16–24 to the uint32
  event. All existing consumers mask to 16 bits. Risk: low.
