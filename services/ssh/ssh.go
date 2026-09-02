// Package main — SSH server over net.wasm TCP (Phase 13.5).
// Logic is host-testable; main.go wires the real kernel in wasip1.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"io"

	lib "kernel.lane/guests/lib"
	"golang.org/x/crypto/ssh"
)

// SSHServer wraps the SSH server config + net listener.
type SSHServer struct {
	config    *ssh.ServerConfig
	listener  *lib.NetListener
	output    io.Writer
	hostKey   *ecdsa.PrivateKey
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

func handleConn(config *ssh.ServerConfig, conn *lib.NetConn) {
	defer conn.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(conn, config)
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
