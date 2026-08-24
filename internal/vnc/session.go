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
	ScaleToFit   bool
	Logger       *slog.Logger
	OnFrame      func(frame *image.RGBA, changed []image.Rectangle, resized bool) error
}

// Session maintains one reconnecting VNC client and accepts Sun Ray input even
// while a connection attempt is in progress.
type Session struct {
	config         Config
	mu             sync.RWMutex
	current        *connection
	scaleMu        sync.Mutex
	scaled         *image.RGBA
	sourceSize     image.Point
	pointerMu      sync.Mutex
	pointerSeen    bool
	pointerButtons uint8
	pointerEvents  chan display.InputEvent
}

func NewSession(config Config) *Session {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Session{config: config, pointerEvents: make(chan display.InputEvent, 1)}
}

func (s *Session) Run(ctx context.Context) {
	go s.pointerLoop(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		requestWidth, requestHeight := s.config.ScreenWidth, s.config.ScreenHeight
		if s.config.ScaleToFit {
			// Scaling requires the complete remote framebuffer, not a top-left
			// crop limited to the physical Sun Ray canvas.
			requestWidth, requestHeight = 0, 0
		}
		conn, desktop, err := dial(ctx, s.config.Address, s.config.Password, requestWidth, requestHeight, s.handleFrame)
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
		displayWidth, displayHeight := s.config.ScreenWidth, s.config.ScreenHeight
		if displayWidth == 0 || displayHeight == 0 {
			displayWidth, displayHeight = width, height
		}
		s.config.Logger.Info("VNC session connected", "server", s.config.Address, "desktop", desktop,
			"framebuffer_resolution", image.Pt(width, height), "display_resolution", image.Pt(displayWidth, displayHeight))

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
		s.pointerMu.Lock()
		buttonChanged := s.pointerSeen && event.Buttons != s.pointerButtons
		s.pointerSeen = true
		s.pointerButtons = event.Buttons
		if buttonChanged {
			select {
			case <-s.pointerEvents:
			default:
			}
		}
		s.pointerMu.Unlock()
		if buttonChanged {
			err = s.sendPointer(conn, event)
			break
		}
		select {
		case s.pointerEvents <- event:
		default:
			select {
			case <-s.pointerEvents:
			default:
			}
			select {
			case s.pointerEvents <- event:
			default:
			}
		}
	}
	if err != nil {
		s.config.Logger.Debug("VNC input forwarding failed", "error", err)
	}
}

func (s *Session) pointerLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second / 60)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case event := <-s.pointerEvents:
				s.mu.RLock()
				conn := s.current
				s.mu.RUnlock()
				if conn != nil {
					if err := s.sendPointer(conn, event); err != nil {
						s.config.Logger.Debug("VNC pointer forwarding failed", "error", err)
					}
				}
			default:
			}
		}
	}
}

func (s *Session) sendPointer(conn *connection, event display.InputEvent) error {
	width, height := conn.size()
	screenWidth, screenHeight := s.config.ScreenWidth, s.config.ScreenHeight
	if screenWidth == 0 || screenHeight == 0 {
		screenWidth, screenHeight = width, height
	}
	x, y := 0, 0
	if s.config.ScaleToFit {
		x, y = translateScaledPoint(int(event.X), int(event.Y), screenWidth, screenHeight, width, height)
	} else {
		x = translateCoordinate(int(event.X), screenWidth, width)
		y = translateCoordinate(int(event.Y), screenHeight, height)
	}
	return conn.sendPointer(event.Buttons, uint16(x), uint16(y))
}

func (s *Session) handleFrame(frame *image.RGBA, changed []image.Rectangle, resized bool) error {
	if !s.config.ScaleToFit {
		if s.config.OnFrame == nil {
			return nil
		}
		return s.config.OnFrame(frame, changed, resized)
	}
	s.scaleMu.Lock()
	defer s.scaleMu.Unlock()
	sourceSize := frame.Bounds().Size()
	full := s.scaled == nil || s.sourceSize != sourceSize || resized
	if full {
		s.scaled = image.NewRGBA(image.Rect(0, 0, s.config.ScreenWidth, s.config.ScreenHeight))
		s.sourceSize = sourceSize
	}
	mapped := scaleFramebuffer(s.scaled, frame, changed, full)
	if s.config.OnFrame == nil || len(mapped) == 0 {
		return nil
	}
	return s.config.OnFrame(s.scaled, mapped, full)
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

func translateScaledPoint(x, y, screenWidth, screenHeight, framebufferWidth, framebufferHeight int) (int, int) {
	fit := fitRectangle(image.Rect(0, 0, framebufferWidth, framebufferHeight), image.Rect(0, 0, screenWidth, screenHeight))
	x = min(max(x, fit.Min.X), fit.Max.X-1)
	y = min(max(y, fit.Min.Y), fit.Max.Y-1)
	x = (x - fit.Min.X) * framebufferWidth / fit.Dx()
	y = (y - fit.Min.Y) * framebufferHeight / fit.Dy()
	return min(x, framebufferWidth-1), min(y, framebufferHeight-1)
}
