#!/usr/bin/env python3
"""Tiny fake Home Assistant for local testing."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
S = {"light.living_room": "on", "light.kitchen": "off", "switch.porch": "off",
     "switch.fan": "on", "scene.movie_night": "scening", "input_boolean.guest_mode": "off"}
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _j(self, o, c=200):
        b = json.dumps(o).encode(); self.send_response(c)
        self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer test-token": return self._j({}, 401)
        eid = self.path.rsplit("/", 1)[-1]
        if eid in S: self._j({"entity_id": eid, "state": S[eid], "attributes": {"friendly_name": eid}})
        else: self._j({}, 404)
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0); body = json.loads(self.rfile.read(n) or b"{}")
        eid = body.get("entity_id")
        if self.path.endswith("homeassistant/toggle") and eid in S:
            S[eid] = "off" if S[eid] == "on" else "on"
        self._j([])
ThreadingHTTPServer(("127.0.0.1", 18123), H).serve_forever()
