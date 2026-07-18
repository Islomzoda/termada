#!/usr/bin/env python3
import json
import pathlib
import sys
import urllib.error
import urllib.request

expected = sys.argv[1] if len(sys.argv) > 1 else "healthy"
root = pathlib.Path(__file__).resolve().parent
port = (root / "service.port").read_text(encoding="utf-8").strip()
try:
    response = urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=2)
    code = response.status
    body = json.loads(response.read())
except urllib.error.HTTPError as error:
    code = error.code
    body = json.loads(error.read())

actual = "healthy" if code == 200 and body.get("status") == "ok" else "broken"
print(json.dumps({"expected": expected, "actual": actual, "http_status": code, "response": body}, sort_keys=True))
raise SystemExit(0 if actual == expected else 1)
