// SPDX-License-Identifier: GPL-2.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAndResolveRelativePaths(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	contents := []byte(`version: 1
server:
  packet_delay: 1ms
sessions:
  test:
    type: card-test
    image: assets/test.png
  vnc:
    type: vnc
    address: 127.0.0.1:5900
    password_file: secrets/vnc
  ssh:
    type: ssh
    hostname: 127.0.0.1
    port: 22
    username: test
    password: test
    insecure_ignore_host_key: true
    font_file: assets/terminal.ttf
routing:
  default:
    no_card: test
    card_present: vnc
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":7009" || cfg.Server.PacketDelay != time.Millisecond {
		t.Fatalf("defaults not applied: %#v", cfg.Server)
	}
	if cfg.Sessions["test"].Image != filepath.Join(directory, "assets/test.png") {
		t.Fatalf("image path = %q", cfg.Sessions["test"].Image)
	}
	if cfg.Sessions["vnc"].PasswordFile != filepath.Join(directory, "secrets/vnc") {
		t.Fatalf("password path = %q", cfg.Sessions["vnc"].PasswordFile)
	}
	if cfg.Sessions["ssh"].FontFile != filepath.Join(directory, "assets/terminal.ttf") || cfg.Sessions["ssh"].FontSize != 20 {
		t.Fatalf("SSH font defaults/paths = %#v", cfg.Sessions["ssh"])
	}
}

func TestResolvePrecedence(t *testing.T) {
	cfg := &Config{
		Routing: Routing{
			Default: Route{
				NoCard:      "global-no-card",
				CardPresent: "global-present",
				Cards:       map[string]string{"global-id": "global-card"},
			},
			Terminals: map[string]Route{
				"terminal-1": {
					NoCard:      "terminal-no-card",
					CardPresent: "terminal-present",
					Cards:       map[string]string{"terminal-id": "terminal-card"},
				},
			},
		},
	}
	tests := []struct {
		serial  string
		cardID  string
		present bool
		want    string
	}{
		{"terminal-1", "terminal-id", true, "terminal-card"},
		{"terminal-1", "global-id", true, "global-card"},
		{"terminal-1", "unknown", true, "terminal-present"},
		{"terminal-1", "", false, "terminal-no-card"},
		{"other", "unknown", true, "global-present"},
		{"other", "", false, "global-no-card"},
	}
	for _, test := range tests {
		got, ok := cfg.Resolve(test.serial, test.cardID, test.present)
		if !ok || got != test.want {
			t.Errorf("Resolve(%q, %q, %v) = %q, %v; want %q", test.serial, test.cardID, test.present, got, ok, test.want)
		}
	}
}

func TestStrictYAMLAndUnknownSessionReference(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown YAML field to fail")
	}

	cfg := Default()
	cfg.Routing.Default.NoCard = "missing"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown session reference to fail")
	}
}

func TestReservedSSHAndRDPSessionsValidate(t *testing.T) {
	cfg := Default()
	cfg.Sessions["ssh"] = Session{Type: "ssh", Hostname: "server", Port: 22, Username: "user", Password: "test", InsecureIgnoreHostKey: true}
	cfg.Sessions["rdp"] = Session{Type: "rdp", Hostname: "desktop", Port: 3389}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVNCPasswordSourcesArePerSessionAndMutuallyExclusive(t *testing.T) {
	cfg := Default()
	cfg.Sessions["vnc-one"] = Session{Type: "vnc", Address: "server-one:5900", Password: "one"}
	cfg.Sessions["vnc-two"] = Session{Type: "vnc", Address: "server-two:5900", PasswordFile: "/run/secrets/vnc_two"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := cfg.Sessions["vnc-one"]
	invalid.PasswordFile = "/run/secrets/vnc_one"
	cfg.Sessions["vnc-one"] = invalid
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected password and password_file together to fail")
	}
}

func TestVNCResolutionModes(t *testing.T) {
	for _, mode := range []string{VNCResolutionCurrent, VNCResolutionTerminal, VNCResolutionServer} {
		cfg := Default()
		cfg.Sessions["vnc"] = Session{Type: "vnc", Address: "server:5900", ResolutionMode: mode}
		if err := cfg.Validate(); err != nil {
			t.Errorf("mode %q: %v", mode, err)
		}
	}
	cfg := Default()
	cfg.Sessions["vnc"] = Session{
		Type: "vnc", Address: "server:5900", ResolutionMode: VNCResolutionManual,
		DisplayWidth: 1400, DisplayHeight: 1050,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := cfg.Sessions["vnc"]
	invalid.ResolutionMode = VNCResolutionCurrent
	cfg.Sessions["vnc"] = invalid
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected dimensions outside manual mode to fail")
	}
	invalid.ResolutionMode = "automatic"
	invalid.DisplayWidth, invalid.DisplayHeight = 0, 0
	cfg.Sessions["vnc"] = invalid
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unknown VNC resolution mode to fail")
	}
}

func TestDisplayOverrideRequiresCompletePair(t *testing.T) {
	cfg := Default()
	cfg.Server.DisplayWidth = 1280
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected incomplete display override to fail")
	}
	cfg.Server.DisplayHeight = 720
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSSHRequiresAuthenticationAndHostKeyPolicy(t *testing.T) {
	cfg := Default()
	cfg.Sessions["ssh"] = Session{Type: "ssh", Hostname: "server", Port: 22, Username: "user"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing SSH authentication and host-key policy to fail")
	}
	cfg.Sessions["ssh"] = Session{
		Type: "ssh", Hostname: "server", Port: 22, Username: "user",
		Password: "test", HostKeySHA256: "not-a-fingerprint",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected malformed SSH fingerprint to fail")
	}
}

func TestSSHFontSizeRange(t *testing.T) {
	cfg := Default()
	cfg.Sessions["ssh"] = Session{
		Type: "ssh", Hostname: "server", Port: 22, Username: "user", Password: "test",
		InsecureIgnoreHostKey: true, FontSize: 7,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an out-of-range SSH font size to fail")
	}
}

func TestProjectTemplateLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.yaml.template"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sessions["card-test"].Type != "card-test" || cfg.Sessions["geometry-test"].Type != "geometry-test" || cfg.Sessions["example-vnc"].Type != "vnc" {
		t.Fatalf("unexpected template sessions: %#v", cfg.Sessions)
	}
}
