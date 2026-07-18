#!/usr/bin/env python3
"""Exercise the complete demo through MCP; operator approval uses the real API."""
import json
import os
import pathlib
import re
import subprocess
import sys
import time
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
STATE = pathlib.Path(os.environ.get("TERMADA_DEMO_STATE", os.path.join(os.environ.get("TMPDIR", "/tmp"), f"termada-mission-control-{os.getuid()}")))
WORKSPACE = STATE / "broken-checkout"
process = subprocess.Popen([str(HERE / "mcp.sh")], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=sys.stderr, text=True, bufsize=1)
next_id = 1


def rpc(method, params):
    global next_id
    request_id = next_id
    next_id += 1
    process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}) + "\n")
    process.stdin.flush()
    while True:
        response = json.loads(process.stdout.readline())
        if response.get("id") == request_id:
            if response.get("error"):
                raise RuntimeError(response["error"])
            return response["result"]


def tool(name, arguments=None):
    result = rpc("tools/call", {"name": name, "arguments": arguments or {}})
    if result.get("isError"):
        raise RuntimeError(result["structuredContent"])
    return result["structuredContent"]


def approve(confirmation_id):
    log = (STATE / "daemon.log").read_text(encoding="utf-8")
    match = re.search(r"dashboard:\s+(http://127\.0\.0\.1:\d+)/", log)
    if not match:
        raise RuntimeError("dashboard address not found")
    token = (STATE / "home/.config/termada/token").read_text(encoding="utf-8").strip()
    request = urllib.request.Request(match.group(1) + "/api/approve", data=json.dumps({"confirmation_id": confirmation_id}).encode(), headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    urllib.request.urlopen(request, timeout=3).read()


def passed_step(mission_id, step_id, job_id, note=""):
    tool("mission_update", {"mission_id": mission_id, "step_id": step_id, "step_status": "passed", "job_id": job_id, "note": note})


try:
    rpc("initialize", {"protocolVersion": "2025-06-18", "clientInfo": {"name": "build-week-verifier", "version": "1"}, "capabilities": {}})
    created = tool("mission_create", {"title": "Restore checkout health", "goal": "Restore the isolated checkout service and prove its health endpoint is ready.", "target": "local", "workspace": "checkout-demo", "plan": ["Establish the expected failure", "Identify the faulty service mode", "Apply the protected fix", "Verify the restored health endpoint"]})
    mission_id, session_id = created["id"], created["session_id"]
    tool("exec_run", {"session": session_id, "command": ["cd", str(WORKSPACE)]})

    diagnosed = tool("exec_run", {"session": session_id, "command": ["./probe.py", "broken"]})
    assert diagnosed["status"] == "exited" and diagnosed["exit_code"] == 0
    passed_step(mission_id, "step_1", diagnosed["job_id"], "HTTP 503 and degraded status reproduced")

    inspected = tool("exec_run", {"session": session_id, "command": ["cat", "service.mode"]})
    assert inspected["status"] == "exited" and inspected["exit_code"] == 0 and "broken" in inspected["stdout"]
    passed_step(mission_id, "step_2", inspected["job_id"], "service.mode was broken")

    gated = tool("exec_run", {"session": session_id, "command": ["./apply-fix.sh"]})
    assert gated["status"] == "awaiting_confirmation" and gated["confirmation_id"]
    if os.environ.get("TERMADA_DEMO_PAUSE_APPROVAL") == "1":
        print(json.dumps({"mission_id": mission_id, "status": "needs_attention", "confirmation_id": gated["confirmation_id"]}, indent=2), flush=True)
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            applied = tool("exec_poll", {"job_id": gated["job_id"], "wait_ms": 1000})
            if applied["status"] in {"exited", "failed", "killed", "timed_out", "orphaned"}:
                break
            time.sleep(0.25)
        else:
            raise RuntimeError("approval was not resolved within 180 seconds")
    else:
        approve(gated["confirmation_id"])
        applied = tool("exec_poll", {"job_id": gated["job_id"], "wait_ms": 30000})
    assert applied["status"] == "exited" and applied["exit_code"] == 0
    passed_step(mission_id, "step_3", gated["job_id"], "Human-approved SIGHUP reload completed")

    verified = tool("exec_run", {"session": session_id, "command": ["./probe.py", "healthy"]})
    assert verified["status"] == "exited" and verified["exit_code"] == 0
    passed_step(mission_id, "step_4", verified["job_id"], "HTTP 200 and status ok observed")

    finished = tool("mission_update", {"mission_id": mission_id, "status": "succeeded", "summary": "Changed the isolated service mode from broken to healthy, reloaded it after human approval, and verified HTTP 200 with status ok."})
    assert finished["status"] == "succeeded"
    report = tool("mission_report", {"mission_id": mission_id})
    assert len(report["sha256"]) == 64 and "Audit Chain Anchors" in report["report"]
    print(json.dumps({"mission_id": mission_id, "status": finished["status"], "report_sha256": report["sha256"], "approval": "observed and resolved", "health": "HTTP 200"}, indent=2))
finally:
    process.terminate()
    process.wait(timeout=5)
