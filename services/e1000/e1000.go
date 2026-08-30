// e1000.go — E1000 Gigabit Ethernet driver core (Phase 13, ABI §12 VFIO).
//
// Core logic is host-testable: MMIO registers are accessed through a BAR
// abstraction so the same driver code runs against a mock for unit tests
// and against the real VFIO-mapped BAR window in wasip1.
package main

import (
	"errors"
	"fmt"
)

// --- E1000 register offsets (relative to BAR0) ---
const (
	e1000RegCtrl        = 0x0000 // Device Control
	e1000RegStatus      = 0x0008 // Device Status
	e1000RegEerd        = 0x0014 // EEPROM Read Register (32-bit)
	e1000RegMacLow      = 0x0020 // Receive Address Low (RAL0)
	e1000RegMacHigh     = 0x0024 // Receive Address High (RAH0)
	e1000RegTxDescBase  = 0x3800 // TX Descriptor Base
	e1000RegTxDtlen     = 0x3808 // TX Descriptor Length
	e1000RegTxDt      = 0x3810 // TX Descriptor Tail
	e1000RegTxStat      = 0x3820 // TX Status (read) / TDT (write)
	e1000RegRcvDescBase = 0x2800 // RX Descriptor Base
	e1000RegRcvDtlen    = 0x2808 // RX Descriptor Length
	e1000RegRcvDt       = 0x2810 // RX Descriptor Tail
	e1000RegRctl        = 0x0100 // Receive Control
	e1000RegRcvExt      = 0x0108 // Receive Control Extended
	e1k3IntRx           = 0x0100 // interrupt status (alias)
	e1000RegIcr          = 0x015C // Interrupt Cause Read
	e1000RegIms          = 0x015C // Interrupt Mask Set — same offset, write 1s
	e1000RegIgc          = 0x015B // Interrupt Mask Clear
)

// --- TX Descriptor ---
type txDesc struct {
	bufferAddr uint64
	length     uint16
	csumOffset uint8
	// command bits: EOP, IFCS, IC, RS, RSV, DEXT, VLE, EN
	command    uint8
	status     uint8 // DD, EC, LC, LU, CE, TF
	css        uint8
	special    uint32
}

// --- RX Descriptor ---
type rxDesc struct {
	bufferAddr uint64
	length     uint16
	checksum   uint16
	status     uint16 // DD, EOP, etc.
	errors     uint8
	special    uint8
}

// --- TX descriptor command bits ---
const (
	txDescEOF = 1 << 0 // End of Packet
	txDescIFCS = 1 << 1 // Insert Frame Check Sequence
	txDescIC  = 1 << 2 // Interrupt on Complete
	txDescRS  = 1 << 3 // Report Status
	txDescVLE = 1 << 7 // VLAN Packet Enable
	txDescE   = 1 << 0 // Descriptor Done (status)
)

// --- RX descriptor status bits ---
const (
	rxDescDD  = 1 << 0 // Descriptor Done
	rxDescEOP = 1 << 1 // End of Packet
)

// --- CTRL bits ---
const (
	e1000CtrlLrst = 1 << 3 // Link Reset
	e1000CtrlSwt  = 1 << 3 // Software Reset (same bit in CTRL_EXT)
	e1000CtrlRe   = 1 << 4 // Receiver Enable
	e1000CtrlTe   = 1 << 3 // Transmit Enable
)

// --- STATUS bits ---
const (
	e1000StatusLanDone = 1 << 0 // Link Up
)

// --- RCTL bits ---
const (
	e1000RctlEn     = 1 << 1 // Receiver Enable
	e1000RctlSdu3 = 1 << 2 // RX Descriptor Update
	e1000RctlBAM   = 1 << 4 // Broadcast Accept Mode
	e1000RctlBSE   = 1 << 5 // Broadcast Suspend enable (promisc? check)
	e1000RctlUPE   = 1 << 3 // Uni/multicast promiscuous
	e1000RctlSEC   = 1 << 5 // 
	e1000RctlDMSE  = 1 << 6 // Discard Pause
	e1000RctlHTR   = 1 << 7 // Hash for exact
	e1000RctlIL    = 1 << 7 // 
	e1000RctlBam   = 1 << 8 // Broadcast Accept
)

// BAR is the MMIO abstraction over a PCI BAR window. In the real driver,
// this is the memory returned by kern_pci_map_bar; in tests it is a mock.
type BAR interface {
	Read32(off int) uint32
	Write32(off int, v uint32)
	Read16(off int) uint16
	Write16(off int, v uint16)
	// Region returns a []byte view into the BAR backing store for descriptor rings.
	Region(off, size int) []byte
}

// E1000Driver is the host-testable E1000 driver core.
type E1000Driver struct {
	bar   BAR
	mac   [6]byte
	txBuf []byte // TX packet buffer area
	rxBuf []byte // RX packet buffer area
}

// NewE1000 creates a driver backed by a BAR.
func NewE1000(bar BAR) *E1000Driver {
	return &E1000Driver{
		bar:   bar,
		txBuf: make([]byte, 4096),
		rxBuf: make([]byte, 8192),
	}
}

// ReadMAC reads the station MAC address from RAL0/RAH0.
func (d *E1000Driver) ReadMAC() ([6]byte, error) {
	lo := d.bar.Read32(e1000RegMacLow)
	hi := d.bar.Read32(e1000RegMacHigh)
	var mac [6]byte
	mac[0] = byte(lo)
	mac[1] = byte(lo >> 8)
	mac[2] = byte(lo >> 16)
	mac[3] = byte(lo >> 24)
	mac[4] = byte(hi)
	mac[5] = byte(hi >> 8)
	if lo == 0 && hi == 0 {
		return mac, errors.New("e1000: MAC address is zero (device not initialized)")
	}
	return mac, nil
}

// ReadEEPROM reads a 16-bit word from the serial EEPROM via EERD.
func (d *E1000Driver) ReadEEPROM(addr uint16) (uint16, error) {
	start := uint32(1) | (uint32(addr) << 8) // EERD.Start=1, EERD.Addr
	d.bar.Write32(e1000RegEerd, start)
	// Poll for done bit
	for i := 0; i < 100000; i++ {
		val := d.bar.Read32(e1000RegEerd)
		if val&0x10 != 0 { // EERD.Done
			return uint16(val >> 16) & 0xFFFF, nil
		}
	}
	return 0, errors.New("e1000: EEPROM read timeout")
}

// LinkUp polls the STATUS register for link negotiation completion.
func (d *E1000Driver) LinkUp(timeout int) bool {
	for i := 0; i < timeout; i++ {
		if d.bar.Read32(e1000RegStatus)&e1000StatusLanDone != 0 {
			return true
		}
	}
	return false
}

// Init performs basic controller initialization.
func (d *E1000Driver) Init() error {
	// Read MAC
	mac, err := d.ReadMAC()
	if err == nil {
		d.mac = mac
	}
	// Check EEPROM
	_, e := d.ReadEEPROM(0)
	if e != nil {
		// EEPROM may not be present on all emulators; that's OK
	}
	// Wait for link
	if !d.LinkUp(1000) {
		return errors.New("e1000: link not up after timeout")
	}
	return nil
}

// TXQueue is the transmit descriptor ring.
type TXQueue struct {
	driver   *E1000Driver
	base     int
	count    int
	tail     int
	buffers  [][]byte
}

// SetupTX configures the TX descriptor ring.
func (d *E1000Driver) SetupTX(base int, count int) *TXQueue {
	q := &TXQueue{driver: d, base: base, count: count}
	// Write base address (split lo/hi)
	physAddr := uint64(base) // identity-mapped in this kernel
	d.bar.Write32(e1000RegTxDescBase, uint32(physAddr&0xFFFFFFFF))
	// High bits (for 64-bit BARs)
	d.bar.Write32(e1000RegTxDescBase+4, uint32(physAddr>>32))
	d.bar.Write32(e1000RegTxDtlen, uint32(count*16))
	d.bar.Write32(e1000RegTxDt, 0) // TDT = 0
	return q
}

// Transmit enqueues a packet into the TX ring.
func (q *TXQueue) Transmit(pkt []byte) bool {
	desc := make([]byte, 16)
	// bufferAddr (placeholder — real hardware needs physical address)
	// length
	desc[8] = byte(len(pkt))
	desc[9] = byte(len(pkt) >> 8)
	// command: EOP + IFCS + RS
	desc[10] = txDescEOF | txDescIFCS | txDescRS
	// Copy packet into TX buffer area
	off := q.tail * 256
	copy(q.driver.txBuf[off:], pkt)
	// Write descriptor to ring
	ring := q.driver.bar.Region(q.base+q.tail*16, 16)
	copy(ring, desc)
	// Advance TDT
	q.tail = (q.tail + 1) % q.count
	q.driver.bar.Write32(e1000RegTxDt, uint32(q.tail))
	return true
}

// RXQueue is the receive descriptor ring.
type RXQueue struct {
	driver *E1000Driver
	base   int
	count  int
	head   int
}

// SetupRX configures the RX descriptor ring and pre-fills descriptors.
func (d *E1000Driver) SetupRX(base int, count int) *RXQueue {
	q := &RXQueue{driver: d, base: base, count: count}
	physAddr := uint64(base)
	d.bar.Write32(e1000RegRcvDescBase, uint32(physAddr&0xFFFFFFFF))
	d.bar.Write32(e1000RegRcvDescBase+4, uint32(physAddr>>32))
	d.bar.Write32(e1000RegRcvDtlen, uint32(count*16))
	// Pre-fill descriptors
	for i := 0; i < count; i++ {
		off := base + i*16
		desc := d.bar.Region(off, 16)
		// bufferAddr (placeholder)
		// length
		desc[2] = byte(256 & 0xFF)
		desc[3] = byte((256 >> 8) & 0xFF)
	}
	// Set RDT to last descriptor
	d.bar.Write32(e1000RegRcvDt, uint32(count-1))
	// Enable receiver
	d.bar.Write32(e1000RegRctl, e1000RctlBam|e1000RctlEn)
	return q
}

// PollRecv checks for received packets. Returns packet data or nil.
func (q *RXQueue) PollRecv() []byte {
	off := q.base + q.head*16
	desc := q.driver.bar.Region(off, 16)
	status := uint16(desc[12]) | uint16(desc[13])<<8
	if status&rxDescDD == 0 {
		return nil // no packet ready
	}
	length := uint16(desc[4]) | uint16(desc[5])<<8
	pkt := make([]byte, length)
	// Copy from RX buffer (placeholder offset)
	descOff := q.head * 256
	copy(pkt, q.driver.rxBuf[descOff:descOff+int(length)])
	// Clear DD bit, advance head
	desc[12] = 0
	desc[13] = 0
	q.head = (q.head + 1) % q.count
	return pkt
}

// String formats a MAC address.
func macString(mac [6]byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5])
}
