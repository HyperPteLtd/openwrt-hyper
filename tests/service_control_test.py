"""Exercise the real worker with BusyBox flock/jsonfilter and a controlled service."""
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import time

if not all(shutil.which(tool) for tool in ("flock", "jsonfilter")):
    print("skip service worker (requires OpenWrt flock/jsonfilter; covered by router tests)")
    raise SystemExit(0)

worker = pathlib.Path(__file__).resolve().parents[1] / "luci-app-hyper/root/etc/hyper/scripts/service_control.sh"
with tempfile.TemporaryDirectory() as tmp:
    root = pathlib.Path(tmp)
    (root / "bin").mkdir()
    (root / "control").mkdir()
    commands = {
        "init": '''#!/bin/sh
mkdir "$HYPER_TEST_ROOT/active" || { echo overlap >> "$HYPER_TEST_ROOT/calls"; exit 1; }
echo "$1" >> "$HYPER_TEST_ROOT/calls"
sleep 1
rmdir "$HYPER_TEST_ROOT/active"
[ ! -e "$HYPER_TEST_ROOT/fail-init" ]
''',
        "uci": '#!/bin/sh\necho 5334\n',
        "curl": '#!/bin/sh\n[ ! -e "$HYPER_TEST_ROOT/fail-ready" ]\n',
    }
    for name, body in commands.items():
        path = root / "bin" / name
        path.write_text(body)
        path.chmod(0o755)
    env = {**os.environ, "HYPER_TEST_ROOT": str(root), "HYPER_INIT": str(root / "bin/init"),
           "HYPER_CONTROL_DIR": str(root / "control"), "HYPER_READY_LIMIT": "2",
           "PATH": str(root / "bin") + ":" + os.environ["PATH"]}
    workers = []

    def request(id, action):
        path = root / "control/request.tmp"
        path.write_text(json.dumps(dict(id=id, action=action, time=int(time.time()))))
        path.replace(root / "control/request.json")
        p = subprocess.Popen(["sh", str(worker)], env=env)
        workers.append(p)
        return p

    def state():
        return json.loads((root / "control/state.json").read_text())

    try:
        request(1, "start")
        deadline = time.monotonic() + 5
        while not (root / "active").exists():
            assert time.monotonic() < deadline, "worker did not start"
            time.sleep(0.01)
        request(2, "stop")
        # A duplicate worker must not execute an already-applied request again.
        workers.append(subprocess.Popen(["sh", str(worker)], env=env))
        for p in workers:
            assert p.wait(timeout=10) == 0
        assert (root / "calls").read_text().splitlines() == ["start", "stop"]
        assert state()["id"] == 2 and state()["state"] == "done"
        print("ok   service worker serializes overlapping requests and skips duplicates")

        (root / "fail-init").touch()
        request(3, "start").wait(timeout=10)
        assert state()["state"] == "failed" and state()["error"] == "service_failed"
        (root / "fail-init").unlink()
        (root / "fail-ready").touch()
        request(4, "start").wait(timeout=10)
        assert state()["state"] == "failed" and state()["error"] == "not_ready"
        (root / "fail-ready").unlink()
        request(5, "start").wait(timeout=10)
        assert state()["id"] == 5 and state()["state"] == "done"
        print("ok   service worker reports init/readiness failures and allows retry")
    finally:
        for p in workers:
            if p.poll() is None:
                p.kill()
                p.wait()
