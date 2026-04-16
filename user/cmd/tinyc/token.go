package main

// TokenKind classifies a lexical token.
type TokenKind int

const (
	TkEOF     TokenKind = iota
	TkNum               // integer literal
	TkStr               // "..." string literal
	TkIdent             // identifier
	TkVar               // var
	TkIf                // if
	TkElse              // else
	TkReturn            // return
	TkWhile             // while
	TkFor               // for
	TkPrintln           // println
	TkPlus              // +
	TkMinus             // -
	TkStar              // *
	TkSlash             // /
	TkPercent           // %
	TkLt                // <
	TkGt                // >
	TkLe                // <=
	TkGe                // >=
	TkEqEq              // ==
	TkNeq               // !=
	TkAssign            // =
	TkLParen            // (
	TkRParen            // )
	TkLBrace            // {
	TkRBrace            // }
	TkLBracket          // [
	TkRBracket          // ]
	TkComma             // ,
	TkSemicolon         // ;
)

// Token is a single lexical unit.
type Token struct {
	Kind TokenKind
	Lit  string // literal text for ident/string
	Num  int64  // value for TkNum
	Line int    // source line (1-based)
}

// keywords maps reserved words to their token kinds.
var keywords = map[string]TokenKind{
	"var":     TkVar,
	"if":      TkIf,
	"else":    TkElse,
	"return":  TkReturn,
	"while":   TkWhile,
	"for":     TkFor,
	"println": TkPrintln,
}
