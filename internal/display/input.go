// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"fmt"
)

// InputKind identifies an input event decoded from the Sun Ray UDP channel.
type InputKind uint8

const (
	InputKey InputKind = iota + 1
	InputPointer
)

// InputEvent is a device-independent event suitable for SSH, VNC or RDP
// adapters. Key events retain the USB HID usage emitted by the terminal.
type InputEvent struct {
	Kind      InputKind
	HID       uint8
	Pressed   bool
	Modifiers uint8
	X         uint16
	Y         uint16
	Buttons   uint8
}

type inputDecoder struct {
	keys      [256]bool
	modifiers uint8
}

var modifierHID = [8]uint8{0xE0, 0xE1, 0xE2, 0xE3, 0xE4, 0xE5, 0xE6, 0xE7}

func (d *inputDecoder) keyboard(operation []byte) ([]InputEvent, error) {
	if len(operation) < 16 || operation[0] != 0xC1 {
		return nil, fmt.Errorf("short or invalid keyboard operation: %d bytes", len(operation))
	}

	modifiers := uint8(binary.BigEndian.Uint16(operation[6:8]))
	var current [256]bool
	for _, hid := range operation[8:14] {
		if hid != 0 {
			current[hid] = true
		}
	}

	events := make([]InputEvent, 0, 12)
	// Release ordinary keys before releasing their modifiers.
	for hid := 1; hid < len(d.keys); hid++ {
		if d.keys[hid] && !current[hid] {
			events = append(events, keyInput(uint8(hid), false, modifiers))
		}
	}
	for bit, hid := range modifierHID {
		mask := uint8(1 << bit)
		if d.modifiers&mask != 0 && modifiers&mask == 0 {
			events = append(events, keyInput(hid, false, modifiers))
		}
	}
	// Press modifiers before ordinary keys so downstream protocols observe the
	// correct chord (for example Shift+A).
	for bit, hid := range modifierHID {
		mask := uint8(1 << bit)
		if d.modifiers&mask == 0 && modifiers&mask != 0 {
			events = append(events, keyInput(hid, true, modifiers))
		}
	}
	for hid := 1; hid < len(current); hid++ {
		if current[hid] && !d.keys[hid] {
			events = append(events, keyInput(uint8(hid), true, modifiers))
		}
	}

	d.keys = current
	d.modifiers = modifiers
	return events, nil
}

func (d *inputDecoder) pointer(operation []byte) (InputEvent, error) {
	if len(operation) < 12 || operation[0] != 0xC2 {
		return InputEvent{}, fmt.Errorf("short or invalid pointer operation: %d bytes", len(operation))
	}
	// Bit 6 is always set by the Sun Ray firmware and is not a mouse button.
	buttons := uint8(binary.BigEndian.Uint16(operation[4:6])) & 0x1F
	return InputEvent{
		Kind:    InputPointer,
		X:       binary.BigEndian.Uint16(operation[6:8]),
		Y:       binary.BigEndian.Uint16(operation[8:10]),
		Buttons: buttons,
	}, nil
}

func keyInput(hid uint8, pressed bool, modifiers uint8) InputEvent {
	return InputEvent{Kind: InputKey, HID: hid, Pressed: pressed, Modifiers: modifiers}
}
