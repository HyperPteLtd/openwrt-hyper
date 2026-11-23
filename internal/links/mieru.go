package links

import (
	"encoding/base64"
	"encoding/binary"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HyperPteLtd/openwrt-hyper/internal/links/body"
)

// mieruProfiles expands Mieru's standard link, mieru://base64(ClientConfig protobuf), into one
// mierus link per profile. A dialer, several servers or an unknown field refuses the whole
// link rather than dropping part of a profile.
func mieruProfiles(line string) ([]string, string) {
	_, tail, _ := strings.Cut(line, "://")
	raw, err := body.Base64(tail)
	if err != nil || len(raw) == 0 {
		return nil, "not a Mieru client config"
	}
	config, ok := wire(raw, 10)
	if !ok || len(config[1]) == 0 {
		return nil, "not a Mieru client config"
	}
	var links []string
	for _, item := range config[1] {
		profile, ok := wire(item.bytes, 7)
		if !ok || !item.text {
			return nil, "malformed Mieru profile"
		}
		name, ok1 := profile.text(1)
		user, ok2 := profile.message(2, 2)
		if !ok1 || !ok2 || len(profile[3]) != 1 {
			return nil, "Mieru profile without one server"
		}
		username, ok1 := user.text(1)
		password, ok2 := user.text(2)
		server, ok3 := profile.message(3, 3)
		if !ok1 || !ok2 || !ok3 {
			return nil, "malformed Mieru profile"
		}
		host, _ := server.text(2)
		if host == "" {
			host, _ = server.text(1)
		}
		if host == "" || len(server[3]) != 1 {
			return nil, "Mieru server without one port binding"
		}
		binding, ok := wire(server[3][0].bytes, 3)
		port, hasPort := binding.number(1)
		ports, hasRange := binding.text(3)
		protocol, hasProtocol := binding.number(2)
		if !ok || hasPort == hasRange || !hasProtocol || protocol < 1 || protocol > 2 {
			return nil, "malformed Mieru port binding"
		}
		if hasPort {
			ports = strconv.FormatUint(port, 10)
		}
		q := url.Values{"profile": {name}, "port": {ports}, "protocol": {[]string{"", "UDP", "TCP"}[protocol]}}
		if _, present := profile[5]; present {
			m, ok := profile.message(5, 1)
			v, ok2 := m.number(1)
			modes := strings.Fields("MULTIPLEXING_DEFAULT MULTIPLEXING_OFF MULTIPLEXING_LOW MULTIPLEXING_MIDDLE MULTIPLEXING_HIGH")
			if !ok || !ok2 || v >= uint64(len(modes)) {
				return nil, "malformed Mieru multiplexing"
			}
			q.Set("multiplexing", modes[v])
		}
		if _, present := profile[6]; present {
			v, ok := profile.number(6)
			modes := strings.Fields("HANDSHAKE_DEFAULT HANDSHAKE_STANDARD HANDSHAKE_NO_WAIT")
			if !ok || v >= uint64(len(modes)) {
				return nil, "malformed Mieru handshake mode"
			}
			q.Set("handshake-mode", modes[v])
		}
		if data := profile[7]; len(data) > 0 {
			if len(data) != 1 || !data[0].text {
				return nil, "malformed Mieru traffic pattern"
			}
			q.Set("traffic-pattern", base64.StdEncoding.EncodeToString(data[0].bytes))
		}
		u := url.URL{Scheme: "mierus", User: url.UserPassword(username, password), Host: hostPort(host), RawQuery: q.Encode()}
		links = append(links, u.String())
	}
	return links, ""
}

func hostPort(host string) string {
	if strings.Contains(host, ":") {
		return "[" + strings.Trim(host, "[]") + "]"
	}
	return host
}

type wireValue struct {
	bytes []byte
	num   uint64
	text  bool
}
type wireFields map[int][]wireValue

// wire reads a protobuf message of varint and length-delimited fields numbered 1 to maxField.
func wire(b []byte, maxField int) (wireFields, bool) {
	fields := wireFields{}
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 || tag>>3 < 1 || tag>>3 > uint64(maxField) {
			return nil, false
		}
		b = b[n:]
		var v wireValue
		switch tag & 7 {
		case 0:
			v.num, n = binary.Uvarint(b)
		case 2:
			var size uint64
			size, n = binary.Uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) {
				return nil, false
			}
			v.bytes, v.text = b[n:n+int(size)], true
			n += int(size)
		default:
			return nil, false
		}
		if n <= 0 {
			return nil, false
		}
		fields[int(tag>>3)] = append(fields[int(tag>>3)], v)
		b = b[n:]
	}
	return fields, true
}

func (f wireFields) text(k int) (string, bool) {
	a := f[k]
	if len(a) != 1 || !a[0].text || !utf8.Valid(a[0].bytes) {
		return "", false
	}
	return string(a[0].bytes), true
}

func (f wireFields) message(k, maxField int) (wireFields, bool) {
	a := f[k]
	if len(a) != 1 || !a[0].text {
		return nil, false
	}
	return wire(a[0].bytes, maxField)
}

func (f wireFields) number(k int) (uint64, bool) {
	a := f[k]
	if len(a) != 1 || a[0].text {
		return 0, false
	}
	return a[0].num, true
}
