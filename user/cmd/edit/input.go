package main

import "github.com/ryogrid/gooos/user/gooos"

// Scancodes for special keys.
const (
	scEsc       = 0x01
	scBackspace = 0x0E
	scEnter     = 0x1C
	// Extended scancodes (after 0xE0 prefix)
	scUp     = 0x48
	scDown   = 0x50
	scLeft   = 0x4B
	scRight  = 0x4D
	scHome   = 0x47
	scEnd    = 0x4F
	scDelete = 0x53
)

// readCommand reads one key and maps it to an EditorCmd based
// on the current mode.
func (e *Editor) readCommand() EditorCmd {
	sc, ascii, _, flags := gooos.ReadKey()
	extended := flags&1 != 0

	// Arrow keys work in all modes.
	if extended {
		switch sc {
		case scUp:
			return CmdUp
		case scDown:
			return CmdDown
		case scLeft:
			return CmdLeft
		case scRight:
			return CmdRight
		case scHome:
			return CmdHome
		case scEnd:
			return CmdEnd
		case scDelete:
			return CmdDeleteFwd
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
	case 'h':
		return CmdLeft
	case 'l':
		return CmdRight
	case 'j':
		return CmdDown
	case 'k':
		return CmdUp
	case '0':
		return CmdHome
	case '$':
		return CmdEnd
	case 'G':
		return CmdBottom
	case 'g':
		return e.readGPrefix()
	case 'w':
		return CmdWordFwd
	case 'b':
		return CmdWordBack
	case 'x':
		return CmdDeleteFwd
	case 'd':
		return e.readDPrefix()
	case 'i':
		return CmdEnterInsert
	case 'a':
		return CmdEnterInsertAfter
	case 'o':
		return CmdOpenBelow
	case 'O':
		return CmdOpenAbove
	case ':':
		return CmdEnterCommand
	}
	return CmdNone
}

func (e *Editor) insertKey(sc, ascii uint8) EditorCmd {
	if sc == scEsc {
		return CmdExitInsert
	}
	if sc == scBackspace {
		return CmdDeleteBack
	}
	if sc == scEnter {
		return CmdNewline
	}
	if ascii >= 0x20 && ascii < 0x7F {
		e.insertCh = ascii
		return CmdInsert
	}
	return CmdNone
}

// readGPrefix waits for a second key after 'g'. Only 'gg' is
// recognized.
func (e *Editor) readGPrefix() EditorCmd {
	_, ascii, _, _ := gooos.ReadKey()
	if ascii == 'g' {
		return CmdTop
	}
	return CmdNone
}

// readDPrefix waits for a second key after 'd'. Only 'dd' is
// recognized.
func (e *Editor) readDPrefix() EditorCmd {
	_, ascii, _, _ := gooos.ReadKey()
	if ascii == 'd' {
		return CmdDeleteLine
	}
	return CmdNone
}

// commandKey handles keystrokes in Command mode (the : prompt).
func (e *Editor) commandKey(sc, ascii uint8) EditorCmd {
	if sc == scEsc {
		// Cancel command
		e.mode = ModeNormal
		e.cmdBuf = nil
		e.clearMessage()
		return CmdNone
	}
	if sc == scEnter {
		// Execute command
		cmd := string(e.cmdBuf)
		e.cmdBuf = nil
		e.mode = ModeNormal
		return e.executeCommand(cmd)
	}
	if sc == scBackspace {
		if len(e.cmdBuf) > 0 {
			e.cmdBuf = e.cmdBuf[:len(e.cmdBuf)-1]
			e.setMessage(":" + string(e.cmdBuf))
		}
		return CmdNone
	}
	if ascii >= 0x20 && ascii < 0x7F {
		e.cmdBuf = append(e.cmdBuf, ascii)
		e.setMessage(":" + string(e.cmdBuf))
		return CmdNone
	}
	return CmdNone
}

// executeCommand parses and runs a : command.
func (e *Editor) executeCommand(cmd string) EditorCmd {
	switch cmd {
	case "w":
		return CmdSave
	case "q":
		return CmdQuit
	case "wq":
		return CmdSaveQuit
	case "q!":
		return CmdForceQuit
	default:
		e.setMessage("Unknown command: " + cmd)
		return CmdNone
	}
}
