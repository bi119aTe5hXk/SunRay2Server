// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"image"
	"testing"
)

func TestTerminalTextCursorAndANSI(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	term := newTerminalWithFont(terminalFont.cellWidth*10, terminalFont.cellHeight*3, terminalFont)
	_, _ = term.Write([]byte("hello\r\n\x1b[31mred\x1b[0m"))
	term.mu.Lock()
	defer term.mu.Unlock()
	if got := string([]rune{term.cells[0].rune, term.cells[1].rune, term.cells[2].rune, term.cells[3].rune, term.cells[4].rune}); got != "hello" {
		t.Fatalf("first row = %q", got)
	}
	if term.cells[10].rune != 'r' || term.cells[10].fg != 1 {
		t.Fatalf("red cell = %#v", term.cells[10])
	}
	if term.cursorX != 3 || term.cursorY != 1 {
		t.Fatalf("cursor = %d,%d", term.cursorX, term.cursorY)
	}
}

func TestTerminalScrollsAndRendererReturnsDirtyBands(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	width, height := terminalFont.cellWidth*8, terminalFont.cellHeight*2
	term := newTerminalWithFont(width, height, terminalFont)
	renderer := newTerminalRendererWithFont(width, height, 8, 2, terminalFont)
	_, _, first := renderer.render(term)
	if !first {
		t.Fatal("initial render was not first")
	}
	_, _ = term.Write([]byte("one\r\ntwo\r\nthree"))
	frame, changed, first := renderer.render(term)
	if first || len(changed) == 0 || frame.Bounds() != image.Rect(0, 0, width, height) {
		t.Fatalf("render result first=%v changed=%v bounds=%v", first, changed, frame.Bounds())
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cells[0].rune != 't' {
		t.Fatalf("scroll did not retain second row: %#v", term.cells[:8])
	}
}

func TestSplitUTF8DoesNotCorruptParser(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	term := newTerminalWithFont(terminalFont.cellWidth*4, terminalFont.cellHeight, terminalFont)
	_, _ = term.Write([]byte{0xE2, 0x82})
	_, _ = term.Write([]byte{0xAC})
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cursorX != 1 || term.cells[0].rune != '€' {
		t.Fatalf("split UTF-8 result = %#v cursor=%d", term.cells[0], term.cursorX)
	}
}

func TestBareLineFeedReturnsToFirstColumn(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	term := newTerminalWithFont(terminalFont.cellWidth*8, terminalFont.cellHeight*2, terminalFont)
	_, _ = term.Write([]byte("abc\nx"))
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cells[term.cols].rune != 'x' || term.cursorX != 1 || term.cursorY != 1 {
		t.Fatalf("bare LF result cursor=%d,%d next-row=%#v", term.cursorX, term.cursorY, term.cells[term.cols])
	}
}

func TestWideRuneOccupiesTwoCells(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	term := newTerminalWithFont(terminalFont.cellWidth*4, terminalFont.cellHeight, terminalFont)
	_, _ = term.Write([]byte("界"))
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cursorX != 2 || term.cells[0].rune != '界' || !term.cells[1].continuation {
		t.Fatalf("wide rune result = %#v %#v cursor=%d", term.cells[0], term.cells[1], term.cursorX)
	}
}

func TestExtendedANSIColorsAreConsumedAsOneSequence(t *testing.T) {
	terminalFont := mustDefaultTerminalFont()
	defer terminalFont.face.Close()
	term := newTerminalWithFont(terminalFont.cellWidth*4, terminalFont.cellHeight, terminalFont)
	_, _ = term.Write([]byte("\x1b[38;5;196mX\x1b[38;2;0;255;0mY"))
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cells[0].fg != 1 || term.cells[1].fg != 2 {
		t.Fatalf("extended colors = %d, %d", term.cells[0].fg, term.cells[1].fg)
	}
}
