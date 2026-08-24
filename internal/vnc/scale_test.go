// SPDX-License-Identifier: GPL-2.0-or-later

package vnc

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"io"
	"net"
	"testing"
	"time"

	"sunray2server/internal/display"
)

func TestFitRectanglePreservesAspectRatio(t *testing.T) {
	got := fitRectangle(image.Rect(0, 0, 2880, 1800), image.Rect(0, 0, 1400, 1050))
	want := image.Rect(0, 87, 1400, 962)
	if got != want {
		t.Fatalf("fit rectangle = %v, want %v", got, want)
	}
}

func TestScaleFramebufferAddsLetterboxAndMapsChanges(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 16, 10))
	draw.Draw(source, source.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	destination := image.NewRGBA(image.Rect(0, 0, 8, 8))
	changed := scaleFramebuffer(destination, source, nil, true)
	if len(changed) != 1 || changed[0] != destination.Bounds() {
		t.Fatalf("full changed rectangles = %v", changed)
	}
	if got := destination.RGBAAt(4, 0); got != (color.RGBA{A: 255}) {
		t.Fatalf("letterbox pixel = %#v, want black", got)
	}
	if got := destination.RGBAAt(4, 3); got.R != 255 || got.A != 255 {
		t.Fatalf("scaled pixel = %#v, want red", got)
	}

	mapped := scaleFramebuffer(destination, source, []image.Rectangle{image.Rect(8, 4, 16, 10)}, false)
	if len(mapped) != 1 || mapped[0] != image.Rect(4, 3, 8, 6) {
		t.Fatalf("mapped changed rectangle = %v", mapped)
	}
}

func TestTranslateScaledPointMapsLetterboxToFramebuffer(t *testing.T) {
	x, y := translateScaledPoint(700, 525, 1400, 1050, 2880, 1800)
	if x != 1440 || y != 901 {
		t.Fatalf("center point = %d,%d, want 1440,901", x, y)
	}
	x, y = translateScaledPoint(700, 0, 1400, 1050, 2880, 1800)
	if x != 1440 || y != 0 {
		t.Fatalf("letterbox point = %d,%d, want 1440,0", x, y)
	}
}

func TestPointerMovementCoalescesButButtonChangeIsImmediate(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	session := NewSession(Config{ScreenWidth: 1400, ScreenHeight: 1050, ScaleToFit: true})
	session.current = &connection{Conn: clientSide, width: 2880, height: 1800}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session.pointerLoop(ctx)

	session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 100, Y: 100})
	session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 700, Y: 525})
	movement := readPointerMessage(t, serverSide)
	if movement[1] != 0 || binary.BigEndian.Uint16(movement[2:4]) != 1440 || binary.BigEndian.Uint16(movement[4:6]) != 901 {
		t.Fatalf("coalesced movement = %x", movement)
	}

	go session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 700, Y: 525, Buttons: 1})
	pressed := readPointerMessage(t, serverSide)
	if pressed[1] != 1 {
		t.Fatalf("button message = %x, want pressed", pressed)
	}
}

func readPointerMessage(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	message := make([]byte, 6)
	if _, err := io.ReadFull(conn, message); err != nil {
		t.Fatal(err)
	}
	return message
}
