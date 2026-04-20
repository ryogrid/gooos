// ps — list running processes via sys_listprocs (feature 2.5).
//
// Column layout: PID PPID S CPU TICKS NAME.
// Invoked from the gooos shell as `$ ps`.

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const maxRows = 32

func main() {
	var buf [maxRows]gooos.ProcInfo
	n, errno := gooos.Listprocs(buf[:])
	if n < 0 {
		gooos.Println("ps: listprocs failed, errno=" + strconv.Itoa(errno))
		gooos.Exit(1)
		return
	}

	gooos.Println("  PID  PPID  S  CPU    TICKS  NAME")
	for i := 0; i < n; i++ {
		p := &buf[i]
		line := pad(strconv.Itoa(int(p.PID)), 5) +
			pad(strconv.Itoa(int(p.PPID)), 6) +
			"  " + p.StateString() +
			pad(strconv.Itoa(int(p.LastCpuID)), 5) +
			pad(strconv.FormatUint(p.Ticks, 10), 9) +
			"  " + p.NameString()
		gooos.Println(line)
	}
}

// pad right-aligns s inside a width-char field.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	out := make([]byte, width)
	spaces := width - len(s)
	for i := 0; i < spaces; i++ {
		out[i] = ' '
	}
	for i := 0; i < len(s); i++ {
		out[spaces+i] = s[i]
	}
	return string(out)
}
