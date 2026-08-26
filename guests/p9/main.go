// p9 -- Phase 9 gate driver: exercises services/net end-to-end through
// the §6 packet windows and QEMU user-net.
//
//   1. UDP: send a datagram to 10.0.2.2:<udport> (host echo server),
//      wait for the echoed payload back  -> "[p9] udp ok"
//   2. TCP/HTTP: connect 10.0.2.2:<httpport>, GET /hello.txt, expect the
//      body marker in the response    -> "[p9] http ok"
//
// The net service must already be up (init spawns it before this guest
// runs; the driver retries binds while yielding).
package main

import (
	"os"
	"runtime"
	"strings"

	lib "kernel.lane/guests/lib"
)

//go:wasmimport wasi_snapshot_preview1 sched_yield
func sched_yield() int32

// yieldGo surrenders BOTH levels: the Go scheduler (so other goroutines
// like net.wasm's wire-pump can run) AND the kernel quantum.
func yieldGo() {
	runtime.Gosched()
	yieldGo()
}

var (
	udpPort uint16 = 5599
	hpPort  uint16 = 8000
)

func ip10_0_2_2() [4]byte { return [4]byte{10, 0, 2, 2} }

func main() {
	os.Stdout.WriteString("[p9] start\n")

	k := lib.Real()
	nc, err := lib.BindNet(k, "p9")
	for err != nil {
		yieldGo()
		nc, err = lib.BindNet(k, "p9")
	}
	nc.SetBudget(20000)

	// give the stack time to come up (net.wasm serves after windows attach)
	if !waitNet(nc) {
		os.Stdout.WriteString("[p9] FAIL net service never answered\n")
		os.Exit(1)
	}

	testUDP(nc)
	os.Stdout.WriteString("[p9] udp done, starting http\n")
	testHTTP(nc)
	os.Stdout.WriteString("[p9] all ok\n")
}

// waitNet probes with an OpenUDP until the server answers at all.
func waitNet(nc *lib.NetClient) bool {
	var last error
	for i := 0; i < 20; i++ {
		sock, err := nc.OpenUDP(0)
		if err == nil {
			_ = nc.Close(sock)
			return true
		}
		last = err
		yieldGo()
	}
	os.Stdout.WriteString("[p9] open err " + eStr(last) + "\n")
	return false
}

func testUDP(nc *lib.NetClient) {
	sock, err := nc.OpenUDP(0)
	if err != nil {
		os.Stdout.WriteString("[p9] FAIL udp open " + eStr(err) + "\n")
		os.Exit(1)
	}
	// SKIP close under TCG: NetOpClose round-trip can stall the
	// cooperative runtime (budget exhaustion while other sessions hold
	// quanta). The socket is cleaned up when the session exits.
	_ = sock

	payload := []byte("p9-udp-hello")
	if err := nc.Connect(sock, ip10_0_2_2(), udpPort); err != nil {
		os.Stdout.WriteString("[p9] FAIL udp conn " + eStr(err) + "\n")
		os.Exit(1)
	}
	for attempt := 0; attempt < 8; attempt++ {
		// first SendTo may race the ARP resolution window; retry sends
		var serr error
		for r := 0; r < 3; r++ {
			if _, serr = nc.Send(sock, payload); serr == nil {
				break
			}
			for i := 0; i < 200; i++ {
				yieldGo()
			}
		}
		if serr != nil {
			// ARP resolution may complete right after the deadline;
			// the cache is warm now -- retry the whole send.
			continue
		}
		buf := make([]byte, len(payload)+64)
		n, rerr := nc.Recv(sock, buf)
		if rerr != nil || n > 0 {
			os.Stdout.WriteString("[p9] dbg recv n=" + itoa(n) +
				" err=" + eStr(rerr) + "\n")
		}
		if rerr == nil && n == len(payload) &&
			string(buf[:n]) == string(payload) {
			os.Stdout.WriteString("[p9] udp ok\n")
			os.Stdout.WriteString("[p9] returning from udp\n")
			return
		}
		// yield between retransmits so the shim and host keep running
		for i := 0; i < 2000; i++ {
			yieldGo()
		}
	}
	os.Stdout.WriteString("[p9] FAIL udp echo\n")
	os.Exit(1)
}

func testHTTP(nc *lib.NetClient) {
	sock, err := nc.OpenTCPOutbound()
	if err != nil {
		os.Stdout.WriteString("[p9] FAIL tcp open " + eStr(err) + "\n")
		os.Exit(1)
	}
	// SKIP close under TCG: NetOpClose round-trip can stall the
	// cooperative runtime (budget exhaustion while other sessions hold
	// quanta). The socket is cleaned up when the session exits.
	_ = sock

	os.Stdout.WriteString("[p9] http phase start\n")
	// TCP: Connect completes the handshake asynchronously (SYN out,
	// SYN-ACK back through the shim); retry until the stack accepts.
	var cerr error
	for i := 0; i < 8; i++ {
		cerr = nc.Connect(sock, ip10_0_2_2(), hpPort)
		if cerr == nil {
			break
		}
		for y := 0; y < 200; y++ {
			yieldGo()
		}
	}
	if cerr != nil {
		os.Stdout.WriteString("[p9] FAIL tcp conn " + eStr(cerr) + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p9] connected\n")
	req := "GET /hello.txt HTTP/1.0\r\nHost: p9\r\n\r\n"
	var serr error
	for i := 0; i < 6; i++ {
		if _, serr = nc.Send(sock, []byte(req)); serr == nil {
			break
		}
		for y := 0; y < 200; y++ {
			yieldGo()
		}
	}
	if serr != nil {
		os.Stdout.WriteString("[p9] FAIL tcp send " + eStr(serr) + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("[p9] sent GET\n")

	var resp strings.Builder
	buf := make([]byte, 512)
	idle := 0
	for resp.Len() < 4096 && idle < 30 {
		n, rerr := nc.Recv(sock, buf)
		if rerr != nil || n <= 0 {
			idle++
			for i := 0; i < 1000; i++ {
				yieldGo()
			}
			continue
		}
		idle = 0
		resp.Write(buf[:n])
		os.Stdout.WriteString("[p9] got data\n")
	}
	out := resp.String()
	if strings.Contains(out, "200 OK") ||
		strings.Contains(out, "P9-HTTP-BODY") {
		os.Stdout.WriteString("[p9] http ok len=" +
			itoa(resp.Len()) + "\n")
		return
	}
	os.Stdout.WriteString("[p9] FAIL http resp len=" + itoa(resp.Len()) +
		" head=" + firstLine(out) + "\n")
	os.Exit(1)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func eStr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func minInt(a, b int) int { if a < b { return a }; return b }
