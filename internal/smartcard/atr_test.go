// SPDX-License-Identifier: GPL-2.0-or-later

package smartcard

import (
	"errors"
	"testing"
)

func TestParseT0ATR(t *testing.T) {
	atr, consumed, err := ParseATR([]byte{0x3B, 0x03, 0x11, 0x22, 0x33})
	if err != nil {
		t.Fatal(err)
	}
	if consumed != 5 || atr.ProtocolNames() != "T=0" || atr.HistoricalHex() != "112233" {
		t.Fatalf("unexpected ATR: %#v, consumed=%d", atr, consumed)
	}
}

func TestParseT1ATR(t *testing.T) {
	atr, consumed, err := ParseATR([]byte{0x3B, 0x80, 0x01, 0x81})
	if err != nil {
		t.Fatal(err)
	}
	if consumed != 4 || atr.ProtocolNames() != "T=1" {
		t.Fatalf("unexpected ATR: %#v, consumed=%d", atr, consumed)
	}
}

func TestParseATRNeedsMoreData(t *testing.T) {
	_, _, err := ParseATR([]byte{0x3B, 0x80, 0x01})
	if !errors.Is(err, ErrNeedMore) {
		t.Fatalf("got %v, want ErrNeedMore", err)
	}
}

func TestFindATRsInFramedTraffic(t *testing.T) {
	data := []byte{0x99, 0x02, 0x00, 0x3B, 0x80, 0x01, 0x81, 0x55}
	found := FindATRs(data)
	if len(found) != 1 || found[0].Hex() != "3B800181" {
		t.Fatalf("unexpected ATRs: %#v", found)
	}
}

func TestRejectsBadTCK(t *testing.T) {
	if _, _, err := ParseATR([]byte{0x3B, 0x80, 0x01, 0x00}); err == nil {
		t.Fatal("expected bad checksum to fail")
	}
}
