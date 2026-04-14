package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	names := gooos.ListDir()
	for _, name := range names {
		gooos.Println(name)
	}
}
