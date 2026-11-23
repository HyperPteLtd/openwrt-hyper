// The only command-line argument is the RPC method. URL, bearer, passwords and
// profile data are read from stdin and are never echoed.
package subscriptioncli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	sub "github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"github.com/HyperPteLtd/openwrt-hyper/internal/tlsdiag"
)

const storeDir = "/etc/hyper/sources"

type input struct {
	ClockRetryGeneration uint64            `json:"clock_retry_generation,omitempty"`
	Scheduled            bool              `json:"scheduled,omitempty"`
	ReplacementURL       string            `json:"replacement_url,omitempty"`
	Rules                *string           `json:"rules,omitempty"`
	RuleSet              string            `json:"rule_set,omitempty"`
	Query                string            `json:"query,omitempty"`
	Source               string            `json:"source"`
	Config               *sub.Source       `json:"config,omitempty"`
	Preferences          *sub.Preferences  `json:"preferences,omitempty"`
	Content              string            `json:"content,omitempty"`
	Confirm              string            `json:"confirm,omitempty"`
	ActiveIDs            []string          `json:"active_ids,omitempty"`
	Choices              map[string]string `json:"choices,omitempty"`
	Origin               string            `json:"origin,omitempty"`
	Email                string            `json:"email,omitempty"`
	Password             string            `json:"password,omitempty"`
	Offset               int               `json:"offset"`
	Limit                int               `json:"limit"`
}
type result struct {
	TLS            *tlsdiag.Diagnostic     `json:"tls,omitempty"`
	OK             bool                    `json:"ok"`
	Data           any                     `json:"data,omitempty"`
	Error          string                  `json:"error,omitempty"`
	Provider       *sub.ProviderDiagnostic `json:"provider,omitempty"`
	ManagerPending bool                    `json:"manager_pending,omitempty"`
}

func notify(id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/sbin/hyperd", "notify-source", id).Run() == nil
}
func paginate[T any](v []T, o, n int) []T {
	if o >= len(v) {
		return []T{}
	}
	if n > len(v)-o {
		n = len(v) - o
	}
	return v[o : o+n]
}

// Probe only if the profile contains a capability-dependent proxy. A changed
// core selection is reflected on the next read without mutating private snapshots.
func nodeAvailability(ctx context.Context, nodes []sub.Member) []sub.Notice {
	var checked bool
	var caps sub.CoreFeatures
	var notices []sub.Notice
	for i := range nodes {
		n := &nodes[i]
		if n.Capability == "" {
			continue
		}
		if !checked {
			caps = sub.CurrentCoreFeatures(ctx)
			checked = true
		}
		n.Availability = caps.Availability(n.Capability)
		var code string
		switch n.Availability {
		case "unsupported":
			code = "mihomo_protocol_unsupported"
		case "unverified":
			code = "mihomo_capability_unknown"
		}
		if code != "" {
			notices = append(notices, sub.Notice{Code: code, At: n.Name, Resource: n.Capability})
		}
	}
	return notices
}
func run(ctx context.Context, store *sub.Store, method string, in input, f sub.Fetcher) (any, []string, error) {
	switch method {
	case "list":
		v, e := store.List()
		return v, nil, e
	case "status":
		v, e := store.Status(in.Source)
		return v, nil, e
	case "url":
		v, e := store.SourceURL(in.Source)
		return map[string]string{"url": v}, nil, e
	case "set_match":
		return setMatch(store, in)
	case "profile", "nodes", "groups", "rules":
		p, e := store.Load(in.Source)
		if e != nil {
			return nil, nil, e
		}
		v := p.Summary()
		v.Notices = append(v.Notices, nodeAvailability(ctx, v.Nodes)...)
		if in.Offset < 0 || in.Limit < 0 || in.Limit > 256 {
			return nil, nil, errors.New("invalid_page")
		}
		if in.Limit == 0 {
			in.Limit = 50
		}
		switch method {
		case "nodes":
			return map[string]any{"items": paginate(v.Nodes, in.Offset, in.Limit), "total": len(v.Nodes)}, nil, nil
		case "groups":
			return map[string]any{"items": paginate(v.Groups, in.Offset, in.Limit), "total": len(v.Groups)}, nil, nil
		case "rules":
			counts := map[string]int{}
			selected := make([]sub.Rule, 0)
			positions := make([]int, 0)
			for i, rule := range v.Rules {
				matched := in.RuleSet == ""
				for _, tag := range rule.SetTags() {
					counts[tag]++
					if tag == in.RuleSet {
						matched = true
					}
				}
				if matched {
					selected = append(selected, rule)
					positions = append(positions, i+1)
				}
			}
			entries := map[string]int{}
			binary := map[string]bool{}
			for _, def := range p.BinarySets {
				entries[def.Tag] = def.Entries
				binary[def.Tag] = true
				if _, ok := counts[def.Tag]; !ok {
					counts[def.Tag] = 0
				}
			}
			return map[string]any{"items": paginate(selected, in.Offset, in.Limit), "positions": paginate(positions, in.Offset, in.Limit), "total": len(selected), "sets": counts, "entries": entries, "binary": binary, "final": v.Final, "local": v.Local}, nil, nil
		default:
			v.Nodes = paginate(v.Nodes, in.Offset, in.Limit)
			v.Groups = paginate(v.Groups, in.Offset, in.Limit)
			v.Rules = paginate(v.Rules, in.Offset, in.Limit)
			return v, nil, nil
		}
	case "agents":
		return sub.UserAgents, nil, nil
	case "site":
		v, e := f.Site(ctx, in.Origin)
		return v, nil, e
	case "login":
		intent, e := store.BeginLogin(in.Origin)
		if e != nil {
			return nil, nil, e
		}
		a, e := f.Login(ctx, in.Origin, in.Email, in.Password)
		if e != nil {
			return nil, nil, e
		}
		changed, e := store.SaveAccount(a, intent)
		if e != nil {
			return nil, changed, e
		}
		var ids []string
		for _, plan := range a.Plans {
			ids = append(ids, a.Source(plan).ID)
		}
		return map[string]any{"site": a.Site, "plans": a.Plans, "sources": ids}, changed, nil
	case "configure":
		if in.Config == nil {
			return nil, nil, errors.New("source_config_required")
		}
		e := store.Configure(*in.Config)
		return nil, []string{in.Config.ID}, e
	case "preferences":
		if in.Preferences == nil {
			return nil, nil, errors.New("source_preferences_required")
		}
		e := store.Preferences(in.Source, *in.Preferences)
		return nil, []string{in.Source}, e
	case "refresh":
		f.ReplacementURL, f.Routing = in.ReplacementURL, in.Rules
		c, e := f.Refresh(ctx, store, in.Source, []byte(in.Content), sub.Policy{ClockRetryGeneration: in.ClockRetryGeneration, Scheduled: in.Scheduled, ActiveIDs: in.ActiveIDs, Choices: in.Choices, Confirm: in.Confirm})
		if e != nil {
			var withdrawn *sub.AccountWithdrawal
			if errors.As(e, &withdrawn) {
				return nil, withdrawn.Sources, e
			}
			if errors.Is(e, sub.ErrRevoked) || errors.Is(e, sub.ErrSuspended) {
				return nil, []string{in.Source}, e
			}
			return nil, nil, e
		}
		c.Notices = append(c.Notices, nodeAvailability(ctx, c.Profile.Summary().Nodes)...)
		changes := []string{}
		if c.Decision == "ready" && !c.Unchanged() {
			changes = append(changes, in.Source)
		}
		data := map[string]any{"changes": c.Changes, "decision": c.Decision, "retains_routing": c.RetainsRouting, "confirmation_id": c.ConfirmationID, "notices": nonNil(c.Notices), "node_count": len(c.Profile.Nodes), "group_count": len(c.Profile.Groups), "rule_count": len(c.Profile.Rules), "local": c.Profile.Local}
		// The daemon logs an update the router did not apply apart from a done refresh.
		if pending := c.Pending(); pending != nil {
			data["pending_update"] = pending
		}
		return data, changes, nil
	case "rollback":
		e := store.RollbackChecked(ctx, in.Source, f.BeforeCommit)
		return nil, []string{in.Source}, e
	case "revoke":
		e := store.Revoke(in.Source)
		return nil, []string{in.Source}, e
	case "logout":
		ids, e := store.Logout(in.Source)
		return nil, ids, e
	default:
		return nil, nil, errors.New("unknown_method")
	}
}
func code(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, os.ErrNotExist) {
		return "not_found"
	}
	if errors.Is(e, sub.ErrRevoked) {
		return "revoked"
	}
	if errors.Is(e, sub.ErrSuperseded) {
		return "superseded"
	}
	if errors.Is(e, sub.ErrLoginRequired) {
		return "login_required"
	}
	if errors.Is(e, sub.ErrSuspended) {
		return "suspended"
	}
	if errors.Is(e, sub.ErrNotReady) {
		return "not_ready"
	}
	msg := strings.ReplaceAll(e.Error(), " ", "_")
	if len(msg) > 64 {
		return "operation_failed"
	}
	for _, r := range msg {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return "operation_failed"
		}
	}
	return msg
}
func Main() {
	state := flag.String("state", storeDir, "private subscription store")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "expected a subscription method")
		os.Exit(2)
	}
	b, e := io.ReadAll(io.LimitReader(os.Stdin, sub.MaxBody*2+1))
	in := input{}
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e != nil || len(b) > sub.MaxBody*2 || d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		json.NewEncoder(os.Stdout).Encode(result{Error: "invalid_request"})
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := sub.OpenStore(*state)
	f := sub.Fetcher{Fallback: true}
	if *state == storeDir {
		f.BeforeCommit = validate
	}
	data, changed, e := run(ctx, store, flag.Arg(0), in, f)
	pending := false
	if *state == storeDir && (e == nil || errors.Is(e, sub.ErrRevoked) || errors.Is(e, sub.ErrSuspended) || flag.Arg(0) == "logout" || flag.Arg(0) == "login") {
		for _, id := range changed {
			if st, statErr := store.Status(id); statErr == nil && st.Revoked {
				deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := exec.CommandContext(deadline, "/usr/sbin/hyper-kernel", "invalidate", "--source", id).Run()
				cancel()
				if err != nil {
					pending = true
					if e == nil {
						e = errors.New("kernel_invalidation_pending")
					}
				}
			}
			if !notify(id) {
				pending = true
			}
		}
	}
	if *state == storeDir && len(changed) > 0 {
		if gcErr := store.CollectSets("/etc/hyper/rulesets"); gcErr != nil && e == nil {
			e = gcErr
		}
	}
	if e != nil {
		data = nil
	}
	json.NewEncoder(os.Stdout).Encode(result{TLS: tlsdiag.From(e), OK: e == nil, Data: data, Error: code(e), Provider: sub.FailureProvider(e), ManagerPending: pending})
	if e != nil {
		os.Exit(1)
	}
}

func validate(ctx context.Context, p sub.Profile) (func() error, error) {
	cfg, e := json.Marshal(p)
	if e != nil {
		return nil, errors.New("invalid_candidate")
	}
	cmd := exec.CommandContext(ctx, "/usr/sbin/hyperd", "validate-source", p.Source)
	cmd.Stdin = bytes.NewReader(cfg)
	out, e := cmd.Output()
	if e != nil {
		return nil, errors.New("candidate_validation_failed")
	}
	revision := strings.TrimSpace(string(out))
	if revision == "" || len(revision) > 256 || strings.ContainsAny(revision, " \t\r\n\x00") {
		return nil, errors.New("preflight_revision_missing")
	}
	return func() error {
		timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if e := exec.CommandContext(timeout, "/usr/sbin/hyperd", "validate-source", p.Source, revision).Run(); e != nil {
			if timeout.Err() != nil {
				return errors.New("preflight_confirmation_timeout")
			}
			return errors.New("preflight_revision_changed")
		}
		return nil
	}, nil
}

// nonNil keeps an empty list as [] in JSON; the LuCI page reads it as an array.
func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func setMatch(store *sub.Store, in input) (any, []string, error) {
	p, e := store.Load(in.Source)
	if e != nil {
		return nil, nil, e
	}
	answer := func(ok bool, e error) (any, []string, error) {
		if e != nil {
			return nil, nil, e
		}
		return map[string]bool{"matched": ok}, nil, nil
	}
	for _, set := range p.BinarySets {
		if set.Tag == in.RuleSet {
			return answer(sub.SetMatch(set, in.Query))
		}
	}
	// GEOSITE,CN and GEOIP,CN rules, and a local profile's mainland routes, match by the router's
	// mainland lists; the rules page asks for them by these tags.
	mainland := map[string]struct{ list, behavior string }{sub.DomesticDomainSet: {cnlist.Domains, "domain"}, sub.DomesticIPSet: {cnlist.Addresses, "ipcidr"}}
	if m, ok := mainland[in.RuleSet]; ok {
		data, e := os.ReadFile(cnlist.Path(m.list))
		if e != nil {
			return nil, nil, e
		}
		return answer(sub.SetMatch(sub.BinarySet{Tag: in.RuleSet, Format: "mrs", Behavior: m.behavior, Data: data}, in.Query))
	}
	return answer(sub.InlineSetMatch(p.Rules, in.RuleSet, in.Query))
}
