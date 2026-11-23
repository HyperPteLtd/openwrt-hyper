package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Traffic struct {
	Available   bool   `json:"available"`
	Down        uint64 `json:"down_bytes_per_second"`
	Up          uint64 `json:"up_bytes_per_second"`
	DownTotal   uint64 `json:"down_bytes"`
	UpTotal     uint64 `json:"up_bytes"`
	Connections uint64 `json:"connections"`
	MemoryBytes uint64 `json:"memory_bytes"`
}
type Connection struct {
	ID          string   `json:"id"`
	Source      string   `json:"device,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Network     string   `json:"network,omitempty"`
	Rule        string   `json:"rule,omitempty"`
	Outbound    string   `json:"outbound,omitempty"`
	Chain       []string `json:"chain,omitempty"`
	Created     int64    `json:"created_at,omitempty"`
	Closed      int64    `json:"closed_at,omitempty"`
	Result      string   `json:"result"`
}
type tracked struct {
	ID, Rule, RulePayload string
	Upload, Download      uint64
	Start                 time.Time
	Chains                []string
	Metadata              struct{ Network, Host, SourceIP, DestinationIP, DestinationPort string }
}

func join(host, port string) string {
	if host == "" || port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
func (t tracked) connection() Connection {
	m := t.Metadata
	dest := m.Host
	if dest == "" {
		dest = m.DestinationIP
	}
	// The device is its address; the source port only tells one connection from the next.
	r := Connection{ID: t.ID, Source: m.SourceIP, Destination: join(dest, m.DestinationPort), Network: m.Network, Rule: t.Rule, Result: "routed", Chain: t.Chains}
	if t.RulePayload != "" {
		r.Rule += "," + t.RulePayload
	}
	for i, n := range r.Chain {
		r.Chain[i] = fromCore(n)
	}
	if len(r.Chain) > 0 {
		r.Outbound = r.Chain[0]
	}
	if !t.Start.IsZero() {
		r.Created = t.Start.UnixMilli()
	}
	if t.Upload > 0 || t.Download > 0 {
		r.Result = "traffic_observed"
	}
	return r
}

// dialFailure matches the core's report of a connection it could not open:
// [TCP] dial <outbound> [(match <rule>/<payload>)] <source>[(<process>)] --> <destination> error: <cause>
var dialFailure = regexp.MustCompile(`\[(TCP|UDP)\] dial (\S+) (?:\(match ([^/]+)/(.*?)\) )?(\S+?)(?:\(.*?\))? --> (\S+) error: `)

// FailedDial reads a core log line that reports a failed dial as a closed connection record.
// The cause stays out: it can carry node addresses and free-form server text.
func FailedDial(line string, now int64) (Connection, bool) {
	m := dialFailure.FindStringSubmatch(line)
	if m == nil {
		return Connection{}, false
	}
	source := m[5]
	if host, _, e := net.SplitHostPort(source); e == nil {
		source = host
	}
	out := fromCore(m[2])
	c := Connection{Source: source, Destination: m[6], Network: strings.ToLower(m[1]), Outbound: out, Chain: []string{out}, Created: now, Closed: now, Result: "failed"}
	if m[3] != "" {
		c.Rule = m[3] + "," + m[4]
	}
	return c, true
}

// Connections records each connection the core opens, changes or closes, comparing one
// snapshot with the next. wait sets the pace: a connection opened and closed between two
// snapshots is never seen.
func (c Client) Connections(ctx context.Context, wait func(context.Context) error, record func(Connection)) error {
	previous := map[string]Connection{}
	for {
		var snapshot struct{ Connections []tracked }
		if err := c.call(ctx, "GET", "/connections", nil, &snapshot); err != nil {
			return err
		}
		active := make(map[string]Connection, len(snapshot.Connections))
		for _, t := range snapshot.Connections {
			if t.ID == "" {
				continue
			}
			item := t.connection()
			active[item.ID] = item
			if old, ok := previous[item.ID]; !ok || old.Result != item.Result || old.Outbound != item.Outbound || !slices.Equal(old.Chain, item.Chain) {
				record(item)
			}
		}
		for id, item := range previous {
			if _, ok := active[id]; !ok {
				item.Closed = time.Now().UnixMilli()
				record(item)
			}
		}
		previous = active
		if err := wait(ctx); err != nil {
			return err
		}
	}
}

func (c Client) Traffic(ctx context.Context) (Traffic, error) {
	var snapshot struct {
		Memory      uint64
		Connections []struct{}
	}
	if e := c.call(ctx, "GET", "/connections", nil, &snapshot); e != nil {
		return Traffic{}, e
	}
	r, e := c.request(ctx, "GET", "/traffic", nil)
	if e != nil {
		return Traffic{}, e
	}
	defer r.Close()
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if e = scanner.Err(); e == nil {
			e = errors.New("empty traffic sample")
		}
		return Traffic{}, e
	}
	var sample struct{ Up, Down, UpTotal, DownTotal uint64 }
	if e = json.Unmarshal(scanner.Bytes(), &sample); e != nil {
		return Traffic{}, e
	}
	return Traffic{Available: true, Up: sample.Up, Down: sample.Down, UpTotal: sample.UpTotal, DownTotal: sample.DownTotal, MemoryBytes: snapshot.Memory, Connections: uint64(len(snapshot.Connections))}, nil
}
