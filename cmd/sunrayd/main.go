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
	"strings"
	"syscall"
	"time"

	"sunray2server/internal/display"
	"sunray2server/internal/server"
	"sunray2server/internal/smartcard"
)

func main() {
	var (
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

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	vncPassword, err := loadPassword(*vncPasswordFile)
	if err != nil {
		logger.Error("unable to load VNC password", "error", err)
		os.Exit(2)
	}
	if len([]byte(vncPassword)) > 8 {
		logger.Warn("classic VNC authentication uses only the first 8 password bytes")
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
		ListenAddress:  *listen,
		FallbackWidth:  *width,
		FallbackHeight: *height,
		PacketDelay:    *packetDelay,
		Image:          img,
		Logger:         logger,
		VNCAddress:     *vncAddress,
		VNCPassword:    vncPassword,
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
	if *smartcardListen != "" {
		serviceCount++
		probe := &smartcard.Probe{ListenAddress: *smartcardListen, Logger: logger}
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

func loadPassword(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimRight(string(contents), "\r\n"), nil
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
