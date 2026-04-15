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

// vgaWriteLine writes a string to the given row of the VGA text buffer.
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
func handleDivisionError(vector uint64) {
	vgaWriteLine(7, "Exception: #DE")
	serialPrintln("Exception: #DE (Division Error)")
}

// handleDefaultIRQ handles any hardware IRQ (vectors 32-47) that does
// not have a specific handler registered. Sends EOI so the PIC is not
// left stuck.
func handleDefaultIRQ(vector uint64) {
	irq := uint8(vector - 32)
	picSendEOI(irq)
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

	// Initialize PIT channel 0 at ~100 Hz and register the timer IRQ handler.
	pitInit()
	registerHandler(32, handleTimer)
	vgaWriteLine(3, "PIT: 100 Hz timer started")
	serialPrintln("PIT: 100 Hz timer started")

	// Initialize keyboard channel and register IRQ1 handler (vector 33).
	keyboardInit()
	registerHandler(33, handleKeyboard)
	vgaWriteLine(4, "Keyboard: ready")
	serialPrintln("Keyboard: ready")

	// Enable maskable interrupts.
	sti()
	vgaWriteLine(5, "Interrupts: enabled")
	serialPrintln("Interrupts: enabled")

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

	// Spike 2 probe — trivial TinyGo goroutine + channel round-trip.
	// Proves scheduler=tasks + gooos runtime patch links and runs.
	// Removed once the full migration lands.
	{
		ch := make(chan int, 1)
		go func() { ch <- 42 }()
		v := <-ch
		if v == 42 {
			serialPrintln("Spike2: goroutine+chan OK")
		} else {
			serialPrintln("Spike2: FAIL")
		}
	}

	// Boot Application Processors via INIT-SIPI-SIPI.
	smpInit()

	// Set up new GDT with Ring 3 code/data segments and TSS.
	gdtInit()
	vgaWriteLine(12, "GDT: Ring 3 + TSS loaded")
	serialPrintln("GDT: Ring 3 + TSS loaded")

	go fsTask()
	go keyboardPump()
	runtime.Gosched()

	vgaWriteLine(13, "Services: fsTask + keyboardPump running")
	serialPrintln("Services: fsTask + keyboardPump running")

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

	// Store a test file for cat/wc demos.
	fsCreate("hello.txt")
	fsWrite("hello.txt", []byte("Hello from the gooos filesystem!\nThis is a test file.\n"))

	vgaWriteLine(14, "Scheduler: TinyGo goroutines active")
	serialPrintln("Scheduler: TinyGo goroutines active")

	// Load shell and jump to Ring 3. Does not return.
	setupUserspace()
}
