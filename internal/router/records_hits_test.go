package router

import (
	"fmt"
	"testing"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
)

func TestRuleHitsOutliveTheConnectionRing(t *testing.T) {
	r := NewRecords()
	for i := range recordLimit + 10 {
		r.Observe(control.Connection{ID: fmt.Sprint(i), Created: int64(i + 1), Destination: "x", Rule: "RuleSet,ads"})
		r.Observe(control.Connection{ID: fmt.Sprint(i), Closed: 5}) // updates are not new hits
	}
	r.Observe(control.Connection{ID: "m", Created: 1, Destination: "x", Rule: "Match"})
	if h := r.RuleHits(); len(h) != 1 || h["ads"].Count != recordLimit+10 || h["ads"].Last != recordLimit+10 {
		t.Fatal(h)
	}
	r.Clear("connections")
	if len(r.RuleHits()) != 0 {
		t.Fatal("not cleared")
	}
}
