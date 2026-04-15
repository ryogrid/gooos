package main

import "github.com/ryogrid/gooos/user/gooos"

// Reserved fd slots used by executeCommand to save and restore
// stdin / stdout across redirection. Picked above 2 (stdio)
// and below the procMaxFDs (16) cap.
const (
	savedStdinFD  = 10
	savedStdoutFD = 11
)

func main() {
	gooos.VgaClear()
	gooos.Println("gooos shell v0.1")
	gooos.Println("Type 'help' for available commands.")
	gooos.Println("")

	for {
		gooos.Print("$ ")
		line := gooos.ReadLine()
		if len(line) == 0 {
			continue
		}
		c, ok := parseLine(line)
		if !ok {
			gooos.Println("sh: syntax error")
			continue
		}
		executeCmdLine(c)
	}
}

// executeCmdLine applies any redirection in c, dispatches to
// a built-in or external command, then restores the shell's
// own stdio so the next prompt sees the console again.
func executeCmdLine(c cmdLine) {
	needRestore := c.stdinFile != "" || c.stdoutFile != ""

	if needRestore {
		// Save current stdio. Dup2 returns the new fd on
		// success; ignore the value (we know the slot we asked
		// for). On failure (table full) bail out.
		if gooos.Dup2(gooos.Stdin, savedStdinFD) < 0 ||
			gooos.Dup2(gooos.Stdout, savedStdoutFD) < 0 {
			gooos.Println("sh: out of fd slots")
			return
		}
	}

	if c.stdinFile != "" {
		fd := gooos.Open(c.stdinFile, gooos.OpenRead)
		if fd < 0 {
			gooos.Println("sh: " + c.stdinFile + ": cannot open")
			restoreStdio(needRestore)
			return
		}
		gooos.Dup2(fd, gooos.Stdin)
		gooos.Close(fd)
	}
	if c.stdoutFile != "" {
		mode := gooos.OpenWrite
		if c.appendOut {
			mode = gooos.OpenAppend
		}
		fd := gooos.Open(c.stdoutFile, mode)
		if fd < 0 {
			gooos.Println("sh: " + c.stdoutFile + ": cannot open")
			restoreStdio(needRestore)
			return
		}
		gooos.Dup2(fd, gooos.Stdout)
		gooos.Close(fd)
	}

	runCommand(c.argv)

	restoreStdio(needRestore)
}

// restoreStdio undoes the dup2 dance from executeCmdLine. No-op
// if the dance was skipped.
func restoreStdio(saved bool) {
	if !saved {
		return
	}
	gooos.Dup2(savedStdinFD, gooos.Stdin)
	gooos.Close(savedStdinFD)
	gooos.Dup2(savedStdoutFD, gooos.Stdout)
	gooos.Close(savedStdoutFD)
}

// runCommand dispatches argv[0] to a built-in or exec's the
// matching ELF. Built-ins write through the shell's currently
// installed stdio (which may be redirected by executeCmdLine).
func runCommand(argv []string) {
	cmd := argv[0]
	args := joinArgs(argv)
	switch cmd {
	case "help":
		cmdHelp()
	case "echo":
		cmdEcho(args)
	case "clear":
		gooos.VgaClear()
	case "exit":
		gooos.Println("Halting system.")
		gooos.Exit(0)
	default:
		filename := cmd + ".elf"
		exitCode := gooos.Exec(filename, args)
		if exitCode == -1 {
			gooos.Println("sh: command not found: " + cmd)
		}
	}
}

func cmdHelp() {
	gooos.Println("Built-in commands:")
	gooos.Println("  help       Show this help message")
	gooos.Println("  echo       Print arguments")
	gooos.Println("  clear      Clear the screen")
	gooos.Println("  exit       Halt the system")
	gooos.Println("")
	gooos.Println("External commands:")
	gooos.Println("  ls         List files")
	gooos.Println("  cat FILE   Display file contents")
	gooos.Println("  wc FILE    Count lines, words, bytes")
	gooos.Println("  hello      Print greeting")
	gooos.Println("  fdprobe    Verify the fd-table syscalls")
	gooos.Println("")
	gooos.Println("Redirection:")
	gooos.Println("  cmd > file       stdout to file (truncate)")
	gooos.Println("  cmd >> file      stdout to file (append)")
	gooos.Println("  cmd < file       stdin from file")
}

func cmdEcho(args string) {
	gooos.Println(args)
}
