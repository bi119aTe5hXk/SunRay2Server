// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"testing"
)

func TestCardStatusImagePreservesSizeAndDraws(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 640, 360))
	result := CardStatusImage(base, "JavaBadge", "00144fd19044", "insert")
	if result.Bounds() != base.Bounds() {
		t.Fatalf("bounds = %v, want %v", result.Bounds(), base.Bounds())
	}
	if got := result.At(320, 180); got == base.At(320, 180) {
		t.Fatal("card overlay did not alter the center of the image")
	}
}

func TestPrintableLabel(t *testing.T) {
	if got := printableLabel("JavaBadge", "UNKNOWN"); got != "JAVABADGE" {
		t.Fatalf("got %q", got)
	}
	if got := printableLabel("", "UNKNOWN"); got != "UNKNOWN" {
		t.Fatalf("got %q", got)
	}
}
