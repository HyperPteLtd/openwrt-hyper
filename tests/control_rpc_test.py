"""Run only inside the disposable router test image with its real rpcd."""
import json
import pathlib
import subprocess
import time

init = pathlib.Path("/etc/init.d/hyper")
original = init.read_bytes()

def rpc(method, args=None):
    return json.loads(subprocess.check_output(["ubus", "-t", "5", "call", "luci.hyper", method, json.dumps(args or {})]))

try:
    init.write_text("#!/bin/sh\nsleep 2\nexit 0\n")
    begin = time.monotonic()
    result = rpc("set_enabled", {"enabled": False})
    elapsed = time.monotonic() - begin
    assert result["result"] and result["accepted"], result
    assert elapsed < 1.5, f"control RPC blocked for {elapsed:.2f}s"
    assert result["operation"]["state"] in ("queued", "running"), result
    begin = time.monotonic()
    status = rpc("status")
    assert time.monotonic() - begin < 1.5, "status blocked behind the service operation"
    assert status["operation"]["id"] == result["operation"]["id"]
    deadline = time.monotonic() + 8
    while status["operation"]["state"] in ("queued", "running"):
        assert time.monotonic() < deadline, status
        time.sleep(0.1)
        status = rpc("status")
    assert status["operation"]["state"] == "done", status
    print(f"ok   control RPC acknowledged in {elapsed:.3f}s; status stays responsive until completion")
finally:
    init.write_bytes(original)
