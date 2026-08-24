# SunRay2Server

An experimental, headless Go server for Sun Ray 2 terminals. This first
milestone implements only enough of the kOpenRay-compatible protocol to:

- accept the terminal authentication connection on TCP port 7009;
- observe terminal, smart-card and resolution properties;
- negotiate the unencrypted display session used by kOpenRay;
- open a bidirectional UDP display channel;
- send bounds, fill, cursor and 24-bit RGB bitmap operations;
- handle display keepalives and NACK-based operation retransmission;
- show a generated color test pattern or a supplied PNG/JPEG.

SSH, VNC, RDP, card registration and input forwarding are intentionally not
part of this milestone. They will be added only after real Sun Ray hardware
confirms that authentication and display traffic work.

## Security

The Sun Ray transport currently announces `encUpType=none` and
`encDownType=none`, matching kOpenRay. Run this server only on an isolated,
trusted LAN or VLAN. Do not expose TCP 7009 or the UDP display traffic to the
internet.

## Build and test

Go 1.23 or later is recommended.

```sh
make test
make vet
make build
```

The test suite includes byte-level operation encoding checks and a local UDP
round trip which verifies packet sequencing and NACK retransmission.

## Run

With the built-in 640x360 test pattern:

```sh
./sunrayd -listen :7009 -debug
```

With an existing PNG or JPEG:

```sh
./sunrayd -listen :7009 -image /absolute/path/to/test.png -debug
```

Useful flags:

```text
-fallback-width 1280
-fallback-height 1024
-packet-delay 200us
```

The server prefers the first resolution from the terminal's `startRes`
property. The fallback is used only when that property is absent or invalid.

## Point the Sun Ray at the server

Use the same DHCP/DNS/static-server configuration that previously pointed the
terminal at kOpenRay. Depending on the terminal firmware, this may involve the
`sunray-config-servers` and `sunray-servers` DNS names, Sun Ray-specific DHCP
options, or a server address configured from the terminal menu.

macOS or Linux firewall rules must permit inbound TCP 7009. The terminal also
publishes its UDP display port as the authentication property `pn`; the server
uses an ephemeral local UDP port to communicate with it, so the isolated LAN
must allow bidirectional UDP traffic between the terminal and server.

Expected log sequence:

```text
Sun Ray authentication server listening
authentication client connected
Sun Ray information ...
display channel opened ... terminal_udp=... server_udp=... resolution=...
test image transmission complete
```

Expected screen result: a centered six-color checkerboard test image with a
white border on a black background.

## Real-hardware acceptance checklist

- The terminal connects and logs its serial number and native resolution.
- Inserting a card logs a stable `card_type` and `card_id`.
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

