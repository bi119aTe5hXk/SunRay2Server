// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"fmt"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
)

const defaultFontSize = 20

type terminalFont struct {
	face       font.Face
	cellWidth  int
	cellHeight int
	ascent     int
}

func loadTerminalFont(path string, size float64) (*terminalFont, error) {
	contents := gomono.TTF
	if path != "" {
		var err error
		contents, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read terminal font %s: %w", path, err)
		}
	}
	collection, err := opentype.ParseCollection(contents)
	if err != nil {
		return nil, fmt.Errorf("parse terminal font: %w", err)
	}
	parsed, err := collection.Font(0)
	if err != nil {
		return nil, fmt.Errorf("select terminal font: %w", err)
	}
	if size <= 0 {
		size = defaultFontSize
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("create terminal font face: %w", err)
	}
	metrics := face.Metrics()
	cellWidth := font.MeasureString(face, "M").Ceil()
	cellHeight := metrics.Height.Ceil()
	if cellWidth < 1 || cellHeight < 1 {
		face.Close()
		return nil, fmt.Errorf("terminal font has invalid metrics %dx%d", cellWidth, cellHeight)
	}
	return &terminalFont{face: face, cellWidth: cellWidth, cellHeight: cellHeight, ascent: metrics.Ascent.Ceil()}, nil
}

func mustDefaultTerminalFont() *terminalFont {
	value, err := loadTerminalFont("", defaultFontSize)
	if err != nil {
		panic(err)
	}
	return value
}
