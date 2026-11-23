// Package main verifies login and import against a disposable subscription service.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func save(path string, v any) {
	data, e := json.MarshalIndent(v, "", "  ")
	must(e)
	must(os.WriteFile(path, append(data, '\n'), 0600))
}

type anonymous struct {
	http.RoundTripper
	requests      int
	shareURL      string
	shareAgent    string
	shareRequests int
}

func (a *anonymous) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		return nil, fmt.Errorf("external authentication leak")
	}
	if r.URL.String() == a.shareURL {
		if (a.shareAgent == "Hyper" && !(strings.HasPrefix(r.UserAgent(), "Hyper/") && strings.HasSuffix(r.UserAgent(), " (OpenWrt)"))) || (a.shareAgent != "Hyper" && r.UserAgent() != a.shareAgent) {
			return nil, fmt.Errorf("incorrect anonymous subscription user agent")
		}
		a.shareRequests++
	}
	a.requests++
	return a.RoundTripper.RoundTrip(r)
}
func main() {
	access := "both"
	if len(os.Args) == 4 && os.Args[3] == "--link-only" {
		access = "link"
	} else if len(os.Args) == 4 && os.Args[3] == "--app-only" {
		access = "app"
	} else if len(os.Args) != 3 {
		panic("usage: ingest WORKER_PORT PRIVATE_FIXTURE_DIR [--link-only|--app-only]")
	}
	port, e := strconv.Atoi(os.Args[1])
	must(e)
	if port < 1 || port > 65535 {
		panic("invalid port")
	}
	dir := os.Args[2]
	origin := fmt.Sprintf("https://127.0.0.1:%d", port)
	pem, e := os.ReadFile(filepath.Join(dir, "cert.pem"))
	must(e)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		panic("invalid fixture certificate")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}, Timeout: 30 * time.Second}
	ctx := context.Background()
	fetcher := subscription.Fetcher{Client: client}
	b, e := os.ReadFile(filepath.Join(dir, "member.json"))
	must(e)
	var user struct{ Email, Password string }
	must(json.Unmarshal(b, &user))
	// The test provisions a single plan. Use the same public API entrypoints as the router helper.
	store := subscription.OpenStore(filepath.Join(dir, "router-store"))
	ticket, e := store.BeginLogin(origin)
	must(e)
	acc, e := fetcher.Login(ctx, origin, user.Email, user.Password)
	must(e)
	if len(acc.Plans) != 1 {
		panic("expected one plan")
	}
	plan := acc.Source(acc.Plans[0])
	ids, e := store.SaveAccount(acc, ticket)
	must(e)
	if !slices.Contains(ids, plan.ID) {
		panic("account was not saved")
	}
	prefix := fmt.Sprintf("%s/api/v1/app/proxy/subscriptions/%d/", origin, plan.PlanID)
	share := ""
	if rawURL, err := os.ReadFile(filepath.Join(dir, "share-url")); err == nil {
		share = strings.TrimSpace(string(rawURL))
		if !strings.HasPrefix(share, origin+"/") {
			panic("nonlocal share URL")
		}
	} else if !os.IsNotExist(err) {
		panic(err)
	}
	detailURL := fmt.Sprintf("%s/api/v1/app/billing/subscriptions/%d", origin, plan.PlanID)
	auditLogin := &accountRefresh{RoundTripper: client.Transport, configURL: prefix + "config", detailURL: detailURL, expectedAccess: access, shareURL: share}
	loginFetcher := subscription.Fetcher{Client: &http.Client{Transport: auditLogin, Timeout: client.Timeout}}
	if access == "link" {
		if share == "" {
			panic("missing issued URL for link-only fixture")
		}
		result, err := loginFetcher.Refresh(ctx, store, plan.ID, nil, subscription.Policy{})
		must(err)
		profile, err := store.Load(plan.ID)
		must(err)
		if result.Decision != "ready" || profile.Format != "hyper" || len(profile.Nodes) != 15 || auditLogin.details != 1 || auditLogin.requests != 0 || auditLogin.shareRequests != 1 {
			panic(fmt.Errorf("link-only import: %s, format %s, nodes %d, detail/config/link requests %d/%d/%d", result.Decision, profile.Format, len(profile.Nodes), auditLogin.details, auditLogin.requests, auditLogin.shareRequests))
		}
		checked := verifyCore(dir, plan.ID, profile)
		save(filepath.Join(dir, "platform-report.json"), map[string]any{"candidate_mihomo_checked": checked, "decision": result.Decision, "format": profile.Format, "nodes": len(profile.Nodes), "groups": len(profile.Groups), "rules": len(profile.Rules), "access": access, "account_detail_requests": auditLogin.details, "account_config_requests": auditLogin.requests, "anonymous_share_requests": auditLogin.shareRequests})
		return
	}
	raw := func(endpoint, ua string) []byte {
		req, e := http.NewRequestWithContext(ctx, "GET", prefix+endpoint, nil)
		must(e)
		req.Header.Set("Authorization", "Bearer "+plan.Token)
		req.Header.Set("User-Agent", ua)
		response, e := client.Do(req)
		must(e)
		defer response.Body.Close()
		if response.StatusCode != 200 {
			panic(fmt.Errorf("%s: HTTP %d", endpoint, response.StatusCode))
		}
		b, e := io.ReadAll(io.LimitReader(response.Body, subscription.MaxBody+1))
		must(e)
		if len(b) > subscription.MaxBody {
			panic("oversize export")
		}
		return b
	}
	var inventory struct {
		Usable bool
		Nodes  []struct{ Protocol string }
	}
	must(json.Unmarshal(raw("nodes", "Hyper/qa (OpenWrt)"), &inventory))
	var inv []string
	for _, n := range inventory.Nodes {
		inv = append(inv, n.Protocol)
	}
	slices.Sort(inv)
	reports := map[string]any{"inventory": inv, "usable": inventory.Usable, "platform_login": "pass"}
	nativeTemplate := raw("config", "Hyper/qa (OpenWrt)")
	c, e := loginFetcher.Refresh(ctx, store, plan.ID, nil, subscription.Policy{})
	must(e)
	var warnings []subscription.Notice
	if auditLogin.warnings > 0 {
		details := auditLogin.warningCodes
		if details == "" {
			details = strconv.FormatUint(auditLogin.warnings, 10)
		}
		warnings = append(warnings, subscription.Notice{Code: "platform_routing_warnings", At: details})
	}
	if c.Decision != "ready" || !slices.Equal(c.Notices, warnings) || auditLogin.details != 1 || auditLogin.requests != 1 || auditLogin.shareRequests != 0 {
		panic(fmt.Errorf("unexpected login refresh: %s, %d notices, detail/config/link requests %d/%d/%d", c.Decision, len(c.Notices), auditLogin.details, auditLogin.requests, auditLogin.shareRequests))
	}
	reports["refresh_decision"] = c.Decision
	repeated := subscription.Fetcher{Client: client, BeforeCommit: func(context.Context, subscription.Profile) (func() error, error) {
		return nil, fmt.Errorf("identical running profile requested preflight")
	}}
	started := time.Now()
	recheck, err := repeated.Refresh(ctx, store, plan.ID, nil, subscription.Policy{})
	must(err)
	if !recheck.Unchanged() {
		panic("unchanged platform response replaced its snapshot")
	}
	reports["metadata_only_repeat_ms"] = time.Since(started).Milliseconds()
	p, e := subscription.OpenStore(filepath.Join(dir, "router-store")).Load(plan.ID)
	must(e)
	if p.Format != "hyper" || auditLogin.publicRuleRequests == 0 {
		panic("native account format or public rule requests missing")
	}
	must(verifyNativeProfile(nativeTemplate, p, inv))
	reports["committed_nodes"] = len(p.Nodes)
	reports["candidate_mihomo_checked"] = verifyCore(dir, plan.ID, p)
	reports["stored_rules"] = len(p.Rules)
	reports["stored_groups"] = len(p.Groups)
	reports["stored_binary_sets"] = len(p.BinarySets)
	reports["account_format"] = p.Format
	reports["dedicated_mtproxy_excluded_from_groups"] = true
	reports["access"] = access
	reports["account_detail_requests"] = auditLogin.details
	reports["account_link_requests"] = auditLogin.shareRequests
	reports["account_config_requests"] = auditLogin.requests
	reports["account_public_rule_requests_without_auth"] = auditLogin.publicRuleRequests
	reports["stored_native_policy_matches_export"] = true
	reports["platform_routing_warning_count"] = auditLogin.warnings
	if access != "app" && share != "" {
		url := share
		audit := &anonymous{RoundTripper: client.Transport, shareURL: url}
		external := subscription.Fetcher{Client: &http.Client{Transport: audit, Timeout: 30 * time.Second}}
		for _, ua := range []string{"Hyper", "clash", "sing-box"} {
			audit.shareAgent = ua
			id := "external-" + strings.ToLower(ua)
			must(store.Configure(subscription.Source{ID: id, Name: id, Kind: "external", URL: url, UserAgent: ua}))
			c, e := external.Refresh(ctx, store, id, nil, subscription.Policy{})
			record := map[string]any{}
			reports[id] = record
			must(e)
			if c.Decision != "ready" {
				panic("anonymous subscription did not become ready")
			}
			record["decision"] = c.Decision
			var kinds []string
			p, e := store.Load(id)
			must(e)
			for _, node := range p.Nodes {
				kinds = append(kinds, node.Protocol)
			}
			if ua == "Hyper" {
				must(verifyNativeProfile(nativeTemplate, p, inv))
			}
			record["candidate_mihomo_checked"] = verifyCore(dir, id, p)
			record["groups"] = len(p.Groups)
			record["rules"] = len(p.Rules)

			slices.Sort(kinds)
			record["committed"] = kinds
		}
		if audit.shareRequests != 3 {
			panic("expected all three user agents on the unchanged share URL")
		}
		reports["anonymous_share_requests"] = audit.shareRequests
		reports["anonymous_requests_without_cookie_or_bearer"] = audit.requests
	}
	save(filepath.Join(dir, "platform-report.json"), reports)
}
