// SPDX-License-Identifier: GPL-2.0-or-later

package rdp

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
	Hostname     string
	Port         int
	Username     string
	Domain       string
	Password     string
	Certificate  string
	ScreenWidth  int
	ScreenHeight int
	Logger       *slog.Logger
	OnFrame      func(frame *image.RGBA, changed []display.RegionUpdate, resized bool) error
}

// Session runs FreeRDP in a private Xvfb display and bridges that display into
// the existing framebuffer/input path through a loopback-only VNC listener.
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
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	if current != nil {
		current.HandleInput(event)
	}
}

// RequestFullFrame forwards a Sun Ray transport resynchronization request to
// the active loopback VNC bridge.
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
		return fmt.Errorf("invalid RDP display resolution %dx%d", s.config.ScreenWidth, s.config.ScreenHeight)
	}
	xvfbPath, err := findExecutable("Xvfb", "/opt/X11/bin/Xvfb")
	if err != nil {
		return err
	}
	x11vncPath, err := findExecutable("x11vnc", "/opt/homebrew/bin/x11vnc", "/usr/local/bin/x11vnc")
	if err != nil {
		return err
	}
	freerdpPath, err := findExecutable("xfreerdp3", "xfreerdp", "/opt/homebrew/bin/xfreerdp3", "/opt/homebrew/bin/xfreerdp", "/usr/local/bin/xfreerdp3", "/usr/local/bin/xfreerdp")
	if err != nil {
		return err
	}

	runtimeDir, err := os.MkdirTemp("", "sunray-rdp-")
	if err != nil {
		return fmt.Errorf("create RDP runtime directory: %w", err)
	}
	defer os.RemoveAll(runtimeDir)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	exits := make(chan processExit, 3)
	baseEnv := environmentWith(os.Environ(), map[string]string{"HOME": runtimeDir, "XDG_RUNTIME_DIR": runtimeDir})
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
	if err := s.startProcess(runCtx, "x11vnc", x11vncPath, x11vncArguments(displayName, port), displayEnv, nil, exits); err != nil {
		return err
	}

	arguments := freeRDPArguments(s.config)
	if err := s.startProcess(runCtx, "xfreerdp", freerdpPath, arguments, displayEnv,
		strings.NewReader(s.config.Password+"\n"), exits); err != nil {
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

	s.config.Logger.Info("RDP helper stack started", "server", net.JoinHostPort(s.config.Hostname, strconv.Itoa(s.config.Port)),
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

func x11vncArguments(displayName string, port int) []string {
	return []string{
		"-display", displayName, "-localhost", "-rfbport", strconv.Itoa(port),
		"-forever", "-shared", "-nopw", "-xkb", "-quiet",
		"-defer", "5", "-wait", "5", "-nowait_bog", "-speeds", "lan",
		"-wirecopyrect", "always", "-scrollcopyrect", "always",
	}
}

func (s *Session) startXvfb(ctx context.Context, path, runtimeDir string, env []string, exits chan processExit) (string, error) {
	arguments := xvfbArguments(s.config.ScreenWidth, s.config.ScreenHeight)
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = env
	command.Dir = runtimeDir
	// Ask Xvfb to report its selected display on stdout. Using ExtraFiles and
	// fd 3 works on a normal host but can lose the inherited descriptor when
	// Docker itself runs inside another minimal container.
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
	case display := <-result:
		if display.err != nil && display.value == "" {
			return "", fmt.Errorf("read Xvfb display number: %w", display.err)
		}
		if _, err := strconv.Atoi(display.value); err != nil {
			return "", fmt.Errorf("Xvfb returned invalid display number %q", display.value)
		}
		s.config.Logger.Debug("Xvfb display selected", "display", display.value)
		return display.value, nil
	}
}

func xvfbArguments(width, height int) []string {
	return []string{"-displayfd", "1", "-screen", "0", fmt.Sprintf("%dx%dx24", width, height), "-nolisten", "tcp"}
}

func (s *Session) startProcess(ctx context.Context, name, path string, arguments, env []string, stdin io.Reader, exits chan<- processExit) error {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = env
	command.Stdin = stdin
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
		s.config.Logger.Debug("RDP helper output", "helper", name, "line", scanner.Text())
	}
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve RDP bridge port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release RDP bridge port: %w", err)
	}
	return port, nil
}

func findExecutable(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return filepath.Clean(path), nil
		}
	}
	return "", fmt.Errorf("required RDP helper not found: %s (install the native RDP dependencies or use the Docker image)", strings.Join(names, " or "))
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

func freeRDPArguments(config Config) []string {
	arguments := []string{
		"/v:" + net.JoinHostPort(config.Hostname, strconv.Itoa(config.Port)),
		"/u:" + config.Username,
		fmt.Sprintf("/size:%dx%d", config.ScreenWidth, config.ScreenHeight),
		"/f", "/bpp:24", "/cert:" + config.Certificate, "/network:lan",
		"+auto-reconnect", "+async-input", "+async-update", "-decorations",
		"-grab-keyboard", "-grab-mouse", "-toggle-fullscreen", "/from-stdin",
	}
	if config.Domain != "" {
		arguments = append(arguments, "/d:"+config.Domain)
	}
	return arguments
}
