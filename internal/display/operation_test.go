// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"image"
	"image/color"
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
