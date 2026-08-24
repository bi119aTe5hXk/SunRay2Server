// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

const (
	opResendDone = 0xAC
	opFill       = 0xA2
	opCopy       = 0xA4
	opBitmap     = 0xA5
	opBitmapRGB  = 0xA6
	opBounds     = 0xA8
	opCursor     = 0xA9
	opPad        = 0xAF
)

// Operation is one ALP display operation. The first 12 bytes are its header.
type Operation struct {
	Bytes     []byte
	Increment bool
}

func operation(code byte, x, y, width, height int, payloadLen int) Operation {
	b := make([]byte, 12+payloadLen)
	b[0] = code
	binary.BigEndian.PutUint16(b[4:6], uint16(x))
	binary.BigEndian.PutUint16(b[6:8], uint16(y))
	binary.BigEndian.PutUint16(b[8:10], uint16(width))
	binary.BigEndian.PutUint16(b[10:12], uint16(height))
	return Operation{Bytes: b, Increment: true}
}

func (o Operation) WithSequence(seq uint16) Operation {
	clone := Operation{Bytes: append([]byte(nil), o.Bytes...), Increment: o.Increment}
	binary.BigEndian.PutUint16(clone.Bytes[2:4], seq)
	return clone
}

func Bounds(width, height int) Operation {
	o := operation(opBounds, 0, 0, width, height, 8)
	binary.BigEndian.PutUint16(o.Bytes[12:14], 0)
	binary.BigEndian.PutUint16(o.Bytes[14:16], 0)
	binary.BigEndian.PutUint16(o.Bytes[16:18], uint16(width))
	binary.BigEndian.PutUint16(o.Bytes[18:20], uint16(height))
	return o
}

func Fill(x, y, width, height int, c color.Color) Operation {
	o := operation(opFill, x, y, width, height, 4)
	r, g, b, _ := c.RGBA()
	o.Bytes[12] = 0xFF
	o.Bytes[13] = byte(b >> 8)
	o.Bytes[14] = byte(g >> 8)
	o.Bytes[15] = byte(r >> 8)
	return o
}

// Copy moves pixels already present in the Sun Ray framebuffer. It replaces
// a potentially multi-megabyte redraw during scrolling or window movement
// with one 16-byte ALP operation.
func Copy(x, y, width, height, sourceX, sourceY int) Operation {
	o := operation(opCopy, x, y, width, height, 4)
	binary.BigEndian.PutUint16(o.Bytes[12:14], uint16(sourceX))
	binary.BigEndian.PutUint16(o.Bytes[14:16], uint16(sourceY))
	return o
}

func InvisibleCursor() Operation {
	o := operation(opCursor, 1, 1, 16, 16, 72)
	// Two colors use the ALP 0,B,G,R representation. Bitmap and mask stay zero.
	o.Bytes[17] = 0xFF
	o.Bytes[18] = 0xFF
	o.Bytes[19] = 0xFF
	return o
}

// LocalCursor installs a small high-contrast cursor rendered by the Sun Ray
// itself. It gives remote sessions immediate pointer feedback without waiting
// for a server-side cursor update to travel back through the framebuffer.
func LocalCursor() Operation {
	bitmap := [...]byte{
		0x00, 0x00, 0x70, 0x0E, 0x78, 0x1E, 0x7C, 0x3E,
		0x3E, 0x7C, 0x1F, 0xF8, 0x0F, 0xF0, 0x07, 0xE0,
		0x07, 0xE0, 0x0F, 0xF0, 0x1F, 0xF8, 0x3E, 0x7C,
		0x7C, 0x3E, 0x78, 0x1E, 0x70, 0x0E, 0x00, 0x00,
	}
	mask := [...]byte{
		0xF0, 0x0F, 0xF8, 0x1F, 0xFC, 0x3F, 0xFE, 0x7F,
		0x7F, 0xFE, 0x3F, 0xFC, 0x1F, 0xF8, 0x0F, 0xF0,
		0x0F, 0xF0, 0x1F, 0xF8, 0x3F, 0xFC, 0x7F, 0xFE,
		0xFE, 0x7F, 0xFC, 0x3F, 0xF8, 0x1F, 0xF0, 0x0F,
	}
	o := operation(opCursor, 7, 7, 16, 16, 72)
	// Color 0 is black; color 1 is orange in ALP 0,B,G,R order.
	copy(o.Bytes[16:20], []byte{0, 0, 200, 255})
	copy(o.Bytes[20:52], bitmap[:])
	copy(o.Bytes[52:84], mask[:])
	return o
}

func Pad() Operation {
	o := operation(opPad, 0, 1, 0xFFFF, 0xFFFF, 12)
	o.Increment = false
	for i := 12; i < len(o.Bytes); i++ {
		o.Bytes[i] = 0xFF
	}
	return o
}

// ResendDone tells the terminal that a requested operation range has been
// replayed. Sun Ray servers send this after a pad operation at the end of a
// NACK response.
func ResendDone(to uint16) Operation {
	o := operation(opResendDone, 0, 0, 0, 0, 8)
	// This is a transport acknowledgement, not a new drawing operation. Some
	// Sun Ray 2 firmware NACKs a newly allocated status sequence forever.
	o.Increment = false
	binary.BigEndian.PutUint16(o.Bytes[12:14], 0)
	binary.BigEndian.PutUint16(o.Bytes[14:16], 1)
	binary.BigEndian.PutUint16(o.Bytes[16:18], 0)
	binary.BigEndian.PutUint16(o.Bytes[18:20], to)
	return o
}

func BitmapRGB(img image.Image, rect image.Rectangle, dst image.Point) (Operation, error) {
	if rect.Empty() || !rect.In(img.Bounds()) {
		return Operation{}, fmt.Errorf("bitmap rectangle %v is outside image bounds %v", rect, img.Bounds())
	}
	width, height := rect.Dx(), rect.Dy()
	stride := round4(width * 3)
	o := operation(opBitmapRGB, dst.X, dst.Y, width, height, stride*height)

	pos := 12
	if rgba, ok := img.(*image.RGBA); ok {
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			rowStart := pos
			source := rgba.PixOffset(rect.Min.X, y)
			for x := 0; x < width; x++ {
				o.Bytes[pos] = rgba.Pix[source+2]
				o.Bytes[pos+1] = rgba.Pix[source+1]
				o.Bytes[pos+2] = rgba.Pix[source]
				pos += 3
				source += 4
			}
			pos = rowStart + stride
		}
		return o, nil
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		rowStart := pos
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			o.Bytes[pos] = byte(b >> 8)
			o.Bytes[pos+1] = byte(g >> 8)
			o.Bytes[pos+2] = byte(r >> 8)
			pos += 3
		}
		pos = rowStart + stride
	}
	return o, nil
}

// BitmapBiColor encodes a rectangle using two colors and one bit per pixel.
// ALP stores each bitmap row on a byte boundary and pads the complete bitplane
// to a 32-bit boundary. Pixels matching c1 have their corresponding bit set.
func BitmapBiColor(img image.Image, rect image.Rectangle, dst image.Point, c0, c1 color.RGBA) (Operation, error) {
	if rect.Empty() || !rect.In(img.Bounds()) {
		return Operation{}, fmt.Errorf("bitmap rectangle %v is outside image bounds %v", rect, img.Bounds())
	}
	stride := (rect.Dx() + 7) / 8
	bitmapLength := round4(stride * rect.Dy())
	o := operation(opBitmap, dst.X, dst.Y, rect.Dx(), rect.Dy(), 8+bitmapLength)
	setOperationColor(o.Bytes[12:16], c0)
	setOperationColor(o.Bytes[16:20], c1)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		row := 20 + (y-rect.Min.Y)*stride
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if rgbaAt(img, x, y) == c1 {
				o.Bytes[row+(x-rect.Min.X)/8] |= 1 << (7 - uint(x-rect.Min.X)%8)
			}
		}
	}
	return o, nil
}

func setOperationColor(destination []byte, c color.RGBA) {
	destination[0] = 0
	destination[1] = c.B
	destination[2] = c.G
	destination[3] = c.R
}

func rgbaAt(img image.Image, x, y int) color.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		offset := rgba.PixOffset(x, y)
		return color.RGBA{R: rgba.Pix[offset], G: rgba.Pix[offset+1], B: rgba.Pix[offset+2], A: rgba.Pix[offset+3]}
	}
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: byte(r >> 8), G: byte(g >> 8), B: byte(b >> 8), A: byte(a >> 8)}
}

func round4(n int) int {
	return (n + 3) &^ 3
}
