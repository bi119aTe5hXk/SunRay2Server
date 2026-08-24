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
	maxHistorySize   = 8192
)

type nackRange struct {
	from uint32
	to   uint32
}

// Client owns the bidirectional UDP display channel for one Sun Ray.
type Client struct {
	conn           *net.UDPConn
	remote         *net.UDPAddr
	delay          time.Duration
	log            *slog.Logger
	logInputEvents bool

	mu                 sync.Mutex
	renderMu           sync.Mutex
	inputMu            sync.Mutex
	packetSeq          uint16
	opSeq              uint16
	history            map[uint16][]byte
	nackRequests       chan nackRange
	done               chan struct{}
	closed             bool
	decoder            inputDecoder
	onInput            func(InputEvent)
	lastPointerLog     time.Time
	lastPointerButtons uint8
}

// SetInputHandler installs the consumer used by a future SSH, VNC or RDP
// adapter. Passing nil keeps decoding and debug logging enabled without
// forwarding events.
func (c *Client) SetInputHandler(handler func(InputEvent)) {
	c.inputMu.Lock()
	c.onInput = handler
	c.inputMu.Unlock()
}

func Open(remoteIP net.IP, remotePort int, delay time.Duration, logInputEvents bool, logger *slog.Logger) (*Client, error) {
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
		conn:           conn,
		remote:         &net.UDPAddr{IP: remoteIP, Port: remotePort},
		delay:          delay,
		log:            logger,
		logInputEvents: logInputEvents,
		history:        make(map[uint16][]byte),
		nackRequests:   make(chan nackRange, 1),
		done:           make(chan struct{}),
	}
	// Sequence zero is a valid resend target even though normal drawing
	// operations start at one. Keep a pad as the initial history anchor.
	c.history[0] = Pad().WithSequence(0).Bytes
	go c.readLoop()
	go c.resendLoop()
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
	close(c.done)
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
		if len(c.history) > maxHistorySize {
			delete(c.history, c.opSeq-uint16(maxHistorySize))
		}
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

	for offset := packetHeaderSize; offset < len(packet); {
		opcode := packet[offset]
		switch opcode {
		case 0xC1:
			if offset+16 > len(packet) {
				c.log.Warn("ignored short keyboard input", "bytes", len(packet)-offset)
				return
			}
			c.inputMu.Lock()
			events, err := c.decoder.keyboard(packet[offset : offset+16])
			c.inputMu.Unlock()
			if err != nil {
				c.log.Warn("ignored invalid keyboard input", "error", err)
				return
			}
			for _, event := range events {
				c.dispatchInput(event)
			}
			offset += 16
		case 0xC2:
			if offset+12 > len(packet) {
				c.log.Warn("ignored short pointer input", "bytes", len(packet)-offset)
				return
			}
			c.inputMu.Lock()
			event, changed, err := c.decoder.pointer(packet[offset : offset+12])
			c.inputMu.Unlock()
			if err != nil {
				c.log.Warn("ignored invalid pointer input", "error", err)
				return
			}
			if changed {
				c.dispatchInput(event)
			}
			offset += 12
		case 0xC4:
			if offset+16 > len(packet) {
				c.log.Warn("ignored short display NACK", "bytes", len(packet)-offset)
				return
			}
			from := binary.BigEndian.Uint32(packet[offset+8 : offset+12])
			to := binary.BigEndian.Uint32(packet[offset+12 : offset+16])
			c.queueResend(from, to)
			return
		case 0xC7:
			// Rectangle/geometry report used during display setup.
			if offset+28 > len(packet) {
				return
			}
			offset += 28
		default:
			c.log.Debug("received unhandled display input", "opcode", fmt.Sprintf("0x%02x", opcode), "bytes", len(packet)-offset)
			return
		}
	}
}

func (c *Client) dispatchInput(event InputEvent) {
	c.inputMu.Lock()
	handler := c.onInput
	c.inputMu.Unlock()
	if handler != nil {
		handler(event)
	}
	if !c.logInputEvents {
		return
	}
	if event.Kind == InputKey {
		c.log.Debug("keyboard input", "hid", fmt.Sprintf("0x%02x", event.HID), "pressed", event.Pressed, "modifiers", fmt.Sprintf("0x%02x", event.Modifiers))
		return
	}
	now := time.Now()
	if event.Buttons != c.lastPointerButtons || now.Sub(c.lastPointerLog) >= 250*time.Millisecond {
		c.log.Debug("pointer input", "x", event.X, "y", event.Y, "buttons", fmt.Sprintf("0x%02x", event.Buttons))
		c.lastPointerLog = now
		c.lastPointerButtons = event.Buttons
	}
}

func (c *Client) queueResend(from, to uint32) {
	if from > uint32(^uint16(0)) || to > uint32(^uint16(0)) || to < from || to-from+1 > maxNACKRange {
		c.log.Warn("ignored invalid NACK range", "from", from, "to", to)
		return
	}
	req := nackRange{from: from, to: to}
	select {
	case c.nackRequests <- req:
		return
	default:
	}
	// Keep only the newest pending request while a replay is in progress. This
	// prevents repeated NACKs from building an unbounded replay backlog.
	select {
	case <-c.nackRequests:
	default:
	}
	select {
	case c.nackRequests <- req:
	default:
	}
}

func (c *Client) resendLoop() {
	for {
		select {
		case req := <-c.nackRequests:
			c.resend(req.from, req.to)
		case <-c.done:
			return
		}
	}
}

func (c *Client) resend(from, to uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.log.Debug("resending display operations", "from", from, "to", to)
	resent := 0
	for seq := from; seq <= to; seq++ {
		encoded, ok := c.history[uint16(seq)]
		if !ok {
			break
		}
		if err := c.sendLocked(encoded); err != nil {
			c.log.Warn("display resend failed", "sequence", seq, "error", err)
			return
		}
		resent++
	}
	if resent == 0 {
		c.log.Debug("display resend range is no longer in history", "from", from, "to", to)
		return
	}
	pad := Pad().WithSequence(c.opSeq).Bytes
	status := ResendDone(uint16(to)).WithSequence(c.opSeq).Bytes
	// Pad and 0xAC form one completion message in the original protocol. If
	// they are split across datagrams, or 0xAC consumes a new drawing sequence,
	// the terminal can enter a self-sustaining resend loop.
	completion := make([]byte, 0, len(pad)+len(status))
	completion = append(completion, pad...)
	completion = append(completion, status...)
	if err := c.sendLocked(completion); err != nil {
		c.log.Warn("display resend completion failed", "error", err)
		return
	}
	c.log.Debug("display operation resend complete", "from", from, "to", to, "resent", resent)
}
