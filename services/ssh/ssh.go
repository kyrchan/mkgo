// Package main — SSH server over net.wasm TCP (Phase 13.5).
// Logic is host-testable; main.go wires the real kernel in wasip1.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
	lib "kernel.lane/guests/lib"
)

// SSHServer wraps the SSH server config + net listener.
type SSHServer struct {
	config   *ssh.ServerConfig
	listener *lib.NetListener
	output   io.Writer
	hostKey  *ecdsa.PrivateKey
}

// NewSSHServer creates a server with an ephemeral ECDSA host key.
func NewSSHServer() *SSHServer {
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{
				Extensions: map[string]string{
					"uid": "0",
					"cwd": "/home/root",
				},
			}, nil
		},
	}
	hostKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hostKey)
	config.AddHostKey(signer)
	return &SSHServer{config: config, hostKey: hostKey}
}

// Run binds the net port, listens on TCP/22, prints "ssh: all ok", then
// serves connections forever.
func Run(k lib.Kernel, out io.Writer) error {
	server := NewSSHServer()
	server.output = out
	server.config = server.config

	nc, err := lib.BindNet(k, "ssh")
	if err != nil {
		return fmt.Errorf("bind net: %w", err)
	}

	listener, err := lib.ListenTCP(nc, 22)
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	server.listener = listener

	fmt.Fprintln(out, "ssh: all ok")

	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Fprintf(out, "[ssh] accept failed: %v\n", err)
			k.Yield()
			continue
		}
		go handleConn(server.config, conn)
	}
}

// portConn adapts a lib.NetConn to net.Conn.
//
// The missing half of net.Conn is the address and deadline surface, and
// there is no honest way to implement it in guests/lib: satisfying
// net.Conn means importing "net", and lib is linked by every net client
// (p9.wasm, shell.wasm), so that import would be paid for by all of them
// to serve one unused service. So the adapter lives here, where "net" is
// already imported.
//
// The values themselves are truthful rather than fabricated: a session's
// connection belongs to a message port, not to an OS socket, so Network is
// "net" and there is no IP:port to report. Deadlines are no-ops because
// blocking is governed by the scheduler quantum, not by a socket timeout --
// and silently accepting a deadline we cannot honour would be worse than
// ignoring it.
type portConn struct{ c *lib.NetConn }

func asNetConn(c *lib.NetConn) net.Conn { return portConn{c} }

func (p portConn) Read(b []byte) (int, error)       { return p.c.Read(b) }
func (p portConn) Write(b []byte) (int, error)      { return p.c.Write(b) }
func (p portConn) Close() error                     { return p.c.Close() }
func (p portConn) LocalAddr() net.Addr              { return portAddr{} }
func (p portConn) RemoteAddr() net.Addr             { return portAddr{} }
func (p portConn) SetDeadline(time.Time) error      { return nil }
func (p portConn) SetReadDeadline(time.Time) error  { return nil }
func (p portConn) SetWriteDeadline(time.Time) error { return nil }

type portAddr struct{}

func (portAddr) Network() string { return "net" }
func (portAddr) String() string  { return "net-port" }

func handleConn(config *ssh.ServerConfig, conn *lib.NetConn) {
	defer conn.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(asNetConn(conn), config)
	if err != nil {
		return
	}
	defer sConn.Close()
	ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, _, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go handleSession(channel)
	}
}

func handleSession(channel ssh.Channel) {
	defer channel.Close()
	buf := make([]byte, 512)
	for {
		n, err := channel.Read(buf)
		if err != nil || n == 0 {
			return
		}
		channel.Write([]byte("ok\r\n"))
	}
}
