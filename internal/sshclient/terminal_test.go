// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"image"
	"testing"
)

func TestTerminalTextCursorAndANSI(t *testing.T) {
	term := newTerminal(cellWidth*10, cellHeight*3)
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
	term := newTerminal(cellWidth*8, cellHeight*2)
	renderer := newTerminalRenderer(cellWidth*8, cellHeight*2, 8, 2)
	_, _, first := renderer.render(term)
	if !first {
		t.Fatal("initial render was not first")
	}
	_, _ = term.Write([]byte("one\r\ntwo\r\nthree"))
	frame, changed, first := renderer.render(term)
	if first || len(changed) == 0 || frame.Bounds() != image.Rect(0, 0, cellWidth*8, cellHeight*2) {
		t.Fatalf("render result first=%v changed=%v bounds=%v", first, changed, frame.Bounds())
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cells[0].rune != 't' {
		t.Fatalf("scroll did not retain second row: %#v", term.cells[:8])
	}
}

func TestSplitUTF8DoesNotCorruptParser(t *testing.T) {
	term := newTerminal(cellWidth*4, cellHeight)
	_, _ = term.Write([]byte{0xE2, 0x82})
	_, _ = term.Write([]byte{0xAC})
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cursorX != 1 || term.cells[0].rune != '\uFFFD' {
		t.Fatalf("split UTF-8 result = %#v cursor=%d", term.cells[0], term.cursorX)
	}
}
