// SPDX-License-Identifier: GPL-2.0-or-later

package display

import (
	"fmt"
	"image"
	"image/color"
)

const maxRGBPayload = maxDatagramSize - packetHeaderSize - 12

func (c *Client) ShowImage(screenWidth, screenHeight int, img image.Image) error {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
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

	return c.sendBitmapTiles(img, source, destination)
}

// ShowCalibrationImage clears the largest geometry visited by an interactive
// calibration before applying the new pointer bounds. This prevents old edge
// markers from remaining visible when the candidate geometry is reduced.
func (c *Client) ShowCalibrationImage(width, height, clearWidth, clearHeight int, img image.Image) error {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if width < 1 || height < 1 || clearWidth < width || clearHeight < height {
		return fmt.Errorf("invalid calibration geometry %dx%d within %dx%d", width, height, clearWidth, clearHeight)
	}
	if img == nil || img.Bounds().Empty() {
		return fmt.Errorf("image is empty")
	}
	for _, op := range []Operation{
		Bounds(clearWidth, clearHeight),
		Fill(0, 0, clearWidth, clearHeight, color.Black),
		Bounds(width, height),
		InvisibleCursor(),
	} {
		if err := c.Send(op); err != nil {
			return err
		}
	}
	return c.sendBitmapTiles(img, img.Bounds(), image.Point{})
}

func (c *Client) sendBitmapTiles(img image.Image, source image.Rectangle, destination image.Point) error {
	tileWidth, tileHeight := bestTileSize(source.Dx(), source.Dy())
	for y := 0; y < source.Dy(); y += tileHeight {
		h := min(tileHeight, source.Dy()-y)
		for x := 0; x < source.Dx(); x += tileWidth {
			w := min(tileWidth, source.Dx()-x)
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

// ShowImageRegion updates only a changed portion of an image. The image uses
// the same centered/cropped placement as ShowImage, allowing remote framebuffer
// updates to avoid retransmitting the entire screen.
func (c *Client) ShowImageRegion(screenWidth, screenHeight int, img image.Image, changed image.Rectangle) error {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if screenWidth < 1 || screenHeight < 1 {
		return fmt.Errorf("invalid screen size %dx%d", screenWidth, screenHeight)
	}
	if img == nil || img.Bounds().Empty() {
		return fmt.Errorf("image is empty")
	}

	source := img.Bounds()
	visibleWidth := min(source.Dx(), screenWidth)
	visibleHeight := min(source.Dy(), screenHeight)
	visible := image.Rect(source.Min.X, source.Min.Y, source.Min.X+visibleWidth, source.Min.Y+visibleHeight)
	changed = changed.Intersect(visible)
	if changed.Empty() {
		return nil
	}
	offset := image.Pt((screenWidth-visibleWidth)/2-source.Min.X, (screenHeight-visibleHeight)/2-source.Min.Y)

	tileWidth, tileHeight := bestTileSize(changed.Dx(), changed.Dy())
	for y := changed.Min.Y; y < changed.Max.Y; y += tileHeight {
		h := min(tileHeight, changed.Max.Y-y)
		for x := changed.Min.X; x < changed.Max.X; x += tileWidth {
			w := min(tileWidth, changed.Max.X-x)
			rect := image.Rect(x, y, x+w, y+h)
			op, err := BitmapRGB(img, rect, image.Pt(x+offset.X, y+offset.Y))
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
