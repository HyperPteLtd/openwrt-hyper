#!/usr/bin/env python3
"""Verify shipped bootstrap rules; --fetch restores files at the manifest pins."""
import argparse
import hashlib
import json
import pathlib
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--fetch", action="store_true")
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parents[1] / "luci-app-hyper/root/etc/hyper/rule-set"
manifest = json.loads((root / "manifest.json").read_text())
for name, item in manifest["files"].items():
    path = root / name
    if args.fetch:
        with urllib.request.urlopen(item["source"], timeout=30) as response:
            data = response.read()
    else:
        data = path.read_bytes()
    assert data[:3] == b"SRS", f"Invalid SRS header: {name}"
    assert len(data) == item["bytes"], f"Size mismatch: {name}"
    assert hashlib.sha256(data).hexdigest() == item["sha256"], f"Checksum mismatch: {name}"
    if args.fetch:
        temp = path.with_suffix(".tmp")
        temp.write_bytes(data)
        temp.replace(path)
    print(f"ok   bundled rule set: {name}")
