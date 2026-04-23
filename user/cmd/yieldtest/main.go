// yieldtest — test Yield() as alternative to Sleep()
package main

import (
	"github.com/ryogrid/gooos/user/gooos"
)

func main() {
	gooos.Println("yieldtest: begin")

	// Test 1: Single yield
	gooos.Println("yieldtest: calling Yield once...")
	gooos.Yield()
	gooos.Println("yieldtest: Yield 1 OK")

	// Test 2: Second yield
	gooos.Println("yieldtest: calling Yield second time...")
	gooos.Yield()
	gooos.Println("yieldtest: Yield 2 OK")

	// Test 3: Third yield
	gooos.Println("yieldtest: calling Yield third time...")
	gooos.Yield()
	gooos.Println("yieldtest: Yield 3 OK")

	gooos.Println("yieldtest: ALL YIELDS PASS")
}
