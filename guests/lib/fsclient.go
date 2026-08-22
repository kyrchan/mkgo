package kern

import "errors"

// FS server wire protocol v0 — LANE-LOCAL contract shared by fs.wasm and
// its clients (kernel-routed preview1 forwarding maps onto the same ops;
// see services/ABI-NOTES.md). Datagram shape:
//
//	request  {u16 op, u16 seq, u16 inboxLen, inbox, payload}
//	reply    {u16 op, u16 seq, ...}   on the client's inbox port
//
// All paths are absolute from the FAT root ("/etc", "/home/u1/x.txt").
// Payloads:
//
//	STAT/LIST/CREATE/MKDIR/DELETE: {u16 pLen, path}
//	READ:  {u16 pLen, path, u64 off, u16 cnt}
//	WRITE: {u16 pLen, path, u64 off, u16 cnt, data}
//
// Replies:
//
//	STAT:  {i32 status, u32 size, u8 attr, u32 cluster}
//	LIST:  {i32 status, u32 n, n × {u16 nLen, name, u8 attr, u32 size}}
//	READ:  {i32 status, u16 got, data[got]}
//	WRITE: {i32 status, u32 newSize}
//	rest:  {i32 status}
const (
	OpFSStat   uint16 = 1
	OpFSList   uint16 = 2
	OpFSRead   uint16 = 3
	OpFSWrite  uint16 = 4
	OpFSCreate uint16 = 5
	OpFSMkdir  uint16 = 6
	OpFSDelete uint16 = 7
)

// Directory-entry attribute bits (FAT semantics).
const (
	AttrDir     uint8 = 0x10
	AttrArchive uint8 = 0x20
	AttrVolume  uint8 = 0x08
)

// FS status codes (negative mirrors of the services/fs error set).
const (
	FSOK       = int32(0)
	FSIO       = int32(-1)
	FSNoEntry  = int32(-2)
	FSExists   = int32(-3)
	FSNotDir   = int32(-4)
	FSIsDir    = int32(-5)
	FSNoSpace  = int32(-6)
	FSBadName  = int32(-7)
	FSNotEmpty = int32(-8)
	FSRange    = int32(-9)
	FSAccess   = int32(-10) // permission denied (multiuser policy)
)

var (
	ErrFSIO       = errors.New("fs: io error")
	ErrFSNoEntry  = errors.New("fs: no such file or directory")
	ErrFSExists   = errors.New("fs: already exists")
	ErrFSNotDir   = errors.New("fs: parent is not a directory")
	ErrFSIsDir    = errors.New("fs: is a directory")
	ErrFSNoSpace  = errors.New("fs: no space")
	ErrFSBadName  = errors.New("fs: bad name")
	ErrFSNotEmpty = errors.New("fs: directory not empty")
	ErrFSRange    = errors.New("fs: offset out of range")
	ErrFSAccess   = errors.New("fs: permission denied")
)

// FSErr converts a wire status into its Go sentinel.
func FSErr(status int32) error {
	switch status {
	case FSOK:
		return nil
	case FSNoEntry:
		return ErrFSNoEntry
	case FSExists:
		return ErrFSExists
	case FSNotDir:
		return ErrFSNotDir
	case FSIsDir:
		return ErrFSIsDir
	case FSNoSpace:
		return ErrFSNoSpace
	case FSBadName:
		return ErrFSBadName
	case FSNotEmpty:
		return ErrFSNotEmpty
	case FSRange:
		return ErrFSRange
	case FSAccess:
		return ErrFSAccess
	default:
		return ErrFSIO
	}
}

// FileInfo is a STAT/LIST record.
type FileInfo struct {
	Name    string
	Size    uint32
	Attr    uint8
	Cluster uint32
}

// IsDir reports directory-ness.
func (fi FileInfo) IsDir() bool { return fi.Attr&AttrDir != 0 }

// FSClient talks to the "fs" well-known service over Inbox mode.
type FSClient struct {
	c *Client
	h Handle
}

// BindFS binds the fs service endpoint and prepares this client's reply
// channel.
func BindFS(k Kernel, roleTag string) (*FSClient, error) {
	h := k.PortBind(NameFS)
	if h == InvalidHandle {
		return nil, ErrBadHandle
	}
	c, err := NewInboxClient(k, roleTag)
	if err != nil {
		return nil, err
	}
	return &FSClient{c: c, h: h}, nil
}

func pathPayload(path string) []byte { return AppendLStr(nil, path) }

// splitReply decodes a canonical-header reply: status at payload start
// (offset 24), body after it.
func splitReply(rep []byte) (int32, []byte, error) {
	if len(rep) < 28 {
		return 0, nil, ErrShort
	}
	st := int32(Get32(rep[24:28]))
	return st, rep[28:], nil
}

// Stat returns metadata for path.
func (f *FSClient) Stat(path string) (FileInfo, error) {
	rep, err := f.c.InboxRequest(f.h, OpFSStat, pathPayload(path))
	if err != nil {
		return FileInfo{}, err
	}
	st, rest, err := splitReply(rep)
	if err != nil {
		return FileInfo{}, err
	}
	if st != FSOK {
		return FileInfo{}, FSErr(st)
	}
	if len(rest) < 9 {
		return FileInfo{}, ErrShort
	}
	return FileInfo{
		Size:    Get32(rest[0:]),
		Attr:    rest[4],
		Cluster: Get32(rest[5:]),
	}, nil
}

// List enumerates a directory.
func (f *FSClient) List(path string) ([]FileInfo, error) {
	rep, err := f.c.InboxRequest(f.h, OpFSList, pathPayload(path))
	if err != nil {
		return nil, err
	}
	st, rest, err := splitReply(rep)
	if err != nil {
		return nil, err
	}
	if st != FSOK {
		return nil, FSErr(st)
	}
	if len(rest) < 4 {
		return nil, ErrShort
	}
	n := int(Get32(rest[0:]))
	out := make([]FileInfo, 0, n)
	off := 4
	for i := 0; i < n; i++ {
		name, next, ok := LStr(rest, off)
		if !ok || next+5 > len(rest) {
			break
		}
		out = append(out, FileInfo{
			Name: name,
			Attr: rest[next],
			Size: Get32(rest[next+1:]),
		})
		off = next + 5
	}
	return out, nil
}

// maxReadChunk keeps a READ reply inside one §1 datagram.
const maxReadChunk = MaxMsg - 4096/2 // conservative headroom

// ReadFile reads up to len(buf) bytes at off via chunked READs; returns
// bytes copied (may be < len(buf) at EOF).
func (f *FSClient) ReadFile(path string, off uint64, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		cnt := len(buf) - total
		if cnt > maxReadChunk {
			cnt = maxReadChunk
		}
		pl := AppendLStr(nil, path)
		var tail [10]byte
		Put64(tail[:], off+uint64(total))
		Put16(tail[8:], uint16(cnt))
		pl = append(pl, tail[:]...)

		rep, err := f.c.InboxRequest(f.h, OpFSRead, pl)
		if err != nil {
			return total, err
		}
		st, rest, err := splitReply(rep)
		if err != nil {
			return total, err
		}
		if st != FSOK {
			if total > 0 {
				return total, nil
			}
			return 0, FSErr(st)
		}
		if len(rest) < 2 {
			return total, ErrShort
		}
		got := int(Get16(rest[0:]))
		if got == 0 {
			break // EOF
		}
		n := copy(buf[total:], rest[2:2+got])
		total += n
		if n < got {
			break
		}
		if got < cnt {
			break
		}
	}
	return total, nil
}

// WriteFile writes data at off via chunked WRITEs, creating/truncating
// nothing implicitly: the file must exist (use Create first).
func (f *FSClient) WriteFile(path string, off uint64, data []byte) (int, error) {
	written := 0
	for written < len(data) || len(data) == 0 && written == 0 {
		cnt := len(data) - written
		if cnt > maxReadChunk {
			cnt = maxReadChunk
		}
		pl := AppendLStr(nil, path)
		var head [10]byte
		Put64(head[:], off+uint64(written))
		Put16(head[8:], uint16(cnt))
		pl = append(pl, head[:]...)
		pl = append(pl, data[written:written+cnt]...)

		rep, err := f.c.InboxRequest(f.h, OpFSWrite, pl)
		if err != nil {
			return written, err
		}
		st, _, err := splitReply(rep)
		if err != nil {
			return written, err
		}
		if st != FSOK {
			return written, FSErr(st)
		}
		written += cnt
		if len(data) == 0 {
			break
		}
	}
	return written, nil
}

// Create makes path (create-or-truncate regular file).
func (f *FSClient) Create(path string) error { return f.simple(OpFSCreate, path) }

// Mkdir makes path as a directory.
func (f *FSClient) Mkdir(path string) error { return f.simple(OpFSMkdir, path) }

// Delete unlinks path (file or empty dir).
func (f *FSClient) Delete(path string) error { return f.simple(OpFSDelete, path) }

func (f *FSClient) simple(op uint16, path string) error {
	rep, err := f.c.InboxRequest(f.h, op, pathPayload(path))
	if err != nil {
		return err
	}
	st, _, err := splitReply(rep)
	if err != nil {
		return err
	}
	return FSErr(st)
}

// SetBudget overrides the recv poll budget for this client (tests).
func (f *FSClient) SetBudget(n int) { f.c.Budget = n }

// OpFSRegister is the lane-local session-registration op (fs server
// multiuser model; services/ABI-NOTES.md §4).
const OpFSRegister uint16 = 8

// Register binds uid→(name, capmask) on the fs server. Issued by
// login/init after successful auth.
func (f *FSClient) Register(uid uint32, name string, capmask uint64) error {
	pl := make([]byte, 0, 14+len(name))
	var head [4]byte
	Put32(head[:], uid)
	pl = append(pl, head[:]...)
	pl = AppendLStr(pl, name)
	var m [8]byte
	Put64(m[:], capmask)
	pl = append(pl, m[:]...)

	rep, err := f.c.InboxRequest(f.h, OpFSRegister, pl)
	if err != nil {
		return err
	}
	st, _, err := splitReply(rep)
	if err != nil {
		return err
	}
	return FSErr(st)
}
