// SPDX-License-Identifier: GPL-2.0-or-later

package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const CurrentVersion = 1

type Config struct {
	Version  int                `yaml:"version"`
	Server   Server             `yaml:"server"`
	Sessions map[string]Session `yaml:"sessions"`
	Routing  Routing            `yaml:"routing"`
}

type Server struct {
	Listen          string        `yaml:"listen"`
	FallbackWidth   int           `yaml:"fallback_width"`
	FallbackHeight  int           `yaml:"fallback_height"`
	DisplayWidth    int           `yaml:"display_width,omitempty"`
	DisplayHeight   int           `yaml:"display_height,omitempty"`
	PacketDelay     time.Duration `yaml:"packet_delay"`
	SmartcardListen string        `yaml:"smartcard_listen"`
	LogInputEvents  bool          `yaml:"log_input_events,omitempty"`
}

type Session struct {
	Type                  string  `yaml:"type"`
	Address               string  `yaml:"address,omitempty"`
	Image                 string  `yaml:"image,omitempty"`
	Hostname              string  `yaml:"hostname,omitempty"`
	Port                  int     `yaml:"port,omitempty"`
	Username              string  `yaml:"username,omitempty"`
	Password              string  `yaml:"password,omitempty"`
	PasswordFile          string  `yaml:"password_file,omitempty"`
	PrivateKeyFile        string  `yaml:"private_key_file,omitempty"`
	KnownHostsFile        string  `yaml:"known_hosts_file,omitempty"`
	HostKeySHA256         string  `yaml:"host_key_sha256,omitempty"`
	InsecureIgnoreHostKey bool    `yaml:"insecure_ignore_host_key,omitempty"`
	FontFile              string  `yaml:"font_file,omitempty"`
	FontSize              float64 `yaml:"font_size,omitempty"`
}

type Routing struct {
	Default   Route            `yaml:"default"`
	Terminals map[string]Route `yaml:"terminals,omitempty"`
}

type Route struct {
	NoCard      string            `yaml:"no_card"`
	CardPresent string            `yaml:"card_present"`
	Cards       map[string]string `yaml:"cards,omitempty"`
}

func Load(path string) (*Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode trailing config data %s: %w", path, err)
		}
		return nil, fmt.Errorf("config %s contains multiple YAML documents", path)
	}
	cfg.applyDefaults()
	cfg.resolveRelativePaths(filepath.Dir(path))
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	return &cfg, nil
}

func Default() *Config {
	cfg := &Config{
		Version: CurrentVersion,
		Sessions: map[string]Session{
			"card-test": {Type: "card-test"},
		},
		Routing: Routing{Default: Route{
			NoCard:      "card-test",
			CardPresent: "card-test",
		}},
	}
	cfg.applyDefaults()
	return cfg
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = ":7009"
	}
	if c.Server.FallbackWidth == 0 {
		c.Server.FallbackWidth = 1280
	}
	if c.Server.FallbackHeight == 0 {
		c.Server.FallbackHeight = 1024
	}
	if c.Server.PacketDelay == 0 {
		c.Server.PacketDelay = 200 * time.Microsecond
	}
	for name, session := range c.Sessions {
		session.Type = strings.ToLower(strings.TrimSpace(session.Type))
		if session.Type == "ssh" && session.Port == 0 {
			session.Port = 22
		}
		if session.Type == "ssh" && session.FontSize == 0 {
			session.FontSize = 20
		}
		if session.Type == "rdp" && session.Port == 0 {
			session.Port = 3389
		}
		c.Sessions[name] = session
	}
}

func (c *Config) resolveRelativePaths(base string) {
	for name, session := range c.Sessions {
		session.Image = resolvePath(base, session.Image)
		session.PasswordFile = resolvePath(base, session.PasswordFile)
		session.PrivateKeyFile = resolvePath(base, session.PrivateKeyFile)
		session.KnownHostsFile = resolvePath(base, session.KnownHostsFile)
		session.FontFile = resolvePath(base, session.FontFile)
		c.Sessions[name] = session
	}
}

func resolvePath(base, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(base, path))
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d (want %d)", c.Version, CurrentVersion)
	}
	if c.Server.FallbackWidth < 1 || c.Server.FallbackWidth > 8192 || c.Server.FallbackHeight < 1 || c.Server.FallbackHeight > 8192 {
		return fmt.Errorf("invalid fallback resolution %dx%d", c.Server.FallbackWidth, c.Server.FallbackHeight)
	}
	if (c.Server.DisplayWidth == 0) != (c.Server.DisplayHeight == 0) {
		return fmt.Errorf("display_width and display_height must be set together")
	}
	if c.Server.DisplayWidth < 0 || c.Server.DisplayWidth > 8192 || c.Server.DisplayHeight < 0 || c.Server.DisplayHeight > 8192 {
		return fmt.Errorf("invalid display override %dx%d", c.Server.DisplayWidth, c.Server.DisplayHeight)
	}
	if c.Server.PacketDelay < 0 {
		return fmt.Errorf("packet_delay cannot be negative")
	}
	if len(c.Sessions) == 0 {
		return fmt.Errorf("at least one session is required")
	}
	for name, session := range c.Sessions {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("session name cannot be empty")
		}
		switch session.Type {
		case "card-test":
		case "vnc":
			if _, _, err := net.SplitHostPort(session.Address); err != nil {
				return fmt.Errorf("session %q has invalid VNC address %q: %w", name, session.Address, err)
			}
			if session.Password != "" && session.PasswordFile != "" {
				return fmt.Errorf("session %q cannot set both password and password_file", name)
			}
		case "ssh", "rdp":
			if strings.TrimSpace(session.Hostname) == "" {
				return fmt.Errorf("session %q requires hostname", name)
			}
			if session.Port < 1 || session.Port > 65535 {
				return fmt.Errorf("session %q has invalid port %d", name, session.Port)
			}
			if session.Password != "" && session.PasswordFile != "" {
				return fmt.Errorf("session %q cannot set both password and password_file", name)
			}
			if session.Type == "ssh" {
				if strings.TrimSpace(session.Username) == "" {
					return fmt.Errorf("session %q requires username", name)
				}
				hostKeyMethods := 0
				if session.KnownHostsFile != "" {
					hostKeyMethods++
				}
				if session.HostKeySHA256 != "" {
					hostKeyMethods++
				}
				if session.InsecureIgnoreHostKey {
					hostKeyMethods++
				}
				if hostKeyMethods != 1 {
					return fmt.Errorf("session %q must set exactly one of known_hosts_file, host_key_sha256, or insecure_ignore_host_key", name)
				}
				if session.HostKeySHA256 != "" && !strings.HasPrefix(session.HostKeySHA256, "SHA256:") {
					return fmt.Errorf("session %q host_key_sha256 must start with SHA256:", name)
				}
				if session.Password == "" && session.PasswordFile == "" && session.PrivateKeyFile == "" {
					return fmt.Errorf("session %q requires password, password_file, or private_key_file", name)
				}
				if session.FontSize != 0 && (session.FontSize < 8 || session.FontSize > 72) {
					return fmt.Errorf("session %q has invalid font_size %.1f (want 8 through 72)", name, session.FontSize)
				}
			}
		default:
			return fmt.Errorf("session %q has unsupported type %q", name, session.Type)
		}
	}
	if err := c.validateRoute("routing.default", c.Routing.Default); err != nil {
		return err
	}
	for serial, route := range c.Routing.Terminals {
		if strings.TrimSpace(serial) == "" {
			return fmt.Errorf("terminal serial cannot be empty")
		}
		if err := c.validateRoute("routing.terminals."+serial, route); err != nil {
			return err
		}
	}
	if c.Routing.Default.NoCard == "" || c.Routing.Default.CardPresent == "" {
		return fmt.Errorf("routing.default requires no_card and card_present")
	}
	return nil
}

func (c *Config) validateRoute(location string, route Route) error {
	for field, sessionName := range map[string]string{
		"no_card": route.NoCard, "card_present": route.CardPresent,
	} {
		if sessionName == "" {
			continue
		}
		if _, ok := c.Sessions[sessionName]; !ok {
			return fmt.Errorf("%s.%s references unknown session %q", location, field, sessionName)
		}
	}
	for cardID, sessionName := range route.Cards {
		if strings.TrimSpace(cardID) == "" {
			return fmt.Errorf("%s.cards contains an empty card ID", location)
		}
		if _, ok := c.Sessions[sessionName]; !ok {
			return fmt.Errorf("%s.cards.%s references unknown session %q", location, cardID, sessionName)
		}
	}
	return nil
}

// Resolve selects a session with deterministic precedence: terminal-specific
// card ID, global card ID, terminal state, then global state.
func (c *Config) Resolve(serial, cardID string, cardPresent bool) (string, bool) {
	terminal, hasTerminal := lookupRoute(c.Routing.Terminals, serial)
	if cardPresent {
		if hasTerminal {
			if session, ok := lookupFold(terminal.Cards, cardID); ok {
				return session, true
			}
		}
		if session, ok := lookupFold(c.Routing.Default.Cards, cardID); ok {
			return session, true
		}
		if hasTerminal && terminal.CardPresent != "" {
			return terminal.CardPresent, true
		}
		return c.Routing.Default.CardPresent, c.Routing.Default.CardPresent != ""
	}
	if hasTerminal && terminal.NoCard != "" {
		return terminal.NoCard, true
	}
	return c.Routing.Default.NoCard, c.Routing.Default.NoCard != ""
}

func lookupRoute(routes map[string]Route, key string) (Route, bool) {
	if route, ok := routes[key]; ok {
		return route, true
	}
	for candidate, route := range routes {
		if strings.EqualFold(candidate, key) {
			return route, true
		}
	}
	return Route{}, false
}

func lookupFold(values map[string]string, key string) (string, bool) {
	if value, ok := values[key]; ok {
		return value, true
	}
	for candidate, value := range values {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return "", false
}
