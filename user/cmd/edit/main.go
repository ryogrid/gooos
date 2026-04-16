// edit — vi-like text editor for gooos.
//
// Usage from the gooos shell:
//
//	$ edit filename.txt
//
// Opens the file (or creates a new empty buffer), provides
// vi-style modal editing (Normal / Insert / Command), and saves
// back to the in-memory filesystem. See impldoc/editor_overview.md.

package main

import "github.com/ryogrid/gooos/user/gooos"

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
		if ed.quit {
			break
		}
		ed.render()
	}

	gooos.VgaClear()
}

// execute dispatches an EditorCmd.
func (e *Editor) execute(cmd EditorCmd) {
	// Clear transient message on any key (except in Command mode).
	if e.mode != ModeCommand && cmd != CmdNone {
		e.clearMessage()
	}

	switch cmd {
	case CmdNone:
		// nothing
	case CmdUp:
		e.buf.cursorUp()
	case CmdDown:
		e.buf.cursorDown()
	case CmdLeft:
		e.buf.cursorLeft()
	case CmdRight:
		e.buf.cursorRight()
	case CmdHome:
		e.buf.cursorHome()
	case CmdEnd:
		e.buf.cursorEnd()
	case CmdTop:
		e.buf.cursorTop()
	case CmdBottom:
		e.buf.cursorBottom()
	case CmdWordFwd:
		e.buf.wordForward()
	case CmdWordBack:
		e.buf.wordBackward()
	case CmdInsert:
		e.buf.insertChar(e.insertCh)
	case CmdNewline:
		e.buf.insertNewline()
	case CmdDeleteBack:
		e.buf.deleteBackward()
	case CmdDeleteFwd:
		e.buf.deleteForward()
	case CmdDeleteLine:
		e.buf.deleteLine()
	case CmdEnterInsert:
		e.mode = ModeInsert
	case CmdEnterInsertAfter:
		if e.buf.cx < len(e.buf.lines[e.buf.cy]) {
			e.buf.cx++
		}
		e.mode = ModeInsert
	case CmdOpenBelow:
		e.buf.openLineBelow()
		e.mode = ModeInsert
	case CmdOpenAbove:
		e.buf.openLineAbove()
		e.mode = ModeInsert
	case CmdExitInsert:
		e.mode = ModeNormal
		// Vi convention: cursor backs up one on exit from Insert
		if e.buf.cx > 0 {
			e.buf.cx--
		}
	case CmdEnterCommand:
		e.mode = ModeCommand
		e.cmdBuf = nil
		e.setMessage(":")
	case CmdSave:
		n, ok := saveFile(e.filename, e.buf)
		if ok {
			e.setMessage("\"" + e.filename + "\" written, " + itoa(n) + " bytes")
		} else {
			e.setMessage("Error: could not save " + e.filename)
		}
	case CmdQuit:
		if e.buf.modified {
			e.setMessage("No write since last change (use :q! to override)")
		} else {
			e.quit = true
		}
	case CmdForceQuit:
		e.quit = true
	case CmdSaveQuit:
		_, ok := saveFile(e.filename, e.buf)
		if ok {
			e.quit = true
		} else {
			e.setMessage("Error: could not save " + e.filename)
		}
	}
}

// itoa converts an int to its decimal string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
