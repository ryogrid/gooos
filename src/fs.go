// src/fs.go -- In-memory filesystem with flat directory structure.
//
// Provides a minimal filesystem API: Create, Write, Read, and List
// for named files stored entirely in memory. Pure Go implementation
// with fixed-size entries and no assembly dependencies.

package main

// Maximum number of files and maximum data size per file.
const (
	maxFiles    = 32
	maxFileData = 262144 // 256 KiB — doubled from 128 KiB to absorb the ~10–16 KiB conservative-GC overhead (metadata bitmap + mark/sweep code + synthetic Elf64 header) on top of tinyc.elf's 126 KiB baseline. FS footprint: 32 × 256 KiB = 8 MiB of .bss (FileSystem lives in globals, not the kernel heap). See impldoc/userspace_conservative_gc_verification.md §3.
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

// fsAppend extends an existing file by len(data) bytes. Used by
// fileFd.Write in fileModeWrite (after fsTruncate) and
// fileModeAppend. Returns the number of bytes written; 0 if the
// file does not exist or the append would overflow maxFileData.
func fsAppend(name string, data []byte) int {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			n := len(data)
			room := maxFileData - fs.files[i].size
			if n > room {
				n = room
			}
			for j := 0; j < n; j++ {
				fs.files[i].data[fs.files[i].size+j] = data[j]
			}
			fs.files[i].size += n
			return n
		}
	}
	return 0
}

// fsTruncate clears the contents of an existing file and returns
// true; returns false if the file does not exist. Creates the
// file if missing? No — caller (fileFd.Open in mode-write) calls
// fsCreate first.
func fsTruncate(name string) bool {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			fs.files[i].size = 0
			return true
		}
	}
	return false
}

// fsSize returns the current size of a file, or -1 if missing.
// Used by fileFd to position the offset for fileModeAppend.
func fsSize(name string) int {
	for i := 0; i < maxFiles; i++ {
		if fs.files[i].used && fs.files[i].name == name {
			return fs.files[i].size
		}
	}
	return -1
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

// ---------- Filesystem task (goroutine + native channel) ----------

// FS operation codes.
type fsOp uint8

const (
	fsOpCreate fsOp = iota + 1
	fsOpWrite
	fsOpRead
	fsOpList
	fsOpDelete
)

// fsRequest travels from any caller to the fsTask kernel thread.
//
// Route C M2: replaced the per-request `reply chan *fsResponse`
// with an embedded KEvent + owned `resp *fsResponse`. The caller
// allocates a req on its own stack (or heap), pushes it onto
// fsReqQ, Waits on req.ev, then reads req.resp. fsTask processes
// requests serially (single consumer), fills resp, and Signals.
type fsRequest struct {
	op   fsOp
	name string
	data []byte
	resp *fsResponse
	ev   KEvent
}

// fsResponse is produced by fsTask on behalf of a request.
type fsResponse struct {
	ok    bool
	data  []byte
	names []string
}

// fsReqQ serializes all FS access through the single fsTask
// kernel thread (§06 service #9). Replaces the Go-chan-based
// `fsReqCh` with a bounded MPSC queue (src/kthread_queue.go).
var fsReqQ fsReqQueue

// fsTask is the FS service kernel thread. Spawned from main() via
// `kschedSpawn("fsTask", fsTask)`. Blocks in fsReqQ.Pop(); processes
// one request at a time; signals the caller's per-request KEvent.
func fsTask() {
	fsTaskHandle = taskCurrent()
	for {
		req := fsReqQ.Pop()
		resp := &fsResponse{}
		switch req.op {
		case fsOpCreate:
			resp.ok = fsCreate(req.name)
		case fsOpWrite:
			resp.ok = fsWrite(req.name, req.data)
		case fsOpRead:
			resp.data = fsRead(req.name)
			resp.ok = resp.data != nil
		case fsOpList:
			resp.names = fsList()
			resp.ok = true
		case fsOpDelete:
			resp.ok = fsDelete(req.name)
		}
		req.resp = resp
		req.ev.Signal()
	}
}

func fsSendCreate(name string) bool {
	req := &fsRequest{op: fsOpCreate, name: name}
	fsReqQ.Push(req)
	req.ev.Wait()
	return req.resp.ok
}

func fsSendWrite(name string, data []byte) bool {
	req := &fsRequest{op: fsOpWrite, name: name, data: data}
	fsReqQ.Push(req)
	req.ev.Wait()
	return req.resp.ok
}

func fsSendRead(name string) []byte {
	req := &fsRequest{op: fsOpRead, name: name}
	fsReqQ.Push(req)
	req.ev.Wait()
	return req.resp.data
}

func fsSendList() []string {
	req := &fsRequest{op: fsOpList}
	fsReqQ.Push(req)
	req.ev.Wait()
	return req.resp.names
}

func fsSendDelete(name string) bool {
	req := &fsRequest{op: fsOpDelete, name: name}
	fsReqQ.Push(req)
	req.ev.Wait()
	return req.resp.ok
}
