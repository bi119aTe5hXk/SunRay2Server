// SPDX-License-Identifier: GPL-2.0-or-later

// Package sshclient connects a Sun Ray display/input channel to a modern SSH
// PTY and a small xterm-compatible framebuffer terminal.
package sshclient

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"sunray2server/internal/display"
)

type Config struct {
	Address               string
	Username              string
	Password              string
	PrivateKeyFile        string
	KnownHostsFile        string
	HostKeySHA256         string
	InsecureIgnoreHostKey bool
	ScreenWidth           int
	ScreenHeight          int
	Logger                *slog.Logger
	OnFrame               func(frame *image.RGBA, changed []image.Rectangle, first bool) error
}

type Session struct {
	config Config
	mu     sync.Mutex
	stdin  io.WriteCloser
	caps   bool
}

func NewSession(config Config) *Session {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Session{config: config}
}

func (s *Session) Run(ctx context.Context) error {
	term := newTerminal(s.config.ScreenWidth, s.config.ScreenHeight)
	cols, rows := term.dimensions()
	renderer := newTerminalRenderer(s.config.ScreenWidth, s.config.ScreenHeight, cols, rows)
	_, _ = fmt.Fprintf(term, "Connecting to %s ...\r\n", s.config.Address)
	if err := s.render(term, renderer); err != nil {
		return err
	}

	client, remoteSession, stdin, err := s.connect(ctx, cols, rows, term)
	if err != nil {
		_, _ = fmt.Fprintf(term, "\r\nSSH connection failed: %v\r\n", err)
		_ = s.render(term, renderer)
		return err
	}
	defer client.Close()
	defer remoteSession.Close()

	s.mu.Lock()
	s.stdin = stdin
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.stdin == stdin {
			s.stdin = nil
		}
		s.mu.Unlock()
	}()

	s.config.Logger.Info("SSH session connected", "server", s.config.Address, "username", s.config.Username, "terminal", fmt.Sprintf("%dx%d", cols, rows))
	done := make(chan error, 1)
	go func() { done <- remoteSession.Wait() }()
	renderErrors := make(chan error, 1)
	renderCtx, cancelRender := context.WithCancel(ctx)
	defer cancelRender()
	renderDone := make(chan struct{})
	go func() {
		defer close(renderDone)
		s.renderLoop(renderCtx, term, renderer, renderErrors)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-renderErrors:
		return err
	case err := <-done:
		cancelRender()
		<-renderDone
		if err != nil {
			_, _ = fmt.Fprintf(term, "\r\nSSH session ended: %v\r\n", err)
		} else {
			_, _ = io.WriteString(term, "\r\nSSH session ended.\r\n")
		}
		_ = s.render(term, renderer)
		return err
	}
}

func (s *Session) connect(ctx context.Context, cols, rows int, output io.Writer) (*ssh.Client, *ssh.Session, io.WriteCloser, error) {
	auth, err := s.authMethods()
	if err != nil {
		return nil, nil, nil, err
	}
	hostKeyCallback, err := s.hostKeyCallback()
	if err != nil {
		return nil, nil, nil, err
	}
	clientConfig := &ssh.ClientConfig{
		User:            s.config.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	netConn, err := dialer.DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial: %w", err)
	}
	closeOnCancelDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			netConn.Close()
		case <-closeOnCancelDone:
		}
	}()
	conn, channels, requests, err := ssh.NewClientConn(netConn, s.config.Address, clientConfig)
	close(closeOnCancelDone)
	if err != nil {
		netConn.Close()
		return nil, nil, nil, fmt.Errorf("handshake: %w", err)
	}
	client := ssh.NewClient(conn, channels, requests)
	remoteSession, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("create session: %w", err)
	}
	remoteSession.Stdout = output
	remoteSession.Stderr = output
	stdin, err := remoteSession.StdinPipe()
	if err != nil {
		remoteSession.Close()
		client.Close()
		return nil, nil, nil, fmt.Errorf("open stdin: %w", err)
	}
	modes := ssh.TerminalModes{
		ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200,
	}
	if err := remoteSession.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		remoteSession.Close()
		client.Close()
		return nil, nil, nil, fmt.Errorf("request PTY: %w", err)
	}
	if err := remoteSession.Shell(); err != nil {
		remoteSession.Close()
		client.Close()
		return nil, nil, nil, fmt.Errorf("start shell: %w", err)
	}
	return client, remoteSession, stdin, nil
}

func (s *Session) authMethods() ([]ssh.AuthMethod, error) {
	methods := make([]ssh.AuthMethod, 0, 3)
	if s.config.PrivateKeyFile != "" {
		contents, err := os.ReadFile(s.config.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(contents)
		if err != nil && s.config.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(contents, []byte(s.config.Password))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if s.config.Password != "" {
		methods = append(methods, ssh.Password(s.config.Password))
		methods = append(methods, ssh.KeyboardInteractive(func(_ string, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = s.config.Password
			}
			return answers, nil
		}))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH authentication method configured")
	}
	return methods, nil
}

func (s *Session) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if s.config.KnownHostsFile != "" {
		callback, err := knownhosts.New(s.config.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts: %w", err)
		}
		return callback, nil
	}
	if s.config.HostKeySHA256 != "" {
		expected := strings.TrimSpace(s.config.HostKeySHA256)
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if actual != expected {
				return fmt.Errorf("host key mismatch for %s: got %s", hostname, actual)
			}
			return nil
		}, nil
	}
	if s.config.InsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil // explicitly opted in by config
	}
	return nil, fmt.Errorf("no SSH host key verification method configured")
}

func (s *Session) HandleInput(event display.InputEvent) {
	if event.Kind != display.InputKey || !event.Pressed {
		return
	}
	s.mu.Lock()
	if event.HID == 0x39 {
		s.caps = !s.caps
		s.mu.Unlock()
		return
	}
	sequence := keyBytes(event, s.caps)
	stdin := s.stdin
	s.mu.Unlock()
	if len(sequence) == 0 || stdin == nil {
		return
	}
	if _, err := stdin.Write(sequence); err != nil {
		s.config.Logger.Debug("SSH input forwarding failed", "error", err)
	}
}

func (s *Session) renderLoop(ctx context.Context, term *terminal, renderer *terminalRenderer, errors chan<- error) {
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.render(term, renderer); err != nil {
				select {
				case errors <- err:
				default:
				}
				return
			}
		}
	}
}

func (s *Session) render(term *terminal, renderer *terminalRenderer) error {
	frame, changed, first := renderer.render(term)
	if len(changed) == 0 || s.config.OnFrame == nil {
		return nil
	}
	return s.config.OnFrame(frame, changed, first)
}
