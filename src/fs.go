// src/fs.go -- In-memory filesystem with flat directory structure.
//
// Provides a minimal filesystem API: Create, Write, Read, and List
// for named files stored entirely in memory. Pure Go implementation
// with fixed-size entries and no assembly dependencies.

package main

// Maximum number of files and maximum data size per file.
const (
	maxFiles    = 32
	maxFileData = 131072 // 128 KiB — one doubling ahead of the current peak (goprobe.elf at 89 KiB post-scheduler flip) per the size-audit policy in impldoc/userspace_verification.md §5. FS footprint: 32 × 128 KiB = 4 MiB of .bss (FileSystem lives in globals, not the kernel heap).
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

// fsRequest travels from any caller to the fsTask goroutine.
type fsRequest struct {
	op    fsOp
	name  string
	data  []byte
	reply chan *fsResponse
}

// fsResponse is sent back on req.reply.
type fsResponse struct {
	ok    bool
	data  []byte
	names []string
}

// fsReqCh serializes all FS access through the single fsTask goroutine.
var fsReqCh = make(chan *fsRequest, 8)

// fsTask is the FS service goroutine. Spawned from main() via `go`.
func fsTask() {
	fsTaskHandle = taskCurrent()
	for req := range fsReqCh {
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
		req.reply <- resp
	}
}

func fsSendCreate(name string) bool {
	reply := make(chan *fsResponse, 1)
	fsReqCh <- &fsRequest{op: fsOpCreate, name: name, reply: reply}
	return (<-reply).ok
}

func fsSendWrite(name string, data []byte) bool {
	reply := make(chan *fsResponse, 1)
	fsReqCh <- &fsRequest{op: fsOpWrite, name: name, data: data, reply: reply}
	return (<-reply).ok
}

func fsSendRead(name string) []byte {
	reply := make(chan *fsResponse, 1)
	fsReqCh <- &fsRequest{op: fsOpRead, name: name, reply: reply}
	return (<-reply).data
}

func fsSendList() []string {
	reply := make(chan *fsResponse, 1)
	fsReqCh <- &fsRequest{op: fsOpList, reply: reply}
	return (<-reply).names
}

func fsSendDelete(name string) bool {
	reply := make(chan *fsResponse, 1)
	fsReqCh <- &fsRequest{op: fsOpDelete, name: name, reply: reply}
	return (<-reply).ok
}
