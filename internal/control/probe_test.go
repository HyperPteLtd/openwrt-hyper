package control

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func serveController(t *testing.T, handler func(*http.Request) (int, string)) Client {
	t.Helper()
	old := transport
	t.Cleanup(func() { transport = old })
	transport = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != Address || req.Header.Get("Authorization") != "Bearer test-control-secret" {
			t.Fatal("request left authenticated loopback")
		}
		code, body := handler(req)
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	return Client{Secret: "test-control-secret"}
}
func TestMeasureUsesDirectResponseAndSeparatesControlFailures(t *testing.T) {
	for _, test := range []struct {
		code       int
		nodeFailed bool
	}{{200, false}, {503, true}, {504, true}, {401, false}, {404, false}, {500, false}} {
		client := serveController(t, func(r *http.Request) (int, string) {
			if r.Method != "GET" || r.URL.EscapedPath() != "/proxies/HK%2F01/delay" || r.URL.Query().Get("url") == "" || r.URL.Query().Get("timeout") == "" {
				t.Fatal(r.URL)
			}
			return test.code, `{"delay":18}`
		})
		s, e := client.Measure(context.Background(), "HK/01")
		if test.code == 200 && (e != nil || s.Delay != 18 || s.Measured == 0) || test.code != 200 && (e == nil || errors.Is(e, ErrProbeFailed) != test.nodeFailed) {
			t.Fatal(test, s, e)
		}
	}
}
func TestSelectRequiresReadbackAndMapsDirect(t *testing.T) {
	selected := "old"
	calls := 0
	c := serveController(t, func(r *http.Request) (int, string) {
		if r.URL.Path != "/proxies/domestic" {
			t.Fatal(r.URL)
		}
		calls++
		if r.Method == "PUT" {
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"name":"DIRECT"}` {
				t.Fatal(string(b))
			}
			return 204, ""
		}
		return 200, `{"now":"` + selected + `"}`
	})
	if e := c.Select(context.Background(), "domestic", "@direct"); e == nil {
		t.Fatal("unconfirmed selection")
	}
	selected = "DIRECT"
	if e := c.Select(context.Background(), "domestic", "@direct"); e != nil || calls != 4 {
		t.Fatal(e, calls)
	}
}
func TestSnapshotConnectionUsesObservedChainAndBytes(t *testing.T) {
	c := serveController(t, func(*http.Request) (int, string) {
		return 200, `{"connections":[{"id":"flow","metadata":{"network":"tcp","sourceIP":"192.0.2.1","sourcePort":"91","host":"example.org","destinationPort":"443"},"chains":["DIRECT","Domestic"],"rule":"Match","upload":10}]}`
	})
	ctx, end := context.WithCancel(context.Background())
	defer end()
	var item Connection
	wait := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	err := c.Connections(ctx, wait, func(v Connection) { item = v; end() })
	if !errors.Is(err, context.Canceled) || item.Outbound != "@direct" || item.Chain[1] != "Domestic" || item.Result != "traffic_observed" || item.Destination != "example.org:443" {
		t.Fatal(item, err)
	}
}
