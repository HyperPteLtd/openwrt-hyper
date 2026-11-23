package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

func TestChinaListProvidersFollowTheDownload(t *testing.T) {
	savedDir, savedPackaged := cnlist.Dir, cnlist.Packaged
	t.Cleanup(func() { cnlist.Dir, cnlist.Packaged = savedDir, savedPackaged })
	cnlist.Dir, cnlist.Packaged = t.TempDir(), t.TempDir()
	p := baseProfile(t, "p")
	i := initial()
	i.Source = p.Source
	n := Network{Method: "tproxy", Resolver: "192.0.2.53"}
	paths := func() map[string]string {
		out := map[string]string{}
		for name, v := range configForTest(t, i, p, n)["rule-providers"].(map[string]any) {
			if path, ok := v.(map[string]any)["path"].(string); ok {
				out[name] = path
			}
		}
		return out
	}
	// The local profile's mainland routes use the built-in sets: one provider each.
	want := map[string]string{dnsDomesticSet: "./builtin/geosite-cn.mrs", domesticIPSet: "./builtin/geoip-cn.mrs"}
	if got := paths(); len(got) != len(want) || got[dnsDomesticSet] != want[dnsDomesticSet] || got[domesticIPSet] != want[domesticIPSet] {
		t.Fatal("no download reads the packaged lists", got)
	}
	data, err := os.ReadFile("../../files/usr/share/hyper/rules/geoip-cn.mrs")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cnlist.Dir, "geoip-cn.mrs"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := paths(); got[domesticIPSet] != "./cnlists/geoip-cn.mrs" || got[dnsDomesticSet] != want[dnsDomesticSet] {
		t.Fatal("a valid download replaces only its own list", got)
	}
	// A plugin update that ships a newer list wins over an older download.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(cnlist.Dir, "geoip-cn.mrs"), old, old)
	if err = os.WriteFile(filepath.Join(cnlist.Packaged, "geoip-cn.mrs"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := chinaList(cnlist.Addresses); got != "./builtin/geoip-cn.mrs" {
		t.Fatal("a newer packaged list wins", got)
	}
}

func TestChinaListMoveDuringStartReloadsOnceRunning(t *testing.T) {
	var starts, reloads atomic.Int32
	proceed := make(chan struct{})
	f := reloadService{fakeService{start: func(c context.Context, _ Intent) error {
		starts.Add(1)
		select {
		case <-proceed:
			return nil
		case <-c.Done():
			return c.Err()
		}
	}}, func(context.Context, Intent) error { reloads.Add(1); return nil }}
	i := initial()
	i.Enabled = true
	i.Source = "primary"
	m := New(&fakeStore{i: i}, f, Timing{time.Second, 100 * time.Millisecond, 5 * time.Millisecond, time.Second})
	ctx, end := context.WithCancel(context.Background())
	defer end()
	go m.Run(ctx)
	eventually(t, func() bool { return starts.Load() == 1 && m.Status().Phase == "starting" })
	m.Reapply()
	close(proceed)
	eventually(t, func() bool { return reloads.Load() == 1 && m.Status().Phase == "running" })
	if starts.Load() != 1 {
		t.Fatal("a list move restarted the core")
	}
}

// Mihomo loads every rule provider on its own, so each mainland list has one provider: a
// subscription's GEOSITE,CN and GEOIP,CN rules, a template's GEOIP,CN, a rule stored before
// subconv and a local profile's mainland routes all use the built-in sets, with the targets
// and no-resolve they had. Every set a rule, the fake-ip filter or the DNS policy names is
// provided, in either mode and with the China shortcut on or off.
func TestEachChinaListLoadsOnce(t *testing.T) {
	own, err := subscription.Parse("own", []byte(`proxies:
 - {name: edge, type: http, server: edge.example, port: 443}
 - {name: far, type: http, server: far.example, port: 443}
proxy-groups:
 - {name: Proxy, type: select, proxies: [edge, far, DIRECT]}
 - {name: Domestic, type: select, proxies: [DIRECT, Proxy]}
rules:
 - DOMAIN-SUFFIX,example.org,Proxy
 - GEOSITE,CN,DIRECT
 - GEOIP,CN,DIRECT,no-resolve
 - GEOIP,cn,Domestic
 - MATCH,Proxy
`))
	if err != nil || len(own.Notices) != 0 || len(own.Rules) != 4 || len(own.Groups) != 2 {
		t.Fatal(err, own.Notices, own.Rules)
	}
	proxy, domestic := own.Groups[0].ID, own.Groups[1].ID
	if own.Rules[1].RuleSet != subscription.DomesticDomainSet || own.Rules[2].RuleSet != subscription.DomesticIPSet || own.Rules[3].RuleSet != subscription.DomesticIPSet {
		t.Fatal(own.Rules)
	}
	// The template's own mainland sets are other lists; its GEOIP,CN goes to its 🎯 Domestic group.
	template := own
	template.Source = "template"
	template.Template = &subscription.TemplateUse{Version: 1}
	template.BinarySets = []subscription.BinarySet{
		{Tag: "AdBlock", Format: "text", Behavior: "classical", SHA256: strings.Repeat("a", 64)},
		{Tag: "ChinaDomain", Format: "mrs", Behavior: "domain", SHA256: strings.Repeat("b", 64)},
		{Tag: "ChinaIP", Format: "mrs", Behavior: "ipcidr", SHA256: strings.Repeat("c", 64)},
	}
	template.Rules = []subscription.Rule{
		{Match: obj{"clash": "RULE-SET,AdBlock"}, RuleSet: "AdBlock", Target: subscription.Reject},
		{Match: obj{"clash": "RULE-SET,ChinaDomain"}, RuleSet: "ChinaDomain", Target: domestic},
		{Match: obj{"clash": "RULE-SET,ChinaIP", "no_resolve": true}, RuleSet: "ChinaIP", Target: domestic},
		own.Rules[3],
	}
	// Line subscriptions stored their GEOIP,CN rule as a rule_set match.
	stored := own
	stored.Source = "stored"
	stored.Rules = []subscription.Rule{{Match: obj{"rule_set": subscription.DomesticIPSet}, RuleSet: subscription.DomesticIPSet, Target: subscription.Direct}}
	local := baseProfile(t, "local")

	mainland := []string{
		"RULE-SET," + dnsDomesticSet + ",DIRECT", "RULE-SET," + domesticIPSet + ",DIRECT,no-resolve", "RULE-SET," + domesticIPSet + "," + domestic,
	}
	for _, tt := range []struct {
		p    subscription.Profile
		want []string // the profile's rules in order, as the core gets them in smart mode
	}{
		{own, append([]string{"DOMAIN-SUFFIX,example.org," + proxy}, mainland...)},
		{template, []string{"RULE-SET,AdBlock,REJECT", "RULE-SET,ChinaDomain," + domestic, "RULE-SET,ChinaIP," + domestic + ",no-resolve", "RULE-SET," + domesticIPSet + "," + domestic}},
		{stored, []string{"RULE-SET," + domesticIPSet + ",DIRECT"}},
		{local, []string{"RULE-SET," + dnsDomesticSet + ",DIRECT", "RULE-SET," + domesticIPSet + ",DIRECT"}},
	} {
		for _, mode := range []string{"smart", "global"} {
			for _, n := range []Network{
				{Method: "tproxy", Resolver: "192.0.2.53"},
				{Method: "tproxy", Resolver: "192.0.2.53", DNS: true, FakeIP: true},
				{Method: "tproxy", Resolver: "192.0.2.53", DNS: true, FakeIP: true, ChinaDirect: true},
			} {
				i := initial()
				i.Source, i.Mode = tt.p.Source, mode
				c := configForTest(t, i, tt.p, n)
				at := fmt.Sprintf("%s %s shortcut=%v", tt.p.Source, mode, chinaShortcut(i, n))
				providers := c["rule-providers"].(map[string]any)
				paths := map[string]string{}
				for name, v := range providers {
					path, _ := v.(map[string]any)["path"].(string)
					if path == "" {
						continue
					}
					if other, ok := paths[path]; ok {
						t.Fatalf("%s: %s and %s both load %s", at, other, name, path)
					}
					paths[path] = name
				}
				if paths[chinaList(cnlist.Domains)] != dnsDomesticSet || paths[chinaList(cnlist.Addresses)] != domesticIPSet {
					t.Fatal(at, paths)
				}
				var named []string
				rules := c["rules"].([]any)
				for _, r := range rules {
					for _, m := range ruleSetRef.FindAllStringSubmatch(r.(string), -1) {
						named = append(named, strings.TrimSpace(m[2]))
					}
				}
				dns := c["dns"].(map[string]any)
				filter, _ := dns["fake-ip-filter"].([]any)
				for _, f := range filter {
					if set, ok := strings.CutPrefix(f.(string), "RULE-SET,"); ok {
						set, _, _ = strings.Cut(set, ",")
						named = append(named, set)
					}
				}
				for key := range dns["nameserver-policy"].(map[string]any) {
					if set, ok := strings.CutPrefix(key, "rule-set:"); ok {
						named = append(named, set)
					}
				}
				for _, set := range named {
					if providers[set] == nil {
						t.Fatalf("%s: no provider for %s", at, set)
					}
				}
				// The profile's rules keep their order, targets and no-resolve.
				want := tt.want
				if mode == "global" {
					want = nil
				}
				pos := 0
				for _, r := range rules {
					if pos < len(want) && r == want[pos] {
						pos++
					}
				}
				if pos != len(want) {
					t.Fatalf("%s: %v lacks %v", at, rules, want)
				}
				if mode == "global" && slices.ContainsFunc(rules, func(r any) bool { return strings.HasPrefix(r.(string), "RULE-SET,"+domesticIPSet+",") }) {
					t.Fatal(at, rules)
				}
			}
		}
	}
}

// The LuCI pages find a mainland list's hits and connections by the name the core loads it
// under, its rules and lookups by the subscription's tag, and show it in Chinese (dom.js
// mainlandSets).
func TestPagesNameTheChinaListsAsTheCore(t *testing.T) {
	dom, err := os.ReadFile("../../files/www/luci-static/resources/hyper/dom.js")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)export const mainlandSets = \{\n(.*?)\n\};`).FindSubmatch(dom)
	if table == nil {
		t.Fatal("dom.js has no mainlandSets")
	}
	entries := regexp.MustCompile(`'([^']+)': \{ label: '([^']+)', tag: '([^']+)' \},`).FindAllSubmatch(table[1], -1)
	if len(entries) != len(mainlandSets) || len(entries) != strings.Count(string(table[1]), "\n")+1 {
		t.Fatalf("dom.js lists %d mainland sets, the core %d", len(entries), len(mainlandSets))
	}
	for _, e := range entries {
		name, label, tag := string(e[1]), string(e[2]), string(e[3])
		if mainlandSets[tag] != name || !regexp.MustCompile(`\p{Han}`).MatchString(label) {
			t.Errorf("dom.js shows %s (%s) as %q", name, tag, label)
		}
	}
}

// The connection log names the router's QUIC rules by the payload the core reports for them:
// the rule as compiled, without its target. A change to either rule must change its label key in
// records.js too, or the log falls back to the raw rule.
func TestConnectionLogNamesTheQUICRules(t *testing.T) {
	page, err := os.ReadFile("../../files/www/luci-static/resources/hyper/pages/records.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{quicProxied, quicBlock} {
		payload := strings.TrimSuffix(rule, ",REJECT")
		if payload == rule || !strings.Contains(string(page), "'"+payload+"'") {
			t.Errorf("records.js routerRules has no label for %s", payload)
		}
	}
}
