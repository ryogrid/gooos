# TODO — Tiny C Interpreter

Design source: `impldoc/tinyc_interpreter.md`.
One git commit per top-level item.

## Items

- [x] **1. Implement Tiny C interpreter (6 source files)**
  - [x] `user/cmd/tinyc/token.go` — token types + keyword table
  - [x] `user/cmd/tinyc/ast.go` — AST node types
  - [x] `user/cmd/tinyc/lexer.go` — hand-written lexer
  - [x] `user/cmd/tinyc/parser.go` — recursive-descent parser
  - [x] `user/cmd/tinyc/eval.go` — tree-walk evaluator
  - [x] `user/cmd/tinyc/main.go` — entry point + I/O
  - [x] Standalone `tinygo build` compiles without errors.

- [x] **2. Build integration**
  - [x] `user/Makefile` CMDS adds `tinyc`.
  - [x] `src/main.go` preloads `tinyc.elf`.
  - [x] `make build` clean; `tinyc.elf` = 128664 bytes
        (125.6 KiB, 2.4 KiB headroom under 128 KiB cap).

- [ ] **3. Test fixtures**
  - [ ] `sum.tc` — while-loop sum 0..9 (expected: `s = 45`).
  - [ ] `fib.tc` — recursive fibonacci(10) (expected: `55`).
  - [ ] `array.tc` — array + function call (expected: `s = 45`).
  - [ ] `for.tc` — for-loop exercise.
  - [ ] All embedded in FS via `src/main.go`.

- [ ] **4. Harness + PASS**
  - [ ] `tmp/test_tinyc.sh` created + `chmod +x`.
  - [ ] `bash tmp/test_tinyc.sh` → PASS.

- [ ] **5. Regression matrix green**
  - [ ] `tmp/test_sendkey.sh 1` PASS.
  - [ ] `tmp/test_goprobe.sh` PASS.
  - [ ] `tmp/test_gochan.sh` PASS.

- [ ] **6. README update**
  - [ ] Progress table row for Tiny C interpreter.
  - [ ] Usage section with shell invocation + sample output.

- [ ] **7. Reviewer pass + completeness**
  - [ ] Reviewer subagent: no CRITICAL/MAJOR.
  - [ ] `grep -rn 'TODO\|FIXME\|XXX'` — no new markers.
  - [ ] Every checked item has a commit.

## Deferred items

(None yet.)

## Reviewer MINOR notes

(None yet.)
