#!/usr/bin/env python3
import http.server
import json
import pathlib
import signal
import sys

ROOT = pathlib.Path(__file__).resolve().parent
MODE_FILE = ROOT / "service.mode"
PORT_FILE = ROOT / "service.port"
mode = "broken"


def reload_mode(*_args):
    global mode
    mode = MODE_FILE.read_text(encoding="utf-8").strip()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/health":
            self.send_error(404)
            return
        healthy = mode == "healthy"
        body = json.dumps({"status": "ok" if healthy else "degraded", "mode": mode}).encode()
        self.send_response(200 if healthy else 503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        sys.stdout.write((fmt % args) + "\n")
        sys.stdout.flush()


reload_mode()
signal.signal(signal.SIGHUP, reload_mode)
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
PORT_FILE.write_text(str(server.server_port), encoding="utf-8")
server.serve_forever()
