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
	OnFrame      func(frame *image.RGBA, changed []image.Rectangle, resized bool) error
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

func (s *Session) Run(ctx context.Context) error {
	if s.config.ScreenWidth < 1 || s.config.ScreenHeight < 1 {
		return fmt.Errorf("invalid RDP display resolution %dx%d", s.config.ScreenWidth, s.config.ScreenHeight)
	}
	xvfbPath, err := findExecutable("Xvfb")
	if err != nil {
		return err
	}
	x11vncPath, err := findExecutable("x11vnc")
	if err != nil {
		return err
	}
	freerdpPath, err := findExecutable("xfreerdp3", "xfreerdp")
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
	if err := s.startProcess(runCtx, "x11vnc", x11vncPath, []string{
		"-display", displayName, "-localhost", "-rfbport", strconv.Itoa(port),
		"-forever", "-shared", "-nopw", "-xkb", "-quiet",
	}, displayEnv, nil, exits); err != nil {
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

func (s *Session) startXvfb(ctx context.Context, path, runtimeDir string, env []string, exits chan processExit) (string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("create Xvfb display pipe: %w", err)
	}
	defer reader.Close()
	arguments := []string{"-displayfd", "3", "-screen", "0", fmt.Sprintf("%dx%dx24", s.config.ScreenWidth, s.config.ScreenHeight), "-nolisten", "tcp"}
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = env
	command.Dir = runtimeDir
	command.ExtraFiles = []*os.File{writer}
	stderr, err := command.StderrPipe()
	if err != nil {
		writer.Close()
		return "", fmt.Errorf("capture Xvfb output: %w", err)
	}
	if err := command.Start(); err != nil {
		writer.Close()
		return "", fmt.Errorf("start Xvfb: %w", err)
	}
	writer.Close()
	go s.logOutput("Xvfb", stderr)
	go func() { exits <- processExit{name: "Xvfb", err: command.Wait()} }()

	type displayResult struct {
		value string
		err   error
	}
	result := make(chan displayResult, 1)
	go func() {
		line, readErr := bufio.NewReader(reader).ReadString('\n')
		result <- displayResult{value: strings.TrimSpace(line), err: readErr}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case exit := <-exits:
		return "", fmt.Errorf("%s exited before selecting a display: %v", exit.name, exit.err)
	case <-timer.C:
		return "", fmt.Errorf("Xvfb did not select a display within 5 seconds")
	case display := <-result:
		if display.err != nil && display.value == "" {
			return "", fmt.Errorf("read Xvfb display number: %w", display.err)
		}
		if _, err := strconv.Atoi(display.value); err != nil {
			return "", fmt.Errorf("Xvfb returned invalid display number %q", display.value)
		}
		return display.value, nil
	}
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
	return "", fmt.Errorf("required RDP helper not found in PATH: %s", strings.Join(names, " or "))
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
