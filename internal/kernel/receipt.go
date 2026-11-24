package kernel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Only the installer writes receipts under its protected persistent metadata
// directory. An upload or mirror is never consulted for a receipt or checksum.
type receipt struct {
	Schema       int                `json:"schema_version"`
	Repository   string             `json:"repository"`
	Tag          string             `json:"tag"`
	Target       string             `json:"target"`
	Asset        Asset              `json:"asset"`
	Capabilities TargetCapabilities `json:"target_capabilities"`
}

func (r receipt) verified(tag, target string) (*Verified, error) {
	plat, ok := platforms[target]
	a := r.Asset
	// The 1.0.0 package installed the core it carried under no repository, named by its version.
	if !ok || r.Schema != 1 || r.Repository != Repository && r.Repository != "" || r.Tag != tag || r.Target != target || (a.Name != assetName(tag, target) && !rawBuild(a.Name, tag, target)) || a.Arch != plat.arch || a.FloatABI != plat.float || a.Endian != plat.endian {
		return nil, errors.New("invalid installed release identity")
	}
	seen, err := checkMembers(a)
	if err != nil {
		return nil, err
	}
	dir := strings.TrimSuffix(assetName(tag, target), ".tar.gz") + "/"
	if seen[dir+"mihomo"] != "core" || strings.HasSuffix(a.Name, ".tar.gz") && seen[dir+"capabilities.json"] != "capabilities" {
		return nil, errors.New("release lacks the matching Mihomo core or capability record")
	}
	tc := r.Capabilities
	if tc.Control != Controller || tc.PeakRAM < 0 || tc.PeakRAM > 16<<30 {
		return nil, errors.New("invalid Mihomo capabilities")
	}
	if tc.PeakRAM == 0 {
		tc.PeakRAM = 96 * MiB
	}
	c := Capabilities{Schema: 1, Repository: Repository, Tag: tag, Build: a.SHA256, Targets: map[string]TargetCapabilities{target: tc}}
	m := Manifest{Schema: 1, Repository: Repository, Tag: tag, Build: a.SHA256, Assets: map[string]Asset{target: a}}
	return &Verified{m, c, a, tc}, nil
}

func (m *Manager) storedMetadata(tag string) (material, *Verified, error) {
	dir := filepath.Join(m.Root, "meta", tag)
	if _, err := os.Stat(dir); err != nil {
		return material{}, nil, err
	}
	b, err := readSmall(filepath.Join(dir, "receipt.json"), maxDocument)
	if os.IsNotExist(err) {
		return (Source{Bundle: dir}).metadata(context.Background(), tag, m.Target, m.Key)
	}
	if err != nil {
		return material{}, nil, err
	}
	var r receipt
	if json.Unmarshal(b, &r) != nil {
		return material{}, nil, errors.New("corrupt installed release receipt")
	}
	v, err := r.verified(tag, m.Target)
	return material{receipt: b}, v, err
}

func archiveKind(name, directory string) string {
	if path.Dir(name) != directory {
		return ""
	}
	base := path.Base(name)
	switch {
	case base == "mihomo":
		return "core"
	case base == "capabilities.json":
		return "capabilities"
	case base == "NOTICE" || base == "LICENSE" || base == "COPYING" || base == "README.md" || strings.HasPrefix(base, "LICENSE.") || strings.HasPrefix(base, "NOTICE."):
		return "notice"
	}
	return ""
}

// inspectArchive runs only after the compressed bytes matched the upstream
// SHA-256. Index file lengths and hashes without extracting to the filesystem.
func inspectArchive(ctx context.Context, file, tag, target string, p pin) (material, *Verified, error) {
	f, err := openRegular(file)
	if err != nil {
		return material{}, nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return material{}, nil, err
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: maxAsset + 2*MiB}

	plat := platforms[target]
	a := Asset{Name: p.Name, Arch: plat.arch, Endian: plat.endian, FloatABI: plat.float, Size: p.Size, SHA256: p.Hash}
	if rawBuild(p.Name, tag, target) {
		hash := sha256.New()
		size, e := io.Copy(hash, io.LimitReader(limited, maxAsset+1))
		if e != nil || size == 0 || size > maxAsset {
			return material{}, nil, errors.New("invalid raw Mihomo binary")
		}
		a.Expanded = size
		a.Files = []Member{{Path: strings.TrimSuffix(assetName(tag, target), ".tar.gz") + "/mihomo", Kind: "core", Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}}
		// The installer verifies native TUN and TProxy configurations before committing this receipt.
		// No Cronet capability is advertised from an unsigned capability claim.
		return newReceipt(tag, target, a, TargetCapabilities{Control: Controller, TUN: true, TProxy: true})
	}
	tr := tar.NewReader(limited)
	var caps TargetCapabilities
	directory := strings.TrimSuffix(a.Name, ".tar.gz")
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return material{}, nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return material{}, nil, err
		}
		name := h.Name
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if entries >= 128 || !safeName(name) {
			return material{}, nil, errors.New("invalid release archive path or member count")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		kind := archiveKind(name, directory)
		if h.Typeflag != tar.TypeReg || kind == "" || len(a.Files) >= 64 || h.Size <= 0 || h.Size > maxAsset-a.Expanded {
			return material{}, nil, errors.New("unexpected or oversized release archive member")
		}
		hash := sha256.New()
		var reader io.Reader = tr
		if kind == "capabilities" {
			if h.Size > maxDocument {
				return material{}, nil, errors.New("oversized capability record")
			}
			b, e := io.ReadAll(tr)
			if e != nil || json.Unmarshal(b, &caps) != nil {
				return material{}, nil, errors.New("invalid capability record")
			}
			reader = bytes.NewReader(b)
		}
		n, err := io.Copy(hash, reader)
		if err != nil || n != h.Size {
			return material{}, nil, errors.New("incomplete release archive member")
		}
		a.Expanded += n
		a.Files = append(a.Files, Member{name, kind, n, hex.EncodeToString(hash.Sum(nil))})
	}
	if limited.N == 0 {
		return material{}, nil, errors.New("release archive exceeds expanded size limit")
	}
	return newReceipt(tag, target, a, caps)
}
func newReceipt(tag, target string, a Asset, caps TargetCapabilities) (material, *Verified, error) {
	r := receipt{1, Repository, tag, target, a, caps}
	v, err := r.verified(tag, target)
	if err != nil {
		return material{}, nil, err
	}
	b, err := json.Marshal(r)
	return material{receipt: b}, v, err
}

type candidate struct {
	material material
	verified *Verified
	source   Source
	scratch  string
}

func (c candidate) close() {
	if c.scratch != "" {
		_ = os.RemoveAll(c.scratch)
	}
}

func (m *Manager) prepare(ctx context.Context, tag string, src Source, releasePin *pin) (candidate, error) {
	c := candidate{source: src}
	var err error
	// Legacy signed offline bundles still authenticate with the package's
	// trust root. Invalid signatures on that path are fatal.
	if src.Bundle != "" && m.Key != "" {
		if _, e := os.Lstat(filepath.Join(src.Bundle, "manifest.json")); e == nil {
			c.material, c.verified, err = src.metadata(ctx, tag, m.Target, m.Key)
			return c, err
		} else if !os.IsNotExist(e) {
			return c, e
		}
	}
	c.material, c.verified, err = m.storedMetadata(tag)
	if !os.IsNotExist(err) {
		return c, err
	}

	var p pin
	if releasePin != nil {
		p = *releasePin
	} else {
		p, err = originalPin(ctx, tag, m.Target)
		if err != nil {
			return c, err
		}
	}
	name := p.Name
	if src.Bundle != "" {
		f, err := openRegular(filepath.Join(src.Bundle, name))
		if err != nil {
			return c, err
		}
		err = copyChecked(io.Discard, f, p.Size, p.Hash)
		f.Close()
		if err != nil {
			return c, err
		}
	} else {
		space, err := m.Capacity(m.Root, m.Temp)
		if err != nil {
			return c, err
		}
		if space.Temporary.Free < p.Size+2*MiB || space.Temporary.InMemory && space.Available < p.Size+48*MiB {
			return c, errors.New("insufficient temporary space for release archive")
		}
		c.scratch, err = os.MkdirTemp(m.Temp, ".archive-")
		if err != nil {
			return c, err
		}
		c.source = Source{Bundle: c.scratch, Cache: src.Cache}
		if err = downloadArchive(ctx, src, tag, name, filepath.Join(c.scratch, name), p); err != nil {
			return c, err
		}
	}
	c.material, c.verified, err = inspectArchive(ctx, filepath.Join(c.source.Bundle, name), tag, m.Target, p)
	return c, err
}

func downloadArchive(ctx context.Context, src Source, tag, name, dst string, p pin) error {
	r, err := src.open(ctx, tag, name, true)
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = copyChecked(f, r, p.Size, p.Hash)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (m material) same(other material) bool {
	if m.receipt != nil && other.receipt != nil {
		return Digest(m.receipt) == Digest(other.receipt)
	}
	return m.receipt == nil && other.receipt == nil && Digest(m.manifest) == Digest(other.manifest)
}
