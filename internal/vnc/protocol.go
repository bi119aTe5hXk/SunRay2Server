// SPDX-License-Identifier: GPL-2.0-or-later

// Package vnc implements the client side of the Remote Framebuffer protocol
// needed by SunRay2Server. It intentionally starts with portable, widely
// supported encodings instead of depending on a desktop VNC application.
package vnc

import (
	"context"
	"crypto/des"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"sunray2server/internal/display"
)

const (
	securityNone = 1
	securityVNC  = 2

	encodingRaw         = 0
	encodingCopyRect    = 1
	encodingDesktopSize = -223

	maxDimension   = 8192
	maxDesktopName = 1 << 20
)

type frameHandler func(frame *image.RGBA, changed []display.RegionUpdate, resized bool) error

type connection struct {
	net.Conn
	writeMu    sync.Mutex
	stateMu    sync.RWMutex
	width      int
	height     int
	viewWidth  int
	viewHeight int
	frame      *image.RGBA
	onFrame    frameHandler
}

func dial(ctx context.Context, address, password string, viewWidth, viewHeight int, onFrame frameHandler) (*connection, string, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	netConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, "", fmt.Errorf("dial VNC server: %w", err)
	}
	c := &connection{Conn: netConn, viewWidth: viewWidth, viewHeight: viewHeight, onFrame: onFrame}
	name, err := c.handshake(password)
	if err != nil {
		netConn.Close()
		return nil, "", err
	}
	return c, name, nil
}

func (c *connection) handshake(password string) (string, error) {
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", err
	}
	defer c.SetDeadline(time.Time{})

	version := make([]byte, 12)
	if _, err := io.ReadFull(c, version); err != nil {
		return "", fmt.Errorf("read RFB protocol version: %w", err)
	}
	major, minor, err := parseVersion(string(version))
	if err != nil || major != 3 {
		return "", fmt.Errorf("unsupported RFB version %q", strings.TrimSpace(string(version)))
	}
	selectedMinor := 8
	if minor < 7 {
		selectedMinor = 3
	} else if minor < 8 {
		selectedMinor = 7
	}
	if _, err := fmt.Fprintf(c, "RFB 003.%03d\n", selectedMinor); err != nil {
		return "", fmt.Errorf("write RFB protocol version: %w", err)
	}
	if err := c.negotiateSecurity(selectedMinor, password); err != nil {
		return "", err
	}
	if _, err := c.Write([]byte{1}); err != nil { // shared desktop
		return "", fmt.Errorf("write RFB ClientInit: %w", err)
	}

	header := make([]byte, 24)
	if _, err := io.ReadFull(c, header); err != nil {
		return "", fmt.Errorf("read RFB ServerInit: %w", err)
	}
	width := int(binary.BigEndian.Uint16(header[0:2]))
	height := int(binary.BigEndian.Uint16(header[2:4]))
	if err := validateSize(width, height); err != nil {
		return "", err
	}
	nameLength := binary.BigEndian.Uint32(header[20:24])
	if nameLength > maxDesktopName {
		return "", fmt.Errorf("VNC desktop name is too long: %d", nameLength)
	}
	nameBytes := make([]byte, int(nameLength))
	if _, err := io.ReadFull(c, nameBytes); err != nil {
		return "", fmt.Errorf("read VNC desktop name: %w", err)
	}
	c.setSize(width, height)
	if err := c.setPixelFormat(); err != nil {
		return "", err
	}
	if err := c.setEncodings(); err != nil {
		return "", err
	}
	if err := c.requestUpdate(false); err != nil {
		return "", err
	}
	return string(nameBytes), nil
}

func parseVersion(version string) (int, int, error) {
	if len(version) != 12 || !strings.HasPrefix(version, "RFB ") || version[7] != '.' || version[11] != '\n' {
		return 0, 0, fmt.Errorf("malformed RFB version")
	}
	major, majorErr := strconv.Atoi(version[4:7])
	minor, minorErr := strconv.Atoi(version[8:11])
	if majorErr != nil || minorErr != nil {
		return 0, 0, fmt.Errorf("malformed RFB version")
	}
	return major, minor, nil
}

func (c *connection) negotiateSecurity(minor int, password string) error {
	security := byte(0)
	if minor == 3 {
		var selected uint32
		if err := binary.Read(c, binary.BigEndian, &selected); err != nil {
			return fmt.Errorf("read RFB 3.3 security type: %w", err)
		}
		if selected == 0 {
			return c.securityFailure("VNC server rejected the connection")
		}
		security = byte(selected)
	} else {
		var count [1]byte
		if _, err := io.ReadFull(c, count[:]); err != nil {
			return fmt.Errorf("read VNC security types: %w", err)
		}
		if count[0] == 0 {
			return c.securityFailure("VNC server offered no security type")
		}
		types := make([]byte, int(count[0]))
		if _, err := io.ReadFull(c, types); err != nil {
			return fmt.Errorf("read VNC security type list: %w", err)
		}
		security = chooseSecurity(types, password != "")
		if security == 0 {
			return fmt.Errorf("VNC server offers unsupported security types %v", types)
		}
		if _, err := c.Write([]byte{security}); err != nil {
			return fmt.Errorf("select VNC security type: %w", err)
		}
	}

	switch security {
	case securityNone:
		// RFB 3.8 added SecurityResult for the None type. RFB 3.7 proceeds
		// directly to ClientInit, so reading a result there would deadlock.
		if minor >= 8 {
			return c.readSecurityResult(minor)
		}
		return nil
	case securityVNC:
		if password == "" {
			return fmt.Errorf("VNC password authentication required but no password was configured")
		}
		challenge := make([]byte, 16)
		if _, err := io.ReadFull(c, challenge); err != nil {
			return fmt.Errorf("read VNC authentication challenge: %w", err)
		}
		response, err := vncResponse(password, challenge)
		if err != nil {
			return err
		}
		if _, err := c.Write(response); err != nil {
			return fmt.Errorf("write VNC authentication response: %w", err)
		}
		return c.readSecurityResult(minor)
	default:
		return fmt.Errorf("unsupported VNC security type %d", security)
	}
}

func chooseSecurity(types []byte, hasPassword bool) byte {
	if hasPassword {
		for _, security := range types {
			if security == securityVNC {
				return securityVNC
			}
		}
	}
	for _, security := range types {
		if security == securityNone {
			return securityNone
		}
	}
	return 0
}

func (c *connection) readSecurityResult(minor int) error {
	var result uint32
	if err := binary.Read(c, binary.BigEndian, &result); err != nil {
		return fmt.Errorf("read VNC security result: %w", err)
	}
	if result == 0 {
		return nil
	}
	if minor >= 8 {
		return c.securityFailure(fmt.Sprintf("VNC authentication failed with result %d", result))
	}
	return fmt.Errorf("VNC authentication failed with result %d", result)
}

func (c *connection) securityFailure(prefix string) error {
	var length uint32
	if err := binary.Read(c, binary.BigEndian, &length); err != nil {
		return fmt.Errorf("%s", prefix)
	}
	if length > maxDesktopName {
		return fmt.Errorf("%s", prefix)
	}
	reason := make([]byte, int(length))
	if _, err := io.ReadFull(c, reason); err != nil {
		return fmt.Errorf("%s", prefix)
	}
	return fmt.Errorf("%s: %s", prefix, strings.TrimSpace(string(reason)))
}

func vncResponse(password string, challenge []byte) ([]byte, error) {
	if len(challenge) != 16 {
		return nil, fmt.Errorf("invalid VNC challenge length %d", len(challenge))
	}
	var key [8]byte
	copy(key[:], []byte(password))
	for i := range key {
		key[i] = reverseBits(key[i])
	}
	cipher, err := des.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize VNC authentication: %w", err)
	}
	response := make([]byte, 16)
	cipher.Encrypt(response[:8], challenge[:8])
	cipher.Encrypt(response[8:], challenge[8:])
	return response, nil
}

func reverseBits(value byte) byte {
	value = value>>4 | value<<4
	value = (value&0xCC)>>2 | (value&0x33)<<2
	return (value&0xAA)>>1 | (value&0x55)<<1
}

func validateSize(width, height int) error {
	if width < 1 || height < 1 || width > maxDimension || height > maxDimension {
		return fmt.Errorf("invalid VNC framebuffer size %dx%d", width, height)
	}
	return nil
}

func (c *connection) setSize(width, height int) {
	c.stateMu.Lock()
	c.width, c.height = width, height
	c.frame = image.NewRGBA(image.Rect(0, 0, width, height))
	c.stateMu.Unlock()
}

func (c *connection) size() (int, int) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.width, c.height
}

func (c *connection) setPixelFormat() error {
	message := make([]byte, 20)
	message[0] = 0
	message[4] = 32 // bits per pixel
	message[5] = 24 // depth
	message[6] = 0  // little endian
	message[7] = 1  // true color
	binary.BigEndian.PutUint16(message[8:10], 255)
	binary.BigEndian.PutUint16(message[10:12], 255)
	binary.BigEndian.PutUint16(message[12:14], 255)
	message[14], message[15], message[16] = 16, 8, 0
	return c.writeMessage(message, "set VNC pixel format")
}

func (c *connection) setEncodings() error {
	// CopyRect is the highest-value encoding for a Sun Ray: the downstream
	// ALP transport can reproduce it as one native 16-byte framebuffer copy.
	encodings := []int32{encodingCopyRect, encodingRaw, encodingDesktopSize}
	message := make([]byte, 4+4*len(encodings))
	message[0] = 2
	binary.BigEndian.PutUint16(message[2:4], uint16(len(encodings)))
	for i, encoding := range encodings {
		binary.BigEndian.PutUint32(message[4+i*4:8+i*4], uint32(encoding))
	}
	return c.writeMessage(message, "set VNC encodings")
}

func (c *connection) requestUpdate(incremental bool) error {
	width, height := c.requestSize()
	message := make([]byte, 10)
	message[0] = 3
	if incremental {
		message[1] = 1
	}
	binary.BigEndian.PutUint16(message[6:8], uint16(width))
	binary.BigEndian.PutUint16(message[8:10], uint16(height))
	return c.writeMessage(message, "request VNC framebuffer update")
}

func (c *connection) requestSize() (int, int) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	width, height := c.width, c.height
	if c.viewWidth > 0 {
		width = min(width, c.viewWidth)
	}
	if c.viewHeight > 0 {
		height = min(height, c.viewHeight)
	}
	return width, height
}

func (c *connection) visibleBounds() image.Rectangle {
	width, height := c.requestSize()
	return image.Rect(0, 0, width, height)
}

func (c *connection) writeMessage(message []byte, action string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.Write(message); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

func (c *connection) serve() error {
	for {
		var messageType [1]byte
		if _, err := io.ReadFull(c, messageType[:]); err != nil {
			return err
		}
		switch messageType[0] {
		case 0:
			if err := c.readFramebufferUpdate(); err != nil {
				return err
			}
			if err := c.requestUpdate(true); err != nil {
				return err
			}
		case 1:
			if err := c.skipColorMap(); err != nil {
				return err
			}
		case 2: // bell
		case 3:
			if err := c.skipServerCutText(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported VNC server message type %d", messageType[0])
		}
	}
}

func (c *connection) readFramebufferUpdate() error {
	header := make([]byte, 3)
	if _, err := io.ReadFull(c, header); err != nil {
		return fmt.Errorf("read VNC framebuffer update header: %w", err)
	}
	count := int(binary.BigEndian.Uint16(header[1:3]))
	changed := make([]display.RegionUpdate, 0, count)
	resized := false
	for range count {
		rectHeader := make([]byte, 12)
		if _, err := io.ReadFull(c, rectHeader); err != nil {
			return fmt.Errorf("read VNC rectangle header: %w", err)
		}
		x := int(binary.BigEndian.Uint16(rectHeader[0:2]))
		y := int(binary.BigEndian.Uint16(rectHeader[2:4]))
		width := int(binary.BigEndian.Uint16(rectHeader[4:6]))
		height := int(binary.BigEndian.Uint16(rectHeader[6:8]))
		encoding := int32(binary.BigEndian.Uint32(rectHeader[8:12]))
		rect := image.Rect(x, y, x+width, y+height)

		switch encoding {
		case encodingRaw:
			if err := c.readRaw(rect); err != nil {
				return err
			}
			changed = appendVisible(changed, display.RegionUpdate{Rectangle: rect}, c.visibleBounds())
		case encodingCopyRect:
			source, err := c.readCopyRect(rect)
			if err != nil {
				return err
			}
			changed = appendVisible(changed, display.RegionUpdate{Rectangle: rect, CopySource: source, Copy: true}, c.visibleBounds())
		case encodingDesktopSize:
			if err := validateSize(width, height); err != nil {
				return err
			}
			c.setSize(width, height)
			changed = append(changed[:0], display.RegionUpdate{Rectangle: c.visibleBounds()})
			resized = true
		default:
			return fmt.Errorf("VNC server used unrequested encoding %d", encoding)
		}
	}
	if len(changed) == 0 || c.onFrame == nil {
		return nil
	}
	c.stateMu.RLock()
	frame := c.frame
	err := c.onFrame(frame, changed, resized)
	c.stateMu.RUnlock()
	return err
}

func (c *connection) readRaw(rect image.Rectangle) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.frame == nil || !rect.In(c.frame.Bounds()) || rect.Empty() {
		return fmt.Errorf("invalid raw VNC rectangle %v for framebuffer %v", rect, c.frame.Bounds())
	}
	row := make([]byte, rect.Dx()*4)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		if _, err := io.ReadFull(c, row); err != nil {
			return fmt.Errorf("read raw VNC pixels: %w", err)
		}
		destination := c.frame.PixOffset(rect.Min.X, y)
		for x := 0; x < rect.Dx(); x++ {
			pos := x * 4
			c.frame.Pix[destination] = row[pos+2]
			c.frame.Pix[destination+1] = row[pos+1]
			c.frame.Pix[destination+2] = row[pos]
			c.frame.Pix[destination+3] = 255
			destination += 4
		}
	}
	return nil
}

func (c *connection) readCopyRect(rect image.Rectangle) (image.Point, error) {
	var source [4]byte
	if _, err := io.ReadFull(c, source[:]); err != nil {
		return image.Point{}, fmt.Errorf("read VNC CopyRect source: %w", err)
	}
	src := image.Pt(int(binary.BigEndian.Uint16(source[0:2])), int(binary.BigEndian.Uint16(source[2:4])))
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.frame == nil || !rect.In(c.frame.Bounds()) {
		return image.Point{}, fmt.Errorf("invalid VNC CopyRect destination %v", rect)
	}
	sourceRect := image.Rectangle{Min: src, Max: src.Add(rect.Size())}
	if !sourceRect.In(c.frame.Bounds()) {
		return image.Point{}, fmt.Errorf("invalid VNC CopyRect source %v", sourceRect)
	}
	temporary := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(temporary, temporary.Bounds(), c.frame, sourceRect.Min, draw.Src)
	draw.Draw(c.frame, rect, temporary, image.Point{}, draw.Src)
	return src, nil
}

func (c *connection) skipColorMap() error {
	header := make([]byte, 5)
	if _, err := io.ReadFull(c, header); err != nil {
		return err
	}
	count := int64(binary.BigEndian.Uint16(header[3:5]))
	_, err := io.CopyN(io.Discard, c, count*6)
	return err
}

func (c *connection) skipServerCutText() error {
	header := make([]byte, 7)
	if _, err := io.ReadFull(c, header); err != nil {
		return err
	}
	length := int64(binary.BigEndian.Uint32(header[3:7]))
	if length > maxDesktopName {
		return fmt.Errorf("VNC clipboard text is too long: %d", length)
	}
	_, err := io.CopyN(io.Discard, c, length)
	return err
}

func appendVisible(rectangles []display.RegionUpdate, changed display.RegionUpdate, visible image.Rectangle) []display.RegionUpdate {
	clipped := changed.Rectangle.Intersect(visible)
	if clipped.Empty() {
		return rectangles
	}
	if clipped != changed.Rectangle {
		// A clipped CopyRect is no longer a direct source/destination mapping;
		// use the already updated framebuffer pixels instead.
		changed.Copy = false
	}
	changed.Rectangle = clipped
	return append(rectangles, changed)
}

func (c *connection) sendKey(keysym uint32, pressed bool) error {
	message := make([]byte, 8)
	message[0] = 4
	if pressed {
		message[1] = 1
	}
	binary.BigEndian.PutUint32(message[4:8], keysym)
	return c.writeMessage(message, "send VNC key event")
}

func (c *connection) sendPointer(buttons uint8, x, y uint16) error {
	message := make([]byte, 6)
	message[0], message[1] = 5, buttons
	binary.BigEndian.PutUint16(message[2:4], x)
	binary.BigEndian.PutUint16(message[4:6], y)
	return c.writeMessage(message, "send VNC pointer event")
}
