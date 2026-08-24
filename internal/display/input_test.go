// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"testing"
)

func TestKeyboardReportProducesTransitions(t *testing.T) {
	var decoder inputDecoder
	press := make([]byte, 16)
	press[0] = 0xC1
	binary.BigEndian.PutUint16(press[6:8], 0x0002) // left shift
	press[8] = 0x04                                // A

	events, err := decoder.keyboard(press)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != keyInput(0xE1, true, 0x02) || events[1] != keyInput(0x04, true, 0x02) {
		t.Fatalf("unexpected key press events: %#v", events)
	}

	release := make([]byte, 16)
	release[0] = 0xC1
	events, err = decoder.keyboard(release)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != keyInput(0x04, false, 0) || events[1] != keyInput(0xE1, false, 0) {
		t.Fatalf("unexpected key release events: %#v", events)
	}
}

func TestKeyboardReportTracksAllSixKeys(t *testing.T) {
	var decoder inputDecoder
	report := make([]byte, 16)
	report[0] = 0xC1
	copy(report[8:14], []byte{4, 5, 6, 7, 8, 9})
	events, err := decoder.keyboard(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6: %#v", len(events), events)
	}
}

func TestPointerReport(t *testing.T) {
	operation := make([]byte, 12)
	operation[0] = 0xC2
	binary.BigEndian.PutUint16(operation[4:6], 0x0045) // firmware bit + buttons 1 and 3
	binary.BigEndian.PutUint16(operation[6:8], 320)
	binary.BigEndian.PutUint16(operation[8:10], 200)

	decoder := new(inputDecoder)
	event, changed, err := decoder.pointer(operation)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || event.Kind != InputPointer || event.X != 320 || event.Y != 200 || event.Buttons != 5 {
		t.Fatalf("unexpected pointer event: %#v", event)
	}
	if _, changed, err := decoder.pointer(operation); err != nil || changed {
		t.Fatalf("unchanged pointer report: changed=%v err=%v", changed, err)
	}
}

func TestPointerReportDecodesSignedWheelDelta(t *testing.T) {
	var decoder inputDecoder
	operation := make([]byte, 12)
	operation[0] = 0xC2
	binary.BigEndian.PutUint16(operation[4:6], 0x0040)
	binary.BigEndian.PutUint16(operation[6:8], 320)
	binary.BigEndian.PutUint16(operation[8:10], 200)
	binary.BigEndian.PutUint16(operation[10:12], uint16(1))
	event, changed, err := decoder.pointer(operation)
	if err != nil || !changed || event.Wheel != 1 {
		t.Fatalf("wheel up event=%#v changed=%v err=%v", event, changed, err)
	}
	binary.BigEndian.PutUint16(operation[10:12], uint16(0xFFFF))
	event, changed, err = decoder.pointer(operation)
	if err != nil || !changed || event.Wheel != -1 {
		t.Fatalf("wheel down event=%#v changed=%v err=%v", event, changed, err)
	}
}

func TestRejectsShortInputOperations(t *testing.T) {
	var decoder inputDecoder
	if _, err := decoder.keyboard([]byte{0xC1}); err == nil {
		t.Fatal("expected short keyboard operation to fail")
	}
	if _, _, err := decoder.pointer([]byte{0xC2}); err == nil {
		t.Fatal("expected short pointer operation to fail")
	}
}
