// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	appconfig "sunray2server/internal/config"
	"sunray2server/internal/display"
	"sunray2server/internal/server"
	"sunray2server/internal/smartcard"
)

func main() {
	var (
		configPath      = flag.String("config", "", "YAML configuration file")
		listen          = flag.String("listen", ":7009", "TCP authentication listen address")
		imagePath       = flag.String("image", "", "PNG or JPEG to display; empty uses the generated test pattern")
		width           = flag.Int("fallback-width", 1280, "screen width when startRes is absent")
		height          = flag.Int("fallback-height", 1024, "screen height when startRes is absent")
		packetDelay     = flag.Duration("packet-delay", 200*time.Microsecond, "delay between UDP display packets")
		smartcardListen = flag.String("smartcard-listen", "", "passive TCP smart-card probe listen address; empty disables it")
		vncAddress      = flag.String("vnc", "", "VNC server address (for example 192.168.1.10:5900); empty shows the test image")
		vncPasswordFile = flag.String("vnc-password-file", "", "file containing the classic VNC password")
		debug           = flag.Bool("debug", false, "enable debug logging")
	)
	flag.Parse()
	overridden := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { overridden[f.Name] = true })

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	configuration := appconfig.Default()
	var err error
	if *configPath != "" {
		configuration, err = appconfig.Load(*configPath)
		if err != nil {
			logger.Error("unable to load configuration", "error", err)
			os.Exit(2)
		}
	}
	if overridden["listen"] {
		configuration.Server.Listen = *listen
	}
	if overridden["fallback-width"] {
		configuration.Server.FallbackWidth = *width
	}
	if overridden["fallback-height"] {
		configuration.Server.FallbackHeight = *height
	}
	if overridden["packet-delay"] {
		configuration.Server.PacketDelay = *packetDelay
	}
	if overridden["smartcard-listen"] {
		configuration.Server.SmartcardListen = *smartcardListen
	}
	if *vncAddress != "" {
		configuration.Sessions["command-line-vnc"] = appconfig.Session{
			Type:         "vnc",
			Address:      *vncAddress,
			PasswordFile: *vncPasswordFile,
		}
		configuration.Routing.Default.NoCard = "command-line-vnc"
		configuration.Routing.Default.CardPresent = "command-line-vnc"
	} else if overridden["vnc-password-file"] {
		logger.Error("-vnc-password-file requires -vnc")
		os.Exit(2)
	}
	if err := configuration.Validate(); err != nil {
		logger.Error("invalid effective configuration", "error", err)
		os.Exit(2)
	}

	img, err := loadImage(*imagePath)
	if err != nil {
		logger.Error("unable to load image", "error", err)
		os.Exit(2)
	}
	if img == nil {
		img = display.TestPattern(640, 360)
	}

	srv, err := server.New(server.Config{
		ListenAddress:  configuration.Server.Listen,
		FallbackWidth:  configuration.Server.FallbackWidth,
		FallbackHeight: configuration.Server.FallbackHeight,
		PacketDelay:    configuration.Server.PacketDelay,
		Image:          img,
		Logger:         logger,
		AppConfig:      configuration,
	})
	if err != nil {
		logger.Error("invalid server configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serviceCount := 1
	results := make(chan error, 2)
	go func() {
		results <- srv.Serve(ctx)
	}()
	if configuration.Server.SmartcardListen != "" {
		serviceCount++
		probe := &smartcard.Probe{ListenAddress: configuration.Server.SmartcardListen, Logger: logger}
		go func() {
			results <- probe.Serve(ctx)
		}()
	}

	var firstErr error
	for range serviceCount {
		if err := <-results; err != nil && firstErr == nil {
			firstErr = err
			stop()
		}
	}
	if firstErr != nil {
		logger.Error("service stopped", "error", firstErr)
		os.Exit(1)
	}
}

func loadImage(path string) (image.Image, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	img, format, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if format != "png" && format != "jpeg" {
		return nil, fmt.Errorf("unsupported image format %q", format)
	}
	return img, nil
}
