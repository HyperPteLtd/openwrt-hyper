// Package proxy builds the mihomo core proxies; every input format shares it.
package proxy

import (
	"strings"

	"github.com/HyperPteLtd/openwrt-hyper/internal/links/model"
)

// Base starts a proxy with the keys every proxy begins with, in Clash's order. Merge fills the
// type in its place; empty values are dropped then.
func Base(name, server string, port int) model.Object {
	return model.Object{
		{Key: "name", Value: name},
		{Key: "type", Value: ""},
		{Key: "server", Value: server},
		{Key: "port", Value: port},
	}
}

// Merge sets the fields of extra on base, keeping base's order for keys it has and dropping
// base's empty values.
func Merge(base, extra model.Object) model.Object {
	out := make(model.Object, 0, len(base)+len(extra))
	for _, field := range base {
		if value, set := extra.Get(field.Key); set {
			out = append(out, model.Field{Key: field.Key, Value: value})
		} else if !model.Empty(field.Value) {
			out = append(out, field)
		}
	}
	for _, field := range extra {
		if _, set := out.Get(field.Key); !set {
			out = append(out, field)
		}
	}
	return out
}

// PluginOptions parses SIP003 plugin options, "key=value;flag;key=value".
func PluginOptions(text string) map[string]string {
	options := map[string]string{}
	for part := range strings.SplitSeq(text, ";") {
		key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		if key != "" {
			options[key] = value
		}
	}
	return options
}

// ShadowsocksPlugin returns the plugin fields of a Shadowsocks proxy for a SIP003 plugin and its
// options, or a reason when mihomo has no counterpart. obfs-local and simple-obfs become obfs;
// v2ray-plugin works in websocket mode only.
func ShadowsocksPlugin(plugin string, options map[string]string) (model.Object, string) {
	switch plugin {
	case "":
		return nil, ""
	case "obfs-local", "simple-obfs", "obfs":
		return model.New("plugin", "obfs", "plugin-opts", model.New(
			"mode", options["obfs"],
			"host", options["obfs-host"],
		)), ""
	case "v2ray-plugin":
		mode := options["mode"]
		if mode == "" {
			mode = "websocket"
		}
		if mode != "websocket" {
			return nil, "v2ray-plugin mode " + mode
		}
		_, tls := options["tls"]
		mux, muxSet := options["mux"]
		return model.New("plugin", "v2ray-plugin", "plugin-opts", model.New(
			"mode", "websocket",
			"tls", tls,
			"host", options["host"],
			"path", options["path"],
			"mux", muxSet && mux != "0",
		)), ""
	default:
		return nil, "plugin " + plugin
	}
}

// PortRanges turns "1000:2000" or "1000-2000" lists into mihomo's "1000-2000,3000".
func PortRanges(ranges []string) string {
	out := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, strings.ReplaceAll(r, ":", "-"))
		}
	}
	return strings.Join(out, ",")
}

// VLESSEncryption returns a VLESS encryption as mihomo takes it: "" for none, which other
// clients also write "none", or "auto" as VMess's ciphers are written (VLESS has no such mode,
// and mihomo refuses it). Anything else stays, for the normalization to check.
func VLESSEncryption(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "auto":
		return ""
	}
	return value
}
