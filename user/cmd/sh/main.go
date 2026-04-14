package main

import "github.com/ryogrid/gooos/user/gooos"

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
		cmd, args := parseCommand(line)
		executeCommand(cmd, args)
	}
}

// parseCommand splits input on the first space into command and arguments.
func parseCommand(line string) (string, string) {
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			return line[:i], line[i+1:]
		}
	}
	return line, ""
}

// executeCommand dispatches to built-in commands or tries an external ELF.
func executeCommand(cmd, args string) {
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
		// Try to execute as external command.
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
}

func cmdEcho(args string) {
	gooos.Println(args)
}
