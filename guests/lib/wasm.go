//go:build wasip1

package kern

// Raw bindings over the frozen ABI v1 imports. The kernel links these
// under module "kernel" (core/wasi_glue.cc); sched_yield is the frozen
// WASI profile's. Signatures must not drift from abi/ABI.md §1/§4.

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield()

//go:wasmimport kernel kern_port_create
func port_create(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_bind
func port_bind(name *byte, nameLen uint32) int32

//go:wasmimport kernel kern_port_send
func port_send(h Handle, buf *byte, length uint32) int32

//go:wasmimport kernel kern_port_recv
func port_recv(h Handle, buf *byte, cap uint32) int32

//go:wasmimport kernel kern_input_recv
func input_recv(buf *byte, cap uint32) int32

//go:wasmimport kernel kern_focus_set
func focus_set(h Handle)

type realKernel struct{}

// Real returns the wasm-backed Kernel (raw kernel imports).
func Real() Kernel { return realKernel{} }

func sb(s string) []byte {
	b := make([]byte, len(s))
	copy(b, s)
	return b
}

func (realKernel) PortCreate(name string) Handle {
	if len(name) == 0 || len(name) > MaxName {
		return InvalidHandle
	}
	b := sb(name)
	return port_create(&b[0], uint32(len(b)))
}

func (realKernel) PortBind(name string) Handle {
	if len(name) == 0 || len(name) > MaxName {
		return InvalidHandle
	}
	b := sb(name)
	return port_bind(&b[0], uint32(len(b)))
}

func (realKernel) PortSend(h Handle, data []byte) int32 {
	if len(data) == 0 || len(data) > MaxMsg {
		return StatusErr // kernel rejects len==0 / oversize outright
	}
	return port_send(h, &data[0], uint32(len(data)))
}

func (realKernel) PortRecv(h Handle, buf []byte) int32 {
	if len(buf) == 0 {
		return StatusErr
	}
	return port_recv(h, &buf[0], uint32(len(buf)))
}

func (realKernel) InputRecv(buf []byte) int32 {
	if len(buf) == 0 {
		return 0
	}
	return input_recv(&buf[0], uint32(len(buf)))
}

func (realKernel) FocusSet(h Handle) { focus_set(h) }

func (realKernel) Yield() { sched_yield() }
