// src/netutil.go -- Network byte-order and address-formatting helpers.
//
// Every on-wire multi-byte field uses network byte order (big-endian).
// The kernel code does its own byte-level serialization (buf[i] = ...),
// so these helpers are mostly used for casting uint32/uint16 values
// that cross the manual-packing boundary (e.g. statistics, checksum
// scratch buffers).
//
// IPv4 addresses are stored in uint32 with the MSB being the first
// octet on the wire: `10.0.2.15` → `0x0A00020F`. Serialization extracts
// the bytes via `>>24 / >>16 / >>8 / &0xFF`, matching network order
// without a runtime htonl call.

package main

//go:nosplit
func htons(v uint16) uint16 {
	return (v<<8)&0xFF00 | (v>>8)&0x00FF
}

//go:nosplit
func ntohs(v uint16) uint16 { return htons(v) }

//go:nosplit
func htonl(v uint32) uint32 {
	return (v<<24)&0xFF000000 |
		(v<<8)&0x00FF0000 |
		(v>>8)&0x0000FF00 |
		(v>>24)&0x000000FF
}

//go:nosplit
func ntohl(v uint32) uint32 { return htonl(v) }

// macToString formats a 6-byte MAC as "XX:XX:XX:XX:XX:XX" (uppercase,
// colon-separated). Alloc-free via a fixed stack buffer + string
// conversion (TinyGo copies bytes into the resulting string header).
func macToString(mac [6]byte) string {
	const hex = "0123456789ABCDEF"
	var buf [17]byte
	j := 0
	for i := 0; i < 6; i++ {
		if i > 0 {
			buf[j] = ':'
			j++
		}
		buf[j] = hex[mac[i]>>4]
		buf[j+1] = hex[mac[i]&0xF]
		j += 2
	}
	return string(buf[:17])
}

// ipToString formats an IPv4 address (uint32, MSB = first octet) as
// "A.B.C.D".
func ipToString(ip uint32) string {
	return utoa(uint64((ip>>24)&0xFF)) + "." +
		utoa(uint64((ip>>16)&0xFF)) + "." +
		utoa(uint64((ip>>8)&0xFF)) + "." +
		utoa(uint64(ip&0xFF))
}

// parseIPv4 converts a dotted-quad string to uint32. Returns 0 on any
// malformed input; callers must check for 0 if 0.0.0.0 is a valid
// sentinel in their context.
func parseIPv4(s string) uint32 {
	var parts [4]uint32
	idx := 0
	cur := uint32(0)
	seenDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if !seenDigit || idx >= 3 {
				return 0
			}
			parts[idx] = cur
			idx++
			cur = 0
			seenDigit = false
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		cur = cur*10 + uint32(c-'0')
		if cur > 255 {
			return 0
		}
		seenDigit = true
	}
	if !seenDigit || idx != 3 {
		return 0
	}
	parts[idx] = cur
	return parts[0]<<24 | parts[1]<<16 | parts[2]<<8 | parts[3]
}
