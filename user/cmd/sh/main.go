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

// executePipeline runs a parsed pipeline. Single-stage uses
// the existing redirect-aware path. Multi-stage uses
// concurrent pipes: every adjacent pair is connected by a
// kernel chan-byte pipe (sys_pipe); each stage is Spawn'd
// concurrently; the shell Wait's on the tail.
func executePipeline(p pipeline) {
	if len(p.stages) == 0 {
		return
	}
	if len(p.stages) == 1 {
		executeCmdLine(p.stages[0])
		return
	}
	executeConcurrentPipe(p.stages)
}

// executeConcurrentPipe spawns N stages connected by N-1
// pipes. POSIX hygiene: every pipe end has at most two
// holders at any moment (the shell's original slot + the
// just-dup'd stdin/stdout slot), and the shell drops its
// ORIGINAL slot reference right after the child that
// inherits that end is spawned. That way the final holder
// is the child, and its processExit closes the last ref.
func executeConcurrentPipe(stages []cmdLine) {
	n := len(stages)
	pids := make([]int, n)

	// Pre-allocate all pipes. pipes[i] connects stage i
	// (writer) → stage i+1 (reader).
	type pipeFds struct{ r, w int }
	pipes := make([]pipeFds, n-1)
	for i := 0; i < n-1; i++ {
		r, w, perr := gooos.Pipe()
		if perr < 0 {
			gooos.Println("sh: pipe failed")
			return
		}
		pipes[i] = pipeFds{r: r, w: w}
	}

	if gooos.Dup2(gooos.Stdin, savedStdinFD) < 0 ||
		gooos.Dup2(gooos.Stdout, savedStdoutFD) < 0 {
		gooos.Println("sh: out of fd slots")
		return
	}

	for i := 0; i < n; i++ {
		// stdin
		if i == 0 {
			gooos.Dup2(savedStdinFD, gooos.Stdin)
		} else {
			gooos.Dup2(pipes[i-1].r, gooos.Stdin)
		}
		// stdout
		if i == n-1 {
			gooos.Dup2(savedStdoutFD, gooos.Stdout)
		} else {
			gooos.Dup2(pipes[i].w, gooos.Stdout)
		}

		if isBuiltin(stages[i].argv[0]) {
			executeCmdLine(stages[i])
			pids[i] = -1
		} else {
			pid, serr := gooos.Spawn(stages[i].argv[0]+".elf", joinArgs(stages[i].argv))
			if serr < 0 {
				gooos.Println("sh: spawn failed: " + stages[i].argv[0])
				pids[i] = -1
			} else {
				pids[i] = pid
			}
		}

		// Drop the shell's ORIGINAL-slot references for the
		// pipe ends this stage just took possession of. Stage i:
		//   - read end: pipes[i-1].r (if i > 0) — stage i
		//     inherited it; shell doesn't need it anymore.
		//   - write end: pipes[i].w (if i < n-1) — stage i
		//     inherited it; shell doesn't need it.
		// Without these closes, the shell would keep extra
		// references and the child ends up racing on them via
		// its own inherited copies of the shell's original
		// slots.
		if i > 0 {
			gooos.Close(pipes[i-1].r)
		}
		if i < n-1 {
			gooos.Close(pipes[i].w)
		}
	}

	// Restore shell stdio. The originals were already saved
	// (and are still at savedStdinFD / savedStdoutFD).
	gooos.Dup2(savedStdinFD, gooos.Stdin)
	gooos.Close(savedStdinFD)
	gooos.Dup2(savedStdoutFD, gooos.Stdout)
	gooos.Close(savedStdoutFD)

	// Wait on each spawned stage. Built-ins ran synchronously
	// above with pid==-1.
	for i := 0; i < n; i++ {
		if pids[i] >= 0 {
			gooos.Wait(pids[i])
		}
	}
}

// isBuiltin returns true iff cmd is a shell built-in (handled
// in-process rather than exec'd).
func isBuiltin(cmd string) bool {
	switch cmd {
	case "help", "echo", "clear", "exit":
		return true
	}
	return false
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
