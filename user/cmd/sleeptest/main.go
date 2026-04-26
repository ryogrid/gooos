// sleeptest — minimal test for multiple Sleep calls
package main

import (
	"github.com/ryogrid/gooos/user/gooos"
)

func main() {
	gooos.Println("sleeptest: begin")

	// Test 1: Single sleep
	gooos.Println("sleeptest: calling Sleep once...")
	gooos.Sleep(10)
	gooos.Println("sleeptest: Sleep 1 OK")

	// Test 2: Second sleep
	gooos.Println("sleeptest: calling Sleep second time...")
	gooos.Sleep(10)
	gooos.Println("sleeptest: Sleep 2 OK")

	// Test 3: Third sleep
	gooos.Println("sleeptest: calling Sleep third time...")
	gooos.Sleep(10)
	gooos.Println("sleeptest: Sleep 3 OK")

	gooos.Println("sleeptest: ALL SLEEPS PASS")
}
