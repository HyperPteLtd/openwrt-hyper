package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"
)

type Check struct {
	OK    bool  `json:"ok"`
	Delay int64 `json:"delay_ms"`
}

// Interactive route diagnostics use the local proxy listener. Their results
// never change forwarding intent or the watchdog's local liveness assessment.
func (s *System) Checks(ctx context.Context, dns bool) map[string]Check {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tests := map[string]func() error{
		"core": func() error { _, err := s.Client().Version(ctx); return err },
	}
	if dns {
		tests["dns"] = func() error {
			r := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:19764")
			}}
			addresses, err := r.LookupNetIP(ctx, "ip4", "probe.hyper.invalid.")
			if err != nil {
				return err
			}
			for _, a := range addresses {
				if a.Unmap() == netip.MustParseAddr("192.0.2.1") {
					return nil
				}
			}
			return errors.New("unexpected DNS canary response")
		}
	}
	proxy, _ := url.Parse("http://127.0.0.1:19763")
	tr := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Sites on both sides of the rules: mainland ones should go direct, the rest through the proxy.
	for name, endpoint := range map[string]string{
		"baidu": "https://www.baidu.com/", "netease": "https://music.163.com/",
		"google": "https://www.google.com/generate_204", "youtube": "https://www.youtube.com/generate_204", "github": "https://github.com/",
	} {
		tests[name] = func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
			if err != nil {
				return err
			}
			res, err := client.Do(req)
			if err != nil {
				return err
			}
			res.Body.Close()
			if res.StatusCode >= 400 {
				return errors.New("route check returned an error")
			}
			return nil
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	result := map[string]Check{}
	for k, f := range tests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := f()
			mu.Lock()
			result[k] = Check{err == nil, time.Since(start).Milliseconds()}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}
