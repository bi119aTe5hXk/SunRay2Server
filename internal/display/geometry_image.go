// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
)

var (
	geometryBackground = color.RGBA{R: 8, G: 12, B: 20, A: 255}
	geometryGrid       = color.RGBA{R: 35, G: 48, B: 64, A: 255}
	geometryMajorGrid  = color.RGBA{R: 58, G: 78, B: 98, A: 255}
	geometryTop        = color.RGBA{R: 255, G: 70, B: 70, A: 255}
	geometryRight      = color.RGBA{R: 80, G: 230, B: 110, A: 255}
	geometryBottom     = color.RGBA{R: 75, G: 135, B: 255, A: 255}
	geometryLeft       = color.RGBA{R: 255, G: 220, B: 70, A: 255}
)

// GeometryTestImage draws an edge-to-edge calibration target. If every colored
// edge is visible without panning, width and height match the physical viewport.
func GeometryTestImage(width, height int) *image.RGBA {
	width = max(width, 64)
	height = max(height, 64)
	bounds := image.Rect(0, 0, width, height)
	result := image.NewRGBA(bounds)
	draw.Draw(result, bounds, image.NewUniform(geometryBackground), image.Point{}, draw.Src)

	for x := 50; x < width; x += 50 {
		gridColor := geometryGrid
		if x%100 == 0 {
			gridColor = geometryMajorGrid
		}
		fillRectangle(result, image.Rect(x, 0, x+1, height), gridColor)
	}
	for y := 50; y < height; y += 50 {
		gridColor := geometryGrid
		if y%100 == 0 {
			gridColor = geometryMajorGrid
		}
		fillRectangle(result, image.Rect(0, y, width, y+1), gridColor)
	}

	border := min(10, max(4, min(width, height)/100))
	fillRectangle(result, image.Rect(0, 0, width, border), geometryTop)
	fillRectangle(result, image.Rect(width-border, 0, width, height), geometryRight)
	fillRectangle(result, image.Rect(0, height-border, width, height), geometryBottom)
	fillRectangle(result, image.Rect(0, 0, border, height), geometryLeft)

	centerX, centerY := width/2, height/2
	fillRectangle(result, image.Rect(centerX-1, border, centerX+1, height-border), color.RGBA{R: 130, G: 145, B: 165, A: 255})
	fillRectangle(result, image.Rect(border, centerY-1, width-border, centerY+1), color.RGBA{R: 130, G: 145, B: 165, A: 255})

	lines := []string{
		"GEOMETRY TEST",
		fmt.Sprintf("CURRENT %d X %d", width, height),
		"LEFT RIGHT CHANGE WIDTH",
		"UP DOWN CHANGE HEIGHT",
		"STEP 10 PX  SHIFT 1 PX  CTRL 100 PX",
		"R RESET  ENTER LOG VALUE",
	}
	maxChars := 1
	for _, line := range lines {
		maxChars = max(maxChars, len(line))
	}
	scale := min((width-60)/(maxChars*6), (height-60)/(len(lines)*9))
	scale = min(max(scale, 1), 6)
	lineHeight := 9 * scale
	totalHeight := len(lines)*lineHeight - 2*scale
	panelWidth := min(width-2*border, maxChars*6*scale+4*scale)
	panelHeight := min(height-2*border, totalHeight+4*scale)
	panel := image.Rect((width-panelWidth)/2, (height-panelHeight)/2, (width+panelWidth)/2, (height+panelHeight)/2)
	fillRectangle(result, panel, color.RGBA{R: 4, G: 7, B: 12, A: 255})

	y := (height - totalHeight) / 2
	for index, line := range lines {
		lineColor := color.RGBA{R: 220, G: 230, B: 240, A: 255}
		if index == 1 {
			lineColor = color.RGBA{R: 255, G: 225, B: 90, A: 255}
		}
		lineWidth := bitmapTextWidth(line, scale)
		drawBitmapText(result, (width-lineWidth)/2, y, scale, line, lineColor)
		y += lineHeight
	}
	return result
}

func fillRectangle(dst draw.Image, rectangle image.Rectangle, value color.Color) {
	draw.Draw(dst, rectangle.Intersect(dst.Bounds()), image.NewUniform(value), image.Point{}, draw.Src)
}
