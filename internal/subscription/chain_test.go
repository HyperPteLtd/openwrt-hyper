package subscription

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// docOf is a document as it reaches the router after its providers were expanded: subconv checks
// the chains it can see, but not those through nodes downloaded later.
func docOf(t *testing.T, body string) conversion {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	return conversion{doc: doc, format: "clash"}
}

func TestChainsResolveToIDsAndDropBrokenNodes(t *testing.T) {
	p, err := parseVerified("source", docOf(t, `proxies:
 - {name: Front, type: http, server: front.example, port: 80}
 - {name: Exit, type: http, server: exit.example, port: 80, dialer-proxy: Relay}
 - {name: Hop, type: http, server: hop.example, port: 80, dialer-proxy: Front}
 - {name: Straight, type: http, server: straight.example, port: 80, dialer-proxy: DIRECT}
 - {name: Loop1, type: http, server: loop1.example, port: 80, dialer-proxy: Loop2}
 - {name: Loop2, type: http, server: loop2.example, port: 80, dialer-proxy: Loop1}
 - {name: Self, type: http, server: self.example, port: 80, dialer-proxy: Pool}
 - {name: Dangling, type: http, server: dangling.example, port: 80, dialer-proxy: Missing}
 - {name: Behind, type: http, server: behind.example, port: 80, dialer-proxy: Dangling}
proxy-groups:
 - {name: Relay, type: select, proxies: [Front, Hop]}
 - {name: Pool, type: url-test, proxies: [Self, Front]}
 - {name: Main, type: select, proxies: [Exit, Loop1, Pool, Straight]}
rules:
 - MATCH,Main`), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Node{}
	for _, n := range p.Nodes {
		byName[n.Name] = n
	}
	group := map[string]Group{}
	for _, g := range p.Groups {
		group[g.Name] = g
	}
	for _, gone := range []string{"Loop1", "Loop2", "Self", "Dangling", "Behind"} {
		if _, ok := byName[gone]; ok {
			t.Fatal("broken chain kept", gone)
		}
	}
	if len(p.Nodes) != 4 || byName["Exit"].Dialer() != group["Relay"].ID || byName["Hop"].Dialer() != byName["Front"].ID || byName["Straight"].Dialer() != "" {
		t.Fatal("chains not resolved to IDs", p.Nodes)
	}
	if !slices.Equal(group["Pool"].Members, []string{byName["Front"].ID}) || !slices.Equal(group["Main"].Members, []string{byName["Exit"].ID, group["Pool"].ID, byName["Straight"].ID}) {
		t.Fatal("dropped chains still referenced", p.Groups)
	}
	codes := map[string]string{}
	for _, n := range p.Notices {
		if n.Code == "dialer_proxy_cycle" || n.Code == "dialer_proxy_unavailable" {
			codes[n.At] = n.Code
		}
	}
	want := map[string]string{"Loop1": "dialer_proxy_cycle", "Loop2": "dialer_proxy_cycle", "Self": "dialer_proxy_cycle", "Dangling": "dialer_proxy_unavailable", "Behind": "dialer_proxy_unavailable"}
	if len(codes) != len(want) {
		t.Fatal(p.Notices)
	}
	for k, v := range want {
		if codes[k] != v {
			t.Fatal(k, codes[k])
		}
	}
	if _, _, probe := byName["Exit"].TCPProbeEndpoint(); probe {
		t.Fatal("a chained node is not reachable from the router")
	}
	// Dropped chains cost nodes, not the subscription's routing.
	if c := Assess(p, nil, Policy{}); c.Decision != "ready" || c.Profile.Local {
		t.Fatal(c.Decision, c.Notices)
	}
}

func TestConverterRepairsTheChainsItSees(t *testing.T) {
	p := mustParse(t, `proxies:
 - {name: Front, type: http, server: front.example, port: 80}
 - {name: Loop1, type: http, server: loop1.example, port: 80, dialer-proxy: Loop2}
 - {name: Loop2, type: http, server: loop2.example, port: 80, dialer-proxy: Loop1}
 - {name: Dangling, type: http, server: dangling.example, port: 80, dialer-proxy: Missing}
proxy-groups:
 - {name: Main, type: select, proxies: [Front, Loop1, Loop2, Dangling]}
rules:
 - MATCH,Main`)
	codes := map[string]bool{}
	for _, n := range p.Notices {
		codes[n.Code] = true
	}
	if len(p.Nodes) != 4 || !codes["subconv_cyclic_reference"] || !codes["subconv_unknown_reference"] {
		t.Fatal(p.Nodes, p.Notices)
	}
	if c := Assess(p, nil, Policy{}); c.Decision != "ready" || c.Profile.Local {
		t.Fatal("repaired chains cost the routing", c.Decision)
	}
}

func TestSingBoxDetourBecomesAChain(t *testing.T) {
	p := mustParse(t, `{"outbounds":[
{"type":"selector","tag":"Front","outbounds":["a","b"]},
{"type":"http","tag":"a","server":"a.example","server_port":80},
{"type":"http","tag":"b","server":"b.example","server_port":80},
{"type":"http","tag":"Exit","server":"exit.example","server_port":80,"detour":"Front"}],
"route":{"final":"Exit"}}`)
	var exit Node
	for _, n := range p.Nodes {
		if n.Name == "Exit" {
			exit = n
		}
	}
	if len(p.Nodes) != 3 || exit.Dialer() == "" || exit.Dialer() != p.Groups[0].ID {
		t.Fatal("detour lost", p.Nodes, p.Notices)
	}
}
