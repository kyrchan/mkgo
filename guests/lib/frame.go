package kern

import (
	"errors"
	"time"
)

// ---- little-endian framing primitives (ABI: all integers LE) ----

func Put16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func Put32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
func Put64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * uint(i)))
	}
}

func Get16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
func Get32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func Get64(b []byte) uint64 {
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// AppendLStr appends a {u16 len, bytes} length-prefixed string (no NULs
// anywhere in the ABI).
func AppendLStr(dst []byte, s string) []byte {
	dst = append(dst, 0, 0)
	Put16(dst[len(dst)-2:], uint16(len(s)))
	return append(dst, s...)
}

// LStr decodes a {u16 len, bytes} field at off, returning the string and
// the offset just past it. ok=false on truncation.
func LStr(b []byte, off int) (s string, next int, ok bool) {
	if off+2 > len(b) {
		return "", 0, false
	}
	n := int(Get16(b[off : off+2]))
	off += 2
	if off+n > len(b) {
		return "", 0, false
	}
	return string(b[off : off+n]), off + n, true
}

// RPC errors.
var (
	ErrNoReply   = errors.New("kern: no reply within budget")
	ErrShort     = errors.New("kern: short reply")
	ErrBadHandle = errors.New("kern: invalid port handle")
	ErrRejected  = errors.New("kern: request rejected")
)

// DefaultRecvBudget is the sched_yield poll budget before a reply is
// declared lost (kernel endpoints reply inline; user services may need
// a few scheduling quanta).
const DefaultRecvBudget = 200000

// Client is one session's request/response machinery.
//
// Kernel-owned §7 endpoints dispatch inline at send time and enqueue the
// reply on the sending handle itself (core/kernsvc.cc), so those use
// Direct mode. User-level servers cannot address the sender's private
// handle — they follow the lane reply-channel convention instead
// (services/ABI-NOTES.md): the client owns a uniquely-named inbox port,
// puts its name in every request, and the server binds it once and
// reuses that alias to deliver replies. That is Inbox mode.
type Client struct {
	k         Kernel
	inboxName string
	inbox     Handle
	seq       uint16
	Budget    int // recv poll budget in yields; DefaultRecvBudget if 0
}

// NewDirectClient returns a client whose replies arrive on h (the same
// handle requests are sent from). h is typically a bind of "registry",
// "devman" or "power".
func NewDirectClient(k Kernel, h Handle) *Client {
	return &Client{k: k, inbox: h}
}

// NewInboxClient creates the client's unique reply-channel port. base is
// a stable role tag ("fs", "login", ...); a nanosecond salt keeps
// same-named sessions from colliding on the global namespace.
func NewInboxClient(k Kernel, base string) (*Client, error) {
	name := base
	for {
		if len(name) <= MaxName {
			if h := k.PortCreate(name); h != InvalidHandle {
				return &Client{k: k, inboxName: name, inbox: h}, nil
			}
		}
		salt := time.Now().UnixNano() & 0xFFFF
		cand := base + "." + uitoa(uint64(salt))
		if len(cand) > MaxName {
			cand = cand[:MaxName]
		}
		if cand == name {
			return nil, ErrRejected
		}
		name = cand
	}
}

// Name returns this client's inbox name ("" in Direct mode).
func (c *Client) Name() string { return c.inboxName }

// Inbox returns the client's own reply-channel handle.
func (c *Client) Inbox() Handle { return c.inbox }

func (c *Client) nextSeq() uint16 { c.seq++; return c.seq }

func (c *Client) budget() int {
	if c.Budget > 0 {
		return c.Budget
	}
	return DefaultRecvBudget
}

// FrameRequest builds {u16 op, u16 seq, payload} for Direct mode.
func FrameRequest(op uint16, seq uint16, payload []byte) []byte {
	req := make([]byte, 4, 4+len(payload))
	Put16(req, op)
	Put16(req[2:], seq)
	return append(req, payload...)
}

// Call sends an already-framed request on h and polls for the reply with
// the matching seq on h as well (Direct mode / kernel endpoints).
func (c *Client) Call(h Handle, req []byte) ([]byte, error) {
	if rc := c.k.PortSend(h, req); rc == StatusErr {
		return nil, ErrBadHandle
	}
	buf := make([]byte, MaxMsg)
	want := Get16(req[2:4])
	for i := 0; i < c.budget(); i++ {
		n := c.k.PortRecv(h, buf)
		if n > 0 {
			if n < 4 {
				return nil, ErrShort
			}
			if Get16(buf[2:4]) == want {
				out := make([]byte, n)
				copy(out, buf[:n])
				return out, nil
			}
			continue // stale/late reply for another seq: drop
		}
		if n < 0 {
			return nil, ErrBadHandle
		}
		c.k.Yield()
	}
	return nil, ErrNoReply
}

// Request performs one Direct-mode round trip: frames {op,seq,payload},
// sends on h, waits for the matching-seq reply.
func (c *Client) Request(h Handle, op uint16, payload []byte) ([]byte, error) {
	return c.Call(h, FrameRequest(op, c.nextSeq(), payload))
}

// InboxRequest frames {u16 op, u16 seq, u16 inboxLen, inbox, payload} and
// sends it on h; the reply arrives on this client's own inbox port.
func (c *Client) InboxRequest(h Handle, op uint16, payload []byte) ([]byte, error) {
	req := make([]byte, 4, 4+2+len(c.inboxName)+len(payload))
	Put16(req, op)
	Put16(req[2:], c.nextSeq())
	req = AppendLStr(req, c.inboxName)
	req = append(req, payload...)
	if rc := c.k.PortSend(h, req); rc == StatusErr {
		return nil, ErrBadHandle
	}
	return c.recvMatch()
}

func (c *Client) recvMatch() ([]byte, error) {
	buf := make([]byte, MaxMsg)
	want := c.seq
	for i := 0; i < c.budget(); i++ {
		n := c.k.PortRecv(c.inbox, buf)
		if n > 0 {
			if n < 4 {
				return nil, ErrShort
			}
			if Get16(buf[2:4]) == want {
				out := make([]byte, n)
				copy(out, buf[:n])
				return out, nil
			}
			continue
		}
		if n < 0 {
			return nil, ErrBadHandle
		}
		c.k.Yield()
	}
	return nil, ErrNoReply
}

// ReplyTo resolves a request's reply channel the server side of Inbox
// mode: bind (once) the client-declared inbox name and remember its
// alias. Returns the handle replies must be sent on. Servers MUST cache
// per name — the kernel has no unbind, so re-binding per request would
// exhaust the 8-handles-per-session budget.
type ReplyBook struct {
	k    Kernel
	byIn map[string]Handle
}

func NewReplyBook(k Kernel) *ReplyBook {
	return &ReplyBook{k: k, byIn: make(map[string]Handle)}
}

// Bind returns the send-handle for the client inbox named name.
func (rb *ReplyBook) Bind(name string) (Handle, error) {
	if h, ok := rb.byIn[name]; ok {
		return h, nil
	}
	h := rb.k.PortBind(name)
	if h == InvalidHandle {
		return InvalidHandle, ErrBadHandle
	}
	rb.byIn[name] = h
	return h, nil
}

func uitoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
