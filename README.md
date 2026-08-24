# SunRay2Server

An experimental, headless Go server for Sun Ray 2 terminals. This first
milestone implements only enough of the kOpenRay-compatible protocol to:

- accept the terminal authentication connection on TCP port 7009;
- observe terminal, smart-card and resolution properties;
- negotiate the unencrypted display session used by kOpenRay;
- open a bidirectional UDP display channel;
- send bounds, fill, cursor and 24-bit RGB bitmap operations;
- handle display keepalives and NACK-based operation retransmission;
- decode USB HID keyboard reports and absolute pointer/button reports from the
  terminal UDP channel;
- show a generated color test pattern or a supplied PNG/JPEG;
- overlay the reported card type, card ID and insert/remove state on screen;
- reduce the observed card events to stable `no-card` and `card-present`
  session slots;
- passively capture the initial Sun Ray smart-card service traffic on TCP 4120
  and recognize valid ISO 7816 ATR byte strings when explicitly enabled.

VNC is the first connected remote-session adapter. SSH, RDP and persistent card
registration are not connected yet. The two card states are detected as session
slots, but this first VNC milestone uses one configured VNC target for both;
binding a different adapter or target to each slot is the next step.

## Security

The Sun Ray transport currently announces `encUpType=none` and
`encDownType=none`, matching kOpenRay. Run this server only on an isolated,
trusted LAN or VLAN. Do not expose TCP 7009 or the UDP display traffic to the
internet. The disabled-by-default smart-card probe on TCP 4120 is read-only: it sends no
PC/SC response and no APDU to the card. It is intended only for protocol
observation on the same isolated network.

Classic VNC password authentication and RFB framebuffer/input traffic are not
encrypted. Use the VNC adapter only on a trusted network or through a separately
secured tunnel. The password is read from a file and is never accepted as a
command-line value, but classic VNC authentication still uses only its first
eight bytes.

## Build and test

Go 1.23 or later is recommended.

```sh
make test
make vet
make build
```

The test suite includes byte-level operation encoding checks, ATR and HID input
parsing, and a local UDP round trip which verifies packet sequencing, input
dispatch and NACK retransmission.

## Run

With the built-in 640x360 test pattern:

```sh
./sunrayd -listen :7009 -debug
```

With an existing PNG or JPEG:

```sh
./sunrayd -listen :7009 -image /absolute/path/to/test.png -debug
```

Connect to a VNC server without authentication:

```sh
./sunrayd -listen :7009 -vnc 192.168.30.10:5900 -debug
```

For a server using classic VNC Password authentication, create a password file
containing only the password, restrict its filesystem permissions, then run:

```sh
./sunrayd -listen :7009 \
  -vnc 192.168.30.10:5900 \
  -vnc-password-file /absolute/path/to/vnc-password \
  -debug
```

The VNC client negotiates RFB 3.3, 3.7 or 3.8 and supports Security None,
classic VNC authentication, Raw, CopyRect and DesktopSize. TLS/VeNCrypt and
compressed encodings such as Tight, ZRLE and Hextile are not part of this first
milestone. Configure the VNC server to permit one of the supported security
types. A smaller VNC desktop is centered on the Sun Ray display; a larger one
is cropped to the terminal resolution.

Useful flags:

```text
-fallback-width 1280
-fallback-height 1024
-packet-delay 200us
-smartcard-listen ''
-vnc ''
-vnc-password-file ''
```

The smart-card probe is disabled by default. It can still be enabled for later
diagnostics with `-smartcard-listen :4120`.

The server prefers the first resolution from the terminal's `startRes`
property. The fallback is used only when that property is absent or invalid.

## Point the Sun Ray at the server

Use the same DHCP/DNS/static-server configuration that previously pointed the
terminal at kOpenRay. Depending on the terminal firmware, this may involve the
`sunray-config-servers` and `sunray-servers` DNS names, Sun Ray-specific DHCP
options, or a server address configured from the terminal menu.

### Sun Ray 2 shortcuts with a standard keyboard

A Sun keyboard is not required to open the Sun Ray 2 configuration and
diagnostic screens. With a standard PC keyboard, hold `Ctrl` and press the
following key sequence:

| Shortcut | Action |
| --- | --- |
| `Ctrl` + `Pause/Break` + `M` | Open the configuration menu. |
| `Ctrl` + `Pause/Break` + `V` | Display firmware version information. |
| `Ctrl` + `Pause/Break` + `C` | Display the clear-configuration options. |

On compact keyboards, `Pause/Break` may require the keyboard's `Fn` modifier.

macOS or Linux firewall rules must permit inbound TCP 7009. TCP 4120 is needed
only when the optional smart-card probe is enabled. The terminal also
publishes its UDP display port as the authentication property `pn`; the server
uses an ephemeral local UDP port to communicate with it, so the isolated LAN
must allow bidirectional UDP traffic between the terminal and server.

Expected log sequence:

```text
Sun Ray authentication server listening
authentication client connected
Sun Ray information ...
session slot selected ... slot=no-card
display channel opened ... terminal_udp=... server_udp=... resolution=...
test image transmission complete
```

With `-vnc`, the test/card image remains visible while the connection is being
established. A successful connection then logs:

```text
VNC session connected ... server=192.168.30.10:5900 desktop=... resolution=(...,...)
```

The session automatically reconnects after network or VNC server failures.

With `-debug`, keyboard and pointer activity should now produce structured
lines such as:

```text
keyboard input ... hid=0x04 pressed=true modifiers=0x00
keyboard input ... hid=0x04 pressed=false modifiers=0x00
pointer input ... x=320 y=200 buttons=0x01
```

The HID value is preserved instead of being converted prematurely, allowing
the VNC, SSH and RDP adapters to apply their own key mappings.

When the optional probe is enabled, a terminal that opens its smart-card service channel
will additionally produce logs such as:

```text
smart-card service connection detected client=192.168.30.153:...
smart-card probe frame ... hex=...
ATR detected ... atr=... protocols=T=0
```

The probe does not answer the terminal's PC/SC/scbus handshake. Therefore a
connection or raw frame without an `ATR detected` line is still a useful first
result: retain the complete debug log so that the next step can implement the
minimum required handshake. No connection on TCP 4120 means the terminal has
not attempted the separate smart-card service, which may require firmware or
server-discovery configuration.

An ATR normally identifies a card family and its supported transport protocol,
not a guaranteed unique physical-card serial number. Two cards with identical
ATRs still require a card-specific command to distinguish them, and this probe
deliberately does not issue one.

Expected screen result: a centered six-color checkerboard test image with a
white border on a black background. A dark panel shows the current card reader
state, reported card type and card ID. `PSEUDO` selects the `no-card` session
slot. A physical card reported as `T1unknown` with an all-zero ID selects the
`card-present` slot. Transient `remove` messages are ignored because this
firmware pairs them with the definitive next `insert` state.

## Real-hardware acceptance checklist

- The terminal connects and logs its serial number and native resolution.
- Inserting and removing a card changes the authentication event, even if the
  firmware reports an all-zero `card_id`.
- Logs alternate reliably between `slot=no-card` and `slot=card-present`.
- Pressing and releasing a key logs matching HID transitions.
- Moving, clicking and scrolling the mouse logs coordinates and button masks.
- A configured VNC desktop replaces the test image after connection.
- Keyboard, pointer buttons, movement and wheel events reach the VNC server.
- Restarting the VNC server causes an automatic reconnect without restarting `sunrayd`.
- Removing the card returns the panel to `READER READY` without restarting the server.
- The generated test pattern is centered and has correct RGB colors.
- No bands, stale regions or persistent corruption remain after retransmits.
- Removing and reinserting the card starts a clean display session.
- The server remains responsive after disconnecting and reconnecting Ethernet.

If the image is incomplete, rerun with a larger packet delay, for example
`-packet-delay 1ms`, and retain the debug log plus a packet capture for protocol
comparison with kOpenRay.

## License and provenance

The ALP authentication and display encoding are derived from kOpenRay/jOpenRay.
Source files carry `SPDX-License-Identifier: GPL-2.0-or-later`; see `NOTICE` and
`LICENSE`.
