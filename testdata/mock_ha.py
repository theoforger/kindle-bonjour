#!/usr/bin/env python3
"""Tiny fake Home Assistant for local testing (token test-token)."""
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
S = {"light.living_room": "on", "light.kitchen": "off", "switch.porch": "off",
     "switch.fan": "on", "scene.movie_night": "scening", "input_boolean.guest_mode": "off",
     "media_player.tv": "playing", "cover.garage": "closed"}
SHELL = {"monitor_off": {}, "restart_kindle": {"name": "Restart the Kindle"}}
SERVICES = [{"domain": d, "services": {"toggle": {}}} for d in
            ("light", "switch", "input_boolean", "media_player", "cover", "homeassistant")]
SERVICES += [{"domain": "scene", "services": {"turn_on": {}}},
             {"domain": "shell_command", "services": SHELL}]

def toggled(eid, st):
    d = eid.split(".")[0]
    if d == "media_player": return "idle" if st in ("off", "standby") else "off"
    if d == "cover": return "open" if st == "closed" else "closed"
    return "off" if st == "on" else "on"

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _j(self, o, c=200):
        b = json.dumps(o).encode(); self.send_response(c)
        self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer test-token": return self._j({}, 401)
        if self.path == "/api/": return self._j({"message": "API running."})
        if self.path == "/api/services": return self._j(SERVICES)
        if self.path == "/api/states":
            return self._j([{"entity_id": k, "state": v, "attributes": {"friendly_name": k}} for k, v in S.items()])
        eid = self.path.rsplit("/", 1)[-1]
        if eid in S: self._j({"entity_id": eid, "state": S[eid], "attributes": {"friendly_name": eid}})
        else: self._j({}, 404)
    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer test-token": return self._j({}, 401)
        n = int(self.headers.get("Content-Length") or 0); body = json.loads(self.rfile.read(n) or b"{}")
        eid = body.get("entity_id")
        if self.path.startswith("/api/services/shell_command/"):
            name = self.path.rsplit("/", 1)[-1]
            if name not in SHELL: return self._j({"message": "Service not found."}, 400)
            print("shell_command", name, flush=True)
        elif self.path.endswith("homeassistant/toggle") and eid in S:
            S[eid] = toggled(eid, S[eid])
        self._j([])
ThreadingHTTPServer((os.environ.get("MOCK_HA_HOST", "127.0.0.1"), 18123), H).serve_forever()
