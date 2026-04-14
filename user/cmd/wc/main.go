package main

import "github.com/ryogrid/gooos/user/gooos"

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

	lines, words := 0, 0
	bytes := len(data)
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
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
