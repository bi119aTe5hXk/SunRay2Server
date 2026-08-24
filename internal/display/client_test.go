// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestClientDispatchesKeyboardAndPointerInput(t *testing.T) {
	client := &Client{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var events []InputEvent
	client.SetInputHandler(func(event InputEvent) {
		events = append(events, event)
	})

	packet := make([]byte, packetHeaderSize+16+12)
	keyboard := packet[packetHeaderSize : packetHeaderSize+16]
	keyboard[0] = 0xC1
	keyboard[8] = 0x04 // A
	pointer := packet[packetHeaderSize+16:]
	pointer[0] = 0xC2
	binary.BigEndian.PutUint16(pointer[4:6], 0x0041)
	binary.BigEndian.PutUint16(pointer[6:8], 640)
	binary.BigEndian.PutUint16(pointer[8:10], 480)

	client.handlePacket(packet)
	want := []InputEvent{
		keyInput(0x04, true, 0),
		{Kind: InputPointer, X: 640, Y: 480, Buttons: 1},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestUDPSendAndNACKResend(t *testing.T) {
	receiver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	remote := receiver.LocalAddr().(*net.UDPAddr)
	client, err := Open(remote.IP, remote.Port, 0, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Send(Fill(1, 2, 3, 4, testColor{})); err != nil {
		t.Fatal(err)
	}
	first := readTestPacket(t, receiver)
	if len(first) != packetHeaderSize+16 || first[packetHeaderSize] != opFill {
		t.Fatalf("unexpected display packet: %x", first)
	}
	opSeq := binary.BigEndian.Uint16(first[packetHeaderSize+2 : packetHeaderSize+4])

	nack := make([]byte, 32)
	nack[packetHeaderSize] = 0xC4
	binary.BigEndian.PutUint32(nack[24:28], uint32(opSeq))
	binary.BigEndian.PutUint32(nack[28:32], uint32(opSeq))
	clientPort := client.LocalAddr().(*net.UDPAddr).Port
	clientLoopback := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: clientPort}
	if _, err := receiver.WriteToUDP(nack, clientLoopback); err != nil {
		t.Fatal(err)
	}
	resent := readTestPacket(t, receiver)
	if binary.BigEndian.Uint16(resent[packetHeaderSize+2:packetHeaderSize+4]) != opSeq {
		t.Fatalf("resent operation sequence changed: %x", resent)
	}
	if binary.BigEndian.Uint16(resent[0:2]) == binary.BigEndian.Uint16(first[0:2]) {
		t.Fatal("resent packet sequence did not advance")
	}
}

type testColor struct{}

func (testColor) RGBA() (uint32, uint32, uint32, uint32) { return 0x1111, 0x2222, 0x3333, 0xFFFF }

func readTestPacket(t *testing.T, conn *net.UDPConn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	n, _, err := conn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buffer[:n]...)
}
