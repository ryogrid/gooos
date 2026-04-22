// gochan — demonstrates native userspace goroutine + channel usage
// on gooos. Intended to be executed from the gooos shell:
//
//	$ gochan
//
// Two mini-demos:
//  1. A 3-stage pipeline (producer → squarer → printer). The producer
//     and squarer run on goroutines; the main goroutine acts as the
//     printer so the demo stays stable on the current TinyGo target.
//  2. A `select` over two tickers that fire at different intervals.
//
// Unlike goprobe (which is a PASS/FAIL probe), this command prints
// user-facing output showing the actual values flowing through the
// channels, so the demo is observable on serial + VGA.

package main

import (
	"strconv"

	"github.com/ryogrid/gooos/user/gooos"
)

func main() {
	gooos.Println("gochan: pipeline demo (5 items across 2 goroutines + main)")

	source := make(chan int, 5)
	squared := make(chan int, 5)

	// Stage 1: enqueue 1..5 on the main goroutine.
	for i := 1; i <= 5; i++ {
		source <- i
	}

	// Stage 2: square every input on a worker goroutine.
	go func() {
		for i := 0; i < 5; i++ {
			n := <-source
			squared <- n * n
		}
	}()

	// Stage 3 runs on main goroutine: print five squared values.
	for i := 0; i < 5; i++ {
		v := <-squared
		gooos.Println("gochan: squared=" + strconv.Itoa(v))
	}

	gooos.Println("gochan: select over two ready channels (alpha/beta)")
	a := make(chan string, 1)
	b := make(chan string, 1)
	go func() { a <- "alpha" }()
	go func() { b <- "beta" }()
	for i := 0; i < 2; i++ {
		select {
		case v := <-a:
			gooos.Println("gochan: got " + v)
		case v := <-b:
			gooos.Println("gochan: got " + v)
		}
	}

	gooos.Println("gochan: finished")
}
