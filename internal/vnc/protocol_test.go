// SPDX-License-Identifier: GPL-2.0-or-later

package vnc

import (
	"encoding/binary"
	"image"
	"image/color"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	major, minor, err := parseVersion("RFB 003.008\n")
	if err != nil || major != 3 || minor != 8 {
		t.Fatalf("parseVersion = %d.%d, %v", major, minor, err)
	}
	if _, _, err := parseVersion("not RFB"); err == nil {
		t.Fatal("expected malformed version to fail")
	}
}

func TestRFB37NoneSecurityDoesNotWaitForResult(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	c := &connection{Conn: clientSide}
	done := make(chan error, 1)
	go func() {
		if _, err := serverSide.Write([]byte{1, securityNone}); err != nil {
			done <- err
			return
		}
		selection := make([]byte, 1)
		_, err := io.ReadFull(serverSide, selection)
		if err == nil && selection[0] != securityNone {
			t.Errorf("security selection = %d", selection[0])
		}
		done <- err
	}()
	if err := c.negotiateSecurity(7, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeAndRawFramebufferUpdate(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	frames := make(chan *image.RGBA, 1)
	c := &connection{
		Conn: clientSide,
		onFrame: func(frame *image.RGBA, changed image.Rectangle, resized bool) error {
			clone := image.NewRGBA(frame.Bounds())
			copy(clone.Pix, frame.Pix)
			frames <- clone
			return nil
		},
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveTestDesktop(serverSide)
	}()

	name, err := c.handshake("")
	if err != nil {
		t.Fatal(err)
	}
	if name != "test" {
		t.Fatalf("desktop name = %q", name)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- c.serve() }()

	select {
	case frame := <-frames:
		if got := frame.RGBAAt(0, 0); got != (color.RGBA{R: 255, A: 255}) {
			t.Fatalf("first pixel = %#v", got)
		}
		if got := frame.RGBAAt(1, 0); got != (color.RGBA{G: 255, A: 255}) {
			t.Fatalf("second pixel = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for framebuffer")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != io.EOF {
		t.Fatalf("serve returned %v, want EOF", err)
	}
}

func serveTestDesktop(conn net.Conn) error {
	defer conn.Close()
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, make([]byte, 12)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{1, securityNone}); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		return err
	}
	if err := binary.Write(conn, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		return err
	}
	serverInit := make([]byte, 24)
	binary.BigEndian.PutUint16(serverInit[0:2], 2)
	binary.BigEndian.PutUint16(serverInit[2:4], 1)
	binary.BigEndian.PutUint32(serverInit[20:24], 4)
	if _, err := conn.Write(append(serverInit, []byte("test")...)); err != nil {
		return err
	}
	for _, length := range []int{20, 16, 10} {
		if _, err := io.ReadFull(conn, make([]byte, length)); err != nil {
			return err
		}
	}

	update := make([]byte, 24)
	update[0] = 0
	binary.BigEndian.PutUint16(update[2:4], 1)
	binary.BigEndian.PutUint16(update[8:10], 2)
	binary.BigEndian.PutUint16(update[10:12], 1)
	binary.BigEndian.PutUint32(update[12:16], uint32(encodingRaw))
	copy(update[16:], []byte{0, 0, 255, 0, 0, 255, 0, 0})
	if _, err := conn.Write(update); err != nil {
		return err
	}
	_, err := io.ReadFull(conn, make([]byte, 10)) // next incremental request
	return err
}

func TestVNCAuthenticationHelpers(t *testing.T) {
	if reverseBits(0x01) != 0x80 || reverseBits(0xF0) != 0x0F {
		t.Fatal("bit reversal failed")
	}
	response, err := vncResponse("password", make([]byte, 16))
	if err != nil || len(response) != 16 {
		t.Fatalf("response length=%d err=%v", len(response), err)
	}
	if chooseSecurity([]byte{securityNone, securityVNC}, true) != securityVNC {
		t.Fatal("password should prefer VNC authentication")
	}
}
