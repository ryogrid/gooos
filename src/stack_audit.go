// src/stack_audit.go — boot-time per-goroutine stack-size audit.
//
// Reports each known kernel goroutine's stack capacity and
// high-water usage on serial. A goroutine whose used/size ratio
// crosses warnThreshold is flagged; the recommended response is
// to bump default-stack-size in src/target.json (see
// impldoc/deferred_gc_and_stacks.md §4).
//
// The audit is gated by `runStackAudit`. Leave it off in
// release builds; the goroutine-handle capture has zero
// runtime cost.

package main

import "unsafe"

// runStackAudit toggles the audit. Flip to true, rebuild, run,
// inspect serial output, then flip back before committing.
const runStackAudit = false

// warnThreshold flags any goroutine whose stack usage exceeds
// this fraction of capacity.
const warnThreshold = 75

// Captured task handles. Each kernel goroutine writes its own
// Task pointer here on entry; nil means "not yet started".
// Only the audit reads them, and only after the goroutine has
// run at least once.
var (
	fsTaskHandle         uintptr
	ring3WrapperHandle   uintptr
)

// taskCanaryAddr reads the canary pointer field (offset 32) and
// returns its dereferenced address — the stack bottom.
func taskCanaryAddr(t uintptr) uintptr {
	cp := *(**uintptr)(unsafe.Pointer(t + stackTopOffset - 8))
	if cp == nil {
		return 0
	}
	return uintptr(unsafe.Pointer(cp))
}

// taskSP reads state.sp (offset 24) for a parked task. Result
// is meaningless for the currently-running task.
func taskSP(t uintptr) uintptr {
	return *(*uintptr)(unsafe.Pointer(t + stackTopOffset - 16))
}

// stackSizeAudit prints a one-line report per captured handle.
// Caller is responsible for ensuring each captured goroutine
// has parked at least once (so state.sp is valid).
func stackSizeAudit() {
	if !runStackAudit {
		return
	}
	serialPrintln("stack-audit: begin")
	auditOne("main", taskCurrent())
	auditOne("fsTask", fsTaskHandle)
	auditOne("ring3Wrapper", ring3WrapperHandle)
	serialPrintln("stack-audit: end")
}

func auditOne(name string, t uintptr) {
	if t == 0 {
		serialPrintln("stack-audit: " + name + " not captured")
		return
	}
	top := taskStackTop(t)
	bottom := taskCanaryAddr(t)
	sp := taskSP(t)
	if top == 0 || bottom == 0 || top <= bottom {
		serialPrintln("stack-audit: " + name + " bogus layout")
		return
	}
	size := top - bottom
	used := uintptr(0)
	if sp >= bottom && sp <= top {
		used = top - sp
	}
	pct := uint64(0)
	if size > 0 {
		pct = uint64(used) * 100 / uint64(size)
	}
	msg := "stack-audit: " + name +
		" size=" + utoa(uint64(size)) +
		" used=" + utoa(uint64(used)) +
		" (" + utoa(pct) + "%)"
	if pct >= warnThreshold {
		msg += "  WARN: exceeds " + utoa(warnThreshold) + "%"
	}
	serialPrintln(msg)
}
