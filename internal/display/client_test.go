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

func TestNACKFromZeroIncludesAnchorAndCompletion(t *testing.T) {
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
	_ = readTestPacket(t, receiver)

	nack := make([]byte, 32)
	nack[packetHeaderSize] = 0xC4
	binary.BigEndian.PutUint32(nack[24:28], 0)
	binary.BigEndian.PutUint32(nack[28:32], 1)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: client.LocalAddr().(*net.UDPAddr).Port}
	if _, err := receiver.WriteToUDP(nack, clientAddr); err != nil {
		t.Fatal(err)
	}

	wantCodes := []byte{opPad, opFill}
	wantSeqs := []uint16{0, 1}
	for i := range wantCodes {
		packet := readTestPacket(t, receiver)
		if got := packet[packetHeaderSize]; got != wantCodes[i] {
			t.Fatalf("packet %d opcode = %#x, want %#x", i, got, wantCodes[i])
		}
		if got := binary.BigEndian.Uint16(packet[packetHeaderSize+2 : packetHeaderSize+4]); got != wantSeqs[i] {
			t.Fatalf("packet %d sequence = %d, want %d", i, got, wantSeqs[i])
		}
	}
	completion := readTestPacket(t, receiver)
	padOffset := packetHeaderSize
	statusOffset := padOffset + len(Pad().Bytes)
	if got := completion[padOffset]; got != opPad {
		t.Fatalf("completion pad opcode = %#x, want %#x", got, opPad)
	}
	if got := binary.BigEndian.Uint16(completion[padOffset+2 : padOffset+4]); got != 1 {
		t.Fatalf("completion pad sequence = %d, want 1", got)
	}
	if got := completion[statusOffset]; got != opResendDone {
		t.Fatalf("completion status opcode = %#x, want %#x", got, opResendDone)
	}
	if got := binary.BigEndian.Uint16(completion[statusOffset+2 : statusOffset+4]); got != 1 {
		t.Fatalf("completion status sequence = %d, want 1", got)
	}
	client.mu.Lock()
	opSeq := client.opSeq
	client.mu.Unlock()
	if opSeq != 1 {
		t.Fatalf("operation sequence advanced to %d after NACK, want 1", opSeq)
	}

	// Repeating the same NACK must not create another operation sequence. This
	// is the regression case for the terminal/server resend feedback loop.
	if _, err := receiver.WriteToUDP(nack, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readTestPacket(t, receiver) // Pad 0.
	_ = readTestPacket(t, receiver) // Fill 1.
	completion = readTestPacket(t, receiver)
	if got := binary.BigEndian.Uint16(completion[statusOffset+2 : statusOffset+4]); got != 1 {
		t.Fatalf("repeated completion status sequence = %d, want 1", got)
	}
	client.mu.Lock()
	opSeq = client.opSeq
	client.mu.Unlock()
	if opSeq != 1 {
		t.Fatalf("operation sequence advanced to %d after repeated NACK, want 1", opSeq)
	}
}

func TestOperationSequenceExtendsAcrossWireWrap(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
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

	client.mu.Lock()
	client.opSeq = 65535
	client.mu.Unlock()
	if err := client.Send(Fill(1, 2, 3, 4, testColor{})); err != nil {
		t.Fatal(err)
	}
	packet := readTestPacket(t, receiver)
	if got := binary.BigEndian.Uint16(packet[packetHeaderSize+2 : packetHeaderSize+4]); got != 0 {
		t.Fatalf("wrapped wire sequence = %d, want 0", got)
	}
	client.mu.Lock()
	_, stored := client.history[65536]
	extended := client.opSeq
	client.mu.Unlock()
	if !stored || extended != 65536 {
		t.Fatalf("extended sequence = %d, stored=%v; want 65536,true", extended, stored)
	}

	nack := make([]byte, 32)
	nack[packetHeaderSize] = 0xC4
	binary.BigEndian.PutUint32(nack[24:28], 65536)
	binary.BigEndian.PutUint32(nack[28:32], nackOpenEnded)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: client.LocalAddr().(*net.UDPAddr).Port}
	if _, err := receiver.WriteToUDP(nack, clientAddr); err != nil {
		t.Fatal(err)
	}
	replayed := readTestPacket(t, receiver)
	if got := replayed[packetHeaderSize]; got != opFill {
		t.Fatalf("replayed opcode = %#x, want %#x", got, opFill)
	}
	completion := readTestPacket(t, receiver)
	statusOffset := packetHeaderSize + len(Pad().Bytes)
	if got := binary.BigEndian.Uint16(completion[statusOffset+18 : statusOffset+20]); got != 0 {
		t.Fatalf("open-ended completion watermark = %#x, want replayed sequence 0", got)
	}
}

func TestWrappedNACKReplaysCurrentSequenceEpoch(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
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

	client.mu.Lock()
	client.opSeq = 65535
	client.mu.Unlock()
	for range 3 {
		if err := client.Send(Fill(1, 2, 3, 4, testColor{})); err != nil {
			t.Fatal(err)
		}
		_ = readTestPacket(t, receiver)
	}

	nack := make([]byte, 32)
	nack[packetHeaderSize] = 0xC4
	binary.BigEndian.PutUint32(nack[24:28], 0)
	binary.BigEndian.PutUint32(nack[28:32], 2)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: client.LocalAddr().(*net.UDPAddr).Port}
	if _, err := receiver.WriteToUDP(nack, clientAddr); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		packet := readTestPacket(t, receiver)
		if got := packet[packetHeaderSize]; got != opFill {
			t.Fatalf("wrapped replay %d opcode = %#x, want fill", i, got)
		}
		if got := binary.BigEndian.Uint16(packet[packetHeaderSize+2 : packetHeaderSize+4]); got != uint16(i) {
			t.Fatalf("wrapped replay %d sequence = %d", i, got)
		}
	}
	completion := readTestPacket(t, receiver)
	if got := completion[packetHeaderSize+len(Pad().Bytes)]; got != opResendDone {
		t.Fatalf("completion opcode = %#x, want %#x", got, opResendDone)
	}
}

func TestMissingNACKHistoryDoesNotSendCompletion(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
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
	_ = readTestPacket(t, receiver)
	client.mu.Lock()
	delete(client.history, 1)
	client.mu.Unlock()

	nack := make([]byte, 32)
	nack[packetHeaderSize] = 0xC4
	binary.BigEndian.PutUint32(nack[24:28], 1)
	binary.BigEndian.PutUint32(nack[28:32], 1)
	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: client.LocalAddr().(*net.UDPAddr).Port}
	if _, err := receiver.WriteToUDP(nack, clientAddr); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	if _, _, err := receiver.ReadFromUDP(buffer); err == nil {
		t.Fatal("missing NACK history unexpectedly produced a completion packet")
	} else if networkError, ok := err.(net.Error); !ok || !networkError.Timeout() {
		t.Fatalf("read after missing NACK history: %v", err)
	}
}

func TestExtendOperationSequence(t *testing.T) {
	tests := []struct {
		wire, current, want uint32
	}{
		{0, 65734, 65536},
		{198, 65734, 65734},
		{65530, 65541, 65530},
		{5, 65541, 65541},
		{70000, 71000, 70000},
		{65536, 27933, 0},
	}
	for _, test := range tests {
		if got := extendOperationSequence(test.wire, test.current); got != test.want {
			t.Errorf("extendOperationSequence(%d, %d) = %d, want %d", test.wire, test.current, got, test.want)
		}
	}
	if !validNACKRange(65530, 5) {
		t.Fatal("short wrapped NACK range was rejected")
	}
}

func TestFlushHistoryPreservesOperationSequence(t *testing.T) {
	client := &Client{
		opSeq:   42,
		history: map[uint32][]byte{41: {1}, 42: {2}},
	}
	client.FlushHistory()
	if len(client.history) != 0 {
		t.Fatalf("history length = %d, want 0", len(client.history))
	}
	if client.opSeq != 42 {
		t.Fatalf("operation sequence = %d, want 42", client.opSeq)
	}
}

func TestPacketPacingGroupsPacketsIntoBursts(t *testing.T) {
	client := &Client{delay: time.Nanosecond}
	for range packetBurstSize {
		client.pacePacketLocked()
	}
	if client.burstPackets != packetBurstSize {
		t.Fatalf("burst packets = %d, want %d", client.burstPackets, packetBurstSize)
	}
	client.pacePacketLocked()
	if client.burstPackets != 1 {
		t.Fatalf("new burst packets = %d, want 1", client.burstPackets)
	}
}

func TestResendStormClearsHistoryAndStartsCooldown(t *testing.T) {
	client := &Client{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		history: map[uint32][]byte{1: {1}, 2: {2}},
	}
	now := time.Now()
	for i := 0; i < resendStormLimit-1; i++ {
		if client.recordResendLocked(now, 1) {
			t.Fatalf("resend storm fired after %d requests", i+1)
		}
	}
	if !client.recordResendLocked(now, 1) {
		t.Fatal("resend storm did not fire at request limit")
	}
	if len(client.history) != 0 {
		t.Fatalf("history length after storm = %d, want 0", len(client.history))
	}
	if got := time.Unix(0, client.nackCooldownUntil.Load()); !got.After(now) {
		t.Fatalf("cooldown deadline = %v, want after %v", got, now)
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
