package main

// EditorCmd represents a single editor action.
type EditorCmd int

const (
	CmdNone EditorCmd = iota
	CmdUp
	CmdDown
	CmdLeft
	CmdRight
	CmdHome
	CmdEnd
	CmdTop
	CmdBottom
	CmdWordFwd
	CmdWordBack
	CmdInsert           // insert e.insertCh at cursor
	CmdNewline          // split line at cursor
	CmdDeleteBack       // delete char before cursor
	CmdDeleteFwd        // delete char at cursor
	CmdDeleteLine       // delete current line (dd)
	CmdEnterInsert      // i — enter Insert mode at cursor
	CmdEnterInsertAfter // a — enter Insert mode after cursor
	CmdOpenBelow        // o — open line below, enter Insert
	CmdOpenAbove        // O — open line above, enter Insert
	CmdExitInsert       // Escape from Insert mode
	CmdEnterCommand     // : — enter Command mode
	CmdSave             // :w
	CmdQuit             // :q
	CmdForceQuit        // :q!
	CmdSaveQuit         // :wq
)

// Mode represents the editor's current input mode.
type Mode int

const (
	ModeNormal  Mode = iota
	ModeInsert
	ModeCommand
)
