// src/fs.go -- In-memory filesystem with flat directory structure.
//
// Provides a minimal filesystem API: Create, Write, Read, and List
// for named files stored entirely in memory. Pure Go implementation
// with fixed-size entries and no assembly dependencies.

package main

import "unsafe"

// Maximum number of files and maximum data size per file.
const (
	maxFiles    = 32
	maxFileData = 65536
)

// FileEntry represents a single file in the filesystem.
type FileEntry struct {
	name string
	data [maxFileData]byte
	size int
	used bool
}

// FileSystem holds the flat directory of file entries.
type FileSystem struct {
	files [maxFiles]FileEntry
}

// Global filesystem instance.
var fs FileSystem

// fsCreate creates a new empty file with the given name.
// Returns true on success, false if the directory is full or name already exists.
func fsCreate(name string) bool {
	// Check if file already exists.
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			return false
		}
	}
	// Find a free slot.
	for i := 0; i < maxFiles; i++ {
		if !fs.files[i].used {
			fs.files[i].used = true
			fs.files[i].name = name
			fs.files[i].size = 0
			return true
		}
	}
	return false
}

// fsWrite writes data to an existing file, replacing any previous content.
// Returns true on success, false if the file doesn't exist or data exceeds maxFileData.
func fsWrite(name string, data []byte) bool {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			if len(data) > maxFileData {
				return false
			}
			for j := 0; j < len(data); j++ {
				fs.files[i].data[j] = data[j]
			}
			fs.files[i].size = len(data)
			return true
		}
	}
	return false
}

// fsRead reads the contents of a named file.
// Returns a copy of the file data, or nil if the file doesn't exist.
func fsRead(name string) []byte {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			result := make([]byte, fs.files[i].size)
			for j := 0; j < fs.files[i].size; j++ {
				result[j] = fs.files[i].data[j]
			}
			return result
		}
	}
	return nil
}

// fsList returns the names of all files in the directory.
func fsList() []string {
	var names []string
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used {
			names = append(names, fs.files[i].name)
		}
	}
	return names
}

// fsDelete removes a file by name.
// Returns true if the file was found and deleted, false otherwise.
func fsDelete(name string) bool {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			fs.files[i].used = false
			fs.files[i].name = ""
			fs.files[i].size = 0
			return true
		}
	}
	return false
}

// ---------- Filesystem task (microkernel service) ----------

// FS operation codes.
const (
	fsOpCreate = 0
	fsOpWrite  = 1
	fsOpRead   = 2
	fsOpList   = 3
	fsOpDelete = 4
)

// FSRequest is sent to the filesystem task via fsRequestChannel.
type FSRequest struct {
	op      uint8
	name    string
	data    []byte
	replyCh *Channel
}

// FSResponse is sent back on the request's replyCh.
type FSResponse struct {
	ok    bool
	data  []byte
	names []string
}

// Pool size for FS requests and responses.
const fsPoolSize = 8

var (
	fsRequestChannel *Channel                    // channel for FS requests (capacity 8)
	fsReqPool        [fsPoolSize]FSRequest        // static pool for request structs
	fsReqPoolNext    int                          // next pool slot (ring)
	fsRespPool       [fsPoolSize]FSResponse       // static pool for response structs
	fsRespPoolNext   int                          // next pool slot (ring)
)

// fsTaskEntryAddr returns the address of fsTaskEntry. Implemented in switch.S.
//
//go:linkname fsTaskEntryAddr fsTaskEntryAddr
func fsTaskEntryAddr() uintptr

// fsTaskEntry loops receiving FSRequest pointers from fsRequestChannel,
// dispatches to the appropriate filesystem operation, and sends an FSResponse
// back on the request's replyCh. Runs as a dedicated kernel task.
//
//export fsTaskEntry
func fsTaskEntry() {
	sti()
	serialPrintln("FS task: started")
	for {
		val := chanRecv(fsRequestChannel)
		req := (*FSRequest)(unsafe.Pointer(val))

		// Allocate response from static pool.
		ri := fsRespPoolNext
		fsRespPoolNext = (fsRespPoolNext + 1) % fsPoolSize
		resp := &fsRespPool[ri]

		switch req.op {
		case fsOpCreate:
			resp.ok = fsCreate(req.name)
			resp.data = nil
			resp.names = nil
		case fsOpWrite:
			resp.ok = fsWrite(req.name, req.data)
			resp.data = nil
			resp.names = nil
		case fsOpRead:
			resp.data = fsRead(req.name)
			resp.ok = resp.data != nil
			resp.names = nil
		case fsOpList:
			resp.names = fsList()
			resp.ok = true
			resp.data = nil
		case fsOpDelete:
			resp.ok = fsDelete(req.name)
			resp.data = nil
			resp.names = nil
		default:
			resp.ok = false
			resp.data = nil
			resp.names = nil
		}

		chanSend(req.replyCh, uintptr(unsafe.Pointer(resp)))
	}
}

// fsSendCreate sends a create request to the FS task and blocks for the response.
func fsSendCreate(name string) bool {
	replyCh := chanCreate(1)
	ri := fsReqPoolNext
	fsReqPoolNext = (fsReqPoolNext + 1) % fsPoolSize
	fsReqPool[ri] = FSRequest{op: fsOpCreate, name: name, replyCh: replyCh}
	chanSend(fsRequestChannel, uintptr(unsafe.Pointer(&fsReqPool[ri])))
	val := chanRecv(replyCh)
	chanFree(replyCh)
	resp := (*FSResponse)(unsafe.Pointer(val))
	return resp.ok
}

// fsSendWrite sends a write request to the FS task and blocks for the response.
func fsSendWrite(name string, data []byte) bool {
	replyCh := chanCreate(1)
	ri := fsReqPoolNext
	fsReqPoolNext = (fsReqPoolNext + 1) % fsPoolSize
	fsReqPool[ri] = FSRequest{op: fsOpWrite, name: name, data: data, replyCh: replyCh}
	chanSend(fsRequestChannel, uintptr(unsafe.Pointer(&fsReqPool[ri])))
	val := chanRecv(replyCh)
	chanFree(replyCh)
	resp := (*FSResponse)(unsafe.Pointer(val))
	return resp.ok
}

// fsSendRead sends a read request to the FS task and blocks for the response.
func fsSendRead(name string) []byte {
	replyCh := chanCreate(1)
	ri := fsReqPoolNext
	fsReqPoolNext = (fsReqPoolNext + 1) % fsPoolSize
	fsReqPool[ri] = FSRequest{op: fsOpRead, name: name, replyCh: replyCh}
	chanSend(fsRequestChannel, uintptr(unsafe.Pointer(&fsReqPool[ri])))
	val := chanRecv(replyCh)
	chanFree(replyCh)
	resp := (*FSResponse)(unsafe.Pointer(val))
	return resp.data
}

// fsSendList sends a list request to the FS task and blocks for the response.
func fsSendList() []string {
	replyCh := chanCreate(1)
	ri := fsReqPoolNext
	fsReqPoolNext = (fsReqPoolNext + 1) % fsPoolSize
	fsReqPool[ri] = FSRequest{op: fsOpList, replyCh: replyCh}
	chanSend(fsRequestChannel, uintptr(unsafe.Pointer(&fsReqPool[ri])))
	val := chanRecv(replyCh)
	chanFree(replyCh)
	resp := (*FSResponse)(unsafe.Pointer(val))
	return resp.names
}

// fsSendDelete sends a delete request to the FS task and blocks for the response.
func fsSendDelete(name string) bool {
	replyCh := chanCreate(1)
	ri := fsReqPoolNext
	fsReqPoolNext = (fsReqPoolNext + 1) % fsPoolSize
	fsReqPool[ri] = FSRequest{op: fsOpDelete, name: name, replyCh: replyCh}
	chanSend(fsRequestChannel, uintptr(unsafe.Pointer(&fsReqPool[ri])))
	val := chanRecv(replyCh)
	chanFree(replyCh)
	resp := (*FSResponse)(unsafe.Pointer(val))
	return resp.ok
}

// fsDemoTaskAddr returns the address of fsDemoTask. Implemented in switch.S.
//
//go:linkname fsDemoTaskAddr fsDemoTaskAddr
func fsDemoTaskAddr() uintptr

// fsDemoTask demonstrates the channel-based filesystem API.
//
//export fsDemoTask
func fsDemoTask() {
	sti()
	serialPrintln("FS demo: starting channel-based demo")

	// Create a file via FS task.
	ok := fsSendCreate("chan.txt")
	serialPrintln("FS demo: fsSendCreate('chan.txt') = " + boolStr(ok))

	// Write data via FS task.
	ok = fsSendWrite("chan.txt", []byte("channel IPC"))
	serialPrintln("FS demo: fsSendWrite('chan.txt', 'channel IPC') = " + boolStr(ok))

	// Read back via FS task.
	data := fsSendRead("chan.txt")
	serialPrintln("FS demo: fsSendRead('chan.txt') = '" + string(data) + "'")

	// List files via FS task.
	names := fsSendList()
	listing := "FS demo: fsSendList ="
	for _, name := range names {
		listing += " " + name
	}
	serialPrintln(listing)

	if string(data) == "channel IPC" {
		serialPrintln("FS demo: channel-based FS PASS")
		vgaWriteLine(10, "FS: channel IPC OK | "+listing)
	} else {
		serialPrintln("FS demo: channel-based FS FAIL")
		vgaWriteLine(10, "FS: channel IPC FAIL")
	}

	for {
		taskSleep(10000)
	}
}

// boolStr returns "true" or "false" for a bool value.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
