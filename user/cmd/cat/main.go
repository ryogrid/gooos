package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	args := gooos.Args()
	if args == "" {
		// No filename arg → read stdin until EOF (POSIX cat).
		var buf [256]byte
		for {
			n := gooos.Read(gooos.Stdin, buf[:])
			if n <= 0 {
				break
			}
			gooos.Write(gooos.Stdout, buf[:n])
		}
		return
	}
	data := gooos.ReadFile(args)
	if data == nil {
		gooos.Println("cat: file not found: " + args)
		gooos.Exit(1)
	}
	gooos.Print(string(data))
}
