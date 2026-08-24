// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	packetHeaderSize = 16
	maxDatagramSize  = 1448
	maxNACKRange     = 4096
	maxResendBatch   = 256
	maxHistorySize   = 8192
	nackOpenEnded    = 0x00FFFFFF
	operationSeqMod  = 1 << 16
	packetBurstSize  = 8
	resendStormLimit = 64
	resendStormOps   = 4096
)

const (
	resendStormWindow   = 2 * time.Second
	resendStormCooldown = 2 * time.Second
)

type nackRange struct {
	marker uint32
	from   uint32
	to     uint32
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
	resyncMu           sync.RWMutex
	packetSeq          uint16
	opSeq              uint32
	history            map[uint32][]byte
	burstPackets       int
	burstStarted       time.Time
	lastMissingNACKLog time.Time
	missingNACKs       int
	lastResendLog      time.Time
	resendLogRequests  int
	resendLogOps       int
	resendWindowStart  time.Time
	resendWindowCount  int
	resendWindowOps    int
	nackCooldownUntil  atomic.Int64
	nackRequests       chan nackRange
	done               chan struct{}
	closed             bool
	decoder            inputDecoder
	onInput            func(InputEvent)
	onResync           func()
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

// SetResyncHandler installs a non-blocking session callback that schedules a
// latest full-frame redraw after a resend storm is reset.
func (c *Client) SetResyncHandler(handler func()) {
	c.resyncMu.Lock()
	c.onResync = handler
	c.resyncMu.Unlock()
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
		history:        make(map[uint32][]byte),
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
	return c.SendBatch([]Operation{op})
}

// SendBatch assigns drawing sequences to all operations and packs consecutive
// small operations into as few ALP datagrams as possible. The original
// jOpenRay transport treats a display message as a byte stream of operations;
// sending every Fill/Bounds/Cursor in its own datagram wastes most of the MTU
// and makes simple desktop updates unnecessarily expensive.
func (c *Client) SendBatch(ops []Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	encoded := make([][]byte, 0, len(ops))
	for _, op := range ops {
		if len(op.Bytes)+packetHeaderSize > maxDatagramSize {
			return fmt.Errorf("operation is too large: %d bytes", len(op.Bytes))
		}
		if op.Increment {
			c.opSeq++
		}
		bytes := op.WithSequence(uint16(c.opSeq)).Bytes
		encoded = append(encoded, bytes)
		if op.Increment {
			c.history[c.opSeq] = append([]byte(nil), bytes...)
			if len(c.history) > maxHistorySize {
				delete(c.history, c.opSeq-uint32(maxHistorySize))
			}
		}
	}
	return c.sendEncodedOperationsLocked(encoded)
}

// FlushHistory marks a full-screen refresh boundary. kOpenRay does the same
// before bulk bitmap updates so a later NACK cannot replay stale pixels from a
// superseded frame.
func (c *Client) FlushHistory() {
	c.mu.Lock()
	clear(c.history)
	c.mu.Unlock()
}

func (c *Client) sendLocked(encoded []byte) error {
	return c.sendPacketLocked(encoded)
}

func (c *Client) sendEncodedOperationsLocked(operations [][]byte) error {
	buffer := make([]byte, 0, maxDatagramSize-packetHeaderSize)
	for _, encoded := range operations {
		if len(buffer) > 0 && len(buffer)+len(encoded) > cap(buffer) {
			if err := c.sendPacketLocked(buffer); err != nil {
				return err
			}
			buffer = buffer[:0]
		}
		buffer = append(buffer, encoded...)
	}
	if len(buffer) == 0 {
		return nil
	}
	return c.sendPacketLocked(buffer)
}

func (c *Client) sendPacketLocked(encoded []byte) error {
	c.pacePacketLocked()
	c.packetSeq++
	packet := make([]byte, packetHeaderSize+len(encoded))
	binary.BigEndian.PutUint16(packet[0:2], c.packetSeq)
	binary.BigEndian.PutUint16(packet[2:4], 0)
	binary.BigEndian.PutUint16(packet[4:6], 1)
	copy(packet[packetHeaderSize:], encoded)
	if _, err := c.conn.WriteToUDP(packet, c.remote); err != nil {
		return fmt.Errorf("send display packet: %w", err)
	}
	return nil
}

// pacePacketLocked preserves the configured average packet delay while
// sending small bursts. Sub-millisecond time.Sleep calls are commonly rounded
// up to roughly a millisecond in Linux containers; sleeping after every packet
// therefore turned a 200 us setting into multi-second full-screen refreshes.
func (c *Client) pacePacketLocked() {
	if c.delay <= 0 {
		return
	}
	if c.burstPackets == 0 {
		c.burstStarted = time.Now()
	}
	if c.burstPackets < packetBurstSize {
		c.burstPackets++
		return
	}
	interval := c.delay * packetBurstSize
	if wait := interval - time.Since(c.burstStarted); wait > 0 {
		time.Sleep(wait)
	}
	c.burstStarted = time.Now()
	c.burstPackets = 1
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
			marker := binary.BigEndian.Uint32(packet[offset+4 : offset+8])
			from := binary.BigEndian.Uint32(packet[offset+8 : offset+12])
			to := binary.BigEndian.Uint32(packet[offset+12 : offset+16])
			c.queueResend(marker, from, to)
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

func (c *Client) queueResend(marker, from, to uint32) {
	if time.Now().UnixNano() < c.nackCooldownUntil.Load() {
		return
	}
	if !validNACKRange(from, to) {
		c.log.Warn("ignored invalid NACK range", "from", from, "to", to)
		return
	}
	req := nackRange{marker: marker, from: from, to: to}
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

func validNACKRange(from, to uint32) bool {
	if from > nackOpenEnded || to > nackOpenEnded {
		return false
	}
	if to == nackOpenEnded {
		return true
	}
	if to >= from {
		return to-from+1 <= maxNACKRange
	}
	// A terminal may express one short range across the 16-bit drawing
	// sequence wrap, for example 65530..5.
	if from < operationSeqMod && to < operationSeqMod {
		return operationSeqMod-from+to+1 <= maxNACKRange
	}
	return false
}

func (c *Client) resendLoop() {
	for {
		select {
		case req := <-c.nackRequests:
			if c.resend(req.marker, req.from, req.to) {
				c.drainNACKRequests()
				c.resyncMu.RLock()
				handler := c.onResync
				c.resyncMu.RUnlock()
				if handler != nil {
					handler()
				}
			}
		case <-c.done:
			return
		}
	}
}

func (c *Client) drainNACKRequests() {
	for {
		select {
		case <-c.nackRequests:
		default:
			return
		}
	}
}

// resend reports whether the resend storm circuit breaker fired.
func (c *Client) resend(marker, from, to uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	requestedFrom, requestedTo := from, to
	from = extendOperationSequence(from, c.opSeq)
	if to == nackOpenEnded {
		to = c.opSeq
	} else {
		to = extendOperationSequence(to, c.opSeq)
		if to < from && requestedFrom < operationSeqMod && requestedTo < operationSeqMod {
			to += operationSeqMod
		}
	}
	resent := 0
	lastResent := uint32(0)
	var replay [][]byte
	if from <= to && from <= c.opSeq {
		to = min(to, c.opSeq)
		if to-from+1 > maxResendBatch {
			to = from + maxResendBatch - 1
		}
		if _, ok := c.history[from]; ok && c.recordResendLocked(time.Now(), int(to-from+1)) {
			return true
		}
		for seq := from; seq <= to; seq++ {
			encoded, ok := c.history[seq]
			if !ok {
				break
			}
			replay = append(replay, encoded)
			resent++
			lastResent = seq
		}
	}
	if resent == 0 {
		// Match kOpenRay: if the first requested operation is unavailable,
		// return without a completion packet. Replying to an impossible range
		// makes Sun Ray 2 firmware immediately repeat the request, creating a
		// self-sustaining request/completion loop after a server restart.
		c.logMissingNACKLocked(marker, from, to, requestedFrom, requestedTo)
		return false
	}
	if err := c.sendEncodedOperationsLocked(replay); err != nil {
		c.log.Warn("display resend failed", "from", from, "to", lastResent, "error", err)
		return false
	}
	pad := Pad().WithSequence(uint16(c.opSeq)).Bytes
	// Report the actual replay watermark. For an open-ended or future request,
	// echoing 0xffff/requestedTo makes the terminal restart from the same
	// beginning indefinitely even though only the current prefix was sent.
	status := ResendDone(uint16(lastResent)).WithSequence(uint16(c.opSeq)).Bytes
	// Pad and 0xAC form one completion message in the original protocol. If
	// they are split across datagrams, or 0xAC consumes a new drawing sequence,
	// the terminal can enter a self-sustaining resend loop.
	completion := make([]byte, 0, len(pad)+len(status))
	completion = append(completion, pad...)
	completion = append(completion, status...)
	if err := c.sendLocked(completion); err != nil {
		c.log.Warn("display resend completion failed", "error", err)
		return false
	}
	c.logResendLocked(marker, from, to, lastResent, requestedFrom, requestedTo, resent)
	return false
}

func (c *Client) logResendLocked(marker, from, to, watermark, requestedFrom, requestedTo uint32, resent int) {
	c.resendLogRequests++
	c.resendLogOps += resent
	now := time.Now()
	if !c.lastResendLog.IsZero() && now.Sub(c.lastResendLog) < time.Second {
		return
	}
	c.log.Debug("display operations resent",
		"marker", marker,
		"from", from,
		"to", to,
		"watermark", watermark,
		"requested_from", requestedFrom,
		"requested_to", requestedTo,
		"requests_since_last_log", c.resendLogRequests,
		"operations_since_last_log", c.resendLogOps,
	)
	c.lastResendLog = now
	c.resendLogRequests = 0
	c.resendLogOps = 0
}

func (c *Client) recordResendLocked(now time.Time, operations int) bool {
	if c.resendWindowStart.IsZero() || now.Sub(c.resendWindowStart) > resendStormWindow {
		c.resendWindowStart = now
		c.resendWindowCount = 0
		c.resendWindowOps = 0
	}
	c.resendWindowCount++
	c.resendWindowOps += operations
	if c.resendWindowCount < resendStormLimit && c.resendWindowOps < resendStormOps {
		return false
	}
	requests, replayOperations := c.resendWindowCount, c.resendWindowOps
	clear(c.history)
	c.resendWindowStart = time.Time{}
	c.resendWindowCount = 0
	c.resendWindowOps = 0
	c.nackCooldownUntil.Store(now.Add(resendStormCooldown).UnixNano())
	c.log.Warn("display resend storm reset",
		"requests", requests,
		"operations", replayOperations,
		"cooldown", resendStormCooldown,
		"sequence", c.opSeq,
	)
	return true
}

func (c *Client) logMissingNACKLocked(marker, from, to, requestedFrom, requestedTo uint32) {
	c.missingNACKs++
	now := time.Now()
	if !c.lastMissingNACKLog.IsZero() && now.Sub(c.lastMissingNACKLog) < 5*time.Second {
		return
	}
	c.log.Debug("display resend range is no longer in history",
		"marker", marker,
		"from", from,
		"to", to,
		"requested_from", requestedFrom,
		"requested_to", requestedTo,
		"requests_since_last_log", c.missingNACKs,
	)
	c.lastMissingNACKLog = now
	c.missingNACKs = 0
}

// extendOperationSequence maps the low 16 operation-sequence bits reported by
// the terminal to the nearest epoch of the server's monotonically increasing
// operation count.
func extendOperationSequence(wire, current uint32) uint32 {
	// The operation header contains only 16 sequence bits. Some firmware
	// expands them into a higher epoch in the 32-bit NACK field; after a server
	// restart that epoch may be ahead of the new server counter. Compare the
	// low 16 bits and select the epoch nearest to the current operation.
	candidate := current&^(uint32(operationSeqMod)-1) | wire&(operationSeqMod-1)
	if candidate > current && candidate-current > operationSeqMod/2 {
		if candidate >= operationSeqMod {
			candidate -= operationSeqMod
		}
	} else if current > candidate && current-candidate > operationSeqMod/2 {
		candidate += operationSeqMod
	}
	return candidate
}
