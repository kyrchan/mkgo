package main

import (
	"errors"
	"io"

	lib "kernel.lane/guests/lib"
)

// NetBridge connects the offload transport (raw 802.3 frames) to the §10 "net"
// socket service. It demuxes inbound Ethernet frames to the net socket and
// wraps outbound socket data into Ethernet frames sent to the offload module.
type NetBridge struct {
	net   *lib.NetClient
	off   OffloadTransport
	sock  uint16
	mac   MAC
	apMAC MAC
	ip    IP4
	gwIP  IP4
}

// NewNetBridge creates a UDP-over-offload bridge on the net service.
func newNetBridge(nc *lib.NetClient, off OffloadTransport, mac, apMAC MAC, ip, gwIP IP4) (*NetBridge, error) {
	sock, err := nc.OpenUDP(0)
	if err != nil {
		return nil, errors.New("wlan: netbridge open udp: " + err.Error())
	}
	if err := nc.Connect(sock, gwIP, 0); err != nil {
		return nil, errors.New("wlan: netbridge connect: " + err.Error())
	}
	return &NetBridge{
		net:   nc,
		off:   off,
		sock:  sock,
		mac:   mac,
		apMAC: apMAC,
		ip:    ip,
		gwIP:  gwIP,
	}, nil
}

// Close tears down the underlying socket.
func (b *NetBridge) Close() error {
	_ = b.net.Close(b.sock)
	return nil
}

// ForwardOnce pumps one direction: either an inbound frame -> net socket,
// or a pending net recv -> outbound frame. Returns true if either fired.
func (b *NetBridge) ForwardOnce() bool {
	if b.recvFromOffload() {
		return true
	}
	return b.recvFromNet()
}

// recvFromOffload pulls a data frame from the offload transport, unwraps the
// UDP payload, and injects it into the net socket.
func (b *NetBridge) recvFromOffload() bool {
	f, ok := b.off.RecvFrame()
	if !ok || len(f) < 1 {
		return false
	}
	if f[0] != FrameTypeData {
		return false
	}
	_, _, udp, err := parseUDPEth(f[1:])
	if err != nil {
		return false
	}
	b.net.Send(b.sock, udp.Data)
	return true
}

// recvFromNet pulls a datagram from the net socket and wraps it as an
// 802.3 data frame to the AP.
func (b *NetBridge) recvFromNet() bool {
	buf := make([]byte, 1500)
	n, err := b.net.Recv(b.sock, buf)
	if err != nil || n == 0 {
		return false
	}
	udp := buildUDP(5000, 1234, buf[:n])
	ip, err := buildIPv4(b.ip, b.gwIP, IP4ProtoUDP, udp)
	if err != nil {
		return false
	}
	eth := buildEth(b.apMAC, b.mac, EthTypeIPv4, ip)
	b.off.SendFrame(append([]byte{FrameTypeData}, eth...))
	return true
}

var _ io.Closer = (*NetBridge)(nil)
