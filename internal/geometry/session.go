// SPDX-License-Identifier: GPL-2.0-or-later

// Package geometry implements the interactive Sun Ray viewport calibration
// session.
package geometry

import (
	"context"
	"fmt"
	"log/slog"

	"sunray2server/internal/display"
)

const (
	hidR     = 0x15
	hidEnter = 0x28
	hidRight = 0x4F
	hidLeft  = 0x50
	hidDown  = 0x51
	hidUp    = 0x52

	modifierControl = 0x01 | 0x10
	modifierShift   = 0x02 | 0x20
	minimumGeometry = 64
	maximumGeometry = 8192
)

type Config struct {
	Width   int
	Height  int
	Logger  *slog.Logger
	OnFrame func(width, height, clearWidth, clearHeight int) error
}

type Session struct {
	config Config
	input  chan display.InputEvent
}

func NewSession(config Config) *Session {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Session{config: config, input: make(chan display.InputEvent, 32)}
}

func (s *Session) HandleInput(event display.InputEvent) {
	if event.Kind != display.InputKey || !event.Pressed || !geometryKey(event.HID) {
		return
	}
	select {
	case s.input <- event:
	default:
	}
}

func (s *Session) Run(ctx context.Context) error {
	if s.config.Width < minimumGeometry || s.config.Height < minimumGeometry {
		return fmt.Errorf("invalid initial geometry %dx%d", s.config.Width, s.config.Height)
	}
	if s.config.OnFrame == nil {
		return fmt.Errorf("geometry session requires OnFrame")
	}
	initialWidth, initialHeight := s.config.Width, s.config.Height
	width, height := initialWidth, initialHeight
	clearWidth, clearHeight := width, height
	if err := s.draw(width, height, clearWidth, clearHeight); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-s.input:
			if event.HID == hidEnter {
				s.config.Logger.Info("geometry calibration value", "width", width, "height", height)
				continue
			}
			newWidth, newHeight := adjustGeometry(width, height, initialWidth, initialHeight, event)
			if newWidth == width && newHeight == height {
				continue
			}
			width, height = newWidth, newHeight
			clearWidth, clearHeight = max(clearWidth, width), max(clearHeight, height)
			s.config.Logger.Info("geometry calibration changed", "width", width, "height", height)
			if err := s.draw(width, height, clearWidth, clearHeight); err != nil {
				return err
			}
		}
	}
}

func (s *Session) draw(width, height, clearWidth, clearHeight int) error {
	return s.config.OnFrame(width, height, clearWidth, clearHeight)
}

func geometryKey(hid uint8) bool {
	return hid == hidR || hid == hidEnter || hid == hidRight || hid == hidLeft || hid == hidDown || hid == hidUp
}

func adjustGeometry(width, height, initialWidth, initialHeight int, event display.InputEvent) (int, int) {
	step := 10
	if event.Modifiers&modifierShift != 0 {
		step = 1
	} else if event.Modifiers&modifierControl != 0 {
		step = 100
	}
	switch event.HID {
	case hidLeft:
		width -= step
	case hidRight:
		width += step
	case hidUp:
		height -= step
	case hidDown:
		height += step
	case hidR:
		width, height = initialWidth, initialHeight
	}
	return min(max(width, minimumGeometry), maximumGeometry), min(max(height, minimumGeometry), maximumGeometry)
}
