//go:build ignore

// Sign only these isolated fixtures. The private key never leaves this process.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aead.dev/minisign"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	root, archive, bad := os.Args[1], os.Args[2], os.Args[3]
	pub, priv, e := minisign.GenerateKey(rand.Reader)
	must(e)
	for _, tag := range []string{"v1.19.34-0.20261117082241-15fb245e9278", "v0.0.0-qa"} {
		dir := filepath.Join(root, tag)
		must(os.MkdirAll(dir, 0755))
		var raw []byte
		if tag == "v1.19.34-0.20261117082241-15fb245e9278" {
			raw, e = os.ReadFile(archive)
			must(e)
			if kernel.Digest(raw) != os.Args[4] {
				panic("unexpected release asset digest")
			}
		} else {
			bin, e := os.ReadFile(bad)
			must(e)
			var data bytes.Buffer
			z := gzip.NewWriter(&data)
			t := tar.NewWriter(z)
			must(t.WriteHeader(&tar.Header{Name: "mihomo-" + strings.TrimPrefix(tag, "v") + "-linux-arm64/mihomo", Mode: 0755, Size: int64(len(bin))}))
			_, e = t.Write(bin)
			must(e)
			must(t.Close())
			must(z.Close())
			raw = data.Bytes()
		}
		a := kernel.Asset{Name: "mihomo-" + strings.TrimPrefix(tag, "v") + "-linux-arm64.tar.gz", Arch: "arm64", Endian: "little", FloatABI: "native", SHA256: kernel.Digest(raw), Size: int64(len(raw))}

		z, e := gzip.NewReader(bytes.NewReader(raw))
		must(e)
		t := tar.NewReader(z)
		for {
			h, e := t.Next()
			if e == io.EOF {
				break
			}
			must(e)
			if h.Typeflag == tar.TypeDir {
				continue
			}
			d := sha256.New()
			n, e := io.Copy(d, t)
			must(e)
			kind := "notice"
			if filepath.Base(h.Name) == "mihomo" {
				kind = "core"
			}
			if filepath.Base(h.Name) == "capabilities.json" {
				kind = "capabilities"
			}
			a.Expanded += n
			a.Files = append(a.Files, kernel.Member{Path: h.Name, Kind: kind, Size: n, SHA256: hex.EncodeToString(d.Sum(nil))})
		}
		must(z.Close())
		caps := kernel.Capabilities{Schema: 1, Repository: kernel.Repository, Tag: tag, Build: "isolated-vm-fixture", Targets: map[string]kernel.TargetCapabilities{"arm64": {Control: kernel.Controller, TUN: true, TProxy: true, PeakRAM: 32 * kernel.MiB}}}
		c, e := json.Marshal(caps)
		must(e)
		m := kernel.Manifest{Schema: 1, Repository: kernel.Repository, Tag: tag, Build: caps.Build, CapabilitiesSHA: kernel.Digest(c), Assets: map[string]kernel.Asset{"arm64": a}}
		b, e := json.Marshal(m)
		must(e)
		for name, data := range map[string][]byte{"manifest.json": b, "manifest.json.minisig": minisign.Sign(priv, b), "capabilities.json": c, a.Name: raw} {
			must(os.WriteFile(filepath.Join(dir, name), data, 0644))
		}
	}
	fmt.Println(pub.String())
}
