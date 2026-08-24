// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseResolution(t *testing.T) {
	tests := []struct {
		value  string
		width  int
		height int
	}{
		{"1280x1024:1280x1024", 1280, 1024},
		{"1920x1080", 1920, 1080},
		{"bad", 800, 600},
		{"0x1080", 800, 600},
	}
	for _, test := range tests {
		width, height := parseResolution(test.value, 800, 600)
		if width != test.width || height != test.height {
			t.Errorf("parseResolution(%q) = %dx%d, want %dx%d", test.value, width, height, test.width, test.height)
		}
	}
}

func TestConfiguredDisplayGeometryOverridesTerminalResolution(t *testing.T) {
	width, height := parseResolution("1400x1050:1400x1050", 800, 600)
	configuredWidth, configuredHeight := 1280, 720
	if configuredWidth > 0 && configuredHeight > 0 {
		width, height = configuredWidth, configuredHeight
	}
	if width != 1280 || height != 720 {
		t.Fatalf("geometry = %dx%d", width, height)
	}
}

func TestCardSessionSlotUsesDefinitiveInsertEvents(t *testing.T) {
	tests := []struct {
		cardType string
		event    string
		slot     string
		ok       bool
	}{
		{"pseudo", "insert", "no-card", true},
		{"T1unknown", "insert", "card-present", true},
		{"card", "remove", "", false},
		{"pseudo", "remove", "", false},
	}
	for _, test := range tests {
		slot, ok := cardSessionSlot(test.cardType, test.event)
		if slot != test.slot || ok != test.ok {
			t.Errorf("cardSessionSlot(%q, %q) = %q, %v; want %q, %v", test.cardType, test.event, slot, ok, test.slot, test.ok)
		}
	}
}

func TestAuthenticationToDisplayEndToEnd(t *testing.T) {
	displayReceiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer displayReceiver.Close()

	authListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, err := New(Config{
		PacketDelay: 0,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, authListener) }()

	conn, err := net.Dial("tcp4", authListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := displayReceiver.LocalAddr().(*net.UDPAddr).Port
	if _, err := fmt.Fprintf(conn, "infoReq tokenSeq=5 event=insert type=JavaBadge id=test-card sn=test-terminal hw=SunRay2 startRes=800x600 pn=%d\n", port); err != nil {
		t.Fatal(err)
	}
	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response, "connInf") || !strings.Contains(response, "tokenSeq=5") {
		t.Fatalf("unexpected authentication response: %q", response)
	}
	if _, err := fmt.Fprintf(conn, "connRsp pn=%d sn=test-terminal\n", port); err != nil {
		t.Fatal(err)
	}

	if err := displayReceiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 1500)
	n, _, err := displayReceiver.ReadFromUDP(packet)
	if err != nil {
		t.Fatal(err)
	}
	if n < 16+20 || packet[16] != 0xA8 {
		t.Fatalf("first display packet is not bounds: %x", packet[:n])
	}
	if width := binary.BigEndian.Uint16(packet[24:26]); width != 800 {
		t.Fatalf("bounds width = %d, want 800", width)
	}
	if height := binary.BigEndian.Uint16(packet[26:28]); height != 600 {
		t.Fatalf("bounds height = %d, want 600", height)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}
