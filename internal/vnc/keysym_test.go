// SPDX-License-Identifier: GPL-2.0-or-later

package vnc

import "testing"

func TestKeysymForHID(t *testing.T) {
	tests := map[uint8]uint32{
		0x04: 'a',
		0x27: '0',
		0x28: 0xFF0D,
		0x3A: 0xFFBE,
		0x45: 0xFFC9,
		0x4F: 0xFF53,
		0xE0: 0xFFE3,
	}
	for hid, want := range tests {
		got, ok := keysymForHID(hid)
		if !ok || got != want {
			t.Errorf("keysymForHID(0x%02x) = 0x%x, %v; want 0x%x", hid, got, ok, want)
		}
	}
	if _, ok := keysymForHID(0); ok {
		t.Fatal("reserved HID usage should not map")
	}
}

func TestTranslateCoordinate(t *testing.T) {
	if got := translateCoordinate(200, 1400, 1024); got != 12 {
		t.Fatalf("centered coordinate = %d, want 12", got)
	}
	if got := translateCoordinate(0, 1400, 1024); got != 0 {
		t.Fatalf("low clipped coordinate = %d", got)
	}
	if got := translateCoordinate(1399, 1400, 1024); got != 1023 {
		t.Fatalf("high clipped coordinate = %d", got)
	}
}
