// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"image/color"
	"image/draw"
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

func TestEncodedBitmapUsesFillForSolidFrame(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1400, 1050))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 20, G: 30, B: 40, A: 255}), image.Point{}, draw.Src)
	lines := make([]scanlineEncoding, img.Bounds().Dy())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		lines[y] = classifyScanline(img, 0, img.Bounds().Dx(), y)
	}
	for y, line := range lines {
		if line.kind != scanlineFill {
			t.Fatalf("line %d kind = %d, want fill", y, line.kind)
		}
		if !compatibleScanlines(lines[0], line) {
			t.Fatalf("line %d is not merge compatible", y)
		}
	}
}

func TestEncodedBitmapOperationReduction(t *testing.T) {
	const width, height = 1400, 1050
	black := color.RGBA{A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	tests := []struct {
		name       string
		paint      func(*image.RGBA)
		maxOps     int
		maxPayload int
	}{
		{
			name: "solid",
			paint: func(img *image.RGBA) {
				draw.Draw(img, img.Bounds(), image.NewUniform(black), image.Point{}, draw.Src)
			},
			maxOps: 1, maxPayload: 16,
		},
		{
			name: "two-color",
			paint: func(img *image.RGBA) {
				for y := 0; y < height; y++ {
					for x := 0; x < width; x++ {
						pixel := black
						if (x/8+y/8)%2 != 0 {
							pixel = white
						}
						img.SetRGBA(x, y, pixel)
					}
				}
			},
			maxOps: 132, maxPayload: 190000,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, width, height))
			test.paint(img)
			ops, err := encodeBitmapOperations(img, img.Bounds(), image.Point{})
			if err != nil {
				t.Fatal(err)
			}
			payload := 0
			for _, op := range ops {
				payload += len(op.Bytes)
			}
			if len(ops) > test.maxOps || payload > test.maxPayload {
				t.Fatalf("encoded as %d operations / %d bytes, want <= %d / %d", len(ops), payload, test.maxOps, test.maxPayload)
			}
		})
	}
}

func BenchmarkEncodeBitmapOperations(b *testing.B) {
	const width, height = 1400, 1050
	black := color.RGBA{A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	tests := []struct {
		name  string
		paint func(*image.RGBA)
	}{
		{"solid", func(img *image.RGBA) {
			draw.Draw(img, img.Bounds(), image.NewUniform(black), image.Point{}, draw.Src)
		}},
		{"two-color", func(img *image.RGBA) {
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					if (x/8+y/8)%2 == 0 {
						img.SetRGBA(x, y, black)
					} else {
						img.SetRGBA(x, y, white)
					}
				}
			}
		}},
		{"full-color", func(img *image.RGBA) {
			for y := 0; y < height; y++ {
				for x := 0; x < width; x++ {
					img.SetRGBA(x, y, color.RGBA{R: byte(x), G: byte(y), B: byte(x + y), A: 255})
				}
			}
		}},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			img := image.NewRGBA(image.Rect(0, 0, width, height))
			test.paint(img)
			b.ResetTimer()
			for range b.N {
				ops, err := encodeBitmapOperations(img, img.Bounds(), image.Point{})
				if err != nil {
					b.Fatal(err)
				}
				payload := 0
				for _, op := range ops {
					payload += len(op.Bytes)
				}
				b.ReportMetric(float64(len(ops)), "operations")
				b.ReportMetric(float64(payload), "wire-bytes")
			}
		})
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
