package main

import (
	"strconv"
	"strings"

	"github.com/ryogrid/gooos/user/gooos"
)

// Reserved fd slots used by executeCommand to save and restore
// stdin / stdout across redirection. Picked above 2 (stdio)
// and below the procMaxFDs (16) cap.
const (
	savedStdinFD  = 10
	savedStdoutFD = 11
)

const autorunScriptName = ".autorun.sh"

func main() {
	gooos.VgaClear()
	gooos.Println("gooos shell v0.1")
	gooos.Println("Type 'help' for available commands.")
	gooos.Println("")
	if gooos.Args() == "--autorun" {
		runAutorunIfPresent()
	}
	gooos.ShellReady()

	for {
		reapBackgroundJobs()
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
		executePipeline(p, p.background)
	}
}

func runAutorunIfPresent() {
	script := gooos.ReadFile(autorunScriptName)
	if script == nil || len(script) == 0 {
		return
	}
	// Let early boot goroutines settle so the first autorun command is
	// less sensitive to immediate post-shell-start scheduling jitter.
	for i := 0; i < 50; i++ {
		gooos.Yield()
	}
	gooos.Println("autorun: start")
	text := string(script)
	start := 0
	for i := 0; i <= len(text); i++ {
		if i != len(text) && text[i] != '\n' {
			continue
		}
		line := strings.TrimSpace(text[start:i])
		start = i + 1
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}
		gooos.Println("autorun: exec " + line)
		p, ok := parsePipeline(line)
		if !ok {
			gooos.Println("autorun: syntax error: " + line)
			continue
		}
		executePipeline(p, p.background)
		gooos.Println("autorun: done " + line)
	}
	if !gooos.WriteFile(autorunScriptName, []byte{}) {
		gooos.Println("autorun: cleanup failed")
		return
	}
	gooos.Println("autorun: done")
}

// executePipeline runs a parsed pipeline. Single-stage uses
// the existing redirect-aware path. Multi-stage uses
// concurrent pipes: every adjacent pair is connected by a
// kernel chan-byte pipe (sys_pipe); each stage is Spawn'd
// concurrently; the shell Wait's on the tail.
//
// When background == true (trailing & in the command line), the
// shell spawns the stages but does NOT wait; PIDs are registered
// in the jobs table (see user/cmd/sh/jobs.go) and reaped lazily
// between prompts via reapBackgroundJobs(). Feature 2.4.
func executePipeline(p pipeline, background bool) {
	if len(p.stages) == 0 {
		return
	}
	if len(p.stages) == 1 {
		executeCmdLine(p.stages[0], background)
		return
	}
	executeConcurrentPipe(p.stages, background)
}

// executeConcurrentPipe spawns N stages connected by N-1
// pipes. POSIX hygiene: every pipe end has at most two
// holders at any moment (the shell's original slot + the
// just-dup'd stdin/stdout slot), and the shell drops its
// ORIGINAL slot reference right after the child that
// inherits that end is spawned. That way the final holder
// is the child, and its processExit closes the last ref.
func executeConcurrentPipe(stages []cmdLine, background bool) {
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
			// Built-ins always run synchronously in-process. A
			// pipeline stage that's a builtin can't be backgrounded
			// on its own; the ensemble background-ness applies to
			// the externally-spawned stages.
			executeCmdLine(stages[i], false)
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

	if background {
		// POSIX: a backgrounded pipeline registers every spawned
		// stage (built-ins stayed foreground; their pids[i] == -1
		// and are skipped). One completion line per stage when the
		// reap poll catches it (impldoc/shell_background_jobs.md §9).
		for i := 0; i < n; i++ {
			if pids[i] < 0 {
				continue
			}
			id := registerJob(pids[i], stages[i].argv[0])
			if id < 0 {
				gooos.Println("sh: too many background jobs; waiting on stage " +
					strconv.Itoa(i))
				gooos.Wait(pids[i])
				continue
			}
			gooos.Println("[" + strconv.Itoa(id) + "] " +
				strconv.Itoa(pids[i]) + " " + stages[i].argv[0])
		}
		return
	}

	// Foreground: wait on each spawned stage. Built-ins ran
	// synchronously above with pid==-1.
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
//
// When background == true, external commands are spawned via
// gooos.Spawn + registerJob (no Wait); built-ins fall back to
// synchronous execution (no background builtins). Feature 2.4.
func executeCmdLine(c cmdLine, background bool) {
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

	if background && !isBuiltin(c.argv[0]) {
		runCommandBackground(c.argv)
	} else {
		runCommand(c.argv)
	}

	restoreStdio(needRestore)
}

// runCommandBackground spawns argv[0].elf without waiting; records
// the PID in the jobs table (see jobs.go) and prints the standard
// `[id] pid cmd` notification. Builtins cannot be backgrounded; the
// caller (executeCmdLine) filters them out upstream.
func runCommandBackground(argv []string) {
	cmd := argv[0]
	args := joinArgs(argv)
	pid, errno := gooos.Spawn(cmd+".elf", args)
	if pid < 0 {
		gooos.Println("sh: spawn failed: " + cmd + " (errno=" +
			strconv.Itoa(errno) + ")")
		return
	}
	id := registerJob(pid, cmd)
	if id < 0 {
		// Table full — still spawned; synthesize an immediate wait
		// to avoid leaking the pid entirely. Reverts to foreground
		// semantics for this one call; documented in
		// impldoc/shell_background_jobs.md §2.3.
		gooos.Println("sh: too many background jobs; running foreground")
		gooos.Wait(pid)
		return
	}
	gooos.Println("[" + strconv.Itoa(id) + "] " + strconv.Itoa(pid) + " " + cmd)
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
