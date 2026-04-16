// goprobe — userspace goroutine + channel probe.
//
// Exercises every concurrency primitive enabled by the userspace
// TinyGo tasks scheduler (scheduler=tasks on user/target.json):
// go func(), chan, select, time.Sleep, and Yield-driven
// cooperation. Each sub-test prints a PASS marker on its own line
// so the harness (tmp/test_goprobe.sh) can pattern-match. A
// failure prints a FAIL line and exits with code 1.

package main

import (
	"time"

	"github.com/ryogrid/gooos/user/gooos"
)

func main() {
	gooos.Println("goprobe: begin")

	// --- Test 1: go + chan round-trip ---
	ch := make(chan int, 1)
	go func() {
		ch <- 42
	}()
	if v := <-ch; v == 42 {
		gooos.Println("goprobe: go+chan OK")
	} else {
		gooos.Println("goprobe: go+chan FAIL")
		gooos.Exit(1)
	}

	// --- Test 2: select with two ready chans ---
	c1 := make(chan int, 1)
	c2 := make(chan int, 1)
	go func() { c1 <- 1 }()
	go func() { c2 <- 2 }()
	sum := 0
	for i := 0; i < 2; i++ {
		select {
		case x := <-c1:
			sum += x
		case x := <-c2:
			sum += x
		}
	}
	if sum == 3 {
		gooos.Println("goprobe: select OK")
	} else {
		gooos.Println("goprobe: select FAIL")
		gooos.Exit(1)
	}

	// --- Test 3: time.Sleep-driven goroutine interleaving ---
	counter := 0
	done := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			time.Sleep(20 * time.Millisecond)
			counter++
		}
		close(done)
	}()
	<-done
	if counter == 3 {
		gooos.Println("goprobe: time.Sleep OK")
	} else {
		gooos.Println("goprobe: time.Sleep FAIL")
		gooos.Exit(1)
	}

	// --- Test 4: Yield-driven goroutine cycling ---
	//
	// Two goroutines increment their own counters, yielding between
	// iterations. Under cooperative scheduling this converges
	// without data races on single-CPU v1. gooos.Yield() invokes
	// sys_yield, which is a kernel-level yield. The userspace
	// Gosched would stay in-process; either works for this test
	// and sys_yield exercises the kernel dispatch path too.
	sharedA, sharedB := 0, 0
	finished := make(chan int, 2)
	go func() {
		for i := 0; i < 100; i++ {
			sharedA++
			gooos.Yield()
		}
		finished <- 1
	}()
	go func() {
		for i := 0; i < 100; i++ {
			sharedB++
			gooos.Yield()
		}
		finished <- 2
	}()
	<-finished
	<-finished
	if sharedA == 100 && sharedB == 100 {
		gooos.Println("goprobe: yield-cycle OK")
	} else {
		gooos.Println("goprobe: yield-cycle FAIL")
		gooos.Exit(1)
	}

	gooos.Println("goprobe: ALL TESTS PASS")
}
