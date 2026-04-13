// src/fs.go -- In-memory filesystem with flat directory structure.
//
// Provides a minimal filesystem API: Create, Write, Read, and List
// for named files stored entirely in memory. Pure Go implementation
// with fixed-size entries and no assembly dependencies.

package main

// Maximum number of files and maximum data size per file.
const (
	maxFiles    = 16
	maxFileData = 4096
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
