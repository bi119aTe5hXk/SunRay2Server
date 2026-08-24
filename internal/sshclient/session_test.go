// SPDX-License-Identifier: GPL-2.0-or-later

package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"image"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"sunray2server/internal/display"
)

func TestSSHSessionPTYInputAndRendering(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if metadata.User() != "tester" || string(password) != "secret" {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(hostSigner)
	received := make(chan byte, 1)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serveSSHTestConnection(listener, serverConfig, received)
	}()

	var frames atomic.Int32
	session := NewSession(Config{
		Address:       listener.Addr().String(),
		Username:      "tester",
		Password:      "secret",
		HostKeySHA256: ssh.FingerprintSHA256(hostSigner.PublicKey()),
		ScreenWidth:   280,
		ScreenHeight:  130,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnFrame: func(frame *image.RGBA, changed []image.Rectangle, first bool) error {
			if frame == nil || len(changed) == 0 {
				t.Error("empty SSH frame")
			}
			frames.Add(1)
			return nil
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientDone := make(chan error, 1)
	go func() { clientDone <- session.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		session.mu.Lock()
		connected := session.stdin != nil
		session.mu.Unlock()
		if connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SSH session did not connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	session.HandleInput(display.InputEvent{Kind: display.InputKey, HID: 0x04, Pressed: true})
	select {
	case got := <-received:
		if got != 'a' {
			t.Fatalf("SSH input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSH input")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	if frames.Load() < 1 {
		t.Fatal("no SSH framebuffer was rendered")
	}
}

func serveSSHTestConnection(listener net.Listener, config *ssh.ServerConfig, received chan<- byte) error {
	netConn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer netConn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(netConn, config)
	if err != nil {
		return err
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session required")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return err
		}
		for request := range channelRequests {
			switch request.Type {
			case "pty-req":
				_ = request.Reply(true, nil)
			case "shell":
				_ = request.Reply(true, nil)
				_, _ = io.WriteString(channel, "\x1b[32mready\x1b[0m\r\n")
				var input [1]byte
				if _, err := io.ReadFull(channel, input[:]); err != nil {
					return err
				}
				received <- input[0]
				_, _ = io.WriteString(channel, "ok\r\n")
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return channel.Close()
			default:
				_ = request.Reply(false, nil)
			}
		}
	}
	return nil
}
