// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
)

type textLine struct {
	text  string
	color color.Color
}

// CardStatusImage overlays the card reader state on the configured test image.
// The small built-in 5x7 font keeps this milestone dependency-free.
func CardStatusImage(base image.Image, cardType, cardID, event string) image.Image {
	if base == nil || base.Bounds().Empty() {
		base = TestPattern(640, 360)
	}
	bounds := image.Rect(0, 0, base.Bounds().Dx(), base.Bounds().Dy())
	result := image.NewRGBA(bounds)
	draw.Draw(result, bounds, base, base.Bounds().Min, draw.Src)

	cardType = printableLabel(cardType, "UNKNOWN")
	cardID = printableLabel(cardID, "UNKNOWN")
	event = printableLabel(event, "STATUS")

	title := "CARD DETECTED"
	footer := "EVENT: " + event
	if cardType == "PSEUDO" {
		title = "READER READY"
		footer = "INSERT A CARD"
	}
	if event == "REMOVE" {
		title = "CARD REMOVED"
		footer = "WAITING FOR CARD"
	}

	lines := []textLine{
		{text: title, color: color.RGBA{R: 255, G: 255, B: 255, A: 255}},
		{text: "TYPE: " + cardType, color: color.RGBA{R: 145, G: 215, B: 255, A: 255}},
		{text: "ID: " + cardID, color: color.RGBA{R: 255, G: 225, B: 90, A: 255}},
		{text: footer, color: color.RGBA{R: 170, G: 255, B: 170, A: 255}},
	}

	maxChars := 1
	for _, line := range lines {
		maxChars = max(maxChars, len(line.text))
	}
	scale := min((bounds.Dx()-40)/(maxChars*6), (bounds.Dy()-40)/(len(lines)*9))
	scale = min(max(scale, 1), 10)
	lineHeight := 9 * scale
	totalHeight := len(lines)*lineHeight - scale
	panel := image.Rect(10, max(10, (bounds.Dy()-totalHeight)/2-10), bounds.Dx()-10, min(bounds.Dy()-10, (bounds.Dy()+totalHeight)/2+10))
	draw.Draw(result, panel, &image.Uniform{C: color.RGBA{R: 5, G: 10, B: 18, A: 225}}, image.Point{}, draw.Over)

	y := (bounds.Dy() - totalHeight) / 2
	for _, line := range lines {
		lineWidth := bitmapTextWidth(line.text, scale)
		drawBitmapText(result, (bounds.Dx()-lineWidth)/2, y, scale, line.text, line.color)
		y += lineHeight
	}
	return result
}

func printableLabel(value, fallback string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	var result strings.Builder
	for _, r := range value {
		if _, ok := glyphs[r]; ok {
			result.WriteRune(r)
		} else {
			result.WriteRune('?')
		}
	}
	return result.String()
}

func bitmapTextWidth(text string, scale int) int {
	if text == "" {
		return 0
	}
	return len([]rune(text))*6*scale - scale
}

func drawBitmapText(dst *image.RGBA, x, y, scale int, text string, c color.Color) {
	for _, r := range text {
		glyph, ok := glyphs[r]
		if !ok {
			glyph = glyphs['?']
		}
		for row, pattern := range glyph {
			for column, pixel := range pattern {
				if pixel != '1' {
					continue
				}
				rect := image.Rect(x+column*scale, y+row*scale, x+(column+1)*scale, y+(row+1)*scale)
				draw.Draw(dst, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
			}
		}
		x += 6 * scale
	}
}

var glyphs = map[rune][7]string{
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	'+': {"00000", "00100", "00100", "11111", "00100", "00100", "00000"},
	'.': {"00000", "00000", "00000", "00000", "00000", "01100", "01100"},
	':': {"00000", "01100", "01100", "00000", "01100", "01100", "00000"},
	'/': {"00001", "00010", "00100", "00100", "01000", "10000", "00000"},
	'?': {"01110", "10001", "00001", "00010", "00100", "00000", "00100"},
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "10000", "11110", "00001", "00001", "11110"},
	'6': {"01110", "10000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00001", "01110"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01111", "10000", "10000", "10000", "10000", "10000", "01111"},
	'D': {"11110", "10001", "10001", "10001", "10001", "10001", "11110"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01111", "10000", "10000", "10111", "10001", "10001", "01111"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'I': {"01110", "00100", "00100", "00100", "00100", "00100", "01110"},
	'J': {"00111", "00010", "00010", "00010", "10010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "10001", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "10101", "01010"},
	'X': {"10001", "10001", "01010", "00100", "01010", "10001", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}
