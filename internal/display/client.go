// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	packetHeaderSize = 16
	maxDatagramSize  = 1448
	maxNACKRange     = 4096
)

// Client owns the bidirectional UDP display channel for one Sun Ray.
type Client struct {
	conn   *net.UDPConn
	remote *net.UDPAddr
	delay  time.Duration
	log    *slog.Logger

	mu        sync.Mutex
	packetSeq uint16
	opSeq     uint16
	history   map[uint16][]byte
	closed    bool
}

func Open(remoteIP net.IP, remotePort int, delay time.Duration, logger *slog.Logger) (*Client, error) {
	if remoteIP == nil || remotePort < 1 || remotePort > 65535 {
		return nil, fmt.Errorf("invalid display destination %v:%d", remoteIP, remotePort)
	}
	remoteIP = remoteIP.To4()
	if remoteIP == nil {
		return nil, fmt.Errorf("Sun Ray display currently requires an IPv4 destination")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, fmt.Errorf("open display UDP socket: %w", err)
	}
	c := &Client{
		conn:    conn,
		remote:  &net.UDPAddr{IP: remoteIP, Port: remotePort},
		delay:   delay,
		log:     logger,
		history: make(map[uint16][]byte),
	}
	go c.readLoop()
	return c, nil
}

func (c *Client) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}

func (c *Client) Send(op Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if len(op.Bytes)+packetHeaderSize > maxDatagramSize {
		return fmt.Errorf("operation is too large: %d bytes", len(op.Bytes))
	}
	if op.Increment {
		c.opSeq++
	}
	encoded := op.WithSequence(c.opSeq).Bytes
	if op.Increment {
		c.history[c.opSeq] = append([]byte(nil), encoded...)
	}
	return c.sendLocked(encoded)
}

func (c *Client) sendLocked(encoded []byte) error {
	c.packetSeq++
	packet := make([]byte, packetHeaderSize+len(encoded))
	binary.BigEndian.PutUint16(packet[0:2], c.packetSeq)
	binary.BigEndian.PutUint16(packet[2:4], 0)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	copy(packet[packetHeaderSize:], encoded)
	if _, err := c.conn.WriteToUDP(packet, c.remote); err != nil {
		return fmt.Errorf("send display packet: %w", err)
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return nil
}

func (c *Client) readLoop() {
	buffer := make([]byte, 1500)
	for {
		n, from, err := c.conn.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				c.log.Warn("display receive failed", "error", err)
			}
			return
		}
		if !from.IP.Equal(c.remote.IP) {
			c.log.Warn("ignored display packet from unexpected address", "from", from)
			continue
		}
		c.handlePacket(buffer[:n])
	}
}

func (c *Client) handlePacket(packet []byte) {
	if len(packet) < packetHeaderSize {
		c.log.Warn("ignored short display packet", "bytes", len(packet))
		return
	}
	if len(packet) == packetHeaderSize {
		if err := c.Send(Pad()); err != nil && !errors.Is(err, net.ErrClosed) {
			c.log.Warn("failed to send display keepalive", "error", err)
		}
		return
	}

	// C4 is the terminal's negative acknowledgement operation. Its second
	// and third 32-bit values identify the missing display operation range.
	for offset := packetHeaderSize; offset+16 <= len(packet); {
		opcode := packet[offset]
		if opcode == 0xC4 {
			from := binary.BigEndian.Uint32(packet[offset+8 : offset+12])
			to := binary.BigEndian.Uint32(packet[offset+12 : offset+16])
			c.resend(from, to)
			return
		}
		// Input packets are only logged in this first milestone. Their complete
		// decoding will be connected to SSH/VNC/RDP in the next phase.
		c.log.Debug("received display input", "opcode", fmt.Sprintf("0x%02x", opcode), "bytes", len(packet)-offset)
		return
	}
}

func (c *Client) resend(from, to uint32) {
	if to < from || to-from+1 > maxNACKRange {
		c.log.Warn("ignored invalid NACK range", "from", from, "to", to)
		return
	}
	c.log.Debug("resending display operations", "from", from, "to", to)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	for seq := from; seq <= to; seq++ {
		encoded, ok := c.history[uint16(seq)]
		if !ok {
			continue
		}
		if err := c.sendLocked(encoded); err != nil {
			c.log.Warn("display resend failed", "sequence", seq, "error", err)
			return
		}
	}
}
