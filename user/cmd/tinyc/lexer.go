package main

import "github.com/ryogrid/gooos/user/gooos"

// Lexer tokenizes Tiny C source bytes.
type Lexer struct {
	src  []byte
	pos  int
	line int
}

// NewLexer creates a lexer over the given source.
func NewLexer(src []byte) *Lexer {
	return &Lexer{src: src, pos: 0, line: 1}
}

func (l *Lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *Lexer) advance() byte {
	ch := l.src[l.pos]
	l.pos++
	if ch == '\n' {
		l.line++
	}
	return ch
}

func (l *Lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			l.advance()
			continue
		}
		// C-style line comment
		if ch == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '/' {
			l.pos += 2
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		// C-style block comment
		if ch == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
			l.pos += 2
			for l.pos+1 < len(l.src) {
				if l.src[l.pos] == '\n' {
					l.line++
				}
				if l.src[l.pos] == '*' && l.src[l.pos+1] == '/' {
					l.pos += 2
					break
				}
				l.pos++
			}
			continue
		}
		break
	}
}

func isDigit(ch byte) bool  { return ch >= '0' && ch <= '9' }
func isAlpha(ch byte) bool  { return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' }
func isAlnum(ch byte) bool  { return isAlpha(ch) || isDigit(ch) }

// NextToken returns the next token from the source.
func (l *Lexer) NextToken() Token {
	l.skipWhitespaceAndComments()
	if l.pos >= len(l.src) {
		return Token{Kind: TkEOF, Line: l.line}
	}
	line := l.line
	ch := l.peek()

	// Number literal
	if isDigit(ch) {
		start := l.pos
		var val int64
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			val = val*10 + int64(l.src[l.pos]-'0')
			l.pos++
		}
		return Token{Kind: TkNum, Num: val, Lit: string(l.src[start:l.pos]), Line: line}
	}

	// Identifier or keyword
	if isAlpha(ch) {
		start := l.pos
		for l.pos < len(l.src) && isAlnum(l.src[l.pos]) {
			l.pos++
		}
		word := string(l.src[start:l.pos])
		if kw, ok := keywords[word]; ok {
			return Token{Kind: kw, Lit: word, Line: line}
		}
		return Token{Kind: TkIdent, Lit: word, Line: line}
	}

	// String literal
	if ch == '"' {
		l.advance() // skip opening quote
		start := l.pos
		for l.pos < len(l.src) && l.src[l.pos] != '"' {
			if l.src[l.pos] == '\\' && l.pos+1 < len(l.src) {
				l.pos++ // skip escape
			}
			l.pos++
		}
		s := string(l.src[start:l.pos])
		if l.pos < len(l.src) {
			l.pos++ // skip closing quote
		}
		return Token{Kind: TkStr, Lit: s, Line: line}
	}

	// Two-character operators
	l.advance()
	if l.pos < len(l.src) {
		next := l.src[l.pos]
		switch {
		case ch == '=' && next == '=':
			l.pos++
			return Token{Kind: TkEqEq, Lit: "==", Line: line}
		case ch == '!' && next == '=':
			l.pos++
			return Token{Kind: TkNeq, Lit: "!=", Line: line}
		case ch == '<' && next == '=':
			l.pos++
			return Token{Kind: TkLe, Lit: "<=", Line: line}
		case ch == '>' && next == '=':
			l.pos++
			return Token{Kind: TkGe, Lit: ">=", Line: line}
		}
	}

	// Single-character tokens
	switch ch {
	case '+':
		return Token{Kind: TkPlus, Lit: "+", Line: line}
	case '-':
		return Token{Kind: TkMinus, Lit: "-", Line: line}
	case '*':
		return Token{Kind: TkStar, Lit: "*", Line: line}
	case '/':
		return Token{Kind: TkSlash, Lit: "/", Line: line}
	case '%':
		return Token{Kind: TkPercent, Lit: "%", Line: line}
	case '<':
		return Token{Kind: TkLt, Lit: "<", Line: line}
	case '>':
		return Token{Kind: TkGt, Lit: ">", Line: line}
	case '=':
		return Token{Kind: TkAssign, Lit: "=", Line: line}
	case '(':
		return Token{Kind: TkLParen, Lit: "(", Line: line}
	case ')':
		return Token{Kind: TkRParen, Lit: ")", Line: line}
	case '{':
		return Token{Kind: TkLBrace, Lit: "{", Line: line}
	case '}':
		return Token{Kind: TkRBrace, Lit: "}", Line: line}
	case '[':
		return Token{Kind: TkLBracket, Lit: "[", Line: line}
	case ']':
		return Token{Kind: TkRBracket, Lit: "]", Line: line}
	case ',':
		return Token{Kind: TkComma, Lit: ",", Line: line}
	case ';':
		return Token{Kind: TkSemicolon, Lit: ";", Line: line}
	}

	gooos.Println("tinyc: error: unexpected character '" + string([]byte{ch}) + "' at line " + itoa(line))
	gooos.Exit(1)
	return Token{}
}
