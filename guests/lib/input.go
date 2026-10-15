package kern

// ---- input records (ABI §4) — shared by wasm guests and the host bus ----

// Input event kinds / modifier bits.
const (
	KeyDown uint8 = 1
	KeyUp   uint8 = 2

	ModShift uint8 = 1 << 0
	ModCtrl  uint8 = 1 << 1
	ModAlt   uint8 = 1 << 2
)

// Record sizes. The DEPLOYED kernel emits the 4-byte v1 record
// {kind, mods, codepoint}; ratified ABI v1.3 extends it to 6 bytes
// {kind, mods, scan, codepoint} (u16 raw i8042 scancode). DecodeInputEvent
// accepts BOTH, so guests built on this lib survive the kernel's v1.3
// transition without recompilation (guest-ABI stability rule). Encode
// stays at the deployed 4-byte form — guests never emit input records;
// the encoder exists for host-bus test injection only.
const (
	InputRecLen    = 4 // deployed v1 record
	InputRecLenV13 = 6 // ratified v1.3 record (adds u16 scan)
)

// InputEvent is a decoded §4 record.
type InputEvent struct {
	Kind      uint8
	Mods      uint8
	Scan      uint16 // v1.3: raw scancode (0 when decoded from v1)
	Codepoint uint16
}

// encode renders the wire form at the deployed record size.
func (e InputEvent) Encode() []byte {
	b := make([]byte, InputRecLen)
	b[0] = e.Kind
	b[1] = e.Mods
	Put16(b[2:], e.Codepoint)
	return b
}

// DecodeInputEvent parses one wire record — v1.3 (6 B, with scan) or the
// deployed 4-byte form (Scan reports 0).
func DecodeInputEvent(b []byte) (InputEvent, bool) {
	switch {
	case len(b) >= InputRecLenV13:
		return InputEvent{Kind: b[0], Mods: b[1],
			Scan:      Get16(b[2:4]),
			Codepoint: Get16(b[4:6]),
		}, true
	case len(b) >= InputRecLen:
		return InputEvent{Kind: b[0], Mods: b[1],
			Codepoint: Get16(b[2:4]),
		}, true
	default:
		return InputEvent{}, false
	}
}

// RecvBufLen is the safe receive-buffer size for one record under any
// ratified layout (callers should allocate this, not InputRecLen).
const RecvBufLen = InputRecLenV13

// PollInput drains at most one §4 record through k (any Kernel impl).
func PollInput(k Kernel) (InputEvent, bool) {
	var buf [RecvBufLen]byte
	n := k.InputRecv(buf[:])
	if n < InputRecLen {
		return InputEvent{}, false
	}
	ev, _ := DecodeInputEvent(buf[:n])
	return ev, true
}
