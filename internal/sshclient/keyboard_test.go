// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"bytes"
	"testing"

	"sunray2server/internal/display"
)

func TestKeyBytes(t *testing.T) {
	tests := []struct {
		event display.InputEvent
		caps  bool
		want  []byte
	}{
		{display.InputEvent{Kind: display.InputKey, HID: 0x04, Pressed: true}, false, []byte("a")},
		{display.InputEvent{Kind: display.InputKey, HID: 0x04, Pressed: true, Modifiers: modifierLeftShift}, false, []byte("A")},
		{display.InputEvent{Kind: display.InputKey, HID: 0x06, Pressed: true, Modifiers: modifierLeftControl}, false, []byte{3}},
		{display.InputEvent{Kind: display.InputKey, HID: 0x52, Pressed: true}, false, []byte("\x1b[A")},
		{display.InputEvent{Kind: display.InputKey, HID: 0x1F, Pressed: true, Modifiers: modifierLeftShift}, false, []byte("@")},
		{display.InputEvent{Kind: display.InputKey, HID: 0x04, Pressed: false}, false, nil},
	}
	for _, test := range tests {
		if got := keyBytes(test.event, test.caps); !bytes.Equal(got, test.want) {
			t.Errorf("keyBytes(%#v, %v) = %q, want %q", test.event, test.caps, got, test.want)
		}
	}
}
