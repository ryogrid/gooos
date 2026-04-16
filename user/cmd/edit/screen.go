package main

import "github.com/ryogrid/gooos/user/gooos"

const (
	vgaRows    = 25
	vgaCols    = 80
	textRows   = 23 // rows 0-22 for text
	statusRow  = 23
	messageRow = 24
	attrNormal = 0x0F // white on black
	attrStatus = 0x1F // white on blue
)

// Viewport tracks which portion of the buffer is visible.
type Viewport struct {
	topLine int
}

// Editor holds all editor state.
type Editor struct {
	buf      *Buffer
	vp       Viewport
	filename string
	mode     Mode
	message  string
	insertCh byte   // character to insert (set by readCommand)
	cmdBuf   []byte // command-mode input buffer
	quit     bool
}

// NewEditor creates a new editor for the given file.
func NewEditor(filename string) *Editor {
	return &Editor{
		buf:      loadFile(filename),
		filename: filename,
		mode:     ModeNormal,
	}
}

// render redraws the full screen.
func (e *Editor) render() {
	e.scrollToCursor()
	for row := 0; row < textRows; row++ {
		fileRow := e.vp.topLine + row
		if fileRow < len(e.buf.lines) {
			e.drawLine(row, e.buf.lines[fileRow])
		} else {
			e.drawTilde(row)
		}
	}
	e.drawStatusBar()
	e.drawMessageBar()
	// Position hardware cursor
	screenCol := e.buf.cx
	if screenCol >= vgaCols {
		screenCol = vgaCols - 1
	}
	screenRow := e.buf.cy - e.vp.topLine
	gooos.VgaSetCursor(screenRow, screenCol)
}

func (e *Editor) scrollToCursor() {
	if e.buf.cy < e.vp.topLine {
		e.vp.topLine = e.buf.cy
	}
	if e.buf.cy >= e.vp.topLine+textRows {
		e.vp.topLine = e.buf.cy - textRows + 1
	}
}

func (e *Editor) drawLine(row int, line []byte) {
	col := 0
	for col < len(line) && col < vgaCols {
		gooos.VgaWriteAt(row, col, line[col], attrNormal)
		col++
	}
	// Pad rest with spaces
	for col < vgaCols {
		gooos.VgaWriteAt(row, col, ' ', attrNormal)
		col++
	}
}

func (e *Editor) drawTilde(row int) {
	gooos.VgaWriteAt(row, 0, '~', attrNormal)
	for col := 1; col < vgaCols; col++ {
		gooos.VgaWriteAt(row, col, ' ', attrNormal)
	}
}

func (e *Editor) drawStatusBar() {
	// Left side: filename + modified flag + mode
	left := " " + e.filename
	if e.buf.modified {
		left += " [+]"
	}
	switch e.mode {
	case ModeInsert:
		left += " -- INSERT --"
	case ModeNormal:
		left += " -- NORMAL --"
	}

	// Right side: L:row C:col
	right := "L:" + itoa(e.buf.cy+1) + " C:" + itoa(e.buf.cx+1) + " "

	col := 0
	for col < len(left) && col < vgaCols {
		gooos.VgaWriteAt(statusRow, col, left[col], attrStatus)
		col++
	}
	// Fill middle with spaces
	rightStart := vgaCols - len(right)
	for col < rightStart && col < vgaCols {
		gooos.VgaWriteAt(statusRow, col, ' ', attrStatus)
		col++
	}
	// Right portion
	ri := 0
	for col < vgaCols {
		if ri < len(right) {
			gooos.VgaWriteAt(statusRow, col, right[ri], attrStatus)
			ri++
		} else {
			gooos.VgaWriteAt(statusRow, col, ' ', attrStatus)
		}
		col++
	}
}

func (e *Editor) drawMessageBar() {
	msg := e.message
	col := 0
	for col < len(msg) && col < vgaCols {
		gooos.VgaWriteAt(messageRow, col, msg[col], attrNormal)
		col++
	}
	for col < vgaCols {
		gooos.VgaWriteAt(messageRow, col, ' ', attrNormal)
		col++
	}
}

func (e *Editor) setMessage(msg string) {
	e.message = msg
}

func (e *Editor) clearMessage() {
	e.message = ""
}
