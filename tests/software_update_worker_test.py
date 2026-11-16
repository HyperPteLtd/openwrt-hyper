#!/usr/bin/env python3
"""Exercise the installed update worker in the isolated router-test container.

Only network/package-manager executables are stubbed; real ucode, UCI, locking,
module loading, checksums, scheduler and installer state transitions are used.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time

assert os.environ.get("HYPER_TEST_CONTAINER") == "1", "run only inside router-test.sh's disposable container"
runtime = Path("/var/run/hyper-software-update")
worker = "/etc/hyper/scripts/auto_update.sh"
backend = "/usr/share/rpcd/ucode/luci.hyper"
control = Path("/usr/lib/opkg/info/luci-app-hyper.control")
apk = Path("/usr/bin/apk").exists() and Path("/etc/apk").exists()
manager = Path("/usr/bin/apk" if apk else "/bin/opkg")
if apk:
    arch = subprocess.check_output([str(manager), "--print-arch"], text=True).strip()
    package = f"25.12+_luci-app-hyper-9.9.9-r1-{arch}.apk"
else:
    architectures = subprocess.check_output([str(manager), "print-architecture"], text=True).splitlines()
    arch = [line.split()[1] for line in architectures if line.startswith("arch ") and line.split()[1] not in ("all", "noarch")][-1]
    package = f"23.05-24.10_luci-app-hyper_9.9.9-r1_{arch}.ipk"


def until(predicate, description):
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.03)
    raise AssertionError(description)


def method(name, args=None):
    source = f"const m=loadfile('{backend}')()['luci.hyper']; printf('%J',m.{name}.call({{args:{json.dumps(args or {})}}}));"
    return json.loads(subprocess.check_output(["/usr/bin/ucode", "-S", "-e", source], text=True, timeout=10))


with tempfile.TemporaryDirectory(prefix="hyper-update-worker-") as temp:
    fixture = Path(temp)
    backups = {}
    for path in [Path("/usr/bin/curl"), manager, Path("/etc/init.d/rpcd"), Path("/etc/config/hyper"), control]:
        backup = fixture / f"backup-{len(backups)}"
        if path.exists() or path.is_symlink():
            shutil.copy2(path, backup, follow_symlinks=False)
            backups[path] = backup
        else:
            backups[path] = None
    if runtime.exists():
        shutil.copytree(runtime, fixture / "runtime-backup")
    content = b"test package data verified by the real checksum path\n"
    (fixture / "package").write_bytes(content)
    (fixture / "sha256sums").write_text(f"{hashlib.sha256(content).hexdigest()}  {package}\n")
    (fixture / "release").write_text(json.dumps({"tag_name": "v9.9.9", "assets": [
        {"name": package, "browser_download_url": "https://release.test/package"},
        {"name": "sha256sums", "browser_download_url": "https://release.test/sha256sums"},
    ]}))

    def replace(path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.unlink(missing_ok=True)
        path.write_text(text)
        path.chmod(0o755)

    replace(Path("/usr/bin/curl"), f'''#!/usr/bin/python3
import json, subprocess, sys, time
from pathlib import Path
root=Path({str(fixture)!r})
config={{}}
for line in Path(sys.argv[2]).read_text().splitlines():
    if " = " in line:
        key,value=line.split(" = ",1)
        try: config[key]=json.loads(value)
        except ValueError: pass
url=config["url"]
with (root/"requests").open("a") as f: f.write(url+"\\n")
mode=(root/"mode").read_text()
name="release" if "/releases/latest" in url else url.rsplit("/",1)[-1]
if mode=="slow-check" and name=="release":
    (root/"check.started").touch()
    while not (root/"check.gate").exists(): time.sleep(.02)
if (mode=="cancel-check" and name=="release") or (mode=="cancel-download" and name=="package"):
    subprocess.run(["uci","set","hyper.update.auto_update=0"],check=True)
    subprocess.run(["uci","commit","hyper"],check=True)
status="503" if mode=="check-failed" else "200"
data=b"{{}}" if status=="503" else (root/name).read_bytes()
if mode=="bad-checksum" and name=="package": data+=b"corrupt"
Path(config["output"]).write_bytes(data)
print(status,end="")
''')
    replace(manager, f'''#!/usr/bin/python3
import sys,time
from pathlib import Path
root=Path({str(fixture)!r})
if "--print-arch" in sys.argv: print({arch!r}); sys.exit(0)
if "print-architecture" in sys.argv: print("arch all 1\\narch {arch} 10"); sys.exit(0)
with (root/"installs").open("a") as f: f.write(" ".join(sys.argv[1:])+"\\n")
(root/"install.started").touch()
while not (root/"install.gate").exists(): time.sleep(.02)
if (root/"mode").read_text()=="install-failed": sys.exit(1)
Path({str(control)!r}).write_text("Package: luci-app-hyper\\nVersion: 9.9.9-r1\\n")
''')
    replace(Path("/etc/init.d/rpcd"), f"#!/bin/sh\nprintf 'reload\\n' >> '{fixture}/reloads'\n")

    def configure(check, install):
        subprocess.run(["uci", "set", f"hyper.update.auto_check={int(check)}"], check=True)
        subprocess.run(["uci", "set", f"hyper.update.auto_update={int(install)}"], check=True)
        subprocess.run(["uci", "commit", "hyper"], check=True)

    def reset(mode="success", check=True, install=False):
        shutil.rmtree(runtime, ignore_errors=True)
        for name in ["requests", "installs", "install.started", "install.gate", "check.started", "check.gate"]:
            (fixture / name).unlink(missing_ok=True)
        (fixture / "mode").write_text(mode)
        control.parent.mkdir(parents=True, exist_ok=True)
        control.write_text("Package: luci-app-hyper\nVersion: 0.1.0-r1\n")
        configure(check, install)

    def count(name):
        file = fixture / name
        return len(file.read_text().splitlines()) if file.exists() else 0

    def run(force=True):
        subprocess.run([worker, *(["force"] if force else [])], check=True, timeout=10)

    def state():
        try:
            return json.loads((runtime / "state.json").read_text())
        except (FileNotFoundError, ValueError):
            return {}

    try:
        reset(check=False, install=False)
        run()
        assert count("requests") == 0
        reset()
        run()
        assert json.loads((runtime / "check.json").read_text())["latest"] == "9.9.9"
        assert count("requests") == 1 and count("installs") == 0
        run(False)
        assert count("requests") == 1
        print("ok   enabled worker loads real rpcd module; check-only and daily throttle work", flush=True)

        reset(mode="slow-check")
        process = subprocess.Popen([worker, "force"])
        until(lambda: (fixture / "check.started").exists(), "scheduler did not start")
        run()
        assert count("requests") == 1
        (fixture / "check.gate").touch()
        assert process.wait(timeout=10) == 0
        print("ok   simultaneous scheduler invocations share one check", flush=True)

        for mode in ["check-failed", "cancel-check", "cancel-download", "bad-checksum"]:
            reset(mode, check=False, install=True)
            run()
            assert count("installs") == 0, mode
        print("ok   failed checks, bad checksums and disabling during check/download never install", flush=True)

        reset(check=False, install=True)
        run()
        until(lambda: (fixture / "install.started").exists(), "detached installer did not take over lock")
        old = state()
        old["time"] = int(time.time()) - 3600
        (runtime / "state.json").write_text(json.dumps(old))
        requests = count("requests")
        assert method("install_update", {"force": True})["error"] == "update_in_progress"
        assert count("requests") == requests and count("installs") == 1
        assert method("status")["update"]["state"]["state"] == "installing"
        assert count("requests") == requests, "local status must not fetch GitHub"
        (fixture / "install.gate").touch()
        until(lambda: state().get("state") == "done", "installer did not finish")
        assert not (runtime / "download").exists() and not (runtime / "install.json").exists()
        assert method("status")["update"]["state"]["state"] == "done"
        assert count("requests") == requests
        assert method("install_update")["error"] == "up_to_date", "lock must release after cleanup"
        print("ok   independent automatic install holds its lock past stale timestamps; local status and cleanup work", flush=True)

        reset(mode="install-failed", check=False, install=False)
        (fixture / "install.gate").touch()
        assert method("install_update")["started"]
        until(lambda: state().get("state") == "failed", "package failure was not published")
        assert count("installs") == 1 and not (runtime / "download").exists()
        print("ok   manual install remains independent of switches; package failure publishes terminal state", flush=True)

        reset(check=False, install=False)
        runtime.mkdir(parents=True)
        download = runtime / "download"
        download.mkdir()
        staged = download / package
        staged.write_bytes(content)
        (runtime / "install.json").write_text(json.dumps({"manager": "apk" if apk else "opkg", "files": [str(staged)], "version": "9.9.9", "automatic": True}))
        reloads = count("reloads")
        subprocess.run([worker, "install"], check=True, timeout=10)
        assert state().get("error") == "automatic_update_disabled" and count("installs") == 0
        assert not download.exists()
        assert count("reloads") == reloads, "cancelled installation must not reload rpcd"
        print("ok   disabling automatic updates before lock handoff cancels installation", flush=True)
    finally:
        (fixture / "install.gate").touch()
        (fixture / "check.gate").touch()
        time.sleep(0.2)
        for path, backup in backups.items():
            path.unlink(missing_ok=True)
            if backup:
                shutil.copy2(backup, path, follow_symlinks=False)
        shutil.rmtree(runtime, ignore_errors=True)
        if (fixture / "runtime-backup").exists():
            shutil.copytree(fixture / "runtime-backup", runtime)
