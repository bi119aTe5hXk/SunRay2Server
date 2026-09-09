// SPDX-License-Identifier: GPL-2.0-or-later

// Package web runs a Chromium application window in an isolated Xvfb display
// and exposes it to the existing framebuffer/input path through loopback VNC.
package web

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sunray2server/internal/display"
	"sunray2server/internal/vnc"
)

const xvfbStartupTimeout = 15 * time.Second

type Config struct {
	URL              string
	ScreenWidth      int
	ScreenHeight     int
	BrowserNoSandbox bool
	Interactive      bool
	ReloadInterval   time.Duration
	Logger           *slog.Logger
	OnFrame          func(frame *image.RGBA, changed []display.RegionUpdate, resized bool) error
}

// Session keeps the browser alive so JavaScript, WebSocket, SSE, and page
// timers continue to update normally. By default there is no page reload or
// screenshot timer; x11vnc reports framebuffer changes as they occur.
type Session struct {
	config  Config
	mu      sync.RWMutex
	current *vnc.Session
}

type processExit struct {
	name string
	err  error
}

func NewSession(config Config) *Session {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Session{config: config}
}

func (s *Session) HandleInput(event display.InputEvent) {
	if !s.config.Interactive {
		return
	}
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		current.HandleInput(event)
	}
}

func (s *Session) RequestFullFrame() {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		current.RequestFullFrame()
	}
}

func (s *Session) Run(ctx context.Context) error {
	if s.config.ScreenWidth < 1 || s.config.ScreenHeight < 1 {
		return fmt.Errorf("invalid web display resolution %dx%d", s.config.ScreenWidth, s.config.ScreenHeight)
	}
	xvfbPath, err := findExecutable("Xvfb", "/opt/X11/bin/Xvfb")
	if err != nil {
		return err
	}
	x11vncPath, err := findExecutable("x11vnc", "/opt/homebrew/bin/x11vnc", "/usr/local/bin/x11vnc")
	if err != nil {
		return err
	}
	chromiumPath, err := findExecutable(
		"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	)
	if err != nil {
		return err
	}

	runtimeDir, err := os.MkdirTemp("", "sunray-web-")
	if err != nil {
		return fmt.Errorf("create web runtime directory: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exits := make(chan processExit, 3)
	baseEnv := environmentWith(os.Environ(), map[string]string{
		"HOME": runtimeDir, "XDG_RUNTIME_DIR": runtimeDir,
		"XDG_CONFIG_HOME": filepath.Join(runtimeDir, "config"),
		"XDG_CACHE_HOME":  filepath.Join(runtimeDir, "cache"),
	})
	displayNumber, err := s.startXvfb(runCtx, xvfbPath, runtimeDir, baseEnv, exits)
	if err != nil {
		return err
	}
	displayName := ":" + displayNumber
	displayEnv := environmentWith(baseEnv, map[string]string{"DISPLAY": displayName})

	port, err := availableLoopbackPort()
	if err != nil {
		return err
	}
	if err := s.startProcess(runCtx, "x11vnc", x11vncPath, x11vncArguments(displayName, port, s.config.Interactive), displayEnv, exits); err != nil {
		return err
	}
	arguments := chromiumArguments(s.config, filepath.Join(runtimeDir, "profile"))
	if err := s.startProcess(runCtx, "chromium", chromiumPath, arguments, displayEnv, exits); err != nil {
		return err
	}

	bridge := vnc.NewSession(vnc.Config{
		Address:      net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		ScreenWidth:  s.config.ScreenWidth,
		ScreenHeight: s.config.ScreenHeight,
		ScaleToFit:   false,
		Logger:       s.config.Logger,
		OnFrame:      s.config.OnFrame,
	})
	s.mu.Lock()
	s.current = bridge
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.current == bridge {
			s.current = nil
		}
		s.mu.Unlock()
	}()
	go bridge.Run(runCtx)
	if s.config.ReloadInterval > 0 {
		go s.reloadLoop(runCtx)
	}

	s.config.Logger.Info("web helper stack started", "url", s.config.URL,
		"resolution", fmt.Sprintf("%dx%d", s.config.ScreenWidth, s.config.ScreenHeight))
	select {
	case <-ctx.Done():
		return nil
	case exit := <-exits:
		if runCtx.Err() != nil {
			return nil
		}
		if exit.err == nil {
			return fmt.Errorf("%s exited unexpectedly", exit.name)
		}
		return fmt.Errorf("%s exited: %w", exit.name, exit.err)
	}
}

func (s *Session) reloadLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.ReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.RLock()
			current := s.current
			s.mu.RUnlock()
			if current == nil {
				continue
			}
			// F5 avoids leaving a modifier held if the local VNC connection is
			// interrupted between events. This internal action remains available
			// when the public session is configured as display-only.
			current.HandleInput(display.InputEvent{Kind: display.InputKey, HID: 0x3E, Pressed: true})
			current.HandleInput(display.InputEvent{Kind: display.InputKey, HID: 0x3E, Pressed: false})
			s.config.Logger.Debug("web page reloaded", "interval", s.config.ReloadInterval)
		}
	}
}

func chromiumArguments(config Config, profileDir string) []string {
	arguments := []string{
		"--app=" + config.URL,
		"--kiosk",
		"--window-position=0,0",
		// Without a window manager Chromium makes an app-mode X11 window one
		// pixel smaller than the requested size. Extending it by one pixel fills
		// the Xvfb root exactly; the excess edge is clipped by the framebuffer.
		fmt.Sprintf("--window-size=%d,%d", config.ScreenWidth+1, config.ScreenHeight+1),
		"--force-device-scale-factor=1",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--noerrdialogs",
		"--disable-session-crashed-bubble",
		"--disable-infobars",
		"--disable-dev-shm-usage",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		// Xvfb has no physical GPU. Chromium no longer enables SwiftShader as
		// an automatic WebGL fallback, so opt in explicitly for canvas-heavy
		// dashboards while leaving ordinary page compositing unchanged.
		"--use-gl=angle",
		"--use-angle=swiftshader-webgl",
		"--enable-unsafe-swiftshader",
		"--password-store=basic",
		"--ozone-platform=x11",
	}
	if config.BrowserNoSandbox {
		arguments = append(arguments, "--no-sandbox")
	}
	return arguments
}

func x11vncArguments(displayName string, port int, interactive bool) []string {
	arguments := []string{
		"-display", displayName, "-localhost", "-rfbport", strconv.Itoa(port),
		"-forever", "-shared", "-nopw", "-xkb", "-quiet",
		"-defer", "5", "-wait", "5", "-nowait_bog", "-speeds", "lan",
		"-wirecopyrect", "always", "-scrollcopyrect", "always",
	}
	if !interactive {
		// Without cursor-shape support in the downstream VNC client, x11vnc's
		// default is to composite the X11 pointer directly into framebuffer
		// pixels. Disable it for a genuinely cursor-free display-only page.
		arguments = append(arguments, "-nocursor")
	}
	return arguments
}

func (s *Session) startXvfb(ctx context.Context, path, runtimeDir string, env []string, exits chan processExit) (string, error) {
	command := exec.CommandContext(ctx, path, xvfbArguments(s.config.ScreenWidth, s.config.ScreenHeight)...)
	command.Env = env
	command.Dir = runtimeDir
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("capture Xvfb display number: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("capture Xvfb output: %w", err)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start Xvfb: %w", err)
	}
	go s.logOutput("Xvfb", stderr)
	go func() { exits <- processExit{name: "Xvfb", err: command.Wait()} }()

	type displayResult struct {
		value string
		err   error
	}
	result := make(chan displayResult, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		result <- displayResult{value: strings.TrimSpace(line), err: readErr}
	}()
	timer := time.NewTimer(xvfbStartupTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case exit := <-exits:
		return "", fmt.Errorf("%s exited before selecting a display: %v", exit.name, exit.err)
	case <-timer.C:
		return "", fmt.Errorf("Xvfb did not select a display within %s", xvfbStartupTimeout)
	case displayNumber := <-result:
		if displayNumber.err != nil && displayNumber.value == "" {
			return "", fmt.Errorf("read Xvfb display number: %w", displayNumber.err)
		}
		if _, err := strconv.Atoi(displayNumber.value); err != nil {
			return "", fmt.Errorf("Xvfb returned invalid display number %q", displayNumber.value)
		}
		s.config.Logger.Debug("Xvfb display selected", "display", displayNumber.value)
		return displayNumber.value, nil
	}
}

func xvfbArguments(width, height int) []string {
	return []string{"-displayfd", "1", "-screen", "0", fmt.Sprintf("%dx%dx24", width, height), "-nolisten", "tcp"}
}

func (s *Session) startProcess(ctx context.Context, name, path string, arguments, env []string, exits chan<- processExit) error {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = env
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("capture %s stdout: %w", name, err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("capture %s stderr: %w", name, err)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	go s.logOutput(name, stdout)
	go s.logOutput(name, stderr)
	go func() { exits <- processExit{name: name, err: command.Wait()} }()
	return nil
}

func (s *Session) logOutput(name string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		s.config.Logger.Debug("web helper output", "helper", name, "line", scanner.Text())
	}
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve web bridge port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release web bridge port: %w", err)
	}
	return port, nil
}

func findExecutable(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return filepath.Clean(path), nil
		}
	}
	return "", fmt.Errorf("required web helper not found: %s (use the Docker image or install the native web dependencies)", strings.Join(names, " or "))
}

func environmentWith(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := replacements[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}
