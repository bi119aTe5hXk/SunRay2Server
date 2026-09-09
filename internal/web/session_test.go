// SPDX-License-Identifier: GPL-2.0-or-later

package web

import (
	"slices"
	"strings"
	"testing"
)

func TestChromiumArgumentsUsePersistentPageAndExactViewport(t *testing.T) {
	config := Config{
		URL:          "https://example.test/dashboard?mode=live",
		ScreenWidth:  1400,
		ScreenHeight: 1050,
	}
	arguments := chromiumArguments(config, "/tmp/profile")
	for _, expected := range []string{
		"--app=https://example.test/dashboard?mode=live",
		"--kiosk",
		"--window-position=0,0",
		"--window-size=1401,1051",
		"--force-device-scale-factor=1",
		"--user-data-dir=/tmp/profile",
	} {
		if !slices.Contains(arguments, expected) {
			t.Errorf("missing argument %q in %#v", expected, arguments)
		}
	}
	if slices.Contains(arguments, "--no-sandbox") {
		t.Fatal("browser sandbox disabled without explicit configuration")
	}
	joined := strings.Join(arguments, " ")
	if strings.Contains(joined, "reload") || strings.Contains(joined, "refresh") {
		t.Fatalf("browser arguments unexpectedly reload the live page: %q", joined)
	}
}

func TestChromiumNoSandboxMustBeExplicit(t *testing.T) {
	arguments := chromiumArguments(Config{
		URL: "https://example.test", ScreenWidth: 800, ScreenHeight: 600,
		BrowserNoSandbox: true,
	}, "/tmp/profile")
	if !slices.Contains(arguments, "--no-sandbox") {
		t.Fatalf("missing explicit --no-sandbox in %#v", arguments)
	}
}

func TestWebX11VNCArgumentsKeepLowLatencyInputAndUpdates(t *testing.T) {
	joined := strings.Join(x11vncArguments(":42", 5901), " ")
	for _, expected := range []string{
		"-display :42", "-localhost", "-rfbport 5901", "-xkb",
		"-defer 5", "-wait 5", "-wirecopyrect always", "-scrollcopyrect always",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in %q", expected, joined)
		}
	}
}

func TestWebXvfbReportsDisplayOnStdout(t *testing.T) {
	joined := strings.Join(xvfbArguments(1400, 1050), " ")
	for _, expected := range []string{"-displayfd 1", "-screen 0 1400x1050x24", "-nolisten tcp"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in %q", expected, joined)
		}
	}
}
