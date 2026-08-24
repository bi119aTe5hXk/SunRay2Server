// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"testing"
)

func TestBestTileSizeFitsDatagram(t *testing.T) {
	w, h := bestTileSize(1280, 1024)
	if round4(w*3)*h > maxRGBPayload {
		t.Fatalf("tile %dx%d exceeds payload", w, h)
	}
	if w < 1 || h < 1 {
		t.Fatalf("invalid tile %dx%d", w, h)
	}
}

func TestChangedRegionClippingGeometry(t *testing.T) {
	source := image.Rect(0, 0, 1920, 1080)
	visible := image.Rect(source.Min.X, source.Min.Y, source.Min.X+min(source.Dx(), 1400), source.Min.Y+min(source.Dy(), 1050))
	if got := image.Rect(1300, 1000, 1500, 1100).Intersect(visible); got != image.Rect(1300, 1000, 1400, 1050) {
		t.Fatalf("clipped region = %v", got)
	}
}
