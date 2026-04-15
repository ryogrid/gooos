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
		p, ok := parsePipeline(line)
		if !ok {
			gooos.Println("sh: syntax error")
			continue
		}
		executePipeline(p)
	}
}

// executePipeline runs a parsed pipeline. Single-stage falls
// through to the existing redirect-aware path. Two-stage uses
// the sequential-pipe variant from src/pipe.go: stage 1's
// stdout is dup2'd onto a pipe write end; stage 1 runs to
// completion buffering its output; stage 2's stdin is dup2'd
// onto the pipe read end; stage 2 reads the buffered data.
//
// Three+ stage pipelines are not supported by the sequential
// variant — phase 5's concurrent pipe handles them.
func executePipeline(p pipeline) {
	switch len(p.stages) {
	case 0:
		return
	case 1:
		executeCmdLine(p.stages[0])
		return
	case 2:
		executeTwoStagePipe(p.stages[0], p.stages[1])
		return
	default:
		gooos.Println("sh: multi-stage pipelines (>2) not supported in this round (phase 5)")
		return
	}
}

func executeTwoStagePipe(stage1, stage2 cmdLine) {
	rfd, wfd, err := gooos.Pipe()
	if err < 0 {
		gooos.Println("sh: pipe failed")
		return
	}

	// Stage 1: dup the write end onto stdout, exec, then
	// restore. Closing wfd happens AFTER exec returns (and
	// after restoring stdout) so the *seqPipeWriter is not
	// marked closed prematurely. Stage 1's processExit
	// closes its inherited fd 1 — that is harmless because
	// our seqPipe Close is idempotent.
	if gooos.Dup2(gooos.Stdout, savedStdoutFD) < 0 {
		gooos.Close(rfd)
		gooos.Close(wfd)
		gooos.Println("sh: out of fd slots")
		return
	}
	gooos.Dup2(wfd, gooos.Stdout)
	executeCmdLine(stage1)
	gooos.Dup2(savedStdoutFD, gooos.Stdout)
	gooos.Close(savedStdoutFD)
	gooos.Close(wfd) // mark writer done after stage 1 has filled the buffer

	// Stage 2: dup the read end onto stdin, exec, restore.
	if gooos.Dup2(gooos.Stdin, savedStdinFD) < 0 {
		gooos.Close(rfd)
		gooos.Println("sh: out of fd slots")
		return
	}
	gooos.Dup2(rfd, gooos.Stdin)
	executeCmdLine(stage2)
	gooos.Dup2(savedStdinFD, gooos.Stdin)
	gooos.Close(savedStdinFD)
	gooos.Close(rfd)
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
