// SPDX-License-Identifier: GPL-2.0-or-later

package vnc

import (
	"image"
	"image/color"
	"image/draw"

	xdraw "golang.org/x/image/draw"
)

func scaleFramebuffer(destination, source *image.RGBA, changed []image.Rectangle, full bool) []image.Rectangle {
	fit := fitRectangle(source.Bounds(), destination.Bounds())
	if full {
		draw.Draw(destination, destination.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
		xdraw.ApproxBiLinear.Scale(destination, fit, source, source.Bounds(), draw.Src, nil)
		return []image.Rectangle{destination.Bounds()}
	}
	mapped := make([]image.Rectangle, 0, len(changed))
	for _, rectangle := range changed {
		rectangle = rectangle.Intersect(source.Bounds())
		if rectangle.Empty() {
			continue
		}
		destinationRectangle := mapRectangle(rectangle, source.Bounds(), fit)
		if destinationRectangle.Empty() {
			continue
		}
		xdraw.ApproxBiLinear.Scale(destination, destinationRectangle, source, rectangle, draw.Src, nil)
		mapped = append(mapped, destinationRectangle)
	}
	return mapped
}

func fitRectangle(source, destination image.Rectangle) image.Rectangle {
	if source.Empty() || destination.Empty() {
		return image.Rectangle{}
	}
	width := destination.Dx()
	height := source.Dy() * width / source.Dx()
	if height > destination.Dy() {
		height = destination.Dy()
		width = source.Dx() * height / source.Dy()
	}
	width, height = max(width, 1), max(height, 1)
	minX := destination.Min.X + (destination.Dx()-width)/2
	minY := destination.Min.Y + (destination.Dy()-height)/2
	return image.Rect(minX, minY, minX+width, minY+height)
}

func mapRectangle(rectangle, source, destination image.Rectangle) image.Rectangle {
	minX := destination.Min.X + (rectangle.Min.X-source.Min.X)*destination.Dx()/source.Dx()
	minY := destination.Min.Y + (rectangle.Min.Y-source.Min.Y)*destination.Dy()/source.Dy()
	maxX := destination.Min.X + divideCeil((rectangle.Max.X-source.Min.X)*destination.Dx(), source.Dx())
	maxY := destination.Min.Y + divideCeil((rectangle.Max.Y-source.Min.Y)*destination.Dy(), source.Dy())
	return image.Rect(minX, minY, maxX, maxY).Intersect(destination)
}

func divideCeil(value, divisor int) int {
	return (value + divisor - 1) / divisor
}
