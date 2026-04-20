// markerprint — periodic sibling workload for feature 2.3 anti-starvation
// testing. Prints 20 marker lines with a short sleep between each.
// Used alongside `cpuhog &` to verify that a hostile spinning sibling
// does not starve cooperative-yield-friendly processes.

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

const (
	iterations = 20
	sleepMs    = 100
)

func main() {
	gooos.Println("markerprint: start cpu=" + strconv.Itoa(gooos.GetCpuID()))
	for i := 0; i < iterations; i++ {
		gooos.Println("marker " + strconv.Itoa(i) +
			" cpu=" + strconv.Itoa(gooos.GetCpuID()))
		gooos.Sleep(sleepMs)
	}
	gooos.Println("markerprint: done")
}
