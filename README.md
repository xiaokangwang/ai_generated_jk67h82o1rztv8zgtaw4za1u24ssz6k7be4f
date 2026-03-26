# socks5udp-proxy

A small Go project that exposes a UDP port and accepts SOCKS5 UDP packets directly, without the normal SOCKS5 TCP handshake or `UDP ASSOCIATE` step.

## What it does

- Listens on a UDP port.
- Expects each incoming datagram to already be in SOCKS5 UDP request format.
- Extracts the target host and port from the packet.
- Forwards the payload to that UDP target.
- Sends upstream replies back to the client wrapped in SOCKS5 UDP format.

## What it does not do

- No TCP SOCKS5 handshake.
- No authentication.
- No support for `FRAG != 0`.
- No TCP proxying.

## Packet format

The proxy expects direct UDP packets in the RFC 1928 SOCKS5 UDP encapsulation format:

```text
+----+----+----+----+------+----------+----------+
|RSV |RSV |FRAG|ATYP| DST  |   PORT   |   DATA   |
+----+----+----+----+------+----------+----------+
| 00 | 00 | 00 | 01 | IPv4 | 2 bytes  | variable |
| 00 | 00 | 00 | 03 | NAME | 2 bytes  | variable |
| 00 | 00 | 00 | 04 | IPv6 | 2 bytes  | variable |
```

Replies are sent back in the same format, using the upstream sender as the encoded address.

## Run

```bash
go run . -listen :1080
```

Optional flags:

```bash
go run . -listen :1080 -idle-timeout 2m
go run . -listen :1080 -split-destinations=true
go run . -listen :1080 -split-destinations=true -max-open-sockets 32
go run . -listen :1080 -split-destinations=false -incoming-filter=true -incoming-allow-period 30s
go run . -listen :1080 -split-destinations=false -incoming-filter=true -incoming-allow-period 30s -max-allowed-destinations 64
```

## Notes

This is intentionally non-standard from a SOCKS5 client perspective. A normal SOCKS5 client will usually expect to negotiate over TCP first. This proxy is for clients that already know the UDP relay port and can emit SOCKS5 UDP packets directly.

`-split-destinations=true` is the strict-NAT-style mode. Each client keeps a map of destination addresses, and each destination gets its own dialed UDP socket, so different targets use different upstream source ports.

`-max-open-sockets` only matters when `-split-destinations=true`. When the client reaches that limit and sends to a new destination, the least recently used destination socket is closed before the new dialed socket is created.

`-split-destinations=false` reuses one bound UDP socket per client and sends packets with `WriteToUDP`, which is useful groundwork for looser NAT simulations later.

`-incoming-filter=true` on `-split-destinations=false` simulates a moderate/address-restricted NAT. The proxy keeps a recent-destination allow list per client and only forwards inbound UDP if it comes from a destination the client sent to within `-incoming-allow-period`.

`-max-allowed-destinations` only matters when `-incoming-filter=true`. When the allow list reaches that limit and the client sends to a new destination, the least recently used destination is removed from the allow list before the new one is recorded.
