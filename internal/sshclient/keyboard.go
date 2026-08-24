// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import "sunray2server/internal/display"

const (
	modifierLeftControl  = 1 << 0
	modifierLeftShift    = 1 << 1
	modifierLeftAlt      = 1 << 2
	modifierRightControl = 1 << 4
	modifierRightShift   = 1 << 5
	modifierRightAlt     = 1 << 6
)

// keyBytes converts one pressed USB HID key into the byte sequence expected by
// an xterm-compatible remote PTY. Releases and standalone modifier keys do not
// produce terminal input.
func keyBytes(event display.InputEvent, capsLock bool) []byte {
	if event.Kind != display.InputKey || !event.Pressed || event.HID >= 0xE0 {
		return nil
	}
	shift := event.Modifiers&(modifierLeftShift|modifierRightShift) != 0
	control := event.Modifiers&(modifierLeftControl|modifierRightControl) != 0
	alt := event.Modifiers&(modifierLeftAlt|modifierRightAlt) != 0

	var sequence []byte
	if event.HID >= 0x04 && event.HID <= 0x1D {
		letter := byte('a' + event.HID - 0x04)
		if control {
			sequence = []byte{letter - 'a' + 1}
		} else {
			if shift != capsLock {
				letter -= 'a' - 'A'
			}
			sequence = []byte{letter}
		}
	} else if event.HID >= 0x1E && event.HID <= 0x27 {
		unshifted := "1234567890"
		shifted := "!@#$%^&*()"
		index := int(event.HID - 0x1E)
		if shift {
			sequence = []byte{shifted[index]}
		} else {
			sequence = []byte{unshifted[index]}
		}
	} else if pair, ok := printableHID[event.HID]; ok {
		if shift {
			sequence = []byte{pair[1]}
		} else {
			sequence = []byte{pair[0]}
		}
	} else if value, ok := specialHID[event.HID]; ok {
		sequence = []byte(value)
	} else if event.HID >= 0x3A && event.HID <= 0x45 {
		sequence = []byte(functionKeys[event.HID-0x3A])
	}
	if len(sequence) == 0 {
		return nil
	}
	if alt {
		return append([]byte{0x1B}, sequence...)
	}
	return sequence
}

var printableHID = map[uint8][2]byte{
	0x2C: {' ', ' '},
	0x2D: {'-', '_'},
	0x2E: {'=', '+'},
	0x2F: {'[', '{'},
	0x30: {']', '}'},
	0x31: {'\\', '|'},
	0x32: {'#', '~'},
	0x33: {';', ':'},
	0x34: {'\'', '"'},
	0x35: {'`', '~'},
	0x36: {',', '<'},
	0x37: {'.', '>'},
	0x38: {'/', '?'},
}

var specialHID = map[uint8]string{
	0x28: "\r",
	0x29: "\x1b",
	0x2A: "\x7f",
	0x2B: "\t",
	0x49: "\x1b[2~",
	0x4A: "\x1b[H",
	0x4B: "\x1b[5~",
	0x4C: "\x1b[3~",
	0x4D: "\x1b[F",
	0x4E: "\x1b[6~",
	0x4F: "\x1b[C",
	0x50: "\x1b[D",
	0x51: "\x1b[B",
	0x52: "\x1b[A",
	0x54: "/",
	0x55: "*",
	0x56: "-",
	0x57: "+",
	0x58: "\r",
	0x59: "1",
	0x5A: "2",
	0x5B: "3",
	0x5C: "4",
	0x5D: "5",
	0x5E: "6",
	0x5F: "7",
	0x60: "8",
	0x61: "9",
	0x62: "0",
	0x63: ".",
}

var functionKeys = [12]string{
	"\x1bOP", "\x1bOQ", "\x1bOR", "\x1bOS",
	"\x1b[15~", "\x1b[17~", "\x1b[18~", "\x1b[19~",
	"\x1b[20~", "\x1b[21~", "\x1b[23~", "\x1b[24~",
}
