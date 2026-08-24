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

func TestWheelNotchIsImmediatePressReleasePulse(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	session := NewSession(Config{ScreenWidth: 1400, ScreenHeight: 1050})
	session.current = &connection{Conn: clientSide, width: 1400, height: 1050}

	done := make(chan struct{})
	go func() {
		session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 700, Y: 525, Buttons: 0x08})
		close(done)
	}()
	pressed := readPointerMessage(t, serverSide)
	released := readPointerMessage(t, serverSide)
	<-done
	if pressed[1] != 0x08 || released[1] != 0 {
		t.Fatalf("wheel pulse = %x then %x, want button4 then release", pressed, released)
	}

	// Repeated firmware pulses must each create a new RFB notch even without
	// an intervening pointer movement.
	done = make(chan struct{})
	go func() {
		session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 700, Y: 525, Buttons: 0x10})
		close(done)
	}()
	pressed = readPointerMessage(t, serverSide)
	released = readPointerMessage(t, serverSide)
	<-done
	if pressed[1] != 0x10 || released[1] != 0 {
		t.Fatalf("reverse wheel pulse = %x then %x, want button5 then release", pressed, released)
	}

	for _, test := range []struct {
		delta int16
		want  byte
	}{{1, 0x08}, {-1, 0x10}} {
		done = make(chan struct{})
		go func() {
			session.HandleInput(display.InputEvent{Kind: display.InputPointer, X: 700, Y: 525, Wheel: test.delta})
			close(done)
		}()
		pressed = readPointerMessage(t, serverSide)
		released = readPointerMessage(t, serverSide)
		<-done
		if pressed[1] != test.want || released[1] != 0 {
			t.Fatalf("wheel delta %d pulse = %x then %x", test.delta, pressed, released)
		}
	}
}

func TestFrameDeliveryCoalescesUpdatesWhileDisplayIsBusy(t *testing.T) {
	type deliveredFrame struct {
		changed image.Rectangle
		pixel   color.RGBA
	}
	delivered := make(chan deliveredFrame, 2)
	releaseFirst := make(chan struct{})
	calls := 0
	session := NewSession(Config{OnFrame: func(frame *image.RGBA, changed []display.RegionUpdate, resized bool) error {
		calls++
		delivered <- deliveredFrame{changed: changed[0].Rectangle, pixel: frame.RGBAAt(7, 7)}
		if calls == 1 {
			<-releaseFirst
		}
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session.frameLoop(ctx)

	frame := image.NewRGBA(image.Rect(0, 0, 10, 10))
	draw.Draw(frame, frame.Bounds(), image.NewUniform(color.RGBA{R: 1, A: 255}), image.Point{}, draw.Src)
	if err := session.enqueueFrame(frame, []display.RegionUpdate{{Rectangle: frame.Bounds()}}, false); err != nil {
		t.Fatal(err)
	}
	first := <-delivered
	if first.changed != frame.Bounds() {
		t.Fatalf("first changed rectangle = %v", first.changed)
	}

	draw.Draw(frame, image.Rect(2, 2, 4, 4), image.NewUniform(color.RGBA{G: 2, A: 255}), image.Point{}, draw.Src)
	_ = session.enqueueFrame(frame, []display.RegionUpdate{{Rectangle: image.Rect(2, 2, 4, 4)}}, false)
	draw.Draw(frame, image.Rect(7, 7, 9, 9), image.NewUniform(color.RGBA{B: 3, A: 255}), image.Point{}, draw.Src)
	_ = session.enqueueFrame(frame, []display.RegionUpdate{{Rectangle: image.Rect(7, 7, 9, 9)}}, false)
	close(releaseFirst)

	select {
	case second := <-delivered:
		if second.changed != image.Rect(2, 2, 9, 9) {
			t.Fatalf("coalesced rectangle = %v, want (2,2)-(9,9)", second.changed)
		}
		if second.pixel.B != 3 {
			t.Fatalf("coalesced frame pixel = %#v", second.pixel)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced frame")
	}
}

func TestChangedRectanglesKeepDistantUpdatesSeparate(t *testing.T) {
	bounds := image.Rect(0, 0, 1400, 1050)
	rectangles := appendChangedUpdate(nil, display.RegionUpdate{Rectangle: image.Rect(10, 10, 20, 20)}, bounds)
	rectangles = appendChangedUpdate(rectangles, display.RegionUpdate{Rectangle: image.Rect(1000, 900, 1010, 910)}, bounds)
	if len(rectangles) != 2 {
		t.Fatalf("distant rectangle count = %d, want 2", len(rectangles))
	}
	rectangles = appendChangedUpdate(rectangles, display.RegionUpdate{Rectangle: image.Rect(18, 18, 30, 30)}, bounds)
	if len(rectangles) != 2 || rectangles[1].Rectangle != image.Rect(10, 10, 30, 30) {
		t.Fatalf("nearby rectangle merge = %v", rectangles)
	}
}

func TestCopyUpdatePreservesOperationOrder(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 100)
	updates := appendChangedUpdate(nil, display.RegionUpdate{Rectangle: image.Rect(0, 0, 10, 10)}, bounds)
	updates = appendChangedUpdate(updates, display.RegionUpdate{Rectangle: image.Rect(20, 20, 30, 30), CopySource: image.Pt(0, 0), Copy: true}, bounds)
	updates = appendChangedUpdate(updates, display.RegionUpdate{Rectangle: image.Rect(5, 5, 15, 15)}, bounds)
	if len(updates) != 3 || updates[0].Copy || !updates[1].Copy || updates[2].Copy {
		t.Fatalf("ordered updates = %+v", updates)
	}
}

func TestRequestFullFrameSchedulesLatestFramebuffer(t *testing.T) {
	session := NewSession(Config{})
	session.latestFrame = image.NewRGBA(image.Rect(0, 0, 1400, 1050))
	session.RequestFullFrame()
	if len(session.pendingChanged) != 1 || session.pendingChanged[0].Rectangle != session.latestFrame.Bounds() {
		t.Fatalf("pending full-frame rectangles = %v", session.pendingChanged)
	}
	if !session.pendingResized {
		t.Fatal("full-frame request was not marked for complete redraw")
	}
	select {
	case <-session.frameWake:
	default:
		t.Fatal("full-frame request did not wake delivery loop")
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
