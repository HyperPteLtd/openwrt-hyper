package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/HyperPteLtd/openwrt-hyper/internal/control"
	"github.com/HyperPteLtd/openwrt-hyper/internal/kernel"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
)

// ErrNeedsRestart asks the manager to fall back to a full stop and start.
var ErrNeedsRestart = errors.New("change needs a full restart")

// applied records what the running takeover was built from, so a hot reload can
// prove the firewall and listeners stay valid for the new configuration.
type applied struct {
	network, guard, telegram string
	tag                      string
	routing                  string   // what decides each connection's route; see routingKey
	config                   [32]byte // digest of the configuration the core is running
}

// routingKey covers the choices that decide where a connection goes: the mode, the
// subscription and the rules it routes by (profileRouting), and the local rules, groups and
// device routes. A subscription refresh with the same choices keeps it, so open connections
// survive periodic updates of its nodes, plan and traffic. Group choices are applied by
// Select, which closes only the connections through that group.
func routingKey(i Intent, p subscription.Profile) string {
	b, _ := json.Marshal(struct {
		Mode, Source, Routing string
		DefaultDirect         bool
		Groups                []LocalGroup
		Devices               []DeviceRule
		Rules                 []CustomRule
		Telegram              *Telegram
	}{i.Mode, i.Source, profileRouting(p), i.Policy.DefaultDirect, i.Policy.Groups, i.Policy.Devices, i.Policy.Rules, i.Policy.Telegram})
	return string(b)
}

// profileRouting names the rules a profile routes by: the router's own default or the
// subscription's own. Falling back to local routing changes it; a refresh does not, since the
// subscription updates its own rules along with its nodes.
func profileRouting(p subscription.Profile) string {
	if p.Local {
		return "local"
	}
	return "subscription"
}

// networkKey covers what the network helper applied: a change needs a new takeover.
func networkKey(i Intent, n Network) string {
	direct, _ := directDevices(i) // an invalid device rule already fails in takeover
	return fmt.Sprint(n.Method, n.DNS, n.FakeIP, n.Family6, takenLAN(n), n.FailOpen, chinaShortcut(i, n), direct, n.SideRouter, n.RouterSelf)
}

// takenLAN keeps the LAN prefixes of the families the helper takes over. Without IPv6 takeover,
// a delegated prefix that rotates on every PPPoE redial must not restart the takeover.
func takenLAN(n Network) map[string][]netip.Prefix {
	if n.Family6 {
		return n.LAN
	}
	lan := make(map[string][]netip.Prefix, len(n.LAN))
	for dev, ps := range n.LAN {
		lan[dev] = slices.DeleteFunc(slices.Clone(ps), func(p netip.Prefix) bool { return !p.Addr().Is4() })
	}
	return lan
}

func takeover(i Intent, n Network) (guard, telegram string, err error) {
	if guard, err = deviceGuardPolicy(i); err != nil {
		return
	}
	telegram, _, err = telegramAllowance(i.Policy.Telegram, n)
	return
}

// remember takes the profile Start compiled config from, so a refresh that lands while the
// core starts still changes the key and closes connections on the next reload.
func (s *System) remember(i Intent, n Network, p subscription.Profile, tag string, config []byte) {
	guard, telegram, err := takeover(i, n)
	routing := routingKey(i, p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.applied = nil
		return
	}
	s.applied = &applied{networkKey(i, n), guard, telegram, tag, routing, sha256.Sum256(config)}
}

// Reload swaps the core configuration in place when the listeners, firewall
// and kernel stay the same: routing mode, source, groups, rules and choices.
// Anything else returns ErrNeedsRestart.
func (s *System) Reload(ctx context.Context, i Intent) error {
	s.mu.Lock()
	last, client, running := s.applied, s.client, s.process != nil
	s.mu.Unlock()
	if last == nil || !running {
		return ErrNeedsRestart
	}
	n, e := networkOptions(ctx)
	s.observe(ctx, n, e)
	if e != nil {
		return e
	}
	guard, telegram, e := takeover(i, n)
	if e != nil {
		return e
	}
	if networkKey(i, n) != last.network || guard != last.guard || telegram != last.telegram {
		return ErrNeedsRestart
	}
	var st kernel.Status
	var caps kernel.Capabilities
	if e = jsonCmd(ctx, &st, kernelCLI, "status"); e != nil {
		return e
	}
	if e = jsonCmd(ctx, &caps, kernelCLI, "capabilities"); e != nil {
		return e
	}
	cap, ok := caps.Targets[st.Target]
	if !ok || st.Pending || st.Selected != last.tag || caps.Tag != last.tag || caps.Repository != kernel.Repository {
		return ErrNeedsRestart
	}
	profile, e := LoadProfile(i, subscription.OpenStore(Root+"/sources"))
	if e != nil {
		return errors.New("active subscription unavailable")
	}
	if e = prepareMihomo(profile); e != nil {
		return errors.New("cannot prepare authorized rule sets")
	}
	if _, e = syncDevices(ctx, policyMACs(i), n); e != nil {
		return e
	}
	config, e := Compile(i, profile, cap, n, client.Secret)
	if e != nil {
		return e
	}
	if e = prepareGeo(ctx, config, profile); e != nil {
		return e
	}
	// Interface notices (address lifetimes, RA refreshes) usually change nothing; reloading
	// the core anyway would pause new connections and reset its DNS clients.
	digest, routing := sha256.Sum256(config), routingKey(i, profile)
	if digest == last.config {
		// The core runs this configuration already, so the choices behind it are the routes in
		// effect: keep the key current, or a later change of nodes alone would close everything.
		s.mu.Lock()
		if s.applied == last {
			s.applied.routing = routing
		}
		s.mu.Unlock()
		return nil
	}
	if e = client.ReloadPayload(ctx, config); e != nil {
		return fmt.Errorf("core rejected the new configuration: %w", e)
	}
	if e = s.verifyChoices(ctx, config); e != nil {
		return e
	}
	// Mihomo keeps open connections on the route they started with. After a change of mode,
	// subscription, its routing rules or local rules, close them all as Clash clients do, so
	// browsers reconnect on the new route: a page opened direct in smart mode would otherwise
	// keep showing the local address after switching to global.
	if routing != last.routing && client.CloseConnections(ctx) != nil {
		s.records.Log("core", "warn", "connections_not_reset")
	}
	s.records.CoreStarted(i.Source)
	s.restartMonitor(i, n, client)
	s.mu.Lock()
	if s.applied != nil {
		s.applied.config = digest
		s.applied.routing = routing
	}
	s.mu.Unlock()
	return nil
}

// restartMonitor replaces the device watcher and connection recorder so they
// follow the reloaded policy.
func (s *System) restartMonitor(i Intent, n Network, client control.Client) {
	s.mu.Lock()
	if s.cancelMonitor != nil {
		s.cancelMonitor()
	}
	s.mu.Unlock()
	s.startMonitor(i, n, client.Secret)
}
