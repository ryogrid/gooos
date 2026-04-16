// fdprobe — verifies the new fd-table syscalls (sys_open /
// sys_read / sys_write / sys_close) end-to-end.
//
// Opens hello.txt (created by main() before the shell launch),
// reads it via the new Read(fd, buf) path, writes the contents
// to stdout via Write(Stdout, buf), closes the fd, and tries
// to open a missing file to confirm the error path.

package main

import "github.com/ryogrid/gooos/user/gooos"

func main() {
	fd := gooos.Open("hello.txt", gooos.OpenRead)
	if fd < 0 {
		gooos.Println("fdprobe: open(hello.txt) failed")
		gooos.Exit(1)
	}

	var buf [256]byte
	for {
		n := gooos.Read(fd, buf[:])
		if n <= 0 {
			break
		}
		gooos.Write(gooos.Stdout, buf[:n])
	}
	gooos.Close(fd)
	gooos.Println("")
	gooos.Println("fdprobe: read/write OK")

	// Negative path: opening a missing file must return < 0.
	missing := gooos.Open("nope.txt", gooos.OpenRead)
	if missing >= 0 {
		gooos.Println("fdprobe: FAIL — open of missing file should have failed")
		gooos.Close(missing)
		gooos.Exit(1)
	}
	gooos.Println("fdprobe: open-missing returns error OK")
}
