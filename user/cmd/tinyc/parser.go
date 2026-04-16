package main

import "github.com/ryogrid/gooos/user/gooos"

// Parser is a recursive-descent parser for Tiny C.
type Parser struct {
	lex  *Lexer
	cur  Token
	peek Token
}

// NewParser creates a parser over the given lexer.
func NewParser(lex *Lexer) *Parser {
	p := &Parser{lex: lex}
	p.cur = lex.NextToken()
	p.peek = lex.NextToken()
	return p
}

func (p *Parser) advance() Token {
	t := p.cur
	p.cur = p.peek
	p.peek = p.lex.NextToken()
	return t
}

func (p *Parser) expect(kind TokenKind) Token {
	if p.cur.Kind != kind {
		gooos.Println("tinyc: parse error at line " + itoa(p.cur.Line) + ": unexpected '" + p.cur.Lit + "'")
		gooos.Exit(1)
	}
	return p.advance()
}

// Parse parses the entire program.
func (p *Parser) Parse() *Node {
	prog := &Node{Kind: NdProgram}
	for p.cur.Kind != TkEOF {
		prog.Children = append(prog.Children, p.parseExternalDef())
	}
	return prog
}

func (p *Parser) parseExternalDef() *Node {
	if p.cur.Kind == TkVar {
		return p.parseGlobalVar()
	}
	return p.parseFuncDef()
}

func (p *Parser) parseGlobalVar() *Node {
	p.advance() // consume 'var'
	name := p.expect(TkIdent).Lit
	// Array declaration: var name[size];
	if p.cur.Kind == TkLBracket {
		p.advance()
		size := p.parseExpr()
		p.expect(TkRBracket)
		p.expect(TkSemicolon)
		return &Node{Kind: NdArrayDecl, Name: name, Left: size}
	}
	// Scalar: var name [= expr];
	var init *Node
	if p.cur.Kind == TkAssign {
		p.advance()
		init = p.parseExpr()
	}
	p.expect(TkSemicolon)
	return &Node{Kind: NdVarDecl, Name: name, Init: init}
}

func (p *Parser) parseFuncDef() *Node {
	name := p.expect(TkIdent).Lit
	p.expect(TkLParen)
	var params []string
	if p.cur.Kind != TkRParen {
		params = append(params, p.expect(TkIdent).Lit)
		for p.cur.Kind == TkComma {
			p.advance()
			params = append(params, p.expect(TkIdent).Lit)
		}
	}
	p.expect(TkRParen)
	body := p.parseBlock()
	return &Node{Kind: NdFuncDef, Name: name, Params: params, Body: body}
}

func (p *Parser) parseBlock() *Node {
	p.expect(TkLBrace)
	block := &Node{Kind: NdBlock}
	// Optional local variable declaration
	if p.cur.Kind == TkVar {
		block.Children = append(block.Children, p.parseLocalVars())
	}
	for p.cur.Kind != TkRBrace && p.cur.Kind != TkEOF {
		block.Children = append(block.Children, p.parseStmt())
	}
	p.expect(TkRBrace)
	return block
}

func (p *Parser) parseLocalVars() *Node {
	p.advance() // consume 'var'
	// Create individual NdVarDecl nodes wrapped in a block
	wrapper := &Node{Kind: NdBlock}
	name := p.expect(TkIdent).Lit
	wrapper.Children = append(wrapper.Children, &Node{Kind: NdVarDecl, Name: name})
	for p.cur.Kind == TkComma {
		p.advance()
		name = p.expect(TkIdent).Lit
		wrapper.Children = append(wrapper.Children, &Node{Kind: NdVarDecl, Name: name})
	}
	p.expect(TkSemicolon)
	return wrapper
}

func (p *Parser) parseStmt() *Node {
	switch p.cur.Kind {
	case TkIf:
		return p.parseIf()
	case TkWhile:
		return p.parseWhile()
	case TkFor:
		return p.parseFor()
	case TkReturn:
		return p.parseReturn()
	case TkLBrace:
		return p.parseBlock()
	default:
		expr := p.parseExpr()
		p.expect(TkSemicolon)
		return &Node{Kind: NdExprStmt, Left: expr}
	}
}

func (p *Parser) parseIf() *Node {
	p.advance() // consume 'if'
	p.expect(TkLParen)
	cond := p.parseExpr()
	p.expect(TkRParen)
	then := p.parseStmt()
	var els *Node
	if p.cur.Kind == TkElse {
		p.advance()
		els = p.parseStmt()
	}
	return &Node{Kind: NdIf, Cond: cond, Then: then, Else: els}
}

func (p *Parser) parseWhile() *Node {
	p.advance() // consume 'while'
	p.expect(TkLParen)
	cond := p.parseExpr()
	p.expect(TkRParen)
	body := p.parseStmt()
	return &Node{Kind: NdWhile, Cond: cond, Body: body}
}

func (p *Parser) parseFor() *Node {
	p.advance() // consume 'for'
	p.expect(TkLParen)
	init := p.parseExpr()
	p.expect(TkSemicolon)
	cond := p.parseExpr()
	p.expect(TkSemicolon)
	step := p.parseExpr()
	p.expect(TkRParen)
	body := p.parseStmt()
	return &Node{Kind: NdFor, Init: &Node{Kind: NdExprStmt, Left: init}, Cond: cond, Step: &Node{Kind: NdExprStmt, Left: step}, Body: body}
}

func (p *Parser) parseReturn() *Node {
	p.advance() // consume 'return'
	if p.cur.Kind == TkSemicolon {
		p.advance()
		return &Node{Kind: NdReturn}
	}
	val := p.parseExpr()
	p.expect(TkSemicolon)
	return &Node{Kind: NdReturn, Left: val}
}

// parseExpr is the entry point for expression parsing (lowest precedence).
func (p *Parser) parseExpr() *Node {
	return p.parseAssign()
}

func (p *Parser) parseAssign() *Node {
	left := p.parseCmp()
	// Assignment: var = expr  or  arr[idx] = expr
	if p.cur.Kind == TkAssign {
		p.advance()
		right := p.parseAssign() // right-associative
		if left.Kind == NdVar {
			return &Node{Kind: NdAssign, Name: left.Name, Right: right}
		}
		if left.Kind == NdIndex {
			return &Node{Kind: NdAssign, Left: left, Right: right}
		}
		gooos.Println("tinyc: parse error at line " + itoa(p.cur.Line) + ": invalid assignment target")
		gooos.Exit(1)
	}
	return left
}

func (p *Parser) parseCmp() *Node {
	left := p.parseAdd()
	for p.cur.Kind == TkLt || p.cur.Kind == TkGt || p.cur.Kind == TkLe || p.cur.Kind == TkGe || p.cur.Kind == TkEqEq || p.cur.Kind == TkNeq {
		op := p.advance()
		right := p.parseAdd()
		left = &Node{Kind: NdBinOp, Op: op.Kind, Left: left, Right: right}
	}
	return left
}

func (p *Parser) parseAdd() *Node {
	left := p.parseMul()
	for p.cur.Kind == TkPlus || p.cur.Kind == TkMinus {
		op := p.advance()
		right := p.parseMul()
		left = &Node{Kind: NdBinOp, Op: op.Kind, Left: left, Right: right}
	}
	return left
}

func (p *Parser) parseMul() *Node {
	left := p.parseUnary()
	for p.cur.Kind == TkStar || p.cur.Kind == TkSlash || p.cur.Kind == TkPercent {
		op := p.advance()
		right := p.parseUnary()
		left = &Node{Kind: NdBinOp, Op: op.Kind, Left: left, Right: right}
	}
	return left
}

func (p *Parser) parseUnary() *Node {
	if p.cur.Kind == TkMinus {
		p.advance()
		operand := p.parsePrimary()
		return &Node{Kind: NdUnaryNeg, Left: operand}
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() *Node {
	switch p.cur.Kind {
	case TkNum:
		t := p.advance()
		return &Node{Kind: NdNum, NumVal: t.Num}

	case TkStr:
		t := p.advance()
		return &Node{Kind: NdStr, StrVal: t.Lit}

	case TkLParen:
		p.advance()
		expr := p.parseExpr()
		p.expect(TkRParen)
		return expr

	case TkPrintln:
		return p.parsePrintln()

	case TkIdent:
		name := p.advance().Lit
		// Function call: name(args...)
		if p.cur.Kind == TkLParen {
			p.advance()
			var args []*Node
			if p.cur.Kind != TkRParen {
				args = append(args, p.parseExpr())
				for p.cur.Kind == TkComma {
					p.advance()
					args = append(args, p.parseExpr())
				}
			}
			p.expect(TkRParen)
			return &Node{Kind: NdCall, Name: name, Args: args}
		}
		// Array index: name[expr]
		if p.cur.Kind == TkLBracket {
			p.advance()
			idx := p.parseExpr()
			p.expect(TkRBracket)
			return &Node{Kind: NdIndex, Name: name, Left: idx}
		}
		// Plain variable
		return &Node{Kind: NdVar, Name: name}

	default:
		gooos.Println("tinyc: parse error at line " + itoa(p.cur.Line) + ": unexpected token '" + p.cur.Lit + "'")
		gooos.Exit(1)
		return nil
	}
}

func (p *Parser) parsePrintln() *Node {
	p.advance() // consume 'println'
	p.expect(TkLParen)
	// println(expr) — bare form, sugar for println("%d", expr)
	// println("fmt", expr) — format form
	node := &Node{Kind: NdPrintln}
	first := p.parseExpr()
	if first.Kind == NdStr {
		node.StrVal = first.StrVal
		if p.cur.Kind == TkComma {
			p.advance()
			node.Args = append(node.Args, p.parseExpr())
			for p.cur.Kind == TkComma {
				p.advance()
				node.Args = append(node.Args, p.parseExpr())
			}
		}
	} else {
		// Bare println(expr) — treat as println("%d", expr)
		node.StrVal = "%d"
		node.Args = append(node.Args, first)
	}
	p.expect(TkRParen)
	return node
}
