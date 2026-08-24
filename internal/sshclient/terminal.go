// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"image"
	"image/color"
	"image/draw"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	fontWidth  = 7
	fontHeight = 13
	fontScale  = 2
	cellWidth  = fontWidth * fontScale
	cellHeight = fontHeight * fontScale
)

type cell struct {
	rune    rune
	fg      uint8
	bg      uint8
	bold    bool
	inverse bool
}

type terminal struct {
	mu sync.Mutex

	cols, rows   int
	cells        []cell
	cursorX      int
	cursorY      int
	savedX       int
	savedY       int
	fg           uint8
	bg           uint8
	bold         bool
	inverse      bool
	scrollTop    int
	scrollBottom int
	state        byte
	csi          []byte
	pendingUTF8  []byte
	dirty        []bool
	dirtySignal  chan struct{}
}

func newTerminal(pixelWidth, pixelHeight int) *terminal {
	cols := max(1, pixelWidth/cellWidth)
	rows := max(1, pixelHeight/cellHeight)
	t := &terminal{
		cols: cols, rows: rows, cells: make([]cell, cols*rows),
		fg: 7, bg: 0, scrollBottom: rows - 1,
		dirty: make([]bool, rows), dirtySignal: make(chan struct{}, 1),
	}
	t.clearAll()
	return t
}

func (t *terminal) dimensions() (int, int) { return t.cols, t.rows }

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	inputLength := len(p)
	data := append(append([]byte(nil), t.pendingUTF8...), p...)
	t.pendingUTF8 = nil
	for i := 0; i < len(data); {
		b := data[i]
		switch t.state {
		case 0:
			if b == 0x1B {
				t.state = 1
				i++
				continue
			}
			if b < 0x20 || b == 0x7F {
				t.control(b)
				i++
				continue
			}
			if b < utf8.RuneSelf {
				t.putRune(rune(b))
				i++
				continue
			}
			if !utf8.FullRune(data[i:]) {
				t.pendingUTF8 = append(t.pendingUTF8, data[i:]...)
				i = len(data)
				continue
			}
			r, size := utf8.DecodeRune(data[i:])
			t.putRune(r)
			i += size
		case 1:
			t.escape(b)
			i++
		case 2:
			if b >= 0x40 && b <= 0x7E {
				t.executeCSI(b)
				t.state = 0
				t.csi = t.csi[:0]
			} else if len(t.csi) < 128 {
				t.csi = append(t.csi, b)
			}
			i++
		case 3:
			if b == 0x07 {
				t.state = 0
			} else if b == 0x1B {
				t.state = 4
			}
			i++
		case 4:
			if b == '\\' {
				t.state = 0
			} else {
				t.state = 3
			}
			i++
		case 5:
			// Character-set selection (for example ESC ( B). The first
			// milestone renders the normal ASCII set, so consume the selector.
			t.state = 0
			i++
		}
	}
	dirty := t.hasDirtyLocked()
	t.mu.Unlock()
	if dirty {
		select {
		case t.dirtySignal <- struct{}{}:
		default:
		}
	}
	return inputLength, nil
}

func (t *terminal) control(b byte) {
	switch b {
	case '\r':
		t.markRow(t.cursorY)
		t.cursorX = 0
	case '\n', '\v', '\f':
		t.lineFeed()
	case '\b':
		t.markRow(t.cursorY)
		if t.cursorX > 0 {
			t.cursorX--
		}
	case '\t':
		t.markRow(t.cursorY)
		t.cursorX = min(t.cols-1, (t.cursorX/8+1)*8)
	}
}

func (t *terminal) escape(b byte) {
	switch b {
	case '[':
		t.state = 2
		t.csi = t.csi[:0]
	case ']':
		t.state = 3
	case '(', ')', '*', '+':
		t.state = 5
	case '7':
		t.savedX, t.savedY = t.cursorX, t.cursorY
		t.state = 0
	case '8':
		t.moveCursor(t.savedY, t.savedX)
		t.state = 0
	case 'D':
		t.lineFeed()
		t.state = 0
	case 'E':
		t.cursorX = 0
		t.lineFeed()
		t.state = 0
	case 'M':
		t.reverseIndex()
		t.state = 0
	case 'c':
		t.reset()
		t.state = 0
	default:
		t.state = 0
	}
}

func (t *terminal) executeCSI(final byte) {
	params := parseParams(string(t.csi))
	p := func(index, fallback int) int {
		if index >= len(params) || params[index] == 0 {
			return fallback
		}
		return params[index]
	}
	switch final {
	case 'A':
		t.moveCursor(t.cursorY-p(0, 1), t.cursorX)
	case 'B', 'e':
		t.moveCursor(t.cursorY+p(0, 1), t.cursorX)
	case 'C', 'a':
		t.moveCursor(t.cursorY, t.cursorX+p(0, 1))
	case 'D':
		t.moveCursor(t.cursorY, t.cursorX-p(0, 1))
	case 'E':
		t.moveCursor(t.cursorY+p(0, 1), 0)
	case 'F':
		t.moveCursor(t.cursorY-p(0, 1), 0)
	case 'G', '`':
		t.moveCursor(t.cursorY, p(0, 1)-1)
	case 'H', 'f':
		t.moveCursor(p(0, 1)-1, p(1, 1)-1)
	case 'd':
		t.moveCursor(p(0, 1)-1, t.cursorX)
	case 'J':
		t.eraseDisplay(p(0, 0))
	case 'K':
		t.eraseLine(p(0, 0))
	case 'm':
		t.sgr(params)
	case 's':
		t.savedX, t.savedY = t.cursorX, t.cursorY
	case 'u':
		t.moveCursor(t.savedY, t.savedX)
	case 'r':
		top, bottom := p(0, 1)-1, p(1, t.rows)-1
		if top >= 0 && bottom < t.rows && top < bottom {
			t.scrollTop, t.scrollBottom = top, bottom
			t.moveCursor(0, 0)
		}
	case 'S':
		for range p(0, 1) {
			t.scrollUp()
		}
	case 'T':
		for range p(0, 1) {
			t.scrollDown()
		}
	case '@':
		t.insertChars(p(0, 1))
	case 'P':
		t.deleteChars(p(0, 1))
	case 'X':
		t.eraseChars(p(0, 1))
	case 'L':
		for range p(0, 1) {
			t.insertLine()
		}
	case 'M':
		for range p(0, 1) {
			t.deleteLine()
		}
	}
}

func parseParams(value string) []int {
	value = strings.TrimLeft(value, "?><!")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ";")
	params := make([]int, len(parts))
	for i, part := range parts {
		params[i], _ = strconv.Atoi(part)
	}
	return params
}

func (t *terminal) putRune(r rune) {
	if r < 0x20 {
		return
	}
	if r > 0x7E {
		r = utf8.RuneError
	}
	t.markRow(t.cursorY)
	t.cells[t.cursorY*t.cols+t.cursorX] = cell{rune: r, fg: t.fg, bg: t.bg, bold: t.bold, inverse: t.inverse}
	t.cursorX++
	if t.cursorX >= t.cols {
		t.cursorX = 0
		t.lineFeed()
	}
}

func (t *terminal) lineFeed() {
	t.markRow(t.cursorY)
	if t.cursorY == t.scrollBottom {
		t.scrollUp()
		return
	}
	t.cursorY = min(t.rows-1, t.cursorY+1)
	t.markRow(t.cursorY)
}

func (t *terminal) reverseIndex() {
	if t.cursorY == t.scrollTop {
		t.scrollDown()
		return
	}
	t.moveCursor(t.cursorY-1, t.cursorX)
}

func (t *terminal) scrollUp() {
	for row := t.scrollTop; row < t.scrollBottom; row++ {
		copy(t.row(row), t.row(row+1))
	}
	t.clearRow(t.scrollBottom)
	t.markRange(t.scrollTop, t.scrollBottom)
}

func (t *terminal) scrollDown() {
	for row := t.scrollBottom; row > t.scrollTop; row-- {
		copy(t.row(row), t.row(row-1))
	}
	t.clearRow(t.scrollTop)
	t.markRange(t.scrollTop, t.scrollBottom)
}

func (t *terminal) insertLine() {
	if t.cursorY < t.scrollTop || t.cursorY > t.scrollBottom {
		return
	}
	for row := t.scrollBottom; row > t.cursorY; row-- {
		copy(t.row(row), t.row(row-1))
	}
	t.clearRow(t.cursorY)
	t.markRange(t.cursorY, t.scrollBottom)
}

func (t *terminal) deleteLine() {
	if t.cursorY < t.scrollTop || t.cursorY > t.scrollBottom {
		return
	}
	for row := t.cursorY; row < t.scrollBottom; row++ {
		copy(t.row(row), t.row(row+1))
	}
	t.clearRow(t.scrollBottom)
	t.markRange(t.cursorY, t.scrollBottom)
}

func (t *terminal) insertChars(count int) {
	count = min(max(count, 1), t.cols-t.cursorX)
	row := t.row(t.cursorY)
	copy(row[t.cursorX+count:], row[t.cursorX:t.cols-count])
	for i := 0; i < count; i++ {
		row[t.cursorX+i] = t.blankCell()
	}
	t.markRow(t.cursorY)
}

func (t *terminal) deleteChars(count int) {
	count = min(max(count, 1), t.cols-t.cursorX)
	row := t.row(t.cursorY)
	copy(row[t.cursorX:], row[t.cursorX+count:])
	for i := t.cols - count; i < t.cols; i++ {
		row[i] = t.blankCell()
	}
	t.markRow(t.cursorY)
}

func (t *terminal) eraseChars(count int) {
	count = min(max(count, 1), t.cols-t.cursorX)
	for i := 0; i < count; i++ {
		t.cells[t.cursorY*t.cols+t.cursorX+i] = t.blankCell()
	}
	t.markRow(t.cursorY)
}

func (t *terminal) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for row := t.cursorY + 1; row < t.rows; row++ {
			t.clearRow(row)
		}
		t.markRange(t.cursorY, t.rows-1)
	case 1:
		for row := 0; row < t.cursorY; row++ {
			t.clearRow(row)
		}
		t.eraseLine(1)
		t.markRange(0, t.cursorY)
	case 2, 3:
		t.clearAll()
	}
}

func (t *terminal) eraseLine(mode int) {
	start, end := 0, t.cols
	if mode == 0 {
		start = t.cursorX
	} else if mode == 1 {
		end = t.cursorX + 1
	}
	for col := start; col < end; col++ {
		t.cells[t.cursorY*t.cols+col] = t.blankCell()
	}
	t.markRow(t.cursorY)
}

func (t *terminal) sgr(params []int) {
	if len(params) == 0 {
		params = []int{0}
	}
	for _, value := range params {
		switch {
		case value == 0:
			t.fg, t.bg, t.bold, t.inverse = 7, 0, false, false
		case value == 1:
			t.bold = true
		case value == 22:
			t.bold = false
		case value == 7:
			t.inverse = true
		case value == 27:
			t.inverse = false
		case value >= 30 && value <= 37:
			t.fg = uint8(value - 30)
		case value == 39:
			t.fg = 7
		case value >= 40 && value <= 47:
			t.bg = uint8(value - 40)
		case value == 49:
			t.bg = 0
		case value >= 90 && value <= 97:
			t.fg = uint8(value - 90 + 8)
		case value >= 100 && value <= 107:
			t.bg = uint8(value - 100 + 8)
		}
	}
}

func (t *terminal) moveCursor(row, col int) {
	t.markRow(t.cursorY)
	t.cursorY = min(max(row, 0), t.rows-1)
	t.cursorX = min(max(col, 0), t.cols-1)
	t.markRow(t.cursorY)
}

func (t *terminal) reset() {
	t.cursorX, t.cursorY, t.savedX, t.savedY = 0, 0, 0, 0
	t.fg, t.bg, t.bold, t.inverse = 7, 0, false, false
	t.scrollTop, t.scrollBottom = 0, t.rows-1
	t.clearAll()
}

func (t *terminal) blankCell() cell { return cell{rune: ' ', fg: t.fg, bg: t.bg} }

func (t *terminal) row(row int) []cell {
	return t.cells[row*t.cols : (row+1)*t.cols]
}

func (t *terminal) clearRow(row int) {
	for col := 0; col < t.cols; col++ {
		t.cells[row*t.cols+col] = t.blankCell()
	}
}

func (t *terminal) clearAll() {
	for row := 0; row < t.rows; row++ {
		t.clearRow(row)
		t.dirty[row] = true
	}
}

func (t *terminal) markRow(row int) {
	if row >= 0 && row < len(t.dirty) {
		t.dirty[row] = true
	}
}

func (t *terminal) markRange(from, to int) {
	for row := max(0, from); row <= min(t.rows-1, to); row++ {
		t.dirty[row] = true
	}
}

func (t *terminal) hasDirtyLocked() bool {
	for _, dirty := range t.dirty {
		if dirty {
			return true
		}
	}
	return false
}

type terminalRenderer struct {
	width, height    int
	offsetX, offsetY int
	frame            *image.RGBA
	first            bool
}

func newTerminalRenderer(width, height, cols, rows int) *terminalRenderer {
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(frame, frame.Bounds(), image.NewUniform(palette[0]), image.Point{}, draw.Src)
	return &terminalRenderer{
		width: width, height: height,
		offsetX: (width - cols*cellWidth) / 2, offsetY: (height - rows*cellHeight) / 2,
		frame: frame, first: true,
	}
}

func (r *terminalRenderer) render(t *terminal) (*image.RGBA, []image.Rectangle, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasDirtyLocked() {
		return r.frame, nil, false
	}
	changed := make([]image.Rectangle, 0)
	bandStart := -1
	for row := 0; row < t.rows; row++ {
		if !t.dirty[row] {
			if bandStart >= 0 {
				changed = append(changed, r.rowBand(bandStart, row))
				bandStart = -1
			}
			continue
		}
		if bandStart < 0 {
			bandStart = row
		}
		r.renderRow(t, row)
		t.dirty[row] = false
	}
	if bandStart >= 0 {
		changed = append(changed, r.rowBand(bandStart, t.rows))
	}
	first := r.first
	r.first = false
	return r.frame, changed, first
}

func (r *terminalRenderer) rowBand(start, end int) image.Rectangle {
	return image.Rect(r.offsetX, r.offsetY+start*cellHeight, r.offsetX+r.widthCells(), r.offsetY+end*cellHeight).Intersect(r.frame.Bounds())
}

func (r *terminalRenderer) widthCells() int {
	return r.width - 2*r.offsetX
}

func (r *terminalRenderer) renderRow(t *terminal, row int) {
	for col := 0; col < t.cols; col++ {
		value := t.cells[row*t.cols+col]
		fg, bg := value.fg, value.bg
		if value.bold && fg < 8 {
			fg += 8
		}
		if value.inverse || row == t.cursorY && col == t.cursorX {
			fg, bg = bg, fg
		}
		x := r.offsetX + col*cellWidth
		y := r.offsetY + row*cellHeight
		rect := image.Rect(x, y, x+cellWidth, y+cellHeight).Intersect(r.frame.Bounds())
		draw.Draw(r.frame, rect, image.NewUniform(palette[bg&15]), image.Point{}, draw.Src)
		glyph := value.rune
		if glyph == 0 {
			glyph = ' '
		}
		r.drawGlyph(x, y, glyph, palette[fg&15])
	}
}

func (r *terminalRenderer) drawGlyph(destinationX, destinationY int, glyph rune, foreground color.RGBA) {
	face := basicfont.Face7x13
	dr, mask, maskPoint, _, ok := face.Glyph(fixed.P(0, face.Ascent), glyph)
	if !ok {
		dr, mask, maskPoint, _, _ = face.Glyph(fixed.P(0, face.Ascent), utf8.RuneError)
	}
	for sourceY := dr.Min.Y; sourceY < dr.Max.Y; sourceY++ {
		for sourceX := dr.Min.X; sourceX < dr.Max.X; sourceX++ {
			_, _, _, alpha := mask.At(maskPoint.X+sourceX-dr.Min.X, maskPoint.Y+sourceY-dr.Min.Y).RGBA()
			if alpha == 0 {
				continue
			}
			x := destinationX + sourceX*fontScale
			y := destinationY + sourceY*fontScale
			draw.Draw(r.frame, image.Rect(x, y, x+fontScale, y+fontScale), image.NewUniform(foreground), image.Point{}, draw.Src)
		}
	}
}

var palette = [16]color.RGBA{
	{0, 0, 0, 255}, {205, 49, 49, 255}, {13, 188, 121, 255}, {229, 229, 16, 255},
	{36, 114, 200, 255}, {188, 63, 188, 255}, {17, 168, 205, 255}, {229, 229, 229, 255},
	{102, 102, 102, 255}, {241, 76, 76, 255}, {35, 209, 139, 255}, {245, 245, 67, 255},
	{59, 142, 234, 255}, {214, 112, 214, 255}, {41, 184, 219, 255}, {255, 255, 255, 255},
}
