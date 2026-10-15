//go:build !wasip1

package main

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	lib "kernel.lane/guests/lib"
)

// udpTransport implements OffloadTransport over real UDP sockets against
// an external DHCP fixture (scripts/dhcpd_shortlease.py). It bridges the
// Ethernet-frame-oriented DHCP client with the raw-UDP fixture by
// stripping/adding Ethernet+IP+UDP headers on each frame.
type udpTransport struct {
	// mgmtHook, when set, receives outbound 802.11 management frames. The
	// DHCP fixture only speaks DHCP, so scan/associate need a fake AP.
	mgmtHook func([]byte)
	conn     *net.UDPConn
	server   *net.UDPAddr
	mu       sync.Mutex
	incoming [][]byte
	closed   bool
}

// newUDPTransport creates a transport bound to an ephemeral local UDP port
// that sends DHCP payloads to serverAddr (e.g. "127.0.0.1:1067").
// captureFixture wires the fixture's output into the test log so a fixture
// that fails to start is visible instead of looking like a client timeout.
func captureFixture(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	t.Cleanup(func() { t.Logf("fixture output:\n%s", buf.String()) })
}

// waitForUDPFixture blocks until something is actually listening on addr.
//
// net.Dial on a UDP socket succeeds even when nothing is bound -- the socket
// is connectionless -- so the old readiness probe never waited at all. The
// test then sent its DISCOVER before Python had bound the port, the datagram
// was dropped, and the client timed out. Probing for it properly: a CONNECTED
// UDP socket that sends and reads gets ECONNREFUSED back when no listener
// exists, and simply times out when one does.
func waitForUDPFixture(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.Dial("udp", addr)
		if err == nil {
			c.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = c.Write([]byte{0})
			c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
			_, rerr := c.Read(make([]byte, 1))
			c.Close()
			if rerr == nil {
				continue // the fixture answered our junk probe; it is up
			}
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				return nil // no ICMP error => a listener exists
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("no listener on %s after %s", addr, timeout)
}

// startFakeAP seeds a beacon for ssid and answers the driver's association
// request with an association response. Run() scans for beacons and then
// associates, so without this the DHCP path is never reached: the service
// exits at "scan: no beacons received" before it ever sends a DISCOVER.
func startFakeAP(t *udpTransport, bssid MAC, ssid string) {
	t.mu.Lock()
	t.incoming = append(t.incoming, buildBeacon(bssid, ssid, 6))
	t.mgmtHook = func(f []byte) {
		m, err := parseMgmt(f[1:])
		if err != nil || m.Subtype() != MgmtAssocReq {
			return
		}
		resp := make([]byte, 4)
		lib.Put16(resp[0:2], 0)      // status 0 = success
		lib.Put16(resp[2:4], 0x0001) // association id
		hdr := make([]byte, 24)
		lib.Put16(hdr[0:2], 0x0010) // FC: type=mgmt, subtype=assoc-resp
		copy(hdr[4:10], m.Addr1[:])
		copy(hdr[10:16], bssid[:])
		copy(hdr[16:22], bssid[:])
		out := make([]byte, 1, 1+len(hdr)+len(resp))
		out[0] = FrameTypeMgmt
		out = append(out, hdr...)
		out = append(out, resp...)
		t.mu.Lock()
		t.incoming = append(t.incoming, out)
		t.mu.Unlock()
	}
	t.mu.Unlock()
}

func newUDPTransport(serverAddr string) (*udpTransport, error) {
	addr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return nil, err
	}
	t := &udpTransport{conn: conn, server: addr}
	go t.recvLoop()
	return t, nil
}

func (t *udpTransport) SendFrame(f []byte) bool {
	if t == nil || t.conn == nil {
		return false
	}
	// 802.11 management frames never traverse the DHCP fixture; hand them to
	// the fake AP instead of trying to parse them as Ethernet/IP/UDP.
	if len(f) >= 1 && f[0] == FrameTypeMgmt {
		t.mu.Lock()
		hook := t.mgmtHook
		t.mu.Unlock()
		if hook != nil {
			hook(f)
		}
		return true
	}
	_, _, udp, err := parseUDPEth(f)
	if err != nil {
		return false
	}
	_, err = t.conn.WriteToUDP(udp.Data, t.server)
	return err == nil
}

func (t *udpTransport) RecvFrame() ([]byte, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.incoming) > 0 {
		f := t.incoming[0]
		t.incoming = t.incoming[1:]
		return f, true
	}
	return nil, false
}

func (t *udpTransport) recvLoop() {
	buf := make([]byte, 4096)
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
		t.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		frame := buildDHCPReplyFrame(payload)
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		t.incoming = append(t.incoming, frame)
		t.mu.Unlock()
	}
}

func (t *udpTransport) Close() error {
	if t == nil || t.conn == nil {
		return nil
	}
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return t.conn.Close()
}

// buildDHCPReplyFrame wraps a raw DHCP payload (no Ethernet/IP/UDP headers)
// in the same frame format the mock transport uses, so the DHCP client's
// parseUDPEth path can extract it.
func buildDHCPReplyFrame(dhcpPayload []byte) []byte {
	udp := buildUDP(DHCPServerPort, DHCPClientPort, dhcpPayload)
	ip, _ := buildIPv4(IP4{10, 0, 2, 1}, IP4{10, 0, 2, 15}, IP4ProtoUDP, udp)
	return buildEth(MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}, MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}, EthTypeIPv4, ip)
}

// TestDhcpEndToEndWithFixture drives the full DHCP client lifecycle against
// the host-side dhcpd_shortlease.py fixture (2s lease, T1=1s). It is the
// host-level gate that proves DORA, Renew, and Release all work against a
// real UDP server rather than the in-memory mock.
func TestDhcpEndToEndWithFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: dhcp fixture test needs a subprocess")
	}

	serverPort := "1067"
	cmd := exec.Command("python3", "scripts/dhcpd_shortlease.py",
		"--lease", "2", "--port", serverPort,
		// The client unicasts its REQUEST to the advertised server identifier
		// (RFC 2131). The fixture's 10.0.2.1 default is the SLIRP gateway,
		// which is unreachable from the host, so the exchange would stall
		// after the OFFER. Advertise loopback: the address the test dials.
		"--ip", "127.0.0.1")
	cmd.Dir = "../.." // the fixture lives at the repo root, not under services/
	captureFixture(t, cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dhcpd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if err := waitForUDPFixture("127.0.0.1:"+serverPort, 5*time.Second); err != nil {
		t.Fatalf("dhcp fixture never bound: %v", err)
	}

	transport, err := newUDPTransport("127.0.0.1:" + serverPort)
	if err != nil {
		t.Fatalf("udp transport: %v", err)
	}
	defer transport.Close()

	mac := MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x09}
	dhcp := newDhcpClient(transport, mac)
	dhcp.serverPort = 1067

	// 1. DORA.
	lease, err := dhcp.Run()
	if err != nil {
		t.Fatalf("DORA: %v", err)
	}
	if lease.IP != (IP4{10, 0, 2, 15}) {
		t.Fatalf("lease IP=%v want 10.0.2.15", lease.IP)
	}
	if lease.LeaseTime != 2*time.Second {
		t.Fatalf("lease=%v want 2s", lease.LeaseTime)
	}
	if lease.T1 != 1*time.Second {
		t.Fatalf("T1=%v want 1s", lease.T1)
	}

	// 2. Renew at T1.
	renewed, err := dhcp.Renew(lease)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if renewed.IP != lease.IP {
		t.Fatalf("renewed IP=%v want %v", renewed.IP, lease.IP)
	}

	// 3. Release.
	if err := dhcp.Release(lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// TestDhcpEndToEndWithFixtureFakeKernel runs the same lifecycle through the
// wlan Run() orchestration entry point with a FakeKernel and the UDP transport,
// proving the full Run() path (scan, associate, DHCP, bridge, renew, release)
// against the real fixture without real hardware.
func TestDhcpEndToEndWithFixtureFakeKernel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: dhcp fixture test needs a subprocess")
	}

	serverPort := "1068"
	cmd := exec.Command("python3", "scripts/dhcpd_shortlease.py",
		"--lease", "2", "--port", serverPort,
		// The client unicasts its REQUEST to the advertised server identifier
		// (RFC 2131). The fixture's 10.0.2.1 default is the SLIRP gateway,
		// which is unreachable from the host, so the exchange would stall
		// after the OFFER. Advertise loopback: the address the test dials.
		"--ip", "127.0.0.1")
	cmd.Dir = "../.." // the fixture lives at the repo root, not under services/
	captureFixture(t, cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dhcpd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("udp", "127.0.0.1:"+serverPort)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	transport, err := newUDPTransport("127.0.0.1:" + serverPort)
	if err != nil {
		t.Fatalf("udp transport: %v", err)
	}
	defer transport.Close()

	startFakeAP(transport, MAC{0x02, 0, 0, 0, 0, 0x01}, "testnet")

	fk := lib.NewFakeKernel()
	fk.PortCreate(lib.NameNet)
	netH := fk.PortBind(lib.NameNet)
	go func() {
		buf := make([]byte, lib.MaxMsg)
		for {
			n := fk.PortRecv(netH, buf)
			if n < lib.CanonicalHeaderLen+2 {
				time.Sleep(2 * time.Millisecond)
				continue
			}
			hdr, _ := lib.ParseHeader(buf[:n])
			// 30 bytes minimum: statusOf reads the status at [24:28] and
			// NetOpOpen then reads the socket id at [28:30]. A 28-byte reply
			// is two bytes short and the client fails with "kern: short reply".
			rep := make([]byte, 32)
			lib.Put16(rep[28:30], 1) // fake socket handle for NetOpOpen
			lib.Put16(rep, hdr.Op)
			lib.Put16(rep[2:], hdr.Seq)
			lib.Put32(rep[24:], 0)
			if hdr.RNam != "" {
				if rh := fk.PortBind(hdr.RNam); rh != lib.InvalidHandle {
					fk.PortSend(rh, rep)
				}
			}
		}
	}()

	out := &bytes.Buffer{}
	exit := Run(fk, transport, out, "testnet")
	if exit != 0 {
		t.Fatalf("Run exit=%d; output:\n%s", exit, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("[wlan] renewed:")) {
		t.Fatalf("renewal marker not found in output:\n%s", out.String())
	}
}
