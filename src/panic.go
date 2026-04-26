// src/panic.go — allocation-free kernel-panic output helpers.
//
// Used by fatal ISR handlers (handlePageFault, handleDivisionError)
// where heap allocation would race with the conservative GC.
//
// Single-threaded discipline: a fatal handler runs once and then
// halts; there is no concurrent user of panicHexBuf.

package main

import "unsafe"

// panicHexBuf is the scratch area every panic helper formats into.
// 96 bytes fits the longest message (PF: addr=… err=… rip=…) with
// margin.
var panicHexBuf [96]byte

// appendStr writes s into buf starting at off and returns the new
// offset. Caller is responsible for buffer bounds.
//
//go:nosplit
func appendStr(buf []byte, off int, s string) int {
	for i := 0; i < len(s); i++ {
		buf[off] = s[i]
		off++
	}
	return off
}

// appendDec formats v as a base-10 integer into buf starting at
// off and returns the new offset. ISR-safe (no allocation).
//
//go:nosplit
func appendDec(buf []byte, off int, v uint64) int {
	if v == 0 {
		buf[off] = '0'
		return off + 1
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	for ; i < len(tmp); i++ {
		buf[off] = tmp[i]
		off++
	}
	return off
}

// appendHex formats v as "0xHHHHHHHHHHHHHHHH" into buf starting at
// off and returns the new offset.
//
//go:nosplit
func appendHex(buf []byte, off int, v uint64) int {
	const hex = "0123456789ABCDEF"
	buf[off] = '0'
	off++
	buf[off] = 'x'
	off++
	for i := 60; i >= 0; i -= 4 {
		buf[off] = hex[(v>>uint(i))&0xF]
		off++
	}
	return off
}

// bytesToString reinterprets a byte slice as a string without
// copying. Lifetime of the result is tied to the underlying slice;
// safe here because panicHexBuf is .bss and the caller halts before
// the slice goes out of scope.
//
//go:nosplit
func bytesToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// gooosStackOverflow is invoked from the patched
// internal/task.Pause() when a goroutine's stack canary check
// fails. Prints the offending Task pointer plus its stackTop /
// canary addresses on serial, then halts.
//
// The corrupted goroutine's stack is unreliable here, so the
// helper allocates nothing, takes no parameters by value-copy of
// any complex type, and is //go:nosplit.
//
// Linkname target matches the patched runtime side
// (~/.local/tinygo/src/internal/task/task_stack.go).
//
//go:linkname gooosStackOverflow runtime.gooosStackOverflow
//go:nosplit
func gooosStackOverflow(t uintptr) {
	off := 0
	off = appendStr(panicHexBuf[:], off, "STACK OVERFLOW: task=")
	off = appendHex(panicHexBuf[:], off, uint64(t))
	if t != 0 {
		top := *(*uintptr)(unsafe.Pointer(t + stackTopOffset))
		canary := *(*uintptr)(unsafe.Pointer(t + stackTopOffset - 8))
		off = appendStr(panicHexBuf[:], off, " top=")
		off = appendHex(panicHexBuf[:], off, uint64(top))
		off = appendStr(panicHexBuf[:], off, " canaryPtr=")
		off = appendHex(panicHexBuf[:], off, uint64(canary))
	}
	vgaWriteLine(15, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')
	for {
		hlt()
	}
}
