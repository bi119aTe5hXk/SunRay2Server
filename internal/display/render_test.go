// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"io"
	"log/slog"
	"net"
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
	if h != 1 {
		t.Fatalf("large framebuffer tile height = %d, want scanline strips", h)
	}
}

func TestChangedRegionClippingGeometry(t *testing.T) {
	source := image.Rect(0, 0, 1920, 1080)
	visible := image.Rect(source.Min.X, source.Min.Y, source.Min.X+min(source.Dx(), 1400), source.Min.Y+min(source.Dy(), 1050))
	if got := image.Rect(1300, 1000, 1500, 1100).Intersect(visible); got != image.Rect(1300, 1000, 1400, 1050) {
		t.Fatalf("clipped region = %v", got)
	}
}

func TestCalibrationTargetUsesCompactOperationCount(t *testing.T) {
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

	if err := client.ShowCalibrationTarget(1400, 1050, 1400, 1050); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	operations := client.opSeq
	client.mu.Unlock()
	if operations > 128 {
		t.Fatalf("calibration target used %d operations, want at most 128", operations)
	}
}
