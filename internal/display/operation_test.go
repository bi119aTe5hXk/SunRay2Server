// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"image"
	"image/color"
	"reflect"
	"testing"
)

func TestFillEncoding(t *testing.T) {
	o := Fill(1, 2, 3, 4, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}).WithSequence(9)
	if len(o.Bytes) != 16 || o.Bytes[0] != opFill {
		t.Fatalf("unexpected operation: %x", o.Bytes)
	}
	if binary.BigEndian.Uint16(o.Bytes[2:4]) != 9 {
		t.Fatalf("unexpected sequence: %x", o.Bytes[2:4])
	}
	if got := o.Bytes[12:16]; string(got) != string([]byte{0xFF, 0x33, 0x22, 0x11}) {
		t.Fatalf("unexpected ALP color: %x", got)
	}
}

func TestBitmapRGBPadsRowsAndUsesBGR(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xFF})
	o, err := BitmapRGB(img, img.Bounds(), image.Pt(4, 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Bytes) != 16 {
		t.Fatalf("got %d bytes, want 16", len(o.Bytes))
	}
	if got := o.Bytes[12:16]; string(got) != string([]byte{0x30, 0x20, 0x10, 0x00}) {
		t.Fatalf("unexpected bitmap bytes: %x", got)
	}
}

func TestBitmapBiColorUsesOneBitPerPixel(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 9, 2))
	c0 := color.RGBA{R: 10, G: 20, B: 30, A: 255}
	c1 := color.RGBA{R: 40, G: 50, B: 60, A: 255}
	for y := 0; y < 2; y++ {
		for x := 0; x < 9; x++ {
			img.SetRGBA(x, y, c0)
		}
	}
	img.SetRGBA(0, 0, c1)
	img.SetRGBA(8, 0, c1)
	img.SetRGBA(1, 1, c1)
	op, err := BitmapBiColor(img, img.Bounds(), image.Pt(3, 4), c0, c1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(op.Bytes), 24; got != want {
		t.Fatalf("operation length = %d, want %d", got, want)
	}
	if got := op.Bytes[20:24]; !reflect.DeepEqual(got, []byte{0x80, 0x80, 0x40, 0x00}) {
		t.Fatalf("bitmap bytes = %x, want 80804000", got)
	}
}

func TestResendDoneEncoding(t *testing.T) {
	o := ResendDone(0x1234).WithSequence(9)
	if len(o.Bytes) != 20 || o.Bytes[0] != opResendDone {
		t.Fatalf("unexpected operation: %x", o.Bytes)
	}
	if got := binary.BigEndian.Uint16(o.Bytes[2:4]); got != 9 {
		t.Fatalf("sequence = %d, want 9", got)
	}
	if o.Increment {
		t.Fatal("resend completion must not consume a drawing sequence")
	}
	want := []uint16{0, 1, 0, 0x1234}
	for i, value := range want {
		if got := binary.BigEndian.Uint16(o.Bytes[12+i*2 : 14+i*2]); got != value {
			t.Fatalf("payload word %d = %#x, want %#x", i, got, value)
		}
	}
}

func TestLocalCursorEncoding(t *testing.T) {
	o := LocalCursor()
	if len(o.Bytes) != 84 || o.Bytes[0] != opCursor {
		t.Fatalf("unexpected cursor operation: length=%d opcode=%#x", len(o.Bytes), o.Bytes[0])
	}
	if got := binary.BigEndian.Uint16(o.Bytes[4:6]); got != 7 {
		t.Fatalf("cursor hotspot x = %d, want 7", got)
	}
	if got := binary.BigEndian.Uint16(o.Bytes[6:8]); got != 7 {
		t.Fatalf("cursor hotspot y = %d, want 7", got)
	}
	if got := o.Bytes[52:84]; got[0] != 0xF0 || got[31] != 0x0F {
		t.Fatalf("unexpected cursor mask: %x", got)
	}
}
