package subscription

import (
	"os"
	"testing"
)

func TestSetMatchBundledMRS(t *testing.T) {
	site, _ := os.ReadFile("../../files/usr/share/hyper/rules/geosite-cn.mrs")
	ip, _ := os.ReadFile("../../files/usr/share/hyper/rules/geoip-cn.mrs")
	domain := BinarySet{Format: "mrs", Behavior: "domain", Data: site}
	cidr := BinarySet{Format: "mrs", Behavior: "ipcidr", Data: ip}
	for q, want := range map[string]bool{"baidu.com": true, "www.baidu.com": true, "qq.com": true, "google.com": false, "github.com": false} {
		if got, err := SetMatch(domain, q); err != nil || got != want {
			t.Errorf("geosite-cn %s = %v, %v", q, got, err)
		}
	}
	for q, want := range map[string]bool{"114.114.114.114": true, "223.5.5.5": true, "8.8.8.8": false, "1.1.1.1": false} {
		if got, err := SetMatch(cidr, q); err != nil || got != want {
			t.Errorf("geoip-cn %s = %v, %v", q, got, err)
		}
	}
	if _, err := SetMatch(domain, "a b"); err != ErrSetQuery {
		t.Fatal(err)
	}
}

func TestSetMatchText(t *testing.T) {
	set := BinarySet{Format: "text", Behavior: "classical", Data: []byte("DOMAIN-SUFFIX,example.com\nIP-CIDR,10.0.0.0/8\nDOMAIN-KEYWORD,ads\n")}
	for q, want := range map[string]bool{"a.example.com": true, "example.com": true, "badexample.com": false, "10.1.2.3": true, "myads.net": true, "11.0.0.1": false} {
		if got, err := SetMatch(set, q); err != nil || got != want {
			t.Errorf("%s = %v, %v", q, got, err)
		}
	}
	dom := BinarySet{Format: "text", Behavior: "domain", Data: []byte("+.a.com\n*.b.com\nc.com\n")}
	for q, want := range map[string]bool{"a.com": true, "x.y.a.com": true, "x.b.com": true, "x.y.b.com": false, "b.com": false, "c.com": true, "x.c.com": false} {
		if got, err := SetMatch(dom, q); err != nil || got != want {
			t.Errorf("%s = %v, %v", q, got, err)
		}
	}
}
