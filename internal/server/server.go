// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"sunray2server/internal/auth"
	appconfig "sunray2server/internal/config"
	"sunray2server/internal/display"
	"sunray2server/internal/geometry"
	"sunray2server/internal/sshclient"
	"sunray2server/internal/vnc"
)

type Config struct {
	ListenAddress  string
	FallbackWidth  int
	FallbackHeight int
	DisplayWidth   int
	DisplayHeight  int
	PacketDelay    time.Duration
	LogInputEvents bool
	Image          image.Image
	Logger         *slog.Logger
	AppConfig      *appconfig.Config
}

type Server struct {
	config   Config
	mu       sync.Mutex
	active   map[string]activeDisplay
	selected map[string]sessionSelection
}

type activeDisplay struct {
	client         *display.Client
	width          int
	height         int
	reportedWidth  int
	reportedHeight int
	ctx            context.Context
	cancel         context.CancelFunc
	sessionCancel  context.CancelFunc
	session        string
	generation     uint64
}

type sessionSelection struct {
	slot     string
	session  string
	cardType string
	cardID   string
	event    string
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
	if config.AppConfig == nil {
		config.AppConfig = appconfig.Default()
	}
	return &Server{
		config:   config,
		active:   make(map[string]activeDisplay),
		selected: make(map[string]sessionSelection),
	}, nil
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
			if _, err := fmt.Fprintln(writer, auth.InfoResponse(message.Get("tokenSeq"))); err != nil {
				logger.Warn("authentication response failed", "error", err)
				return
			}
			if err := writer.Flush(); err != nil {
				logger.Warn("authentication flush failed", "error", err)
				return
			}
			if slot, definitive := cardSessionSlot(properties["type"], properties["event"]); definitive {
				s.selectSession(clientKey, properties["sn"], slot, properties["type"], properties["id"], properties["event"], logger)
			}
		case "keepAliveReq":
			if _, err := fmt.Fprintln(writer, "keepAliveCnf"); err != nil {
				return
			}
			if err := writer.Flush(); err != nil {
				return
			}
		case "connRsp":
			if err := s.startDisplay(ctx, clientKey, remoteIP, properties, logger); err != nil {
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

func cardSessionSlot(cardType, event string) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(event), "insert") {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(cardType), "pseudo") {
		return "no-card", true
	}
	return "card-present", true
}

func (s *Server) selectSession(key, serial, slot, cardType, cardID, event string, logger *slog.Logger) {
	sessionName, ok := s.config.AppConfig.Resolve(serial, cardID, slot == "card-present")
	if !ok {
		logger.Error("no session route matched", "slot", slot, "serial", serial, "card_id", cardID)
		return
	}
	selection := sessionSelection{slot: slot, session: sessionName, cardType: cardType, cardID: cardID, event: event}
	s.mu.Lock()
	previous := s.selected[key]
	s.selected[key] = selection
	active, hasDisplay := s.active[key]
	s.mu.Unlock()
	if previous.slot != slot || previous.session != sessionName {
		logger.Info("session selected", "slot", slot, "session", sessionName, "previous_session", previous.session, "card_type", cardType, "card_id", cardID)
	}
	definition := s.config.AppConfig.Sessions[sessionName]
	if hasDisplay && active.client != nil && (active.session != sessionName || definition.Type == "card-test") {
		s.switchSession(key, selection, logger)
	}
}

func (s *Server) startDisplay(ctx context.Context, key string, remoteIP net.IP, properties map[string]string, logger *slog.Logger) error {
	port, err := strconv.Atoi(properties["pn"])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid terminal display port pn=%q", properties["pn"])
	}
	reportedWidth, reportedHeight := parseResolution(properties["startRes"], s.config.FallbackWidth, s.config.FallbackHeight)
	width, height := reportedWidth, reportedHeight
	if s.config.DisplayWidth > 0 && s.config.DisplayHeight > 0 {
		logger.Info("overriding terminal display geometry", "reported", fmt.Sprintf("%dx%d", width, height), "configured", fmt.Sprintf("%dx%d", s.config.DisplayWidth, s.config.DisplayHeight))
		width, height = s.config.DisplayWidth, s.config.DisplayHeight
	}
	client, err := display.Open(remoteIP, port, s.config.PacketDelay, s.config.LogInputEvents, logger)
	if err != nil {
		return err
	}

	displayCtx, cancelDisplay := context.WithCancel(ctx)
	s.mu.Lock()
	old := s.active[key]
	s.active[key] = activeDisplay{
		client: client, width: width, height: height,
		reportedWidth: reportedWidth, reportedHeight: reportedHeight,
		ctx: displayCtx, cancel: cancelDisplay,
	}
	selection, selected := s.selected[key]
	s.mu.Unlock()
	if old.sessionCancel != nil {
		old.sessionCancel()
	}
	if old.cancel != nil {
		old.cancel()
	}
	if old.client != nil {
		old.client.Close()
	}

	logger.Info("display channel opened",
		"serial", properties["sn"],
		"card_type", properties["type"],
		"card_id", properties["id"],
		"terminal_udp", net.JoinHostPort(remoteIP.String(), strconv.Itoa(port)),
		"server_udp", client.LocalAddr(),
		"resolution", fmt.Sprintf("%dx%d", width, height))

	if !selected {
		slot, _ := cardSessionSlot(properties["type"], "insert")
		sessionName, _ := s.config.AppConfig.Resolve(properties["sn"], properties["id"], slot == "card-present")
		selection = sessionSelection{slot: slot, session: sessionName, cardType: properties["type"], cardID: properties["id"], event: "insert"}
		s.mu.Lock()
		s.selected[key] = selection
		s.mu.Unlock()
	}
	s.switchSession(key, selection, logger)
	return nil
}

func (s *Server) switchSession(key string, selection sessionSelection, logger *slog.Logger) {
	definition, ok := s.config.AppConfig.Sessions[selection.session]
	if !ok {
		logger.Error("selected session does not exist", "session", selection.session)
		return
	}
	s.mu.Lock()
	active, ok := s.active[key]
	if !ok || active.client == nil {
		s.mu.Unlock()
		return
	}
	if active.sessionCancel != nil {
		active.sessionCancel()
	}
	sessionCtx, cancelSession := context.WithCancel(active.ctx)
	active.sessionCancel = cancelSession
	active.session = selection.session
	active.generation++
	generation := active.generation
	s.active[key] = active
	s.mu.Unlock()
	active.client.SetInputHandler(nil)
	go s.runSession(sessionCtx, key, active, generation, selection, definition, logger)
}

func (s *Server) runSession(ctx context.Context, key string, active activeDisplay, generation uint64, selection sessionSelection, definition appconfig.Session, logger *slog.Logger) {
	logger.Info("session starting", "session", selection.session, "type", definition.Type)
	switch definition.Type {
	case "card-test":
		base := s.sessionImage(definition, logger)
		cardImage := display.CardStatusImage(base, selection.cardType, selection.cardID, selection.event)
		if !s.isCurrentSession(key, active.client, generation) {
			return
		}
		if err := active.client.ShowImage(active.width, active.height, cardImage); err != nil && !errors.Is(err, net.ErrClosed) {
			logger.Error("card-test display failed", "session", selection.session, "error", err)
			return
		}
		logger.Info("card-test session displayed", "session", selection.session)
	case "geometry-test":
		s.startGeometryTest(ctx, key, active, generation, logger)
	case "vnc":
		password, err := sessionPassword(definition)
		if err != nil {
			logger.Error("unable to load VNC password", "session", selection.session, "error", err)
			return
		}
		if len([]byte(password)) > 8 {
			logger.Warn("classic VNC authentication uses only the first 8 password bytes", "session", selection.session)
		}
		connecting := display.CardStatusImage(s.config.Image, "VNC", definition.Address, "CONNECT")
		if s.isCurrentSession(key, active.client, generation) {
			if err := active.client.ShowImage(active.width, active.height, connecting); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Warn("VNC connecting screen failed", "error", err)
			}
		}
		s.startVNC(ctx, key, active, generation, definition, password, logger)
	case "ssh":
		password, err := sessionPassword(definition)
		if err != nil {
			logger.Error("unable to load SSH password", "session", selection.session, "error", err)
			return
		}
		address := net.JoinHostPort(definition.Hostname, strconv.Itoa(definition.Port))
		connecting := display.CardStatusImage(s.config.Image, "SSH", address, "CONNECT")
		if s.isCurrentSession(key, active.client, generation) {
			if err := active.client.ShowImage(active.width, active.height, connecting); err != nil && !errors.Is(err, net.ErrClosed) {
				logger.Warn("SSH connecting screen failed", "error", err)
			}
		}
		s.startSSH(ctx, key, active, generation, definition, address, password, logger)
	case "rdp":
		unsupported := display.CardStatusImage(s.config.Image, strings.ToUpper(definition.Type), "NOT IMPLEMENTED", "STATUS")
		if s.isCurrentSession(key, active.client, generation) {
			_ = active.client.ShowImage(active.width, active.height, unsupported)
		}
		logger.Warn("session type is configured but not implemented", "session", selection.session, "type", definition.Type)
	}
}

func (s *Server) startGeometryTest(ctx context.Context, key string, active activeDisplay, generation uint64, logger *slog.Logger) {
	session := geometry.NewSession(geometry.Config{
		Width: active.width, Height: active.height, Logger: logger,
		OnFrame: func(width, height, clearWidth, clearHeight int) error {
			if !s.isCurrentSession(key, active.client, generation) {
				return context.Canceled
			}
			return active.client.ShowCalibrationTarget(width, height, clearWidth, clearHeight)
		},
	})
	active.client.SetInputHandler(session.HandleInput)
	if err := session.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Warn("geometry-test session stopped", "error", err)
	}
}

func (s *Server) startSSH(ctx context.Context, key string, active activeDisplay, generation uint64, definition appconfig.Session, address, password string, logger *slog.Logger) {
	session := sshclient.NewSession(sshclient.Config{
		Address:               address,
		Username:              definition.Username,
		Password:              password,
		PrivateKeyFile:        definition.PrivateKeyFile,
		KnownHostsFile:        definition.KnownHostsFile,
		HostKeySHA256:         definition.HostKeySHA256,
		InsecureIgnoreHostKey: definition.InsecureIgnoreHostKey,
		FontFile:              definition.FontFile,
		FontSize:              definition.FontSize,
		ScreenWidth:           active.width,
		ScreenHeight:          active.height,
		Logger:                logger,
		OnFrame: func(frame *image.RGBA, changed []image.Rectangle, first bool) error {
			if !s.isCurrentSession(key, active.client, generation) {
				return context.Canceled
			}
			if first {
				return active.client.ShowImage(active.width, active.height, frame)
			}
			for _, rectangle := range changed {
				if err := active.client.ShowImageRegion(active.width, active.height, frame, rectangle); err != nil {
					return err
				}
			}
			return nil
		},
	})
	active.client.SetInputHandler(session.HandleInput)
	if err := session.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Warn("SSH session stopped", "server", address, "error", err)
	}
}

func sessionPassword(definition appconfig.Session) (string, error) {
	if definition.Password != "" {
		return definition.Password, nil
	}
	return readSecret(definition.PasswordFile)
}

func (s *Server) startVNC(ctx context.Context, key string, active activeDisplay, generation uint64, definition appconfig.Session, password string, logger *slog.Logger) {
	mode, screenWidth, screenHeight := vncDisplayGeometry(active, definition)
	logger.Info("VNC display geometry selected", "mode", mode, "resolution", resolutionDescription(screenWidth, screenHeight))
	firstFrame := true
	session := vnc.NewSession(vnc.Config{
		Address:      definition.Address,
		Password:     password,
		ScreenWidth:  screenWidth,
		ScreenHeight: screenHeight,
		Logger:       logger,
		OnFrame: func(frame *image.RGBA, changed []image.Rectangle, resized bool) error {
			if !s.isCurrentSession(key, active.client, generation) {
				return context.Canceled
			}
			displayWidth, displayHeight := screenWidth, screenHeight
			if mode == appconfig.VNCResolutionServer {
				displayWidth, displayHeight = frame.Bounds().Dx(), frame.Bounds().Dy()
			}
			if firstFrame || resized {
				firstFrame = false
				return active.client.ShowImage(displayWidth, displayHeight, frame)
			}
			for _, rectangle := range changed {
				if err := active.client.ShowImageRegion(displayWidth, displayHeight, frame, rectangle); err != nil {
					return err
				}
			}
			return nil
		},
	})
	active.client.SetInputHandler(session.HandleInput)
	session.Run(ctx)
}

func vncDisplayGeometry(active activeDisplay, definition appconfig.Session) (string, int, int) {
	switch definition.ResolutionMode {
	case appconfig.VNCResolutionTerminal:
		return definition.ResolutionMode, active.reportedWidth, active.reportedHeight
	case appconfig.VNCResolutionServer:
		return definition.ResolutionMode, 0, 0
	case appconfig.VNCResolutionManual:
		return definition.ResolutionMode, definition.DisplayWidth, definition.DisplayHeight
	default:
		return appconfig.VNCResolutionCurrent, active.width, active.height
	}
}

func resolutionDescription(width, height int) string {
	if width == 0 && height == 0 {
		return "VNC framebuffer"
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func (s *Server) isCurrentSession(key string, client *display.Client, generation uint64) bool {
	s.mu.Lock()
	active, ok := s.active[key]
	s.mu.Unlock()
	return ok && active.client == client && active.generation == generation
}

func (s *Server) sessionImage(definition appconfig.Session, logger *slog.Logger) image.Image {
	if definition.Image == "" {
		return s.config.Image
	}
	file, err := os.Open(definition.Image)
	if err != nil {
		logger.Warn("unable to open session image; using default", "path", definition.Image, "error", err)
		return s.config.Image
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		logger.Warn("unable to decode session image; using default", "path", definition.Image, "error", err)
		return s.config.Image
	}
	return img
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimRight(string(contents), "\r\n"), nil
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
	active := s.active[key]
	delete(s.active, key)
	s.mu.Unlock()
	if active.sessionCancel != nil {
		active.sessionCancel()
	}
	if active.cancel != nil {
		active.cancel()
	}
	if active.client != nil {
		active.client.Close()
	}
}

func (s *Server) closeAll() {
	s.mu.Lock()
	clients := s.active
	s.active = make(map[string]activeDisplay)
	s.selected = make(map[string]sessionSelection)
	s.mu.Unlock()
	for _, active := range clients {
		if active.sessionCancel != nil {
			active.sessionCancel()
		}
		if active.cancel != nil {
			active.cancel()
		}
		active.client.Close()
	}
}
