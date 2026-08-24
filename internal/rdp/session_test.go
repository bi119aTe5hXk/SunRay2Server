// SPDX-License-Identifier: GPL-2.0-or-later

package rdp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestFreeRDPArguments(t *testing.T) {
	config := Config{Hostname: "2001:db8::1", Port: 3389, Username: "tester", Domain: "LAB", Password: "do-not-leak", Certificate: "deny", ScreenWidth: 1400, ScreenHeight: 1050}
	arguments := freeRDPArguments(config)
	for _, argument := range arguments {
		if strings.Contains(argument, config.Password) {
			t.Fatalf("password leaked into argument %q", argument)
		}
	}
	for _, expected := range []string{"/v:[2001:db8::1]:3389", "/u:tester", "/d:LAB", "/size:1400x1050", "/cert:deny", "/from-stdin"} {
		if !slices.Contains(arguments, expected) {
			t.Errorf("missing argument %q in %#v", expected, arguments)
		}
	}
}

func TestX11VNCArgumentsEnableLowLatencyCopyRect(t *testing.T) {
	arguments := x11vncArguments(":42", 5901)
	joined := strings.Join(arguments, " ")
	for _, expected := range []string{
		"-display :42", "-rfbport 5901", "-defer 5", "-wait 5",
		"-nowait_bog", "-speeds lan", "-wirecopyrect always", "-scrollcopyrect always",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in %q", expected, joined)
		}
	}
}

func TestXvfbReportsSelectedDisplayOnStdout(t *testing.T) {
	arguments := xvfbArguments(1400, 1050)
	joined := strings.Join(arguments, " ")
	for _, expected := range []string{"-displayfd 1", "-screen 0 1400x1050x24", "-nolisten tcp"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in %q", expected, joined)
		}
	}
}

func TestStartXvfbReadsDisplayNumberFromStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	helper := filepath.Join(t.TempDir(), "fake-xvfb")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '77\\n'\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	session := NewSession(Config{ScreenWidth: 1400, ScreenHeight: 1050, Logger: logger})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	displayNumber, err := session.startXvfb(ctx, helper, t.TempDir(), os.Environ(), make(chan processExit, 1))
	if err != nil {
		t.Fatal(err)
	}
	if displayNumber != "77" {
		t.Fatalf("display number = %q, want 77", displayNumber)
	}
}

func TestFindExecutableReportsAllCandidates(t *testing.T) {
	_, err := findExecutable("sunray-helper-that-does-not-exist-a", "sunray-helper-that-does-not-exist-b")
	if err == nil || !strings.Contains(err.Error(), "sunray-helper-that-does-not-exist-a or sunray-helper-that-does-not-exist-b") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnvironmentWithReplacesExistingValues(t *testing.T) {
	result := environmentWith([]string{"PATH=/bin", "HOME=/old", "DISPLAY=:99"}, map[string]string{"HOME": "/isolated", "DISPLAY": ":12"})
	for _, expected := range []string{"PATH=/bin", "HOME=/isolated", "DISPLAY=:12"} {
		if !slices.Contains(result, expected) {
			t.Errorf("missing %q in %#v", expected, result)
		}
	}
	for _, stale := range []string{"HOME=/old", "DISPLAY=:99"} {
		if slices.Contains(result, stale) {
			t.Errorf("stale environment %q remains in %#v", stale, result)
		}
	}
}
