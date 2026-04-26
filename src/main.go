// src/main.go — Conservative GC demo for the gooos bare-metal kernel.
//
// With gc="conservative", TinyGo's mark/sweep GC automatically reclaims
// unreachable objects. This demo allocates many objects, triggers GC, and
// displays reclamation statistics on the VGA text buffer.

package main

import (
	"runtime"
	"unsafe"
)

// cli disables maskable interrupts. Implemented in stubs.S.
//
//go:linkname cli cli
func cli()

// sti enables maskable interrupts. Implemented in stubs.S.
//
//go:linkname sti sti
func sti()

const (
	vgaAddr   = uintptr(0xB8000)
	vgaWidth  = 80
	vgaHeight = 25
	vgaCells  = vgaWidth * vgaHeight
	colorAttr = uint16(0x0F00) // bright white on black
)

// smpShellProbeLaunched prevents duplicate 2.3 probe launches.
var smpShellProbeLaunched uint32

// vgaWriteLine writes a string to the given row of the VGA text buffer.
//
//go:nosplit
func vgaWriteLine(row int, s string) {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	offset := row * vgaWidth
	for i := 0; i < len(s) && offset+i < vgaCells; i++ {
		vga[offset+i] = uint16(s[i]) | colorAttr
	}
}

// vgaClear fills the entire VGA text buffer with spaces.
func vgaClear() {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	for i := 0; i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}
}

// vgaClearLine clears a VGA row from the given column to end of line.
func vgaClearLine(row int, fromCol int) {
	vga := (*[vgaCells]uint16)(unsafe.Pointer(vgaAddr))
	offset := row*vgaWidth + fromCol
	for i := offset; i < (row+1)*vgaWidth && i < vgaCells; i++ {
		vga[i] = uint16(' ') | colorAttr
	}
}

// utoa converts a uint64 to its decimal string representation.
// Implemented manually because importing strconv or fmt would pull in
// OS-dependent runtime code that does not work in bare-metal.
func utoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte // max uint64 is 20 digits
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// allocateGarbage creates a heap-allocated object and returns a pointer.
// The caller discards it, making it garbage collectible.
//
//go:noinline
func allocateGarbage() *[256]byte {
	p := new([256]byte)
	p[0] = 42
	return p
}

// handleDivisionError displays an exception message on VGA and serial
// when vector 0 (#DE - Division Error) fires.
//
// Allocation-free for ISR safety; see src/panic.go.
//
//go:nosplit
func handleDivisionError(vector uint64) {
	off := 0
	off = appendStr(panicHexBuf[:], off, "#DE: division error")
	vgaWriteLine(7, bytesToString(panicHexBuf[:off]))
	serialPrintBytes(panicHexBuf[:off])
	serialPutChar('\r')
	serialPutChar('\n')
}

// handleDefaultIRQ handles any hardware IRQ (vectors 32-47) that does
// not have a specific handler registered. Sends EOI so the PIC is not
// left stuck.
func handleDefaultIRQ(vector uint64) {
	if ioapicActive {
		lapicSendEOI()
	} else {
		irq := uint8(vector - 32)
		picSendEOI(irq)
	}
}

// hlt executes the HLT instruction. Implemented in stubs.S.
//
//go:linkname hlt hlt
func hlt()

func main() {
	vgaClear()

	// Initialize serial output on COM1.
	serialInit()

	// Display and log serial status.
	vgaWriteLine(0, "Serial: OK")
	serialPrintln("Serial: OK")

	// Initialize and load the 256-entry IDT with ISR stubs.
	idtInit()
	vgaWriteLine(1, "IDT: loaded, 256 entries")
	serialPrintln("IDT: loaded, 256 entries")

	// Register exception handlers.
	registerHandler(0, handleDivisionError)
	registerHandler(14, handlePageFault)
	vgaWriteLine(2, "ISR: 256 stubs installed")
	serialPrintln("ISR: 256 stubs installed")

	// Remap 8259A PIC: IRQ 0-7 -> vectors 32-39, IRQ 8-15 -> vectors 40-47.
	picRemap()

	// Register default handlers for all hardware IRQs (vectors 32-47)
	// so that spurious or unhandled IRQs still get EOI and don't hang the PIC.
	for i := 32; i <= 47; i++ {
		registerHandler(i, handleDefaultIRQ)
	}

	// Set up per-CPU GS base for BSP BEFORE interrupts are enabled.
	// The ISR prologue uses %gs:4 for the per-CPU interrupt depth counter.
	percpuInitBSPEarly()

	// Initialize PIT channel 0 at ~100 Hz and register the timer IRQ handler.
	pitInit()
	registerHandler(32, handleTimer)
	vgaWriteLine(3, "PIT: 100 Hz timer started")
	serialPrintln("PIT: 100 Hz timer started")

	// Initialize keyboard translation tables and register IRQ1 handler (vector 33).
	keyboardInit()
	registerHandler(33, handleKeyboard)
	vgaWriteLine(4, "Keyboard: ready")
	serialPrintln("Keyboard: ready")

	// Enable maskable interrupts.
	sti()
	vgaWriteLine(5, "Interrupts: enabled")
	serialPrintln("Interrupts: enabled")

	// Start the afterTicks timer-wheel dispatcher. Must be after
	// pitInit (dispatcher reads pitTicks) and before any caller of
	// afterTicks. M4.2.f: dispatcher is now a kthread; afterTicksInit
	// calls kschedInit defensively (idempotent).
	afterTicksInit()
	serialPrintln("Timer wheel: afterTicksInit")

	// Phase 1: Allocate many objects that immediately become garbage.
	const numAllocs = 500
	for i := 0; i < numAllocs; i++ {
		_ = allocateGarbage()
	}

	// Read stats before GC.
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	vgaWriteLine(6, "Mallocs: "+utoa(before.Mallocs)+"  TotalAlloc: "+utoa(before.TotalAlloc))
	serialPrintln("Mallocs: " + utoa(before.Mallocs) + "  TotalAlloc: " + utoa(before.TotalAlloc))

	// Phase 2: Trigger garbage collection.
	runtime.GC()

	// Read stats after GC.
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	vgaWriteLine(7, "GC done. Frees: "+utoa(after.Frees)+"  HeapInuse: "+utoa(after.HeapInuse))
	serialPrintln("GC done. Frees: " + utoa(after.Frees) + "  HeapInuse: " + utoa(after.HeapInuse))

	// Phase 3: Allocate again to prove memory was reclaimed.
	for i := 0; i < 100; i++ {
		_ = allocateGarbage()
	}
	vgaWriteLine(8, "Post-GC alloc OK - GC works!")
	serialPrintln("Post-GC alloc OK - GC works!")

	// Virtual memory demo: map a 4 KiB page, write, read back, unmap.
	vmInit()

	// Allocate the Ring-3 kernel-stack pool (item 9). Each Ring-3
	// process gets one slot via ring3Wrapper; the slot returns to
	// the pool on processExit. Bounds the per-exec heap leak.
	ring3StackPoolInit()

	// Capture the boot PML4 phys addr. processExit (per-process
	// PML4 path) writes CR3 back to this before freeing a child's
	// PML4. See impldoc/shell_io_multiprocess.md §3.
	captureBootPML4()
	testVaddr := uintptr(0x40000000) // 1 GiB — outside the boot-time identity map
	testPaddr := allocPage()         // allocate a physical page from free memory
	mapPage(testVaddr, testPaddr, pagePresent|pageWrite)

	// Write a test value to the mapped virtual page.
	testPtr := (*uint64)(unsafe.Pointer(testVaddr))
	*testPtr = 0xDEADBEEF

	// Read back and verify.
	testVal := *testPtr

	// Unmap the page and flush TLB.
	unmapPage(testVaddr)

	if testVal == 0xDEADBEEF {
		vgaWriteLine(9, "VM: map/unmap OK")
		serialPrintln("VM: map/unmap OK")
	} else {
		vgaWriteLine(9, "VM: FAIL - read back 0x"+hextoa(testVal))
		serialPrintln("VM: FAIL - read back 0x" + hextoa(testVal))
	}

	// Free-list allocator test: allocate, free, allocate again — same address expected.
	flTestPage := allocPage()
	serialPrintln("FreeList: alloc1=0x" + hextoa(uint64(flTestPage)))
	freePage(flTestPage)
	flTestPage2 := allocPage()
	serialPrintln("FreeList: alloc2=0x" + hextoa(uint64(flTestPage2)))
	if flTestPage == flTestPage2 {
		serialPrintln("FreeList: OK — same address returned after free")
	} else {
		serialPrintln("FreeList: FAIL — expected same address")
	}

	// ELF64 parser test: construct a synthetic ELF64 binary with one PT_LOAD segment.
	serialPrintln("ELF: testing parser")
	var elfTest [120]byte // 64-byte header + 56-byte program header
	// e_ident: magic + class + data + version
	elfTest[0] = 0x7f
	elfTest[1] = 'E'
	elfTest[2] = 'L'
	elfTest[3] = 'F'
	elfTest[4] = 2 // ELFCLASS64
	elfTest[5] = 1 // ELFDATA2LSB
	elfTest[6] = 1 // EV_CURRENT
	// e_type = ET_EXEC (2) at offset 16
	elfTest[16] = 2
	// e_machine = EM_X86_64 (0x3E) at offset 18
	elfTest[18] = 0x3E
	// e_version = 1 at offset 20
	elfTest[20] = 1
	// e_entry = 0x401000 at offset 24 (little-endian)
	elfTest[24] = 0x00
	elfTest[25] = 0x10
	elfTest[26] = 0x40
	// e_phoff = 64 at offset 32
	elfTest[32] = 64
	// e_phentsize = 56 at offset 54
	elfTest[54] = 56
	// e_phnum = 1 at offset 56
	elfTest[56] = 1
	// Program header at offset 64: PT_LOAD segment.
	// p_type = PT_LOAD (1)
	elfTest[64] = 1
	// p_flags = PF_R|PF_X (5) at +4
	elfTest[68] = 5
	// p_vaddr = 0x400000 at +16
	elfTest[80] = 0x00
	elfTest[81] = 0x00
	elfTest[82] = 0x40
	// p_paddr = 0x400000 at +24
	elfTest[88] = 0x00
	elfTest[89] = 0x00
	elfTest[90] = 0x40
	// p_filesz = 0x1000 at +32
	elfTest[96] = 0x00
	elfTest[97] = 0x10
	// p_memsz = 0x2000 at +40
	elfTest[104] = 0x00
	elfTest[105] = 0x20
	// p_align = 0x1000 at +48
	elfTest[112] = 0x00
	elfTest[113] = 0x10

	elfEntry, elfPhdrs, elfOk := elfParse(elfTest[:])
	if elfOk {
		serialPrintln("ELF: parse OK, entry=0x" + hextoa(uint64(elfEntry)))
		serialPrintln("ELF: " + utoa(uint64(len(elfPhdrs))) + " PT_LOAD segment(s)")
		if len(elfPhdrs) > 0 {
			serialPrintln("ELF: phdr[0] vaddr=0x" + hextoa(uint64(elfPhdrs[0].Vaddr)) +
				" filesz=0x" + hextoa(elfPhdrs[0].Filesz) +
				" memsz=0x" + hextoa(elfPhdrs[0].Memsz))
		}
		if elfEntry == 0x401000 {
			serialPrintln("ELF: entry point PASS")
		} else {
			serialPrintln("ELF: entry point FAIL — got 0x" + hextoa(uint64(elfEntry)))
		}
	} else {
		serialPrintln("ELF: parse FAIL")
	}

	// In-memory filesystem demo (direct calls, before scheduler starts).
	// The channel-based FS demo runs after the FS task is spawned below.
	serialPrintln("FS: starting direct demo")
	fsCreate("hello.txt")
	fsWrite("hello.txt", []byte("hello world"))
	readBack := fsRead("hello.txt")
	if string(readBack) == "hello world" {
		vgaWriteLine(10, "FS: direct create/write/read OK")
		serialPrintln("FS: direct create/write/read OK")
	} else {
		vgaWriteLine(10, "FS: FAIL - read mismatch")
		serialPrintln("FS: FAIL - read mismatch")
	}

	// Spin-wait to let the timer accumulate ticks, then display count.
	for pitTicks < 200 {
		hlt()
	}
	tickStr := utoa(pitTicks)
	vgaWriteLine(11, "Timer: "+tickStr+" ticks")
	serialPrintln("Timer: " + tickStr + " ticks")

	// (Route C M4.2.a: removed the Spike2 chan probe and the
	// afterTicks self-test that used to live here. Both were
	// boot-time TinyGo goroutine probes — the first existed only
	// to prove scheduler=tasks + the runtime patch link, which
	// is now established by the entire kernel; the second was
	// observable-only ("afterTicks: OK") and not consumed by any
	// harness. Deleting them removes 2 of the 12 `go` sites that
	// blocked the M5 scheduler=none flip; deletion also resolves
	// the M4.1 smoke-test boot panic that surfaced in the
	// internal/task.PauseLocked path the chan probe walks.)

	// (Route C M0: the Phase 4.3 kernelThreadInit / ktPool machinery
	// has been superseded by the gooos-owned kthread scheduler in
	// src/kthread_*.go; it was dormant after commit 6a45e74 removed
	// the netRxLoop spawn, so nothing to initialise here.)

	// Boot Application Processors via INIT-SIPI-SIPI.
	// smpInit maps the LAPIC MMIO page, so per-CPU init must follow.
	smpInit()

	// Fill in BSP's APIC ID (requires LAPIC MMIO mapped by smpInit).
	percpuInitBSPLate()

	// Set up new GDT with Ring 3 code/data segments and TSS.
	// gdtInit also calls gdtInitPerCPU(0) for the BSP.
	gdtInit()
	gdtReady = 1 // Signal APs that gdtTable template is populated
	vgaWriteLine(12, "GDT: Ring 3 + TSS loaded")
	serialPrintln("GDT: Ring 3 + TSS loaded")

	// Register IPI handlers before enabling any preempt broadcast
	// source. If vector 0xFB fires before registration, the generic
	// dispatcher does not send LAPIC EOI and interrupt delivery can
	// stall after the first tick.
	registerHandler(ipiWakeupVector, handleWakeupIPI)
	serialPrintln("IPI: wakeup handler registered at vector 0xFC")
	registerHandler(ipiPreemptVector, handlePreemptIPI)
	serialPrintln("IPI: preempt handler registered at vector 0xFB")

	// Calibrate the LAPIC timer using PIT as reference, then start
	// the BSP's LAPIC timer at 100 Hz and register the handler.
	registerHandler(lapicTimerVector, handleLAPICTimer)
	lapicTimerCalibrate()
	lapicTimerInit()
	serialPrintln("LAPIC timer: BSP initialized at 100 Hz")

	// PCI scan + e1000 NIC init (Phases 1-4 of the networking stack).
	pciInit()
	if e1000Found {
		e1000Init() // leaves IMS masked — we unmask after the handler.
		e1000Vector := int(32 + e1000PCI.IRQLine)
		registerHandler(e1000Vector, handleE1000IRQ)
		serialPrintln("e1000: IRQ handler registered at vector " + utoa(uint64(e1000Vector)))
		e1000EnableInterrupts() // safe now that the handler is live.
		serialPrintln("e1000: NIC initialized")
		netInit()
		testNetBuf()
		testICMPEchoReply()
		// M4.2.g: was an inline `go func()`; now a netDiagLoop
		// kthread spawned later from main() (after kschedInit
		// + fsTask pin).
	}

	// IOAPIC initialization disabled: QEMU's IOAPIC IRQ0
	// redirection does not deliver PIT timer interrupts correctly
	// when switching from PIC pass-through, causing afterTicks and
	// sys_sleep to hang. PIC pass-through via LINT0 (ExtINT)
	// continues to work. IOAPIC support deferred until the IRQ
	// routing issue is resolved.
	//
	// ioapicInit()
	serialPrintln("IOAPIC: disabled (PIC pass-through active)")

	// M4.2.g cleanup: checkTaskOffset (TinyGo Task layout self-test)
	// removed — required `go func(){}()` and was the last `go `
	// site in src/*.go. The Task layout is no longer load-bearing
	// post-Route-C since gInfoByTask is dead-on-the-kthread-side.
	// M5 cleanup will delete the goroutine_tss.go support code.

	// Route C M0 self-test: verify the KernelThread struct layout
	// assumed by src/kthread_switch.S.
	checkKernelThreadOffset()

	// Route C M0 smoke test. Briefly enters the gooos kernel-thread
	// scheduler to prove kschedSwitch works, then returns to the
	// normal TinyGo boot path. Gated off by default; enabled by
	// scripts/test_kthread_smoke.sh via sed + rebuild.
	if runKthreadSmoke {
		kschedSmokeRun()
	}

	// Route C M2: fsTask runs as a gooos kernel thread. Callers
	// (fsSend*) now push onto a bounded fsReqQ and park on a
	// per-request KEvent instead of a chan *fsResponse.
	kschedInit()
	// Pin fsTask to CPU 0 so the BSP elf.go pump can dispatch it
	// directly via local kschedPop. Without this, after M4.2.{c,d}
	// added tcpRTOScanner + tcpEcho kthreads ahead of fsTask in the
	// round-robin counter, fsTask would land on an AP and the BSP
	// pump would hang at fsSendRead until the AP's idle hook fired.
	kschedSpawnAt("fsTask", fsTask, 0)
	// Route C M4.2.{b,e}: spawn net-service kthreads (netRxLoop,
	// udpEchoServer) here so they're after the smoke test and
	// before bspBootDone.
	if !runMinimalKthreads {
		netSpawnServices()
		if e1000Found {
			// M4.2.g: periodic netDiag, was inline `go func()`.
			// §14 U4: BSP-pinned (covered by kschedSpawnAt clamp,
			// but the explicit form documents intent).
			kschedSpawnAt("netDiagLoop", netDiagLoop, 0)
		}
	}
	runtime.Gosched()

	vgaWriteLine(13, "Services: fsTask running")
	serialPrintln("Services: fsTask running")

	// Run boot-time stack-size audit if enabled (compile-time
	// const). Service goroutines have parked at least once after
	// the runtime.Gosched above.
	stackSizeAudit()

	// Store user ELF binaries in the filesystem (direct calls, before
	// scheduler starts so FS task is not needed yet).
	serialPrintln("Storing user ELF binaries in filesystem...")
	fsCreate("sh.elf")
	fsWrite("sh.elf", userElf_sh[:])
	serialPrintln("  sh.elf: " + utoa(uint64(len(userElf_sh))) + " bytes")

	fsCreate("hello.elf")
	fsWrite("hello.elf", userElf_hello[:])
	serialPrintln("  hello.elf: " + utoa(uint64(len(userElf_hello))) + " bytes")

	fsCreate("ls.elf")
	fsWrite("ls.elf", userElf_ls[:])
	serialPrintln("  ls.elf: " + utoa(uint64(len(userElf_ls))) + " bytes")

	fsCreate("cat.elf")
	fsWrite("cat.elf", userElf_cat[:])
	serialPrintln("  cat.elf: " + utoa(uint64(len(userElf_cat))) + " bytes")

	fsCreate("wc.elf")
	fsWrite("wc.elf", userElf_wc[:])
	serialPrintln("  wc.elf: " + utoa(uint64(len(userElf_wc))) + " bytes")

	fsCreate("goprobe.elf")
	fsWrite("goprobe.elf", userElf_goprobe[:])
	serialPrintln("  goprobe.elf: " + utoa(uint64(len(userElf_goprobe))) + " bytes")

	fsCreate("gochan.elf")
	fsWrite("gochan.elf", userElf_gochan[:])
	serialPrintln("  gochan.elf: " + utoa(uint64(len(userElf_gochan))) + " bytes")

	fsCreate("fdprobe.elf")
	fsWrite("fdprobe.elf", userElf_fdprobe[:])
	serialPrintln("  fdprobe.elf: " + utoa(uint64(len(userElf_fdprobe))) + " bytes")

	fsCreate("tinyc.elf")
	fsWrite("tinyc.elf", userElf_tinyc[:])
	serialPrintln("  tinyc.elf: " + utoa(uint64(len(userElf_tinyc))) + " bytes")

	fsCreate("edit.elf")
	fsWrite("edit.elf", userElf_edit[:])
	serialPrintln("  edit.elf: " + utoa(uint64(len(userElf_edit))) + " bytes")

	fsCreate("smpprobe.elf")
	fsWrite("smpprobe.elf", userElf_smpprobe[:])
	serialPrintln("  smpprobe.elf: " + utoa(uint64(len(userElf_smpprobe))) + " bytes")

	fsCreate("udpecho.elf")
	fsWrite("udpecho.elf", userElf_udpecho[:])
	serialPrintln("  udpecho.elf: " + utoa(uint64(len(userElf_udpecho))) + " bytes")

	fsCreate("dhcp.elf")
	fsWrite("dhcp.elf", userElf_dhcp[:])
	serialPrintln("  dhcp.elf: " + utoa(uint64(len(userElf_dhcp))) + " bytes")

	fsCreate("tcpecho.elf")
	fsWrite("tcpecho.elf", userElf_tcpecho[:])
	serialPrintln("  tcpecho.elf: " + utoa(uint64(len(userElf_tcpecho))) + " bytes")

	fsCreate("tcpcli.elf")
	fsWrite("tcpcli.elf", userElf_tcpcli[:])
	serialPrintln("  tcpcli.elf: " + utoa(uint64(len(userElf_tcpcli))) + " bytes")

	fsCreate("ps.elf")
	fsWrite("ps.elf", userElf_ps[:])
	serialPrintln("  ps.elf: " + utoa(uint64(len(userElf_ps))) + " bytes")

	fsCreate("cpuhog.elf")
	fsWrite("cpuhog.elf", userElf_cpuhog[:])
	serialPrintln("  cpuhog.elf: " + utoa(uint64(len(userElf_cpuhog))) + " bytes")

	fsCreate("markerprint.elf")
	fsWrite("markerprint.elf", userElf_markerprint[:])
	serialPrintln("  markerprint.elf: " + utoa(uint64(len(userElf_markerprint))) + " bytes")

	fsCreate("userpreempt.elf")
	fsWrite("userpreempt.elf", userElf_userpreempt[:])
	serialPrintln("  userpreempt.elf: " + utoa(uint64(len(userElf_userpreempt))) + " bytes")

	fsCreate("sleeptest.elf")
	fsWrite("sleeptest.elf", userElf_sleeptest[:])
	serialPrintln("  sleeptest.elf: " + utoa(uint64(len(userElf_sleeptest))) + " bytes")

	fsCreate("yieldtest.elf")
	fsWrite("yieldtest.elf", userElf_yieldtest[:])
	serialPrintln("  yieldtest.elf: " + utoa(uint64(len(userElf_yieldtest))) + " bytes")

	// Store a test file for cat/wc demos.
	fsCreate("hello.txt")
	fsWrite("hello.txt", []byte("Hello from the gooos filesystem!\nThis is a test file.\n"))

	// Tiny C test fixtures for tinyc interpreter verification.
	fsCreate("sum.tc")
	fsWrite("sum.tc", []byte("main()\n{\n    var i, s;\n    s = 0;\n    i = 0;\n    while (i < 10) {\n        s = s + i;\n        i = i + 1;\n    }\n    println(\"s = %d\", s);\n}\n"))

	fsCreate("fib.tc")
	fsWrite("fib.tc", []byte("fib(n)\n{\n    if (n <= 1) return n;\n    return fib(n - 1) + fib(n - 2);\n}\n\nmain()\n{\n    println(\"%d\", fib(10));\n}\n"))

	fsCreate("array.tc")
	fsWrite("array.tc", []byte("var A[10];\n\narraySum(a, n)\n{\n    var i, s;\n    s = 0;\n    for (i = 0; i < n; i = i + 1) s = s + a[i];\n    return s;\n}\n\nmain()\n{\n    var i;\n    for (i = 0; i < 10; i = i + 1) A[i] = i;\n    println(\"s = %d\", arraySum(A, 10));\n}\n"))

	fsCreate("for.tc")
	fsWrite("for.tc", []byte("main()\n{\n    var i, sum;\n    sum = 0;\n    for (i = 1; i <= 10; i = i + 1) {\n        sum = sum + i;\n    }\n    println(\"sum = %d\", sum);\n}\n"))

	if runSMPProbeShellTest {
		// One-shot shell autorun path for deterministic SMP shell probes.
		// Executed by user/cmd/sh/main.go before the interactive prompt.
		fsCreate(".autorun.sh")
		fsWrite(".autorun.sh", []byte("smpprobe\necho POST_SMPPROBE_OK\n"))
		bootShellArgs = "--autorun"
		serialPrintln("preempt_probe: prepared .autorun.sh for smpprobe shell test")
	} else if runGoprobeTest {
		// One-shot shell autorun path for deterministic goprobe userspace tests.
		// Executed by user/cmd/sh/main.go before the interactive prompt.
		fsCreate(".autorun.sh")
		fsWrite(".autorun.sh", []byte("goprobe\necho POST_GOPROBE_OK\n"))
		bootShellArgs = "--autorun"
		serialPrintln("preempt_probe: prepared .autorun.sh for goprobe shell test")
	} else if runSleeputestTest {
		// One-shot shell autorun path for deterministic sleeptest validation.
		// Executed by user/cmd/sh/main.go before the interactive prompt.
		fsCreate(".autorun.sh")
		fsWrite(".autorun.sh", []byte("sleeptest\necho POST_SLEEPTEST_OK\n"))
		bootShellArgs = "--autorun"
		serialPrintln("preempt_probe: prepared .autorun.sh for sleeptest shell test")
	} else if runYieldtestTest {
		// One-shot shell autorun path for deterministic yieldtest validation.
		// Executed by user/cmd/sh/main.go before the interactive prompt.
		fsCreate(".autorun.sh")
		fsWrite(".autorun.sh", []byte("yieldtest\necho POST_YIELDTEST_OK\n"))
		bootShellArgs = "--autorun"
		serialPrintln("preempt_probe: prepared .autorun.sh for yieldtest shell test")
	} else if preemptEnabled && runUserPreemptProbe {
		// Feature 2.2 harness auto-launch via shell autorun path to keep
		// user process startup deterministic without HMP sendkey.
		fsCreate(".autorun.sh")
		fsWrite(".autorun.sh", []byte("userpreempt\n"))
		bootShellArgs = "--autorun"
		serialPrintln("preempt_probe: prepared .autorun.sh for userpreempt test")
	} else {
		bootShellArgs = ""
	}

	vgaWriteLine(14, "Scheduler: TinyGo goroutines active")
	serialPrintln("Scheduler: TinyGo goroutines active")

	// Load shell and jump to Ring 3. Does not return.
	setupUserspace()
}

var bootPostShellReadyDone uint32

func bootActivatePostShellReady() {
	if bootPostShellReadyDone != 0 {
		return
	}
	bootPostShellReadyDone = 1

	if !ioapicActive {
		restoreBSPVirtualWire()
	}

	bspBootDone = 1

	if runSMPBasicProbe {
		// M4.2.g: was `go smpBasicProbe()`. Pin to AP 1 (not BSP)
		// so the test_smp_basic harness can observe non-zero cpuIDs
		// (proves a kthread runs on an AP, not just BSP).
		//
		// §14 U4: under uniprocessorKernel, no kthread runs on AP.
		// kschedSpawnAt's flag clamp routes the spawn to BSP. The
		// test_smp_basic harness is updated to SKIP under M6 per
		// §14 §6.2 (re-purposed for Ring-3 distribution under M7).
		var apTarget uint32 = 1
		if numCoresOnline <= 1 {
			apTarget = 0
		}
		kschedSpawnAt("smpBasicProbe", smpBasicProbe, apTarget)
	}

	if preemptEnabled && runSMPShellPreemptProbe {
		serialPrintln("preempt_probe: waiting for AP launcher for cpuhog+markerprint")
		n := uint32(numCoresOnline)
		if n == 0 {
			n = 1
		}
		for i := uint32(0); i < n; i++ {
			serialPrintln("preempt_probe: apicid cpu=" + utoa(uint64(i)) +
				" id=" + utoa(uint64(perCPUBlocks[i].APICID)))
		}
	}

	if preemptEnabled && runPreemptProbe {
		serialPrintln("preempt_probe: spawning kpMarker + kpHog")
		// M4.2.g: kpMarker + kpHog migrated to gooos kernel
		// threads. The M1 attempt's "no banner" mystery is
		// expected to be resolved by the M4.0 + M4.1 stack
		// (kthread ring3Wrapper, gcLock spinlock, sysYield
		// kthread fork). Each runs to preempt-IPI rescheduling.
		// §14 U4: BSP-pinned (covered by kschedSpawn flag clamp,
		// but the explicit form documents intent).
		kschedSpawnAt("kpMarker", kpMarker, 0)
		kschedSpawnAt("kpHog", kpHog, 0)
	}

	preemptPhaseAdvance(preemptPhaseSchedReady)
}

// netDiagLoop is the periodic netDiag dumper kthread. M4.2.g:
// was an inline `go func()` in the e1000 init block. First dump
// at ~5 s after spawn (for test scripts that grep "=== Network
// Diagnostics ==="), then every ~10 s thereafter.
func netDiagLoop() {
	if kschedRunning[cpuID()] != nil {
		kschedTimedPark(500)
	} else {
		<-afterTicks(500)
	}
	netDiag()
	for {
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(1000)
		} else {
			<-afterTicks(1000)
		}
		netDiag()
	}
}

// kpHog is a tight compute loop with zero cooperative-yield points.
// Under preemptEnabled, AP preempt IPIs must force it off the CPU
// periodically so kpMarker can run on the same core.
//
//go:noinline
func kpHog() {
	serialPrintln("kpHog: started on cpu=" + utoa(uint64(cpuID())))
	var x uint64
	for {
		x++
		if x == 0 {
			serialPrintln("kpHog: wrapped (should never print)")
		}
	}
}

// kpMarker prints a marker line every ~50 ms. Under preemption, it
// makes forward progress even while kpHog is hogging a core. Without
// preemption, and if kpMarker and kpHog happen to land on the same
// runqueue, kpMarker starves.
func kpMarker() {
	serialPrintln("kpMarker: started on cpu=" + utoa(uint64(cpuID())))
	for iter := 0; iter < 20; iter++ {
		serialPrintln("preempt_probe_marker=" + utoa(uint64(iter)) +
			" cpu=" + utoa(uint64(cpuID())))
		// M4.2.g: kthread context — kschedTimedPark.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(5)
		} else {
			<-afterTicks(5)
		}
	}
	serialPrintln("kpMarker: done")
}

// smpBasicProbe yields between iterations; each yield re-queues
// the goroutine and lets an AP have a chance to steal it. The
// goroutine reports its current CPU once per tick; printing the
// same marker with N != 0 is the success signal.
func smpBasicProbe() {
	for iter := 0; iter < 50; iter++ {
		c := cpuID()
		if preemptEnabled && runSMPShellPreemptProbe && smpShellProbeLaunched == 0 {
			if c != 0 || iter >= 5 {
				smpShellProbeLaunched = 1
				if c == 0 {
					serialPrintln("preempt_probe: AP launcher timeout in smpBasicProbe, fallback cpu=0")
				}
				serialPrintln("preempt_probe: launching cpuhog+markerprint from cpu=" +
					utoa(uint64(c)))
				_, _ = elfSpawn("markerprint.elf", "", nil)
				_, _ = elfSpawn("cpuhog.elf", "", nil)
			}
		}
		out := "smp_basic_cpu=" + utoa(uint64(c)) + " iter=" + utoa(uint64(iter))
		serialPrintln(out)
		// M4.2.g: kthread context — kschedTimedPark.
		if kschedRunning[cpuID()] != nil {
			kschedTimedPark(1)
		} else {
			<-afterTicks(1)
		}
	}
}
