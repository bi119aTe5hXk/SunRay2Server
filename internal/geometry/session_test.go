// SPDX-License-Identifier: GPL-2.0-or-later

package geometry

import (
	"context"
	"image"
	"testing"
	"time"

	"sunray2server/internal/display"
)

type capturedFrame struct {
	width, height           int
	clearWidth, clearHeight int
}

func TestAdjustGeometryStepsAndReset(t *testing.T) {
	width, height := adjustGeometry(1400, 1050, 1400, 1050, key(hidRight, 0))
	if width != 1410 || height != 1050 {
		t.Fatalf("normal step = %dx%d", width, height)
	}
	width, height = adjustGeometry(width, height, 1400, 1050, key(hidDown, modifierShift))
	if width != 1410 || height != 1051 {
		t.Fatalf("fine step = %dx%d", width, height)
	}
	width, height = adjustGeometry(width, height, 1400, 1050, key(hidLeft, modifierControl))
	if width != 1310 || height != 1051 {
		t.Fatalf("coarse step = %dx%d", width, height)
	}
	width, height = adjustGeometry(width, height, 1400, 1050, key(hidR, 0))
	if width != 1400 || height != 1050 {
		t.Fatalf("reset = %dx%d", width, height)
	}
}

func TestSessionDrawsInitialAndAdjustedGeometry(t *testing.T) {
	frames := make(chan capturedFrame, 2)
	session := NewSession(Config{
		Width: 1400, Height: 1050,
		OnFrame: func(width, height, clearWidth, clearHeight int, _ *image.RGBA) error {
			frames <- capturedFrame{width, height, clearWidth, clearHeight}
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx) }()
	if got := receiveFrame(t, frames); got != (capturedFrame{1400, 1050, 1400, 1050}) {
		t.Fatalf("initial frame = %#v", got)
	}
	session.HandleInput(key(hidRight, 0))
	if got := receiveFrame(t, frames); got != (capturedFrame{1410, 1050, 1410, 1050}) {
		t.Fatalf("adjusted frame = %#v", got)
	}
	session.HandleInput(key(hidLeft, 0))
	if got := receiveFrame(t, frames); got != (capturedFrame{1400, 1050, 1410, 1050}) {
		t.Fatalf("reduced frame did not retain clear bounds = %#v", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func key(hid, modifiers uint8) display.InputEvent {
	return display.InputEvent{Kind: display.InputKey, HID: hid, Pressed: true, Modifiers: modifiers}
}

func receiveFrame(t *testing.T, frames <-chan capturedFrame) capturedFrame {
	t.Helper()
	select {
	case value := <-frames:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for geometry frame")
		return capturedFrame{}
	}
}
