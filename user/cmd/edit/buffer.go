package main

import "github.com/ryogrid/gooos/user/gooos"

// Buffer holds the file content as a slice of lines.
type Buffer struct {
	lines    [][]byte
	cx, cy   int  // cursor column (byte offset), row (line index)
	modified bool // true if content changed since last save
}

func newBuffer() *Buffer {
	return &Buffer{lines: [][]byte{{}}}
}

func loadFile(name string) *Buffer {
	data := gooos.ReadFile(name)
	if data == nil {
		return newBuffer()
	}
	buf := &Buffer{}
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			buf.lines = append(buf.lines, copyBytes(data[start:i]))
			start = i + 1
		}
	}
	if start <= len(data) {
		buf.lines = append(buf.lines, copyBytes(data[start:]))
	}
	if len(buf.lines) == 0 {
		buf.lines = [][]byte{{}}
	}
	return buf
}

func saveFile(name string, buf *Buffer) (int, bool) {
	fd := gooos.Open(name, gooos.OpenWrite)
	if fd < 0 {
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
	buf.modified = false
	return total, true
}

func copyBytes(src []byte) []byte {
	dst := make([]byte, len(src))
	for i := 0; i < len(src); i++ {
		dst[i] = src[i]
	}
	return dst
}

// --- Cursor movement ---

func (b *Buffer) cursorLeft() {
	if b.cx > 0 {
		b.cx--
	}
}

func (b *Buffer) cursorRight() {
	if b.cx < len(b.lines[b.cy]) {
		b.cx++
	}
}

func (b *Buffer) cursorUp() {
	if b.cy > 0 {
		b.cy--
		b.clampCX()
	}
}

func (b *Buffer) cursorDown() {
	if b.cy < len(b.lines)-1 {
		b.cy++
		b.clampCX()
	}
}

func (b *Buffer) cursorHome() { b.cx = 0 }

func (b *Buffer) cursorEnd() { b.cx = len(b.lines[b.cy]) }

func (b *Buffer) cursorTop() {
	b.cy = 0
	b.clampCX()
}

func (b *Buffer) cursorBottom() {
	b.cy = len(b.lines) - 1
	b.clampCX()
}

func (b *Buffer) clampCX() {
	if b.cx > len(b.lines[b.cy]) {
		b.cx = len(b.lines[b.cy])
	}
}

func (b *Buffer) wordForward() {
	line := b.lines[b.cy]
	x := b.cx
	// Skip current word
	for x < len(line) && !isSpace(line[x]) {
		x++
	}
	// Skip spaces
	for x < len(line) && isSpace(line[x]) {
		x++
	}
	if x >= len(line) && b.cy < len(b.lines)-1 {
		b.cy++
		b.cx = 0
	} else {
		b.cx = x
	}
}

func (b *Buffer) wordBackward() {
	if b.cx == 0 && b.cy > 0 {
		b.cy--
		b.cx = len(b.lines[b.cy])
		return
	}
	line := b.lines[b.cy]
	x := b.cx
	if x > 0 {
		x--
	}
	for x > 0 && isSpace(line[x]) {
		x--
	}
	for x > 0 && !isSpace(line[x-1]) {
		x--
	}
	b.cx = x
}

func isSpace(ch byte) bool { return ch == ' ' || ch == '\t' }

// --- Editing ---

func (b *Buffer) insertChar(ch byte) {
	line := b.lines[b.cy]
	newLine := make([]byte, len(line)+1)
	for i := 0; i < b.cx; i++ {
		newLine[i] = line[i]
	}
	newLine[b.cx] = ch
	for i := b.cx; i < len(line); i++ {
		newLine[i+1] = line[i]
	}
	b.lines[b.cy] = newLine
	b.cx++
	b.modified = true
}

func (b *Buffer) insertNewline() {
	line := b.lines[b.cy]
	before := copyBytes(line[:b.cx])
	after := copyBytes(line[b.cx:])
	b.lines[b.cy] = before
	// Insert after line into lines slice
	newLines := make([][]byte, len(b.lines)+1)
	for i := 0; i <= b.cy; i++ {
		newLines[i] = b.lines[i]
	}
	newLines[b.cy+1] = after
	for i := b.cy + 1; i < len(b.lines); i++ {
		newLines[i+1] = b.lines[i]
	}
	b.lines = newLines
	b.cy++
	b.cx = 0
	b.modified = true
}

func (b *Buffer) deleteBackward() {
	if b.cx > 0 {
		line := b.lines[b.cy]
		newLine := make([]byte, len(line)-1)
		for i := 0; i < b.cx-1; i++ {
			newLine[i] = line[i]
		}
		for i := b.cx; i < len(line); i++ {
			newLine[i-1] = line[i]
		}
		b.lines[b.cy] = newLine
		b.cx--
		b.modified = true
	} else if b.cy > 0 {
		// Join with previous line
		prev := b.lines[b.cy-1]
		cur := b.lines[b.cy]
		joined := make([]byte, len(prev)+len(cur))
		for i := 0; i < len(prev); i++ {
			joined[i] = prev[i]
		}
		for i := 0; i < len(cur); i++ {
			joined[len(prev)+i] = cur[i]
		}
		b.cx = len(prev)
		b.lines[b.cy-1] = joined
		// Remove current line
		newLines := make([][]byte, len(b.lines)-1)
		for i := 0; i < b.cy; i++ {
			newLines[i] = b.lines[i]
		}
		for i := b.cy + 1; i < len(b.lines); i++ {
			newLines[i-1] = b.lines[i]
		}
		b.lines = newLines
		b.cy--
		b.modified = true
	}
}

func (b *Buffer) deleteForward() {
	line := b.lines[b.cy]
	if b.cx < len(line) {
		newLine := make([]byte, len(line)-1)
		for i := 0; i < b.cx; i++ {
			newLine[i] = line[i]
		}
		for i := b.cx + 1; i < len(line); i++ {
			newLine[i-1] = line[i]
		}
		b.lines[b.cy] = newLine
		b.modified = true
	} else if b.cy < len(b.lines)-1 {
		// Join with next line
		next := b.lines[b.cy+1]
		joined := make([]byte, len(line)+len(next))
		for i := 0; i < len(line); i++ {
			joined[i] = line[i]
		}
		for i := 0; i < len(next); i++ {
			joined[len(line)+i] = next[i]
		}
		b.lines[b.cy] = joined
		newLines := make([][]byte, len(b.lines)-1)
		for i := 0; i <= b.cy; i++ {
			newLines[i] = b.lines[i]
		}
		for i := b.cy + 2; i < len(b.lines); i++ {
			newLines[i-1] = b.lines[i]
		}
		b.lines = newLines
		b.modified = true
	}
}

func (b *Buffer) deleteLine() {
	if len(b.lines) == 1 {
		b.lines[0] = []byte{}
		b.cx = 0
		b.modified = true
		return
	}
	newLines := make([][]byte, len(b.lines)-1)
	for i := 0; i < b.cy; i++ {
		newLines[i] = b.lines[i]
	}
	for i := b.cy + 1; i < len(b.lines); i++ {
		newLines[i-1] = b.lines[i]
	}
	b.lines = newLines
	if b.cy >= len(b.lines) {
		b.cy = len(b.lines) - 1
	}
	b.clampCX()
	b.modified = true
}

func (b *Buffer) openLineBelow() {
	newLines := make([][]byte, len(b.lines)+1)
	for i := 0; i <= b.cy; i++ {
		newLines[i] = b.lines[i]
	}
	newLines[b.cy+1] = []byte{}
	for i := b.cy + 1; i < len(b.lines); i++ {
		newLines[i+1] = b.lines[i]
	}
	b.lines = newLines
	b.cy++
	b.cx = 0
	b.modified = true
}

func (b *Buffer) openLineAbove() {
	newLines := make([][]byte, len(b.lines)+1)
	for i := 0; i < b.cy; i++ {
		newLines[i] = b.lines[i]
	}
	newLines[b.cy] = []byte{}
	for i := b.cy; i < len(b.lines); i++ {
		newLines[i+1] = b.lines[i]
	}
	b.cx = 0
	b.modified = true
}
