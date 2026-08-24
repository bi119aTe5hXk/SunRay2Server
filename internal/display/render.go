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
	if err := c.SendBatch([]Operation{
		Bounds(screenWidth, screenHeight),
		Fill(0, 0, screenWidth, screenHeight, color.Black),
		InvisibleCursor(),
	}); err != nil {
		return err
	}
	// Bounds/cursor setup is complete and the following bitmap supersedes the
	// previous screen. Keep only this new frame available for retransmission.
	c.FlushHistory()

	source := img.Bounds()
	visibleWidth := min(source.Dx(), screenWidth)
	visibleHeight := min(source.Dy(), screenHeight)
	source = image.Rect(source.Min.X, source.Min.Y, source.Min.X+visibleWidth, source.Min.Y+visibleHeight)
	destination := image.Pt((screenWidth-visibleWidth)/2, (screenHeight-visibleHeight)/2)

	return c.sendEncodedBitmap(img, source, destination)
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
	c.FlushHistory()
	return c.sendEncodedBitmap(img, img.Bounds(), image.Point{})
}

// ShowCalibrationTarget renders the live geometry target mostly with compact
// ALP fill operations. A 1400x1050 raw RGB frame needs roughly 3,100 operation
// packets, so repeated full-frame calibration updates quickly wrap the Sun Ray
// operation sequence. Only the small central label remains a bitmap here.
func (c *Client) ShowCalibrationTarget(width, height, clearWidth, clearHeight int) error {
	c.renderMu.Lock()
	defer c.renderMu.Unlock()
	if width < 1 || height < 1 || clearWidth < width || clearHeight < height {
		return fmt.Errorf("invalid calibration geometry %dx%d within %dx%d", width, height, clearWidth, clearHeight)
	}
	c.FlushHistory()

	ops := []Operation{
		Bounds(clearWidth, clearHeight),
		Fill(0, 0, clearWidth, clearHeight, color.Black),
		Bounds(width, height),
		Fill(0, 0, width, height, geometryBackground),
	}
	for x := 50; x < width; x += 50 {
		gridColor := geometryGrid
		if x%100 == 0 {
			gridColor = geometryMajorGrid
		}
		ops = append(ops, Fill(x, 0, 1, height, gridColor))
	}
	for y := 50; y < height; y += 50 {
		gridColor := geometryGrid
		if y%100 == 0 {
			gridColor = geometryMajorGrid
		}
		ops = append(ops, Fill(0, y, width, 1, gridColor))
	}
	border := min(10, max(4, min(width, height)/100))
	ops = append(ops,
		Fill(0, 0, width, border, geometryTop),
		Fill(width-border, 0, border, height, geometryRight),
		Fill(0, height-border, width, border, geometryBottom),
		Fill(0, 0, border, height, geometryLeft),
		Fill(width/2-1, border, 2, height-border*2, color.RGBA{R: 130, G: 145, B: 165, A: 255}),
		Fill(border, height/2-1, width-border*2, 2, color.RGBA{R: 130, G: 145, B: 165, A: 255}),
		InvisibleCursor(),
	)
	for _, op := range ops {
		if err := c.Send(op); err != nil {
			return err
		}
	}

	label := GeometryLabelImage(width, height)
	labelBounds := label.Bounds()
	destination := image.Pt((width-labelBounds.Dx())/2, (height-labelBounds.Dy())/2)
	return c.sendBitmapTiles(label, labelBounds, destination)
}

func (c *Client) sendBitmapTiles(img image.Image, source image.Rectangle, destination image.Point) error {
	return c.sendBitmapTilesInto(img, source, destination, nil)
}

func (c *Client) sendBitmapTilesInto(img image.Image, source image.Rectangle, destination image.Point, ops []Operation) error {
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
			ops = append(ops, op)
		}
	}
	return c.SendBatch(ops)
}

type scanlineKind uint8

const (
	scanlineRGB scanlineKind = iota
	scanlineFill
	scanlineBiColor
)

type scanlineEncoding struct {
	kind   scanlineKind
	color0 color.RGBA
	color1 color.RGBA
}

// sendEncodedBitmap follows the proven jOpenRay strategy: classify scanlines,
// merge adjacent compatible lines, encode flat areas as Fill and two-color
// areas as one-bit bitmaps, and retain RGB as the lossless fallback.
func (c *Client) sendEncodedBitmap(img image.Image, source image.Rectangle, destination image.Point) error {
	ops, err := encodeBitmapOperations(img, source, destination)
	if err != nil {
		return err
	}
	return c.SendBatch(ops)
}

func encodeBitmapOperations(img image.Image, source image.Rectangle, destination image.Point) ([]Operation, error) {
	if source.Empty() {
		return nil, nil
	}
	lines := make([]scanlineEncoding, source.Dy())
	for y := source.Min.Y; y < source.Max.Y; y++ {
		lines[y-source.Min.Y] = classifyScanline(img, source.Min.X, source.Max.X, y)
	}
	ops := make([]Operation, 0, source.Dy())
	for first := 0; first < len(lines); {
		last := first + 1
		for last < len(lines) && compatibleScanlines(lines[first], lines[last]) {
			last++
		}
		rect := image.Rect(source.Min.X, source.Min.Y+first, source.Max.X, source.Min.Y+last)
		dst := image.Pt(destination.X, destination.Y+first)
		switch lines[first].kind {
		case scanlineFill:
			ops = append(ops, Fill(dst.X, dst.Y, rect.Dx(), rect.Dy(), lines[first].color0))
		case scanlineBiColor:
			var err error
			ops, err = appendBiColorTiles(ops, img, rect, dst, lines[first].color0, lines[first].color1)
			if err != nil {
				return nil, err
			}
		default:
			var err error
			ops, err = appendBitmapTiles(ops, img, rect, dst)
			if err != nil {
				return nil, err
			}
		}
		first = last
	}
	return ops, nil
}

func classifyScanline(img image.Image, minX, maxX, y int) scanlineEncoding {
	first := rgbaAt(img, minX, y)
	line := scanlineEncoding{kind: scanlineFill, color0: first}
	for x := minX + 1; x < maxX; x++ {
		pixel := rgbaAt(img, x, y)
		if pixel == line.color0 {
			continue
		}
		if line.kind == scanlineFill {
			line.kind = scanlineBiColor
			line.color1 = pixel
			continue
		}
		if pixel != line.color1 {
			line.kind = scanlineRGB
			return line
		}
	}
	if line.kind == scanlineBiColor && rgbaLess(line.color1, line.color0) {
		line.color0, line.color1 = line.color1, line.color0
	}
	return line
}

func rgbaLess(a, b color.RGBA) bool {
	if a.R != b.R {
		return a.R < b.R
	}
	if a.G != b.G {
		return a.G < b.G
	}
	if a.B != b.B {
		return a.B < b.B
	}
	return a.A < b.A
}

func compatibleScanlines(a, b scanlineEncoding) bool {
	if a.kind != b.kind {
		return false
	}
	if a.kind == scanlineRGB {
		return true
	}
	return a.color0 == b.color0 && (a.kind != scanlineBiColor || a.color1 == b.color1)
}

func appendBitmapTiles(ops []Operation, img image.Image, source image.Rectangle, destination image.Point) ([]Operation, error) {
	tileWidth, tileHeight := bestTileSize(source.Dx(), source.Dy())
	for y := 0; y < source.Dy(); y += tileHeight {
		h := min(tileHeight, source.Dy()-y)
		for x := 0; x < source.Dx(); x += tileWidth {
			w := min(tileWidth, source.Dx()-x)
			rect := image.Rect(source.Min.X+x, source.Min.Y+y, source.Min.X+x+w, source.Min.Y+y+h)
			op, err := BitmapRGB(img, rect, image.Pt(destination.X+x, destination.Y+y))
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
	}
	return ops, nil
}

func appendBiColorTiles(ops []Operation, img image.Image, source image.Rectangle, destination image.Point, c0, c1 color.RGBA) ([]Operation, error) {
	const maxBitmapBytes = maxDatagramSize - packetHeaderSize - 20
	tileWidth := min(source.Dx(), maxBitmapBytes*8)
	stride := (tileWidth + 7) / 8
	tileHeight := min(source.Dy(), max(1, maxBitmapBytes/stride))
	for y := 0; y < source.Dy(); y += tileHeight {
		h := min(tileHeight, source.Dy()-y)
		for x := 0; x < source.Dx(); x += tileWidth {
			w := min(tileWidth, source.Dx()-x)
			rect := image.Rect(source.Min.X+x, source.Min.Y+y, source.Min.X+x+w, source.Min.Y+y+h)
			op, err := BitmapBiColor(img, rect, image.Pt(destination.X+x, destination.Y+y), c0, c1)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
	}
	return ops, nil
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

	return c.sendEncodedBitmap(img, changed, changed.Min.Add(offset))
}

func bestTileSize(width, height int) (int, int) {
	if width < 1024 {
		bestW, bestH := 1, 1
		bestPackets := width * height
		maxWidth := min(width, maxRGBPayload/3)
		for w := 1; w <= maxWidth; w++ {
			h := min(height, max(1, maxRGBPayload/round4(w*3)))
			packets := (width + w - 1) / w * ((height + h - 1) / h)
			if packets < bestPackets || packets == bestPackets && w > bestW {
				bestW, bestH, bestPackets = w, h, packets
			}
		}
		return bestW, bestH
	}
	// Prefer the widest possible horizontal strips. The previous packet-count
	// optimizer selected roughly square tiles, making a full refresh visibly
	// appear as a grid. Scanline-oriented strips use almost the same number of
	// packets but produce a much less distracting top-to-bottom sweep.
	tileWidth := min(width, maxRGBPayload/3)
	tileHeight := min(height, max(1, maxRGBPayload/round4(tileWidth*3)))
	return tileWidth, tileHeight
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
