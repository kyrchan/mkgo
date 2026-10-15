package main

// Big-endian wire helpers. Network byte order is BIG-endian for every
// multi-byte field in the Ethernet/IP/ARP/UDP/TCP headers — distinct
// from the guest ABI's little-endian datagram convention (guests/lib).

func BePut16(b []byte, v uint16) {
	b[0] = byte(v >> 8)
	b[1] = byte(v)
}

func BePut32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func BeGet16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func BeGet32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
