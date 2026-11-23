package router

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/HyperPteLtd/openwrt-hyper/internal/atomicfile"
	"github.com/HyperPteLtd/openwrt-hyper/internal/cnlist"
	"github.com/HyperPteLtd/openwrt-hyper/internal/subscription"
	"go.yaml.in/yaml/v3"
)

const coreHome = RunDir + "/mihomo"
const packagedRules = "/usr/share/hyper/rules"

// GeodataDir keeps the geodata files the core's GEOIP and GEOSITE rules need, downloaded on
// demand (subscription.PrepareGeodata).
const GeodataDir = Root + "/geodata"

// The core runs as root (coreCommand), so its home and device lists, like the rule sets
// (subscription.MaterializeSets), are private to root: 0700 directories and 0600 files.
func privateDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() || int(st.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return errors.New("unsafe core work directory")
	}
	return os.Chmod(path, 0700)
}

// reclaimDir removes a runtime directory that holds anything another account owns: an older
// Hyper left its home and work directory in /var/run under its own core account, which
// privateDir and the work directory check refuse until a reboot. Only a full start calls it,
// before the core runs; the state there is rebuilt (the fake-ip cache starts empty).
func reclaimDir(path string) error {
	foreign := false
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
			foreign = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if foreign {
		return os.RemoveAll(path)
	}
	return nil
}

func prepareMihomo(p subscription.Profile) error {
	if err := privateDir(coreHome); err != nil {
		return err
	}
	if err := privateDir(coreHome + "/devices"); err != nil {
		return err
	}
	for name, target := range map[string]string{
		"rulesets": Root + "/rulesets", "builtin": packagedRules, "cnlists": cnlist.Dir,
		"GeoSite.dat": GeodataDir + "/GeoSite.dat", "GeoIP.dat": GeodataDir + "/GeoIP.dat", "country.mmdb": GeodataDir + "/country.mmdb", "ASN.mmdb": GeodataDir + "/ASN.mmdb",
	} {
		path := filepath.Join(coreHome, name)
		if err := os.Symlink(target, path); err != nil {
			if actual, e := os.Readlink(path); e != nil || actual != target {
				return errors.New("unexpected path in core home")
			}
		}
	}
	// The core keeps its Bolt database here; create it private rather than with the core's umask.
	f, err := os.OpenFile(coreHome+"/cache.db", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return subscription.MaterializeSets(p, Root+"/rulesets")
}

func prepareGeo(ctx context.Context, config []byte, p subscription.Profile) error {
	var cfg struct {
		Providers map[string]obj `yaml:"rule-providers"`
		Rules     []string       `yaml:"rules"`
	}
	if err := yaml.Unmarshal(config, &cfg); err != nil {
		return err
	}
	active := p
	active.BinarySets = nil
	for _, set := range p.BinarySets {
		if _, ok := cfg.Providers[profileSet(set.Tag)]; ok {
			active.BinarySets = append(active.BinarySets, set)
		}
	}
	cfg.Rules = append(cfg.Rules, subscription.GeoRules(active)...)
	return subscription.PrepareGeodata(ctx, cfg.Rules, GeodataDir, packagedRules+"/GEODATA")
}

func macProviderName(mac string) string { return "hyper-device-" + strings.ReplaceAll(mac, ":", "") }
func policyMACs(i Intent) []string {
	var macs []string
	for _, dev := range i.Policy.Devices {
		if i.Mode == "global" && dev.Target != subscription.Direct {
			continue
		}
		match, _ := deviceMatch(dev.Address)
		if mac := stringOption(match, "source_mac_address"); mac != "" && !slices.Contains(macs, mac) {
			macs = append(macs, mac)
		}
	}
	return macs
}

// A candidate preflight only needs readable provider files. The running core's
// watcher owns updates so a concurrent check cannot consume a DHCP change.
func ensureDeviceFiles(dir string, macs []string) error {
	for _, mac := range macs {
		file := filepath.Join(dir, macProviderName(mac)+".txt")
		if _, err := os.Stat(file); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		// Link a fully initialized empty file (0600, as CreateTemp makes it) without
		// replacing a concurrent update.
		tmp, err := os.CreateTemp(dir, ".empty-")
		if err != nil {
			return err
		}
		err = tmp.Close()
		if err == nil {
			err = os.Link(tmp.Name(), file)
			if errors.Is(err, os.ErrExist) {
				err = nil
			}
		}
		os.Remove(tmp.Name())
		if err != nil {
			return err
		}
	}
	return nil
}

func syncDevices(ctx context.Context, macs []string, n Network) ([]string, error) {
	if len(macs) == 0 {
		return nil, nil
	}
	inventory, err := devicesOnNetwork(ctx, n)
	if err != nil {
		return nil, err
	}
	byMAC := map[string][]string{}
	for _, d := range inventory {
		byMAC[d.MAC] = d.Addresses
	}
	var changed []string
	for _, mac := range macs {
		var data strings.Builder
		for _, raw := range byMAC[mac] {
			if ip, e := netip.ParseAddr(raw); e == nil {
				data.WriteString(netip.PrefixFrom(ip, ip.BitLen()).String() + "\n")
			}
		}
		name := macProviderName(mac)
		file := filepath.Join(coreHome, "devices", name+".txt")
		var content = []byte(data.String())
		if old, e := os.ReadFile(file); e == nil && bytes.Equal(old, content) {
			continue
		}
		if err := writeDeviceProvider(file, content); err != nil {
			return nil, err
		}
		changed = append(changed, name)
	}
	return changed, nil
}

// writeDeviceProvider replaces the list in one rename, 0600.
func writeDeviceProvider(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0600, ".device-")
}

func (s *System) watchDevices(ctx context.Context, macs []string, n Network) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	pending := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		changed, err := syncDevices(ctx, macs, n)
		if err != nil {
			continue
		}
		for _, name := range changed {
			pending[name] = true
		}
		for name := range pending {
			if err := s.Client().RefreshRules(ctx, name); err == nil {
				delete(pending, name)
			}
		}
	}
}
