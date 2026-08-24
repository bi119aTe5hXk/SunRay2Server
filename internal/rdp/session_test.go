// SPDX-License-Identifier: GPL-2.0-or-later

package rdp

import (
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
