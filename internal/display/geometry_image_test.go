// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"image/color"
	"testing"
)

func TestGeometryTestImageUsesRequestedBoundsAndColoredEdges(t *testing.T) {
	result := GeometryTestImage(1400, 1050)
	if result.Bounds() != image.Rect(0, 0, 1400, 1050) {
		t.Fatalf("bounds = %v", result.Bounds())
	}
	tests := []struct {
		point image.Point
		want  color.RGBA
	}{
		{image.Pt(700, 2), geometryTop},
		{image.Pt(1397, 525), geometryRight},
		{image.Pt(700, 1047), geometryBottom},
		{image.Pt(2, 525), geometryLeft},
	}
	for _, test := range tests {
		if got := result.RGBAAt(test.point.X, test.point.Y); got != test.want {
			t.Errorf("pixel %v = %#v, want %#v", test.point, got, test.want)
		}
	}
}

func TestGeometryLabelImageIsSmall(t *testing.T) {
	result := GeometryLabelImage(1400, 1050)
	if result.Bounds().Dx() > 400 || result.Bounds().Dy() > 100 {
		t.Fatalf("label bounds = %v, want a small partial bitmap", result.Bounds())
	}
}
