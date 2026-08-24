// SPDX-License-Identifier: GPL-2.0-or-later

package vnc

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"net"
	"sync"
	"time"

	"sunray2server/internal/display"
)

type Config struct {
	Address      string
	Password     string
	ScreenWidth  int
	ScreenHeight int
	Logger       *slog.Logger
	OnFrame      func(frame *image.RGBA, changed image.Rectangle, resized bool) error
}

// Session maintains one reconnecting VNC client and accepts Sun Ray input even
// while a connection attempt is in progress.
type Session struct {
	config  Config
	mu      sync.RWMutex
	current *connection
}

func NewSession(config Config) *Session {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Session{config: config}
}

func (s *Session) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		conn, desktop, err := dial(ctx, s.config.Address, s.config.Password, s.config.OnFrame)
		if err != nil {
			s.config.Logger.Warn("VNC connection failed", "server", s.config.Address, "error", err)
			if !waitForRetry(ctx) {
				return
			}
			continue
		}
		s.mu.Lock()
		s.current = conn
		s.mu.Unlock()
		width, height := conn.size()
		s.config.Logger.Info("VNC session connected", "server", s.config.Address, "desktop", desktop, "resolution", image.Pt(width, height))

		closed := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				conn.Close()
			case <-closed:
			}
		}()
		err = conn.serve()
		close(closed)
		conn.Close()
		s.mu.Lock()
		if s.current == conn {
			s.current = nil
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, net.ErrClosed) {
			s.config.Logger.Warn("VNC session disconnected", "server", s.config.Address, "error", err)
		}
		if !waitForRetry(ctx) {
			return
		}
	}
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// HandleInput forwards one decoded Sun Ray event to the active RFB connection.
func (s *Session) HandleInput(event display.InputEvent) {
	s.mu.RLock()
	conn := s.current
	s.mu.RUnlock()
	if conn == nil {
		return
	}

	var err error
	switch event.Kind {
	case display.InputKey:
		keysym, ok := keysymForHID(event.HID)
		if !ok {
			s.config.Logger.Debug("VNC ignored unmapped HID key", "hid", event.HID)
			return
		}
		err = conn.sendKey(keysym, event.Pressed)
	case display.InputPointer:
		width, height := conn.size()
		x := translateCoordinate(int(event.X), s.config.ScreenWidth, width)
		y := translateCoordinate(int(event.Y), s.config.ScreenHeight, height)
		err = conn.sendPointer(event.Buttons, uint16(x), uint16(y))
	}
	if err != nil {
		s.config.Logger.Debug("VNC input forwarding failed", "error", err)
	}
}

func translateCoordinate(value, screenSize, framebufferSize int) int {
	if framebufferSize < 1 {
		return 0
	}
	offset := 0
	if screenSize > framebufferSize {
		offset = (screenSize - framebufferSize) / 2
	}
	value -= offset
	return min(max(value, 0), framebufferSize-1)
}
