#!/usr/bin/env python3
"""Client-side tunnel probes: tcp echo, udp echo, http GET.

Each subcommand is deterministic, bounded (hard timeouts), and compares exact
bytes, so a pass really proves the tunnel carried the traffic.
"""

import socket
import sys
import urllib.request

TIMEOUT = 10


def probe_tcp(host, port, payload=b"tcp-echo-0123456789"):
    s = socket.create_connection((host, port), timeout=TIMEOUT)
    s.settimeout(TIMEOUT)
    try:
        s.sendall(payload)
        s.shutdown(socket.SHUT_WR)
        got = b""
        while True:
            chunk = s.recv(65536)
            if not chunk:
                break
            got += chunk
        return got == payload
    finally:
        s.close()


def probe_udp(host, port, payload=b"udp-echo-0123456789"):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(TIMEOUT)
    try:
        s.sendto(payload, (host, port))
        got, _ = s.recvfrom(65536)
        return got == payload
    finally:
        s.close()


def probe_http(url, want=b"pong"):
    with urllib.request.urlopen(url, timeout=TIMEOUT) as r:
        return r.status == 200 and r.read() == want


def main():
    if len(sys.argv) < 3:
        print(f"usage: {sys.argv[0]} tcp HOST PORT | udp HOST PORT | http URL", file=sys.stderr)
        return 2
    kind = sys.argv[1]
    try:
        if kind == "tcp":
            ok = probe_tcp(sys.argv[2], int(sys.argv[3]))
        elif kind == "udp":
            ok = probe_udp(sys.argv[2], int(sys.argv[3]))
        elif kind == "http":
            ok = probe_http(sys.argv[2])
        else:
            print(f"unknown probe: {kind}", file=sys.stderr)
            return 2
    except Exception as e:  # noqa: BLE001 — probe must report, not crash
        print(f"probe {kind} error: {e}", file=sys.stderr)
        return 1
    if not ok:
        print(f"probe {kind}: wrong response", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
