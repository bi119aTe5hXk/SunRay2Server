// SPDX-License-Identifier: GPL-2.0-or-later

package smartcard

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const maxCapturedBytes = 64 * 1024

// Probe passively records the initial TCP 4120 PC/SC/scbus conversation. It
// deliberately sends no APDU or guessed protocol response to an inserted card.
type Probe struct {
	ListenAddress string
	Logger        *slog.Logger

	mu   sync.Mutex
	seen map[string]struct{}
}

func (p *Probe) Serve(ctx context.Context) error {
	if p.ListenAddress == "" {
		p.ListenAddress = ":4120"
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	p.seen = make(map[string]struct{})
	listener, err := net.Listen("tcp", p.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for smart-card probe: %w", err)
	}
	defer listener.Close()
	p.Logger.Info("passive smart-card probe listening", "address", listener.Addr(), "mode", "read-only")
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept smart-card probe connection: %w", err)
		}
		go p.handleConnection(conn)
	}
}

func (p *Probe) handleConnection(conn net.Conn) {
	defer conn.Close()
	logger := p.Logger.With("client", conn.RemoteAddr())
	logger.Info("smart-card service connection detected")
	buffer := make([]byte, 4096)
	captured := make([]byte, 0, 4096)
	for len(captured) < maxCapturedBytes {
		if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return
		}
		n, err := conn.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			logger.Debug("smart-card probe frame", "bytes", n, "hex", hex.EncodeToString(chunk))
			captured = append(captured, chunk...)
			for _, atr := range FindATRs(captured) {
				p.logATR(logger, atr)
			}
		}
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				logger.Info("smart-card probe timed out waiting for more data", "captured_bytes", len(captured))
			} else if !errors.Is(err, net.ErrClosed) {
				logger.Debug("smart-card service connection ended", "error", err, "captured_bytes", len(captured))
			}
			return
		}
	}
	logger.Warn("smart-card probe capture limit reached", "bytes", len(captured))
}

func (p *Probe) logATR(logger *slog.Logger, atr ATR) {
	key := atr.Hex()
	p.mu.Lock()
	_, exists := p.seen[key]
	if !exists {
		p.seen[key] = struct{}{}
	}
	p.mu.Unlock()
	if exists {
		return
	}
	logger.Info("ATR detected",
		"atr", key,
		"convention", map[bool]string{true: "direct", false: "inverse"}[atr.Direct],
		"protocols", atr.ProtocolNames(),
		"historical_bytes", atr.HistoricalHex())
}
