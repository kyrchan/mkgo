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

// InputRecLen is one §4 record: u8 kind, u8 mods, u16 codepoint.
const InputRecLen = 4

// InputEvent is a decoded §4 record.
type InputEvent struct {
	Kind      uint8
	Mods      uint8
	Codepoint uint16
}

// Encode renders the wire form.
func (e InputEvent) Encode() []byte {
	b := make([]byte, InputRecLen)
	b[0] = e.Kind
	b[1] = e.Mods
	Put16(b[2:], e.Codepoint)
	return b
}

// DecodeInputEvent parses one wire record.
func DecodeInputEvent(b []byte) (InputEvent, bool) {
	if len(b) < InputRecLen {
		return InputEvent{}, false
	}
	return InputEvent{Kind: b[0], Mods: b[1], Codepoint: Get16(b[2:4])}, true
}

// PollInput drains at most one §4 record through k (any Kernel impl).
func PollInput(k Kernel) (InputEvent, bool) {
	var buf [InputRecLen]byte
	n := k.InputRecv(buf[:])
	if n < InputRecLen {
		return InputEvent{}, false
	}
	ev, _ := DecodeInputEvent(buf[:])
	return ev, true
}
