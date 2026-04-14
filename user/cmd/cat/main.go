package main

import "github.com/ryogrid/gooos/user/gooos"

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
