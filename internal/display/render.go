// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"fmt"
	"image"
	"image/color"
)

const maxRGBPayload = maxDatagramSize - packetHeaderSize - 12

func (c *Client) ShowImage(screenWidth, screenHeight int, img image.Image) error {
	if screenWidth < 1 || screenHeight < 1 {
		return fmt.Errorf("invalid screen size %dx%d", screenWidth, screenHeight)
	}
	if img == nil || img.Bounds().Empty() {
		return fmt.Errorf("image is empty")
	}
	for _, op := range []Operation{
		Bounds(screenWidth, screenHeight),
		Fill(0, 0, screenWidth, screenHeight, color.Black),
		InvisibleCursor(),
	} {
		if err := c.Send(op); err != nil {
			return err
		}
	}

	source := img.Bounds()
	visibleWidth := min(source.Dx(), screenWidth)
	visibleHeight := min(source.Dy(), screenHeight)
	source = image.Rect(source.Min.X, source.Min.Y, source.Min.X+visibleWidth, source.Min.Y+visibleHeight)
	destination := image.Pt((screenWidth-visibleWidth)/2, (screenHeight-visibleHeight)/2)

	tileWidth, tileHeight := bestTileSize(visibleWidth, visibleHeight)
	for y := 0; y < visibleHeight; y += tileHeight {
		h := min(tileHeight, visibleHeight-y)
		for x := 0; x < visibleWidth; x += tileWidth {
			w := min(tileWidth, visibleWidth-x)
			rect := image.Rect(source.Min.X+x, source.Min.Y+y, source.Min.X+x+w, source.Min.Y+y+h)
			op, err := BitmapRGB(img, rect, image.Pt(destination.X+x, destination.Y+y))
			if err != nil {
				return err
			}
			if err := c.Send(op); err != nil {
				return err
			}
		}
	}
	return nil
}

func bestTileSize(width, height int) (int, int) {
	bestW, bestH := 1, 1
	bestPackets := width * height
	maxWidth := min(width, maxRGBPayload/3)
	for w := 1; w <= maxWidth; w++ {
		h := max(1, maxRGBPayload/round4(w*3))
		h = min(h, height)
		packets := (width + w - 1) / w * ((height + h - 1) / h)
		if packets < bestPackets || packets == bestPackets && w > bestW {
			bestW, bestH, bestPackets = w, h, packets
		}
	}
	return bestW, bestH
}

func TestPattern(width, height int) image.Image {
	width = max(width, 64)
	height = max(height, 64)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var c color.RGBA
			switch x * 6 / width {
			case 0:
				c = color.RGBA{R: 230, G: 55, B: 55, A: 255}
			case 1:
				c = color.RGBA{R: 240, G: 185, B: 45, A: 255}
			case 2:
				c = color.RGBA{R: 45, G: 190, B: 95, A: 255}
			case 3:
				c = color.RGBA{R: 45, G: 165, B: 220, A: 255}
			case 4:
				c = color.RGBA{R: 85, G: 90, B: 210, A: 255}
			default:
				c = color.RGBA{R: 180, G: 65, B: 190, A: 255}
			}
			if (x/32+y/32)%2 == 0 {
				c.R = byte(int(c.R) * 4 / 5)
				c.G = byte(int(c.G) * 4 / 5)
				c.B = byte(int(c.B) * 4 / 5)
			}
			if x < 6 || y < 6 || x >= width-6 || y >= height-6 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
