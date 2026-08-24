// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"sunray2server/internal/auth"
	"sunray2server/internal/display"
)

type Config struct {
	ListenAddress  string
	FallbackWidth  int
	FallbackHeight int
	PacketDelay    time.Duration
	Image          image.Image
	Logger         *slog.Logger
}

type Server struct {
	config Config
	mu     sync.Mutex
	active map[string]*display.Client
}

func New(config Config) (*Server, error) {
	if config.ListenAddress == "" {
		config.ListenAddress = ":7009"
	}
	if config.FallbackWidth < 1 {
		config.FallbackWidth = 1280
	}
	if config.FallbackHeight < 1 {
		config.FallbackHeight = 1024
	}
	if config.Image == nil {
		config.Image = display.TestPattern(640, 360)
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Server{config: config, active: make(map[string]*display.Client)}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for Sun Ray authentication: %w", err)
	}
	return s.ServeListener(ctx, listener)
}

// ServeListener runs the server on an existing listener. It primarily enables
// deterministic end-to-end protocol tests with an ephemeral loopback port.
func (s *Server) ServeListener(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	s.config.Logger.Info("Sun Ray authentication server listening", "address", listener.Addr())

	go func() {
		<-ctx.Done()
		listener.Close()
		s.closeAll()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept authentication connection: %w", err)
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remoteHost, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		s.config.Logger.Warn("invalid client address", "address", conn.RemoteAddr(), "error", err)
		return
	}
	remoteIP := net.ParseIP(remoteHost)
	logger := s.config.Logger.With("client_ip", remoteHost)
	logger.Info("authentication client connected")

	properties := make(map[string]string)
	var clientKey string
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	writer := bufio.NewWriter(conn)

	for scanner.Scan() {
		line := scanner.Text()
		message, err := auth.Parse(line)
		if err != nil {
			logger.Warn("ignored malformed authentication message", "error", err, "line", line)
			continue
		}
		for key, value := range message.Fields {
			properties[key] = value
		}
		if properties["sn"] != "" {
			clientKey = properties["sn"]
		} else {
			clientKey = remoteHost
		}

		switch message.Type {
		case "infoReq":
			logger.Info("Sun Ray information",
				"serial", properties["sn"],
				"hardware", properties["hw"],
				"card_type", properties["type"],
				"card_id", properties["id"],
				"event", properties["event"],
				"resolution", properties["startRes"])
			if properties["event"] == "remove" {
				s.closeClient(clientKey)
			}
			if _, err := fmt.Fprintln(writer, auth.InfoResponse(message.Get("tokenSeq"))); err != nil {
				logger.Warn("authentication response failed", "error", err)
				return
			}
			if err := writer.Flush(); err != nil {
				logger.Warn("authentication flush failed", "error", err)
				return
			}
		case "keepAliveReq":
			if _, err := fmt.Fprintln(writer, "keepAliveCnf"); err != nil {
				return
			}
			if err := writer.Flush(); err != nil {
				return
			}
		case "connRsp":
			if err := s.startDisplay(clientKey, remoteIP, properties, logger); err != nil {
				logger.Error("display startup failed", "error", err)
			}
		default:
			logger.Debug("unhandled authentication message", "type", message.Type)
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Warn("authentication connection ended", "error", err)
	} else {
		logger.Info("authentication client disconnected")
	}
}

func (s *Server) startDisplay(key string, remoteIP net.IP, properties map[string]string, logger *slog.Logger) error {
	port, err := strconv.Atoi(properties["pn"])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid terminal display port pn=%q", properties["pn"])
	}
	width, height := parseResolution(properties["startRes"], s.config.FallbackWidth, s.config.FallbackHeight)
	client, err := display.Open(remoteIP, port, s.config.PacketDelay, logger)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.active[key]
	s.active[key] = client
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}

	logger.Info("display channel opened",
		"serial", properties["sn"],
		"card_type", properties["type"],
		"card_id", properties["id"],
		"terminal_udp", net.JoinHostPort(remoteIP.String(), strconv.Itoa(port)),
		"server_udp", client.LocalAddr(),
		"resolution", fmt.Sprintf("%dx%d", width, height))

	go func() {
		if err := client.ShowImage(width, height, s.config.Image); err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Error("test image transmission failed", "error", err)
			return
		}
		logger.Info("test image transmission complete")
	}()
	return nil
}

func parseResolution(value string, fallbackWidth, fallbackHeight int) (int, int) {
	first, _, _ := strings.Cut(value, ":")
	widthText, heightText, ok := strings.Cut(first, "x")
	if !ok {
		return fallbackWidth, fallbackHeight
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > 8192 || height > 8192 {
		return fallbackWidth, fallbackHeight
	}
	return width, height
}

func (s *Server) closeClient(key string) {
	s.mu.Lock()
	client := s.active[key]
	delete(s.active, key)
	s.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

func (s *Server) closeAll() {
	s.mu.Lock()
	clients := s.active
	s.active = make(map[string]*display.Client)
	s.mu.Unlock()
	for _, client := range clients {
		client.Close()
	}
}
