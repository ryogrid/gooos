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
