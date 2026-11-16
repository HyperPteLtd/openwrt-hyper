"""Offline staging regression: real shell/archive handling, stubbed Go/Docker/network."""
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
LOCK = (ROOT / "scripts/core.lock").read_text()
COMMIT = next(line.split("=", 1)[1] for line in LOCK.splitlines() if line.startswith("LIBBOX_COMMIT="))
ARCHIVE_NAME = f"libbox-{COMMIT}.tar.gz"
SOURCE_ROOT = f"libbox-{COMMIT}"
TAGS = "with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0"
GO_STUB = '''import json, os, pathlib, sys
args = sys.argv[1:]
keys = ("CGO_ENABLED", "GOOS", "GOARCH", "GOARM", "GOMIPS", "GO386", "GOTOOLCHAIN", "GOWORK", "GOFLAGS", "GOEXPERIMENT", "GOMODCACHE", "GOCACHE")
with open(os.environ["GO_LOG"], "a") as log:
    log.write(json.dumps({"args": args, "env": {key: os.environ.get(key) for key in keys}}) + "\\n")
output = pathlib.Path(args[args.index("-o") + 1])
if os.environ.get("BUILD_FAIL") == output.name:
    sys.exit(19)
output.write_text("built " + output.name)
'''


class StageBinariesTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="hyper-stage-test-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.repo = self.base / "repo with spaces"
        self.bin = self.base / "tools"
        self.cache = self.base / "cache"
        self.out = self.base / "output"
        self.scratch = self.base / "scratch"
        for path in (self.repo / "scripts", self.repo / "hyper-crypt", self.bin, self.cache, self.out, self.scratch):
            path.mkdir(parents=True)
        shutil.copy2(ROOT / "scripts/stage-binaries.sh", self.repo / "scripts/stage-binaries.sh")
        (self.repo / "hyper-crypt/go.mod").write_text("module test/hyper-crypt\n\ngo 1.25\n")
        # A closed PATH ensures no real compiler, network client or Docker runs.
        for name in ("sh", "dirname", "mkdir", "mktemp", "rm", "cut", "mv", "sed", "cp", "chmod", "ls", "tar", "cat"):
            (self.bin / name).symlink_to(shutil.which(name))
        checksum = "sha256sum" if shutil.which("sha256sum") else "shasum"
        (self.bin / checksum).symlink_to(shutil.which(checksum))
        (self.bin / "python3").symlink_to(sys.executable)
        self.env = {**os.environ, "PATH": str(self.bin), "OUT_DIR": str(self.out), "CACHE_DIR": str(self.cache),
                    "TMPDIR": str(self.scratch), "GO_LOG": str(self.base / "go.jsonl"),
                    "DOCKER_LOG": str(self.base / "docker.json"), "CURL_LOG": str(self.base / "curl.json")}
        for key in ("GOMODCACHE", "GOCACHE", "GO_IMAGE", "BUILD_FAIL"):
            self.env.pop(key, None)
        self.tool("go", GO_STUB)
        self.tool("curl", '''import json, os, shutil, sys
args = sys.argv[1:]
open(os.environ["CURL_LOG"], "w").write(json.dumps(args))
shutil.copyfile(os.environ["DOWNLOAD_ARCHIVE"], args[args.index("-o") + 1])
''')
        self.source()

    def tool(self, name, body):
        path = self.bin / name
        path.write_text(f"#!{sys.executable}\n{body}")
        path.chmod(0o755)

    def source(self, sdk="0.1.3", core="v1.15.0-alpha.13", extra=None):
        archive = self.cache / ARCHIVE_NAME
        with tarfile.open(archive, "w:gz") as bundle:
            files = {
                f"{SOURCE_ROOT}/internal/singbox/runtime.go": f'const sdkVersion = "{sdk}"\nconst pinnedVersion = "{core}"\n',
                # Execution of upstream build scripts would fail this test.
                f"{SOURCE_ROOT}/scripts/build-linux-cli.sh": "#!/bin/sh\nexit 99\n",
            }
            for name, content in files.items():
                member = tarfile.TarInfo(name)
                data = content.encode()
                member.size = len(data)
                bundle.addfile(member, io.BytesIO(data))
            if extra:
                bundle.addfile(extra, io.BytesIO(b"x") if extra.isfile() else None)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        lock = "\n".join(f"LIBBOX_SOURCE_SHA256={digest}" if line.startswith("LIBBOX_SOURCE_SHA256=") else line
                         for line in LOCK.splitlines()) + "\n"
        (self.repo / "scripts/core.lock").write_text(lock)

    def run_stage(self, arch="x86_64", ok=True):
        result = subprocess.run(["/bin/sh", str(self.repo / "scripts/stage-binaries.sh"), arch],
                                env=self.env, cwd=self.repo, capture_output=True, text=True)
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((self.out / "sing-box").read_text(), "built sing-box")
            self.assertEqual((self.out / "hyper-crypt").read_text(), "built hyper-crypt")
            self.assertEqual((self.out / "sing-box").stat().st_mode & 0o777, 0o755)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(list(self.scratch.iterdir()), [], "staging directories must be cleaned on success/failure")
        return result

    def builds(self):
        path = self.base / "go.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def assert_build_flags(self, builds):
        self.assertEqual(len(builds), 2)
        for build in builds:
            args, env = build["args"], build["env"]
            self.assertEqual(args[0], "build")
            for flag in ("-mod=readonly", "-trimpath", "-buildvcs=false"):
                self.assertIn(flag, args)
            self.assertIn("-s -w -buildid=", args[args.index("-ldflags") + 1])
            self.assertEqual(env["CGO_ENABLED"], "0")
            self.assertEqual(env["GOOS"], "linux")
            self.assertEqual(env["GOTOOLCHAIN"], "go1.25.5")
            self.assertEqual(env["GOWORK"], "off")
            self.assertEqual(env["GOFLAGS"], "")
        core = builds[0]["args"]
        self.assertEqual(core[-1], "./cmd/sing-box")
        self.assertEqual(core[core.index("-tags") + 1], TAGS)
        linker = core[core.index("-ldflags") + 1]
        for flag in ("-checklinkname=0", "github.com/sagernet/sing-box/constant.Version=1.15.0-alpha.13",
                     f"main.libboxRevision={COMMIT}", f"internal/singbox.buildTags={TAGS}",
                     "runtime.godebugDefault=multipathtcp=0,tlssha1=1"):
            self.assertIn(flag, linker)

    def test_architectures_and_shared_static_flags(self):
        cases = [
            ("x86_64", "amd64", "", "", ""),
            ("aarch64_cortex-a53", "arm64", "", "", ""),
            ("arm_arm1176jzf-s_vfp", "arm", "6", "", ""),
            ("arm_arm926ej-s", "arm", "5", "", ""),
            ("mipsel_24kc", "mipsle", "", "softfloat", ""),
            ("mips_24kc", "mips", "", "softfloat", ""),
            ("i386_pentium4", "386", "", "", "softfloat"),
            ("riscv64_generic", "riscv64", "", "", ""),
            ("loongarch64_generic", "loong64", "", "", ""),
        ]
        cases.extend((f"arm_{arch}", "arm", "7", "", "") for arch in (
            "cortex-a5_vfpv4", "cortex-a7_vfpv4", "cortex-a7_neon-vfpv4", "cortex-a8_vfpv3",
            "cortex-a9_neon", "cortex-a9_vfpv3-d16", "cortex-a15_neon-vfpv4"))
        for arch, *expected in cases:
            with self.subTest(arch=arch):
                self.run_stage(arch)
                builds = self.builds()[-2:]
                self.assert_build_flags(builds)
                for build in builds:
                    self.assertEqual([build["env"][key] for key in ("GOARCH", "GOARM", "GOMIPS", "GO386")], expected)
                    self.assertIsNone(build["env"]["GOMODCACHE"])
                    self.assertIsNone(build["env"]["GOCACHE"])

    def test_cache_overrides_and_toolchain_cannot_drift(self):
        self.env.update(GOMODCACHE=str(self.base / "shared-mod"), GOCACHE=str(self.base / "shared-build"),
                        GOTOOLCHAIN="auto", GOFLAGS="-tags=with_gvisor", GOARM="6", GOMIPS="hardfloat", GO386="sse2")
        self.run_stage()
        self.assert_build_flags(self.builds())
        for build in self.builds():
            self.assertEqual(build["env"]["GOMODCACHE"], self.env["GOMODCACHE"])
            self.assertEqual(build["env"]["GOCACHE"], self.env["GOCACHE"])
            self.assertEqual([build["env"][k] for k in ("GOARM", "GOMIPS", "GO386")], ["", "", ""])

    def test_verified_download_uses_commit_url(self):
        download = self.base / "download.tar.gz"
        (self.cache / ARCHIVE_NAME).rename(download)
        self.env["DOWNLOAD_ARCHIVE"] = str(download)
        self.run_stage()
        args = json.loads((self.base / "curl.json").read_text())
        self.assertEqual(args[-1], f"https://codeload.github.com/HyperPteLtd/libbox/tar.gz/{COMMIT}")
        self.assertEqual((self.cache / ARCHIVE_NAME).read_bytes(), download.read_bytes())

    def test_relative_output_and_cache_paths(self):
        self.env.update(OUT_DIR="../output", CACHE_DIR="../cache")
        self.run_stage()
        self.assertFalse((self.base / "curl.json").exists())

    def test_bad_cached_archive_is_rejected_before_extraction_or_build(self):
        (self.cache / ARCHIVE_NAME).write_bytes(b"untrusted archive")
        result = self.run_stage(ok=False)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertEqual(self.builds(), [])
        self.assertFalse((self.base / "curl.json").exists())

    def test_bad_download_is_not_cached(self):
        (self.cache / ARCHIVE_NAME).unlink()
        download = self.base / "bad-download"
        download.write_bytes(b"untrusted download")
        self.env["DOWNLOAD_ARCHIVE"] = str(download)
        result = self.run_stage(ok=False)
        self.assertIn("checksum mismatch", result.stderr)
        self.assertFalse((self.cache / ARCHIVE_NAME).exists())
        self.assertEqual(self.builds(), [])

    def test_unsafe_paths_and_links_rejected_even_with_matching_checksum(self):
        for name, kind in ((f"{SOURCE_ROOT}/../escape", tarfile.REGTYPE), ("/escape", tarfile.REGTYPE),
                           (f"{SOURCE_ROOT}/link", tarfile.SYMTYPE), (f"{SOURCE_ROOT}/hard", tarfile.LNKTYPE)):
            with self.subTest(name=name):
                member = tarfile.TarInfo(name)
                member.type = kind
                member.size = 1 if kind == tarfile.REGTYPE else 0
                member.linkname = "../../escape" if kind != tarfile.REGTYPE else ""
                self.source(extra=member)
                result = self.run_stage(ok=False)
                self.assertIn("unsafe source archive member", result.stderr)
                self.assertEqual(self.builds(), [])

    def test_source_version_guard(self):
        for sdk, core in (("0.1.4", "v1.15.0-alpha.13"), ("0.1.3", "")):
            with self.subTest(sdk=sdk, core=core):
                self.source(sdk=sdk, core=core)
                result = self.run_stage(ok=False)
                self.assertIn("source version mismatch", result.stderr)
                self.assertEqual(self.builds(), [])

    def test_unsupported_architecture(self):
        result = self.run_stage("unsupported_cpu", ok=False)
        self.assertIn("no libbox build", result.stderr)
        self.assertEqual(self.builds(), [])

    def test_missing_compiler_never_falls_back_to_a_prebuilt_core(self):
        (self.bin / "go").unlink()
        result = self.run_stage(ok=False)
        self.assertIn("need Go or Docker", result.stderr)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertFalse((self.base / "curl.json").exists())

    def test_failed_build_does_not_replace_previous_binaries(self):
        for name in ("sing-box", "hyper-crypt"):
            (self.out / name).write_text("previous build")
        self.env["BUILD_FAIL"] = "hyper-crypt"
        result = self.run_stage(ok=False)
        self.assertEqual(result.returncode, 19)
        self.assertEqual(len(self.builds()), 2)
        for name in ("sing-box", "hyper-crypt"):
            self.assertEqual((self.out / name).read_text(), "previous build")

    def test_docker_fallback_runs_same_build_and_reuses_cache(self):
        (self.bin / "go").unlink()
        fake_go = self.base / "container-tools"
        fake_go.mkdir()
        (fake_go / "go").write_text(f"#!{sys.executable}\n{GO_STUB}")
        (fake_go / "go").chmod(0o755)
        self.env["CONTAINER_GO"] = str(fake_go)
        self.tool("docker", '''import json, os, pathlib, subprocess, sys, tarfile, tempfile
args = sys.argv[1:]
open(os.environ["DOCKER_LOG"], "w").write(json.dumps(args))
env = dict(os.environ)
for i, arg in enumerate(args):
    if arg == "-e" and "=" in args[i + 1]:
        key, value = args[i + 1].split("=", 1)
        env[key] = value
env["PATH"] = env["CONTAINER_GO"] + os.pathsep + env["PATH"]
with tempfile.TemporaryDirectory() as temp:
    with tarfile.open(fileobj=sys.stdin.buffer, mode="r|*") as archive:
        archive.extractall(temp)  # Only this test's generated Docker context.
    result = subprocess.run(["/bin/sh", str(pathlib.Path(temp) / "build.sh")], env=env, stdout=sys.stderr)
    if result.returncode:
        sys.exit(result.returncode)
    with tarfile.open(fileobj=sys.stdout.buffer, mode="w|") as archive:
        for name in ("sing-box", "hyper-crypt"):
            archive.add(pathlib.Path(temp) / "bin" / name, arcname=name)
''')
        self.run_stage("mipsel_24kc")
        self.assert_build_flags(self.builds())
        args = json.loads((self.base / "docker.json").read_text())
        self.assertIn("golang:1.25.5-bookworm", args)
        self.assertIn("hyper-router-go-cache:/cache", args)
        self.assertIn("-e", args)
        for build in self.builds():
            self.assertEqual(build["env"]["GOMODCACHE"], "/cache/mod")
            self.assertEqual(build["env"]["GOCACHE"], "/cache/build")
            self.assertEqual(build["env"]["GOMIPS"], "softfloat")


if __name__ == "__main__":
    unittest.main(verbosity=2)
