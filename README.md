# SunRay2Server

SunRay2Server is an experimental Go server for Sun Ray 2 terminals. It accepts
the Sun Ray authentication/display protocol and routes each terminal to a
local test page, a full-screen web dashboard, or a remote SSH, VNC, or RDP
session.

The current card reader integration reliably distinguishes `no-card` and
`card-present`. Some firmware reports `card_id=0`, so exact card routing is
available only when the terminal provides a real ID.

## Features and limitations

- Sun Ray authentication, display, keyboard, pointer, and wheel input.
- `card-test` and interactive `geometry-test` sessions.
- Full-screen Chromium web sessions with live page updates and input.
- SSH with a PTY and framebuffer terminal.
- VNC with per-session credentials, scaling, and CopyRect updates.
- RDP through FreeRDP + Xvfb + x11vnc.
- Optional passive smart-card/ATR observation on TCP 4120.
- No audio forwarding yet. Sun Ray and VNC transports are not encrypted;
  use them only on a trusted network or through a secure tunnel.
- A physical monitor is not required. Web and RDP sessions use Xvfb.

## Build and test

Go 1.23 or newer is recommended.

```sh
make test
make vet
make build
```

Run the generated test pattern:

```sh
./sunrayd -listen :7009 -debug
```

For configuration-based operation:

```sh
cp config.yaml.template config.yaml
# Edit config.yaml, then:
./sunrayd -config ./config.yaml -debug
```

The old one-command VNC path is still available:

```sh
./sunrayd -vnc 192.168.30.10:5900 -debug
```

## Configuration

`config.yaml.template` is the complete starting point. The file has three
sections:

- `server`: TCP listener, fallback/canvas resolution, packet pacing, logging,
  and optional smart-card probing.
- `sessions`: named `card-test`, `geometry-test`, `web`, `vnc`, `ssh`, or `rdp`
  sessions.
- `routing`: maps `no_card`, `card_present`, exact card IDs, or terminal serials
  to session names. Terminal-specific routes take precedence over defaults.

Example:

```yaml
version: 1

server:
  listen: ":7009"
  display_width: 1400
  display_height: 1050
  packet_delay: 150us
  log_input_events: false

sessions:
  card-test:
    type: card-test
  dashboard:
    type: web
    url: https://example.com/
    interactive: true
    max_fps: 20
    resolution_mode: current
  office-vnc:
    type: vnc
    address: 192.168.30.10:5900
    password: change-me
    interactive: true
    max_fps: 20
    resolution_mode: current
  admin-ssh:
    type: ssh
    hostname: 192.168.30.20
    port: 22
    username: user
    password: change-me
    insecure_ignore_host_key: true # use a known_hosts file in production

routing:
  default:
    no_card: card-test
    card_present: office-vnc
    # cards:
    #   "0x0123456789abcdef": admin-ssh
```

Passwords are per session. Use `password_file` instead of inline `password`
when the configuration is shared or committed. They are mutually exclusive.
Relative image, font, key, known-hosts, and password paths are resolved from
the directory containing `config.yaml`.

Web, VNC, and RDP sessions accept `interactive`. It defaults to `true`; setting
it to `false` disables keyboard, mouse, button, and wheel input and hides the
cursor for a display-only session.

Those graphical sessions also accept `max_fps` from `1` to `60`. It defaults
to `20` and coalesces intermediate updates instead of queueing stale frames.

### Display geometry

The Sun Ray reports a logical canvas, which is separate from the monitor's DVI
timing. To calibrate it, temporarily route a card state to `geometry-test`:

```yaml
sessions:
  geometry-test:
    type: geometry-test
routing:
  default:
    no_card: geometry-test
    card_present: geometry-test
```

Use arrow keys to change the width/height, `Shift` for 1-pixel steps, `Ctrl`
for 100-pixel steps, `R` to reset, and `Enter` to log the result. Adjust until
all four colored edges fit without panning, then copy the displayed value to
`server.display_width` and `server.display_height`. A tested Sun Ray 2 with a
1920x1080 monitor used `1400x1050`; measure each installation.

VNC, RDP, and web resolution modes:

- `current`: the configured/current Sun Ray canvas (default).
- `terminal`: the original `startRes` reported by the terminal.
- `manual`: session-level `display_width` and `display_height`.
- VNC also supports `vnc`, which keeps the remote framebuffer at native size;
  a larger desktop may pan on the Sun Ray.

### Web dashboards

A `web` session opens its `url` as a full-screen Chromium application inside a
private Xvfb display. The page remains loaded: JavaScript timers, WebSocket,
SSE, and page-managed updates continue normally, with no periodic reload.
Framebuffer changes are forwarded through the local VNC bridge. WebGL and
WebGL2 use Chromium's CPU-based SwiftShader renderer because Xvfb has no GPU.

```yaml
sessions:
  dashboard:
    type: web
    url: https://example.com/
    interactive: false
    reload_interval: 30m # optional; omit or use 0 to disable automatic reload
    resolution_mode: current
    browser_no_sandbox: true # commonly required by Docker/LXC
```

`reload_interval` accepts Go-style durations such as `30s`, `10m`, or `2h` and
performs a real browser reload at that interval. It defaults to `0`, so pages
that update themselves remain loaded indefinitely. Automatic reload also works
with `interactive: false`. When interaction is enabled, `Ctrl+R` or `F5`
reloads, `Alt+Left` goes back, and `Alt+Right` goes forward. Web sessions support
the same `current`, `terminal`, and `manual` resolution modes as RDP.

Chromium's sandbox is enabled by default in the application. Docker and nested
LXC environments commonly block the required namespaces, so the supplied YAML
template explicitly sets `browser_no_sandbox: true`. This weakens browser
isolation: use trusted URLs and set it back to `false` when the host supports
Chromium's sandbox. SwiftShader itself is also an explicitly enabled software
WebGL fallback, so avoid loading untrusted pages.

### SSH

SSH requires exactly one host-key policy: `known_hosts_file`,
`host_key_sha256`, or `insecure_ignore_host_key`. The last option is intended
only for testing. Passwords and private keys can be supplied with `password`,
`password_file`, or `private_key_file`.

### RDP

RDP sessions require `hostname`, `username`, and a password/password file. The
`certificate` policy defaults to `deny`; `ignore` is convenient for a trusted
LAN test with a self-signed server. Supported policies are `deny`, `ignore`,
`tofu`, `name:<certificate-name>`, and `fingerprint:<hash>`.

The supplied Docker image includes Chromium, Japanese/emoji fonts, `xfreerdp`,
`Xvfb`, and `x11vnc`. A native installation must provide the relevant programs
in `PATH`.

## Docker Compose

The default Compose file pulls the published image from GHCR and uses host
networking. Host networking is required for TCP 7009 and the dynamic,
bidirectional UDP display channel.

```sh
cp config.yaml.template config.yaml
# Edit config.yaml
SUNRAY_CONFIG=./config.yaml COMPOSE_MENU=false docker compose up
```

After a new `main` build:

```sh
docker compose pull
SUNRAY_CONFIG=./config.yaml COMPOSE_MENU=false docker compose up -d
```

Each push to `main` publishes `linux/amd64` and `linux/arm64` images. A local
checkout can be built with:

```sh
SUNRAY_CONFIG=./config.yaml \
docker compose -f compose.yaml -f compose.build.yaml up --build
```

For a private GHCR package, run `docker login ghcr.io` first. The optional
`SUNRAY_VNC_PASSWORD_FILE` variable supplies the Compose secret used by a
session with `password_file: /run/secrets/vnc_password`.

### Docker Desktop and nested containers

On Docker Desktop, enable **Settings → Resources → Network → host networking**
before starting Compose. If the server logs `client_ip=::1`, Docker Desktop is
proxying the connection; map the terminal's serial to its stable IPv4 address:

```yaml
server:
  terminal_ips:
    "00144fd19044": 192.168.30.153
```

When Compose runs inside a minimal Alpine container, `COMPOSE_MENU=false`
suppresses the harmless `termbox: unsupported terminal` warning. If a custom
`/tmp` mount is used, keep `/tmp/.X11-unix` as a root-owned directory with mode
`1777`; otherwise Xvfb may fail to select a display. No physical screen is
needed, including in a PVE/LXC container.

## Sun Ray networking

The terminal must discover this server using the same DHCP/DNS/static settings
that were used for kOpenRay. Permit:

- TCP `7009` for authentication;
- dynamic bidirectional UDP between the server and the terminal for display;
- TCP `4120` only when the optional smart-card probe is enabled.

The Sun Ray display transport currently uses no encryption. Classic VNC
authentication and RFB traffic are also unencrypted. Keep the service on an
isolated trusted LAN or use an external tunnel/VPN.

## Standard keyboard shortcuts

With a normal PC keyboard, hold `Ctrl` and press `Pause/Break`, then:

| Shortcut | Action |
| --- | --- |
| `Ctrl` + `Pause/Break` + `M` | Open the Sun Ray configuration menu |
| `Ctrl` + `Pause/Break` + `V` | Show firmware version information |
| `Ctrl` + `Pause/Break` + `C` | Show clear-configuration options |

On compact keyboards, `Pause/Break` may require `Fn`.

## Troubleshooting

- **The terminal cannot connect:** check TCP 7009, Sun Ray discovery settings,
  and host networking. On Docker Desktop, add `server.terminal_ips` when the
  log shows `client_ip=::1`.
- **The display is slow or resends repeat:** keep `log_input_events: false`,
  reduce the logical resolution, or lower the session's `max_fps` (the default
  is `20`; `15` is a conservative starting point). `packet_delay` remains the
  fixed low-level packet pacing value.
- **Web or RDP stops before showing a desktop:** verify that Chromium or
  `xfreerdp`, plus `Xvfb` and `x11vnc`, are present; in a customized container
  also verify `/tmp/.X11-unix` permissions.
- **The interactive Compose menu warns about terminfo:** set
  `COMPOSE_MENU=false`; this is a Compose CLI warning, not a Sun Ray error.

## License

The ALP authentication/display implementation is derived from
kOpenRay/jOpenRay. Source files are GPL-2.0-or-later; see [LICENSE](LICENSE)
and [NOTICE](NOTICE).
