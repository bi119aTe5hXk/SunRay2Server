// SPDX-License-Identifier: GPL-2.0-or-later

package auth

import "testing"

func TestParse(t *testing.T) {
	m, err := Parse("infoReq tokenSeq=7 startRes=1280x1024:1280x1024 id=card-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "infoReq" || m.Get("tokenSeq") != "7" || m.Get("id") != "card-1" {
		t.Fatalf("unexpected message: %#v", m)
	}
}

func TestParseRejectsMalformedField(t *testing.T) {
	if _, err := Parse("infoReq malformed"); err == nil {
		t.Fatal("expected malformed field to be rejected")
	}
}

func TestInfoResponse(t *testing.T) {
	got := InfoResponse("42")
	want := "connInf useReal=true encUpType=none tokenSeq=42 module=StartSession.m3 access=allowed token=pseudo.00212839eff9 encDownType=none"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
