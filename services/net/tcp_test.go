package main

import (
	"bytes"
	"testing"
	"time"
)

// tcpPair builds two stacks with always-running pumps and returns them
// plus a stop channel.
func tcpPair(t *testing.T) (a, b *Stack, stop chan struct{}) {
	t.Helper()
	a, b = pair(t)
	stop = make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.pump()
			b.pump()
			time.Sleep(50 * time.Microsecond)
		}
	}()
	return a, b, stop
}

// waitForTCP drives until cond or deadline (accepts need pumping time).
func waitForTCP(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

const testPort uint16 = 9000

func TestTCPHandshakeDataClose(t *testing.T) {
	a, b, stop := tcpPair(t)
	defer close(stop)

	ln, err := b.tcp.Listen(testPort)
	if err != nil {
		t.Fatal(err)
	}

	cli, err := a.tcp.Dial(a.IP, b.IP, testPort)
	if err != nil {
		t.Fatal(err)
	}
	waitForTCP(t, func() bool { return cli.State() == "ESTABLISHED" },
		"client never reached ESTABLISHED: "+cli.State())

	var srv *TCPConn
	waitForTCP(t, func() bool { srv, _ = ln.Accept(); return srv != nil },
		"server never accepted")
	if srv.State() != "ESTABLISHED" {
		t.Fatalf("server state %s", srv.State())
	}

	// client → server data
	msg := bytes.Repeat([]byte("x"), 1000) // forces multi-chunk flush? no: 2 segs
	if n, err := cli.Write(msg); err != nil || n != len(msg) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	buf := make([]byte, len(msg)+64)
	got := make([]byte, 0, len(msg))
	waitForTCP(t, func() bool {
		for {
			n := srv.Recv(buf)
			if n == 0 {
				break
			}
			got = append(got, buf[:n]...)
		}
		return len(got) == len(msg)
	}, "server stream incomplete")
	if !bytes.Equal(got, msg) {
		t.Fatal("stream corrupted")
	}

	// server → client data
	back := []byte("ack from server")
	if _, err := srv.Write(back); err != nil {
		t.Fatal(err)
	}
	rbuf := make([]byte, 64)
	rgot := make([]byte, 0, len(back))
	waitForTCP(t, func() bool {
		for {
			n := cli.Recv(rbuf)
			if n == 0 {
				break
			}
			rgot = append(rgot, rbuf[:n]...)
		}
		return len(rgot) == len(back)
	}, "client stream incomplete")
	if !bytes.Equal(rgot, back) {
		t.Fatal("reverse stream corrupted")
	}

	// orderly teardown: client closes first
	if err := cli.Close(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	waitForTCP(t, func() bool { return srv.State() == "CLOSED" },
		"server never closed: "+srv.State())
}

func TestTCPSlidingWindowMultiSegment(t *testing.T) {
	a, b, stop := tcpPair(t)
	defer close(stop)

	ln, _ := b.tcp.Listen(testPort)
	cli, _ := a.tcp.Dial(a.IP, b.IP, testPort)
	waitForTCP(t, func() bool { return cli.State() == "ESTABLISHED" }, "no est")
	var srv *TCPConn
	waitForTCP(t, func() bool { srv, _ = ln.Accept(); return srv != nil }, "no accept")

	// burst larger than one segment AND than the v1 in-flight cap:
	// sliding window must pace segments as ACKs drain inFlight.
	total := 4096 // > tcpWindowCap(1024), > 8×maxSeg
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			cli.mu.Lock()
			cli.flushLocked() // keep pushing while window opens
			cli.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	if _, err := cli.Write(payload); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 0, total)
	buf := make([]byte, 1500)
	waitForTCP(t, func() bool {
		for {
			n := srv.Recv(buf)
			if n == 0 {
				break
			}
			got = append(got, buf[:n]...)
		}
		return len(got) == total
	}, "stream transfer incomplete")
	if !bytes.Equal(got, payload) {
		t.Fatal("sliding-window stream corrupted")
	}
}

func TestTCPResetKillsConnection(t *testing.T) {
	a, b, stop := tcpPair(t)
	defer close(stop)

	ln, _ := b.tcp.Listen(testPort)
	cli, _ := a.tcp.Dial(a.IP, b.IP, testPort)
	waitForTCP(t, func() bool { return cli.State() == "ESTABLISHED" }, "no est")
	var srv *TCPConn
	waitForTCP(t, func() bool { srv, _ = ln.Accept(); return srv != nil }, "no accept")

	// forge a RST from the peer by injecting into a's inbound path
	rst := &TCPSegment{
		SrcPort: testPort,
		DstPort: cli.LocalPort,
		Seq:     srv.rcvNXTUnderRace(),
		Flags:   TCPRst,
	}
	a.injectFrom(b, rst)

	waitForTCP(t, func() bool { return cli.State() == "CLOSED" && cli.Err() == ErrConnReset },
		"RST ignored: "+cli.State())
}
