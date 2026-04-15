# BusyBox Shell — Shell Specification

## 1. Overview

The gooos shell (`sh.elf`) is a TinyGo-compiled ELF binary that runs in Ring 3 and provides an interactive command-line interface. It is the first user process launched by the kernel at boot. The shell reads commands from the keyboard, parses them, and either executes built-in functions or loads external ELF binaries via `sys_exec`.

## 2. Shell Main Loop

```
func main() {
    clearScreen()
    println("gooos shell v0.1")
    println("Type 'help' for available commands.")
    println("")

    for {
        print("$ ")
        line := readLine()
        if len(line) == 0 {
            continue
        }
        cmd, args := parseCommand(line)
        executeCommand(cmd, args)
    }
}
```

### 2.1 Command Parsing

The input line is split on the first space character:
- `cmd` = everything before the first space (the command name)
- `args` = everything after the first space (passed to built-in handler or `sys_exec`)

Examples:
- `"hello"` → cmd=`"hello"`, args=`""`
- `"echo foo bar"` → cmd=`"echo"`, args=`"foo bar"`
- `"cat notes.txt"` → cmd=`"cat"`, args=`"notes.txt"`

### 2.2 Command Dispatch

```
func executeCommand(cmd, args string) {
    switch cmd {
    case "help":
        cmdHelp()
    case "echo":
        cmdEcho(args)
    case "clear":
        cmdClear()
    case "exit":
        cmdExit()
    case "uptime":
        cmdUptime()
    default:
        // Try to execute as external command
        filename := cmd + ".elf"
        exitCode := gooos.Exec(filename, args)
        if exitCode == -1 {
            gooos.Println("sh: command not found: " + cmd)
        }
    }
}
```

## 3. Built-in Commands

### 3.1 `help`

```
func cmdHelp() {
    gooos.Println("Built-in commands:")
    gooos.Println("  help       Show this help message")
    gooos.Println("  echo       Print arguments")
    gooos.Println("  clear      Clear the screen")
    gooos.Println("  exit       Halt the system")
    gooos.Println("  uptime     Show system uptime")
    gooos.Println("")
    gooos.Println("External commands:")
    gooos.Println("  ls         List files")
    gooos.Println("  cat FILE   Display file contents")
    gooos.Println("  wc FILE    Count lines, words, bytes")
    gooos.Println("  hello      Print greeting")
}
```

### 3.2 `echo [args...]`

```
func cmdEcho(args string) {
    gooos.Println(args)
}
```

### 3.3 `clear`

```
func cmdClear() {
    gooos.VgaClear()
}
```

Calls `sys_vga_clear` to reset the VGA text buffer.

### 3.4 `exit`

```
func cmdExit() {
    gooos.Println("Halting system.")
    gooos.Exit(0)
}
```

### 3.5 `uptime`

```
func cmdUptime() {
    // sys_sleep(0) could return the current tick count, or we add a
    // dedicated sys_gettime syscall. For simplicity, this command is
    // deferred to a later iteration.
    gooos.Println("uptime: not yet implemented")
}
```

## 4. External Commands

### 4.1 `ls` (`user/cmd/ls/main.go`)

Lists all files in the in-memory filesystem.

```go
package main

import "user/gooos"

func main() {
    names := gooos.ListDir()
    for _, name := range names {
        gooos.Println(name)
    }
}
```

### 4.2 `cat` (`user/cmd/cat/main.go`)

Displays the contents of a named file.

```go
package main

import "user/gooos"

func main() {
    args := gooos.Args()
    if args == "" {
        gooos.Println("usage: cat <filename>")
        gooos.Exit(1)
    }
    data := gooos.ReadFile(args)
    if data == nil {
        gooos.Println("cat: file not found: " + args)
        gooos.Exit(1)
    }
    gooos.Print(string(data))
}
```

### 4.3 `wc` (`user/cmd/wc/main.go`)

Counts lines, words, and bytes in a file.

```go
package main

import "user/gooos"

func main() {
    args := gooos.Args()
    if args == "" {
        gooos.Println("usage: wc <filename>")
        gooos.Exit(1)
    }
    data := gooos.ReadFile(args)
    if data == nil {
        gooos.Println("wc: file not found: " + args)
        gooos.Exit(1)
    }

    lines, words, bytes := 0, 0, len(data)
    inWord := false
    for _, b := range data {
        if b == '\n' {
            lines++
        }
        if b == ' ' || b == '\n' || b == '\t' {
            inWord = false
        } else if !inWord {
            inWord = true
            words++
        }
    }

    gooos.Println(itoa(lines) + " " + itoa(words) + " " + itoa(bytes) + " " + args)
}

func itoa(n int) string {
    if n == 0 { return "0" }
    buf := [20]byte{}
    i := len(buf)
    for n > 0 {
        i--
        buf[i] = byte('0' + n%10)
        n /= 10
    }
    return string(buf[i:])
}
```

### 4.4 `hello` (`user/cmd/hello/main.go`)

```go
package main

import "user/gooos"

func main() {
    gooos.Println("Hello, World from gooos userspace!")
}
```

## 5. VGA Console Behavior

When the shell runs, all console output goes through `sys_write` with fd=0 (VGA+serial):

- Characters are written to the VGA text buffer at the current cursor position
- Newline (`\n`) advances to the next row, column 0
- When the cursor reaches row 25, the screen scrolls up (row 0 is lost, row 24 is cleared)
- `sys_vga_clear` resets the cursor to (0, 0) and clears all cells

The shell prompt (`$ `) and user-typed characters are echoed by the kernel's `sys_read` implementation. Command output is written via `sys_write`.

## 6. Input Handling

### 6.1 Line Editing (handled by kernel `sys_read`)

| Key | Action |
|---|---|
| Printable character | Append to line buffer, echo to VGA+serial |
| Backspace | Remove last character, move cursor back, clear cell |
| Enter | Submit line to caller, echo newline |

### 6.2 Unsupported (Future)

- Arrow keys (command history, cursor movement within line)
- Tab completion
- Ctrl+C (interrupt running command)
- Ctrl+D (EOF / exit shell)

## 7. Error Handling

| Condition | Behavior |
|---|---|
| Unknown command | Print `"sh: command not found: <cmd>"` |
| `sys_exec` returns -1 | File not found or invalid ELF → print error |
| File not found (`cat`, `wc`) | Print `"<cmd>: file not found: <file>"`, exit 1 |
| Empty input | Skip (re-prompt) |

## 8. Example Session

```
gooos shell v0.1
Type 'help' for available commands.

$ help
Built-in commands:
  help       Show this help message
  echo       Print arguments
  clear      Clear the screen
  exit       Halt the system
  uptime     Show system uptime

External commands:
  ls         List files
  cat FILE   Display file contents
  wc FILE    Count lines, words, bytes
  hello      Print greeting

$ hello
Hello, World from gooos userspace!
$ echo foo bar baz
foo bar baz
$ ls
sh.elf
cat.elf
ls.elf
wc.elf
hello.elf
notes.txt
$ cat notes.txt
This is a test file.
$ wc notes.txt
1 5 21 notes.txt
$ exit
Halting system.
```
