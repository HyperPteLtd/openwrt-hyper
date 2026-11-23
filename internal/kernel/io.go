package kernel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/mirror"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

type Source struct {
	Mirror string `json:"mirror,omitempty"`
	URL    string `json:"url,omitempty"`
	Bundle string `json:"-"`
	Cache  string `json:"cache,omitempty"`
}
type material struct{ manifest, sig, caps, receipt []byte }

var origin = "https://github.com/" + Repository + "/releases/download"

func trustedTransportURL(x string) (*url.URL, error) {
	u, e := url.Parse(x)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("an HTTPS source without embedded account credentials is required")
	}
	return u, nil
}

var httpClient = &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DisableCompression: true}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) > 6 {
		return errors.New("redirect limit")
	}
	if _, e := trustedTransportURL(req.URL.String()); e != nil {
		return e
	}
	req.Header = make(http.Header)
	return nil
}}

var ErrTemporaryDownload = errors.New("release source temporarily unreachable")
var ErrReleaseRateLimit = errors.New("GitHub release API rate limit reached")

func temporaryTransport(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	var timeout net.Error
	return errors.As(err, &dns) || errors.As(err, &op) || errors.As(err, &timeout) && timeout.Timeout() ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

type releaseBody struct{ io.ReadCloser }

func (b releaseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if temporaryTransport(err) {
		return n, ErrTemporaryDownload
	}
	return n, err
}

var errReleaseNotFound = errors.New("release file not found")

func fetch(ctx context.Context, address string) (io.ReadCloser, error) {
	u, e := trustedTransportURL(address)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, errors.New("invalid release request")
	}
	res, e := httpClient.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if failure := tlsdiag.Classify(e, time.Now()); failure != nil {
			return nil, failure
		}
		if temporaryTransport(e) {
			return nil, ErrTemporaryDownload
		}
		return nil, errors.New("release source unreachable")
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		if res.StatusCode == 404 {
			return nil, errReleaseNotFound
		}
		// Identify the responding host after redirects. A mirror cannot claim a
		// GitHub quota error on an unrelated download.
		if res.Request != nil && res.Request.URL.Hostname() == "api.github.com" &&
			(res.StatusCode == 403 || res.StatusCode == 429) && res.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, ErrReleaseRateLimit
		}
		if res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500 {
			return nil, ErrTemporaryDownload
		}
		return nil, fmt.Errorf("release source returned HTTP %d", res.StatusCode)
	}
	return releaseBody{res.Body}, nil
}
func (s Source) validate() error {
	n := 0
	for _, v := range []string{s.Mirror, s.URL, s.Bundle} {
		if v != "" {
			n++
		}
	}
	if n > 1 {
		return errors.New("choose only one download source")
	}
	for _, v := range []string{s.Mirror, s.URL} {
		if v != "" {
			if _, e := trustedTransportURL(v); e != nil {
				return e
			}
		}
	}
	for _, v := range []string{s.Bundle, s.Cache} {
		if v != "" && (!filepath.IsAbs(v) || filepath.Clean(v) != v) {
			return errors.New("use an absolute local directory")
		}
	}
	return nil
}
func (s Source) open(ctx context.Context, tag, name string, isArchive bool) (io.ReadCloser, error) {
	if !identifier.MatchString(name) || !TagPattern.MatchString(tag) {
		return nil, errors.New("invalid release file name")
	}
	if s.Bundle != "" {
		return openRegular(filepath.Join(s.Bundle, name))
	}
	if s.URL != "" {
		u, e := trustedTransportURL(s.URL)
		if e != nil {
			return nil, e
		}
		if isArchive {
			return fetch(ctx, s.URL)
		}
		return fetch(ctx, u.ResolveReference(&url.URL{Path: name}).String())
	}
	base := origin
	if s.Mirror != "" {
		base = s.Mirror
	}
	u, e := trustedTransportURL(base)
	if e != nil {
		return nil, e
	}
	target := u.JoinPath(releaseRef(tag), name).String()
	if s.Mirror != "" {
		return fetch(ctx, target)
	}
	// GitHub first (or whichever address worked last), then the accelerators. The caller
	// checks size and SHA-256 from the release metadata, so any copy that arrives is safe.
	var first error
	for _, c := range mirror.Candidates(target, mirror.Configured(ctx)) {
		r, err := fetch(ctx, c.URL)
		if err == nil {
			mirror.Remember(c.Prefix)
			return r, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}
func getBounded(r io.ReadCloser, e error, limit int64) ([]byte, error) {
	if e != nil {
		return nil, e
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("release file exceeds size limit")
	}
	return b, e
}
func readSmall(file string, limit int64) ([]byte, error) {
	r, e := openRegular(file)
	return getBounded(r, e, limit)
}
func openRegular(name string) (*os.File, error) {
	info, e := os.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	}
	f, e := os.Open(name)
	if e != nil {
		return nil, e
	}
	now, e := f.Stat()
	if e != nil || !os.SameFile(info, now) {
		f.Close()
		return nil, errors.New("input changed while opening")
	}
	return f, nil
}
func (m material) parts() map[string][]byte {
	if m.receipt != nil {
		return map[string][]byte{"receipt.json": m.receipt}
	}
	return map[string][]byte{"manifest.json": m.manifest, "manifest.json.minisig": m.sig, "capabilities.json": m.caps}
}
func (s Source) metadata(ctx context.Context, tag, target, key string) (material, *Verified, error) {
	var m material
	if _, e := verifyKey(key); e != nil {
		return m, nil, e
	}
	for _, p := range []struct {
		name  string
		out   *[]byte
		limit int64
	}{{"manifest.json", &m.manifest, maxDocument}, {"manifest.json.minisig", &m.sig, 4096}, {"capabilities.json", &m.caps, maxDocument}} {
		f, e := s.open(ctx, tag, p.name, false)
		data, e := getBounded(f, e, p.limit)
		if e != nil {
			return m, nil, e
		}
		*p.out = data
	}
	v, e := Verify(key, tag, target, m.manifest, m.sig, m.caps)
	return m, v, e
}

// writeAtomic replaces a file whole or not at all (atomicfile.WriteFile).
func writeAtomic(file string, data []byte, mode os.FileMode) error {
	return atomicfile.WriteFile(file, data, mode, ".write-")
}
func uidToken() (string, error) {
	var id [16]byte
	_, e := rand.Read(id[:])
	return hex.EncodeToString(id[:]), e
}
func copyChecked(out io.Writer, input io.Reader, size int64, sum string) error {
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(input, size+1))
	if e != nil {
		return e
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sum {
		return errors.New("release file size or SHA-256 mismatch")
	}
	return nil
}

func checkELF(file, target, board string) error {
	e, err := elf.Open(file)
	if err != nil {
		return errors.New("invalid Linux ELF")
	}
	defer e.Close()
	p := platforms[target]
	machines := map[string]elf.Machine{"arm": elf.EM_ARM, "arm64": elf.EM_AARCH64, "386": elf.EM_386, "amd64": elf.EM_X86_64, "mips": elf.EM_MIPS, "mipsle": elf.EM_MIPS, "riscv64": elf.EM_RISCV, "loong64": elf.EM_LOONGARCH}
	bits := elf.ELFCLASS64
	switch p.arch {
	case "386", "arm", "mips", "mipsle":
		bits = elf.ELFCLASS32
	}
	if e.Class != bits || e.Machine != machines[p.arch] || (e.ByteOrder == binary.LittleEndian) != (p.endian == "little") || (e.Type != elf.ET_EXEC && e.Type != elf.ET_DYN) {
		return errors.New("wrong ELF machine, class or endian")
	}
	for _, ph := range e.Progs {
		if ph.Type == elf.PT_INTERP {
			return errors.New("Mihomo core requires a dynamic loader")
		}
	}
	libs, err := e.ImportedLibraries()
	if err != nil || len(libs) != 0 {
		return errors.New("Mihomo core must be statically linked")
	}
	b, err := buildinfo.ReadFile(file)
	if err != nil {
		return errors.New("missing Go architecture metadata")
	}
	build := map[string]string{}
	for _, s := range b.Settings {
		build[s.Key] = s.Value
	}
	if build["GOOS"] != "linux" || build["GOARCH"] != p.arch {
		return errors.New("core OS/architecture mismatch")
	}
	// Go ISA flags describe Go-generated instructions only. A native archive
	// linked by cgo can independently require NEON, an FPU or a newer x86
	// instruction set. Match the linked objects to a qualified board.
	if e.Class == elf.ELFCLASS32 && build["CGO_ENABLED"] != "0" {
		if err := native32(file, e, board); err != nil {
			return err
		}
	}
	// These three release identities advertise the baseline ISA, including
	// boards without optional LSE, AVX or newer RISC-V extensions.
	for _, level := range []struct{ arch, option, baseline string }{
		{"amd64", "GOAMD64", "v1"}, {"arm64", "GOARM64", "v8.0"},
		{"riscv64", "GORISCV64", "rva20u64"},
	} {
		if level.arch == p.arch && build[level.option] != "" && build[level.option] != level.baseline {
			return errors.New("core instruction set exceeds the declared target")
		}
	}
	if p.arch == "arm" {
		ver, fp, _ := strings.Cut(build["GOARM"], ",")
		if "armv"+ver != target || fp != "" && fp != p.float+"float" {
			return errors.New("wrong ARM instruction or floating-point ABI")
		}
	} else if p.float == "soft" {
		name := "GOMIPS"
		if p.arch == "386" {
			name = "GO386"
		}
		if build[name] != "softfloat" {
			return errors.New("wrong software floating-point ABI")
		}
	}
	return nil
}

// The compressed stream has an independently verified hash. Extraction only
// materializes listed regular files in a newly created directory.
func unpack(archive, dir, target, board string, a Asset) error {
	f, e := openRegular(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	reader := io.LimitReader(f, a.Size+1)
	archiveHash := sha256.New()
	raw := io.TeeReader(reader, archiveHash)
	z, e := gzip.NewReader(raw)
	if e != nil {
		return e
	}
	defer z.Close()
	decompressed := &io.LimitedReader{R: z, N: a.Expanded + 2*MiB}
	if !strings.HasSuffix(a.Name, ".tar.gz") {
		if len(a.Files) != 1 || a.Files[0].Kind != "core" {
			return errors.New("invalid gzip core receipt")
		}
		m := a.Files[0]
		dst := filepath.Join(dir, m.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		err = copyChecked(out, decompressed, m.Size, m.SHA256)
		if err == nil {
			err = out.Sync()
		}
		ce := out.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		if _, err = io.Copy(io.Discard, raw); err != nil || hex.EncodeToString(archiveHash.Sum(nil)) != a.SHA256 {
			return errors.New("archive was modified during extraction")
		}
		if err = checkELF(dst, target, board); err != nil {
			return err
		}
		return atomicfile.SyncDir(filepath.Dir(dst))
	}
	tr := tar.NewReader(decompressed)
	remaining := map[string]Member{}
	for _, m := range a.Files {
		remaining[m.Path] = m
	}
	dirs := map[string]bool{}
	for _, m := range a.Files {
		for p := filepath.Dir(m.Path); p != "."; p = filepath.Dir(p) {
			dirs[p] = true
		}
	}
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if entries >= 4096 {
			return errors.New("too many archive entries")
		}
		if h.Typeflag == tar.TypeDir && dirs[strings.TrimSuffix(h.Name, "/")] {
			continue
		}
		m, ok := remaining[h.Name]
		if !ok || h.Typeflag != tar.TypeReg || h.Size != m.Size {
			return errors.New("unexpected or invalid archive member")
		}
		dst := filepath.Join(dir, m.Path)
		if e = os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if m.Kind == "core" {
			mode = 0755
		}
		out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		e = copyChecked(out, tr, m.Size, m.SHA256)
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if m.Kind == "core" {
			if e = checkELF(dst, target, board); e != nil {
				return e
			}
		}
		if e = atomicfile.SyncDir(filepath.Dir(dst)); e != nil {
			return e
		}
		delete(remaining, m.Path)
	}
	if decompressed.N == 0 {
		return errors.New("archive exceeds expanded size limit")
	}
	if _, e = io.Copy(io.Discard, raw); e != nil {
		return e
	}
	if hex.EncodeToString(archiveHash.Sum(nil)) != a.SHA256 {
		return errors.New("archive was modified during extraction")
	}
	if len(remaining) != 0 {
		return errors.New("archive omits verified members")
	}
	// Nested directory entries must also reach disk before a state commit.
	for p := range dirs {
		if e = atomicfile.SyncDir(filepath.Join(dir, p)); e != nil {
			return e
		}
	}
	return atomicfile.SyncDir(dir)
}
