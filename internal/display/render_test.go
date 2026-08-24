// SPDX-License-Identifier: GPL-2.0-or-later

package display

import "testing"

func TestBestTileSizeFitsDatagram(t *testing.T) {
	w, h := bestTileSize(1280, 1024)
	if round4(w*3)*h > maxRGBPayload {
		t.Fatalf("tile %dx%d exceeds payload", w, h)
	}
	if w < 1 || h < 1 {
		t.Fatalf("invalid tile %dx%d", w, h)
	}
}
