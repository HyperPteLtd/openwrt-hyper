"""Start the pinned core with locally served rules, without deprecation overrides.

Keep the generated rule download clients and outbound protocols. Replace external
URLs with loopback fixtures and use direct-out in the selector so this test needs
no subscription or Internet access. Syntax-only `check` misses startup failures.
"""
import copy
import http.server
import json
import os
import pathlib
import re
import socket
import socketserver
import struct
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

config_path = pathlib.Path(sys.argv[1])
config = json.loads(config_path.read_text())
requests = []
dns_queries = []

class DNS(socketserver.BaseRequestHandler):
    def handle(self):
        query, sock = self.request
        pos, labels = 12, []
        while query[pos]:
            size = query[pos]
            labels.append(query[pos + 1:pos + 1 + size].decode())
            pos += size + 1
        name = ".".join(labels)
        dns_queries.append((self.server.kind, name))
        address = "223.5.5.5" if self.server.kind == "local" and name != "foreign-answer.test" else "1.0.0.1"
        question = query[12:pos + 5]
        answer = b"\xc0\x0c" + struct.pack("!HHIH", 1, 1, 60, 4) + socket.inet_aton(address)
        response = query[:2] + struct.pack("!HHHHH", 0x8180, 1, 1, 0, 0) + question + answer
        sock.sendto(response, self.client_address)

dns_servers = []
for kind in ("local", "remote"):
    dns = socketserver.ThreadingUDPServer(("127.0.0.1", 0), DNS)
    dns.kind = kind
    threading.Thread(target=dns.serve_forever, daemon=True).start()
    dns_servers.append(dns)

class Rules(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        requests.append(self.path)
        rules = {"/geoip-cn.json": {"ip_cidr": ["223.5.5.0/24"]},
                 "/geosite-cn.json": {"domain": ["cn.test"]},
                 "/geosite-noncn.json": {"domain": ["overseas.test"]}}
        body = json.dumps({"version": 3, "rules": [rules[self.path]]}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Rules)
threading.Thread(target=server.serve_forever, daemon=True).start()
with tempfile.TemporaryDirectory() as root:
    root = pathlib.Path(root)
    # Keep DNS rule behavior but replace privileged transparent listeners and
    # external DNS transports with loopback fixtures.
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        dns_port = sock.getsockname()[1]
    config["inbounds"] = [{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dns_port}]
    config["ntp"] = {"enabled": False}
    config["dns"]["servers"] = [
        {"tag": tag, "type": "udp", "server": "127.0.0.1", "server_port": dns_servers[index].server_address[1],
         **({"detour": "main-out"} if index else {})}
        for tag, index in [("default-dns", 0), ("system-dns", 0), ("china-dns", 0), ("main-dns", 1)]
    ]
    for outbound in config["outbounds"]:
        # The hosted CI runner is unprivileged. Router tests run as root with
        # NET_ADMIN and keep marks; this loopback smoke needs no policy routing.
        if sys.platform != "linux" or os.geteuid() != 0:
            outbound.pop("routing_mark", None)
        if outbound["tag"] == "main-out":
            outbound["outbounds"].append("direct-out")
            outbound["default"] = "direct-out"
    if sys.platform != "linux":
        # Without Linux's routing_mark, the direct outbound is intentionally empty.
        for dns in config["dns"]["servers"]:
            if dns.get("detour") == "direct-out":
                dns.pop("detour")
    remote = [r for r in config["route"]["rule_set"] if r["type"] == "remote"]
    assert remote, "fixture must exercise remote rule downloads"
    assert all(r.get("http_client", {}).get("detour") == "main-out" for r in remote)
    assert all("download_detour" not in r for r in config["route"]["rule_set"])
    bootstrap = {r["tag"]: r.pop("initial_path") for r in remote}
    assert all(pathlib.Path(path).read_bytes()[:3] == b"SRS" for path in bootstrap.values())
    for rule in remote:
        rule.update(format="source", url=f"http://127.0.0.1:{server.server_port}/{rule['tag']}.json")
    # Reserve a unique port for the local management API.
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config["experimental"]["clash_api"] = {"external_controller": f"127.0.0.1:{port}", "secret": "test-only"}
    config["experimental"]["cache_file"]["path"] = str(root / "cache.db")
    config["log"] = {"level": "info", "timestamp": False}
    env = {k: v for k, v in os.environ.items() if not k.startswith("ENABLE_DEPRECATED_")}

    def launch(data, filename):
        path = root / filename
        path.write_text(json.dumps(data))
        log = open(root / (filename + ".log"), "w+")
        process = subprocess.Popen(["sing-box", "run", "-c", str(path)], env=env, stdout=log, stderr=log)
        return process, log

    legacy = copy.deepcopy(config)
    legacy_rule = next(r for r in legacy["route"]["rule_set"] if r["type"] == "remote")
    legacy_rule["download_detour"] = legacy_rule.pop("http_client")["detour"]
    version = subprocess.check_output(["sing-box", "version", "-n"], text=True)
    # 1.14 warns; the shipped 1.15 core refuses the old field at startup.
    if tuple(map(int, re.match(r"(\d+)\.(\d+)", version).groups())) >= (1, 15):
        process, log = launch(legacy, "legacy.json")
        try:
            assert process.wait(timeout=10) != 0, "legacy startup unexpectedly succeeded"
            log.seek(0)
            output = log.read()
            assert "download_detour" in output, output
            print("ok   core startup: reported legacy download_detour failure reproduced")
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            log.close()

    process, log = launch(config, "modern.json")
    try:
        deadline = time.monotonic() + 15
        ready = False
        while process.poll() is None and time.monotonic() < deadline:
            try:
                req = urllib.request.Request(f"http://127.0.0.1:{port}/version", headers={"Authorization": "Bearer test-only"})
                with urllib.request.urlopen(req, timeout=1) as reply:
                    ready = reply.status == 200
                if ready:
                    break
            except OSError:
                time.sleep(0.1)
        log.flush()
        log.seek(0)
        output = log.read()
        assert ready, output
        time.sleep(1)
        assert process.poll() is None, output
        assert len(set(requests)) == len(remote), (requests, output)
        assert "deprecated" not in output.lower(), output
        print("ok   core startup: modern rules downloaded and API remains healthy")
        for name, expected in [("cn.test", ["local"]), ("overseas.test", ["remote"]),
                               ("cn-answer.test", ["local"]), ("foreign-answer.test", ["local", "remote"])]:
            question = b"".join(bytes([len(part)]) + part.encode() for part in name.split(".")) + b"\x00\x00\x01\x00\x01"
            query = struct.pack("!HHHHHH", 17, 0x0100, 1, 0, 0, 0) + question
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
                client.settimeout(3)
                client.sendto(query, ("127.0.0.1", dns_port))
                reply, _ = client.recvfrom(4096)
            assert reply[3] & 15 == 0 and struct.unpack("!H", reply[6:8])[0] == 1, (name, reply)
            assert [kind for kind, queried in dns_queries if queried == name] == expected, (name, dns_queries)
        print("ok   DNS: local/overseas rules and evaluated CN/non-CN answers preserve routing")
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        log.close()

    # Empty cache + unreachable updates must still start using the packaged files.
    offline = copy.deepcopy(config)
    offline["experimental"]["cache_file"]["path"] = str(root / "offline-cache.db")
    for rule in offline["route"]["rule_set"]:
        if rule["type"] == "remote":
            rule.update(format="binary", initial_path=bootstrap[rule["tag"]], url="http://127.0.0.1:1/unreachable.srs")
    process, log = launch(offline, "offline.json")
    try:
        deadline = time.monotonic() + 10
        ready = False
        while process.poll() is None and time.monotonic() < deadline:
            try:
                req = urllib.request.Request(f"http://127.0.0.1:{port}/version", headers={"Authorization": "Bearer test-only"})
                with urllib.request.urlopen(req, timeout=1) as reply:
                    ready = reply.status == 200
                if ready:
                    break
            except OSError:
                time.sleep(0.1)
        log.seek(0)
        output = log.read()
        assert ready, output
        time.sleep(1)
        assert process.poll() is None, output
        assert "deprecated" not in output.lower(), output
        print("ok   core startup: empty cache and unreachable updates use bundled rules")
    finally:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        log.close()
server.shutdown()
for dns in dns_servers:
    dns.shutdown()
    dns.server_close()
