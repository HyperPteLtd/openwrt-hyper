package control

import (
	"context"
	"errors"
	"net/url"
	"time"
)

var ErrProbeFailed = errors.New("node probe failed")

type Latency struct {
	Measured int64  `json:"measured_at"`
	Delay    uint64 `json:"delay_ms"`
}

func (c Client) Delays(ctx context.Context) (map[string]Latency, error) {
	ps, e := c.proxies(ctx)
	if e != nil {
		return nil, e
	}
	out := make(map[string]Latency, len(ps))
	for name, p := range ps {
		var sample Latency
		if n := len(p.History); n > 0 {
			last := p.History[n-1]
			if !last.Time.IsZero() && last.Delay > 0 {
				sample = Latency{last.Time.Unix(), last.Delay}
			}
		}
		out[name] = sample
	}
	return out, nil
}
func (c Client) delay(ctx context.Context, name string, out any) error {
	part, e := segment(name)
	if e != nil {
		return e
	}
	q := url.Values{"url": {"https://www.gstatic.com/generate_204"}, "timeout": {"5000"}}
	e = c.call(ctx, "GET", "/proxies/"+part+"/delay?"+q.Encode(), nil, out)
	if errors.Is(e, statusError(504)) || errors.Is(e, statusError(503)) {
		return ErrProbeFailed
	}
	return e
}
func (c Client) Measure(ctx context.Context, name string) (Latency, error) {
	var r struct{ Delay uint64 }
	if e := c.delay(ctx, name, &r); e != nil {
		return Latency{}, e
	}
	if r.Delay == 0 {
		return Latency{}, ErrProbeFailed
	}
	return Latency{time.Now().Unix(), r.Delay}, nil
}
