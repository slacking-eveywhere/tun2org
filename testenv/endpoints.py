#!/usr/bin/env python3
"""Endpoint services for the e2e target container.

Serves, in one process:
  - HTTP on :8080 (GET /ping -> 200 "pong")
  - TCP echo on :9000
  - UDP echo on :9001
"""

import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HTTP_PORT = 8080
TCP_PORT = 9000
UDP_PORT = 9001


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"pong"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def tcp_echo():
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", TCP_PORT))
    srv.listen(32)
    while True:
        conn, _ = srv.accept()
        threading.Thread(target=echo_conn, args=(conn,), daemon=True).start()


def echo_conn(conn):
    with conn:
        while True:
            data = conn.recv(65536)
            if not data:
                return
            conn.sendall(data)


def udp_echo():
    srv = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    srv.bind(("0.0.0.0", UDP_PORT))
    while True:
        data, addr = srv.recvfrom(65536)
        srv.sendto(data, addr)


def main():
    threading.Thread(target=tcp_echo, daemon=True).start()
    threading.Thread(target=udp_echo, daemon=True).start()
    httpd = ThreadingHTTPServer(("0.0.0.0", HTTP_PORT), Handler)
    print(
        f"endpoints: http://0.0.0.0:{HTTP_PORT}/ping "
        f"tcp-echo 0.0.0.0:{TCP_PORT} udp-echo 0.0.0.0:{UDP_PORT}",
        flush=True,
    )
    httpd.serve_forever()


if __name__ == "__main__":
    main()
