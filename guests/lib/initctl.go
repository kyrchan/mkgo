package kern

// Phase 19 supervision protocol (AGENTS.md Phase 19, ABI §7.4).
//
// initctl is NOT a kernel op: the shell sends canonical-framed datagrams
// (FrameCanonical/InboxRequest) to the well-known "init" port and init.wasm
// answers them like any user server (ReplyBook on the requester's rname).
// The datagram op is the subop; the payload carries the service name.
//
//	1=restart      payload: service name bytes
//	2=reload-conf  payload: empty (init re-reads /etc/init.conf itself)
//	3=apply-knobs  payload: empty (init re-applies its kernel.conf text)
//	4=respawn      payload: name + NUL + '0'|'1' flag
//
// Reply payload: {u32 status, detail bytes}. Status codes:
const (
	NameInit = "init"

	InitSubRestart   uint16 = 1
	InitSubReload    uint16 = 2
	InitSubApplyKnobs uint16 = 3
	InitSubPolicy    uint16 = 4

	InitOK          uint32 = 0
	InitNotFound    uint32 = 1
	InitBadName     uint32 = 2
	InitAlready     uint32 = 3
	InitUnavailable uint32 = 4
)

// InitStatusText renders an initctl reply status for shell display.
func InitStatusText(st uint32) string {
	switch st {
	case InitOK:
		return "ok"
	case InitNotFound:
		return "not found"
	case InitBadName:
		return "bad name"
	case InitAlready:
		return "already"
	case InitUnavailable:
		return "unavailable"
	}
	return "status=" + uitoa(uint64(st))
}

// InitClient speaks the initctl protocol to the "init" well-known port.
type InitClient struct {
	c *Client
	h Handle // bind of "init" (requests ride here, replies land on c inbox)
}

// BindInit attaches to the "init" port, creating a private reply inbox.
// ErrBadHandle means init is not running (shell reports "not responding").
func BindInit(k Kernel) (*InitClient, error) {
	h := k.PortBind(NameInit)
	if h == InvalidHandle {
		return nil, ErrBadHandle
	}
	c, err := NewInboxClient(k, "shinit")
	if err != nil {
		return nil, err
	}
	return &InitClient{c: c, h: h}, nil
}

// SetBudget bounds the reply poll (yields) for every initctl call.
func (ic *InitClient) SetBudget(n int) { ic.c.Budget = n }

// call performs one subop round trip, returning status + detail string.
func (ic *InitClient) call(subop uint16, payload []byte) (uint32, string, error) {
	rep, err := ic.c.InboxRequest(ic.h, subop, payload)
	if err != nil {
		return 0, "", err
	}
	if len(rep) < CanonicalHeaderLen+4 {
		return 0, "", ErrShort
	}
	return Get32(rep[CanonicalHeaderLen:]), string(rep[CanonicalHeaderLen+4:]), nil
}

// Restart asks init to kill and respawn svc immediately.
func (ic *InitClient) Restart(svc string) (uint32, string, error) {
	return ic.call(InitSubRestart, []byte(svc))
}

// Reload asks init to re-read /etc/init.conf and adopt the diff.
func (ic *InitClient) Reload() (uint32, string, error) {
	return ic.call(InitSubReload, nil)
}

// ApplyKnobs asks init to re-apply its kernel.conf text via SETCONF.
func (ic *InitClient) ApplyKnobs() (uint32, string, error) {
	return ic.call(InitSubApplyKnobs, nil)
}

// RespawnPolicy sets svc's respawn flag (yes=true).
func (ic *InitClient) RespawnPolicy(svc string, yes bool) (uint32, string, error) {
	pl := append(append([]byte(svc), 0), boolByte(yes))
	return ic.call(InitSubPolicy, pl)
}

func boolByte(yes bool) byte {
	if yes {
		return '1'
	}
	return '0'
}

// SplitPolicyPayload parses a subop-4 payload into name + flag.
func SplitPolicyPayload(pl []byte) (name string, yes bool, ok bool) {
	for i, b := range pl {
		if b == 0 {
			if i+1 != len(pl)-1 {
				return "", false, false
			}
			switch pl[i+1] {
			case '1':
				return string(pl[:i]), true, true
			case '0':
				return string(pl[:i]), false, true
			}
			return "", false, false
		}
	}
	return "", false, false
}
