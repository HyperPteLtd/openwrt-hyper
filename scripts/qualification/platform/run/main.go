// Command run installs a disposable loopback platform from its source, issues a plan through
// its public HTTP API, and exercises both router entries. Run it from the repository root:
//
//	go run ./scripts/qualification/platform/run --platform DIR --output FILE [--keep DIR] [--dependencies DIR] [--link-only|--app-only]
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Only application source, migrations and the public local binding definitions.
// No persisted Cloudflare state, account dotfiles or development credentials.
var source = []string{"src", "resources", "assets", "drizzle", "package.json", "bun.lock", "tsconfig.json", "wrangler.jsonc"}

func main() {
	platform := flag.String("platform", "", "local checkout with the platform source (required)")
	keep := flag.String("keep", "", "new private directory to retain the synthetic profiles for wire tests")
	deps := flag.String("dependencies", "", "optional existing node_modules, copied into the isolated directory")
	output := flag.String("output", "", "sanitized statistics only (required)")
	linkOnly := flag.Bool("link-only", false, "exercise login when the server issues subscription links only")
	appOnly := flag.Bool("app-only", false, "exercise login when the server only allows authenticated configuration")
	flag.Parse()
	if *platform == "" || *output == "" || flag.NArg() != 0 || (*linkOnly && *appOnly) {
		flag.Usage()
		os.Exit(2)
	}
	mode := "both"
	if *linkOnly {
		mode = "link"
	} else if *appOnly {
		mode = "app"
	}
	if err := run(*platform, *keep, *deps, *output, mode); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		os.Exit(1)
	}
}

func run(platform, keep, deps, output, mode string) error {
	var flags []string
	if mode != "both" {
		flags = []string{"--" + mode + "-only"}
	}
	syscall.Umask(0o077)
	stage := keep
	if keep != "" {
		if err := os.Mkdir(keep, 0o700); err != nil {
			return err
		}
	} else {
		tmp, err := os.MkdirTemp("", "hyper-platform-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		stage = tmp
	}
	stage, _ = filepath.Abs(stage)
	origin, _ := filepath.Abs(platform)
	revRaw, err := exec.Command("git", "-C", origin, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("git rev-parse: %w", err)
	}
	var files []string
	for _, part := range source {
		err := filepath.WalkDir(filepath.Join(origin, part), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), "._") || d.Name() == "__pycache__" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(origin, path)
			dest := filepath.Join(stage, rel)
			if d.IsDir() {
				return os.MkdirAll(dest, 0o700)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return os.WriteFile(dest, data, 0o600)
		})
		if err != nil {
			return err
		}
	}
	slices.Sort(files)
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(stage, f))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		h.Write(append([]byte(f), 0))
		h.Write(sum[:])
	}
	home := filepath.Join(stage, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return err
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "WRANGLER_SEND_METRICS=false"}
	command := func(stdout io.Writer, name string, args ...string) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = stage, env, stdout, stdout
		return cmd
	}
	if deps != "" {
		if err := exec.Command("cp", "-R", deps, filepath.Join(stage, "node_modules")).Run(); err != nil {
			return fmt.Errorf("copy dependencies: %w", err)
		}
		exec.Command("find", filepath.Join(stage, "node_modules"), "(", "-name", ".cache", "-o", "-name", ".vite", "-o", "-name", "._*", ")",
			"-prune", "-exec", "rm", "-rf", "{}", "+").Run()
	} else if err := command(os.Stderr, "bun", "install", "--frozen-lockfile", "--ignore-scripts").Run(); err != nil {
		return fmt.Errorf("bun install: %w", err)
	}
	wrangler := filepath.Join(stage, "node_modules/.bin/wrangler")
	cert, key := filepath.Join(stage, "cert.pem"), filepath.Join(stage, "key.pem")
	if err := command(io.Discard, "openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "1", "-subj", "/CN=localhost",
		"-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-addext", "basicConstraints=critical,CA:TRUE",
		"-keyout", key, "-out", cert).Run(); err != nil {
		return fmt.Errorf("openssl: %w", err)
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	log, err := os.Create(filepath.Join(stage, "worker.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	if err := command(log, wrangler, "d1", "migrations", "apply", "DB", "--local", "--persist-to", "state").Run(); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	worker := command(log, wrangler, "dev", "--local", "--ip", "127.0.0.1", "--port", strconv.Itoa(port), "--local-protocol", "https",
		"--https-key-path", key, "--https-cert-path", cert, "--persist-to", "state")
	worker.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := worker.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { worker.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
		default:
			syscall.Kill(-worker.Process.Pid, syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(15 * time.Second):
				syscall.Kill(-worker.Process.Pid, syscall.SIGKILL)
			}
		}
	}()
	pem, err := os.ReadFile(cert)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	client := &http.Client{Timeout: 75 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	base := fmt.Sprintf("https://127.0.0.1:%d", port)
	ready := false
	for range 100 {
		select {
		case <-exited:
			return errors.New("local Worker exited")
		default:
		}
		probe := &http.Client{Timeout: time.Second, Transport: client.Transport}
		if resp, err := probe.Get(base + "/"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return errors.New("local Worker did not become ready")
	}
	if err := seed(client, base, filepath.Join(stage, "member.json"), mode); err != nil {
		return err
	}
	ingest := exec.Command("go", append([]string{"run", "./scripts/qualification/platform/ingest", strconv.Itoa(port), stage}, flags...)...)
	ingest.Stdout, ingest.Stderr = os.Stdout, os.Stderr
	if err := ingest.Run(); err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(stage, "platform-report.json"))
	if err != nil {
		return err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	result["suite"] = "local-platform-" + mode + "-v2"
	result["platform_revision"] = strings.TrimSpace(string(revRaw))
	result["platform_source_sha256"] = hex.EncodeToString(h.Sum(nil))
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(output, append(out, '\n'), 0o600)
}

var protocols = []struct {
	name     string
	settings map[string]any
}{
	{"vless", map[string]any{"security": "none"}},
	{"vmess", map[string]any{"security": "none"}},
	{"trojan", map[string]any{"security": "tls", "sni": "router-fixture.test"}},
	{"shadowsocks", map[string]any{"cipher": "aes-128-gcm"}},
	{"shadowsocksr", map[string]any{"cipher": "aes-128-cfb", "protocol": "auth_aes128_sha1"}},
	{"anytls", map[string]any{"sni": "router-fixture.test"}},
	{"hysteria2", map[string]any{"sni": "router-fixture.test"}},
	{"tuic", map[string]any{"sni": "router-fixture.test"}},
	{"naive", map[string]any{"sni": "router-fixture.test", "network": "tcp"}},
	{"http", map[string]any{}},
	{"https", map[string]any{"sni": "router-fixture.test"}},
	{"socks5", map[string]any{}},
	{"snell", map[string]any{"version": "6", "mode": "unshaped"}},
	{"mieru", map[string]any{"network": "tcp"}},
	{"nowhere", map[string]any{"sni": "router-fixture.test", "cert_mode": "file", "cert_file": "/fixture/cert.pem",
		"cert_key_file": "/fixture/key.pem", "obfs": "none"}},
	{"mtproxy", map[string]any{"security": "none", "sni": "router-fixture.test"}},
}

func token() string {
	b := make([]byte, 28)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// seed creates an independent router subscription fixture through the Worker's public HTTP API.
func seed(client *http.Client, origin, output, access string) error {
	const admin = "/api/v1/admin"
	rootToken := ""
	req := func(method, path string, body any) (map[string]any, http.Header, error) {
		var payload io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, nil, err
			}
			payload = bytes.NewReader(data)
		}
		r, err := http.NewRequest(method, origin+path, payload)
		if err != nil {
			return nil, nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if rootToken != "" {
			r.Header.Set("Authorization", "Bearer "+rootToken)
		}
		resp, err := client.Do(r)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var out map[string]any
		if len(raw) > 0 {
			json.Unmarshal(raw, &out)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			code, _ := out["code"].(string)
			return nil, nil, fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, code)
		}
		return out, resp.Header, nil
	}
	root := map[string]any{"email": "setup@fixture.test", "password": token()}
	member := map[string]any{"email": "router@fixture.test", "password": token()}
	if _, _, err := req("POST", "/", map[string]any{"siteName": "Router Fixture", "name": "Fixture Admin",
		"email": root["email"], "password": root["password"]}); err != nil {
		return err
	}
	session, hdr, err := req("POST", "/api/auth/sign-in/email", root)
	if err != nil {
		return err
	}
	if rootToken = hdr.Get("set-auth-token"); rootToken == "" {
		rootToken, _ = session["token"].(string)
	}
	user, _, err := req("POST", admin+"/users", map[string]any{"name": "Router Test", "email": member["email"], "password": member["password"]})
	if err != nil {
		return err
	}
	if _, _, err := req("PUT", admin+"/settings/domains", map[string]any{"subscriptionAccess": access}); err != nil {
		return err
	}
	for i, p := range protocols {
		if _, _, err := req("POST", admin+"/proxy/protocols", map[string]any{"name": p.name, "type": p.name,
			"defaultPort": 42001 + i, "settings": p.settings, "globalDefault": true}); err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
	}
	if _, _, err := req("POST", admin+"/proxy/servers", map[string]any{"name": "Router", "region": "Test", "ipv4": "127.0.0.1"}); err != nil {
		return err
	}
	if _, _, err := req("POST", admin+"/billing/plans", map[string]any{"id": "qualify", "names": map[string]any{"en-US": "Test plan"},
		"kind": "subscription", "prices": []any{map[string]any{"period": "month", "amount": 1000}}}); err != nil {
		return err
	}
	grant, _, err := req("POST", admin+"/billing/subscriptions", map[string]any{"userId": user["id"], "planId": "qualify", "period": "month"})
	if err != nil {
		return err
	}
	url, isString := grant["url"].(string)
	if access == "app" {
		if grant["url"] != nil {
			return errors.New("app-only mode issued a share URL")
		}
	} else if !isString || !strings.HasPrefix(url, origin+"/") {
		return errors.New("platform did not return its local share URL")
	} else if err := os.WriteFile(filepath.Join(filepath.Dir(output), "share-url"), []byte(url+"\n"), 0o600); err != nil {
		return err
	}
	data, err := json.Marshal(member)
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, data, 0o600); err != nil {
		return err
	}
	fmt.Println("Created one isolated plan with 16 protocol definitions.")
	return nil
}
